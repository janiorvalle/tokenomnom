package cli

import (
	"fmt"
	"time"
	_ "time/tzdata"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/version"
)

// NewRootCommand creates the tokenomnom command tree.
func NewRootCommand() *cobra.Command {
	var codexDir string
	var claudeDir string
	var timezone string

	cmd := &cobra.Command{
		Use:   "tokenomnom",
		Short: "See what your coding agents' tokens would cost at API list prices",
		Long: `Your agents nom tokens. This shows the bill they would have run up.

tokenomnom reconstructs local coding-agent token usage. All dollar figures are
API list-price equivalents, not actual bills.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
		Version: version.Version,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if timezone == "" {
				return nil
			}
			if _, err := time.LoadLocation(timezone); err != nil {
				return fmt.Errorf("invalid timezone %q: %w", timezone, err)
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&codexDir, "codex-dir", "", "override the Codex data directory")
	cmd.PersistentFlags().StringVar(&claudeDir, "claude-dir", "", "override the Claude Code data directory")
	cmd.PersistentFlags().StringVar(&timezone, "tz", "", "bucket usage in an IANA timezone (default: system local)")
	cmd.AddCommand(newDoctorCommand(&codexDir, &claudeDir))
	cmd.AddCommand(newSyncCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newSummaryCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newDailyCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newMonthlyCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newModelsCommand(&codexDir, &claudeDir, &timezone))
	cmd.AddCommand(newPricingCommand())

	return cmd
}

// Execute runs the tokenomnom CLI.
func Execute() error {
	return NewRootCommand().Execute()
}
