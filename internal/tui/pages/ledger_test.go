package pages

import (
	"fmt"
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

func TestLedgerProvenanceSummaryLabelsUserRates(t *testing.T) {
	view := ledgerProvenanceSummary(ledgerTestRender(), LedgerProvenance{
		UserModels: 1, UserCost: pricing.Money(2_500_000_000), UserTokens: 1_000_000,
	}, 120)
	if !strings.Contains(view, "user rate 1") || !strings.Contains(view, "$2.50") {
		t.Fatalf("ledger provenance omitted user-rate estimate:\\n%s", view)
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

func TestLedgerWidePeriodsKeepAllPeriodsAndPanelsFilled(t *testing.T) {
	months := make([]LedgerMonth, 0, 12)
	rows := make([]Row, 0, 12)
	for month := 1; month <= 12; month++ {
		key := fmt.Sprintf("2026-%02d", month)
		value := LedgerMonth{Key: key, Label: time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("Jan 2006")}
		if month == 6 {
			value.Codex = ProviderTotals{Cost: pricing.Money(2_000_000_000), Tokens: 2_000_000, PricedTokens: 2_000_000}
		}
		if month == 7 {
			value.Claude = ProviderTotals{Cost: pricing.Money(1_000_000_000), Tokens: 1_000_000, PricedTokens: 1_000_000}
		}
		months = append(months, value)
		rows = append(rows, Row{Key: value.Key, Label: value.Label, Codex: value.Codex, Claude: value.Claude})
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	day := Row{Key: "total", Label: "TOTAL"}
	for _, row := range rows {
		day = day.Add(row)
	}
	data := Data{
		Available: true, Zoom: ZoomMonth, Year: 2026, Rows: rows, Total: day,
		Analytics: LedgerAnalytics{
			Months:        months,
			Models:        []LedgerModel{{Provider: "codex", Model: "gpt-5", Tokens: 2_000_000, Cost: pricing.Money(2_000_000_000), PricedTokens: 2_000_000, HasRate: true}},
			Weekdays:      []LedgerProfile{{Label: "Mon", Value: 2}, {Label: "Tue", Value: 3}},
			Hours:         []LedgerProfile{{Label: "09", Value: 2}, {Label: "15", Value: 3}},
			Projects:      []LedgerProject{{Label: "tokenomnom", Sessions: 3, Share: 1}},
			ProjectMonths: []LedgerProjectMonth{{Project: "tokenomnom", Month: "2026-06", Sessions: 2}, {Project: "tokenomnom", Month: "2026-07", Sessions: 1}},
		},
	}
	view := Render(theme.Context{Mode: theme.Plain, Width: 192, Palette: theme.NewPalette(nil)}, data, State{Zoom: ZoomMonth, Year: 2026, Cursor: -1}, 66)
	for _, fragment := range []string{"PERIOD DETAIL", "PRICING PROVENANCE", "COST PER 1M", "PROVIDER × MONTH", "ZOOM STACK", "SPEND BY MONTH", "PROJECT × MONTH", "WEEKDAY PROFILE", "HOUR OF DAY", "Jan 2026", "Dec 2026", "$0.00"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("wide ledger missing %q:\n%s", fragment, view)
		}
	}
	for index, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width != 192 {
			t.Fatalf("wide ledger line %d width=%d, want 192:\n%s", index+1, width, view)
		}
	}
	t.Logf("\nSource: internal/tui/pages/ledger_test.go::TestLedgerWidePeriodsKeepAllPeriodsAndPanelsFilled\nCommand: GOFLAGS=-buildvcs=false go test ./internal/tui/pages -run TestLedgerWidePeriodsKeepAllPeriodsAndPanelsFilled -count=1 -v\n\n%s", view)
}

func TestLedgerDayProviderMonthsUseSelectedMonth(t *testing.T) {
	data := Data{Analytics: LedgerAnalytics{ProviderMonths: []LedgerProviderMonth{
		{Provider: "codex", Month: "2026-07", Cost: pricing.Money(1), Tokens: 1},
		{Provider: "claude", Month: "2026-06", Cost: pricing.Money(1), Tokens: 1},
	}}}
	values := selectedLedgerProviderMonths(data, State{Zoom: ZoomDay, Month: "2026-07"}, Row{Key: "2026-07-14"})
	if len(values) != 1 || values[0].Month != "2026-07" {
		t.Fatalf("day provider months = %+v, want only July", values)
	}
}

func TestLedgerEmptyMonthDoesNotRenderAnalyticsRows(t *testing.T) {
	data := Data{Available: true, Zoom: ZoomMonth, Year: 2026, Analytics: LedgerAnalytics{
		Months: []LedgerMonth{{Key: "2026-07", Label: "Jul 2026"}},
	}}
	if rows := ledgerDisplayRows(data, State{Zoom: ZoomMonth, Year: 2026}); len(rows) != 0 {
		t.Fatalf("empty month rows = %+v, want no selectable rows", rows)
	}
}

func TestLedgerDefaultsToLatestActiveMonth(t *testing.T) {
	data := Data{Available: true, Zoom: ZoomMonth, Year: 2026, Rows: []Row{
		{Key: "2026-12", Label: "Dec 2026"},
		{Key: "2026-07", Label: "Jul 2026", Sessions: 2, Codex: ProviderTotals{Tokens: 200}},
		{Key: "2026-06", Label: "Jun 2026", Sessions: 1, Codex: ProviderTotals{Tokens: 100}},
	}}
	if selected := SelectedIndex(data, State{Zoom: ZoomMonth, Year: 2026, Cursor: -1}); selected != 1 {
		t.Fatalf("default month selection = %d, want latest active row 1", selected)
	}
}

func TestLedgerEmptyMonthShowsHonestPeriodAndChartStates(t *testing.T) {
	months := make([]LedgerMonth, 0, 12)
	for month := 1; month <= 12; month++ {
		value := LedgerMonth{Key: fmt.Sprintf("2026-%02d", month), Label: time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("Jan 2006")}
		if month == 7 {
			value.Codex = ProviderTotals{Cost: pricing.Money(2_000_000_000), Tokens: 2_000_000, PricedTokens: 2_000_000}
		}
		months = append(months, value)
	}
	data := Data{Available: true, Zoom: ZoomDay, Month: "2026-12", Analytics: LedgerAnalytics{Months: months}}
	state := State{Zoom: ZoomDay, Month: "2026-12", Cursor: -1}

	period := renderPeriodTableBand(ledgerTestRender(), data, state, 156, 17, true)
	if !strings.Contains(period, "no indexed activity in this period") || strings.Contains(period, "PERIOD          SESSIONS") {
		t.Fatalf("empty day period pane did not show its honest state:\n%s", period)
	}
	chart := renderSpendByMonth(ledgerTestRender(), data, state, 215, 16)
	if !strings.Contains(chart, "no indexed activity in this period") || strings.Contains(chart, "avg $0.00") || strings.Contains(chart, "··") {
		t.Fatalf("empty day chart was not suppressed:\n%s", chart)
	}
	selected := selectedLedgerRow(data, state)
	if selected.Key != "2026-12" {
		t.Fatalf("empty day selected row = %+v, want the requested month", selected)
	}
	activeDays, averageCost, peakCost, peakDay, _, _ := selectedLedgerMetrics(data, state, selected)
	if activeDays != 0 || averageCost != 0 || peakCost != 0 || peakDay != "" {
		t.Fatalf("empty day metrics = %d, %d, %d, %q, want zero values", activeDays, averageCost, peakCost, peakDay)
	}
}

func TestLedgerChartKeepsCostOnlyAndSessionOnlyMonths(t *testing.T) {
	for _, month := range []LedgerMonth{
		{Key: "2026-07", Label: "Jul 2026", Sessions: 1},
		{Key: "2026-07", Label: "Jul 2026", Codex: ProviderTotals{Cost: pricing.Money(1_000_000)}},
	} {
		data := Data{Available: true, Zoom: ZoomYear, Analytics: LedgerAnalytics{Months: []LedgerMonth{month}}}
		chart := renderSpendByMonth(ledgerTestRender(), data, State{Zoom: ZoomYear, Cursor: -1}, 90, 12)
		if strings.Contains(chart, ledgerEmptyPeriodMessage) {
			t.Fatalf("month with activity was rendered empty: %+v\n%s", month, chart)
		}
	}
}

func TestLedgerChartUsesResolvedPeriodAnchors(t *testing.T) {
	data := Data{
		Year: 2026, Month: "2026-02",
		Rows: []Row{{Key: "2026-02", Label: "Feb 2026"}},
		Analytics: LedgerAnalytics{Months: []LedgerMonth{
			{Key: "2025-12", Label: "Dec 2025"},
			{Key: "2026-01", Label: "Jan 2026"},
			{Key: "2026-02", Label: "Feb 2026"},
			{Key: "2027-01", Label: "Jan 2027"},
		}},
	}
	months := selectedLedgerChartMonths(data, State{Zoom: ZoomMonth, Year: 0})
	if len(months) != 2 || months[0].Key != "2026-01" || months[1].Key != "2026-02" {
		t.Fatalf("resolved year chart months = %+v", months)
	}
	months = selectedLedgerChartMonths(data, State{Zoom: ZoomDay, Month: ""})
	if len(months) != 1 || months[0].Key != "2026-02" {
		t.Fatalf("resolved month chart months = %+v", months)
	}
}

func TestScaleMoneyAvoidsIntermediateOverflow(t *testing.T) {
	maximum := pricing.Money(1<<63 - 1)
	if got := scaleMoney(maximum, 8, 8); got != maximum {
		t.Fatalf("scaleMoney(max, 8, 8) = %d, want %d", got, maximum)
	}
	if got := scaleMoney(maximum/2, 9, 1); got != pricing.Money(1<<63-1) {
		t.Fatalf("scaleMoney saturation = %d", got)
	}
}

func TestLedgerStandardPeriodsRespectShortHeight(t *testing.T) {
	for height := 1; height <= 4; height++ {
		view := Render(theme.Context{Mode: theme.Plain, Width: 100, Palette: theme.NewPalette(nil)}, ledgerTestData(), State{Cursor: -1}, height)
		if lines := strings.Split(view, "\n"); len(lines) != height {
			t.Fatalf("standard ledger height %d rendered %d lines:\n%s", height, len(lines), view)
		}
	}
}

func TestSessionPreviewSanitizesSessionID(t *testing.T) {
	view := RenderSessionPreview(theme.Context{Mode: theme.Plain, Width: 42, Palette: theme.NewPalette(nil)}, SessionPreview{
		SessionID: "ses\x1b]52;c;clipboard\a\nbad", Preview: "prompt",
	}, 42, 8)
	if strings.ContainsAny(view, "\x1b\a") || !strings.Contains(view, "ses]52;c;clipboard bad") {
		t.Fatalf("session preview did not sanitize the id:\n%q", view)
	}
}

func TestSessionPreviewKeepsSummaryVisibleForLongPrompts(t *testing.T) {
	view := RenderSessionPreview(theme.Context{Mode: theme.Plain, Width: 42, Palette: theme.NewPalette(nil)}, SessionPreview{
		SessionID: "session-long", Provider: "codex", Project: "tokenomnom", Preview: strings.Repeat("a long prompt that should wrap many times ", 20),
		Tokens: 100, Cost: pricing.Money(100_000_000), PricedTokens: 100,
	}, 42, 16)
	for _, fragment := range []string{"OVERVIEW", "COST & TOKENS", "tokens", "$0.10"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("long session preview missing %q:\n%s", fragment, view)
		}
	}
	for index, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width != 42 {
			t.Fatalf("preview line %d width=%d, want 42:\n%s", index+1, width, view)
		}
	}
}

func TestLedgerDayPreviewMovesWithSessionCursor(t *testing.T) {
	first, second := "2026-07-14T09:30:00Z", "2026-07-14T14:12:00Z"
	data := Data{
		Available: true, Zoom: ZoomDay, Month: "2026-07", SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC,
		Rows: []Row{{Key: "2026-07-14", Label: "Jul 14", Sessions: 2}},
		Sessions: []LedgerSession{
			{CatalogSession: historystore.CatalogSession{SessionID: "ses_one", Provider: history.ProviderCodex, Project: "alpha", Preview: "first preview", FirstTimestamp: &first}, Tokens: 100, Cost: pricing.Money(100_000_000), PricedTokens: 100},
			{CatalogSession: historystore.CatalogSession{SessionID: "ses_two", Provider: history.ProviderClaude, Project: "beta", Preview: "second preview", FirstTimestamp: &second}, Tokens: 200, Cost: pricing.Money(200_000_000), PricedTokens: 200},
		},
	}
	render := theme.Context{Mode: theme.Plain, Width: 120, Palette: theme.NewPalette(nil)}
	firstView := Render(render, data, State{Zoom: ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14"}, 33)
	secondView := Render(render, data, State{Zoom: ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14", SessionCursor: 1}, 33)
	firstPreview := ledgerRightPane(firstView, 80)
	secondPreview := ledgerRightPane(secondView, 80)
	if !strings.Contains(firstPreview, "first preview") || strings.Contains(firstPreview, "second preview") {
		t.Fatalf("first session preview =\n%s\nfull view:\n%s", firstPreview, firstView)
	}
	if !strings.Contains(secondPreview, "second preview") || strings.Contains(secondPreview, "first preview") {
		t.Fatalf("second session preview =\n%s\nfull view:\n%s", secondPreview, secondView)
	}
	for index, line := range strings.Split(secondView, "\n") {
		if width := lipgloss.Width(line); width != 120 {
			t.Fatalf("day master-detail line %d width=%d, want 120:\n%s", index+1, width, secondView)
		}
	}
}

func TestLedgerDayRollupBandFillsWideTallPane(t *testing.T) {
	sessions := make([]LedgerSession, 0, 20)
	for index := 0; index < 20; index++ {
		stamp := fmt.Sprintf("2026-07-14T%02d:00:00Z", 9+index%10)
		provider, project := history.ProviderCodex, "tokenomnom"
		if index%2 == 1 {
			provider, project = history.ProviderClaude, "billing-api"
		}
		sessions = append(sessions, LedgerSession{
			CatalogSession: historystore.CatalogSession{Provider: provider, Project: project, FirstTimestamp: &stamp},
			Tokens:         int64(900_000 + index*10_000), Cost: pricing.Money(5_000_000_000 + int64(index)*250_000_000),
			PricedTokens: int64(900_000 + index*10_000), ActivityTimestamp: stamp,
		})
	}
	data := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC, Sessions: sessions,
		Rows: []Row{{Key: "2026-07-14", Label: "Jul 14", Sessions: len(sessions)}},
		DayModels: []LedgerModel{
			{Provider: "codex", Model: "gpt-5.2", Tokens: 9_900_000, Cost: pricing.Money(72_500_000_000), PricedTokens: 9_900_000},
			{Provider: "claude", Model: "claude-sonnet", Tokens: 10_000_000, Cost: pricing.Money(75_000_000_000), PricedTokens: 10_000_000},
		},
		DayProjects: []LedgerProject{{Label: "tokenomnom", Sessions: 10, Share: 0.5}, {Label: "billing-api", Sessions: 10, Share: 0.5}},
	}
	state := State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}
	view := renderExpandedDayList(ledgerTestRender(), data, state, 110, 59)
	if lines := strings.Split(view, "\n"); len(lines) != 59 {
		t.Fatalf("wide tall day pane rendered %d lines, want 59:\n%s", len(lines), view)
	}
	for _, fragment := range []string{"MODELS ON THIS DAY", "PROJECTS ON THIS DAY", "gpt-5.2", "billing-api", "SESSION STARTS BY HOUR"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("day rollup missing %q:\n%s", fragment, view)
		}
	}
	blankRun := 0
	for index, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > 1 {
				t.Fatalf("day pane has a blank void at row %d:\n%s", index+1, view)
			}
			continue
		}
		blankRun = 0
	}
}

func TestLedgerDayRollupHeadersAlignWithValues(t *testing.T) {
	const width = 110
	leftWidth := (width - 2) / 2
	rightWidth := width - leftWidth - 2
	modelHeader := ledgerDayModelHeader(leftWidth)
	projectHeader := ledgerDayProjectHeader(rightWidth)
	modelLabelWidth := ledgerDayModelLabelWidth(leftWidth)
	projectLabelWidth := ledgerDayProjectLabelWidth(rightWidth)

	for _, column := range []struct {
		name string
		end  int
	}{
		{name: "COST", end: modelLabelWidth + 1 + 9},
		{name: "SHARE", end: modelLabelWidth + 1 + 9 + 1 + 5},
		{name: "TOKENS", end: modelLabelWidth + 1 + 9 + 1 + 5 + 1 + 9},
	} {
		if got := strings.Index(modelHeader, column.name) + len(column.name); got != column.end {
			t.Fatalf("model %s header edge=%d, want %d: %q", column.name, got, column.end, modelHeader)
		}
	}
	for _, column := range []struct {
		name string
		end  int
	}{
		{name: "SESSIONS", end: projectLabelWidth + 1 + 8},
		{name: "SHARE", end: projectLabelWidth + 1 + 8 + 1 + 6},
	} {
		if got := strings.Index(projectHeader, column.name) + len(column.name); got != column.end {
			t.Fatalf("project %s header edge=%d, want %d: %q", column.name, got, column.end, projectHeader)
		}
	}

	data := Data{
		DayModels:   []LedgerModel{{Provider: "codex", Model: "gpt-5.2", Tokens: 9_900_000, Cost: pricing.Money(72_500_000_000)}},
		DayProjects: []LedgerProject{{Label: "tokenomnom", Sessions: 10, Share: 1}},
	}
	view := renderLedgerDayRollups(ledgerTestRender(), data, width, 59)
	if !strings.Contains(view, strings.TrimRight(modelHeader, " ")) || !strings.Contains(view, strings.TrimRight(projectHeader, " ")) {
		t.Fatalf("day rollup does not use aligned headers:\n%s", view)
	}
}

func TestLedgerDayRollupBandFillsSparseWideTallPane(t *testing.T) {
	stamp := "2026-07-14T09:00:00Z"
	data := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC,
		Sessions: []LedgerSession{{
			CatalogSession: historystore.CatalogSession{Provider: history.ProviderCodex, Project: "tokenomnom", FirstTimestamp: &stamp},
			Tokens:         900_000, Cost: pricing.Money(5_000_000_000), PricedTokens: 900_000, ActivityTimestamp: stamp,
		}},
		Rows:        []Row{{Key: "2026-07-14", Label: "Jul 14", Sessions: 1}},
		DayModels:   []LedgerModel{{Provider: "codex", Model: "gpt-5.2", Tokens: 900_000, Cost: pricing.Money(5_000_000_000), PricedTokens: 900_000}},
		DayProjects: []LedgerProject{{Label: "tokenomnom", Sessions: 1, Share: 1}},
	}
	view := renderExpandedDayList(ledgerTestRender(), data, State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}, 110, 59)
	if lines := strings.Split(view, "\n"); len(lines) != 59 {
		t.Fatalf("sparse wide tall day pane rendered %d lines, want 59:\n%s", len(lines), view)
	}
	for _, fragment := range []string{"MODELS ON THIS DAY", "PROJECTS ON THIS DAY", "SESSION STARTS BY HOUR"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("sparse day rollup missing %q:\n%s", fragment, view)
		}
	}
}

func TestLedgerDayRollupFactsKeepUnknownCountsHonest(t *testing.T) {
	stamp := "2026-07-14T09:00:00Z"
	projects := make([]LedgerProject, 8)
	for index := range projects {
		projects[index] = LedgerProject{Label: fmt.Sprintf("project-%d", index), Sessions: 1, Share: 1.0 / 8}
	}
	data := Data{
		SessionDay: "2026-07-14", SessionIndexAvailable: true, Location: time.UTC, SessionsHaveMore: true,
		Sessions: []LedgerSession{{
			CatalogSession: historystore.CatalogSession{Provider: history.ProviderCodex, Project: "project-0", FirstTimestamp: &stamp},
			Tokens:         900_000, Cost: pricing.Money(5_000_000_000), PricedTokens: 900_000, ActivityTimestamp: stamp,
		}},
		Rows:            []Row{{Key: "2026-07-14", Label: "Jul 14", Sessions: 12}},
		DayProjects:     projects,
		DayProjectCount: 12,
	}
	view := renderExpandedDayList(ledgerTestRender(), data, State{Zoom: ZoomDay, ExpandedDay: "2026-07-14"}, 110, 59)
	for _, fragment := range []string{"projects represented 12", "sessions on day      ≥ 1"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("day rollup fact missing %q:\n%s", fragment, view)
		}
	}
}

func TestLedgerDayHourProfileUsesSessionStart(t *testing.T) {
	first, activity := "2026-07-14T09:00:00Z", "2026-07-14T17:00:00Z"
	profiles := ledgerDayHourProfiles([]LedgerSession{{
		CatalogSession:    historystore.CatalogSession{FirstTimestamp: &first},
		ActivityTimestamp: activity,
	}}, time.UTC)
	if profiles[9].Value != 1 || profiles[17].Value != 0 {
		t.Fatalf("hour profile = %+v, want the session start bucket", profiles)
	}
}

func TestMonthlyCaptionKeepsCompleteRecentEntries(t *testing.T) {
	months := make([]LedgerMonth, 0, 12)
	for month := 1; month <= 12; month++ {
		months = append(months, LedgerMonth{
			Key: fmt.Sprintf("2026-%02d", month), Label: time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("Jan 2006"),
			Codex: ProviderTotals{Cost: pricing.Money(month) * 1_000_000_000, Tokens: 1, PricedTokens: 1},
		})
	}
	caption := monthlyCaption(months, 80)
	entries := strings.Split(caption, "  ·  ")
	if len(entries) != 3 || !strings.HasPrefix(entries[0], "Oct 2026 ") || !strings.HasPrefix(entries[1], "Nov 2026 ") || !strings.HasPrefix(entries[2], "Dec 2026 ") {
		t.Fatalf("monthly caption = %q, want the three complete recent entries", caption)
	}
	if strings.Contains(caption, "Sep 2026") || strings.HasSuffix(caption, "2026") {
		t.Fatalf("monthly caption contains a dangling month label: %q", caption)
	}
}

func ledgerRightPane(view string, offset int) string {
	lines := strings.Split(view, "\n")
	for index, line := range lines {
		line = ansi.Strip(line)
		runes := []rune(line)
		if len(runes) > offset {
			lines[index] = string(runes[offset:])
		} else {
			lines[index] = ""
		}
	}
	return strings.Join(lines, "\n")
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
	view := Render(theme.Context{Mode: theme.Plain, Width: 192, Palette: theme.NewPalette(nil)}, data, State{Zoom: ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14"}, 66)
	for _, fragment := range []string{"$2,209.23", "Investigate the production latency", "Prepare the migration rollout plan"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("quest 116 snapshot missing %q:\n%s", fragment, view)
		}
	}
	t.Logf("\nSource: internal/tui/pages/ledger_test.go::TestQuest116AfterSnapshot\nCommand: GOFLAGS=-buildvcs=false go test ./internal/tui/pages -run TestQuest116AfterSnapshot -count=1 -v\n\n%s", view)
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
