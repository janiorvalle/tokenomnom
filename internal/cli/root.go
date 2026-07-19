package cli

import (
	"fmt"
	"time"
	_ "time/tzdata"

	"github.com/spf13/cobra"

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
			if theme.FromContext(cmd.Context()).Mode == theme.Styled {
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

	return cmd
}

// Execute runs the tokenomnom CLI.
func Execute() error {
	return NewRootCommand().Execute()
}
