package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/history/indexer"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

func newHistoryCommand(codexDir, claudeDir *string) *cobra.Command {
	command := &cobra.Command{
		Use:   "history",
		Short: "Index and inspect local transcript history",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newHistoryIndexCommand(codexDir, claudeDir))
	command.AddCommand(newHistoryStatusCommand())
	command.AddCommand(newHistoryListCommand())
	command.AddCommand(newHistorySearchCommand())
	command.AddCommand(newHistoryShowCommand(codexDir, claudeDir))
	command.AddCommand(newHistoryPromptsCommand())
	command.AddCommand(newHistorySampleCommand())
	command.AddCommand(newHistoryStatsCommand())
	command.AddCommand(newHistoryPurgeCommand())
	return command
}

func newHistoryIndexCommand(codexDir, claudeDir *string) *cobra.Command {
	var provider string
	var source string
	var full bool
	command := &cobra.Command{
		Use:   "index",
		Short: "Incrementally index provider transcript history",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			indexAssistant := appconfig.FromContext(cmd.Context()).Config.History.IndexAssistant
			providers, err := historyProviders(provider)
			if err != nil {
				return err
			}
			if source != "all" && source != "provider" && source != "vault" {
				return fmt.Errorf("invalid --source %q (expected all, provider, or vault)", source)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}
			roots, err := resolveRoots(cmd, *codexDir, *claudeDir, home)
			if err != nil {
				return err
			}
			path, err := historyDatabasePath(home)
			if err != nil {
				return err
			}
			attempt := time.Now()
			providerSummary := indexer.Summary{Errors: []indexer.Issue{}, Warnings: []indexer.Issue{}, Full: full}
			vaultSummary := indexer.VaultSummary{Errors: []indexer.Issue{}, Warnings: []indexer.Issue{}, Full: full}
			var providerErr, vaultErr error
			markProviderError := func() {
				if providerErr != nil && providerSummary.ErrorCount == 0 {
					providerSummary.ErrorCount = 1
					providerSummary.Errors = append(providerSummary.Errors, indexer.Issue{Path: "provider", Error: providerErr.Error()})
				}
			}
			markVaultError := func() {
				if vaultErr != nil && vaultSummary.ErrorCount == 0 {
					vaultSummary.ErrorCount = 1
					vaultSummary.Errors = append(vaultSummary.Errors, indexer.Issue{Path: "vault", Error: vaultErr.Error()})
				}
			}
			runHistoryLocked := func(run func(*historystore.Store) error) error {
				release, err := historystore.Lock(path)
				if err != nil {
					return err
				}
				defer release()
				database, err := historystore.Open(path)
				if err != nil {
					return err
				}
				defer database.Close()
				return run(database)
			}

			if source == "provider" {
				err = runHistoryLocked(func(database *historystore.Store) error {
					providerSummary, providerErr = indexer.Index(indexer.Options{
						Store: database, Roots: roots, Providers: providers, Full: full, Now: func() time.Time { return attempt }, LockHeld: true, NarrowSource: true, IndexAssistant: indexAssistant,
					})
					markProviderError()
					return database.RecordScopedRun(attempt, providerSummary.ErrorCount, false)
				})
				if err != nil {
					return err
				}
			} else {
				instance, usageDatabase, openErr := openVault(cmd, *codexDir, *claudeDir)
				combinedComplete := false
				if openErr != nil {
					vaultErr = openErr
				} else {
					vaultSummary, vaultErr = indexer.IndexVault(indexer.VaultOptions{
						StorePath: path, Vault: instance, Providers: providers, Full: full, Now: func() time.Time { return attempt }, SkipRunRecord: source == "all", IndexAssistant: indexAssistant,
						After: func(database *historystore.Store, current indexer.VaultSummary) error {
							combinedComplete = true
							if source != "all" {
								return nil
							}
							providerSummary, providerErr = indexer.Index(indexer.Options{
								Store: database, Roots: roots, Providers: providers, Full: full, Now: func() time.Time { return attempt }, LockHeld: true, SkipRunRecord: true, IndexAssistant: indexAssistant,
								CompleteAssistantScope: provider == "" && current.ErrorCount == 0,
							})
							markProviderError()
							return database.RecordScopedRun(attempt, providerSummary.ErrorCount+current.ErrorCount, provider == "")
						},
					})
					_ = usageDatabase.Close()
				}
				markVaultError()
				if !combinedComplete && vaultErr != nil {
					err = runHistoryLocked(func(database *historystore.Store) error {
						if source == "all" {
							providerSummary, providerErr = indexer.Index(indexer.Options{
								Store: database, Roots: roots, Providers: providers, Full: full, Now: func() time.Time { return attempt }, LockHeld: true, SkipRunRecord: true, IndexAssistant: indexAssistant,
							})
							markProviderError()
							return database.RecordScopedRun(attempt, providerSummary.ErrorCount+vaultSummary.ErrorCount, provider == "")
						}
						return database.RecordScopedRun(attempt, vaultSummary.ErrorCount, false)
					})
					if err != nil {
						return err
					}
				}
			}
			if writeErr := writeHistoryIndex(cmd, provider, source, providerSummary, vaultSummary); writeErr != nil {
				return writeErr
			}
			indexErr := errors.Join(providerErr, vaultErr)
			if indexErr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), indexErr)
			}
			return indexErr
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "index only codex or claude")
	command.Flags().StringVar(&source, "source", "all", "source set to index (all, provider, or vault)")
	command.Flags().BoolVar(&full, "full", false, "rebuild selected source kinds")
	return command
}

func newHistoryListCommand() *cobra.Command {
	var provider, since, until, cwd, repo, branch, source, cursor, threadKind string
	var limit int
	var rootOnly bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List indexed logical transcript sessions",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := historyProviders(provider); err != nil {
				return err
			}
			if err := validateDateFlag("since", since); err != nil {
				return err
			}
			if err := validateDateFlag("until", until); err != nil {
				return err
			}
			if since != "" && until != "" && until < since {
				return errors.New("--until must be on or after --since")
			}
			if source != "any" && source != "provider" && source != "provider-live" && source != "provider-archive" && source != "vault" {
				return fmt.Errorf("invalid --source %q (expected any, provider, provider-live, provider-archive, or vault)", source)
			}
			if rootOnly && cmd.Flags().Changed("thread-kind") {
				return errors.New("--root-only and --thread-kind are mutually exclusive")
			}
			if threadKind != "all" && threadKind != "root" && threadKind != "subagent" && threadKind != "unknown" {
				return fmt.Errorf("invalid --thread-kind %q (expected root, subagent, unknown, or all)", threadKind)
			}
			if cmd.Flags().Changed("limit") && (limit < 1 || limit > 500) {
				return errors.New("--limit must be between 1 and 500")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			path, err := historyDatabasePath(home)
			if err != nil {
				return err
			}
			release, err := historystore.Lock(path)
			if err != nil {
				return err
			}
			defer release()
			info, err := historystore.Inspect(path)
			if err != nil {
				return err
			}
			if !info.Exists {
				return errors.New("history index does not exist; run tokenomnom history index first")
			}
			database, err := historystore.Open(path)
			if err != nil {
				return err
			}
			defer database.Close()
			requestedLimit := limit
			if cursor != "" && !cmd.Flags().Changed("limit") {
				requestedLimit = 0
			}
			effectiveThreadKind := threadKind
			if rootOnly {
				effectiveThreadKind = "root"
			}
			query := historystore.CatalogQuery{Provider: history.Provider(provider), CWD: cwd, Repo: repo, Branch: branch, Source: historystore.CatalogSource(source), ThreadKind: effectiveThreadKind, Limit: requestedLimit, Cursor: cursor}
			if since != "" {
				value, _ := time.Parse("2006-01-02", since)
				query.Since = &value
			}
			if until != "" {
				value, _ := time.Parse("2006-01-02", until)
				value = value.Add(24*time.Hour - time.Nanosecond)
				query.Until = &value
			}
			page, err := database.ListCatalog(query)
			if err != nil {
				return err
			}
			location, _ := historyPresentationTimezone(cmd)
			presentHistoryCatalogPage(&page, location)
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history list", jsonFilters{Provider: optionalString(provider), Since: optionalString(since), Until: optionalString(until), ThreadKind: optionalString(effectiveThreadKind)}, page.Warnings, page)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-38s %-7s %-20s %-8s %-8s %s\n", "SESSION", "SOURCE", "LAST", "PROMPTS", "VERSIONS", "PREVIEW")
			for _, session := range page.Sessions {
				last := "-"
				if session.LastTimestamp != nil {
					last = *session.LastTimestamp
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%d\t%d\t%s\n", session.SessionID, session.PreferredRetrievalSource, last, session.LogicalPromptCount, session.PreservedSnapshotCount, safePrettyPreview(session.Preview))
			}
			for _, warning := range page.Warnings {
				writeWarningLine(cmd, "WARNING: "+warning)
			}
			if page.HasMore {
				fmt.Fprintf(cmd.OutOrStdout(), "More results: rerun with the same filters and --cursor %s\n", page.NextCursor)
			}
			return nil
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "filter by provider (codex or claude)")
	command.Flags().StringVar(&since, "since", "", "include sessions active on or after YYYY-MM-DD")
	command.Flags().StringVar(&until, "until", "", "include sessions active on or before YYYY-MM-DD")
	command.Flags().StringVar(&cwd, "cwd", "", "filter by exact working directory")
	command.Flags().StringVar(&repo, "repo", "", "filter by known repository name")
	command.Flags().StringVar(&branch, "branch", "", "filter by known branch")
	command.Flags().StringVar(&source, "source", "any", "filter by availability source")
	command.Flags().IntVar(&limit, "limit", 100, "maximum page rows (1-500)")
	command.Flags().StringVar(&cursor, "cursor", "", "continue a previous page")
	command.Flags().StringVar(&threadKind, "thread-kind", "all", "filter by thread kind (root, subagent, unknown, or all)")
	command.Flags().BoolVar(&rootOnly, "root-only", false, "include only directly evidenced root sessions")
	return command
}

func safePrettyPreview(value string) string {
	var result strings.Builder
	for _, current := range value {
		switch {
		case current == '\n' || current == '\r':
			result.WriteByte(' ')
		case unicode.IsControl(current):
			fmt.Fprintf(&result, "\\u%04x", current)
		default:
			result.WriteRune(current)
		}
	}
	return result.String()
}

func newHistoryStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show transcript history index status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}
			path, err := historyDatabasePath(home)
			if err != nil {
				return err
			}
			health, err := inspectHistoryHealth(path)
			if err != nil {
				return err
			}
			return writeHistoryStatus(cmd, health)
		},
	}
}

func inspectHistoryHealth(path string) (historystore.Health, error) {
	health, err := historystore.InspectHealth(path)
	if err == nil {
		return health, nil
	}
	health = historystore.Health{Path: path, ErrorSources: 1, InspectionError: err.Error()}
	if len(health.InspectionError) > 512 {
		health.InspectionError = health.InspectionError[:512]
	}
	if stat, statErr := os.Stat(path); statErr == nil {
		health.Exists = true
		health.SizeBytes = stat.Size()
	} else if !os.IsNotExist(statErr) {
		return historystore.Health{}, statErr
	}
	return health, nil
}

func newHistoryPurgeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "purge",
		Short: "Remove the rebuildable transcript history index",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}
			path, err := historyDatabasePath(home)
			if err != nil {
				return err
			}
			release, err := historystore.Lock(path)
			if err != nil {
				return err
			}
			defer release()
			removed := 0
			for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
				if err := os.Remove(candidate); err == nil {
					removed++
				} else if !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove history index file %q: %w", candidate, err)
				}
			}
			if currentFormat(cmd) == "json" {
				return writeHistoryJSONEnvelope(cmd, "history purge", jsonFilters{}, nil, struct {
					Path         string `json:"path"`
					FilesRemoved int    `json:"files_removed"`
				}{Path: path, FilesRemoved: removed})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "History index purged: %s (%d files removed)\n", path, removed)
			return nil
		},
	}
}

func historyProviders(provider string) ([]history.Provider, error) {
	switch provider {
	case "":
		return nil, nil
	case string(history.ProviderCodex):
		return []history.Provider{history.ProviderCodex}, nil
	case string(history.ProviderClaude):
		return []history.Provider{history.ProviderClaude}, nil
	default:
		return nil, fmt.Errorf("invalid --provider %q (expected codex or claude)", provider)
	}
}

func historyDatabasePath(home string) (string, error) {
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, historystore.DatabaseName), nil
}

type jsonHistoryIndexData struct {
	ScannedSources        int             `json:"scanned_sources"`
	IndexedSources        int             `json:"indexed_sources"`
	NewSources            int             `json:"new_sources"`
	SkippedSources        int             `json:"skipped_sources"`
	AppendedSources       int             `json:"appended_sources"`
	RewrittenSources      int             `json:"rewritten_sources"`
	MissingSources        int             `json:"missing_sources"`
	IndexedPrompts        int             `json:"indexed_prompts"`
	OversizedPrompts      int             `json:"oversized_prompts"`
	ErrorCount            int             `json:"error_count"`
	Errors                []indexer.Issue `json:"errors"`
	Warnings              []indexer.Issue `json:"warnings"`
	Full                  bool            `json:"full"`
	DurationMS            int64           `json:"duration_ms"`
	Source                string          `json:"source"`
	SelectedVaultBundles  int             `json:"selected_vault_bundles"`
	SelectedVaultVersions int             `json:"selected_vault_versions"`
	TraversedVaultBundles int             `json:"traversed_vault_bundles"`
	IndexedVaultBundles   int             `json:"indexed_vault_bundles"`
	SkippedVaultBundles   int             `json:"skipped_vault_bundles"`
	IndexedVaultVersions  int             `json:"indexed_vault_versions"`
	BrokenSkippedBundles  int             `json:"broken_skipped_bundles"`
}

func writeHistoryIndex(cmd *cobra.Command, provider, source string, summary indexer.Summary, vaultSummary indexer.VaultSummary) error {
	errorsFound := append(append([]indexer.Issue{}, summary.Errors...), vaultSummary.Errors...)
	warnings := append(append([]indexer.Issue{}, summary.Warnings...), vaultSummary.Warnings...)
	errorCount := summary.ErrorCount + vaultSummary.ErrorCount
	duration := summary.Duration
	if source == "vault" || source == "all" {
		// Vault duration encloses the provider callback in combined runs.
		duration = vaultSummary.Duration
	}
	if currentFormat(cmd) == "json" {
		return writeHistoryJSONEnvelope(cmd, "history index", jsonFilters{Provider: optionalString(provider)}, nil, jsonHistoryIndexData{
			ScannedSources: summary.ScannedSources, IndexedSources: summary.IndexedSources, NewSources: summary.NewSources,
			SkippedSources: summary.SkippedSources, AppendedSources: summary.AppendedSources, RewrittenSources: summary.RewrittenSources,
			MissingSources: summary.MissingSources, IndexedPrompts: summary.IndexedPrompts + vaultSummary.IndexedPrompts, OversizedPrompts: summary.OversizedPrompts + vaultSummary.OversizedPrompts,
			ErrorCount: errorCount, Errors: errorsFound, Warnings: warnings, Full: summary.Full || vaultSummary.Full,
			DurationMS: duration.Milliseconds(), Source: source,
			SelectedVaultBundles: vaultSummary.SelectedBundles, SelectedVaultVersions: vaultSummary.SelectedVersions,
			TraversedVaultBundles: vaultSummary.TraversedBundles, IndexedVaultBundles: vaultSummary.IndexedBundles,
			SkippedVaultBundles: vaultSummary.SkippedBundles, IndexedVaultVersions: vaultSummary.IndexedVersions,
			BrokenSkippedBundles: vaultSummary.BrokenSkippedBundles,
		})
	}
	writeHeading(cmd, "History Index")
	fmt.Fprintf(cmd.OutOrStdout(), "  Scanned: %d  Indexed: %d  Skipped: %d\n", summary.ScannedSources, summary.IndexedSources, summary.SkippedSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  New: %d  Appended: %d  Rewritten: %d  Missing: %d\n", summary.NewSources, summary.AppendedSources, summary.RewrittenSources, summary.MissingSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  Vault bundles: %d indexed  %d skipped  %d failed  Versions: %d\n", vaultSummary.IndexedBundles, vaultSummary.SkippedBundles, vaultSummary.ErrorCount, vaultSummary.IndexedVersions)
	fmt.Fprintf(cmd.OutOrStdout(), "  Prompts: %d  Oversized: %d  Errors: %d  Duration: %s\n", summary.IndexedPrompts+vaultSummary.IndexedPrompts, summary.OversizedPrompts+vaultSummary.OversizedPrompts, errorCount, duration.Round(time.Millisecond))
	return nil
}

func historyStatusValue(health historystore.Health) string {
	if !health.Exists {
		return "not_indexed"
	}
	if health.ErrorSources > 0 || health.LastRunErrorCount > 0 {
		return "error"
	}
	if health.StaleSources > 0 || health.MissingSources > 0 || !health.SamplingReady {
		return "degraded"
	}
	return "ready"
}

func writeHistoryStatus(cmd *cobra.Command, health historystore.Health) error {
	status := historyStatusValue(health)
	location, _ := historyPresentationTimezone(cmd)
	if currentFormat(cmd) == "json" {
		return writeHistoryJSONEnvelope(cmd, "history status", jsonFilters{}, nil, configuredHistoryHealth(cmd, health))
	}
	writeHeading(cmd, "History")
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Status:", status)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Path:", health.Path)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Size:", humanBytes(health.SizeBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Sessions:", health.Sessions)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Source heads:", health.SourceHeads)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Prompts:", health.Prompts)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d / %d\n", "User/assistant:", health.UserPrompts, health.AssistantPrompts)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Searchable users:", health.SearchableUserPrompts)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Occurrences:", health.Occurrences)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Vault snapshots:", health.PreservedSnapshots)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d / %d\n", "Vault bundles/versions:", health.IndexedVaultBundles, health.IndexedVaultVersions)
	coverage := "-"
	if health.CoverageFirst != "" || health.CoverageLast != "" {
		coverage = dashIfEmpty(stringValue(presentHistoryTimestamp(optionalString(health.CoverageFirst), location))) + " to " + dashIfEmpty(stringValue(presentHistoryTimestamp(optionalString(health.CoverageLast), location)))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Coverage:", coverage)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s to %s\n", "User coverage:", stringValue(presentHistoryTimestamp(optionalString(health.UserCoverageFirst), location)), stringValue(presentHistoryTimestamp(optionalString(health.UserCoverageLast), location)))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s to %s\n", "Assistant coverage:", stringValue(presentHistoryTimestamp(optionalString(health.AssistantCoverageFirst), location)), stringValue(presentHistoryTimestamp(optionalString(health.AssistantCoverageLast), location)))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d / %d / %d\n", "Stale/error/missing:", health.StaleSources, health.ErrorSources, health.MissingSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last index:", stringValue(presentHistoryUnix(health.LastIndexUnix, location)))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Index generation:", health.IndexGeneration)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Sampling ready:", yesNo(health.SamplingReady))
	cfg := appconfig.FromContext(cmd.Context()).Config.History
	nextDue := (*string)(nil)
	if cfg.AutoIndex {
		nextDue = historyNextDue(health.LastAttemptUnix, cfg.AutoInterval, location)
		if nextDue == nil {
			value := time.Now().In(location).Format(time.RFC3339)
			nextDue = &value
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Auto-index enabled:", yesNo(cfg.AutoIndex))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Index assistant:", yesNo(cfg.IndexAssistant))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Assistant indexed:", yesNo(health.AssistantIndexed))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Auto interval:", cfg.AutoInterval)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last attempt:", stringValue(presentHistoryUnix(health.LastAttemptUnix, location)))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last success:", stringValue(presentHistoryUnix(health.LastCompleteSuccessUnix, location)))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Next due:", stringValue(nextDue))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Last error:", dashIfEmpty(health.LastErrorSummary))
	if health.InspectionError != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Inspection error:", health.InspectionError)
	}
	return nil
}

type jsonHistoryHealth struct {
	Status                  string   `json:"status"`
	DatabasePath            string   `json:"database_path"`
	DatabaseSizeBytes       int64    `json:"database_size_bytes"`
	SchemaVersion           int      `json:"schema_version"`
	ExtractorVersion        int      `json:"extractor_version"`
	LogicalSessions         int      `json:"logical_sessions"`
	SourceHeads             int      `json:"source_heads"`
	LogicalPrompts          int      `json:"logical_prompts"`
	UserLogicalPrompts      int      `json:"user_logical_prompts"`
	SearchableUserPrompts   int      `json:"searchable_user_prompts"`
	AssistantLogicalPrompts int      `json:"assistant_logical_prompts"`
	Occurrences             int      `json:"occurrences"`
	LiveSources             int      `json:"live_sources"`
	ProviderArchiveSources  int      `json:"provider_archive_sources"`
	PreservedSnapshots      int      `json:"preserved_snapshots"`
	VaultLocations          int      `json:"vault_locations"`
	ProviderLiveOnly        int      `json:"provider_live_only"`
	ProviderArchiveOnly     int      `json:"provider_archive_only"`
	VaultOnly               int      `json:"vault_only"`
	ExactLiveAndVaulted     int      `json:"exact_live_and_vaulted"`
	UnavailableMetadata     int      `json:"unavailable_metadata"`
	IndexedVaultBundles     int      `json:"indexed_vault_bundles"`
	IndexedVaultVersions    int      `json:"indexed_vault_versions"`
	BrokenSkippedBundles    int      `json:"broken_skipped_bundles"`
	CoverageFirst           *string  `json:"coverage_first"`
	CoverageLast            *string  `json:"coverage_last"`
	UserCoverageFirst       *string  `json:"user_coverage_first"`
	UserCoverageLast        *string  `json:"user_coverage_last"`
	AssistantCoverageFirst  *string  `json:"assistant_coverage_first"`
	AssistantCoverageLast   *string  `json:"assistant_coverage_last"`
	StaleSources            int      `json:"stale_sources"`
	ErrorSources            int      `json:"error_sources"`
	MissingSources          int      `json:"missing_sources"`
	LastIndex               *string  `json:"last_index"`
	LastAttempt             *string  `json:"last_attempt"`
	LastCompleteSuccess     *string  `json:"last_complete_success"`
	LastRunErrorCount       int      `json:"last_run_error_count"`
	LastErrorSummary        *string  `json:"last_error_summary"`
	AutoIndexEnabled        bool     `json:"auto_index_enabled"`
	IndexAssistantEnabled   bool     `json:"index_assistant_enabled"`
	AssistantIndexed        bool     `json:"assistant_indexed"`
	AutoInterval            string   `json:"auto_interval"`
	Providers               []string `json:"providers"`
	NextDue                 *string  `json:"next_due"`
	SamplingReady           bool     `json:"sampling_ready"`
	IndexGeneration         int64    `json:"index_generation"`
	InspectionError         *string  `json:"inspection_error"`
}

func historyHealthJSON(health historystore.Health) jsonHistoryHealth {
	return jsonHistoryHealth{
		Status: historyStatusValue(health), DatabasePath: health.Path, DatabaseSizeBytes: health.SizeBytes,
		SchemaVersion: health.SchemaVersion, ExtractorVersion: health.ExtractorVersion, LogicalSessions: health.Sessions,
		SourceHeads: health.SourceHeads, LogicalPrompts: health.Prompts, UserLogicalPrompts: health.UserPrompts,
		SearchableUserPrompts: health.SearchableUserPrompts, AssistantLogicalPrompts: health.AssistantPrompts, AssistantIndexed: health.AssistantIndexed, Occurrences: health.Occurrences,
		LiveSources: health.LiveSources, ProviderArchiveSources: health.ProviderArchiveSources,
		PreservedSnapshots: health.PreservedSnapshots, VaultLocations: health.VaultLocations,
		ProviderLiveOnly: health.ProviderLiveOnly, ProviderArchiveOnly: health.ProviderArchiveOnly,
		VaultOnly: health.VaultOnly, ExactLiveAndVaulted: health.ExactLiveAndVaulted,
		UnavailableMetadata: health.UnavailableMetadata, IndexedVaultBundles: health.IndexedVaultBundles,
		IndexedVaultVersions: health.IndexedVaultVersions, BrokenSkippedBundles: health.BrokenSkippedBundles,
		CoverageFirst: optionalString(health.CoverageFirst), CoverageLast: optionalString(health.CoverageLast),
		UserCoverageFirst: optionalString(health.UserCoverageFirst), UserCoverageLast: optionalString(health.UserCoverageLast),
		AssistantCoverageFirst: optionalString(health.AssistantCoverageFirst), AssistantCoverageLast: optionalString(health.AssistantCoverageLast),
		StaleSources: health.StaleSources, ErrorSources: health.ErrorSources, MissingSources: health.MissingSources,
		LastIndex: optionalUnix(health.LastIndexUnix), LastAttempt: optionalUnix(health.LastAttemptUnix),
		LastCompleteSuccess: optionalUnix(health.LastCompleteSuccessUnix), SamplingReady: health.SamplingReady, IndexGeneration: health.IndexGeneration,
		LastRunErrorCount: health.LastRunErrorCount,
		LastErrorSummary:  optionalString(health.LastErrorSummary),
		InspectionError:   optionalString(health.InspectionError),
	}
}

func configuredHistoryHealth(cmd *cobra.Command, health historystore.Health) jsonHistoryHealth {
	value := historyHealthJSON(health)
	location, _ := historyPresentationTimezone(cmd)
	value.CoverageFirst = presentHistoryTimestamp(value.CoverageFirst, location)
	value.CoverageLast = presentHistoryTimestamp(value.CoverageLast, location)
	value.UserCoverageFirst = presentHistoryTimestamp(value.UserCoverageFirst, location)
	value.UserCoverageLast = presentHistoryTimestamp(value.UserCoverageLast, location)
	value.AssistantCoverageFirst = presentHistoryTimestamp(value.AssistantCoverageFirst, location)
	value.AssistantCoverageLast = presentHistoryTimestamp(value.AssistantCoverageLast, location)
	value.LastIndex = presentHistoryUnix(health.LastIndexUnix, location)
	value.LastAttempt = presentHistoryUnix(health.LastAttemptUnix, location)
	value.LastCompleteSuccess = presentHistoryUnix(health.LastCompleteSuccessUnix, location)
	cfg := appconfig.FromContext(cmd.Context()).Config.History
	value.AutoIndexEnabled = cfg.AutoIndex
	value.IndexAssistantEnabled = cfg.IndexAssistant
	value.AutoInterval = cfg.AutoInterval
	value.Providers = append([]string(nil), cfg.Providers...)
	if cfg.AutoIndex {
		value.NextDue = historyNextDue(health.LastAttemptUnix, cfg.AutoInterval, location)
		if value.NextDue == nil {
			now := time.Now().In(location).Format(time.RFC3339)
			value.NextDue = &now
		}
	}
	return value
}

func historyNextDue(lastAttempt int64, intervalText string, location *time.Location) *string {
	if lastAttempt == 0 {
		return nil
	}
	interval, err := time.ParseDuration(intervalText)
	if err != nil {
		return nil
	}
	value := time.Unix(lastAttempt, 0).Add(interval).In(location).Format(time.RFC3339)
	return &value
}
