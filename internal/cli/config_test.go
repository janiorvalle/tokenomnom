package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestConfigPathDoesNotRequireValidConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"config", "path"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != filepath.Join(dir, "config.toml") {
		t.Fatalf("config path = %q", output.String())
	}
}

func TestConfigShowAnnotatesEffectiveSources(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", dir)
	t.Setenv("TOKENOMNOM_CLAUDE_DIR", "/env/claude")
	withoutEnv(t, "NO_COLOR")
	contents := "[discovery]\ncodex_dir = \"/config/codex\"\n[reports]\ndaily_last = 7\n[backup]\ninterval = \"1h\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"config", "show", "--codex-dir", "/flag/codex", "--provider", "codex", "--last", "2", "--no-chart"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if _, err := toml.Decode(output.String(), &parsed); err != nil {
		t.Fatalf("config show is not valid TOML: %v\n%s", err, output.String())
	}
	for _, want := range []string{
		`codex_dir = "/flag/codex" # flag`,
		`claude_dir = "/env/claude" # env TOKENOMNOM_CLAUDE_DIR`,
		`timezone = "" # default`,
		`charts = false # flag`,
		`daily_last = 2 # flag`,
		`default_provider = "codex" # flag`,
		`interval = "1h" # config`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("config show missing %q:\n%s", want, output.String())
		}
	}
}

func TestConfigShowJSONUsesEnvelope(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", dir)
	withoutEnv(t, "NO_COLOR")
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"config", "show", "--format", "json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Command string `json:"command"`
		Data    struct {
			Config struct {
				Reports struct {
					DailyLast int `json:"daily_last"`
				} `json:"reports"`
			} `json:"config"`
			Sources map[string]string `json:"sources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Command != "config" || envelope.Data.Config.Reports.DailyLast != 30 || envelope.Data.Sources["reports.daily_last"] != "default" {
		t.Fatalf("config envelope = %#v", envelope)
	}
}

func TestDoctorReportsConfigDiscoverySource(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	for _, dir := range []string{configDir, codexDir, claudeDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	contents := fmt.Sprintf("[discovery]\ncodex_dir = %q\nclaude_dir = %q\n[backup]\nenabled = false\n", codexDir, claudeDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	withoutEnv(t, "TOKENOMNOM_CODEX_DIR")
	withoutEnv(t, "TOKENOMNOM_CLAUDE_DIR")
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "Source:      config") != 2 {
		t.Fatalf("doctor config sources:\n%s", output.String())
	}
}

func TestDoctorKeepsAutomaticSourceForEmptyConfigDirectories(t *testing.T) {
	configDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[discovery]\ncodex_dir = \"\"\nclaude_dir = \"\"\n[backup]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	for _, name := range []string{"TOKENOMNOM_CODEX_DIR", "TOKENOMNOM_CLAUDE_DIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR"} {
		withoutEnv(t, name)
	}
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Source:      config") || strings.Count(output.String(), "Source:      default") != 2 {
		t.Fatalf("doctor automatic sources:\n%s", output.String())
	}
}

func TestReportConfigDefaultsApplyAndFlagsWin(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	configDir := os.Getenv("TOKENOMNOM_CONFIG_DIR")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := "[reports]\ndaily_last = 1\ndefault_provider = \"claude\"\ncharts = false\n[backup]\nenabled = false\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := executeReport([]string{"daily", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output, "2026-02-01") || !strings.Contains(output, "2026-02-03") || strings.Contains(output, "■") {
		t.Fatalf("configured daily report:\n%s", output)
	}
	output, err = executeReport([]string{"daily", "--no-sync", "--provider", "codex", "--last", "2"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "2026-01-31") || !strings.Contains(output, "2026-02-01") || strings.Contains(output, "2026-02-03") {
		t.Fatalf("flag overrides:\n%s", output)
	}
}

func TestBackupFailureWarnsWithoutFailingSync(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	codexDir := filepath.Join(root, "codex")
	sessions := filepath.Join(codexDir, "sessions")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	configText := fmt.Sprintf("[backup]\ndir = %q\ninterval = \"1h\"\n", blocked)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := "{\"timestamp\":\"2026-07-19T10:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-test\"}}\n" +
		"{\"timestamp\":\"2026-07-19T10:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"last_token_usage\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n"
	if err := os.WriteFile(filepath.Join(sessions, "session.jsonl"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	withoutEnv(t, "NO_COLOR")
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"sync", "--format", "json", "--tz", "UTC", "--codex-dir", codexDir, "--claude-dir", filepath.Join(root, "missing")})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync failed instead of warning: %v", err)
	}
	var envelope struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Warnings) != 1 || !strings.Contains(envelope.Warnings[0], "backup usage") {
		t.Fatalf("backup warnings = %#v", envelope.Warnings)
	}
}

func withoutEnv(t *testing.T, key string) {
	t.Helper()
	value, present := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(key, value)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}
