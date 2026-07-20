package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVaultCommandsAndJSONEnvelopes(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	source := filepath.Join(codexDir, "sessions", "2020", "01", "session.jsonl")
	content := []byte("{\"timestamp\":\"2020-01-02T03:04:05Z\",\"type\":\"fixture\"}\n")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, old, old); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))

	base := []string{"--codex-dir", codexDir, "--claude-dir", claudeDir}
	for _, command := range [][]string{
		{"vault", "archive", "--all", "--format", "json"},
		{"vault", "verify", "--deep", "--format", "json"},
		{"vault", "list", "--format", "json"},
		{"vault", "status", "--format", "json"},
		{"vault", "cat", "sessions/2020/01/session.jsonl", "--format", "json"},
	} {
		output := executeVaultCommand(t, append(append([]string{}, base...), command...))
		var envelope struct {
			Schema  string `json:"schema"`
			Command string `json:"command"`
		}
		if err := json.Unmarshal(output, &envelope); err != nil {
			t.Fatalf("%v did not return JSON: %v\n%s", command, err, output)
		}
		if envelope.Schema != reportSchema || !strings.HasPrefix(envelope.Command, "vault ") {
			t.Fatalf("%v envelope = %#v", command, envelope)
		}
	}

	raw := executeVaultCommand(t, append(append([]string{}, base...), "vault", "cat", "sessions/2020/01/session.jsonl"))
	if !bytes.Equal(raw, content) {
		t.Fatalf("raw vault cat = %q", raw)
	}
	status := executeVaultCommand(t, append(append([]string{}, base...), "vault", "status"))
	if !strings.Contains(string(status), "tokenomnom never deletes source files") {
		t.Fatalf("status missing deletion safety statement:\n%s", status)
	}
	var invalidOutput bytes.Buffer
	invalid := NewRootCommand()
	invalid.SetOut(&invalidOutput)
	invalid.SetErr(&invalidOutput)
	invalid.SetArgs(append(append([]string{}, base...), "vault", "list", "--since", "2026-02-01", "--until", "2026-01-01"))
	if err := invalid.Execute(); err == nil || !strings.Contains(err.Error(), "--until must be on or after --since") {
		t.Fatalf("inverted range error = %v", err)
	}
}

func executeVaultCommand(t *testing.T, args []string) []byte {
	t.Helper()
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute %v: %v\n%s", args, err, output.String())
	}
	return append([]byte(nil), output.Bytes()...)
}
