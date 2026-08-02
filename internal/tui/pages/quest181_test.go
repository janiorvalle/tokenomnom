package pages

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
)

func quest181DailyDetail() DailyDetail {
	return DailyDetail{
		Date:  "2026-08-01",
		Value: DailyValue{Cost: pricing.Money(200_000_000_000), Tokens: 20_000_000, PricedTokens: 18_000_000, Sessions: 20},
		Providers: []DailyProvider{
			{Provider: "codex", Value: DailyValue{Cost: pricing.Money(120_000_000_000), Tokens: 12_000_000, PricedTokens: 12_000_000, Sessions: 14}},
			{Provider: "claude", Value: DailyValue{Cost: pricing.Money(80_000_000_000), Tokens: 8_000_000, PricedTokens: 6_000_000, Sessions: 6}},
		},
		Models: []DailyModel{
			{Provider: "codex", Model: "gpt-test", Value: DailyValue{Cost: pricing.Money(120_000_000_000), Tokens: 9_000_000, PricedTokens: 9_000_000}},
			{Provider: "claude", Model: "claude-test", Value: DailyValue{Cost: pricing.Money(60_000_000_000), Tokens: 5_000_000, PricedTokens: 5_000_000}},
			{Provider: "codex", Model: "gpt-mini", Value: DailyValue{Cost: pricing.Money(15_000_000_000), Tokens: 2_000_000, PricedTokens: 2_000_000}},
			{Provider: "claude", Model: "claude-small", Value: DailyValue{Cost: pricing.Money(5_000_000_000), Tokens: 1_000_000, PricedTokens: 1_000_000}},
			{Provider: "codex", Model: "gpt-terra", Value: DailyValue{Tokens: 2_000_000}},
			{Provider: "codex", Model: "auto-review", Value: DailyValue{Tokens: 1_000_000}},
		},
	}
}

func TestQuest181DailyModelsDiscloseUnpricedAndHidden(t *testing.T) {
	data := DailyPageData{Detail: quest181DailyDetail(), SelectedDate: "2026-08-01"}
	lines := strings.Join(dailyDetailLines(data, 70, false), "\n")
	if !strings.Contains(lines, "· 2 unpriced") {
		t.Fatalf("unpriced models are not disclosed:\n%s", lines)
	}
	if !strings.Contains(lines, "+1 more models") {
		t.Fatalf("hidden model count is not disclosed:\n%s", lines)
	}
}

func TestQuest181DailyModelsDisclosePartialPricing(t *testing.T) {
	detail := quest181DailyDetail()
	// A model priced in some sessions but not others must count as not fully
	// priced — its displayed cost is a floor, not a total.
	detail.Models = append(detail.Models, DailyModel{
		Provider: "claude", Model: "claude-mixed",
		Value: DailyValue{Cost: pricing.Money(2_000_000_000), Tokens: 3_000_000, PricedTokens: 1_000_000},
	})
	data := DailyPageData{Detail: detail, SelectedDate: "2026-08-01"}
	lines := strings.Join(dailyDetailLines(data, 70, false), "\n")
	if !strings.Contains(lines, "· 3 not fully priced") {
		t.Fatalf("partially priced model is not disclosed:\n%s", lines)
	}
}

func TestQuest181DailyChartStacksProvidersWithoutMajorityGlyph(t *testing.T) {
	rows := make([]DailyPoint, 0, 10)
	for index := 0; index < 10; index++ {
		total := DailyValue{Cost: pricing.Money(int64(index+1) * 10_000_000_000), PricedTokens: 1}
		codex := DailyValue{Cost: pricing.Money(int64(index+1) * 6_000_000_000), PricedTokens: 1}
		claude := DailyValue{Cost: pricing.Money(int64(index+1) * 4_000_000_000), PricedTokens: 1}
		rows = append(rows, DailyPoint{Date: fmt.Sprintf("2026-07-%02d", index+1), Total: total, Codex: codex, Claude: claude})
	}
	view := renderDailyChart(testRender(), DailyPageData{Rows: rows, TrendRows: rows}, 120, 14)
	if !strings.Contains(view, "■ Codex") || !strings.Contains(view, "■ Claude") {
		t.Fatalf("chart legend missing:\n%s", view)
	}
	// Both segments must survive color stripping: Codex fills with █ from the
	// baseline and Claude caps the stack with ▓ — per column, not per bar.
	if !strings.Contains(view, "█") || !strings.Contains(view, "▓") {
		t.Fatalf("stacked provider glyphs missing:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	firstClaude, lastCodex := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "▓") && firstClaude == -1 {
			firstClaude = index
		}
		if strings.Contains(line, "█") {
			lastCodex = index
		}
	}
	if firstClaude == -1 || lastCodex <= firstClaude {
		t.Fatalf("claude segment does not cap the codex stack (claude first at %d, codex last at %d):\n%s", firstClaude, lastCodex, view)
	}
}

func TestQuest181HeatmapClampsLeadingEmptyWindow(t *testing.T) {
	from := time.Date(2025, 8, 3, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	first := time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC)
	// Days deliberately newest-first: materializeHeatmapDays sorts before the
	// clamp reads the earliest entry, and this pins that invariant.
	data := HeatmapData{Window: HeatmapWindow{From: from, To: to}, Days: []HeatmapDay{
		{Date: to, Cost: pricing.Money(2_000_000_000)},
		{Date: first, Cost: pricing.Money(1_000_000_000)},
	}}
	window, days := materializeHeatmapDays(data)
	if !window.From.Equal(first) {
		t.Fatalf("window start %s, want clamp to first activity %s", window.From, first)
	}
	if len(days) != daysBetween(first, to)+1 {
		t.Fatalf("materialized %d days, want %d", len(days), daysBetween(first, to)+1)
	}
}

func TestQuest181LedgerRollupEngagesBelowFixedTallHeight(t *testing.T) {
	sessions := make([]LedgerSession, 0, 12)
	for index := 0; index < 12; index++ {
		stamp := fmt.Sprintf("2026-07-14T%02d:00:00Z", 9+index%10)
		provider, project := history.ProviderCodex, "tokenomnom"
		if index%2 == 1 {
			provider, project = history.ProviderClaude, "billing-api"
		}
		sessions = append(sessions, LedgerSession{
			CatalogSession: historystore.CatalogSession{Provider: provider, Project: project, FirstTimestamp: &stamp},
			Tokens:         int64(900_000 + index*10_000), Cost: pricing.Money(5_000_000_000),
			PricedTokens: int64(900_000 + index*10_000), ActivityTimestamp: stamp,
		})
	}
	data := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC, Sessions: sessions,
		Rows: []Row{{Key: "2026-07-14", Label: "Jul 14", Sessions: len(sessions)}},
		DayModels: []LedgerModel{
			{Provider: "codex", Model: "gpt-5.2", Tokens: 9_900_000, Cost: pricing.Money(72_500_000_000), PricedTokens: 9_900_000},
		},
		DayProjects: []LedgerProject{{Label: "tokenomnom", Sessions: 12, Share: 1}},
	}
	state := State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}
	// Height 40 is below the old fixed ledgerTallHeight gate (45); with 12
	// session rows the leftover space must still engage the rollup band.
	view := renderExpandedDayList(ledgerTestRender(), data, state, 110, 40)
	if !strings.Contains(view, "MODELS ON THIS DAY") {
		t.Fatalf("rollup band did not engage on leftover height:\n%s", view)
	}
	// A busy day must not lose the band: sessions page inside the remaining
	// capacity while the rollup keeps its reserved floor.
	busy := data
	busy.Sessions = append([]LedgerSession(nil), sessions...)
	for len(busy.Sessions) < 60 {
		busy.Sessions = append(busy.Sessions, sessions...)
	}
	busy.SessionsHaveMore = true
	busyView := renderExpandedDayList(ledgerTestRender(), busy, state, 110, 59)
	if !strings.Contains(busyView, "MODELS ON THIS DAY") {
		t.Fatalf("busy day lost the rollup band:\n%s", busyView)
	}
	if !strings.Contains(busyView, "more sessions") {
		t.Fatalf("busy day should page sessions:\n%s", busyView)
	}
	blankRun := 0
	for index, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > 3 {
				t.Fatalf("day pane keeps a void at row %d:\n%s", index+1, view)
			}
			continue
		}
		blankRun = 0
	}
}
