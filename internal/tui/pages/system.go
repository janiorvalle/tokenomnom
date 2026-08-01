package pages

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// RenderSystem renders doctor findings and effective pricing into a viewport.
func RenderSystem(render theme.Context, data SystemPageData, width, height, offset int) string {
	lines := systemLines(render, data, max(1, width))
	viewportHeight := max(1, height)
	start := min(max(0, offset), systemPageMaxOffset(len(lines), viewportHeight))
	end := min(len(lines), start+viewportHeight)
	return strings.Join(lines[start:end], "\n")
}

// UpdateSystemOffset applies keyboard scrolling to the System page.
func UpdateSystemOffset(render theme.Context, data SystemPageData, width, height, offset int, key string) (int, bool) {
	lines := systemLines(render, data, max(1, width))
	maxOffset := systemPageMaxOffset(len(lines), max(1, height))
	previous := offset
	switch key {
	case "up":
		offset = max(0, min(maxOffset, offset)-1)
	case "down":
		offset = min(maxOffset, offset+1)
	default:
		return offset, false
	}
	return offset, previous != offset
}

func systemLines(render theme.Context, data SystemPageData, width int) []string {
	logicalLines := []string{render.Palette.Header().Render("Doctor"), ""}
	if len(data.Findings) == 0 {
		logicalLines = append(logicalLines, render.Palette.Subtle().Render("Doctor findings are not loaded yet."))
	} else {
		for _, finding := range data.Findings {
			logicalLines = append(logicalLines, systemFact(render, finding))
		}
	}
	if len(data.Warnings) > 0 {
		logicalLines = append(logicalLines, "", render.Palette.Header().Render("Warnings"))
		for _, warning := range data.Warnings {
			logicalLines = append(logicalLines, "  "+render.Palette.Warning().Render(warning))
		}
	}

	logicalLines = append(logicalLines, "", render.Palette.Header().Render("Effective pricing"))
	if data.PricingDisclaimer != "" {
		logicalLines = append(logicalLines, render.Palette.Subtle().Render(data.PricingDisclaimer))
	}
	if len(data.Pricing) == 0 {
		logicalLines = append(logicalLines, render.Palette.Subtle().Render("No pricing entries are available."))
	} else {
		for _, row := range data.Pricing {
			logicalLines = append(logicalLines, pricingFact(render, row))
		}
	}
	return wrapSystemLines(logicalLines, width)
}

func wrapSystemLines(logicalLines []string, width int) []string {
	lines := make([]string, 0, len(logicalLines))
	for _, line := range logicalLines {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		wrapped := lipgloss.NewStyle().Width(width).Render(line)
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}
	return lines
}

func systemPageMaxOffset(lineCount, viewportHeight int) int {
	return max(0, lineCount-viewportHeight)
}

func systemFact(render theme.Context, finding SystemFinding) string {
	style := render.Palette.Subtle()
	switch finding.State {
	case FindingOK:
		style = render.Palette.Success()
	case FindingWarning:
		style = render.Palette.Warning()
	}
	return "  " + render.Palette.Subtle().Render(fmt.Sprintf("%-12s", finding.Name+":")) + " " + style.Render(finding.Value)
}

func pricingFact(render theme.Context, row PricingRow) string {
	model := render.Palette.Emphasis().Render(row.Model)
	values := fmt.Sprintf("input %s · cache %s · write %s/%s · output %s", row.BaseInput, row.CacheRead, row.Write5m, row.Write1h, row.Output)
	status := row.Status
	if row.Override != "" {
		status += " · " + row.Override
	}
	details := strings.Trim(strings.Join([]string{status, row.Effective, row.Source}, " · "), " ·")
	return "  " + model + "  " + render.Palette.Subtle().Render(values) + "  " + render.Palette.Emphasis().Render(details)
}
