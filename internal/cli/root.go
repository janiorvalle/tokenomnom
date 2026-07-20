package cli

import (
	"fmt"
	"os"
	"time"
	_ "time/tzdata"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	"github.com/janiorvalle/tokenomnom/internal/version"
)

// NewRootCommand creates the tokenomnom command tree.
func NewRootCommand() *cobra.Command {
	return newRootCommand(theme.ResolveOptions{})
}

func newRootCommand(renderOptions theme.ResolveOptions) *cobra.Command {
	var codexDir string
	var claudeDir string
	var timezone string
	var format string
	var noColor bool

	cmd := &cobra.Command{
		Use:   "tokenomnom",
		Short: "See what your coding agents' tokens would cost at API list prices",
		Long: `Your agents nom tokens. This shows the bill they would have run up.

tokenomnom reconstructs local coding-agent token usage. All dollar figures are
API list-price equivalents, not actual bills.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if theme.FromContext(cmd.Context()).Interactive {
				return runDashboard(cmd, &codexDir, &claudeDir, &timezone)
			}
			return cmd.Help()
		},
		Version: version.Version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			validFormats := "pretty or json"
			if cmd.Name() == "export" {
				validFormats = "csv or json"
			}
			value, err := cmd.Flags().GetString("format")
			if err != nil {
				return err
			}
			if (cmd.Name() == "export" && value != "csv" && value != "json") ||
				(cmd.Name() != "export" && value != "pretty" && value != "json") {
				return fmt.Errorf("invalid --format %q (expected %s)", value, validFormats)
			}
			if cmd.CommandPath() != "tokenomnom config path" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("find user home directory: %w", err)
				}
				overrides := config.Overrides{NoColor: noColor, NoColorChanged: cmd.Flags().Changed("no-color")}
				if cmd.Flags().Changed("codex-dir") {
					overrides.CodexDir = &codexDir
				}
				if cmd.Flags().Changed("claude-dir") {
					overrides.ClaudeDir = &claudeDir
				}
				if cmd.Flags().Changed("tz") {
					overrides.Timezone = &timezone
				}
				if flag := cmd.Flags().Lookup("no-chart"); flag != nil && flag.Changed {
					value, _ := cmd.Flags().GetBool("no-chart")
					overrides.NoChart, overrides.NoChartChanged = value, true
				}
				if flag := cmd.Flags().Lookup("last"); flag != nil && flag.Changed {
					value, _ := cmd.Flags().GetInt("last")
					overrides.DailyLast = &value
				}
				if flag := cmd.Flags().Lookup("provider"); flag != nil && flag.Changed {
					value, _ := cmd.Flags().GetString("provider")
					overrides.Provider = &value
				}
				lookupEnv := renderOptions.LookupEnv
				if lookupEnv == nil {
					lookupEnv = os.LookupEnv
				}
				loaded, err := config.Load(config.LoadOptions{Home: home, Getenv: os.Getenv, LookupEnv: lookupEnv, Output: cmd.ErrOrStderr(), Flags: overrides})
				if err != nil {
					return err
				}
				if source := loaded.Sources[config.KeyCodexDir]; source == "config" || source == "flag" {
					codexDir = loaded.Config.Discovery.CodexDir
				}
				if source := loaded.Sources[config.KeyClaudeDir]; source == "config" || source == "flag" {
					claudeDir = loaded.Config.Discovery.ClaudeDir
				}
				timezone = loaded.Config.Sync.Timezone
				noColor = loaded.Config.Reports.Color == "never"
				renderOptions.Color = loaded.Config.Reports.Color
				renderOptions.IgnoreNoColorEnv = true
				cmd.SetContext(config.WithContext(cmd.Context(), loaded))
			}
			if timezone != "" {
				if _, err := time.LoadLocation(timezone); err != nil {
					return fmt.Errorf("invalid timezone %q: %w", timezone, err)
				}
			}
			renderOptions.NoColor = noColor
			renderOptions.Format = value
			renderOptions.Output = cmd.OutOrStdout()
			cmd.SetContext(theme.WithContext(cmd.Context(), theme.Resolve(renderOptions)))
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&codexDir, "codex-dir", "", "override the Codex data directory")
	cmd.PersistentFlags().StringVar(&claudeDir, "claude-dir", "", "override the Claude Code data directory")
	cmd.PersistentFlags().StringVar(&timezone, "tz", "", "bucket usage in an IANA timezone (default: system local)")
	cmd.PersistentFlags().StringVar(&format, "format", "pretty", "output format (pretty or json)")
	cmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "disable styled output")
	cmd.AddCommand(newDoctorCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newSyncCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newSummaryCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newDailyCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newMonthlyCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newModelsCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newHeatmapCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newPricingCommand(&timezone))
	cmd.AddCommand(newExportCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newInstallSkillCommand(&codexDir, &claudeDir))
	cmd.AddCommand(newConfigCommand())
	cmd.AddCommand(newVaultCommand(&codexDir, &claudeDir))
	cmd.AddCommand(newScheduleCommand())

	return cmd
}

// Execute runs the tokenomnom CLI.
func Execute() error {
	return NewRootCommand().Execute()
}
