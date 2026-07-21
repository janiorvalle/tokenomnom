package cli

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/vault"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

func newVaultCommand(codexDir, claudeDir *string) *cobra.Command {
	cmd := &cobra.Command{Use: "vault", Short: "Archive and inspect coding-agent transcripts", Args: cobra.NoArgs}
	var all bool
	archive := &cobra.Command{
		Use: "archive", Short: "Archive settled transcripts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			instance, database, err := openVault(cmd, *codexDir, *claudeDir)
			if err != nil {
				return err
			}
			defer database.Close()
			result, err := instance.Archive(all)
			if err != nil {
				return err
			}
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "vault archive", requestedTimezone(""), jsonFilters{}, result.Warnings, result)
			}
			for _, provider := range result.Providers {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d archived, %s input, %s stored, %d deduplicated, %d skipped, %d changed during read\n",
					providerName(provider.Provider), provider.Archived, humanBytes(provider.InputBytes), humanBytes(provider.StoredBytes),
					provider.Deduplicated, provider.Skipped, provider.Changed)
			}
			for _, warning := range result.Warnings {
				writeWarningLine(cmd, "WARNING: "+warning)
			}
			return nil
		},
	}
	archive.Flags().BoolVar(&all, "all", false, "archive files regardless of settle age")
	cmd.AddCommand(archive)

	var deep bool
	verify := &cobra.Command{
		Use: "verify", Short: "Verify archived transcripts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			instance, database, err := openVault(cmd, *codexDir, *claudeDir)
			if err != nil {
				return err
			}
			defer database.Close()
			result, verifyErr := instance.Verify(deep)
			if currentFormat(cmd) == "json" {
				if err := writeJSONEnvelope(cmd, "vault verify", requestedTimezone(""), jsonFilters{}, nil, result); err != nil {
					return err
				}
			} else {
				for _, failure := range result.Failures {
					writeWarningLine(cmd, fmt.Sprintf("FAIL: %s version %d (%s): %s", failure.SourcePath, failure.Version, failure.Archive, failure.Error))
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Verified %d of %d manifest entries", result.Verified, result.Checked)
				if deep {
					fmt.Fprint(cmd.OutOrStdout(), " (deep)")
				}
				fmt.Fprintln(cmd.OutOrStdout())
			}
			return verifyErr
		},
	}
	verify.Flags().BoolVar(&deep, "deep", false, "decompress and hash every archived transcript")
	cmd.AddCommand(verify)

	var provider, since, until, sortBy, cursor string
	var limit int
	var latest bool
	list := &cobra.Command{
		Use: "list", Short: "List archived transcripts", Args: cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if provider != "" && provider != "codex" && provider != "claude" {
				return fmt.Errorf("invalid --provider %q (expected codex or claude)", provider)
			}
			if err := validateDateFlag("since", since); err != nil {
				return err
			}
			if err := validateDateFlag("until", until); err != nil {
				return err
			}
			if since != "" && until != "" && until < since {
				return fmt.Errorf("--until must be on or after --since")
			}
			if sortBy != "source" && sortBy != "first_ts" && sortBy != "last_ts" && sortBy != "size" {
				return fmt.Errorf("invalid --sort %q (expected source, first_ts, last_ts, or size)", sortBy)
			}
			if cmd.Flags().Changed("limit") && (limit < 1 || limit > 500) {
				return fmt.Errorf("--limit must be between 1 and 500")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			instance, database, err := openVault(cmd, *codexDir, *claudeDir)
			if err != nil {
				return err
			}
			defer database.Close()
			filter := vault.ListFilter{Provider: discover.Provider(provider)}
			if cmd.Flags().Changed("sort") {
				filter.Sort = store.VaultSort(sortBy)
			}
			if since != "" {
				value, _ := time.Parse("2006-01-02", since)
				filter.Since = &value
			}
			if until != "" {
				value, _ := time.Parse("2006-01-02", until)
				value = value.Add(24*time.Hour - time.Nanosecond)
				filter.Until = &value
			}
			pageMode := cmd.Flags().Changed("limit") || cursor != "" || latest
			var entries []vault.ListEntry
			var page *struct {
				Limit      int    `json:"limit"`
				HasMore    bool   `json:"has_more"`
				NextCursor string `json:"next_cursor"`
			}
			if pageMode {
				requestedLimit := limit
				if cursor != "" && !cmd.Flags().Changed("limit") {
					requestedLimit = 0
				}
				result, err := instance.ListPage(vault.ListPageQuery{ListFilter: filter, Sort: store.VaultSort(sortBy), Limit: requestedLimit, Cursor: cursor, LatestOnly: latest})
				if err != nil {
					return err
				}
				entries = result.Entries
				page = &struct {
					Limit      int    `json:"limit"`
					HasMore    bool   `json:"has_more"`
					NextCursor string `json:"next_cursor"`
				}{Limit: result.Limit, HasMore: result.HasMore, NextCursor: result.NextCursor}
			} else {
				var err error
				entries, err = instance.List(filter)
				if err != nil {
					return err
				}
			}
			if currentFormat(cmd) == "json" {
				filters := jsonFilters{Provider: optionalString(provider), Since: optionalString(since), Until: optionalString(until)}
				data := struct {
					Files []vault.ListEntry `json:"files"`
					Page  *struct {
						Limit      int    `json:"limit"`
						HasMore    bool   `json:"has_more"`
						NextCursor string `json:"next_cursor"`
					} `json:"page,omitempty"`
				}{Files: entries, Page: page}
				if data.Files == nil {
					data.Files = []vault.ListEntry{}
				}
				return writeJSONEnvelope(cmd, "vault list", requestedTimezone(""), filters, nil, data)
			}
			if pageMode {
				fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-7s %-7s %-10s %-8s %-20s %-20s %s\n", "PROVIDER", "SOURCE", "VERSION", "ARCHIVE", "SIZE", "FIRST", "LAST", "ORIGINAL")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "%-7s %-7s %-10s %-8s %-20s %-20s %s\n", "SOURCE", "VERSION", "ARCHIVE", "SIZE", "FIRST", "LAST", "ORIGINAL")
			}
			for _, entry := range entries {
				if pageMode {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\tv%d\t%s\t%s\t%s\t%s\t%s\n", entry.Provider, entry.SourcePath, entry.Version, entry.Archive,
						humanBytes(entry.Size), dashIfEmpty(entry.FirstTS), dashIfEmpty(entry.LastTS), yesNo(entry.OriginalExists))
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "%s\tv%d\t%s\t%s\t%s\t%s\t%s\n", entry.SourcePath, entry.Version, entry.Archive,
						humanBytes(entry.Size), dashIfEmpty(entry.FirstTS), dashIfEmpty(entry.LastTS), yesNo(entry.OriginalExists))
				}
			}
			if page != nil && page.HasMore {
				fmt.Fprintf(cmd.OutOrStdout(), "More results: rerun with the same filters and --cursor %s\n", page.NextCursor)
			}
			return nil
		},
	}
	list.Flags().StringVar(&provider, "provider", "", "filter by provider (codex or claude)")
	list.Flags().StringVar(&since, "since", "", "include transcripts active on or after YYYY-MM-DD")
	list.Flags().StringVar(&until, "until", "", "include transcripts active on or before YYYY-MM-DD")
	list.Flags().StringVar(&sortBy, "sort", "last_ts", "page sort (source, first_ts, last_ts, or size)")
	list.Flags().IntVar(&limit, "limit", 100, "maximum page rows (1-500)")
	list.Flags().StringVar(&cursor, "cursor", "", "continue a previous page")
	list.Flags().BoolVar(&latest, "latest", false, "return only the latest version of each source")
	cmd.AddCommand(list)

	var version int
	cat := &cobra.Command{
		Use: "cat <source-path | rel-path>", Short: "Stream an archived transcript", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if version < 0 {
				return errorsNewVersion()
			}
			instance, database, err := openVault(cmd, *codexDir, *claudeDir)
			if err != nil {
				return err
			}
			defer database.Close()
			if currentFormat(cmd) != "json" {
				_, err = instance.Cat(args[0], version, cmd.OutOrStdout())
				return err
			}
			return writeVaultCatJSON(cmd, instance, args[0], version)
		},
	}
	cat.Flags().IntVar(&version, "version", 0, "select an archived version (default latest)")
	cmd.AddCommand(cat)

	cmd.AddCommand(&cobra.Command{
		Use: "status", Short: "Show vault storage and reclaimable originals", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			instance, database, err := openVault(cmd, *codexDir, *claudeDir)
			if err != nil {
				return err
			}
			defer database.Close()
			status, err := instance.Status()
			if err != nil {
				return err
			}
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "vault status", requestedTimezone(""), jsonFilters{}, nil, status)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Vault: %s (format %d, encryption %s)\n", status.Dir, status.Format, status.Encryption)
			fmt.Fprintf(cmd.OutOrStdout(), "Files: %d  Raw: %s  Stored: %s  Ratio: %.2fx  Reclaimable: %s\n", status.Files,
				humanBytes(status.RawBytes), humanBytes(status.StoredBytes), status.Ratio, humanBytes(status.ReclaimableBytes))
			for _, provider := range status.Providers {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %d files, %s raw, %s stored, %s reclaimable\n", providerName(provider.Provider), provider.Files,
					humanBytes(provider.RawBytes), humanBytes(provider.StoredBytes), humanBytes(provider.ReclaimableBytes))
			}
			if len(status.ReclaimablePaths) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Reclaimable originals:")
				for _, path := range status.ReclaimablePaths {
					fmt.Fprintln(cmd.OutOrStdout(), "  "+path)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "tokenomnom never deletes source files; you may reclaim the listed paths manually.")
			return nil
		},
	})
	return cmd
}

func writeVaultCatJSON(cmd *cobra.Command, instance *vault.Vault, name string, version int) error {
	temp, err := os.CreateTemp("", ".tokenomnom-vault-cat-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	manifest, err := instance.Cat(name, version, temp)
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(tempPath)
	if err != nil {
		return err
	}
	encoding := "base64"
	var content any
	if utf8.Valid(contents) {
		encoding = "utf-8"
		content = string(contents)
	}
	temp, err = os.Open(tempPath)
	if err != nil {
		return err
	}
	defer temp.Close()

	writer := cmd.OutOrStdout()
	if _, err := io.WriteString(writer, "{"); err != nil {
		return err
	}
	fields := []struct {
		key   string
		value any
	}{
		{"schema", reportSchema},
		{"command", "vault cat"},
		{"generated_at", time.Now().UTC().Format(time.RFC3339)},
		{"timezone", requestedTimezone("")},
		{"filters", jsonFilters{}},
		{"disclaimer", pricingDisclaimer},
		{"warnings", []string{}},
	}
	for index, field := range fields {
		if err := writeStreamingJSONField(writer, index > 0, field.key, field.value); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(writer, ",\"data\":{"); err != nil {
		return err
	}
	dataFields := []struct {
		key   string
		value any
	}{
		{"source_path", manifest.SourcePath},
		{"rel_path", manifest.RelPath},
		{"version", manifest.Version},
		{"encoding", encoding},
		{"content", content},
	}
	for index, field := range dataFields {
		if err := writeStreamingJSONField(writer, index > 0, field.key, field.value); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(writer, ",\"content_base64\":\""); err != nil {
		return err
	}
	encoder := base64.NewEncoder(base64.StdEncoding, writer)
	if _, err := io.Copy(encoder, temp); err != nil {
		encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	_, err = io.WriteString(writer, "\"}}\n")
	return err
}

func writeStreamingJSONField(writer io.Writer, comma bool, key string, value any) error {
	keyJSON, err := json.Marshal(key)
	if err != nil {
		return err
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if comma {
		if _, err := io.WriteString(writer, ","); err != nil {
			return err
		}
	}
	if _, err := writer.Write(keyJSON); err != nil {
		return err
	}
	if _, err := io.WriteString(writer, ":"); err != nil {
		return err
	}
	_, err = writer.Write(valueJSON)
	return err
}

func errorsNewVersion() error { return fmt.Errorf("--version must be zero or a positive integer") }

func openVault(cmd *cobra.Command, codexDir, claudeDir string) (*vault.Vault, *store.Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, fmt.Errorf("find user home directory: %w", err)
	}
	roots, err := resolveRoots(cmd, codexDir, claudeDir, home)
	if err != nil {
		return nil, nil, err
	}
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return nil, nil, err
	}
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		return nil, nil, err
	}
	loaded := appconfig.FromContext(cmd.Context())
	dir, err := configuredVaultDir(loaded.Config, home)
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	providers := make([]discover.Provider, 0, len(loaded.Config.Vault.Providers))
	for _, provider := range loaded.Config.Vault.Providers {
		providers = append(providers, discover.Provider(provider))
	}
	minAge, _ := time.ParseDuration(loaded.Config.Vault.MinAge)
	instance, err := vault.New(vault.Options{Dir: dir, Store: database, Roots: roots, Providers: providers, MinAge: minAge})
	if err != nil {
		database.Close()
		return nil, nil, err
	}
	return instance, database, nil
}

func configuredVaultDir(cfg appconfig.Config, home string) (string, error) {
	if cfg.Vault.Dir != "" {
		return filepath.Abs(cfg.Vault.Dir)
	}
	dataDir, err := xdg.DataDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "vault"), nil
}

func parseLastVault(database *store.Store) (*string, error) {
	value, err := database.Meta("last_vault_archive_unix")
	if err != nil || value == "" {
		return nil, err
	}
	unix, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil, err
	}
	formatted := time.Unix(unix, 0).Format(time.RFC3339)
	return &formatted, nil
}
