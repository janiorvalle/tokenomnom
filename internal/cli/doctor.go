package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

func newDoctorCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Show discovered coding-agent session data",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}

			roots, err := discover.Resolve(discover.ResolveOptions{
				CodexDir:  *codexDir,
				ClaudeDir: *claudeDir,
				Home:      home,
				Getenv:    os.Getenv,
			})
			if err != nil {
				return err
			}
			stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
			if err != nil {
				return err
			}

			return writeDoctorReport(cmd, roots, filepath.Join(stateDir, store.DatabaseName), *timezone)
		},
	}
}

func writeDoctorReport(cmd *cobra.Command, roots []discover.Root, databasePath, requestedZone string) error {
	if currentFormat(cmd) == "json" {
		return writeDoctorJSON(cmd, roots, databasePath, requestedZone)
	}
	found := make([]discover.Provider, 0, len(roots))
	for index, root := range roots {
		if index > 0 {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		files, walkErrors := discover.ListSourceFiles(root)
		writeProviderReport(cmd, root, files, walkErrors)
		if root.Exists {
			found = append(found, root.Provider)
		}
	}

	fmt.Fprintln(cmd.OutOrStdout())
	if err := writeStoreReport(cmd, databasePath); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout())
	switch len(found) {
	case 0:
		fmt.Fprintln(cmd.OutOrStdout(), "Status: no provider data directories found. Use --codex-dir, --claude-dir, or the TOKENOMNOM_*_DIR environment variables to point tokenomnom at them.")
	case 1:
		fmt.Fprintf(cmd.OutOrStdout(), "Status: only %s was found; discovery is ready to use.\n", providerName(found[0]))
	default:
		fmt.Fprintln(cmd.OutOrStdout(), "Status: both providers found; discovery is ready to use.")
	}
	return nil
}

type jsonDoctorProvider struct {
	Provider   string   `json:"provider"`
	Path       string   `json:"path"`
	Source     string   `json:"source"`
	Exists     bool     `json:"exists"`
	JSONLFiles int      `json:"jsonl_files"`
	TotalBytes int64    `json:"total_bytes"`
	Oldest     *string  `json:"oldest"`
	Newest     *string  `json:"newest"`
	WalkErrors []string `json:"walk_errors"`
}

type jsonDoctorStore struct {
	Path           string        `json:"path"`
	Exists         bool          `json:"exists"`
	SizeBytes      int64         `json:"size_bytes"`
	SchemaVersion  *int          `json:"schema_version"`
	Timezone       *string       `json:"timezone"`
	LastSync       *string       `json:"last_sync"`
	UsageRows      int           `json:"usage_rows"`
	DistinctModels int           `json:"distinct_models"`
	DateRange      jsonDateRange `json:"date_range"`
	MissingFiles   int           `json:"missing_files"`
}

type jsonDoctorData struct {
	Providers []jsonDoctorProvider `json:"providers"`
	Store     jsonDoctorStore      `json:"store"`
}

func writeDoctorJSON(cmd *cobra.Command, roots []discover.Root, databasePath, requestedZone string) error {
	data := jsonDoctorData{Providers: make([]jsonDoctorProvider, 0, len(roots)), Store: jsonDoctorStore{Path: databasePath, DateRange: jsonDateRange{}}}
	warnings := []string{}
	for _, root := range roots {
		files, walkErrors := discover.ListSourceFiles(root)
		provider := jsonDoctorProvider{Provider: string(root.Provider), Path: root.Path, Source: root.Source, Exists: root.Exists, JSONLFiles: len(files), WalkErrors: []string{}}
		var oldest, newest time.Time
		for _, file := range files {
			provider.TotalBytes += file.Size
			if oldest.IsZero() || file.ModTime.Before(oldest) {
				oldest = file.ModTime
			}
			if newest.IsZero() || file.ModTime.After(newest) {
				newest = file.ModTime
			}
		}
		if !oldest.IsZero() {
			value := oldest.Format(time.RFC3339)
			provider.Oldest = &value
		}
		if !newest.IsZero() {
			value := newest.Format(time.RFC3339)
			provider.Newest = &value
		}
		for _, walkErr := range walkErrors {
			message := walkErr.Error()
			provider.WalkErrors = append(provider.WalkErrors, message)
			warnings = append(warnings, fmt.Sprintf("%s discovery: %s", root.Provider, message))
		}
		data.Providers = append(data.Providers, provider)
	}

	zone := requestedTimezone(requestedZone)
	fileInfo, err := os.Stat(databasePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat usage store: %w", err)
	}
	if err == nil {
		data.Store.Exists = true
		data.Store.SizeBytes = fileInfo.Size()
		database, openErr := store.Open(databasePath)
		if openErr != nil {
			return fmt.Errorf("inspect usage store: %w", openErr)
		}
		defer database.Close()
		info, infoErr := database.Info()
		if infoErr != nil {
			return infoErr
		}
		data.Store.SchemaVersion = &info.SchemaVersion
		storeZone := info.Timezone
		if storeZone == "Local" {
			storeZone = localTimezoneName()
		}
		data.Store.Timezone = optionalString(storeZone)
		if storeZone != "" {
			zone = storeZone
		}
		if info.LastSyncUnix != 0 {
			value := time.Unix(info.LastSyncUnix, 0).Format(time.RFC3339)
			data.Store.LastSync = &value
		}
		data.Store.UsageRows = info.UsageRows
		data.Store.DistinctModels = info.DistinctModels
		data.Store.DateRange = jsonDateRange{FirstDate: optionalString(info.OldestDate), LastDate: optionalString(info.NewestDate)}
		data.Store.MissingFiles = info.MissingFiles
	}
	return writeJSONEnvelope(cmd, "doctor", zone, jsonFilters{}, warnings, data)
}

func writeStoreReport(cmd *cobra.Command, databasePath string) error {
	writer := cmd.OutOrStdout()
	fmt.Fprintln(writer, "Store")
	fmt.Fprintf(writer, "  %-17s %s\n", "Path:", databasePath)
	fileInfo, err := os.Stat(databasePath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(writer, "  %-17s no\n", "Exists:")
			fmt.Fprintf(writer, "  %-17s -\n", "Size:")
			fmt.Fprintf(writer, "  %-17s -\n", "Schema version:")
			fmt.Fprintf(writer, "  %-17s -\n", "Timezone:")
			fmt.Fprintf(writer, "  %-17s -\n", "Last sync:")
			fmt.Fprintf(writer, "  %-17s 0\n", "Usage rows:")
			fmt.Fprintf(writer, "  %-17s 0\n", "Distinct models:")
			fmt.Fprintf(writer, "  %-17s -\n", "Date range:")
			fmt.Fprintf(writer, "  %-17s 0\n", "Missing files:")
			return nil
		}
		return fmt.Errorf("stat usage store: %w", err)
	}
	database, err := store.Open(databasePath)
	if err != nil {
		return fmt.Errorf("inspect usage store: %w", err)
	}
	defer database.Close()
	info, err := database.Info()
	if err != nil {
		return err
	}
	fmt.Fprintf(writer, "  %-17s yes\n", "Exists:")
	fmt.Fprintf(writer, "  %-17s %s\n", "Size:", humanBytes(fileInfo.Size()))
	fmt.Fprintf(writer, "  %-17s %d\n", "Schema version:", info.SchemaVersion)
	fmt.Fprintf(writer, "  %-17s %s\n", "Timezone:", dashIfEmpty(info.Timezone))
	fmt.Fprintf(writer, "  %-17s %s\n", "Last sync:", formatUnix(info.LastSyncUnix))
	fmt.Fprintf(writer, "  %-17s %d\n", "Usage rows:", info.UsageRows)
	fmt.Fprintf(writer, "  %-17s %d\n", "Distinct models:", info.DistinctModels)
	fmt.Fprintf(writer, "  %-17s %s\n", "Date range:", dateRange(info.OldestDate, info.NewestDate))
	fmt.Fprintf(writer, "  %-17s %d\n", "Missing files:", info.MissingFiles)
	return nil
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatUnix(value int64) string {
	if value == 0 {
		return "-"
	}
	return time.Unix(value, 0).Format(time.RFC3339)
}

func dateRange(oldest, newest string) string {
	if oldest == "" {
		return "-"
	}
	return oldest + " to " + newest
}

func writeProviderReport(cmd *cobra.Command, root discover.Root, files []discover.SourceFile, walkErrors []error) {
	var totalSize int64
	var oldest time.Time
	var newest time.Time
	for _, file := range files {
		totalSize += file.Size
		if oldest.IsZero() || file.ModTime.Before(oldest) {
			oldest = file.ModTime
		}
		if newest.IsZero() || file.ModTime.After(newest) {
			newest = file.ModTime
		}
	}

	writer := cmd.OutOrStdout()
	fmt.Fprintln(writer, providerName(root.Provider))
	fmt.Fprintf(writer, "  %-12s %s\n", "Path:", root.Path)
	fmt.Fprintf(writer, "  %-12s %s\n", "Source:", root.Source)
	fmt.Fprintf(writer, "  %-12s %s\n", "Exists:", yesNo(root.Exists))
	fmt.Fprintf(writer, "  %-12s %d\n", "JSONL files:", len(files))
	fmt.Fprintf(writer, "  %-12s %s\n", "Total size:", humanBytes(totalSize))
	fmt.Fprintf(writer, "  %-12s %s\n", "Oldest:", formatDate(oldest))
	fmt.Fprintf(writer, "  %-12s %s\n", "Newest:", formatDate(newest))
	if len(walkErrors) == 0 {
		fmt.Fprintf(writer, "  %-12s none\n", "Walk errors:")
		return
	}

	fmt.Fprintf(writer, "  %-12s %d\n", "Walk errors:", len(walkErrors))
	for _, err := range walkErrors {
		fmt.Fprintf(writer, "    - %v\n", err)
	}
}

func providerName(provider discover.Provider) string {
	switch provider {
	case discover.ProviderCodex:
		return "Codex"
	case discover.ProviderClaude:
		return "Claude"
	default:
		return strings.ToUpper(string(provider))
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func formatDate(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format("2006-01-02")
}

func humanBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}

	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	value := float64(size)
	unit := "B"
	for _, candidate := range units {
		value /= 1024
		unit = candidate
		if value < 1024 {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}
