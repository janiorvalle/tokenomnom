package pages

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestRenderDailyWideTallFillsTheDenseDeskContract(t *testing.T) {
	data := testDailyPageData()
	render := testDailyRender(168)
	view := RenderDaily(render, data, 192, 66, 59, 0)
	lines := strings.Split(view, "\n")
	if len(lines) != 59 {
		t.Fatalf("wide Daily lines=%d, want 59", len(lines))
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 168 {
			t.Fatalf("wide Daily line %d width=%d, want 168: %q", index+1, width, line)
		}
		if strings.TrimSpace(line) == "" {
			t.Fatalf("wide Daily line %d is blank", index+1)
		}
	}
	for _, fragment := range []string{"COST / DAY", "DAY DETAIL", "TOP MODELS BY COST", "PROJECTS", "30-DAY TRENDS", "LAST 10 DAYS", "SESSIONS", "FIRST PROMPT"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("wide Daily missing %q:\n%s", fragment, view)
		}
	}
	if got := DailyDetailMaxOffset(data, 168, 192, 66, 59); got != 0 {
		t.Fatalf("wide Daily detail offset=%d, want 0", got)
	}
}

func TestRenderDailyStandardUsesTwoAnalysisPanesAndTopThree(t *testing.T) {
	data := testDailyPageData()
	render := testDailyRender(98)
	view := RenderDaily(render, data, 120, 40, 33, 0)
	lines := strings.Split(view, "\n")
	if len(lines) != 33 {
		t.Fatalf("standard Daily lines=%d, want 33", len(lines))
	}
	for _, fragment := range []string{"PROJECTS · 30-DAY TRENDS", "top 3 by cost of 20", "↓ 17 more sessions", "Cost attribution unavailable"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("standard Daily missing %q:\n%s", fragment, view)
		}
	}
	if strings.Contains(view, "LAST 10 DAYS · RESCALED") {
		t.Fatalf("standard Daily rendered the wide mini-chart:\n%s", view)
	}
}

func TestDailyTrendLabelFollowsSelectedRange(t *testing.T) {
	data := testDailyPageData()
	data.RangeStart, data.RangeEnd = "2026-05-04", "2026-08-01"
	if got := dailyTrendRangeLabel(data); got != "90-DAY TRENDS" {
		t.Fatalf("trend range label=%q, want 90-DAY TRENDS", got)
	}
	data.RangeStart, data.RangeEnd = "", ""
	if got := dailyTrendRangeLabel(data); got != "ALL-TIME TRENDS" {
		t.Fatalf("all-time trend range label=%q, want ALL-TIME TRENDS", got)
	}
}

func TestDailySessionComparisonKeepsFractionalAverage(t *testing.T) {
	line := dailySessionsComparisonLine(DailyValue{Sessions: 1}, 0.5)
	if !strings.Contains(line, "0.5") || !strings.Contains(line, "+100.0%") {
		t.Fatalf("fractional session average was truncated: %q", line)
	}
}

func TestRenderDailyFloorKeepsChartAndWarningBand(t *testing.T) {
	data := testDailyPageData()
	render := testDailyRender(78)
	view := RenderDaily(render, data, 80, 24, 18, 0)
	lines := strings.Split(view, "\n")
	if len(lines) != 18 {
		t.Fatalf("floor Daily lines=%d, want 18", len(lines))
	}
	for _, fragment := range []string{"COST / DAY", "DAY DETAIL", "PROVIDER SPLIT", "TOP MODELS BY COST", "Cost attribution unavailable"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("floor Daily missing %q:\n%s", fragment, view)
		}
	}
	if strings.Contains(view, "SESSIONS ·") {
		t.Fatalf("floor Daily rendered a sessions band:\n%s", view)
	}
	if got := DailyDetailMaxOffset(data, 78, 80, 24, 18); got == 0 {
		t.Fatalf("floor Daily reported no detail overflow")
	}
}

func TestRenderDailyRollsUpAnOverwideSeriesExplicitly(t *testing.T) {
	data := testDailyPageData()
	data.Rows = append(data.Rows, DailyPoint{Date: "2026-08-01", Total: DailyValue{Cost: pricing.Money(1_000_000_000), PricedTokens: 1}})
	view := RenderDaily(testDailyRender(58), data, 60, 24, 18, 0)
	if !strings.Contains(view, "31-day rollup") {
		t.Fatalf("overwide Daily series did not disclose its rollup:\n%s", view)
	}
	if got := strings.Count(view, "2026-"); got > 2 {
		t.Fatalf("overwide Daily chart leaked too many raw date labels: %d", got)
	}
}

func TestRenderDailySessionRowsFollowAvailableWideHeight(t *testing.T) {
	data := testDailyPageData()
	short := RenderDaily(testDailyRender(168), data, 192, 50, 43, 0)
	tall := RenderDaily(testDailyRender(168), data, 192, 66, 59, 0)
	shortRows := strings.Count(short, "12:00")
	tallRows := strings.Count(tall, "12:00")
	if shortRows == 0 || tallRows <= shortRows {
		t.Fatalf("wide session rows did not grow with body height: short=%d tall=%d", shortRows, tallRows)
	}
}

func TestRenderDailySanitizesSessionMetadataAndDisclosesPartialRankings(t *testing.T) {
	data := testDailyPageData()
	data.Sessions.Rows[0].Project = "unsafe\x1b]8;;https://example.test\x07project"
	data.Sessions.Rows[0].Model = "model\x1b[31m"
	data.Sessions.Rows[0].SessionID = "session\x1b[2J"
	data.Sessions.HasMore = true
	data.Sessions.TotalKnown = true
	data.Sessions.Total = 120
	view := RenderDaily(testDailyRender(98), data, 120, 40, 33, 0)
	if strings.Contains(view, "\x1b") {
		t.Fatalf("Daily session metadata reached the terminal unescaped:\n%q", view)
	}
	if !strings.Contains(view, "first 3 loaded of 120") || !strings.Contains(view, "Project ranking unavailable") {
		t.Fatalf("Daily partial ranking disclosure missing:\n%s", view)
	}
}

func TestRenderDailyUsesExplicitTokenFallbackLabels(t *testing.T) {
	data := testDailyPageData()
	data.UsesTokens = true
	data.DetailUsesTokens = true
	lines := dailyDetailLines(data, 72, false)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "cost") || !strings.Contains(joined, "—") || !strings.Contains(joined, "tokens") {
		t.Fatalf("token fallback comparison labels are inconsistent:\n%s", joined)
	}
	trends := strings.Join(dailyProjectsAndTrends(data, 48), "\n")
	if !strings.Contains(trends, "cost/day · unavailable") || !strings.Contains(trends, "claude share") {
		t.Fatalf("token fallback trends are inconsistent:\n%s", trends)
	}
}

func TestRenderDailyFallsBackToTokensForIncompleteAttribution(t *testing.T) {
	data := testDailyPageData()
	for index := range data.Sessions.Rows {
		data.Sessions.Rows[index].AttributionStatus = "incomplete"
	}
	view := RenderDaily(testDailyRender(98), data, 120, 40, 33, 0)
	if !strings.Contains(view, "top 3 by tokens of 20") {
		t.Fatalf("incomplete session attribution still ranked by cost:\n%s", view)
	}
	if !strings.Contains(view, "—") {
		t.Fatalf("incomplete session attribution still displayed a dollar amount:\n%s", view)
	}
	trends := strings.Join(dailyProjectsAndTrends(data, 48), "\n")
	if !strings.Contains(trends, "181.0M") {
		t.Fatalf("project summary did not fall back to tokens:\n%s", trends)
	}
}

func TestRenderDailyKeepsCompleteCostRankingsWithPartialRows(t *testing.T) {
	data := testDailyPageData()
	data.Sessions.Rows[0].AttributionStatus = "incomplete"
	view := RenderDaily(testDailyRender(98), data, 120, 40, 33, 0)
	if !strings.Contains(view, "top 3 by cost of 20") || !strings.Contains(view, "—") {
		t.Fatalf("partial attribution changed the complete cost ranking:\n%s", view)
	}
	trends := strings.Join(dailyProjectsAndTrends(data, 48), "\n")
	if !strings.Contains(trends, "$17.10") {
		t.Fatalf("project summary included the partial cost:\n%s", trends)
	}
}

func TestQuest146DailyFrames(t *testing.T) {
	data := testDailyPageData()
	for _, size := range []struct {
		name           string
		terminalWidth  int
		terminalHeight int
		contentWidth   int
		bodyHeight     int
	}{
		{name: "wide+tall", terminalWidth: 192, terminalHeight: 66, contentWidth: 168, bodyHeight: 59},
		{name: "standard", terminalWidth: 120, terminalHeight: 40, contentWidth: 98, bodyHeight: 33},
		{name: "floor", terminalWidth: 80, terminalHeight: 24, contentWidth: 78, bodyHeight: 18},
	} {
		t.Run(size.name, func(t *testing.T) {
			view := RenderDaily(testDailyRender(size.contentWidth), data, size.terminalWidth, size.terminalHeight, size.bodyHeight, 0)
			t.Logf("FRAME: Daily %s %dx%d\nSource: internal/tui/pages/daily_test.go::TestQuest146DailyFrames\nCommand: go test ./internal/tui/pages -run TestQuest146DailyFrames -count=1 -v\n\n%s", size.name, size.terminalWidth, size.terminalHeight, view)
		})
	}
}

func testDailyPageData() DailyPageData {
	rows := make([]DailyPoint, 0, 30)
	for index := 0; index < 30; index++ {
		day := index + 1
		rows = append(rows, DailyPoint{
			Date:   "2026-07-" + twoDigits(day),
			Total:  DailyValue{Cost: pricing.Money((index + 1) * 10_000_000_000), Tokens: int64((index + 1) * 1_000_000), PricedTokens: 1},
			Codex:  DailyValue{Cost: pricing.Money((index + 1) * 6_000_000_000), Tokens: int64((index + 1) * 600_000), PricedTokens: 1},
			Claude: DailyValue{Cost: pricing.Money((index + 1) * 4_000_000_000), Tokens: int64((index + 1) * 400_000), PricedTokens: 1},
		})
	}
	rows[len(rows)-1].Selected = true
	sessions := make([]DailySession, 0, 20)
	for index := 0; index < 20; index++ {
		sessions = append(sessions, DailySession{
			Time: "12:00", Provider: "codex", Project: "project-a", SessionID: "ses_demo", Model: "gpt-test",
			Tokens: int64(10_000_000 - index*100_000), Cost: pricing.Money(1_000_000_000 - int64(index)*10_000_000), PricedTokens: 1,
			Prompt: "Synthetic prompt fixture", PromptCount: index + 1, AttributionStatus: "complete",
		})
	}
	return DailyPageData{
		Rows: rows, TrendRows: rows, SelectedDate: rows[len(rows)-1].Date,
		Detail: DailyDetail{
			Date:  rows[len(rows)-1].Date,
			Value: DailyValue{Cost: pricing.Money(200_000_000_000), Tokens: 20_000_000, PricedTokens: 1, Sessions: 20},
			Providers: []DailyProvider{
				{Provider: "codex", Value: DailyValue{Cost: pricing.Money(120_000_000_000), Tokens: 12_000_000, PricedTokens: 1, Sessions: 14}},
				{Provider: "claude", Value: DailyValue{Cost: pricing.Money(80_000_000_000), Tokens: 8_000_000, PricedTokens: 1, Sessions: 6}},
			},
			Models: []DailyModel{{Provider: "codex", Model: "gpt-test", Value: DailyValue{Cost: pricing.Money(120_000_000_000), Tokens: 12_000_000, PricedTokens: 1}}},
		},
		Sessions: DailySessionData{Rows: sessions, Total: 20, Warning: "Cost attribution unavailable for 2 of 20 sessions; restore the source and rerun history index."},
		Average:  DailyValue{Cost: pricing.Money(100_000_000_000), Tokens: 10_000_000, PricedTokens: 1}, AverageSessions: 10,
		Peak: DailyValue{Cost: pricing.Money(300_000_000_000), Tokens: 30_000_000, PricedTokens: 1}, PeakDate: "2026-07-09",
		RangeStart: "2026-07-03", RangeEnd: "2026-08-01",
	}
}

func testDailyRender(width int) theme.Context {
	return theme.Context{Mode: theme.Plain, Width: width, Palette: theme.NewPalette(nil)}
}

func twoDigits(value int) string {
	if value < 10 {
		return "0" + string(rune('0'+value))
	}
	return string(rune('0'+value/10)) + string(rune('0'+value%10))
}
