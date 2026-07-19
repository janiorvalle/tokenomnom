package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

func newSyncCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Incrementally ingest coding-agent token usage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}
			roots, err := discover.Resolve(discover.ResolveOptions{
				CodexDir: *codexDir, ClaudeDir: *claudeDir, Home: home, Getenv: os.Getenv,
			})
			if err != nil {
				return err
			}
			stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
			if err != nil {
				return err
			}
			databasePath := filepath.Join(stateDir, store.DatabaseName)
			release, err := store.Lock(databasePath)
			if err != nil {
				return err
			}
			defer release()
			database, err := store.Open(databasePath)
			if err != nil {
				return err
			}
			defer database.Close()

			location := time.Local
			name := location.String()
			if *timezone != "" {
				location, err = time.LoadLocation(*timezone)
				if err != nil {
					return fmt.Errorf("load timezone %q: %w", *timezone, err)
				}
				name = *timezone
			}
			summary, err := syncer.Sync(syncer.Options{
				Store: database, Roots: roots, Location: location, Timezone: name,
				TimezoneFingerprint: timezoneFingerprint(location), Full: full, LockHeld: true,
			})
			if err != nil {
				return fmt.Errorf("sync usage: %w", err)
			}
			writeSyncSummary(cmd, summary)
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "re-ingest all files while retaining vanished history")
	return cmd
}

func timezoneFingerprint(location *time.Location) string {
	hash := sha256.New()
	end := time.Date(2101, time.January, 1, 12, 0, 0, 0, time.UTC)
	for instant := time.Date(1970, time.January, 1, 12, 0, 0, 0, time.UTC); instant.Before(end); instant = instant.AddDate(0, 0, 1) {
		name, offset := instant.In(location).Zone()
		fmt.Fprintf(hash, "%s:%s:%d\n", instant.Format("2006-01-02"), name, offset)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func writeSyncSummary(cmd *cobra.Command, summary syncer.Summary) {
	writer := cmd.OutOrStdout()
	fmt.Fprintln(writer, "Sync complete")
	fmt.Fprintf(writer, "  %-19s %d\n", "Files scanned:", summary.FilesScanned)
	fmt.Fprintf(writer, "  %-19s %d\n", "Files skipped:", summary.FilesSkipped)
	fmt.Fprintf(writer, "  %-19s %d\n", "Files appended:", summary.FilesAppended)
	fmt.Fprintf(writer, "  %-19s %d\n", "Files rewritten:", summary.FilesRewritten)
	fmt.Fprintf(writer, "  %-19s %d\n", "Files missing:", summary.FilesMissing)
	fmt.Fprintf(writer, "  %-19s %d\n", "Events applied:", summary.EventsApplied)
	fmt.Fprintf(writer, "  %-19s %d\n", "Usage rows:", summary.UsageRows)
	fmt.Fprintf(writer, "  %-19s %s\n", "Duration:", summary.Duration.Round(time.Millisecond))
	if summary.FullReingest {
		fmt.Fprintln(writer, "Full re-ingest completed.")
	}
	if summary.UnknownModelTokens > 0 {
		fmt.Fprintf(writer, "WARNING: %d unknown-model tokens were ingested and remain explicitly attributed to unknown.\n", summary.UnknownModelTokens)
	}
	if summary.UnclassifiedCacheWriteTokens > 0 {
		fmt.Fprintf(writer, "WARNING: %d unclassified cache-write tokens were ingested and remain unclassified.\n", summary.UnclassifiedCacheWriteTokens)
	}
}
