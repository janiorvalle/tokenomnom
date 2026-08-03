package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/version"
)

func TestRootCommandShowsHelpWithNoArguments(t *testing.T) {
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute root command: %v", err)
	}

	if !strings.Contains(output.String(), "Your agents nom tokens") {
		t.Fatalf("help output missing tagline:\n%s", output.String())
	}
}

func TestTimezoneFingerprintChangesWithRules(t *testing.T) {
	t.Parallel()
	first := timezoneFingerprint(time.FixedZone("Local", 0))
	second := timezoneFingerprint(time.FixedZone("Local", -5*60*60))
	if first == second || first == "" || second == "" {
		t.Fatalf("timezone fingerprints should differ: %q %q", first, second)
	}
}

func TestRootCommandShowsVersion(t *testing.T) {
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"--version"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute --version: %v", err)
	}

	if !strings.Contains(output.String(), version.Version) {
		t.Fatalf("version output %q does not contain %q", output.String(), version.Version)
	}
}

func TestDevelopmentBuildRequiresExplicitMigrationPermission(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("TOKENOMNOM_STATE_DIR", "")
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	previousVersion := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = previousVersion })
	codexDir := filepath.Join(root, "missing-codex")
	claudeDir := filepath.Join(root, "missing-claude")
	args := []string{"sync", "--tz", "UTC", "--codex-dir", codexDir, "--claude-dir", claudeDir}

	command := NewRootCommand()
	command.SetArgs(args)
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	err := command.Execute()
	if !errors.Is(err, store.ErrDevMigrationBlocked) || !strings.Contains(err.Error(), "--allow-migrate") {
		t.Fatalf("unapproved migration error = %v, output = %s", err, output.String())
	}

	command = NewRootCommand()
	command.SetArgs(append([]string{"sync", "--allow-migrate"}, args[1:]...))
	command.SetOut(&output)
	command.SetErr(&output)
	if err := command.Execute(); err != nil {
		t.Fatalf("approved migration: %v\n%s", err, output.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".local", "state", "tokenomnom", store.DatabaseName)); err != nil {
		t.Fatalf("approved migration did not create default store: %v", err)
	}
}

func TestLocalTimezoneNameUsesTZEnvironment(t *testing.T) {
	t.Setenv("TZ", "America/New_York")
	if got := localTimezoneName(); got != "America/New_York" {
		t.Fatalf("local timezone = %q", got)
	}
}
