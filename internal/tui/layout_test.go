package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLayoutTiersUseContractBoundaries(t *testing.T) {
	for _, test := range []struct {
		value int
		want  WidthTier
	}{
		{99, WidthFloor}, {100, WidthStandard}, {159, WidthStandard}, {160, WidthWide},
	} {
		if got := WidthTierFor(test.value); got != test.want {
			t.Fatalf("width %d = %s, want %s", test.value, got, test.want)
		}
	}
	for _, test := range []struct {
		value int
		want  HeightTier
	}{
		{29, HeightShort}, {30, HeightStandard}, {49, HeightStandard}, {50, HeightTall},
	} {
		if got := HeightTierFor(test.value); got != test.want {
			t.Fatalf("height %d = %s, want %s", test.value, got, test.want)
		}
	}
}

func TestCockpitLayoutExactArithmeticAtReferenceSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 40}, {192, 66}} {
		layout := newCockpitLayout(size.width, size.height)
		if layout.chrome.Total()+layout.bodyHeight != layout.height {
			t.Fatalf("%dx%d chrome=%d body=%d", size.width, size.height, layout.chrome.Total(), layout.bodyHeight)
		}
		if layout.showRail != (size.width >= standardWidth) {
			t.Fatalf("%dx%d showRail=%v", size.width, size.height, layout.showRail)
		}
		if layout.paneWidth <= 0 || layout.bodyHeight <= 0 {
			t.Fatalf("%dx%d invalid pane/body dimensions: %+v", size.width, size.height, layout)
		}
	}
	if got := ContentWidth(80); got != 78 {
		t.Fatalf("floor content width=%d, want full inner width 78", got)
	}
	if got := railWidthFor(WidthWide, 190); got != 20 {
		t.Fatalf("wide rail width=%d, want fixed 20", got)
	}
	if got := railWidthFor(WidthStandard, 118); got != 20 {
		t.Fatalf("standard rail width=%d, want fixed 20", got)
	}
}

func TestRenderBandFillsEveryCellAndKeepsPaneArithmetic(t *testing.T) {
	render := testRender()
	view := RenderBand(render, NewBand("analysis", 40, 8,
		NewPane("left", "one\ntwo"),
		NewPane("right", "three\nfour"),
	))
	lines := strings.Split(view, "\n")
	if len(lines) != 8 {
		t.Fatalf("band lines=%d, want 8:\n%s", len(lines), view)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 40 {
			t.Fatalf("band line %d width=%d, want 40:\n%s", index+1, width, view)
		}
	}
	if !strings.Contains(view, "ANALYSIS") || !strings.Contains(view, "LEFT") || !strings.Contains(view, "RIGHT") {
		t.Fatalf("band titles missing:\n%s", view)
	}
	explicit := RenderBand(render, Band{
		Width: 40, Height: 2, Gap: 2,
		Panes: []Pane{{Width: 5, Content: "a"}, {Width: 5, Content: "b"}},
	})
	for index, line := range strings.Split(explicit, "\n") {
		if width := lipgloss.Width(line); width != 40 {
			t.Fatalf("explicit band line %d width=%d, want 40: %q", index+1, width, line)
		}
	}
}

func TestSharedDensityPrimitivesStayBounded(t *testing.T) {
	if got := Sparkline([]float64{1, 2, 3, 4}, 3); lipgloss.Width(got) != 3 {
		t.Fatalf("sparkline width=%d, value=%q", lipgloss.Width(got), got)
	}
	if got := IntensityCells([]float64{0, 1, 2, 3}, 3); lipgloss.Width(got) != 3 {
		t.Fatalf("intensity width=%d, value=%q", lipgloss.Width(got), got)
	}
	if got := FullRangeChartWidth(30, ChartColumnWidth(160, 30)); got < 30 {
		t.Fatalf("full-range chart width=%d", got)
	}
	if got := WarningRow(testRender(), "history index is stale", 30); lipgloss.Width(got) != 30 {
		t.Fatalf("warning row width=%d, value=%q", lipgloss.Width(got), got)
	}
}

func TestRailDropsOptionalBlocksFromTheBottom(t *testing.T) {
	model := loadedTestModel()
	model.request.Width, model.request.Height = 120, 30
	layout := newCockpitLayout(model.request.Width, model.request.Height)
	view := model.railView(layout)
	if !strings.Contains(view, "FILTERS") {
		t.Fatalf("required rail block missing:\n%s", view)
	}
	if strings.Contains(view, "PROJECTS") {
		t.Fatalf("bottom rail block should drop first in a short standard rail:\n%s", view)
	}
	model.request.Width = 80
	if view := model.railView(newCockpitLayout(model.request.Width, model.request.Height)); view != "" {
		t.Fatalf("floor rail should be removed: %q", view)
	}
	model.router = newRouter(NewVaultPage(), NewSystemPage(), NewHistorySearchPage(HistorySearchOptions{}))
	model.request.Width, model.request.Height = 120, 24
	shortRail := model.railView(newCockpitLayout(model.request.Width, model.request.Height))
	if !strings.Contains(shortRail, "FILTERS") || !strings.Contains(shortRail, "provider") || !strings.Contains(shortRail, "range") {
		t.Fatalf("short standard rail truncated required filters:\n%s", shortRail)
	}
}

func TestRailUsesContractBlocksAndChromeJunctions(t *testing.T) {
	model := realisticEvidenceModel()
	model.request.Width, model.request.Height = 192, 66
	layout := newCockpitLayout(model.request.Width, model.request.Height)
	view := model.View()
	for _, fragment := range []string{"today $2,209.23", "MIX · 30D", "Codex 72%", "PROJECTS 30D", "alpha 50%"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("rail missing %q:\n%s", fragment, view)
		}
	}
	if !strings.Contains(view, "┬") || !strings.Contains(view, "┴") {
		t.Fatalf("rail junctions missing:\n%s", view)
	}
	if !strings.Contains(view, "192x66 · wide + tall") {
		t.Fatalf("size badge was not moved to the disclaimer row:\n%s", view)
	}
	if lipgloss.Width(model.chromeDividerView(layout, '┬')) != layout.innerWidth {
		t.Fatalf("top divider width=%d, want %d", lipgloss.Width(model.chromeDividerView(layout, '┬')), layout.innerWidth)
	}
}

func TestQuest145FoundationFrames(t *testing.T) {
	for _, size := range []struct{ width, height int }{{192, 66}, {120, 40}, {80, 24}} {
		model := realisticEvidenceModel()
		model.request.Width, model.request.Height = size.width, size.height
		model.render.Width = size.width
		view := model.View()
		lines := strings.Split(view, "\n")
		if len(lines) != size.height {
			t.Fatalf("%dx%d rendered %d rows", size.width, size.height, len(lines))
		}
		for index, line := range lines {
			if width := lipgloss.Width(line); width != size.width {
				t.Fatalf("%dx%d row %d width=%d", size.width, size.height, index+1, width)
			}
		}
		t.Logf("FRAME: foundation %dx%d\nSource: internal/tui/layout_test.go::TestQuest145FoundationFrames\nCommand: go test ./internal/tui -run TestQuest145FoundationFrames -count=1 -v\n\n%s", size.width, size.height, view)
	}
}
