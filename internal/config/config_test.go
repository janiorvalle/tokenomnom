package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	got := Defaults()
	if got.Reports.Color != "auto" || !got.Reports.Charts || got.Reports.DailyLast != 30 ||
		got.Backup.Enabled != true || got.Backup.Interval != "24h" || got.Backup.Keep != 14 {
		t.Fatalf("unexpected defaults: %#v", got)
	}
}

func TestLoadPrecedenceAndSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := `[discovery]
codex_dir = "/config/codex"
claude_dir = "/config/claude"
[sync]
timezone = "UTC"
[reports]
color = "always"
charts = false
daily_last = 7
default_provider = "claude"
[backup]
enabled = false
interval = "1h"
dir = "/config/backups"
keep = 3
`
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"TOKENOMNOM_CODEX_DIR": "/env/codex",
		"CLAUDE_CONFIG_DIR":    "/env/claude",
		"NO_COLOR":             "1",
	}
	flagCodex, flagZone, flagProvider, flagLast := "/flag/codex", "America/New_York", "codex", 2
	loaded, err := Load(LoadOptions{
		Path: path, Getenv: func(key string) string { return env[key] },
		LookupEnv: func(key string) (string, bool) { value, ok := env[key]; return value, ok },
		Flags: Overrides{
			CodexDir: &flagCodex, Timezone: &flagZone, Provider: &flagProvider, DailyLast: &flagLast,
			NoColorChanged: true, NoColor: false, NoChartChanged: true, NoChart: false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Discovery.CodexDir != flagCodex || loaded.Sources[KeyCodexDir] != "flag" {
		t.Fatalf("codex precedence = %#v, %q", loaded.Config.Discovery, loaded.Sources[KeyCodexDir])
	}
	if loaded.Config.Discovery.ClaudeDir != "/env/claude" || loaded.Sources[KeyClaudeDir] != "env CLAUDE_CONFIG_DIR" {
		t.Fatalf("claude precedence = %#v, %q", loaded.Config.Discovery, loaded.Sources[KeyClaudeDir])
	}
	if loaded.Config.Sync.Timezone != flagZone || loaded.Config.Reports.Color != "auto" || !loaded.Config.Reports.Charts ||
		loaded.Config.Reports.DailyLast != flagLast || loaded.Config.Reports.DefaultProvider != flagProvider {
		t.Fatalf("flag precedence = %#v", loaded.Config)
	}
	for _, key := range []string{KeyBackupEnabled, KeyBackupInterval, KeyBackupDir, KeyBackupKeep} {
		if loaded.Sources[key] != "config" {
			t.Errorf("source[%s] = %q", key, loaded.Sources[key])
		}
	}
}

func TestLoadMissingMalformedInvalidAndUnknown(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	missing := filepath.Join(t.TempDir(), "missing.toml")
	loaded, err := Load(LoadOptions{Path: missing, Getenv: func(string) string { return "" }, LookupEnv: none})
	if err != nil || loaded.Found || loaded.Sources[KeyBackupInterval] != "default" {
		t.Fatalf("missing load = %#v, %v", loaded, err)
	}

	malformed := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(malformed, []byte("[reports\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadOptions{Path: malformed, LookupEnv: none}); err == nil || !strings.Contains(err.Error(), "read config") {
		t.Fatalf("malformed error = %v", err)
	}

	if err := os.WriteFile(malformed, []byte("[backup]\ninterval = \"soon\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadOptions{Path: malformed, LookupEnv: none}); err == nil || !strings.Contains(err.Error(), "backup.interval") {
		t.Fatalf("invalid error = %v", err)
	}

	if err := os.WriteFile(malformed, []byte("[reports]\ndaily_last = 5\ntypo = true\n[unknown]\nvalue = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warning bytes.Buffer
	if _, err := Load(LoadOptions{Path: malformed, LookupEnv: none, Output: &warning}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning.String(), "reports.typo") || !strings.Contains(warning.String(), "unknown") {
		t.Fatalf("unknown-key warning = %q", warning.String())
	}
}

func TestEnvironmentPrecedencePrefersTokenomnomVariables(t *testing.T) {
	env := map[string]string{
		"TOKENOMNOM_CODEX_DIR":  "/tokenomnom/codex",
		"CODEX_HOME":            "/native/codex",
		"TOKENOMNOM_CLAUDE_DIR": "/tokenomnom/claude",
		"CLAUDE_CONFIG_DIR":     "/native/claude",
	}
	loaded, err := Load(LoadOptions{
		Path: filepath.Join(t.TempDir(), "missing"), Getenv: func(key string) string { return env[key] },
		LookupEnv: func(key string) (string, bool) { value, ok := env[key]; return value, ok },
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Config.Discovery.CodexDir != "/tokenomnom/codex" || loaded.Config.Discovery.ClaudeDir != "/tokenomnom/claude" {
		t.Fatalf("environment precedence = %#v", loaded.Config.Discovery)
	}
}
