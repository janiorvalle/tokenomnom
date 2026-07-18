package cli

import (
	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/version"
)

// NewRootCommand creates the tokenomnom command tree.
func NewRootCommand() *cobra.Command {
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
	}

	return cmd
}

// Execute runs the tokenomnom CLI.
func Execute() error {
	return NewRootCommand().Execute()
}
