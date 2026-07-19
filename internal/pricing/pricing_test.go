package pricing

import (
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestEmbeddedTable(t *testing.T) {
	table, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(table.models); got != 12 {
		t.Fatalf("model count = %d, want 12", got)
	}
	spark, ok := table.RateFor("gpt-5.3-codex-spark", "2026-07-18")
	if !ok || spark.Status != "proxy" || !strings.Contains(spark.Notes, "Janior explicitly chose") {
		t.Fatalf("spark rate = %+v, found %v", spark, ok)
	}
	sonnet, ok := table.RateFor("claude-sonnet-5", "2026-08-31")
	if !ok || sonnet.EffectiveUntil != "2026-08-31" {
		t.Fatalf("sonnet rate = %+v, found %v", sonnet, ok)
	}
}

func TestOverrideReplacesWholeModelAndAddsUnknownModel(t *testing.T) {
	override := `{
		"gpt-5.2": [{"base_input": 9, "status": "estimated", "source": "https://example.com/rate"}],
		"local-model": [{"output": 3, "status": "estimated", "source": "https://example.com/local"}]
	}`
	table, err := Load(strings.NewReader(override))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := table.RateFor("gpt-5.2", "2026-07-18")
	if !ok || entry.BaseInput == nil || *entry.BaseInput != 9000 || entry.CacheRead != nil || entry.Status != "estimated" {
		t.Fatalf("replacement entry = %+v, found %v", entry, ok)
	}
	if !table.IsOverridden("gpt-5.2") || !table.IsOverridden("local-model") || table.IsOverridden("gpt-5.3-codex") {
		t.Fatalf("override markers = %+v", table.overridden)
	}
}

func TestLoadRejectsInvalidTables(t *testing.T) {
	tests := []struct {
		name string
		json string
		want string
	}{
		{"malformed", `{`, "decode pricing JSON"},
		{"unknown field", `{"x":[{"base":1,"status":"published","source":"https://example.com"}]}`, "unknown field"},
		{"overlap", `{"x":[
			{"base_input":1,"status":"published","source":"https://example.com","effective_until":"2026-07-18"},
			{"base_input":2,"status":"published","source":"https://example.com","effective_from":"2026-07-18"}
		]}`, "overlapping effective ranges"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(test.json))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCostMathAndDiagnostics(t *testing.T) {
	table, err := Load(strings.NewReader(`{"test-model":[{
		"base_input":1,"cache_read":0.1,"write_5m":1.25,"write_1h":2,"output":5,
		"status":"published","source":"https://example.com"
	}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := table.Cost(store.Usage{
		Date: "2026-07-18", Model: "test-model", Input: 5_000_000,
		CacheRead: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000,
		CacheWriteUnclassified: 1_000_000, Output: 1_000_000, Reasoning: 750_000,
	})
	if got.BaseInput != 1_000_000_000 || got.CacheRead != 100_000_000 || got.CacheWrite5m != 1_250_000_000 ||
		got.CacheWrite1h != 2_000_000_000 || got.CacheWriteUnclassified != 2_000_000_000 || got.Output != 5_000_000_000 || got.Total != 11_350_000_000 {
		t.Fatalf("cost breakdown = %+v", got)
	}
	if got.PricedTokens != 6_000_000 || got.UnpricedTokens != 0 || got.UnclassifiedCacheWriteTokens != 1_000_000 {
		t.Fatalf("cost diagnostics = %+v", got)
	}

	unpriced := table.Cost(store.Usage{Date: "2026-07-18", Model: "gpt-5.2", Input: 2_000_000, CacheWrite5m: 1_000_000})
	if unpriced.Total != 1_750_000_000 || unpriced.PricedTokens != 1_000_000 || unpriced.UnpricedTokens != 1_000_000 {
		t.Fatalf("unpriced bucket = %+v", unpriced)
	}
}

func TestEffectiveDateBoundaries(t *testing.T) {
	table, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-08-30", "2026-08-31"} {
		got := table.Cost(store.Usage{Date: date, Model: "claude-sonnet-5", Input: 1_000_000})
		if got.Total != 2_000_000_000 || got.UnpricedTokens != 0 {
			t.Fatalf("cost on %s = %+v", date, got)
		}
	}
	after := table.Cost(store.Usage{Date: "2026-09-01", Model: "claude-sonnet-5", Input: 1_000_000})
	if after.Total != 0 || after.UnpricedTokens != 1_000_000 || after.PricedTokens != 0 {
		t.Fatalf("cost after effective window = %+v", after)
	}
}

func TestSparkFrozenSnapshotGolden(t *testing.T) {
	table, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got := table.Cost(store.Usage{
		Date: "2026-07-18", Provider: discover.ProviderCodex, Model: "gpt-5.3-codex-spark",
		Input: 3_882_004_043, CacheRead: 3_724_110_336, Output: 23_039_137,
	})
	if got.BaseInput.RoundedCents() != 27_631 || got.CacheRead.RoundedCents() != 65_172 || got.Output.RoundedCents() != 32_255 || got.Total.RoundedCents() != 125_058 {
		t.Fatalf("spark golden cost = base %d read %d output %d total %d cents", got.BaseInput.RoundedCents(), got.CacheRead.RoundedCents(), got.Output.RoundedCents(), got.Total.RoundedCents())
	}
}

func TestCodexFrozenCSVGolden(t *testing.T) {
	file, err := os.Open("../../archive/2026-07-18-snapshot/codex_daily_token_usage_by_model_2026-02-03_to_2026-07-18.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	table, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	var total Money
	for index, record := range records[1:] {
		input := parseCSVInt(t, index+2, record[4])
		cached := parseCSVInt(t, index+2, record[5])
		output := parseCSVInt(t, index+2, record[8])
		cost := table.Cost(store.Usage{Date: record[0], Provider: discover.ProviderCodex, Model: record[3], Input: input, CacheRead: cached, Output: output})
		if cost.UnpricedTokens != 0 {
			t.Fatalf("row %d has %d unpriced tokens", index+2, cost.UnpricedTokens)
		}
		total += cost.Total
	}
	// The exact nanodollar sum rounds to the workbook subtotal; allow one cent
	// because the workbook may round detailed rows before summing.
	wantCents := int64(12_705_005)
	delta := total.RoundedCents() - wantCents
	if delta < -1 || delta > 1 {
		t.Fatalf("Codex golden subtotal = %d cents, want %d +/- 1", total.RoundedCents(), wantCents)
	}
}

func parseCSVInt(t *testing.T, row int, value string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatalf("parse row %d value %q: %v", row, value, err)
	}
	return parsed
}
