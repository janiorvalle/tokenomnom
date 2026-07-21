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

func TestDoctorReportsCorruptOptionalHistoryIndexWithoutAborting(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, historystore.DatabaseName), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := executeReport([]string{"doctor", "--format", "json"}, filepath.Join(root, "codex"), filepath.Join(root, "claude"))
	if err != nil {
		t.Fatalf("doctor aborted on corrupt optional history index: %v\n%s", err, output)
	}
	var doctor struct {
		History jsonHistoryHealth `json:"history"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, output).Data, &doctor); err != nil {
		t.Fatal(err)
	}
	if doctor.History.Status != "error" || doctor.History.InspectionError == nil || *doctor.History.InspectionError == "" {
		t.Fatalf("doctor corrupt history = %+v", doctor.History)
	}
	statusOutput, err := executeReport([]string{"history", "status", "--format", "json"}, filepath.Join(root, "codex"), filepath.Join(root, "claude"))
	if err != nil {
		t.Fatalf("status aborted on corrupt history index: %v\n%s", err, statusOutput)
	}
	var status jsonHistoryHealth
	if err := json.Unmarshal(decodeEnvelope(t, statusOutput).Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "error" || status.InspectionError == nil {
		t.Fatalf("corrupt history status = %+v", status)
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

func TestHistoryDefaultIndexIncludesProviderAndVaultAndListsOnce(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	path := filepath.Join(codexDir, "sessions", "2026", "07", "shared.jsonl")
	writeTextFixture(t, path, historyCodexFixture("shared", "one logical prompt"))
	if _, err := executeReport([]string{"vault", "archive", "--all"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"history", "index", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var indexed jsonHistoryIndexData
	if err := json.Unmarshal(decodeEnvelope(t, output).Data, &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.Source != "all" || indexed.NewSources != 1 || indexed.IndexedVaultBundles != 1 || indexed.IndexedVaultVersions != 1 || indexed.IndexedPrompts != 2 || indexed.ErrorCount != 0 {
		t.Fatalf("combined index = %+v", indexed)
	}

	listOutput, err := executeReport([]string{"history", "list", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var page historystore.CatalogPage
	if err := json.Unmarshal(decodeEnvelope(t, listOutput).Data, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].LogicalPromptCount != 1 || page.Sessions[0].OccurrenceCount != 2 || !page.Sessions[0].Availability.ExactLiveAndVaulted || !strings.HasPrefix(page.Sessions[0].SessionID, "ses_") || len(page.Sessions[0].PreservedSnapshotIDs) != 1 {
		t.Fatalf("combined catalog = %+v", page)
	}

	second, err := executeReport([]string{"history", "index", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(decodeEnvelope(t, second).Data, &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.IndexedSources != 0 || indexed.SkippedSources != 1 || indexed.IndexedVaultBundles != 0 || indexed.SkippedVaultBundles != 1 || indexed.IndexedVaultVersions != 0 {
		t.Fatalf("idempotent combined index = %+v", indexed)
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
	for _, args := range [][]string{{"history", "index", "--provider", "other"}, {"history", "index", "--source", "other"}} {
		if _, err := executeReport(args, filepath.Join(root, "codex"), filepath.Join(root, "claude")); err == nil {
			t.Fatalf("%v succeeded", args)
		}
	}
}

func TestSafePrettyPreviewEscapesTerminalControls(t *testing.T) {
	input := "hello\x1b]52;c;clipboard\a\rnext\b"
	got := safePrettyPreview(input)
	if strings.ContainsAny(got, "\x1b\a\b\r") || !strings.Contains(got, `\u001b]52;c;clipboard\u0007 next\u0008`) {
		t.Fatalf("safe preview = %q", got)
	}
}

func historyCodexFixture(sessionID, prompt string) string {
	return `{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `"}}` + "\n" +
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"` + prompt + `"}}` + "\n"
}
