package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
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
	if status.Status != "not_indexed" || status.AutoIndexEnabled || status.AutoInterval != "24h" || strings.Join(status.Providers, ",") != "codex,claude" || status.NextDue != nil || status.SourceDriftAsOf == "" {
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

func TestHistoryStatusAndDoctorReportLiveSourceDrift(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
	first := filepath.Join(codexDir, "sessions", "first.jsonl")
	writeTextFixture(t, first, historyCodexFixture("first", "first prompt"))
	if _, err := executeReport([]string{"history", "index", "--source", "provider"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	unchangedOutput, err := executeReport([]string{"history", "status", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var unchanged jsonHistoryHealth
	if err := json.Unmarshal(decodeEnvelope(t, unchangedOutput).Data, &unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged.ChangedSourcesSinceIndex != 0 || unchanged.NewSourcesSinceIndex != 0 || unchanged.NewestSourceChange != nil || unchanged.SourceDriftAsOf == "" {
		t.Fatalf("unchanged freshness = %+v", unchanged)
	}
	file, err := os.OpenFile(first, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"timestamp":"2026-07-20T12:02:00Z","type":"event_msg","payload":{"type":"user_message","message":"pending append"}}` + "\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "new.jsonl"), historyCodexFixture("new", "new prompt"))

	statusOutput, err := executeReport([]string{"history", "status", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var status jsonHistoryHealth
	if err := json.Unmarshal(decodeEnvelope(t, statusOutput).Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "ready" || status.ChangedSourcesSinceIndex != 2 || status.NewSourcesSinceIndex != 1 || status.NewestSourceChange == nil || status.SourceDriftAsOf == "" {
		t.Fatalf("history drift status = %+v", status)
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
	if doctor.History.ChangedSourcesSinceIndex != 2 || doctor.History.NewSourcesSinceIndex != 1 {
		t.Fatalf("doctor drift status = %+v", doctor.History)
	}
	pretty, err := executeReport([]string{"history", "status", "--no-color"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pretty, "ready (2 sources changed since last index)") {
		t.Fatalf("pretty drift status:\n%s", pretty)
	}
}

func TestParallelHistoryReadCommandsIgnoreWriterLock(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "parallel.jsonl"), historyCodexFixture("parallel", "parallel history read"))
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(stateDir, historystore.DatabaseName)
	release, err := historystore.Lock(historyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	commands := [][]string{
		{"history", "list", "--format", "json"},
		{"history", "search", "parallel", "--format", "json"},
		{"history", "prompts", "--format", "json"},
		{"history", "stats", "--format", "json"},
		{"history", "sample", "--count", "1", "--format", "json"},
		{"history", "status", "--format", "json"},
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, len(commands))
	for _, args := range commands {
		args := args
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := executeReport(args, codexDir, claudeDir)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatalf("parallel history read: %v", err)
		}
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
	if status.Status != "ready" || status.LiveSources != 1 || status.ProviderArchiveSources != 1 || status.LogicalPrompts != 2 || status.UserLogicalPrompts != 2 || status.SearchableUserPrompts != 2 || status.IndexGeneration != 2 {
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

func TestHistoryIndexReportsStableLegacyThreadReclassification(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
	fixture := `{"timestamp":"2026-02-01T12:00:00Z","type":"session_meta","payload":{"id":"legacy-session","timestamp":"2026-02-01T12:00:00Z","cwd":"/workspace/legacy","originator":"codex_cli_rs","cli_version":"0.93.0","source":"exec"}}` + "\n" +
		`{"timestamp":"2026-02-01T12:01:00Z","type":"event_msg","payload":{"type":"user_message","message":"legacy root prompt"}}` + "\n"
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "legacy.jsonl"), fixture)
	initialOutput, err := executeReport([]string{"history", "index", "--source", "provider", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var initial jsonHistoryIndexData
	if err := json.Unmarshal(decodeEnvelope(t, initialOutput).Data, &initial); err != nil {
		t.Fatal(err)
	}
	if initial.ThreadKindDeltas.Root != 1 || initial.ThreadKindDeltas.Subagent != 0 || initial.ThreadKindDeltas.Unknown != 0 {
		t.Fatalf("initial thread-kind summary = %+v", initial.ThreadKindDeltas)
	}
	beforeOutput, err := executeReport([]string{"history", "list", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var before historystore.CatalogPage
	if err := json.Unmarshal(decodeEnvelope(t, beforeOutput).Data, &before); err != nil {
		t.Fatal(err)
	}
	if len(before.Sessions) != 1 || before.Sessions[0].ThreadKind != history.ThreadRoot {
		t.Fatalf("initial legacy session = %+v", before.Sessions)
	}
	sessionID := before.Sessions[0].SessionID

	db, err := sql.Open("sqlite", filepath.Join(stateDir, historystore.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE sessions SET thread_kind='unknown',thread_evidence='',thread_confidence='unknown',thread_rule_version=0`,
		`UPDATE session_thread_supports SET thread_kind='unknown',evidence='',confidence='unknown',rule_version=0`,
		fmt.Sprintf(`UPDATE source_heads SET extractor_version=%d`, history.ExtractorVersion-1),
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"history", "index", "--source", "provider", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var indexed jsonHistoryIndexData
	if err := json.Unmarshal(decodeEnvelope(t, output).Data, &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.RewrittenSources != 1 || indexed.ThreadKindDeltas.Root != 1 || indexed.ThreadKindDeltas.Subagent != 0 || indexed.ThreadKindDeltas.Unknown != -1 {
		t.Fatalf("legacy reclassification summary = %+v", indexed)
	}
	afterOutput, err := executeReport([]string{"history", "list", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var after historystore.CatalogPage
	if err := json.Unmarshal(decodeEnvelope(t, afterOutput).Data, &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Sessions) != 1 || after.Sessions[0].SessionID != sessionID || after.Sessions[0].ThreadKind != history.ThreadRoot {
		t.Fatalf("rebuilt legacy session = %+v, want stable id %s", after.Sessions, sessionID)
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
	for _, args := range [][]string{
		{"history", "sample", "--unit", "occurrence"},
		{"history", "sample", "--strategy", "randomish"},
		{"history", "sample", "--strategy", "stratified"},
		{"history", "sample", "--group-by", "topic"},
		{"history", "sample", "--count", "101"},
	} {
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

func TestSafePrettyTextPreservesLinesAndEscapesTerminalControls(t *testing.T) {
	input := "first\r\nsecond\tvalue\x1b]52;c;clipboard\a\rthird\u009d52;c;again\u009c"
	got := safePrettyText(input)
	if !strings.Contains(got, "first\nsecond\tvalue") || !strings.Contains(got, `\u001b]52;c;clipboard\u0007`+"\nthird") || !strings.Contains(got, `\u009d52;c;again\u009c`) || strings.ContainsAny(got, "\x1b\a\r\u009d\u009c") {
		t.Fatalf("safe text = %q", got)
	}
}

func TestHistorySearchShowPromptsStatsAndRawEndToEnd(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	providerPath := filepath.Join(codexDir, "sessions", "2026", "07", "query.jsonl")
	fixture := historyCodexFixture("query-session", "foo OR bar exact prompt")
	writeTextFixture(t, providerPath, fixture)
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	searchOutput, err := executeReport([]string{"history", "search", "foo OR bar", "--since", "2026-01-01", "--until", "2026-12-31", "--limit", "1", "--all-occurrences", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var search historystore.SearchPage
	searchEnvelope := decodeEnvelope(t, searchOutput)
	if err := json.Unmarshal(searchEnvelope.Data, &search); err != nil {
		t.Fatal(err)
	}
	if len(search.Hits) != 1 || search.Hits[0].Text != nil || len(search.Hits[0].Occurrences) != 1 || search.Hits[0].Rank == nil || len(searchEnvelope.Warnings) != 2 || searchEnvelope.Timezone != localTimezoneName() {
		t.Fatalf("search envelope=%+v page=%+v", searchEnvelope, search)
	}
	promptID, sessionID := search.Hits[0].PromptID, search.Hits[0].SessionID
	emptyOutput, err := executeReport([]string{"history", "search", "definitely absent phrase", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var empty historystore.SearchPage
	if err := json.Unmarshal(decodeEnvelope(t, emptyOutput).Data, &empty); err != nil || empty.Hits == nil || len(empty.Hits) != 0 {
		t.Fatalf("empty search err=%v page=%+v", err, empty)
	}

	showPrompt, err := executeReport([]string{"history", "show", promptID, "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var prompt historystore.PromptResult
	if err := json.Unmarshal(decodeEnvelope(t, showPrompt).Data, &prompt); err != nil || prompt.Text == nil || *prompt.Text != "foo OR bar exact prompt" {
		t.Fatalf("show prompt err=%v value=%+v", err, prompt)
	}

	showSession, err := executeReport([]string{"history", "show", sessionID, "--prompts", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var sessionPrompts historystore.PromptsPage
	if err := json.Unmarshal(decodeEnvelope(t, showSession).Data, &sessionPrompts); err != nil || len(sessionPrompts.Prompts) != 1 || sessionPrompts.Prompts[0].Text == nil {
		t.Fatalf("show session prompts err=%v value=%+v", err, sessionPrompts)
	}

	promptsOutput, err := executeReport([]string{"history", "prompts", "--all-occurrences", "--include-text", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var prompts historystore.PromptsPage
	promptsEnvelope := decodeEnvelope(t, promptsOutput)
	if err := json.Unmarshal(promptsEnvelope.Data, &prompts); err != nil || promptsEnvelope.Timezone != localTimezoneName() || len(prompts.Prompts) != 1 || len(prompts.Prompts[0].Occurrences) != 1 || prompts.Prompts[0].Text == nil {
		t.Fatalf("prompts err=%v value=%+v", err, prompts)
	}

	sampleOutput, err := executeReport([]string{"history", "sample", "--group-by", "month,repo", "--min-length", "10", "--one-per-session", "--all-occurrences", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var sample historystore.SampleResult
	sampleEnvelope := decodeEnvelope(t, sampleOutput)
	if err := json.Unmarshal(sampleEnvelope.Data, &sample); err != nil || sampleEnvelope.Timezone != localTimezoneName() || len(sample.Items) != 1 || sample.Strategy != "stratified" || sample.Items[0].Prompt == nil || sample.Items[0].Prompt.Text != nil || sample.Items[0].Groups["repo"] != "unknown" || len(sample.Items[0].Prompt.Occurrences) != 1 {
		t.Fatalf("sample err=%v envelope=%+v value=%+v", err, sampleEnvelope, sample)
	}
	sampleTextOutput, err := executeReport([]string{"history", "sample", "--count", "1", "--include-text", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(decodeEnvelope(t, sampleTextOutput).Data, &sample); err != nil || sample.Items[0].Prompt.Text == nil {
		t.Fatalf("sample text err=%v value=%+v", err, sample)
	}

	statsOutput, err := executeReport([]string{"history", "stats", "--group-by", "provider", "--top", "1", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var statistics historystore.Statistics
	statsEnvelope := decodeEnvelope(t, statsOutput)
	if err := json.Unmarshal(statsEnvelope.Data, &statistics); err != nil || statsEnvelope.Timezone != localTimezoneName() || statistics.Scope != "searchable_prompt_corpus" || statistics.LogicalSessions != 1 || statistics.LogicalPrompts != 1 || len(statistics.Groups) != 1 || statistics.Groups[0].Values["provider"] != "codex" {
		t.Fatalf("stats err=%v value=%+v", err, statistics)
	}
	prettyStats, err := executeReport([]string{"history", "stats", "--group-by", "provider, provider, repo,"}, codexDir, claudeDir)
	if err != nil || !strings.Contains(prettyStats, "provider=codex,repo=unknown") || strings.Contains(prettyStats, "provider=codex,provider=") || strings.Contains(prettyStats, ",=") {
		t.Fatalf("pretty stats err=%v output=%q", err, prettyStats)
	}

	rawOutput, err := executeReport([]string{"history", "show", sessionID, "--raw"}, codexDir, claudeDir)
	if err != nil || rawOutput != fixture {
		t.Fatalf("raw err=%v\ngot=%q\nwant=%q", err, rawOutput, fixture)
	}
	writeTextFixture(t, providerPath, fixture+historyCodexFixture("query-session", "new prompt"))
	if _, err := executeReport([]string{"history", "show", sessionID, "--raw"}, codexDir, claudeDir); err == nil || !strings.Contains(err.Error(), "changed since indexing") {
		t.Fatalf("stale raw error=%v", err)
	}
}

func TestHistoryAssistantRoleConsentWorkflow(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configDir := filepath.Join(root, "config")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	fixture := historyCodexFixture("roles", "roleword user") +
		`{"timestamp":"2026-07-20T12:00:02Z","type":"response_item","payload":{"type":"message","id":"assistant-1","role":"assistant","content":[{"type":"output_text","text":"roleword assistant proposal"}]}}` + "\n"
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "2026", "07", "roles.jsonl"), fixture)
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	assertNotIndexed := func() {
		t.Helper()
		output, err := executeReport([]string{"history", "search", "roleword", "--role", "assistant", "--format", "json"}, codexDir, claudeDir)
		if err != nil {
			t.Fatal(err)
		}
		envelope := decodeEnvelope(t, output)
		var page historystore.SearchPage
		if json.Unmarshal(envelope.Data, &page) != nil || len(page.Hits) != 0 || len(envelope.Warnings) == 0 || !strings.Contains(envelope.Warnings[0], "assistant content is not indexed") {
			t.Fatalf("assistant disabled envelope=%+v page=%+v", envelope, page)
		}
	}
	assertNotIndexed()
	writeTextFixture(t, filepath.Join(configDir, "config.toml"), "[history]\nindex_assistant = true\n")
	assertNotIndexed()
	if _, err := executeReport([]string{"history", "index", "--source", "provider", "--provider", "codex"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	assertNotIndexed()

	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	output, err := executeReport([]string{"history", "search", "roleword", "--role", "assistant", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var assistant historystore.SearchPage
	if json.Unmarshal(decodeEnvelope(t, output).Data, &assistant) != nil || len(assistant.Hits) != 1 || assistant.Hits[0].Role != history.RoleAssistant {
		t.Fatalf("assistant enabled page=%+v", assistant)
	}
	output, err = executeReport([]string{"history", "search", "roleword", "--role", "any", "--format", "json"}, codexDir, claudeDir)
	var any historystore.SearchPage
	if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &any) != nil || len(any.Hits) != 2 {
		t.Fatalf("any role err=%v page=%+v", err, any)
	}
	pretty, err := executeReport([]string{"history", "search", "roleword", "--role", "any"}, codexDir, claudeDir)
	if err != nil || !strings.Contains(pretty, "\tuser\t") || !strings.Contains(pretty, "\tassistant\t") {
		t.Fatalf("any role pretty output=%q err=%v", pretty, err)
	}
	pretty, err = executeReport([]string{"history", "prompts", "--role", "any"}, codexDir, claudeDir)
	if err != nil || !strings.Contains(pretty, "\tuser\t") || !strings.Contains(pretty, "\tassistant\t") {
		t.Fatalf("any role prompts output=%q err=%v", pretty, err)
	}

	writeTextFixture(t, filepath.Join(configDir, "config.toml"), "[history]\nindex_assistant = false\n")
	assertNotIndexed()
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	statusOutput, err := executeReport([]string{"history", "status", "--format", "json"}, codexDir, claudeDir)
	var status jsonHistoryHealth
	if err != nil || json.Unmarshal(decodeEnvelope(t, statusOutput).Data, &status) != nil || status.AssistantIndexed || status.AssistantLogicalPrompts != 0 {
		t.Fatalf("assistant prune err=%v status=%+v", err, status)
	}
}

func TestHistoryPromptKindFilterLeavesControlOutsideDefaultCorpus(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
	fixture := historyCodexFixture("prompt-kinds", "<heartbeat>controlword complete</heartbeat>")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "2026", "07", "prompt-kinds.jsonl"), fixture)
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	read := func(args ...string) historystore.SearchPage {
		t.Helper()
		output, err := executeReport(append([]string{"history", "search", "controlword", "--format", "json"}, args...), codexDir, claudeDir)
		if err != nil {
			t.Fatal(err)
		}
		var page historystore.SearchPage
		if err := json.Unmarshal(decodeEnvelope(t, output).Data, &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	if page := read(); len(page.Hits) != 0 {
		t.Fatalf("default corpus included control prompt: %+v", page)
	}
	page := read("--prompt-kind", "control")
	if len(page.Hits) != 1 || page.Hits[0].PromptKind != history.PromptKindControl {
		t.Fatalf("control filter page=%+v", page)
	}
}

func TestHistoryRawFallsBackToExactVaultSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	providerPath := filepath.Join(codexDir, "sessions", "2026", "07", "vaulted.jsonl")
	fixture := historyCodexFixture("vaulted-session", "vaulted exact prompt")
	writeTextFixture(t, providerPath, fixture)
	if _, err := executeReport([]string{"vault", "archive", "--all"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	searchOutput, err := executeReport([]string{"history", "search", "vaulted exact", "--all-occurrences", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var search historystore.SearchPage
	if err := json.Unmarshal(decodeEnvelope(t, searchOutput).Data, &search); err != nil || len(search.Hits) != 1 || len(search.Hits[0].PreservedSnapshotIDs) != 1 {
		t.Fatalf("vault search err=%v value=%+v", err, search)
	}
	writeTextFixture(t, providerPath, fixture+historyCodexFixture("vaulted-session", "changed"))
	var fallbackOutput, fallbackErrors bytes.Buffer
	fallbackCommand := NewRootCommand()
	fallbackCommand.SetOut(&fallbackOutput)
	fallbackCommand.SetErr(&fallbackErrors)
	fallbackCommand.SetArgs([]string{"history", "show", search.Hits[0].SessionID, "--raw", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	err = fallbackCommand.Execute()
	if err != nil || fallbackOutput.String() != fixture || !strings.Contains(fallbackErrors.String(), "changed since indexing") {
		t.Fatalf("vault fallback raw err=%v\nstdout=%q\nstderr=%q\nwant=%q", err, fallbackOutput.String(), fallbackErrors.String(), fixture)
	}
	rawOutput, err := executeReport([]string{"history", "show", search.Hits[0].SessionID, "--raw", "--snapshot", search.Hits[0].PreservedSnapshotIDs[0], "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var raw struct {
		Encoding string  `json:"encoding"`
		Content  *string `json:"content"`
	}
	if err := json.Unmarshal(decodeEnvelope(t, rawOutput).Data, &raw); err != nil || raw.Encoding != "utf-8" || raw.Content == nil || *raw.Content != fixture {
		t.Fatalf("vault raw err=%v value=%+v", err, raw)
	}
}

func TestHistoryShowRejectsSessionPaginationWithoutPrompts(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	for _, args := range [][]string{
		{"history", "show", "ses_00000000000000000000000000000000", "--limit", "1"},
		{"history", "show", "ses_00000000000000000000000000000000", "--cursor", "v1:invalid"},
	} {
		if _, err := executeReport(args, filepath.Join(root, "codex"), filepath.Join(root, "claude")); err == nil || !strings.Contains(err.Error(), "require --prompts") {
			t.Fatalf("show args %v error=%v", args, err)
		}
	}
}

func TestHistoryRepositoryFiltersCoverageAndGrouping(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
	fixture := `{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"repository-session","thread_source":"user","git":{"repository_url":"git@github.com:janiorvalle/tokenomnom.git"}}}` + "\n" +
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"repository filter prompt"}}` + "\n"
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "repository.jsonl"), fixture)
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"history", "search", "repository filter", "--repo", "tokenomnom", "--format", "json"}, codexDir, claudeDir)
	var search historystore.SearchPage
	if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &search) != nil || len(search.Hits) != 1 || search.Hits[0].RepositoryName == nil || *search.Hits[0].RepositoryName != "tokenomnom" || search.Coverage.Repository.Known != 1 {
		t.Fatalf("repository search err=%v page=%+v", err, search)
	}

	output, err = executeReport([]string{"history", "stats", "--group-by", "repo", "--format", "json"}, codexDir, claudeDir)
	var stats historystore.Statistics
	if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &stats) != nil {
		t.Fatal(err)
	}
	found := false
	for _, group := range stats.Groups {
		found = found || group.Values["repo"] == "tokenomnom" && group.LogicalPrompts == 1
	}
	if !found || stats.Coverage.Repository.Known != 1 || stats.Coverage.Repository.Unknown != 0 {
		t.Fatalf("repository statistics = %+v", stats)
	}
}

func TestHistoryCommandsUseOneEffectiveTimezone(t *testing.T) {
	tests := []struct {
		name     string
		tzEnv    string
		flag     string
		wantZone string
	}{
		{name: "explicit", tzEnv: "America/Los_Angeles", flag: "Asia/Tokyo", wantZone: "Asia/Tokyo"},
		{name: "default", tzEnv: "America/Los_Angeles", wantZone: "America/Los_Angeles"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("TZ", test.tzEnv)
			t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
			t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
			t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
			codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
			writeTextFixture(t, filepath.Join(codexDir, "sessions", "timezone.jsonl"), historyCodexFixture("timezone-session", "timezone prompt"))
			if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
				t.Fatal(err)
			}
			args := func(values ...string) []string {
				result := append([]string{}, values...)
				result = append(result, "--format", "json")
				if test.flag != "" {
					result = append(result, "--tz", test.flag)
				}
				return result
			}
			zone, err := time.LoadLocation(test.wantZone)
			if err != nil {
				t.Fatal(err)
			}
			expected := func(value string) string {
				parsed, err := time.Parse(time.RFC3339Nano, value)
				if err != nil {
					t.Fatal(err)
				}
				return parsed.In(zone).Format(time.RFC3339Nano)
			}
			decode := func(command []string, target any) {
				t.Helper()
				output, err := executeReport(command, codexDir, claudeDir)
				if err != nil {
					t.Fatal(err)
				}
				envelope := decodeEnvelope(t, output)
				if envelope.Timezone != test.wantZone {
					t.Fatalf("%s timezone = %q, want %q", envelope.Command, envelope.Timezone, test.wantZone)
				}
				if err := json.Unmarshal(envelope.Data, target); err != nil {
					t.Fatal(err)
				}
			}

			var status jsonHistoryHealth
			decode(args("history", "status"), &status)
			if status.CoverageFirst == nil || *status.CoverageFirst != expected("2026-07-20T12:00:00Z") {
				t.Fatalf("status coverage = %v", status.CoverageFirst)
			}
			var list historystore.CatalogPage
			decode(args("history", "list"), &list)
			if len(list.Sessions) != 1 || list.Sessions[0].FirstTimestamp == nil || *list.Sessions[0].FirstTimestamp != expected("2026-07-20T12:00:00Z") {
				t.Fatalf("list = %+v", list)
			}
			var search historystore.SearchPage
			decode(args("history", "search", "timezone prompt"), &search)
			if len(search.Hits) != 1 || search.Hits[0].Timestamp == nil || *search.Hits[0].Timestamp != expected("2026-07-20T12:00:01Z") {
				t.Fatalf("search = %+v", search)
			}
			var stats historystore.Statistics
			decode(args("history", "stats"), &stats)
			if stats.Coverage.FirstTimestamp == nil || *stats.Coverage.FirstTimestamp != expected("2026-07-20T12:00:01Z") {
				t.Fatalf("stats coverage = %+v", stats.Coverage)
			}
			var sample historystore.SampleResult
			decode(args("history", "sample", "--count", "1"), &sample)
			if len(sample.Items) != 1 || sample.Items[0].Prompt == nil || sample.Items[0].Prompt.Timestamp == nil || *sample.Items[0].Prompt.Timestamp != expected("2026-07-20T12:00:01Z") {
				t.Fatalf("sample = %+v", sample)
			}
			var shown historystore.CatalogSession
			decode(args("history", "show", list.Sessions[0].SessionID), &shown)
			if shown.FirstTimestamp == nil || *shown.FirstTimestamp != expected("2026-07-20T12:00:00Z") {
				t.Fatalf("show = %+v", shown)
			}
		})
	}
}

func historyCodexFixture(sessionID, prompt string) string {
	return `{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"` + sessionID + `","thread_source":"user","source":"cli"}}` + "\n" +
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"` + prompt + `"}}` + "\n"
}

func TestHistoryThreadKindTruthTable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "2026", "07", "root.jsonl"), historyCodexFixture("root-session", "threadtest root"))
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "2026", "07", "subagent.jsonl"),
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"child-session","parent_thread_id":"root-session","source":{"subagent":{"thread_spawn":{"parent_thread_id":"root-session","depth":1}}}}}`+"\n"+
			`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"threadtest delegated"}}`+"\n")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "2026", "07", "unknown.jsonl"),
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"unknown-session","source":"cli"}}`+"\n"+
			`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"threadtest unknown"}}`+"\n")
	if _, err := executeReport([]string{"history", "index"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name  string
		flags []string
		want  int
		kind  history.ThreadKind
	}{
		{name: "default", want: 3},
		{name: "all", flags: []string{"--thread-kind", "all"}, want: 3},
		{name: "root-only", flags: []string{"--root-only"}, want: 1, kind: history.ThreadRoot},
		{name: "root", flags: []string{"--thread-kind", "root"}, want: 1, kind: history.ThreadRoot},
		{name: "subagent", flags: []string{"--thread-kind", "subagent"}, want: 1, kind: history.ThreadSubagent},
		{name: "unknown", flags: []string{"--thread-kind", "unknown"}, want: 1, kind: history.ThreadUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			listArgs := append([]string{"history", "list", "--format", "json"}, test.flags...)
			output, err := executeReport(listArgs, codexDir, claudeDir)
			var list historystore.CatalogPage
			if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &list) != nil || len(list.Sessions) != test.want {
				t.Fatalf("list flags=%v err=%v page=%+v", test.flags, err, list)
			}
			searchArgs := append([]string{"history", "search", "threadtest", "--format", "json"}, test.flags...)
			output, err = executeReport(searchArgs, codexDir, claudeDir)
			var search historystore.SearchPage
			if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &search) != nil || len(search.Hits) != test.want {
				t.Fatalf("search flags=%v err=%v page=%+v", test.flags, err, search)
			}
			promptArgs := append([]string{"history", "prompts", "--format", "json"}, test.flags...)
			output, err = executeReport(promptArgs, codexDir, claudeDir)
			var prompts historystore.PromptsPage
			if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &prompts) != nil || len(prompts.Prompts) != test.want {
				t.Fatalf("prompts flags=%v err=%v page=%+v", test.flags, err, prompts)
			}
			statsArgs := append([]string{"history", "stats", "--format", "json"}, test.flags...)
			output, err = executeReport(statsArgs, codexDir, claudeDir)
			var stats historystore.Statistics
			if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &stats) != nil || stats.LogicalSessions != test.want {
				t.Fatalf("stats flags=%v err=%v stats=%+v", test.flags, err, stats)
			}
			if test.kind != "" && (list.Sessions[0].ThreadKind != test.kind || search.Hits[0].ThreadKind != test.kind || prompts.Prompts[0].ThreadKind != test.kind) {
				t.Fatalf("thread kind list=%q search=%q prompts=%q want=%q", list.Sessions[0].ThreadKind, search.Hits[0].ThreadKind, prompts.Prompts[0].ThreadKind, test.kind)
			}
			if list.Coverage.Repository.Unknown != test.want || search.Coverage.Repository.Unknown != test.want ||
				prompts.Coverage.Repository.Unknown != test.want || stats.Coverage.Repository.Unknown != test.want {
				t.Fatalf("metadata coverage list=%+v search=%+v prompts=%+v stats=%+v want=%d", list.Coverage, search.Coverage, prompts.Coverage, stats.Coverage, test.want)
			}
			if list.Coverage.ThreadKind.Root != 1 || list.Coverage.ThreadKind.Subagent != 1 || list.Coverage.ThreadKind.Unknown != 1 {
				t.Fatalf("thread coverage should disclose the full selected corpus: %+v", list.Coverage.ThreadKind)
			}
			if test.kind == history.ThreadSubagent && (len(list.Sessions[0].Relationships) != 1 ||
				list.Sessions[0].Relationships[0].ResolutionState != history.ResolutionResolved ||
				list.Sessions[0].Relationships[0].Evidence != "session_meta.source.subagent") {
				t.Fatalf("subagent JSON relationship=%+v", list.Sessions[0].Relationships)
			}
		})
	}

	output, err := executeReport([]string{"history", "stats", "--group-by", "thread-kind", "--format", "json"}, codexDir, claudeDir)
	var grouped historystore.Statistics
	if err != nil || json.Unmarshal(decodeEnvelope(t, output).Data, &grouped) != nil || len(grouped.Groups) != 3 {
		t.Fatalf("thread-kind groups err=%v stats=%+v", err, grouped)
	}
	for _, command := range [][]string{{"history", "list"}, {"history", "search", "threadtest"}, {"history", "prompts"}, {"history", "stats"}} {
		args := append(command, "--root-only", "--thread-kind", "all")
		if _, err := executeReport(args, codexDir, claudeDir); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Fatalf("flags %v error=%v", args, err)
		}
	}
	if _, err := executeReport([]string{"history", "list", "--include-subagents"}, codexDir, claudeDir); err == nil {
		t.Fatal("removed --include-subagents flag unexpectedly exists")
	}
}
