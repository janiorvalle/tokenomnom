package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

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
			providers, err := historyProviders(provider)
			if err != nil {
				return err
			}
			if source != "provider" {
				return fmt.Errorf("invalid --source %q (expected provider)", source)
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
			summary, indexErr := indexer.Index(indexer.Options{Store: database, Roots: roots, Providers: providers, Full: full, LockHeld: true})
			if writeErr := writeHistoryIndex(cmd, provider, summary); writeErr != nil {
				return writeErr
			}
			if indexErr != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), indexErr)
			}
			return indexErr
		},
	}
	command.Flags().StringVar(&provider, "provider", "", "index only codex or claude")
	command.Flags().StringVar(&source, "source", "provider", "source set to index (provider)")
	command.Flags().BoolVar(&full, "full", false, "rebuild selected provider source heads")
	return command
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
			health, err := historystore.InspectHealth(path)
			if err != nil {
				return err
			}
			return writeHistoryStatus(cmd, health)
		},
	}
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
				return writeJSONEnvelope(cmd, "history purge", "", jsonFilters{}, nil, struct {
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
	ScannedSources   int             `json:"scanned_sources"`
	IndexedSources   int             `json:"indexed_sources"`
	NewSources       int             `json:"new_sources"`
	SkippedSources   int             `json:"skipped_sources"`
	AppendedSources  int             `json:"appended_sources"`
	RewrittenSources int             `json:"rewritten_sources"`
	MissingSources   int             `json:"missing_sources"`
	IndexedPrompts   int             `json:"indexed_prompts"`
	OversizedPrompts int             `json:"oversized_prompts"`
	ErrorCount       int             `json:"error_count"`
	Errors           []indexer.Issue `json:"errors"`
	Warnings         []indexer.Issue `json:"warnings"`
	Full             bool            `json:"full"`
	DurationMS       int64           `json:"duration_ms"`
}

func writeHistoryIndex(cmd *cobra.Command, provider string, summary indexer.Summary) error {
	if currentFormat(cmd) == "json" {
		return writeJSONEnvelope(cmd, "history index", "", jsonFilters{Provider: optionalString(provider)}, nil, jsonHistoryIndexData{
			ScannedSources: summary.ScannedSources, IndexedSources: summary.IndexedSources, NewSources: summary.NewSources,
			SkippedSources: summary.SkippedSources, AppendedSources: summary.AppendedSources, RewrittenSources: summary.RewrittenSources,
			MissingSources: summary.MissingSources, IndexedPrompts: summary.IndexedPrompts, OversizedPrompts: summary.OversizedPrompts,
			ErrorCount: summary.ErrorCount, Errors: summary.Errors, Warnings: summary.Warnings, Full: summary.Full,
			DurationMS: summary.Duration.Milliseconds(),
		})
	}
	writeHeading(cmd, "History Index")
	fmt.Fprintf(cmd.OutOrStdout(), "  Scanned: %d  Indexed: %d  Skipped: %d\n", summary.ScannedSources, summary.IndexedSources, summary.SkippedSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  New: %d  Appended: %d  Rewritten: %d  Missing: %d\n", summary.NewSources, summary.AppendedSources, summary.RewrittenSources, summary.MissingSources)
	fmt.Fprintf(cmd.OutOrStdout(), "  Prompts: %d  Oversized: %d  Errors: %d  Duration: %s\n", summary.IndexedPrompts, summary.OversizedPrompts, summary.ErrorCount, summary.Duration.Round(time.Millisecond))
	return nil
}

func historyStatusValue(health historystore.Health) string {
	if !health.Exists {
		return "not_indexed"
	}
	if health.ErrorSources > 0 || health.LastRunErrorCount > 0 {
		return "error"
	}
	if health.StaleSources > 0 || health.MissingSources > 0 {
		return "degraded"
	}
	return "ready"
}

func writeHistoryStatus(cmd *cobra.Command, health historystore.Health) error {
	status := historyStatusValue(health)
	if currentFormat(cmd) == "json" {
		return writeJSONEnvelope(cmd, "history status", "", jsonFilters{}, nil, historyHealthJSON(health))
	}
	writeHeading(cmd, "History")
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Status:", status)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Path:", health.Path)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Size:", humanBytes(health.SizeBytes))
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Sessions:", health.Sessions)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Source heads:", health.SourceHeads)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Prompts:", health.Prompts)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Occurrences:", health.Occurrences)
	fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %d\n", "Index generation:", health.IndexGeneration)
	if health.InspectionError != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "  %-20s %s\n", "Inspection error:", health.InspectionError)
	}
	return nil
}

type jsonHistoryHealth struct {
	Status                 string  `json:"status"`
	DatabasePath           string  `json:"database_path"`
	DatabaseSizeBytes      int64   `json:"database_size_bytes"`
	SchemaVersion          int     `json:"schema_version"`
	ExtractorVersion       int     `json:"extractor_version"`
	LogicalSessions        int     `json:"logical_sessions"`
	SourceHeads            int     `json:"source_heads"`
	LogicalPrompts         int     `json:"logical_prompts"`
	Occurrences            int     `json:"occurrences"`
	LiveSources            int     `json:"live_sources"`
	ProviderArchiveSources int     `json:"provider_archive_sources"`
	StaleSources           int     `json:"stale_sources"`
	ErrorSources           int     `json:"error_sources"`
	MissingSources         int     `json:"missing_sources"`
	LastIndex              *string `json:"last_index"`
	LastAttempt            *string `json:"last_attempt"`
	LastCompleteSuccess    *string `json:"last_complete_success"`
	LastRunErrorCount      int     `json:"last_run_error_count"`
	IndexGeneration        int64   `json:"index_generation"`
	InspectionError        *string `json:"inspection_error"`
}

func historyHealthJSON(health historystore.Health) jsonHistoryHealth {
	return jsonHistoryHealth{
		Status: historyStatusValue(health), DatabasePath: health.Path, DatabaseSizeBytes: health.SizeBytes,
		SchemaVersion: health.SchemaVersion, ExtractorVersion: health.ExtractorVersion, LogicalSessions: health.Sessions,
		SourceHeads: health.SourceHeads, LogicalPrompts: health.Prompts, Occurrences: health.Occurrences,
		LiveSources: health.LiveSources, ProviderArchiveSources: health.ProviderArchiveSources,
		StaleSources: health.StaleSources, ErrorSources: health.ErrorSources, MissingSources: health.MissingSources,
		LastIndex: optionalUnix(health.LastIndexUnix), LastAttempt: optionalUnix(health.LastAttemptUnix),
		LastCompleteSuccess: optionalUnix(health.LastCompleteSuccessUnix), IndexGeneration: health.IndexGeneration,
		LastRunErrorCount: health.LastRunErrorCount,
		InspectionError:   optionalString(health.InspectionError),
	}
}
