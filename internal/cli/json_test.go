package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/store"
)

type decodedEnvelope struct {
	Schema      string          `json:"schema"`
	Command     string          `json:"command"`
	GeneratedAt string          `json:"generated_at"`
	Timezone    string          `json:"timezone"`
	Filters     jsonFilters     `json:"filters"`
	Disclaimer  string          `json:"disclaimer"`
	Warnings    []string        `json:"warnings"`
	Data        json.RawMessage `json:"data"`
}

func TestJSONEnvelopeForEveryCommand(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	commands := []struct {
		name string
		args []string
	}{
		{"summary", []string{"summary", "--format", "json", "--no-sync"}},
		{"daily", []string{"daily", "--format", "json", "--no-sync"}},
		{"monthly", []string{"monthly", "--format", "json", "--no-sync"}},
		{"models", []string{"models", "--format", "json", "--no-sync"}},
		{"heatmap", []string{"heatmap", "--year", "2026", "--format", "json", "--no-sync"}},
		{"pricing", []string{"pricing", "--format", "json"}},
		{"doctor", []string{"doctor", "--format", "json"}},
		{"export", []string{"export", "--format", "json", "--no-sync"}},
		{"install-skill", []string{"install-skill", "--format", "json"}},
	}
	for _, test := range commands {
		t.Run(test.name, func(t *testing.T) {
			output, err := executeReport(test.args, codexDir, claudeDir)
			if err != nil {
				t.Fatal(err)
			}
			envelope := decodeEnvelope(t, output)
			assertEnvelope(t, envelope, test.name)
		})
	}
}

func TestSyncJSONEnvelopeAndWarnings(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "unknown.jsonl"),
		"{\"timestamp\":\"2026-07-18T09:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"\"}}\n"+
			"{\"timestamp\":\"2026-07-18T10:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":5,\"output_tokens\":2},\"last_token_usage\":{\"input_tokens\":5,\"output_tokens\":2}}}}\n")
	output, err := executeReport([]string{"sync", "--format", "json", "--tz", "UTC"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	assertEnvelope(t, envelope, "sync")
	if envelope.Timezone != "UTC" || len(envelope.Warnings) == 0 {
		t.Fatalf("sync envelope missing timezone/warnings: %+v", envelope)
	}
	var data jsonSyncData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.UnknownModelTokens != 7 || data.FilesScanned != 1 {
		t.Fatalf("sync data = %+v", data)
	}
}

func TestJSONEmptyResultsAreEnvelopes(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "missing-state"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir := filepath.Join(root, "missing-codex")
	claudeDir := filepath.Join(root, "missing-claude")

	for _, command := range []string{"summary", "daily", "monthly", "models", "heatmap", "export"} {
		args := []string{command, "--format", "json"}
		if command != "summary" {
			args = append(args, "--no-sync")
		}
		output, err := executeReport(args, codexDir, claudeDir)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		envelope := decodeEnvelope(t, output)
		assertEnvelope(t, envelope, command)
		if strings.Contains(output, noProviderHint) || strings.Contains(output, noUsageMessage) {
			t.Fatalf("%s JSON contains pretty message: %s", command, output)
		}
	}
}

func TestDailyJSONMatchesSeededPrettyNumbersAndOrdering(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	output, err := executeReport([]string{"daily", "--format", "json", "--no-sync", "--last", "3"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	var data struct {
		Rows []jsonDailyRow `json:"rows"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 3 || data.Rows[0].Date != "2026-01-31" || data.Rows[2].Date != "2026-02-03" {
		t.Fatalf("daily rows not stably ordered: %+v", data.Rows)
	}
	if data.Rows[1].InputTokens != 200_300 || data.Rows[1].TotalTokens != 205_350 || data.Rows[1].CostUSD != 0.18 {
		t.Fatalf("daily JSON numbers differ from pretty table: %+v", data.Rows[1])
	}
}

func TestSummaryJSONDuplicatesDiagnosticsAsWarnings(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	output, err := executeReport([]string{"summary", "--format", "json", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	var data jsonSummaryData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.UnpricedTokens != 1_910 || data.UnclassifiedCacheWriteTokens != 5 || data.UnknownModelTokens != 350 {
		t.Fatalf("summary diagnostics = %+v", data)
	}
	joined := strings.Join(envelope.Warnings, "\n")
	for _, fragment := range []string{"Unpriced tokens", "unclassified cache-write", "unknown model"} {
		if !strings.Contains(joined, fragment) {
			t.Errorf("warnings missing %q: %v", fragment, envelope.Warnings)
		}
	}
}

func TestModelsAndPricingJSONValues(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	output, err := executeReport([]string{"models", "--format", "json", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	modelsEnvelope := decodeEnvelope(t, output)
	var modelsData jsonModelsData
	if err := json.Unmarshal(modelsEnvelope.Data, &modelsData); err != nil {
		t.Fatal(err)
	}
	if len(modelsData.Rows) != 4 || modelsData.Rows[0].Model != "gpt-5.2" {
		t.Fatalf("model ordering = %+v", modelsData.Rows)
	}
	top := modelsData.Rows[0]
	if top.Share != 99.1 || top.CostShare == nil || *top.CostShare != 100 || !top.Priced {
		t.Fatalf("priced model JSON = %+v", top)
	}
	var unpriced *jsonModelRow
	for index := range modelsData.Rows {
		if modelsData.Rows[index].Model == "gpt-small" {
			unpriced = &modelsData.Rows[index]
		}
	}
	if unpriced == nil || unpriced.Priced || unpriced.CostShare != nil || unpriced.CostUSD != 0 {
		t.Fatalf("unpriced model JSON = %+v", unpriced)
	}

	output, err = executeReport([]string{"pricing", "--format", "json"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	pricingEnvelope := decodeEnvelope(t, output)
	var pricingData jsonPricingData
	if err := json.Unmarshal(pricingEnvelope.Data, &pricingData); err != nil {
		t.Fatal(err)
	}
	var gpt52 *jsonPricingEntry
	for _, model := range pricingData.Models {
		if model.Model == "gpt-5.2" && len(model.Entries) == 1 {
			gpt52 = &model.Entries[0]
		}
	}
	if gpt52 == nil || gpt52.BaseInput == nil || *gpt52.BaseInput != 1.75 || gpt52.CacheRead == nil || *gpt52.CacheRead != 0.175 || gpt52.Write5m != nil || gpt52.Output == nil || *gpt52.Output != 14 {
		t.Fatalf("gpt-5.2 pricing JSON = %+v", gpt52)
	}
}

func TestJSONFreshnessFailureStaysInsideWarnings(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *store.Tx) error { return tx.SetMeta("pending_timezone", "Pacific/Honolulu") }); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"summary", "--format", "json", "--tz", "UTC"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	if !strings.Contains(strings.Join(envelope.Warnings, "\n"), "incomplete timezone migration") {
		t.Fatalf("freshness warning missing: %v", envelope.Warnings)
	}
}

func TestNoSyncDoesNotRelabelStoredLocalTimezone(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *store.Tx) error { return tx.SetMeta("timezone", "Local") }); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Setenv("TZ", "America/New_York")
	got, err := reportTimezone(database, "Asia/Tokyo")
	database.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got != "America/New_York" {
		t.Fatalf("stored Local timezone was relabeled as requested zone: %q", got)
	}
}

func TestFormatValidationListsValidValues(t *testing.T) {
	_, err := executeCLI("summary", "--format", "xml")
	if err == nil || !strings.Contains(err.Error(), "expected pretty or json") {
		t.Fatalf("summary format error = %v", err)
	}
	_, err = executeCLI("export", "--format", "pretty")
	if err == nil || !strings.Contains(err.Error(), "expected csv or json") {
		t.Fatalf("export format error = %v", err)
	}
}

func TestAgentAPIDocMentionsEveryCommand(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "docs", "agent-api.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"summary", "daily", "monthly", "models", "heatmap", "pricing", "doctor", "sync", "export", "install-skill"} {
		if !strings.Contains(string(contents), "`"+command+"`") && !strings.Contains(string(contents), " "+command+" ") {
			t.Errorf("agent API documentation does not mention %q", command)
		}
	}
}

func decodeEnvelope(t *testing.T, output string) decodedEnvelope {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(output))
	var envelope decodedEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		t.Fatalf("decode JSON output %q: %v", output, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("extra JSON/stdout after envelope: %q", output)
	}
	return envelope
}

func assertEnvelope(t *testing.T, envelope decodedEnvelope, command string) {
	t.Helper()
	if envelope.Schema != reportSchema || envelope.Command != command || envelope.GeneratedAt == "" || envelope.Timezone == "" {
		t.Fatalf("invalid envelope: %+v", envelope)
	}
	if envelope.Warnings == nil || envelope.Disclaimer != pricingDisclaimer || len(envelope.Data) == 0 {
		t.Fatalf("incomplete envelope: %+v", envelope)
	}
}
