package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
)

func TestHistoryCostsPricesExactCodexTranscript(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	fixture := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"cost-session","thread_source":"user","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"turn_context","payload":{"model":"gpt-5.2"}}`,
		`{"timestamp":"2026-07-20T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000,"cached_input_tokens":20000,"cache_write_input_tokens":0,"output_tokens":30000,"reasoning_output_tokens":4000,"total_tokens":130000},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":20000,"cache_write_input_tokens":0,"output_tokens":30000,"reasoning_output_tokens":4000,"total_tokens":130000}}}}`,
		`{"timestamp":"2026-07-20T12:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"cost prompt"}}`,
	}, "\n") + "\n"
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "cost.jsonl"), fixture)
	if _, err := executeReport([]string{"history", "index", "--source", "provider", "--format", "json"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"history", "costs", "--limit", "1", "--cwd", "/repo", "--project", "repo", "--source", "provider", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	assertEnvelope(t, envelope, "history costs")
	if envelope.Filters.CWD == nil || *envelope.Filters.CWD != "/repo" || envelope.Filters.Project == nil || *envelope.Filters.Project != "repo" || envelope.Filters.Source == nil || *envelope.Filters.Source != "provider" || envelope.Filters.Limit == nil || *envelope.Filters.Limit != 1 {
		t.Fatalf("history cost filters = %+v", envelope.Filters)
	}
	var data historySessionCostData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 1 {
		t.Fatalf("session cost data = %+v", data)
	}
	row := data.Sessions[0]
	if row.Provider != "codex" || row.AttributionStatus != "complete" || row.RawLocationKind != "provider_live" || row.Tokens.InputTokens != 100000 || row.Tokens.CacheReadTokens != 20000 || row.Tokens.OutputTokens != 30000 || row.Tokens.TotalTokens != 130000 || row.Tokens.CostUSD != 0.56 || row.Tokens.PricedTokens != 130000 || row.Tokens.UnpricedTokens != 0 {
		t.Fatalf("session cost row = %+v", row)
	}
	if len(row.Models) != 1 || row.Models[0].Model != "gpt-5.2" || row.Models[0].Date != "2026-07-20" {
		t.Fatalf("session cost model rows = %+v", row.Models)
	}
	if data.Page.Limit != 1 || data.Generation == 0 || data.Bounds.DefaultSessionsPerPage != 20 || data.Bounds.MaxSessionsPerPage != 100 || !strings.Contains(data.Bounds.NapkinMath, "12 pages") {
		t.Fatalf("session cost bounds/page = %+v / %+v", data.Bounds, data.Page)
	}
}

func TestHistoryCostsSettledMissingSourceStopsWarning(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	sourcePath := filepath.Join(codexDir, "sessions", "settle-missing.jsonl")
	fixture := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"settle-missing","thread_source":"user","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"settle this missing source"}}`,
	}, "\n") + "\n"
	writeTextFixture(t, sourcePath, fixture)
	if _, err := executeReport([]string{"history", "index", "--source", "provider", "--provider", "codex", "--format", "json"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	if _, err := executeReport([]string{"history", "index", "--source", "provider", "--provider", "codex", "--format", "json"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}
	beforeOutput, err := executeReport([]string{"history", "costs", "--provider", "codex", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	before := decodeEnvelope(t, beforeOutput)
	if len(before.Warnings) == 0 || !strings.Contains(strings.Join(before.Warnings, " "), "no available exact transcript location") {
		t.Fatalf("missing-source warning before settlement = %+v", before.Warnings)
	}
	var beforeData historySessionCostData
	if err := json.Unmarshal(before.Data, &beforeData); err != nil {
		t.Fatal(err)
	}
	if len(beforeData.Sessions) != 1 || beforeData.Sessions[0].AttributionStatus != "unavailable" {
		t.Fatalf("unsettled cost row = %+v", beforeData.Sessions)
	}

	settledOutput, err := executeReport([]string{"history", "index", "--source", "provider", "--provider", "codex", "--settle-missing", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var settledIndex jsonHistoryIndexData
	if err := json.Unmarshal(decodeEnvelope(t, settledOutput).Data, &settledIndex); err != nil {
		t.Fatal(err)
	}
	if settledIndex.SettledMissingSources != 1 {
		t.Fatalf("settlement receipt = %+v", settledIndex)
	}

	afterOutput, err := executeReport([]string{"history", "costs", "--provider", "codex", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	after := decodeEnvelope(t, afterOutput)
	if len(after.Warnings) != 0 {
		t.Fatalf("settled cost warnings = %+v", after.Warnings)
	}
	var afterData historySessionCostData
	if err := json.Unmarshal(after.Data, &afterData); err != nil {
		t.Fatal(err)
	}
	if len(afterData.Sessions) != 1 || afterData.Sessions[0].AttributionStatus != "settled_missing" || !afterData.Sessions[0].MissingSourceSettled || afterData.Sessions[0].TokenSource != "settled_missing" || len(afterData.Sessions[0].Warnings) != 0 {
		t.Fatalf("settled cost row = %+v", afterData.Sessions)
	}

	statusOutput, err := executeReport([]string{"history", "status", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var status jsonHistoryHealth
	if err := json.Unmarshal(decodeEnvelope(t, statusOutput).Data, &status); err != nil {
		t.Fatal(err)
	}
	if status.MissingSources != 1 || status.SettledMissingSources != 1 || status.UnsettledMissingSources != 0 || status.Status != "ready" {
		t.Fatalf("settled history status = %+v", status)
	}
}

func TestHistoryIndexSettleMissingRejectsVaultScope(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	_, err := executeReport([]string{"history", "index", "--source", "vault", "--settle-missing"}, filepath.Join(root, "codex"), filepath.Join(root, "claude"))
	if err == nil || !strings.Contains(err.Error(), "history_index_settle_missing_scope") || !strings.Contains(err.Error(), "--source provider") {
		t.Fatalf("settle scope error = %v", err)
	}
}

func TestHistoryCostsDefaultLimitIsBoundedAndDocumented(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))

	command := newHistoryCostsCommand(nil, nil)
	flag := command.Flags().Lookup("limit")
	if flag == nil || flag.DefValue != "20" || flag.Usage != "maximum page rows (1-100, default 20)" {
		t.Fatalf("history costs limit flag = %+v", flag)
	}
}

func TestHistoryCostsLimitErrorExplainsBound(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	_, err := executeReport([]string{"history", "costs", "--limit", "101"}, filepath.Join(root, "codex"), filepath.Join(root, "claude"))
	if err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
		t.Fatalf("history costs limit error = %v", err)
	}
}

func TestHistoryCostsPricesClaudeCacheBuckets(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	fixture := strings.Join([]string{
		`{"type":"user","uuid":"user-1","sessionId":"claude-cost","cwd":"/repo","timestamp":"2026-07-20T12:00:00Z","message":{"role":"user","content":"cost prompt"}}`,
		`{"type":"assistant","uuid":"assistant-1","sessionId":"claude-cost","cwd":"/repo","timestamp":"2026-07-20T12:00:01Z","message":{"id":"assistant-1","role":"assistant","model":"claude-sonnet-5","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":100000,"cache_read_input_tokens":20000,"cache_creation_input_tokens":10000,"output_tokens":30000,"cache_creation":{"ephemeral_5m_input_tokens":5000,"ephemeral_1h_input_tokens":2000}}}}`,
	}, "\n") + "\n"
	writeTextFixture(t, filepath.Join(claudeDir, "projects", "fixture", "claude-cost.jsonl"), fixture)
	if _, err := executeReport([]string{"history", "index", "--source", "provider", "--provider", "claude", "--format", "json"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"history", "costs", "--provider", "claude", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var data historySessionCostData
	if err := json.Unmarshal(decodeEnvelope(t, output).Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Sessions) != 1 {
		t.Fatalf("Claude session cost data = %+v", data)
	}
	tokens := data.Sessions[0].Tokens
	if data.Sessions[0].AttributionStatus != "complete" || tokens.CacheWrite5mTokens != 5000 || tokens.CacheWrite1hTokens != 2000 || tokens.CacheWriteUnclassifiedTokens != 3000 || tokens.CacheWriteTokens != 10000 || tokens.CostUSD != 0.54 {
		t.Fatalf("Claude session cost = %+v", data.Sessions[0])
	}
}

func TestAggregateHistoryUsageKeepsUnknownDateTokens(t *testing.T) {
	known, unknown, warnings := aggregateHistoryUsage([]ingest.UsageEvent{
		{Timestamp: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), Provider: discover.ProviderCodex, Model: "gpt-5.2", Input: 100, Output: 20},
		{Provider: discover.ProviderCodex, Model: "gpt-5.2", Input: 7, Output: 3},
	}, history.ProviderCodex)
	if len(known) != 1 || known[0].Input != 100 || len(unknown) != 1 || unknown[0].Date != "" || unknown[0].Input != 7 || unknown[0].Output != 3 {
		t.Fatalf("known=%+v unknown=%+v", known, unknown)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "10 tokens remain visible") {
		t.Fatalf("warnings=%+v", warnings)
	}
}

func TestHistoryUsageEventsForWindowWarnsAboutExcludedUnknownDates(t *testing.T) {
	since := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	until := since.Add(24*time.Hour - time.Nanosecond)
	events, warnings := historyUsageEventsForWindow([]ingest.UsageEvent{
		{Timestamp: since.Add(12 * time.Hour), Input: 100, Output: 20},
		{Timestamp: since.AddDate(0, 0, 1), Input: 200, Output: 30},
		{Input: 7, Output: 3},
	}, since, until)

	if len(events) != 1 || events[0].Input != 100 || events[0].Output != 20 {
		t.Fatalf("events=%+v", events)
	}
	if len(warnings) != 1 || warnings[0] != "1 token-usage record had no timestamp; 10 tokens are excluded from day-scoped totals" {
		t.Fatalf("warnings=%+v", warnings)
	}
}
