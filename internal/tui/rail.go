package tui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// railBlock is deliberately data-free: the rail is an ambient summary of the
// snapshot already held by the TUI, not another loader-owned page.
type railBlock struct {
	title    string
	rows     []string
	optional bool
}

func (m Model) railView(layout cockpitLayout) string {
	if !layout.showRail || layout.railWidth <= 0 {
		return ""
	}
	innerWidth := max(1, layout.railWidth-1)
	blocks := m.railBlocks(innerWidth)
	rows := m.railNavigationRows()
	if len(rows)+1+1+len(blocks[0].rows) > layout.bodyHeight {
		rows = m.railCompactNavigationRows()
	}
	dropping := false
	for _, block := range blocks {
		if dropping {
			break
		}
		candidate := append([]string(nil), rows...)
		candidate = append(candidate, "")
		candidate = append(candidate, m.render.Palette.Header().Render(block.title))
		candidate = append(candidate, block.rows...)
		if len(candidate) > layout.bodyHeight && block.optional {
			dropping = true
			continue
		}
		rows = candidate
	}

	content := fitBlock(strings.Join(rows, "\n"), innerWidth, layout.bodyHeight)
	lines := strings.Split(content, "\n")
	divider := m.render.Palette.Border().Render("│")
	for index := range lines {
		lines[index] += divider
	}
	return strings.Join(lines, "\n")
}

func (m Model) railNavigationRows() []string {
	rows := make([]string, 0, len(m.router.Pages())+8)
	active := m.activePageID()
	for _, group := range m.router.groups() {
		if len(group.pages) == 0 {
			continue
		}
		if len(rows) > 0 {
			rows = append(rows, "")
		}
		rows = append(rows, m.render.Palette.Header().Render(string(group.section)))
		for _, page := range group.pages {
			pageIndex := m.router.IndexOf(page.ID())
			label := fmt.Sprintf("%d  %s", pageIndex+1, page.Title())
			style := m.render.Palette.Subtle()
			if page.ID() == active {
				label = "> " + label
				style = m.render.Palette.Emphasis().Bold(true)
			} else {
				// Two-space prefix keeps page numbers in one column whether or
				// not the active marker is present.
				label = "  " + label
			}
			rows = append(rows, style.Render(label))
		}
	}
	return rows
}

func (m Model) railCompactNavigationRows() []string {
	return []string{m.render.Palette.Subtle().Render(fmt.Sprintf("1-%d pages", len(m.router.Pages())))}
}

func (m Model) railBlocks(width int) []railBlock {
	return []railBlock{
		{
			title: "FILTERS",
			rows: []string{
				m.render.Palette.Subtle().Render("provider"),
				m.filterProviderView(),
				m.render.Palette.Subtle().Render("range"),
				m.filterRangeView(),
				m.render.Palette.Subtle().Render("project"),
				m.filterProjectView(),
			},
		},
		{
			title:    "SNAPSHOT",
			optional: true,
			rows:     m.railSnapshotRows(width),
		},
		{
			title:    "MIX · 30D",
			optional: true,
			rows:     m.railMixRows(width),
		},
		{
			title:    "PROJECTS 30D",
			optional: true,
			rows:     m.railProjectRows(width),
		},
	}
}

func (m Model) railSnapshotRows(width int) []string {
	data := m.snapshot.Rail.Snapshot
	rows := []string{
		m.railMetricRow("today", data.Today, width),
		m.railMetricRow("7d", data.SevenDays, width),
		m.railMetricRow("30d", data.ThirtyDays, width),
		m.railMetricRow("peak", data.Peak, width),
	}
	if data.PeakDate != "" {
		rows = append(rows, m.railMetricRow("", data.PeakDate, width))
	}
	return rows
}

func (m Model) railMetricRow(label, value string, width int) string {
	if value == "" {
		value = "—"
	}
	labelWidth := min(lipgloss.Width(label), max(1, width/2))
	label = truncate(label, labelWidth)
	value = compactRailValue(value, max(1, width-labelWidth-1))
	if label == "" {
		return fitLine("  "+m.render.Palette.Subtle().Render(value), width)
	}
	return fitLine(m.render.Palette.Subtle().Render(label)+" "+m.render.Palette.Emphasis().Render(value), width)
}

func compactRailValue(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	clean := strings.ReplaceAll(strings.TrimSpace(value), ",", "")
	prefix := ""
	if strings.HasPrefix(clean, "$") {
		prefix, clean = "$", strings.TrimPrefix(clean, "$")
	}
	number, err := strconv.ParseFloat(clean, 64)
	if err == nil {
		for _, unit := range []struct {
			threshold float64
			symbol    string
		}{
			{1_000_000_000_000, "T"},
			{1_000_000_000, "B"},
			{1_000_000, "M"},
			{1_000, "k"},
		} {
			if number < unit.threshold {
				continue
			}
			compact := fmt.Sprintf("%s%.1f%s", prefix, number/unit.threshold, unit.symbol)
			compact = strings.Replace(compact, ".0", "", 1)
			if lipgloss.Width(compact) <= width {
				return compact
			}
		}
	}
	if width <= 1 {
		return "~"
	}
	return truncate(value, width-1) + "~"
}

func (m Model) railMixRows(width int) []string {
	return []string{
		ShareBarAligned(m.render, railShareLabel("Codex", m.snapshot.Rail.Mix.Codex), m.snapshot.Rail.Mix.Codex, 1, width),
		ShareBarAligned(m.render, railShareLabel("Claude", m.snapshot.Rail.Mix.Claude), m.snapshot.Rail.Mix.Claude, 1, width),
	}
}

func railShareLabel(label string, share float64) string {
	// Fixed name and percent columns so every bar starts at the same cell.
	return fmt.Sprintf("%-6s %3d%%", label, int(math.Round(maxFloat(0, minFloat(1, share))*100)))
}

func (m Model) railProjectRows(width int) []string {
	projects := m.snapshot.Rail.Projects
	if len(projects) == 0 {
		return []string{m.render.Palette.Subtle().Render("no project data")}
	}
	rows := make([]string, 0, min(6, len(projects)))
	for index, project := range projects {
		if index >= 4 {
			break
		}
		label := project.Label
		if label == "" {
			label = "unknown"
		}
		nameWidth := max(4, width-11)
		name := truncate(label, nameWidth)
		// Pad by terminal-cell width, not rune count: wide glyphs in project
		// names must not shift the percent and bar columns.
		pad := strings.Repeat(" ", max(0, nameWidth-lipgloss.Width(name)))
		aligned := fmt.Sprintf("%s%s %3d%%", name, pad, int(math.Round(maxFloat(0, minFloat(1, project.Share))*100)))
		rows = append(rows, ShareBarAligned(m.render, aligned, project.Share, 1, width))
	}
	return rows
}
