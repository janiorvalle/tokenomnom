package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect tokenomnom configuration", Args: cobra.NoArgs}
	cmd.AddCommand(&cobra.Command{
		Use: "path", Short: "Print the user config path", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}
			path, err := appconfig.Path(xdg.Options{Home: home, Getenv: os.Getenv})
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	})
	show := &cobra.Command{
		Use: "show", Short: "Print the effective configuration", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			loaded := appconfig.FromContext(cmd.Context())
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "config", requestedTimezone(loaded.Config.Sync.Timezone), jsonFilters{}, nil, loaded)
			}
			writeEffectiveConfig(cmd, loaded)
			return nil
		},
	}
	show.Flags().String("provider", "", "filter by provider (codex or claude)")
	show.Flags().Int("last", 30, "show the most recent N active days")
	show.Flags().Bool("no-chart", false, "suppress the terminal chart")
	cmd.AddCommand(show)
	return cmd
}

func writeEffectiveConfig(cmd *cobra.Command, loaded appconfig.Loaded) {
	cfg, sources := loaded.Config, loaded.Sources
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, "[discovery]")
	writeConfigString(w, "codex_dir", cfg.Discovery.CodexDir, sources[appconfig.KeyCodexDir])
	writeConfigString(w, "claude_dir", cfg.Discovery.ClaudeDir, sources[appconfig.KeyClaudeDir])
	fmt.Fprintln(w, "\n[sync]")
	writeConfigString(w, "timezone", cfg.Sync.Timezone, sources[appconfig.KeyTimezone])
	fmt.Fprintln(w, "\n[reports]")
	writeConfigString(w, "color", cfg.Reports.Color, sources[appconfig.KeyColor])
	writeConfigBool(w, "charts", cfg.Reports.Charts, sources[appconfig.KeyCharts])
	writeConfigInt(w, "daily_last", cfg.Reports.DailyLast, sources[appconfig.KeyDailyLast])
	writeConfigString(w, "default_provider", cfg.Reports.DefaultProvider, sources[appconfig.KeyDefaultProvider])
	fmt.Fprintln(w, "\n[backup]")
	writeConfigBool(w, "enabled", cfg.Backup.Enabled, sources[appconfig.KeyBackupEnabled])
	writeConfigString(w, "interval", cfg.Backup.Interval, sources[appconfig.KeyBackupInterval])
	writeConfigString(w, "dir", cfg.Backup.Dir, sources[appconfig.KeyBackupDir])
	writeConfigInt(w, "keep", cfg.Backup.Keep, sources[appconfig.KeyBackupKeep])
	fmt.Fprintln(w, "\n[vault]")
	writeConfigString(w, "dir", cfg.Vault.Dir, sources[appconfig.KeyVaultDir])
	writeConfigString(w, "min_age", cfg.Vault.MinAge, sources[appconfig.KeyVaultMinAge])
	writeConfigStrings(w, "providers", cfg.Vault.Providers, sources[appconfig.KeyVaultProviders])
	writeConfigBool(w, "auto", cfg.Vault.Auto, sources[appconfig.KeyVaultAuto])
	writeConfigString(w, "auto_interval", cfg.Vault.AutoInterval, sources[appconfig.KeyVaultAutoInterval])
	fmt.Fprintln(w, "\n[schedule]")
	writeConfigString(w, "interval", cfg.Schedule.Interval, sources[appconfig.KeyScheduleInterval])
}

func writeConfigString(w io.Writer, key, value, source string) {
	var encoded strings.Builder
	_ = toml.NewEncoder(&encoded).Encode(map[string]string{"value": value})
	quoted := strings.TrimSpace(strings.TrimPrefix(encoded.String(), "value = "))
	fmt.Fprintf(w, "%s = %s # %s\n", key, quoted, source)
}

func writeConfigBool(w io.Writer, key string, value bool, source string) {
	fmt.Fprintf(w, "%s = %t # %s\n", key, value, source)
}

func writeConfigInt(w io.Writer, key string, value int, source string) {
	fmt.Fprintf(w, "%s = %d # %s\n", key, value, source)
}

func writeConfigStrings(w io.Writer, key string, value []string, source string) {
	var encoded strings.Builder
	_ = toml.NewEncoder(&encoded).Encode(map[string][]string{"value": value})
	array := strings.TrimSpace(strings.TrimPrefix(encoded.String(), "value = "))
	fmt.Fprintf(w, "%s = %s # %s\n", key, array, source)
}
