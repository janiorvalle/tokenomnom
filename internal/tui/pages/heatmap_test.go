package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestRenderHeatmapExactFillAtReferenceViewports(t *testing.T) {
	data := heatmapTestData()
	for _, test := range []struct {
		name                    string
		rawWidth, width, height int
	}{
		{name: "wide tall", rawWidth: 192, width: 168, height: 59},
		{name: "standard", rawWidth: 120, width: 96, height: 33},
		{name: "floor", rawWidth: 80, width: 78, height: 18},
	} {
		t.Run(test.name, func(t *testing.T) {
			render := theme.Context{Mode: theme.Plain, Width: test.rawWidth, Palette: theme.NewPalette(nil)}
			view := RenderHeatmap(render, data, test.width, test.height)
			lines := strings.Split(view, "\n")
			if len(lines) != test.height {
				t.Fatalf("rendered %d lines, want %d:\n%s", len(lines), test.height, view)
			}
			for index, line := range lines {
				if width := lipgloss.Width(line); width != test.width {
					t.Fatalf("line %d width=%d, want %d: %q", index+1, width, test.width, line)
				}
			}
		})
	}
}

func TestRenderHeatmapWideUsesProfilesAndHasNoVoids(t *testing.T) {
	render := theme.Context{Mode: theme.Plain, Width: 192, Palette: theme.NewPalette(nil)}
	view := RenderHeatmap(render, heatmapTestData(), 168, 59)
	for _, fragment := range []string{"YEAR GRID", "MONTH Σ", "WEEKDAY PROFILE", "STREAKS & RECORDS", "MONTH TABLE", "BUSIEST DAY", "LONGEST STREAK", "ACTIVE / TOTAL"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("wide heatmap missing %q:\n%s", fragment, view)
		}
	}
	blankRun := 0
	for index, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > 3 {
				t.Fatalf("wide heatmap has a blank run ending at line %d:\n%s", index+1, view)
			}
			continue
		}
		blankRun = 0
	}
}

func TestRenderHeatmapWideKeepsFullYearAndCompleteSummary(t *testing.T) {
	render := theme.Context{Mode: theme.Plain, Width: 192, Palette: theme.NewPalette(nil)}
	view := RenderHeatmap(render, heatmapTestData(), 168, 59)
	grid := strings.Join(strings.Split(view, "\n")[:12], "\n")
	for _, fragment := range []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec", "range Jan 2026 - Dec 2026", "$84.00", "24 active", "Dec 2026", "6-day streak"} {
		if !strings.Contains(grid, fragment) {
			t.Errorf("wide year grid missing %q:\n%s", fragment, grid)
		}
	}
	if strings.Contains(grid, "showing ") {
		t.Fatalf("wide year grid should not clip a full-year fixture:\n%s", grid)
	}
	if strings.Count(grid, "MONTH Σ") != 1 {
		t.Fatalf("wide year grid has a duplicated summary header:\n%s", grid)
	}
	for _, fragment := range []string{"24 activ ", "6 streak"} {
		if strings.Contains(grid, fragment) {
			t.Fatalf("wide year grid truncated summary label %q:\n%s", fragment, grid)
		}
	}
}

func TestRenderHeatmapPanesKeepRoleContentDistinct(t *testing.T) {
	data := heatmapTestData()
	_, days := materializeHeatmapDays(data)
	stats := calculateHeatmapStats(days, data.UsesTokens)
	panes := map[string]string{
		"weekday profile":     renderWeekdayProfile(data, days, 46),
		"streaks and records": renderStreaksAndRecords(data, days, stats, 46, false),
		"month table":         renderMonthTable(data, days, 46),
	}
	if !strings.Contains(panes["streaks and records"], "RECENT STREAK HISTORY") {
		t.Fatalf("streak pane does not label its clipped history:\n%s", panes["streaks and records"])
	}
	if strings.Contains(panes["month table"], "\n  —") {
		t.Fatalf("month pane ends with an unlabeled placeholder row:\n%s", panes["month table"])
	}
	if got := len(strings.Split(renderMonthTable(data, days, 20), "\n")); got != 19 {
		t.Fatalf("short month pane rendered %d content rows, want 19", got)
	}
	compact := renderStreaksAndRecords(data, days, stats, 20, true)
	if strings.Index(compact, "RECENT STREAK HISTORY") > strings.Index(compact, "MONTH TABLE") || strings.Contains(compact, "\n  —") {
		t.Fatalf("compact streak and month sections are not ordered and filled:\n%s", compact)
	}
	seen := map[string]string{}
	for name, content := range panes {
		for _, line := range strings.Split(content, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || line == "—" {
				continue
			}
			if previous, ok := seen[line]; ok {
				t.Fatalf("%s repeats %q from %s", name, line, previous)
			}
			seen[line] = name
		}
	}
}

func TestRenderHeatmapMonthTableLabelsActiveMetric(t *testing.T) {
	data := heatmapTestData()
	_, days := materializeHeatmapDays(data)
	if view := renderMonthTable(data, days, 46); !strings.Contains(view, "MONTH       COST") || strings.Contains(view, "COST / TOKENS") {
		t.Fatalf("cost month table header is not metric-specific:\n%s", view)
	}
	data.UsesTokens = true
	if view := renderMonthTable(data, days, 46); !strings.Contains(view, "MONTH       TOKENS") || strings.Contains(view, "COST / TOKENS") {
		t.Fatalf("token month table header is not metric-specific:\n%s", view)
	}
}

func TestRenderHeatmapStandardKeepsLatestActivityAndMonth(t *testing.T) {
	render := theme.Context{Mode: theme.Plain, Width: 120, Palette: theme.NewPalette(nil)}
	view := RenderHeatmap(render, heatmapTestData(), 96, 33)
	for _, fragment := range []string{"MONTH Σ", "Dec 2026", "RECENT DAILY ACTIVITY", "Dec 31"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("standard heatmap dropped latest %q:\n%s", fragment, view)
		}
	}
}

func TestQuest149HeatmapReferenceFrames(t *testing.T) {
	data := heatmapTestData()
	for _, test := range []struct {
		name                string
		rawWidth, rawHeight int
		width, height       int
	}{
		{name: "wide+tall", rawWidth: 192, rawHeight: 66, width: 168, height: 59},
		{name: "standard", rawWidth: 120, rawHeight: 40, width: 96, height: 33},
		{name: "floor", rawWidth: 80, rawHeight: 24, width: 78, height: 18},
	} {
		render := theme.Context{Mode: theme.Plain, Width: test.rawWidth, Palette: theme.NewPalette(nil)}
		view := RenderHeatmap(render, data, test.width, test.height)
		t.Logf("FRAME: Heatmap %s · %dx%d\nSource: internal/tui/pages/heatmap_test.go::TestQuest149HeatmapReferenceFrames\nCommand: go test ./internal/tui/pages -run TestQuest149HeatmapReferenceFrames -count=1 -v\n\n%s", test.name, test.rawWidth, test.rawHeight, view)
	}
}

func TestHeatmapProfilesDeriveMonthAndStreakMetricsFromDailyRows(t *testing.T) {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	days := []HeatmapDay{
		{Date: from, TotalTokens: 10, Cost: 10_000_000, PricedTokens: 10},
		{Date: from.AddDate(0, 0, 1), TotalTokens: 20, Cost: 20_000_000, PricedTokens: 20},
		{Date: from.AddDate(0, 0, 2), TotalTokens: 30, Cost: 30_000_000, PricedTokens: 30},
		{Date: from.AddDate(0, 1, 4), TotalTokens: 40, Cost: 40_000_000, PricedTokens: 40},
	}
	data := HeatmapData{Window: HeatmapWindow{From: from, To: from.AddDate(0, 1, 4)}, Days: days}
	view := RenderHeatmap(theme.Context{Mode: theme.Plain, Width: 192, Palette: theme.NewPalette(nil)}, data, 168, 59)
	for _, fragment := range []string{"LONGEST STREAK  3 days", "ACTIVE / TOTAL   4 / 36 days", "Jan 2026", "Feb 2026"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("derived heatmap metric missing %q:\n%s", fragment, view)
		}
	}
}

func heatmapTestData() HeatmapData {
	from := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.December, 31, 0, 0, 0, 0, time.UTC)
	days := make([]HeatmapDay, 0, 365)
	for date := from; !date.After(to); date = date.AddDate(0, 0, 1) {
		day := HeatmapDay{Date: date}
		if date.Day()%4 != 0 {
			day.TotalTokens = int64(date.YearDay() * 1_000)
			day.Cost = pricing.Money(int64(date.YearDay()) * 10_000_000)
			day.PricedTokens = day.TotalTokens
			day.Level = date.Day()%4 + 1
		}
		days = append(days, day)
	}
	return HeatmapData{Window: HeatmapWindow{From: from, To: to}, Days: days}
}
