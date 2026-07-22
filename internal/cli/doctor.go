package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/backup"
	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	historyfreshness "github.com/janiorvalle/tokenomnom/internal/history/freshness"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/schedule"
	"github.com/janiorvalle/tokenomnom/internal/skill"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/vault"
	"github.com/janiorvalle/tokenomnom/internal/version"
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

			roots, err := resolveRoots(cmd, *codexDir, *claudeDir, home)
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
	offer, err := storedSkillOffer(databasePath)
	if err != nil {
		return err
	}
	writeSkillsReport(cmd, roots, offer)

	fmt.Fprintln(cmd.OutOrStdout())
	if err := writeStoreReport(cmd, databasePath); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout())
	if err := writeDoctorHistoryReport(cmd, roots, databasePath); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout())
	if err := writeBackupsReport(cmd, databasePath); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout())
	scheduleData, err := doctorSchedule(cmd)
	if err != nil {
		return err
	}
	if err := writeVaultReport(cmd, roots, databasePath, scheduleData); err != nil {
		return err
	}

	fmt.Fprintln(cmd.OutOrStdout())
	writeScheduleStatus(cmd, scheduleData)

	fmt.Fprintln(cmd.OutOrStdout())
	switch len(found) {
	case 0:
		writeWarningLine(cmd, "Status: no provider data directories found. Use --codex-dir, --claude-dir, or the TOKENOMNOM_*_DIR environment variables to point tokenomnom at them.")
	case 1:
		writeEmphasisLine(cmd, fmt.Sprintf("Status: only %s was found; discovery is ready to use.", providerName(found[0])))
	default:
		writeEmphasisLine(cmd, "Status: both providers found; discovery is ready to use.")
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
	Skills    []jsonDoctorSkill    `json:"skills"`
	Offer     *string              `json:"offer"`
	Store     jsonDoctorStore      `json:"store"`
	History   jsonHistoryHealth    `json:"history"`
	Backups   jsonDoctorBackups    `json:"backups"`
	Vault     jsonDoctorVault      `json:"vault"`
	Schedule  jsonScheduleData     `json:"schedule"`
}

type jsonDoctorBackups struct {
	Enabled    bool    `json:"enabled"`
	Dir        string  `json:"dir"`
	Interval   string  `json:"interval"`
	LastBackup *string `json:"last_backup"`
	Count      int     `json:"count"`
	TotalBytes int64   `json:"total_bytes"`
	NewestFile *string `json:"newest_file"`
}

type jsonDoctorVault struct {
	Dir                     string  `json:"dir"`
	Initialized             bool    `json:"initialized"`
	Format                  *int    `json:"format"`
	Encryption              *string `json:"encryption"`
	Files                   int     `json:"files"`
	RawBytes                int64   `json:"raw_bytes"`
	StoredBytes             int64   `json:"stored_bytes"`
	LastUsageSync           *string `json:"last_usage_sync"`
	LastArchive             *string `json:"last_archive"`
	LastDeepVerification    *string `json:"last_deep_verification"`
	LastStatusScan          *string `json:"last_status_scan"`
	ReclaimableBytes        int64   `json:"reclaimable_bytes"`
	ReclaimableCachedAt     *string `json:"reclaimable_cached_at"`
	VaultedSources          int     `json:"vaulted_sources"`
	SettledUnvaultedSources int     `json:"settled_unvaulted_sources"`
	RecentUnsettledSources  int     `json:"recent_unsettled_sources"`
	KnownBrokenBundles      int     `json:"known_broken_bundles"`
	AutoVaultEnabled        bool    `json:"auto_vault_enabled"`
	SchedulerInstalled      bool    `json:"scheduler_installed"`
	SchedulerCurrent        bool    `json:"scheduler_current"`
}

type jsonDoctorSkill struct {
	Provider        string  `json:"provider"`
	Path            string  `json:"path"`
	Status          string  `json:"status"`
	Version         *string `json:"version"`
	CurrentVersion  string  `json:"current_version"`
	UpdateAvailable bool    `json:"update_available"`
}

func writeDoctorJSON(cmd *cobra.Command, roots []discover.Root, databasePath, requestedZone string) error {
	data := jsonDoctorData{Providers: make([]jsonDoctorProvider, 0, len(roots)), Skills: make([]jsonDoctorSkill, 0, len(roots)), Store: jsonDoctorStore{Path: databasePath, DateRange: jsonDateRange{}}}
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
		skillData := doctorSkillJSON(root)
		data.Skills = append(data.Skills, skillData)
		if skillData.UpdateAvailable {
			warnings = append(warnings, fmt.Sprintf("%s tokenomnom skill is outdated. Run: tokenomnom install-skill", providerName(root.Provider)))
		}
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
		if warning := missingFilesWarning(info.MissingFiles); warning != "" {
			warnings = append(warnings, warning)
		}
		data.Offer = optionalString(info.SkillOffer)
	}
	backupData, err := doctorBackups(cmd, databasePath)
	if err != nil {
		return err
	}
	data.Backups = backupData
	scheduleData, err := doctorSchedule(cmd)
	if err != nil {
		return err
	}
	data.Schedule = scheduleData
	vaultData, err := doctorVault(cmd, roots, databasePath, scheduleData)
	if err != nil {
		return err
	}
	data.Vault = vaultData
	historyHealth, drift, err := doctorHistory(cmd, roots, databasePath)
	if err != nil {
		return err
	}
	data.History = configuredHistoryHealth(cmd, historyHealth, drift)
	if historyHealth.LastErrorSummary != "" {
		warnings = append(warnings, historyHealth.LastErrorSummary)
	}
	warnings = append(warnings, drift.Warnings...)
	return writeJSONEnvelope(cmd, "doctor", zone, jsonFilters{}, warnings, data)
}

func doctorHistory(cmd *cobra.Command, roots []discover.Root, usageDatabasePath string) (historystore.Health, historyfreshness.Result, error) {
	path := filepath.Join(filepath.Dir(usageDatabasePath), historystore.DatabaseName)
	health, err := inspectHistoryHealth(path)
	if err != nil {
		return historystore.Health{}, historyfreshness.Result{}, err
	}
	return health, historyfreshness.Probe(path, configuredHistoryRoots(cmd, roots), nil), nil
}

func writeDoctorHistoryReport(cmd *cobra.Command, roots []discover.Root, usageDatabasePath string) error {
	health, drift, err := doctorHistory(cmd, roots, usageDatabasePath)
	if err != nil {
		return err
	}
	return writeHistoryStatus(cmd, health, drift)
}

func doctorSchedule(cmd *cobra.Command) (jsonScheduleData, error) {
	options, err := currentScheduleOptions(cmd)
	if err != nil {
		return jsonScheduleData{}, err
	}
	status, err := schedule.Inspect(options)
	if err != nil {
		return jsonScheduleData{}, err
	}
	return scheduleData(cmd, status)
}

func writeVaultReport(cmd *cobra.Command, roots []discover.Root, databasePath string, scheduleData jsonScheduleData) error {
	data, err := doctorVault(cmd, roots, databasePath, scheduleData)
	if err != nil {
		return err
	}
	writeHeading(cmd, "Vault")
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s\n", "Directory:", data.Dir)
	format := "not initialized"
	if data.Format != nil {
		format = fmt.Sprintf("v%d, %s", *data.Format, stringValue(data.Encryption))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s\n", "Format:", format)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %d\n", "Files:", data.Files)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s\n", "Raw size:", humanBytes(data.RawBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s\n", "Stored size:", humanBytes(data.StoredBytes))
	last := "-"
	if data.LastArchive != nil {
		last = *data.LastArchive
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s\n", "Last archive:", last)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", "Last usage sync:", stringValue(data.LastUsageSync))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", "Last deep verification:", stringValue(data.LastDeepVerification))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", "Last status scan:", stringValue(data.LastStatusScan))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %d\n", "Vaulted sources:", data.VaultedSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %d\n", "Settled, unvaulted:", data.SettledUnvaultedSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %d\n", "Recent, unsettled:", data.RecentUnsettledSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %d\n", "Known broken bundles:", data.KnownBrokenBundles)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", "Auto-vault enabled:", yesNo(data.AutoVaultEnabled))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", "Scheduler installed:", yesNo(data.SchedulerInstalled))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-24s %s\n", "Scheduler current:", yesNo(data.SchedulerCurrent))
	reclaimable := humanBytes(data.ReclaimableBytes)
	if data.ReclaimableCachedAt != nil {
		reclaimable += " (status scan " + *data.ReclaimableCachedAt + ")"
	} else {
		reclaimable += " (run vault status to verify)"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-16s %s\n", "Reclaimable:", reclaimable)
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return "-"
	}
	return *value
}

func doctorVault(cmd *cobra.Command, roots []discover.Root, databasePath string, scheduleData jsonScheduleData) (jsonDoctorVault, error) {
	loaded := appconfig.FromContext(cmd.Context())
	home, err := os.UserHomeDir()
	if err != nil {
		return jsonDoctorVault{}, err
	}
	dir, err := configuredVaultDir(loaded.Config, home)
	if err != nil {
		return jsonDoctorVault{}, err
	}
	result := jsonDoctorVault{
		Dir: dir, AutoVaultEnabled: loaded.Config.Vault.Auto,
		SchedulerInstalled: scheduleData.Installed,
		SchedulerCurrent:   scheduleData.Installed && scheduleData.DefinitionExists && scheduleData.BinaryExists && !scheduleData.IntervalDrift,
	}
	marker, found, err := vault.InspectFormat(dir)
	if err != nil {
		return result, err
	}
	if found {
		result.Initialized = true
		result.Format = &marker.VaultFormat
		result.Encryption = &marker.Encryption
	}
	if _, err := os.Stat(databasePath); err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}
	database, err := store.Open(databasePath)
	if err != nil {
		return result, err
	}
	defer database.Close()
	info, err := database.Info()
	if err != nil {
		return result, err
	}
	result.LastUsageSync = optionalUnix(info.LastSyncUnix)
	providers := make([]discover.Provider, 0, len(loaded.Config.Vault.Providers))
	for _, provider := range loaded.Config.Vault.Providers {
		providers = append(providers, discover.Provider(provider))
	}
	minAge, _ := time.ParseDuration(loaded.Config.Vault.MinAge)
	instance, err := vault.New(vault.Options{Dir: dir, Store: database, Roots: roots, Providers: providers, MinAge: minAge})
	if err != nil {
		return result, err
	}
	readiness, err := instance.Readiness()
	if err != nil {
		return result, err
	}
	result.Initialized = readiness.Initialized
	if readiness.Initialized {
		result.Format = &readiness.Status.Format
		result.Encryption = &readiness.Status.Encryption
	}
	result.Files, result.RawBytes, result.StoredBytes = readiness.Status.Files, readiness.Status.RawBytes, readiness.Status.StoredBytes
	result.ReclaimableBytes = readiness.Status.ReclaimableBytes
	result.LastArchive = optionalUnix(readiness.LastArchiveUnix)
	result.LastDeepVerification = optionalUnix(readiness.LastDeepVerificationUnix)
	result.LastStatusScan = optionalUnix(readiness.LastStatusScanUnix)
	result.ReclaimableCachedAt = result.LastStatusScan
	result.VaultedSources = readiness.VaultedSources
	result.SettledUnvaultedSources = readiness.SettledUnvaulted
	result.RecentUnsettledSources = readiness.RecentUnsettled
	result.KnownBrokenBundles = readiness.KnownBrokenBundles
	return result, nil
}

func optionalUnix(value int64) *string {
	if value == 0 {
		return nil
	}
	formatted := time.Unix(value, 0).Format(time.RFC3339)
	return &formatted
}

func writeBackupsReport(cmd *cobra.Command, databasePath string) error {
	data, err := doctorBackups(cmd, databasePath)
	if err != nil {
		return err
	}
	writeHeading(cmd, "Backups")
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %s\n", "Enabled:", yesNo(data.Enabled))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %s\n", "Directory:", data.Dir)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %s\n", "Interval:", data.Interval)
	last := "-"
	if data.LastBackup != nil {
		last = *data.LastBackup
	}
	newest := "-"
	if data.NewestFile != nil {
		newest = *data.NewestFile
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %s\n", "Last backup:", last)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %d\n", "Count:", data.Count)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %s\n", "Total size:", humanBytes(data.TotalBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-13s %s\n", "Newest file:", newest)
	return nil
}

func doctorBackups(cmd *cobra.Command, databasePath string) (jsonDoctorBackups, error) {
	cfg := appconfig.FromContext(cmd.Context()).Config.Backup
	dir, err := backupDir(cmd)
	if err != nil {
		return jsonDoctorBackups{}, err
	}
	stats, err := backup.Inspect(dir)
	if err != nil {
		return jsonDoctorBackups{}, err
	}
	result := jsonDoctorBackups{
		Enabled: cfg.Enabled, Dir: dir, Interval: cfg.Interval, Count: stats.Count,
		TotalBytes: stats.TotalBytes,
	}
	if stats.NewestFile != "" {
		value := filepath.Join(dir, stats.NewestFile)
		result.NewestFile = &value
	}
	if _, err := os.Stat(databasePath); err == nil {
		database, err := store.Open(databasePath)
		if err != nil {
			return jsonDoctorBackups{}, fmt.Errorf("inspect usage store backups: %w", err)
		}
		lastText, metaErr := database.Meta(backup.MetaKey)
		database.Close()
		if metaErr != nil {
			return jsonDoctorBackups{}, metaErr
		}
		if lastText != "" {
			unix, parseErr := strconv.ParseInt(lastText, 10, 64)
			if parseErr != nil {
				return jsonDoctorBackups{}, fmt.Errorf("parse last backup time: %w", parseErr)
			}
			value := time.Unix(unix, 0).Format(time.RFC3339)
			result.LastBackup = &value
		}
	} else if !os.IsNotExist(err) {
		return jsonDoctorBackups{}, fmt.Errorf("stat usage store: %w", err)
	}
	return result, nil
}

func writeSkillsReport(cmd *cobra.Command, roots []discover.Root, offer string) {
	writeHeading(cmd, "Skills")
	fmt.Fprintf(cmd.OutOrStdout(), "  %-8s %s\n", "Offer:", dashIfEmpty(offer))
	for _, root := range roots {
		status, _, updateAvailable := doctorSkillStatus(root)
		fmt.Fprintf(cmd.OutOrStdout(), "  %-8s %s\n", providerName(root.Provider)+":", status)
		if updateAvailable {
			writeWarningLine(cmd, "  Update available. Run: tokenomnom install-skill")
		}
	}
}

func storedSkillOffer(databasePath string) (string, error) {
	if _, err := os.Stat(databasePath); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("stat usage store: %w", err)
	}
	database, err := store.Open(databasePath)
	if err != nil {
		return "", fmt.Errorf("inspect usage store: %w", err)
	}
	defer database.Close()
	info, err := database.Info()
	if err != nil {
		return "", err
	}
	return info.SkillOffer, nil
}

func doctorSkillJSON(root discover.Root) jsonDoctorSkill {
	status, installedVersion, updateAvailable := doctorSkillStatus(root)
	return jsonDoctorSkill{
		Provider: string(root.Provider), Path: skill.Path(root.Path), Status: status,
		Version: optionalString(installedVersion), CurrentVersion: version.Version, UpdateAvailable: updateAvailable,
	}
}

func doctorSkillStatus(root discover.Root) (string, string, bool) {
	installedVersion, owned, exists, err := skill.Inspect(skill.Path(root.Path))
	if err != nil {
		return "unreadable: " + err.Error(), "", false
	}
	if !exists {
		return "not installed", "", false
	}
	if !owned {
		return "foreign file present", "", false
	}
	return "installed v" + installedVersion, installedVersion, skill.UpdateAvailable(installedVersion, version.Version)
}

func writeStoreReport(cmd *cobra.Command, databasePath string) error {
	writer := cmd.OutOrStdout()
	writeHeading(cmd, "Store")
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
			fmt.Fprintf(writer, "  %-45s 0\n", "Synced transcript files no longer present:")
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
	fmt.Fprintf(writer, "  %-45s %d\n", "Synced transcript files no longer present:", info.MissingFiles)
	if warning := missingFilesWarning(info.MissingFiles); warning != "" {
		writeWarningLine(cmd, "WARNING: "+warning)
	}
	return nil
}

func missingFilesWarning(count int) string {
	if count == 0 {
		return ""
	}
	return fmt.Sprintf("%d synced transcript files are no longer present. Their usage remains retained. Raw transcript availability depends on whether those files were vaulted.", count)
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
	writeProviderHeading(cmd, string(root.Provider), providerName(root.Provider))
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
		writeWarningLine(cmd, fmt.Sprintf("    - %v", err))
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
