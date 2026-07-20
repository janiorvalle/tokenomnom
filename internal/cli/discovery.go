package cli

import (
	"os"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
)

func resolveRoots(cmd *cobra.Command, codexDir, claudeDir, home string) ([]discover.Root, error) {
	roots, err := discover.Resolve(discover.ResolveOptions{
		CodexDir: codexDir, ClaudeDir: claudeDir, Home: home, Getenv: os.Getenv,
	})
	if err != nil {
		return nil, err
	}
	loaded := appconfig.FromContext(cmd.Context())
	for index := range roots {
		key := appconfig.KeyCodexDir
		configuredDir := loaded.Config.Discovery.CodexDir
		if roots[index].Provider == discover.ProviderClaude {
			key = appconfig.KeyClaudeDir
			configuredDir = loaded.Config.Discovery.ClaudeDir
		}
		if loaded.Sources[key] == "config" && configuredDir != "" {
			roots[index].Source = "config"
		}
	}
	return roots, nil
}
