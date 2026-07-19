package store

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/discover"
)

func TestAggregateQueriesAndFilters(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	seed := []Usage{
		{Date: "2026-01-31", Provider: discover.ProviderCodex, Model: "gpt-a", Input: 100, CacheRead: 40, Output: 20, Reasoning: 5},
		{Date: "2026-02-01", Provider: discover.ProviderCodex, Model: "gpt-a", Input: 200, CacheRead: 80, Output: 30, Reasoning: 8},
		{Date: "2026-02-01", Provider: discover.ProviderClaude, Model: "claude-b", Input: 300, CacheRead: 90, CacheWrite5m: 10, CacheWrite1h: 20, CacheWriteUnclassified: 5, Output: 50},
		{Date: "2026-02-03", Provider: discover.ProviderClaude, Model: "claude-b", Input: 400, CacheRead: 100, CacheWrite5m: 30, Output: 60},
	}
	if err := database.Transaction(func(tx *Tx) error {
		for _, usage := range seed {
			if err := tx.ApplyUsage(usage, ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	daily, err := database.Daily(Filter{Since: "2026-02-01", Until: "2026-02-03"})
	if err != nil {
		t.Fatal(err)
	}
	if len(daily) != 2 || daily[0].Date != "2026-02-01" || daily[0].Input != 500 || daily[0].Output != 80 || daily[0].Total != 580 || daily[0].CacheWrite != 35 {
		t.Fatalf("daily aggregates = %+v", daily)
	}

	monthly, err := database.Monthly(Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(monthly) != 2 || monthly[0].Month != "2026-01" || monthly[0].Total != 120 || monthly[1].Month != "2026-02" || monthly[1].Total != 1040 {
		t.Fatalf("monthly aggregates = %+v", monthly)
	}

	models, err := database.ByModel(Filter{Provider: discover.ProviderClaude, Model: "claude-b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Provider != discover.ProviderClaude || models[0].Total != 810 || models[0].CacheWrite != 65 || models[0].ActiveDays != 2 || models[0].FirstDate != "2026-02-01" || models[0].LastDate != "2026-02-03" {
		t.Fatalf("model aggregates = %+v", models)
	}

	totals, err := database.Totals(Filter{Since: "2026-02-01", Until: "2026-02-01"})
	if err != nil {
		t.Fatal(err)
	}
	if totals.Total != 580 || totals.ActiveDays != 1 || totals.FirstDate != "2026-02-01" || totals.LastDate != "2026-02-01" || len(totals.Providers) != 2 {
		t.Fatalf("totals = %+v", totals)
	}
	gotProviders := []discover.Provider{totals.Providers[0].Provider, totals.Providers[1].Provider}
	wantProviders := []discover.Provider{discover.ProviderClaude, discover.ProviderCodex}
	if !reflect.DeepEqual(gotProviders, wantProviders) {
		t.Fatalf("providers = %v, want %v", gotProviders, wantProviders)
	}

	empty, err := database.Totals(Filter{Since: "2030-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Total != 0 || empty.ActiveDays != 0 || empty.FirstDate != "" || len(empty.Providers) != 0 {
		t.Fatalf("empty totals = %+v", empty)
	}
}
