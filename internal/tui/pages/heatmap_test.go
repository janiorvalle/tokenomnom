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

func TestRenderHeatmapStandardKeepsLatestActivityAndMonth(t *testing.T) {
	render := theme.Context{Mode: theme.Plain, Width: 120, Palette: theme.NewPalette(nil)}
	view := RenderHeatmap(render, heatmapTestData(), 96, 33)
	for _, fragment := range []string{"MONTH Σ", "Dec 2026", "Dec 31"} {
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
