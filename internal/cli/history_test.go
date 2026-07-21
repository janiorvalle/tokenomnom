package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestHistoryStatusAndDoctorAbsentDoNotCreateIndex(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "missing-codex")
	claudeDir := filepath.Join(root, "missing-claude")

	statusOutput, err := executeReport([]string{"history", "status", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var status jsonHistoryHealth
	if err := json.Unmarshal(decodeEnvelope(t, statusOutput).Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "not_indexed" {
		t.Fatalf("history status = %+v", status)
	}
	historyPath := filepath.Join(stateDir, historystore.DatabaseName)
	if _, err := os.Stat(historyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created history index: %v", err)
	}

	doctorOutput, err := executeReport([]string{"doctor", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var doctor struct {
		History jsonHistoryHealth `json:"history"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, doctorOutput).Data, &doctor); err != nil {
		t.Fatal(err)
	}
	if doctor.History.Status != "not_indexed" {
		t.Fatalf("doctor history = %+v", doctor.History)
	}
	if _, err := os.Stat(historyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("doctor created history index: %v", err)
	}
}

func TestHistoryIndexStatusAndProviderKinds(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "live.jsonl"), historyCodexFixture("live", "hello live"))
	writeTextFixture(t, filepath.Join(codexDir, "archived_sessions", "archive.jsonl"), historyCodexFixture("archive", "hello archive"))

	output, err := executeReport([]string{"history", "index", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	assertEnvelope(t, envelope, "history index")
	var indexed jsonHistoryIndexData
	if err := json.Unmarshal(envelope.Data, &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.NewSources != 2 || indexed.IndexedPrompts != 2 || len(indexed.Errors) != 0 {
		t.Fatalf("index data = %+v", indexed)
	}

	statusOutput, err := executeReport([]string{"history", "status", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var status jsonHistoryHealth
	if err := json.Unmarshal(decodeEnvelope(t, statusOutput).Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "ready" || status.LiveSources != 1 || status.ProviderArchiveSources != 1 || status.LogicalPrompts != 2 || status.IndexGeneration != 2 {
		t.Fatalf("history status = %+v", status)
	}

	second, err := executeReport([]string{"history", "index", "--provider", "codex", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(decodeEnvelope(t, second).Data, &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.SkippedSources != 2 || indexed.IndexedSources != 0 {
		t.Fatalf("unchanged data = %+v", indexed)
	}
}

func TestHistoryPurgeLockAndFileSafety(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	providerPath := filepath.Join(codexDir, "sessions", "keep.jsonl")
	writeTextFixture(t, providerPath, historyCodexFixture("keep", "keep source"))
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	usagePath := filepath.Join(stateDir, store.DatabaseName)
	if err := os.WriteFile(usagePath, []byte("keep usage"), 0o600); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(stateDir, historystore.DatabaseName)
	release, err := historystore.Lock(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, purgeErr := executeReport([]string{"history", "purge"}, codexDir, claudeDir)
	release()
	if !errors.Is(purgeErr, historystore.ErrStoreInUse) {
		t.Fatalf("locked purge error = %v", purgeErr)
	}

	output, err := executeReport([]string{"history", "purge", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"command":"history purge"`) {
		t.Fatalf("purge JSON = %s", output)
	}
	if _, err := os.Stat(historyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("history database still exists: %v", err)
	}
	for _, path := range []string{usagePath, providerPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("purge removed protected file %q: %v", path, err)
		}
	}
}

func TestHistoryRejectsUnsupportedSelectors(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	for _, args := range [][]string{{"history", "index", "--provider", "other"}, {"history", "index", "--source", "all"}} {
		if _, err := executeReport(args, filepath.Join(root, "codex"), filepath.Join(root, "claude")); err == nil {
			t.Fatalf("%v succeeded", args)
		}
	}
}

func historyCodexFixture(sessionID, prompt string) string {
	return `{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `"}}` + "\n" +
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"` + prompt + `"}}` + "\n"
}
