package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestExportCSVGoldenQuotingAndDerivedColumns(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	seedExportCommaModel(t, stateDir)

	output, err := executeReport([]string{"export", "--no-sync", "--model", "model,comma"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"provider,date,month,year,model,input_tokens,cached_input_tokens,cache_write_5m_tokens,cache_write_1h_tokens,cache_write_unclassified_tokens,cache_write_input_tokens,uncached_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cost_usd",
		"codex,2026-03-14,March,2026,\"model,comma\",20,3,2,4,1,7,17,5,2,25,",
		"",
	}, "\n")
	if output != want {
		t.Fatalf("CSV export:\n%s\nwant:\n%s", output, want)
	}

	reader := csv.NewReader(strings.NewReader(output))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || len(records[1]) != len(exportCSVHeader) || records[1][4] != "model,comma" {
		t.Fatalf("CSV round trip = %#v", records)
	}
}

func TestExportOutWritesAtomicallyAndPrintsNothing(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	outPath := filepath.Join(t.TempDir(), "usage.csv")
	if err := os.WriteFile(outPath, []byte("old contents\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output, err := executeReport([]string{"export", "--no-sync", "--provider", "claude", "--out", outPath}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if output != "" {
		t.Fatalf("--out printed to stdout: %q", output)
	}
	contents, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(contents))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("provider filter wrote %d records:\n%s", len(records), contents)
	}
	for _, row := range records[1:] {
		if row[0] != "claude" {
			t.Fatalf("provider filter leaked row: %#v", row)
		}
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(outPath), ".usage.csv-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary export files remain: %v", matches)
	}

	jsonPath := filepath.Join(filepath.Dir(outPath), "usage.json")
	output, err = executeReport([]string{"export", "--format", "json", "--no-sync", "--provider", "claude", "--out", jsonPath}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if output != "" {
		t.Fatalf("JSON --out printed to stdout: %q", output)
	}
	jsonContents, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, string(jsonContents))
	assertEnvelope(t, envelope, "export")
}

func TestExportJSONMatchesCSVFieldsAndFilters(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	seedExportCommaModel(t, stateDir)
	output, err := executeReport([]string{"export", "--format", "json", "--no-sync", "--since", "2026-03-01"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	if envelope.Filters.Since == nil || *envelope.Filters.Since != "2026-03-01" {
		t.Fatalf("filters = %+v", envelope.Filters)
	}
	var data jsonExportData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("rows = %+v", data.Rows)
	}
	row := data.Rows[0]
	if row.Model != "model,comma" || row.Month != "March" || row.Year != "2026" || row.CacheWriteInputTokens != 7 || row.CostUSD != nil {
		t.Fatalf("JSON export row = %+v", row)
	}
	if data.UnpricedTokens != 25 || data.UnknownModelTokens != 0 {
		t.Fatalf("JSON export diagnostics = %+v", data)
	}
}

func TestExportEmptyCSVAlwaysHasExactHeader(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	output, err := executeReport([]string{"export", "--no-sync"}, filepath.Join(root, "codex"), filepath.Join(root, "claude"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := csv.NewReader(strings.NewReader(output)).Read()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record, exportCSVHeader) {
		t.Fatalf("header = %#v", record)
	}
}

func TestExportCSVSyncWarningGoesToStderr(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	release, err := store.Lock(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	var stdout, stderr bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"export", "--codex-dir", codexDir, "--claude-dir", claudeDir})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "WARNING") {
		t.Fatalf("CSV stdout was corrupted by warning: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "WARNING: usage store is busy") {
		t.Fatalf("sync warning missing from stderr: %q", stderr.String())
	}
	if _, err := csv.NewReader(strings.NewReader(stdout.String())).ReadAll(); err != nil {
		t.Fatalf("CSV stdout is not parseable: %v\n%s", err, stdout.String())
	}
}

func TestExportCSVNeutralizesFormulaModelNames(t *testing.T) {
	for _, model := range []string{"=FORMULA", "+FORMULA", "-FORMULA", "@FORMULA", "\t=FORMULA", "\r=FORMULA", "\n=FORMULA"} {
		t.Run(string([]byte{model[0]}), func(t *testing.T) {
			row := store.Usage{Date: "2026-03-14", Provider: discover.ProviderCodex, Model: model}
			var output strings.Builder
			if err := writeExportCSV(&output, []store.Usage{row}, mustPricingTable(t)); err != nil {
				t.Fatal(err)
			}
			records, err := csv.NewReader(strings.NewReader(output.String())).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if got := records[1][4]; got != "'"+model {
				t.Fatalf("formula-like model was not neutralized: %q", got)
			}
		})
	}
}

func mustPricingTable(t *testing.T) pricing.Table {
	t.Helper()
	table, err := pricing.Load()
	if err != nil {
		t.Fatal(err)
	}
	return table
}

func seedExportCommaModel(t *testing.T, stateDir string) {
	t.Helper()
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	err = database.Transaction(func(tx *store.Tx) error {
		return tx.ApplyUsage(store.Usage{
			Date: "2026-03-14", Provider: discover.ProviderCodex, Model: "model,comma",
			Input: 20, CacheRead: 3, CacheWrite5m: 2, CacheWrite1h: 4,
			CacheWriteUnclassified: 1, Output: 5, Reasoning: 2,
		}, "")
	})
	if err != nil {
		t.Fatal(err)
	}
}
