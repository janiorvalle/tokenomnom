package store

import (
	"fmt"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestLedgerAnalyticsGroupsCatalogProfiles(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()

	addSession := func(provider history.Provider, nativeID, project string, timestamp time.Time) {
		source := history.SourceReference{Provider: provider, Kind: history.LocationProviderLive, Path: "/provider/" + nativeID + ".jsonl"}
		value := extraction("native:"+nativeID, nativeID, source, prompt("native:"+nativeID+":prompt", nativeID+":prompt", nativeID+" prompt", 1))
		value.Session.RepositoryName = project
		value.Session.FirstTimestamp, value.Session.LastTimestamp = &timestamp, &timestamp
		value.Prompts[0].Timestamp = &timestamp
		value.Occurrences[0].Variant.Timestamp = &timestamp
		if _, err := database.ApplySource(value, head(source, fmt.Sprintf("hash-%s", nativeID), 32, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}

	addSession(history.ProviderCodex, "codex-july", "alpha", time.Date(2026, time.July, 21, 9, 0, 0, 0, time.UTC))
	addSession(history.ProviderClaude, "claude-july", "alpha", time.Date(2026, time.July, 22, 15, 0, 0, 0, time.UTC))
	addSession(history.ProviderCodex, "codex-august", "beta", time.Date(2026, time.August, 1, 20, 0, 0, 0, time.UTC))

	all, err := database.LedgerAnalytics(CatalogQuery{Source: CatalogSourceAny}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Months) != 2 || all.Months[0] != (LedgerMonthStat{Month: "2026-07", Sessions: 2}) || all.Months[1] != (LedgerMonthStat{Month: "2026-08", Sessions: 1}) {
		t.Fatalf("month profile = %+v", all.Months)
	}
	if len(all.Days) != 3 || all.Days[0] != (LedgerDayStat{Day: "2026-07-21", Sessions: 1}) || all.Days[1] != (LedgerDayStat{Day: "2026-07-22", Sessions: 1}) || all.Days[2] != (LedgerDayStat{Day: "2026-08-01", Sessions: 1}) {
		t.Fatalf("day profile = %+v", all.Days)
	}
	if len(all.Weekdays) != 3 || all.Weekdays[0] != (LedgerProfileStat{Bucket: 2, Sessions: 1}) || all.Weekdays[1] != (LedgerProfileStat{Bucket: 3, Sessions: 1}) || all.Weekdays[2] != (LedgerProfileStat{Bucket: 6, Sessions: 1}) {
		t.Fatalf("weekday profile = %+v", all.Weekdays)
	}
	if len(all.Hours) != 3 || all.Hours[0] != (LedgerProfileStat{Bucket: 9, Sessions: 1}) || all.Hours[1] != (LedgerProfileStat{Bucket: 15, Sessions: 1}) || all.Hours[2] != (LedgerProfileStat{Bucket: 20, Sessions: 1}) {
		t.Fatalf("hour profile = %+v", all.Hours)
	}
	if len(all.ProjectMonths) != 2 || all.ProjectMonths[0] != (LedgerProjectMonthStat{Project: "alpha", Month: "2026-07", Sessions: 2}) || all.ProjectMonths[1] != (LedgerProjectMonthStat{Project: "beta", Month: "2026-08", Sessions: 1}) {
		t.Fatalf("project/month profile = %+v", all.ProjectMonths)
	}

	codex, err := database.LedgerAnalytics(CatalogQuery{Provider: history.ProviderCodex, Source: CatalogSourceAny}, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(codex.Months) != 2 || codex.Months[0].Sessions != 1 || codex.Months[1].Sessions != 1 || len(codex.ProjectMonths) != 2 {
		t.Fatalf("provider-filtered profile = %+v", codex)
	}

	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	codexLocal, err := database.LedgerAnalytics(CatalogQuery{Provider: history.ProviderCodex, Source: CatalogSourceAny}, location)
	if err != nil {
		t.Fatal(err)
	}
	if len(codexLocal.Hours) != 2 || codexLocal.Hours[0] != (LedgerProfileStat{Bucket: 5, Sessions: 1}) || codexLocal.Hours[1] != (LedgerProfileStat{Bucket: 16, Sessions: 1}) {
		t.Fatalf("timezone-adjusted hours = %+v", codexLocal.Hours)
	}
}
