package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/vault"
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
	var catEnvelope struct {
		Data struct {
			Encoding      string  `json:"encoding"`
			Content       *string `json:"content"`
			ContentBase64 string  `json:"content_base64"`
		} `json:"data"`
	}
	catJSON := executeVaultCommand(t, append(append([]string{}, base...), "vault", "cat", "sessions/2020/01/session.jsonl", "--format", "json"))
	if err := json.Unmarshal(catJSON, &catEnvelope); err != nil {
		t.Fatal(err)
	}
	if catEnvelope.Data.Encoding != "utf-8" || catEnvelope.Data.Content == nil || *catEnvelope.Data.Content != string(content) || catEnvelope.Data.ContentBase64 != base64.StdEncoding.EncodeToString(content) {
		t.Fatalf("UTF-8 cat JSON = %#v", catEnvelope.Data)
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

func TestVaultCatJSONPreservesInvalidUTF8AsBase64(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	source := filepath.Join(codexDir, "sessions", "binary.jsonl")
	content := []byte{0xff, 0xfe, '\n', 0x00, 0x80}
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
	executeVaultCommand(t, append(append([]string{}, base...), "vault", "archive", "--all", "--format", "json"))
	output := executeVaultCommand(t, append(append([]string{}, base...), "vault", "cat", "sessions/binary.jsonl", "--format", "json"))
	var envelope struct {
		Data struct {
			Encoding      string  `json:"encoding"`
			Content       *string `json:"content"`
			ContentBase64 string  `json:"content_base64"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Encoding != "base64" || envelope.Data.Content != nil || envelope.Data.ContentBase64 != base64.StdEncoding.EncodeToString(content) {
		t.Fatalf("binary cat JSON = %#v", envelope.Data)
	}
	raw := executeVaultCommand(t, append(append([]string{}, base...), "vault", "cat", "sessions/binary.jsonl"))
	if !bytes.Equal(raw, content) {
		t.Fatalf("binary raw cat = %v", raw)
	}
}

func TestVaultListPaginationJSONPrettyAndValidation(t *testing.T) {
	root := t.TempDir()
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		source := filepath.Join(codexDir, "sessions", "2020", "01", fmt.Sprintf("session-%d.jsonl", index))
		if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
			t.Fatal(err)
		}
		content := fmt.Sprintf("{\"timestamp\":\"2020-01-0%dT03:04:05Z\",\"type\":\"fixture\"}\n", index+1)
		if err := os.WriteFile(source, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Date(2020, 1, index+2, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(source, old, old); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	base := []string{"--codex-dir", codexDir, "--claude-dir", claudeDir}
	executeVaultCommand(t, append(append([]string{}, base...), "vault", "archive", "--all", "--format", "json"))

	type listEnvelope struct {
		Data struct {
			Files []vault.ListEntry `json:"files"`
			Page  *struct {
				Limit      int    `json:"limit"`
				HasMore    bool   `json:"has_more"`
				NextCursor string `json:"next_cursor"`
			} `json:"page"`
		} `json:"data"`
	}
	var first listEnvelope
	output := executeVaultCommand(t, append(append([]string{}, base...), "vault", "list", "--limit", "1", "--latest", "--format", "json"))
	if err := json.Unmarshal(output, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Data.Files) != 1 || first.Data.Page == nil || first.Data.Page.Limit != 1 || !first.Data.Page.HasMore || !strings.HasPrefix(first.Data.Page.NextCursor, "v1:") {
		t.Fatalf("first page = %#v\n%s", first, output)
	}
	var second listEnvelope
	output = executeVaultCommand(t, append(append([]string{}, base...), "vault", "list", "--latest", "--cursor", first.Data.Page.NextCursor, "--format", "json"))
	if err := json.Unmarshal(output, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Data.Files) != 1 || second.Data.Page == nil || second.Data.Page.Limit != 1 || second.Data.Files[0].SourcePath == first.Data.Files[0].SourcePath {
		t.Fatalf("second page = %#v\n%s", second, output)
	}

	var legacy listEnvelope
	output = executeVaultCommand(t, append(append([]string{}, base...), "vault", "list", "--format", "json"))
	if err := json.Unmarshal(output, &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy.Data.Files) != 3 || legacy.Data.Page != nil {
		t.Fatalf("legacy list = %#v", legacy)
	}
	if bytes.Contains(output, []byte(`"page"`)) {
		t.Fatalf("legacy response unexpectedly added page metadata: %s", output)
	}
	pretty := string(executeVaultCommand(t, append(append([]string{}, base...), "vault", "list", "--limit", "1")))
	if !strings.Contains(pretty, "PROVIDER") || !strings.Contains(pretty, "More results: rerun with the same filters and --cursor v1:") {
		t.Fatalf("pretty page missing provider or continuation:\n%s", pretty)
	}
	var sorted listEnvelope
	output = executeVaultCommand(t, append(append([]string{}, base...), "vault", "list", "--sort", "first_ts", "--format", "json"))
	if err := json.Unmarshal(output, &sorted); err != nil {
		t.Fatal(err)
	}
	if sorted.Data.Page != nil || !strings.HasSuffix(sorted.Data.Files[0].SourcePath, "session-2.jsonl") {
		t.Fatalf("unbounded sorted list = %#v", sorted)
	}
	var empty listEnvelope
	output = executeVaultCommand(t, append(append([]string{}, base...), "vault", "list", "--provider", "claude", "--limit", "10", "--format", "json"))
	if err := json.Unmarshal(output, &empty); err != nil || empty.Data.Files == nil || len(empty.Data.Files) != 0 || empty.Data.Page == nil {
		t.Fatalf("empty page = %#v, %v", empty, err)
	}

	for _, args := range [][]string{{"vault", "list", "--limit", "0"}, {"vault", "list", "--limit", "501"}, {"vault", "list", "--cursor", "bad"}} {
		var commandOutput bytes.Buffer
		cmd := NewRootCommand()
		cmd.SetOut(&commandOutput)
		cmd.SetErr(&commandOutput)
		cmd.SetArgs(append(append([]string{}, base...), args...))
		if err := cmd.Execute(); err == nil {
			t.Fatalf("%v unexpectedly succeeded", args)
		}
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
