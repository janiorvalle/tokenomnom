package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestHeatmapLevelsUseNonzeroQuartiles(t *testing.T) {
	tests := []struct {
		name   string
		values []int64
		want   []int
	}{
		{name: "outlier", values: []int64{0, 1, 2, 3, 1000}, want: []int{0, 1, 2, 3, 4}},
		{name: "single active day", values: []int64{0, 7, 0}, want: []int{0, 4, 0}},
		{name: "all zero", values: []int64{0, 0, 0}, want: []int{0, 0, 0}},
		{name: "ties bias upward", values: []int64{1, 1, 2, 3}, want: []int{2, 2, 3, 4}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			days := heatmapDaysWithValues(test.values, false)
			assignHeatmapLevels(days, false)
			for index, want := range test.want {
				if days[index].Level != want {
					t.Fatalf("day %d level = %d, want %d; days = %+v", index, days[index].Level, want, days)
				}
			}
		})
	}
}

func TestHeatmapAllUnpricedFallsBackToTokenLevels(t *testing.T) {
	days := heatmapDaysWithValues([]int64{0, 10, 20, 30, 40}, true)
	assignHeatmapLevels(days, true)
	for index, want := range []int{0, 1, 2, 3, 4} {
		if days[index].Level != want {
			t.Fatalf("token day %d level = %d, want %d", index, days[index].Level, want)
		}
	}
}

func TestHeatmapStatsBusiestDayAndWindowBoundedStreak(t *testing.T) {
	days := []heatmapDay{
		{Date: mustHeatmapDate(t, "2026-07-10"), Cost: pricing.Money(10)},
		{Date: mustHeatmapDate(t, "2026-07-11"), Cost: pricing.Money(20)},
		{Date: mustHeatmapDate(t, "2026-07-12")},
		{Date: mustHeatmapDate(t, "2026-07-13"), Cost: pricing.Money(28_474_650_000_000)},
		{Date: mustHeatmapDate(t, "2026-07-14"), Cost: pricing.Money(30)},
		{Date: mustHeatmapDate(t, "2026-07-15"), Cost: pricing.Money(40)},
	}
	stats := calculateHeatmapStats(days, false)
	if stats.ActiveDays != 5 || stats.BusiestDate != "2026-07-13" || stats.BusiestCost != days[3].Cost || stats.LongestStreak != 3 {
		t.Fatalf("stats = %+v", stats)
	}
	if got := heatmapCaption(heatmapReport{Stats: stats}); !strings.Contains(got, "2026-07-13 · $28,474.65") || !strings.Contains(got, "longest streak 3 days") {
		t.Fatalf("caption = %q", got)
	}
}

func TestHeatmapPlainLayoutGolden(t *testing.T) {
	window := heatmapWindow{From: mustHeatmapDate(t, "2026-02-04"), To: mustHeatmapDate(t, "2026-02-10")}
	report := heatmapReport{
		Window: window,
		Metric: "cost_usd",
		Days: []heatmapDay{
			{Date: mustHeatmapDate(t, "2026-02-04"), Level: 1},
			{Date: mustHeatmapDate(t, "2026-02-08"), Level: 4},
			{Date: mustHeatmapDate(t, "2026-02-10"), Level: 2},
		},
		Stats: heatmapStats{ActiveDays: 3, BusiestDate: "2026-02-08", LongestStreak: 1},
	}
	got := renderHeatmap(theme.Context{Mode: theme.Plain, Width: 80}, report)
	want := "" +
		"    Feb\n" +
		"      █ \n" +
		"Mon   · \n" +
		"      ▒ \n" +
		"Wed ░   \n" +
		"    ·   \n" +
		"Fri ·   \n" +
		"    ·   \n" +
		"Less ·░▒▓█ More\n" +
		"3 active days · total cost $0.00 · busiest 2026-02-08 · $0.00 · longest streak 1 day\n"
	if got != want {
		t.Fatalf("plain heatmap mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestHeatmapMonthLabelsAlignToStartingWeek(t *testing.T) {
	window := heatmapWindow{From: mustHeatmapDate(t, "2026-04-26"), To: mustHeatmapDate(t, "2026-06-06")}
	output := renderHeatmap(theme.Context{Mode: theme.Plain, Width: 80}, heatmapReport{
		Window: window,
		Days:   heatmapDaysForWindow(window),
	})
	monthLine := strings.Split(output, "\n")[0]
	if monthLine != "    May       Jun" {
		t.Fatalf("month labels are not aligned to their starting weeks: %q", monthLine)
	}
}

func TestHeatmapCellWidthFallbackAndMonthTruncation(t *testing.T) {
	window := heatmapWindow{From: mustHeatmapDate(t, "2026-01-01"), To: mustHeatmapDate(t, "2026-12-31")}
	report := heatmapReport{Window: window, Days: heatmapDaysForWindow(window)}
	oneColumn := renderHeatmap(theme.Context{Mode: theme.Plain, Width: 80}, report)
	lines := strings.Split(oneColumn, "\n")
	if len([]rune(lines[1])) != 57 {
		t.Fatalf("one-column Sunday row width = %d, want 57: %q", len([]rune(lines[1])), lines[1])
	}
	if strings.Contains(oneColumn, "showing ") {
		t.Fatalf("80-column heatmap unexpectedly truncated:\n%s", oneColumn)
	}

	truncated := renderHeatmap(theme.Context{Mode: theme.Plain, Width: 20}, report)
	if !strings.Contains(truncated, "showing Oct–Dec of Jan 2026–Dec 2026") {
		t.Fatalf("truncation line or whole-month selection changed:\n%s", truncated)
	}
	for _, line := range strings.Split(truncated, "\n")[:8] {
		if len([]rune(line)) > 20 {
			t.Fatalf("truncated grid line exceeds width: %q", line)
		}
	}
}

func TestHeatmapDateWindowsAndYearValidation(t *testing.T) {
	today := mustHeatmapDate(t, "2026-07-19")
	window, err := heatmapDateWindow(0, today)
	if err != nil || window.From.Format(heatmapDateLayout) != "2025-07-20" || window.To.Format(heatmapDateLayout) != "2026-07-19" {
		t.Fatalf("trailing window = %+v, %v", window, err)
	}
	window, err = heatmapDateWindow(2024, today)
	if err != nil || window.From.Format(heatmapDateLayout) != "2024-01-01" || window.To.Format(heatmapDateLayout) != "2024-12-31" {
		t.Fatalf("leap-year window = %+v, %v", window, err)
	}
	if _, err := heatmapDateWindow(999, today); err == nil {
		t.Fatal("three-digit year was accepted")
	}

	_, codexDir, claudeDir := seedReportStore(t)
	for _, args := range [][]string{{"heatmap", "--year", "0", "--no-sync"}, {"heatmap", "--year", "2026", "--since", "2026-01-01", "--no-sync"}} {
		if _, err := executeReport(args, codexDir, claudeDir); err == nil {
			t.Fatalf("invalid args were accepted: %v", args)
		}
	}
}

func TestHeatmapJSONEnvelopeDaysAndLevels(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	output, err := executeReport([]string{"heatmap", "--year", "2026", "--format", "json", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeEnvelope(t, output)
	assertEnvelope(t, envelope, "heatmap")
	var data jsonHeatmapData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Window.From != "2026-01-01" || data.Window.To != "2026-12-31" || data.Metric != "cost_usd" || len(data.Days) != 365 {
		t.Fatalf("heatmap JSON header = %+v; days = %d", data, len(data.Days))
	}
	var priced *jsonHeatmapDay
	for index := range data.Days {
		if data.Days[index].Date == "2026-02-01" {
			priced = &data.Days[index]
		}
	}
	if priced == nil || priced.Level != 4 || priced.CostUSD != 0.18 || priced.TotalTokens != 205_350 {
		t.Fatalf("priced heatmap day = %+v", priced)
	}
	if data.Stats.ActiveDays != 1 || data.Stats.BusiestDay.Date == nil || *data.Stats.BusiestDay.Date != "2026-02-01" || data.Stats.LongestStreak != 1 {
		t.Fatalf("heatmap JSON stats = %+v", data.Stats)
	}
}

func TestBuildHeatmapReportFallsBackWhenEveryDayIsUnpriced(t *testing.T) {
	window := heatmapWindow{From: mustHeatmapDate(t, "2026-07-18"), To: mustHeatmapDate(t, "2026-07-19")}
	report := buildHeatmapReport(window, []store.DailyRow{
		{Date: "2026-07-18", TokenTotals: store.TokenTotals{Total: 10}},
		{Date: "2026-07-19", TokenTotals: store.TokenTotals{Total: 20}},
	}, reportCosts{ByDate: map[string]aggregateCost{}})
	if report.Metric != "tokens" || !report.UsesTokens || report.Stats.ActiveDays != 2 || report.Stats.BusiestDate != "2026-07-19" {
		t.Fatalf("token fallback report = %+v", report)
	}
	if report.Days[0].Level != 2 || report.Days[1].Level != 4 {
		t.Fatalf("token fallback levels = %+v", report.Days)
	}
	if caption := heatmapCaption(report); !strings.Contains(caption, "showing tokens (unpriced)") || !strings.Contains(caption, "2026-07-19 · 20 tokens") {
		t.Fatalf("token fallback caption = %q", caption)
	}
}

func heatmapDaysWithValues(values []int64, tokens bool) []heatmapDay {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	days := make([]heatmapDay, len(values))
	for index, value := range values {
		days[index].Date = start.AddDate(0, 0, index)
		if tokens {
			days[index].TotalTokens = value
		} else {
			days[index].Cost = pricing.Money(value)
		}
	}
	return days
}

func heatmapDaysForWindow(window heatmapWindow) []heatmapDay {
	var days []heatmapDay
	for date := window.From; !date.After(window.To); date = date.AddDate(0, 0, 1) {
		days = append(days, heatmapDay{Date: date})
	}
	return days
}

func mustHeatmapDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(heatmapDateLayout, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
