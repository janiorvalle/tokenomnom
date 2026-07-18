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
