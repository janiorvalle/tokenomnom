package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/skill"
	"github.com/janiorvalle/tokenomnom/internal/upgrade"
	"github.com/janiorvalle/tokenomnom/internal/version"
)

var errUpgradeAvailable = errors.New("tokenomnom update available")

type upgradeEngine interface {
	Check(context.Context) (upgrade.Release, bool, error)
	Install(context.Context, upgrade.Release) (upgrade.Result, error)
}

type upgradeCommandOptions struct {
	newEngine  func() (upgradeEngine, error)
	runCommand func(context.Context, string, ...string) (stdout, stderr []byte, err error)
}

type jsonUpgradeData struct {
	CurrentVersion  string          `json:"current_version"`
	LatestVersion   string          `json:"latest_version"`
	UpdateAvailable bool            `json:"update_available"`
	ReleaseURL      string          `json:"release_url"`
	Installed       *upgrade.Result `json:"installed,omitempty"`
	SkillRefreshed  bool            `json:"skill_refreshed"`
}

func newUpgradeCommand(codexDir, claudeDir *string) *cobra.Command {
	return newUpgradeCommandWithOptions(codexDir, claudeDir, upgradeCommandOptions{
		newEngine: func() (upgradeEngine, error) {
			executable, err := os.Executable()
			if err != nil {
				return nil, &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_EXECUTABLE_UNKNOWN", Message: "the running tokenomnom executable could not be located", Action: "reinstall with install.sh", Cause: err}
			}
			executable, err = filepath.EvalSymlinks(executable)
			if err != nil {
				return nil, &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_EXECUTABLE_UNKNOWN", Message: "the installed tokenomnom target could not be resolved", Action: "reinstall with install.sh", Cause: err}
			}
			return upgrade.New(upgrade.Options{
				Client: &http.Client{Timeout: 2 * time.Minute}, CurrentVersion: version.Version,
				GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, ExecutablePath: executable,
				GitHubToken: os.Getenv("GITHUB_TOKEN"),
			})
		},
		runCommand: func(ctx context.Context, path string, args ...string) ([]byte, []byte, error) {
			command := exec.CommandContext(ctx, path, args...)
			command.Stdin = os.Stdin
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			return stdout.Bytes(), stderr.Bytes(), err
		},
	})
}

func newUpgradeCommandWithOptions(codexDir, claudeDir *string, options upgradeCommandOptions) *cobra.Command {
	var checkOnly bool
	command := &cobra.Command{
		Use:          "upgrade",
		Short:        "Upgrade tokenomnom to the latest release",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			engine, err := options.newEngine()
			if err != nil {
				return err
			}
			release, available, err := engine.Check(cmd.Context())
			if err != nil {
				return err
			}
			data := jsonUpgradeData{
				CurrentVersion: version.Version, LatestVersion: release.Version,
				UpdateAvailable: available, ReleaseURL: release.URL,
			}
			if checkOnly {
				if currentFormat(cmd) == "json" {
					if err := writeJSONEnvelope(cmd, "upgrade", requestedTimezone(""), jsonFilters{}, nil, data); err != nil {
						return err
					}
				} else if available {
					fmt.Fprintf(cmd.OutOrStdout(), "Update available: v%s → v%s\n%s\n", version.Version, release.Version, release.URL)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "tokenomnom v%s is current.\n", version.Version)
				}
				if available {
					return errUpgradeAvailable
				}
				return nil
			}

			_, roots, err := resolveSkillRoots(*codexDir, *claudeDir)
			if err != nil {
				return err
			}
			installedSkills, err := installedSkillRoots(roots)
			if err != nil {
				return err
			}
			if available {
				result, err := engine.Install(cmd.Context(), release)
				if err != nil {
					return err
				}
				data.Installed = &result
				if currentFormat(cmd) != "json" {
					fmt.Fprintf(cmd.OutOrStdout(), "Upgraded tokenomnom v%s → v%s\n%s\n", result.PreviousVersion, result.Version, result.ReleaseURL)
				}
			} else if currentFormat(cmd) != "json" {
				fmt.Fprintf(cmd.OutOrStdout(), "tokenomnom v%s is current.\n", version.Version)
			}
			if len(installedSkills) > 0 {
				binaryPath := ""
				targetSkillVersion := version.Version
				if data.Installed != nil {
					binaryPath = data.Installed.ExecutablePath
					targetSkillVersion = data.Installed.Version
				} else {
					binaryPath, err = os.Executable()
					if err != nil {
						return err
					}
				}
				for _, installedRoot := range installedSkills {
					args, cleanup, err := isolatedSkillRefreshArgs(installedRoot)
					if err != nil {
						return err
					}
					childOutput, childErrors, runErr := options.runCommand(cmd.Context(), binaryPath, args...)
					cleanup()
					if runErr == nil {
						runErr = validateSkillRefreshReceipt(childOutput, installedRoot, targetSkillVersion)
					}
					if runErr != nil {
						details := strings.TrimSpace(string(childErrors))
						if details == "" {
							details = strings.TrimSpace(string(childOutput))
						}
						if details != "" {
							runErr = fmt.Errorf("%w: %s", runErr, details)
						}
						return &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_SKILL_REFRESH_FAILED", Message: fmt.Sprintf("tokenomnom v%s is installed, but its %s agent skill could not be refreshed", targetSkillVersion, installedRoot.Provider), Action: "run tokenomnom install-skill now so the skill matches the binary", Retryable: true, Cause: runErr}
					}
				}
				data.SkillRefreshed = true
				if currentFormat(cmd) != "json" {
					fmt.Fprintln(cmd.OutOrStdout(), "Refreshed the installed tokenomnom agent skill.")
				}
			} else if currentFormat(cmd) != "json" {
				fmt.Fprintln(cmd.OutOrStdout(), "Agent skill not installed. Run tokenomnom install-skill to add it.")
			}
			if currentFormat(cmd) == "json" {
				return writeJSONEnvelope(cmd, "upgrade", requestedTimezone(""), jsonFilters{}, nil, data)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&checkOnly, "check", false, "report whether an update is available without changing anything")
	return command
}

func installedSkillRoots(roots []discover.Root) ([]discover.Root, error) {
	installed := make([]discover.Root, 0, len(roots))
	for _, root := range roots {
		if !root.Exists {
			continue
		}
		_, owned, exists, err := skill.Inspect(skill.Path(root.Path))
		if err != nil {
			return nil, &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_SKILL_INSPECT_FAILED", Message: fmt.Sprintf("the installed %s skill could not be inspected before upgrade", root.Provider), Action: "fix access to the installed SKILL.md and retry", Cause: err}
		}
		if exists && owned {
			installed = append(installed, root)
		}
	}
	return installed, nil
}

func isolatedSkillRefreshArgs(installedRoot discover.Root) ([]string, func(), error) {
	disabledRoot, err := os.MkdirTemp("", "tokenomnom-upgrade-disabled-root-*")
	if err != nil {
		return nil, func() {}, &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_SKILL_REFRESH_PREPARE_FAILED", Message: "a temporary path for the skill refresh could not be reserved", Action: "check temporary directory access and retry", Retryable: true, Cause: err}
	}
	if err := os.Remove(disabledRoot); err != nil {
		return nil, func() {}, &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_SKILL_REFRESH_PREPARE_FAILED", Message: "the temporary path for the skill refresh could not be prepared", Action: "check temporary directory access and retry", Retryable: true, Cause: err}
	}
	args := []string{"install-skill", "--format", "json", "--skip-offer-state"}
	switch installedRoot.Provider {
	case discover.ProviderCodex:
		args = append(args, "--codex-dir", installedRoot.Path, "--claude-dir", disabledRoot)
	case discover.ProviderClaude:
		args = append(args, "--codex-dir", disabledRoot, "--claude-dir", installedRoot.Path)
	default:
		return nil, func() {}, &upgrade.Error{Code: "TOKENOMNOM_UPGRADE_SKILL_PROVIDER_UNSUPPORTED", Message: fmt.Sprintf("the installed %q skill provider cannot be refreshed", installedRoot.Provider), Action: "run tokenomnom install-skill after the upgrade"}
	}
	return args, func() { _ = os.RemoveAll(disabledRoot) }, nil
}

func validateSkillRefreshReceipt(contents []byte, installedRoot discover.Root, targetVersion string) error {
	var receipt struct {
		Schema  string `json:"schema"`
		Command string `json:"command"`
		Data    struct {
			Providers []skillInstallResult `json:"providers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(contents, &receipt); err != nil {
		return fmt.Errorf("read install-skill receipt: %w", err)
	}
	if receipt.Schema != reportSchema || receipt.Command != "install-skill" {
		return fmt.Errorf("install-skill returned an unexpected receipt")
	}
	for _, provider := range receipt.Data.Providers {
		if provider.Provider == string(installedRoot.Provider) && provider.Version == targetVersion && (provider.Action == "updated" || provider.Action == "up_to_date") {
			return nil
		}
	}
	return fmt.Errorf("install-skill did not confirm %s at v%s", installedRoot.Provider, targetVersion)
}
