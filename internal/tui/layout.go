package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	framePadding = 1
	railMaxWidth = 18
	railMinWidth = 14
	gridGap      = 2
	footerHeight = 4
)

type cockpitLayout struct {
	width      int
	height     int
	innerWidth int
	railWidth  int
	paneWidth  int
	bodyHeight int
}

func newCockpitLayout(width, height int) cockpitLayout {
	width = max(width, minimumWidth)
	height = max(height, minimumHeight)
	innerWidth := max(1, width-framePadding*2)
	railWidth := min(railMaxWidth, max(railMinWidth, innerWidth/4))
	paneWidth := max(1, innerWidth-railWidth-gridGap)
	return cockpitLayout{
		width: width, height: height, innerWidth: innerWidth,
		railWidth: railWidth, paneWidth: paneWidth,
		bodyHeight: max(1, height-1-1-footerHeight),
	}
}

// ContentWidth returns the width available to the active dashboard view.
func ContentWidth(width int) int {
	return newCockpitLayout(width, minimumHeight).paneWidth
}

// ContentHeight returns the height available to the active dashboard view.
func ContentHeight(height int) int {
	return newCockpitLayout(minimumWidth, height).bodyHeight
}

func fitLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = lipgloss.NewStyle().Inline(true).MaxWidth(width).Render(value)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func fitBlock(value string, width, height int) string {
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

func frameBlock(value string, layout cockpitLayout) string {
	block := fitBlock(value, layout.innerWidth, layout.height)
	lines := strings.Split(block, "\n")
	for index, line := range lines {
		lines[index] = strings.Repeat(" ", framePadding) + line + strings.Repeat(" ", framePadding)
	}
	return strings.Join(lines, "\n")
}
