package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestLedgerZoomAndRowNavigation(t *testing.T) {
	data := ledgerTestData()
	state := State{Cursor: -1}

	state, changed := Update(state, data, "l")
	if !changed || state.Zoom != ZoomMonth || state.Year != 2026 || state.Cursor != -1 {
		t.Fatalf("year-to-month state = %+v, changed=%v", state, changed)
	}

	monthData := Data{Available: true, Zoom: ZoomMonth, Year: 2026, Rows: []Row{
		{Key: "2026-07", Label: "Jul 2026"},
		{Key: "2026-06", Label: "Jun 2026"},
	}}
	state, changed = Update(state, monthData, "l")
	if !changed || state.Zoom != ZoomDay || state.Month != "2026-07" {
		t.Fatalf("month-to-day state = %+v, changed=%v", state, changed)
	}

	state, changed = Update(state, data, "h")
	if !changed || state.Zoom != ZoomMonth || state.Year != 2026 {
		t.Fatalf("day-to-month state = %+v, changed=%v", state, changed)
	}
	state, changed = Update(state, monthData, "h")
	if !changed || state.Zoom != ZoomYear || state.Year != 2026 {
		t.Fatalf("month-to-year state = %+v, changed=%v", state, changed)
	}

	dayData := Data{Available: true, Zoom: ZoomDay, Month: "2026-07", Rows: []Row{
		{Key: "2026-07-14", Label: "Jul 14"},
		{Key: "2026-07-13", Label: "Jul 13"},
	}}
	state = State{Zoom: ZoomDay, Month: "2026-07", Cursor: -1}
	state, changed = Update(state, dayData, "j")
	if !changed || state.Cursor != 1 {
		t.Fatalf("j selection = %+v, changed=%v", state, changed)
	}
	state, changed = Update(state, dayData, "k")
	if !changed || state.Cursor != 0 {
		t.Fatalf("k selection = %+v, changed=%v", state, changed)
	}
}

func TestLedgerCanZoomOutFromAnEmptyPeriod(t *testing.T) {
	state := State{Zoom: ZoomDay, Year: 2026, Month: "2026-07", Cursor: -1}
	state, changed := Update(state, Data{}, "h")
	if !changed || state.Zoom != ZoomMonth {
		t.Fatalf("day empty zoom-out = %+v, changed=%v", state, changed)
	}
	state, changed = Update(state, Data{}, "h")
	if !changed || state.Zoom != ZoomYear {
		t.Fatalf("month empty zoom-out = %+v, changed=%v", state, changed)
	}
}

func TestLedgerIgnoresStaleRowsDuringZoom(t *testing.T) {
	state := State{Zoom: ZoomMonth, Year: 2026, Cursor: -1}
	stale := Data{Available: true, Zoom: ZoomYear, Rows: []Row{{Key: "2026", Label: "2026"}}}
	next, changed := Update(state, stale, "l")
	if changed || next != state {
		t.Fatalf("stale drill-down changed state: before=%+v after=%+v changed=%v", state, next, changed)
	}
	next, changed = Update(state, stale, "h")
	if !changed || next.Zoom != ZoomYear {
		t.Fatalf("stale zoom-out did not recover: state=%+v changed=%v", next, changed)
	}
}

func TestLedgerRenderShowsProvidersDeltaActivityAndTotal(t *testing.T) {
	data := ledgerTestData()
	view := Render(ledgerTestRender(), data, State{Cursor: -1}, 30)
	for _, fragment := range []string{"ALL YEARS", "PERIOD", "CODEX", "CLAUDE", "DELTA", "› 2026", "+$2.00", "TOTAL", "█", "▓"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("ledger view missing %q:\n%s", fragment, view)
		}
	}
	lines := strings.Split(view, "\n")
	activityStart := func(line string) int {
		line = ansi.Strip(line)
		index := strings.IndexAny(line, "█▓·")
		if index < 0 {
			return -1
		}
		return lipgloss.Width(line[:index])
	}
	rowActivityStart := activityStart(lines[2])
	smallActivityStart := activityStart(lines[3])
	totalActivityStart := activityStart(lines[4])
	if rowActivityStart != smallActivityStart || smallActivityStart != totalActivityStart {
		t.Fatalf("ledger columns shifted between rows: starts=%d,%d,%d\n%s", rowActivityStart, smallActivityStart, totalActivityStart, view)
	}
	maxTokens := maxRowTokens(data.Rows)
	largeBar := lipgloss.Width(activityBar(ledgerTestRender(), data.Rows[0], 18, maxTokens, false))
	smallBar := lipgloss.Width(activityBar(ledgerTestRender(), data.Rows[1], 18, maxTokens, false))
	if smallBar >= largeBar {
		t.Fatalf("activity bar did not encode row magnitude: large=%d small=%d\n%s", largeBar, smallBar, view)
	}
	if totalBar := lipgloss.Width(activityBar(ledgerTestRender(), data.Total, 18, maxTokens, true)); totalBar != 18 {
		t.Fatalf("total activity bar width = %d, want 18", totalBar)
	}
}

func TestLedgerCompactRenderFitsNarrowPane(t *testing.T) {
	view := Render(theme.Context{Mode: theme.Plain, Width: 42, Palette: theme.NewPalette(nil)}, ledgerTestData(), State{Cursor: -1}, 18)
	if !strings.Contains(view, "C ") || !strings.Contains(view, "L ") || !strings.Contains(view, "TOTAL") {
		t.Fatalf("compact ledger omitted provider or total rows:\n%s", view)
	}
	for index, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 42 {
			t.Fatalf("compact line %d width=%d:\n%s", index+1, width, view)
		}
	}
}

func TestLedgerCompactHeaderFitsVeryNarrowPane(t *testing.T) {
	view := Render(theme.Context{Mode: theme.Plain, Width: 20, Palette: theme.NewPalette(nil)}, ledgerTestData(), State{Cursor: -1}, 18)
	for index, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 20 {
			t.Fatalf("narrow compact line %d width=%d:\n%s", index+1, width, view)
		}
	}
}

func TestLedgerMarksPartiallyPricedCosts(t *testing.T) {
	data := Data{Available: true, Zoom: ZoomDay, Month: "2026-07", Rows: []Row{
		{Key: "2026-07-14", Label: "Jul 14", Codex: ProviderTotals{Cost: pricing.Money(1_000_000_000), Tokens: 100, PricedTokens: 60, UnpricedTokens: 40}},
		{Key: "2026-07-13", Label: "Jul 13", Codex: ProviderTotals{Cost: pricing.Money(500_000_000), Tokens: 100, PricedTokens: 60, UnpricedTokens: 40}},
	}}
	data.Total = data.Rows[0].Add(data.Rows[1])
	view := Render(ledgerTestRender(), data, State{Zoom: ZoomDay, Month: "2026-07", Cursor: -1}, 30)
	for _, fragment := range []string{"~$1.00", "+~$0.50"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("partial ledger value missing %q:\n%s", fragment, view)
		}
	}
}

func TestLedgerDayExpandsSessionsAndOpensSharedDetail(t *testing.T) {
	first := "2026-07-14T09:30:00Z"
	data := Data{
		Available: true, Zoom: ZoomDay, Month: "2026-07", SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC,
		Rows: []Row{{Key: "2026-07-14", Label: "Jul 14", Codex: ProviderTotals{Cost: pricing.Money(560_000_000), Tokens: 130_000, PricedTokens: 130_000}}},
		Sessions: []LedgerSession{{
			CatalogSession: historystore.CatalogSession{SessionID: "ses_cost", Provider: history.ProviderCodex, Project: "tokenomnom", Preview: "trace the expensive request", FirstTimestamp: &first},
			Tokens:         130_000, Cost: pricing.Money(560_000_000), PricedTokens: 130_000,
		}},
	}
	state := State{Zoom: ZoomDay, Month: "2026-07", Cursor: -1}
	state, changed := Update(state, data, "l")
	if !changed || state.ExpandedDay != "2026-07-14" {
		t.Fatalf("expanded ledger state = %+v changed=%v", state, changed)
	}
	view := Render(ledgerTestRender(), data, state, 24)
	for _, fragment := range []string{"Jul 14", "09:30", "codex", "tokenomnom", "trace the expensive request", "130,000", "$0.56", "enter open"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("expanded ledger missing %q:\n%s", fragment, view)
		}
	}
	state, changed = Update(state, data, "enter")
	if !changed || state.DetailID != "ses_cost" {
		t.Fatalf("opened ledger session = %+v changed=%v", state, changed)
	}
	detail := Render(ledgerTestRender(), data, state, 24)
	if !strings.Contains(detail, "SESSION DETAIL") || !strings.Contains(detail, "esc back to ledger") {
		t.Fatalf("ledger detail did not reuse shared view:\n%s", detail)
	}
	state, changed = Update(state, data, "esc")
	if !changed || state.DetailID != "" || state.ExpandedDay == "" {
		t.Fatalf("detail back state = %+v changed=%v", state, changed)
	}
}

func TestLedgerExpandedDayShowsHistoryIndexHint(t *testing.T) {
	state := State{Zoom: ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14"}
	view := Render(ledgerTestRender(), Data{SessionDay: "2026-07-14"}, state, 20)
	if !strings.Contains(view, "No history index is available.") || !strings.Contains(view, "tokenomnom history index") {
		t.Fatalf("expanded ledger index hint missing:\n%s", view)
	}
}

func TestLedgerExpandedDayDoesNotCallPricingFailureAMissingIndex(t *testing.T) {
	state := State{Zoom: ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14"}
	data := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true, SessionDataUnavailable: true,
		SessionWarning: "Session costs could not be priced; check the pricing override and press R to retry.",
	}
	view := Render(ledgerTestRender(), data, state, 20)
	if !strings.Contains(view, "Session costs could not be priced") || strings.Contains(view, "No history index") || strings.Contains(view, "history index to refresh") {
		t.Fatalf("pricing failure rendered as an index failure:\n%s", view)
	}
}

func TestLedgerExpandedSessionsPageForwardAndBack(t *testing.T) {
	state := State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}
	firstPage := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true,
		Sessions:         []LedgerSession{{CatalogSession: historystore.CatalogSession{SessionID: "ses_100"}}},
		SessionsHaveMore: true, SessionsNextCursor: "page-two",
	}
	state, changed := Update(state, firstPage, "down")
	if !changed || state.SessionPageCursor != "page-two" || state.SessionCursorStack != "\x00" || state.SessionCursor != 0 {
		t.Fatalf("next ledger session page = %+v changed=%v", state, changed)
	}
	secondPage := Data{
		SessionDay: "2026-07-14", SessionPageCursor: "page-two", SessionIndexAvailable: true,
		Sessions: []LedgerSession{{CatalogSession: historystore.CatalogSession{SessionID: "ses_101"}}},
	}
	state, changed = Update(state, secondPage, "up")
	if !changed || state.SessionPageCursor != "" || state.SessionCursorStack != "" || !state.SessionSelectLast {
		t.Fatalf("previous ledger session page = %+v changed=%v", state, changed)
	}
}

func TestLedgerExpandedSessionSurfacesPartialAttributionWarning(t *testing.T) {
	first := "2026-07-13T09:30:00Z"
	data := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true,
		Sessions: []LedgerSession{{
			CatalogSession: historystore.CatalogSession{Provider: history.ProviderCodex, Project: "tokenomnom", Preview: "costly prompt", FirstTimestamp: &first},
			Tokens:         100, Cost: pricing.Money(500_000_000), PricedTokens: 100,
			AttributionStatus: "incomplete", ActivityTimestamp: "2026-07-14T15:45:00Z",
			Warning: "the preferred transcript was unavailable; cost uses a fallback location",
		}},
	}
	view := Render(ledgerTestRender(), data, State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}, 18)
	if !strings.Contains(view, "15:45") || !strings.Contains(view, "~$0.50") || !strings.Contains(view, "cost uses a fallback location") {
		t.Fatalf("partial ledger attribution was not disclosed:\n%s", view)
	}
}

func TestLedgerExpandedSessionRowsFitNarrowPane(t *testing.T) {
	first := "2026-07-14T09:30:00Z"
	data := Data{SessionDay: "2026-07-14", SessionIndexAvailable: true, Sessions: []LedgerSession{{
		CatalogSession: historystore.CatalogSession{Provider: history.ProviderClaude, Project: "a very long project name", Preview: "a first prompt that must stay inside the pane", FirstTimestamp: &first},
		Tokens:         123_456, Cost: pricing.Money(12_340_000_000), PricedTokens: 123_456,
	}}}
	view := Render(theme.Context{Mode: theme.Plain, Width: 42, Palette: theme.NewPalette(nil)}, data, State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}, 18)
	for index, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 42 {
			t.Fatalf("expanded ledger line %d width=%d:\n%s", index+1, width, view)
		}
	}
}

func TestQuest116AfterSnapshot(t *testing.T) {
	first, second := "2026-07-14T09:30:00Z", "2026-07-14T14:12:00Z"
	day := Row{Key: "2026-07-14", Label: "Jul 14", Codex: ProviderTotals{Cost: pricing.Money(1_584_230_000_000), Tokens: 91_200_000, PricedTokens: 91_200_000}, Claude: ProviderTotals{Cost: pricing.Money(625_000_000_000), Tokens: 34_800_000, PricedTokens: 34_800_000}}
	data := Data{
		Available: true, Zoom: ZoomDay, Month: "2026-07", Rows: []Row{day}, Total: day,
		SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC,
		Sessions: []LedgerSession{
			{CatalogSession: historystore.CatalogSession{SessionID: "ses_codex", Provider: history.ProviderCodex, Project: "tokenomnom", Preview: "Investigate the production latency regression", FirstTimestamp: &first}, Tokens: 91_200_000, Cost: pricing.Money(1_584_230_000_000), PricedTokens: 91_200_000},
			{CatalogSession: historystore.CatalogSession{SessionID: "ses_claude", Provider: history.ProviderClaude, Project: "billing-api", Preview: "Prepare the migration rollout plan", FirstTimestamp: &second}, Tokens: 34_800_000, Cost: pricing.Money(625_000_000_000), PricedTokens: 34_800_000},
		},
	}
	view := Render(theme.Context{Mode: theme.Plain, Width: 100, Palette: theme.NewPalette(nil)}, data, State{Zoom: ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14"}, 24)
	for _, fragment := range []string{"$2,209.23", "Investigate the production latency", "Prepare the migration rollout plan"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("quest 116 snapshot missing %q:\n%s", fragment, view)
		}
	}
	t.Log("\n" + view)
}

func TestFitMoneyRoundsAbbreviatedValues(t *testing.T) {
	for _, test := range []struct {
		value string
		width int
		want  string
	}{
		{value: "$1,999.99", width: 3, want: "$2k"},
		{value: "+$1,999.99", width: 4, want: "+$2k"},
		{value: "~$1,999.99", width: 5, want: "~$2k"},
	} {
		if got := fitMoney(test.value, test.width); got != test.want {
			t.Errorf("fitMoney(%q, %d) = %q, want %q", test.value, test.width, got, test.want)
		}
	}
}

func ledgerTestData() Data {
	rows := []Row{
		{
			Key: "2026", Label: "2026",
			Codex:  ProviderTotals{Cost: pricing.Money(2_000_000_000), Tokens: 200, PricedTokens: 200},
			Claude: ProviderTotals{Cost: pricing.Money(1_000_000_000), Tokens: 100, PricedTokens: 100},
		},
		{
			Key: "2025", Label: "2025",
			Codex: ProviderTotals{Cost: pricing.Money(1_000_000_000), Tokens: 100, PricedTokens: 100},
		},
	}
	total := Row{Key: "total", Label: "TOTAL"}
	for _, row := range rows {
		total = total.Add(row)
	}
	return Data{Available: true, Zoom: ZoomYear, Rows: rows, Total: total}
}

func ledgerTestRender() theme.Context {
	return theme.Context{Mode: theme.Plain, Width: 90, Palette: theme.NewPalette(nil)}
}
