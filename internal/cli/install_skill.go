package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/skill"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/version"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

type skillInstallResult struct {
	Provider string `json:"provider"`
	Path     string `json:"path"`
	Action   string `json:"action"`
	Version  string `json:"version"`
	Previous string `json:"-"`
}

type jsonInstallSkillData struct {
	Providers []skillInstallResult `json:"providers"`
}

func newInstallSkillCommand(codexDir, claudeDir *string) *cobra.Command {
	var force bool
	var remove bool
	cmd := &cobra.Command{
		Use:   "install-skill",
		Short: "Install the tokenomnom agent skill",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			home, roots, err := resolveSkillRoots(*codexDir, *claudeDir)
			if err != nil {
				return err
			}
			results, err := applySkills(roots, version.Version, force, remove)
			if err != nil {
				return err
			}
			if !remove && skillInstallSucceeded(results) {
				if err := setSkillOffer(home, skill.OfferAccepted); err != nil {
					return err
				}
			}
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "install-skill", requestedTimezone(""), jsonFilters{}, nil, jsonInstallSkillData{Providers: results})
			}
			for _, result := range results {
				fmt.Fprintln(cmd.OutOrStdout(), formatSkillResult(result))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "replace or remove a foreign skill file")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove the installed tokenomnom skill")
	return cmd
}

func resolveSkillRoots(codexDir, claudeDir string) (string, []discover.Root, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", nil, fmt.Errorf("find user home directory: %w", err)
	}
	roots, err := discover.Resolve(discover.ResolveOptions{
		CodexDir: codexDir, ClaudeDir: claudeDir, Home: home, Getenv: os.Getenv,
	})
	return home, roots, err
}

func applySkills(roots []discover.Root, targetVersion string, force, remove bool) ([]skillInstallResult, error) {
	results := make([]skillInstallResult, 0, len(roots))
	for _, root := range roots {
		result, err := applySkill(root, targetVersion, force, remove)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func skillInstallSucceeded(results []skillInstallResult) bool {
	for _, result := range results {
		switch result.Action {
		case "installed", "updated", "up_to_date":
			return true
		}
	}
	return false
}

func skillOfferDatabasePath(home string) (string, error) {
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, store.DatabaseName), nil
}

func setSkillOffer(home, value string) error {
	databasePath, err := skillOfferDatabasePath(home)
	if err != nil {
		return err
	}
	database, err := store.Open(databasePath)
	if err != nil {
		return err
	}
	defer database.Close()
	// Offer state belongs to the usage state directory, so a fresh state dir may ask again.
	return database.Transaction(func(tx *store.Tx) error { return tx.SetMeta(skill.OfferMetaKey, value) })
}

func applySkill(root discover.Root, targetVersion string, force, remove bool) (skillInstallResult, error) {
	path := skill.Path(root.Path)
	result := skillInstallResult{Provider: string(root.Provider), Path: path}
	if !root.Exists {
		result.Action = "skipped_no_root"
		return result, nil
	}
	currentVersion, owned, exists, err := skill.Inspect(path)
	if err != nil {
		return result, fmt.Errorf("inspect %s skill: %w", root.Provider, err)
	}
	if remove {
		if !exists {
			result.Action = "not_installed"
			return result, nil
		}
		if !owned && !force {
			result.Action = "refused_foreign"
			return result, nil
		}
		if err := skill.Remove(path); err != nil {
			return result, fmt.Errorf("remove %s skill: %w", root.Provider, err)
		}
		result.Action = "removed"
		result.Version = currentVersion
		return result, nil
	}
	if exists && !owned && !force {
		result.Action = "refused_foreign"
		return result, nil
	}
	if owned && currentVersion == targetVersion {
		result.Action = "up_to_date"
		result.Version = targetVersion
		return result, nil
	}
	if err := skill.Write(path, skill.Document(targetVersion)); err != nil {
		return result, fmt.Errorf("install %s skill: %w", root.Provider, err)
	}
	result.Version = targetVersion
	if owned {
		result.Action = "updated"
		result.Previous = currentVersion
	} else {
		result.Action = "installed"
	}
	return result, nil
}

func formatSkillResult(result skillInstallResult) string {
	provider := providerName(discover.Provider(result.Provider))
	switch result.Action {
	case "installed":
		return fmt.Sprintf("%s: installed v%s · %s", provider, result.Version, result.Path)
	case "updated":
		return fmt.Sprintf("%s: updated v%s → v%s · %s", provider, result.Previous, result.Version, result.Path)
	case "up_to_date":
		return fmt.Sprintf("%s: up to date v%s · %s", provider, result.Version, result.Path)
	case "skipped_no_root":
		return fmt.Sprintf("%s: skipped: no root · %s", provider, result.Path)
	case "refused_foreign":
		return fmt.Sprintf("%s: refused: foreign file present (use --force) · %s", provider, result.Path)
	case "removed":
		versionText := ""
		if result.Version != "" {
			versionText = " v" + result.Version
		}
		return fmt.Sprintf("%s: removed%s · %s", provider, versionText, result.Path)
	default:
		return fmt.Sprintf("%s: not installed · %s", provider, result.Path)
	}
}
