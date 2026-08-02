package tui

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

const (
	framePadding = 1
	gridGap      = 2

	// The tier thresholds are part of the TUI contract. Keep them independent
	// from the minimum terminal guard so a caller can classify any viewport.
	standardWidth  = 100
	wideWidth      = 160
	standardHeight = 30
	tallHeight     = 50

	footerHeight = 3
)

// WidthTier is the horizontal density tier used by page renderers.
type WidthTier uint8

const (
	WidthFloor WidthTier = iota
	WidthStandard
	WidthWide
)

func (tier WidthTier) String() string {
	switch tier {
	case WidthStandard:
		return "standard"
	case WidthWide:
		return "wide"
	default:
		return "floor"
	}
}

// HeightTier is the vertical density tier used by page renderers.
type HeightTier uint8

const (
	HeightShort HeightTier = iota
	HeightStandard
	HeightTall
)

func (tier HeightTier) String() string {
	switch tier {
	case HeightStandard:
		return "standard"
	case HeightTall:
		return "tall"
	default:
		return "short"
	}
}

// WidthTierFor classifies a terminal width without applying a minimum-size
// clamp. This is useful to page renderers that need to choose a compact mode.
func WidthTierFor(width int) WidthTier {
	switch {
	case width >= wideWidth:
		return WidthWide
	case width >= standardWidth:
		return WidthStandard
	default:
		return WidthFloor
	}
}

// HeightTierFor classifies a terminal height without applying a minimum-size
// clamp.
func HeightTierFor(height int) HeightTier {
	switch {
	case height >= tallHeight:
		return HeightTall
	case height >= standardHeight:
		return HeightStandard
	default:
		return HeightShort
	}
}

// LayoutTiers is the pair of independent viewport classifications.
type LayoutTiers struct {
	Width  WidthTier
	Height HeightTier
}

// ChromeRows describes the fixed rows outside the page body. The values add
// up with BodyHeight to the exact terminal height.
type ChromeRows struct {
	TopBar  int
	Summary int
	Divider int
	Status  int
	Footer  int
}

func (rows ChromeRows) Total() int {
	return rows.TopBar + rows.Summary + rows.Divider + rows.Status + rows.Footer
}

func globalChromeRows(tiers LayoutTiers) ChromeRows {
	// The badge lives on the disclaimer row. The two reserved divider rows keep
	// the body boundary stable across tiers, including the floor layout.
	footer := footerHeight - 1 // the bottom divider is counted separately
	if tiers.Width == WidthFloor {
		footer-- // floor combines hints and the badge into one final row
	}
	return ChromeRows{TopBar: 1, Summary: 1, Divider: 2, Status: 1, Footer: footer}
}

// Pane is a borderless rectangular content region inside a Band. Pane titles
// use one row and a trailing rule; content is never allowed to resize it.
type Pane struct {
	Title   string
	Content string
	Width   int
	Height  int
}

// Band is a horizontal composition of panes with a shared title row.
type Band struct {
	Title  string
	Width  int
	Height int
	Gap    int
	Panes  []Pane
}

// NewPane creates a pane whose width is assigned by its containing band.
func NewPane(title, content string) Pane {
	return Pane{Title: title, Content: content}
}

// NewBand creates a band with equal-width panes by default.
func NewBand(title string, width, height int, panes ...Pane) Band {
	return Band{Title: title, Width: width, Height: height, Gap: gridGap, Panes: panes}
}

// RenderBand renders a full-width, exact-height band. The function is kept
// independent of Model so later pages can share the same arithmetic without
// changing the loader or the dashboard state machine.
func RenderBand(render theme.Context, band Band) string {
	width := max(1, band.Width)
	height := max(1, band.Height)
	if len(band.Panes) == 0 {
		return fitBlock(bandTitle(render, band.Title, width), width, height)
	}
	gap := bandGap(width, len(band.Panes), band.Gap)

	contentHeight := height
	rows := make([]string, 0, height)
	if strings.TrimSpace(band.Title) != "" {
		rows = append(rows, bandTitle(render, band.Title, width))
		contentHeight--
	}
	contentHeight = max(1, contentHeight)

	widths := bandPaneWidths(band.Panes, width, gap)
	panes := make([]string, 0, len(band.Panes))
	for index, pane := range band.Panes {
		pane.Width = widths[index]
		pane.Height = contentHeight
		panes = append(panes, renderPane(render, pane))
	}
	content := joinBandPanes(panes, gap, contentHeight)
	rows = append(rows, strings.Split(fitBlock(content, width, contentHeight), "\n")...)
	return fitBlock(strings.Join(rows, "\n"), width, height)
}

func bandTitle(render theme.Context, title string, width int) string {
	title = strings.ToUpper(strings.TrimSpace(title))
	if title == "" {
		return strings.Repeat("─", width)
	}
	label := render.Palette.Header().Render(title)
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitLine(label, width)
	}
	return fitLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func renderPane(render theme.Context, pane Pane) string {
	width, height := max(1, pane.Width), max(1, pane.Height)
	contentHeight := height
	rows := make([]string, 0, height)
	if strings.TrimSpace(pane.Title) != "" {
		rows = append(rows, bandTitle(render, pane.Title, width))
		contentHeight--
	}
	contentHeight = max(1, contentHeight)
	rows = append(rows, strings.Split(fitBlock(pane.Content, width, contentHeight), "\n")...)
	return fitBlock(strings.Join(rows, "\n"), width, height)
}

func bandPaneWidths(panes []Pane, width, gap int) []int {
	if len(panes) == 0 {
		return nil
	}
	available := max(len(panes), width-gap*(len(panes)-1))
	widths := make([]int, len(panes))
	fixedTotal, flexible := 0, 0
	for _, pane := range panes {
		if pane.Width > 0 {
			fixedTotal += max(1, pane.Width)
		} else {
			flexible++
		}
	}
	if fixedTotal+flexible <= available {
		for index, pane := range panes {
			if pane.Width > 0 {
				widths[index] = max(1, pane.Width)
			}
		}
		remaining := available - fixedTotal
		for index, pane := range panes {
			if pane.Width > 0 {
				continue
			}
			share := remaining / flexible
			if remaining%flexible > 0 {
				share++
			}
			widths[index] = max(1, share)
			remaining -= widths[index]
			flexible--
		}
		if flexible == 0 && remaining > 0 {
			widths[len(widths)-1] += remaining
		}
		return widths
	}

	// Fixed requests cannot all fit. Preserve their order, reserve one cell
	// for every later pane, and let the final pane absorb any rounding.
	remaining := available
	for index, pane := range panes {
		minForLater := len(panes) - index - 1
		if pane.Width <= 0 {
			widths[index] = 1
			remaining--
			continue
		}
		allocation := min(max(1, pane.Width), max(1, remaining-minForLater))
		widths[index] = allocation
		remaining -= allocation
	}
	if remaining > 0 {
		widths[len(widths)-1] += remaining
	}
	for index := range widths {
		widths[index] = max(1, widths[index])
	}
	return widths
}

func bandGap(width, paneCount, requested int) int {
	if paneCount <= 1 {
		return 0
	}
	return min(max(0, requested), max(0, (width-paneCount)/(paneCount-1)))
}

func joinBandPanes(panes []string, gap, height int) string {
	if len(panes) == 0 {
		return ""
	}
	if len(panes) == 1 {
		return panes[0]
	}
	separator := fitBlock("", gap, height)
	parts := make([]string, 0, len(panes)*2-1)
	for index, pane := range panes {
		if index > 0 {
			parts = append(parts, separator)
		}
		parts = append(parts, pane)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

// ShareBar renders a compact proportional bar. Values are expected to be
// non-negative and may be unnormalized; the largest total is used as the bar
// denominator so callers can pass raw provider costs or token counts.
func ShareBar(render theme.Context, label string, value, total float64, width int) string {
	width = max(1, width)
	if value < 0 {
		value = 0
	}
	if total <= 0 {
		total = value
	}
	fillWidth := 0
	if total > 0 {
		fillWidth = int(math.Round(float64(max(0, width-lipgloss.Width(label)-1)) * value / total))
	}
	fillWidth = min(max(0, fillWidth), max(0, width-lipgloss.Width(label)-1))
	barWidth := max(0, width-lipgloss.Width(label)-1)
	bar := strings.Repeat("█", fillWidth) + strings.Repeat("·", max(0, barWidth-fillWidth))
	return fitLine(strings.TrimSpace(label)+" "+render.Palette.Emphasis().Render(bar), width)
}

// Sparkline renders values using eight stable intensity levels. It is safe for
// empty input and always returns at most width terminal cells.
func Sparkline(values []float64, width int) string {
	if width <= 0 || len(values) == 0 {
		return ""
	}
	values = append([]float64(nil), values...)
	if len(values) > width {
		values = values[len(values)-width:]
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum, maximum = minFloat(minimum, value), maxFloat(maximum, value)
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	result := make([]rune, len(values))
	for index, value := range values {
		level := 0
		if maximum > minimum {
			level = int(math.Round((value - minimum) / (maximum - minimum) * float64(len(levels)-1)))
		}
		result[index] = levels[min(max(0, level), len(levels)-1)]
	}
	return string(result)
}

// IntensityCells maps values onto the shared five-cell intensity vocabulary.
func IntensityCells(values []float64, width int) string {
	if width <= 0 || len(values) == 0 {
		return ""
	}
	if len(values) > width {
		values = values[:width]
	}
	maximum := 0.0
	for _, value := range values {
		maximum = maxFloat(maximum, value)
	}
	levels := []rune("·░▒▓█")
	result := make([]rune, len(values))
	for index, value := range values {
		level := 0
		if maximum > 0 {
			level = int(math.Ceil(value / maximum * float64(len(levels)-1)))
		}
		result[index] = levels[min(max(0, level), len(levels)-1)]
	}
	return string(result)
}

// WarningRow creates the single-line warning treatment shared by dense bands.
func WarningRow(render theme.Context, message string, width int) string {
	if strings.TrimSpace(message) == "" {
		return fitLine("", width)
	}
	return fitLine(render.Palette.Warning().Render("! "+message), width)
}

// FullRangeChartWidth returns the width needed to show every point without
// silently clipping the requested range. At least one cell is reserved per
// point; when the viewport is narrower, callers can reduce the point count by
// an explicit tier policy rather than letting a chart library crop it.
func FullRangeChartWidth(pointCount, columnWidth int) int {
	if pointCount <= 0 {
		return 0
	}
	return pointCount*max(1, columnWidth) + max(0, pointCount-1)
}

// ChartColumnWidth computes a stable column width for a full-range chart.
func ChartColumnWidth(contentWidth, pointCount int) int {
	if contentWidth <= 0 || pointCount <= 0 {
		return 1
	}
	return max(1, min(4, (contentWidth-max(0, pointCount-1))/pointCount))
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

type cockpitLayout struct {
	width      int
	height     int
	innerWidth int
	tiers      LayoutTiers
	chrome     ChromeRows
	showRail   bool
	railWidth  int
	paneWidth  int
	bodyWidth  int
	bodyHeight int
	pageHeight int
}

func newCockpitLayout(width, height int) cockpitLayout {
	width = max(width, minimumWidth)
	height = max(height, minimumHeight)
	tiers := LayoutTiers{Width: WidthTierFor(width), Height: HeightTierFor(height)}
	chrome := globalChromeRows(tiers)
	innerWidth := max(1, width-framePadding*2)
	showRail := tiers.Width != WidthFloor
	railWidth := railWidthFor(tiers.Width, innerWidth)
	paneWidth := innerWidth
	if showRail {
		paneWidth = max(1, innerWidth-railWidth-gridGap)
	}
	bodyHeight := max(1, height-chrome.Total())
	return cockpitLayout{
		width: width, height: height, innerWidth: innerWidth,
		tiers: tiers, chrome: chrome, showRail: showRail,
		railWidth: railWidth, paneWidth: paneWidth, bodyWidth: innerWidth,
		bodyHeight: bodyHeight, pageHeight: max(1, bodyHeight),
	}
}

func railWidthFor(tier WidthTier, innerWidth int) int {
	if tier == WidthStandard || tier == WidthWide {
		return min(20, innerWidth)
	}
	return 0
}

// ContentWidth returns the width available to the active dashboard view.
func ContentWidth(width int) int {
	return newCockpitLayout(width, minimumHeight).paneWidth
}

// ContentHeight returns the legacy height for callers that only know H. New
// loader and page code should use ContentHeightFor so the width-tier chrome is
// included in the arithmetic.
func ContentHeight(height int) int {
	return ContentHeightFor(minimumWidth, height)
}

// ContentHeightFor returns the page height for the active W/H pair.
func ContentHeightFor(width, height int) int {
	return newCockpitLayout(width, height).pageHeight
}

func fitLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = lipgloss.NewStyle().Inline(true).MaxWidth(width).Render(value)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func fitBlock(value string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index, line := range lines {
		lines[index] = fitLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func fitRight(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = lipgloss.NewStyle().Inline(true).MaxWidth(width).Render(value)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func frameBlock(value string, layout cockpitLayout) string {
	block := fitBlock(value, layout.innerWidth, layout.height)
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		lines[index] = strings.Repeat(" ", framePadding) + line + strings.Repeat(" ", framePadding)
	}
	return strings.Join(lines, "\n")
}

func sizeBadge(render theme.Context, layout cockpitLayout) string {
	return fitRight(render.Palette.Subtle().Render(sizeBadgeLabel(layout)), layout.innerWidth)
}

func sizeBadgeLabel(layout cockpitLayout) string {
	label := fmt.Sprintf("%dx%d · %s", layout.width, layout.height, layout.tiers.Width.String())
	if layout.tiers.Height == HeightTall {
		label += " + tall"
	}
	return label
}
