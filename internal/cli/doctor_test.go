package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorReportsFixtureDirectories(t *testing.T) {
	tempDir := t.TempDir()
	codexDir := filepath.Join(tempDir, "codex")
	claudeDir := filepath.Join(tempDir, "claude")
	writeDoctorFixture(t, filepath.Join(codexDir, "sessions", "2026", "06", "13", "one.jsonl"), 1024, time.Date(2026, time.June, 13, 12, 0, 0, 0, time.UTC))
	writeDoctorFixture(t, filepath.Join(codexDir, "archived_sessions", "two.jsonl"), 512, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
	writeDoctorFixture(t, filepath.Join(claudeDir, "projects", "project-a", "three.jsonl"), 10, time.Date(2026, time.May, 5, 12, 0, 0, 0, time.UTC))

	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor", "--codex-dir", codexDir, "--claude-dir", claudeDir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute doctor: %v", err)
	}

	wantFragments := []string{
		"Codex\n",
		"Path:        " + codexDir,
		"Source:      flag",
		"Exists:      yes",
		"JSONL files: 2",
		"Total size:  1.5 KiB",
		"Oldest:      2026-06-13",
		"Newest:      2026-07-01",
		"Claude\n",
		"Path:        " + claudeDir,
		"JSONL files: 1",
		"Status: both providers found",
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("doctor output missing %q:\n%s", fragment, output.String())
		}
	}
}

func TestDoctorAllowsNoProviders(t *testing.T) {
	tempDir := t.TempDir()

	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{
		"doctor",
		"--codex-dir", filepath.Join(tempDir, "missing-codex"),
		"--claude-dir", filepath.Join(tempDir, "missing-claude"),
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute doctor with no providers: %v", err)
	}
	if !strings.Contains(output.String(), "Status: no provider data directories found") {
		t.Fatalf("doctor output missing no-provider status:\n%s", output.String())
	}
	if strings.Count(output.String(), "Exists:      no") != 2 {
		t.Fatalf("doctor output should report two missing roots:\n%s", output.String())
	}
}

func TestDoctorUsesTokenomnomEnvironment(t *testing.T) {
	tempDir := t.TempDir()
	codexDir := filepath.Join(tempDir, "env-codex")
	claudeDir := filepath.Join(tempDir, "env-claude")
	if err := os.Mkdir(codexDir, 0o755); err != nil {
		t.Fatalf("create Codex fixture root: %v", err)
	}
	if err := os.Mkdir(claudeDir, 0o755); err != nil {
		t.Fatalf("create Claude fixture root: %v", err)
	}
	t.Setenv("TOKENOMNOM_CODEX_DIR", codexDir)
	t.Setenv("TOKENOMNOM_CLAUDE_DIR", claudeDir)

	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute doctor with environment roots: %v", err)
	}
	if !strings.Contains(output.String(), "Source:      env:TOKENOMNOM_CODEX_DIR") {
		t.Fatalf("doctor output missing Codex environment source:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "Source:      env:TOKENOMNOM_CLAUDE_DIR") {
		t.Fatalf("doctor output missing Claude environment source:\n%s", output.String())
	}
}

func TestSyncSummaryAndDoctorStoreSection(t *testing.T) {
	tempDir := t.TempDir()
	stateDir := filepath.Join(tempDir, "state")
	codexDir := filepath.Join(tempDir, "codex")
	claudeDir := filepath.Join(tempDir, "claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "one.jsonl"),
		"{\"timestamp\":\"2026-07-18T09:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-test\"}}\n"+
			"{\"timestamp\":\"2026-07-18T10:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":5,\"output_tokens\":2},\"last_token_usage\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n")

	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"sync", "--tz", "UTC", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute sync: %v", err)
	}
	for _, fragment := range []string{"Sync complete", "Files scanned:", "1", "Events applied:", "Usage rows:"} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("sync output missing %q:\n%s", fragment, output.String())
		}
	}

	output.Reset()
	cmd = NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute doctor after sync: %v", err)
	}
	for _, fragment := range []string{"Store\n", "Path:             " + filepath.Join(stateDir, "usage.db"), "Exists:           yes", "Schema version:   3", "Timezone:         UTC", "Usage rows:       1", "Distinct models:  1", "Date range:       2026-07-18 to 2026-07-18", "Backups\n", "Enabled:      yes", "Last backup:", "Vault\n", "Format:          v1, none", "Schedule\n", "Mechanism:"} {
		if !strings.Contains(output.String(), fragment) {
			t.Errorf("doctor output missing %q:\n%s", fragment, output.String())
		}
	}

	if err := os.Remove(filepath.Join(codexDir, "sessions", "one.jsonl")); err != nil {
		t.Fatal(err)
	}
	cmd = NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"sync", "--tz", "UTC", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sync deletion: %v", err)
	}
	output.Reset()
	cmd = NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor deletion: %v", err)
	}
	if !strings.Contains(output.String(), "Missing files:    1") || !strings.Contains(output.String(), "Usage rows:       1") {
		t.Fatalf("doctor did not report retained missing file:\n%s", output.String())
	}
}

func TestSyncPrintsResidualWarningsAndRejectsInvalidTimezone(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(tempDir, "state"))
	codexDir := filepath.Join(tempDir, "codex")
	claudeDir := filepath.Join(tempDir, "claude")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "unknown.jsonl"),
		"{\"timestamp\":\"2026-07-18T09:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"\"}}\n"+
			"{\"timestamp\":\"2026-07-18T10:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":5,\"output_tokens\":2},\"last_token_usage\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n")
	writeTextFixture(t, filepath.Join(claudeDir, "projects", "fixture", "cache.jsonl"),
		"{\"timestamp\":\"2026-07-18T11:00:00Z\",\"type\":\"assistant\",\"message\":{\"id\":\"msg-cache\",\"model\":\"claude-test\",\"usage\":{\"input_tokens\":1,\"cache_creation_input_tokens\":5,\"output_tokens\":1,\"cache_creation\":{\"ephemeral_5m_input_tokens\":1,\"ephemeral_1h_input_tokens\":1}}}}\n")

	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"sync", "--tz", "UTC", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "WARNING: 7 unknown-model tokens") || !strings.Contains(output.String(), "WARNING: 3 unclassified cache-write tokens") {
		t.Fatalf("sync warnings missing:\n%s", output.String())
	}

	cmd = NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"doctor", "--tz", "Mars/Olympus"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "invalid timezone") {
		t.Fatalf("invalid timezone error = %v", err)
	}
}

func writeDoctorFixture(t *testing.T, path string, size int, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set fixture mod time: %v", err)
	}
}

func writeTextFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
