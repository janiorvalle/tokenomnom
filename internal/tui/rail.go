package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
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
			},
		},
		{
			title:    "SNAPSHOT",
			optional: true,
			rows:     m.railSnapshotRows(width),
		},
		{
			title:    "MIX",
			optional: true,
			rows:     m.railMixRows(),
		},
		{
			title:    "PROJECTS",
			optional: true,
			rows:     m.railProjectRows(),
		},
	}
}

func (m Model) railSnapshotRows(width int) []string {
	labels := [...]string{"total", "tokens", "active days"}
	rows := make([]string, 0, len(labels))
	for index, label := range labels {
		metric := m.snapshot.Summary.Metrics[index]
		value := metric.Value
		if value == "" {
			value = "-"
		}
		labelWidth := min(lipgloss.Width(label), max(1, width/2))
		label = truncate(label, labelWidth)
		value = compactRailValue(value, max(1, width-labelWidth-1))
		rows = append(rows, m.render.Palette.Subtle().Render(label)+" "+m.summaryValueStyle(metric).Render(value))
	}
	return rows
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

func (m Model) railMixRows() []string {
	provider := strings.ToUpper(m.request.Provider.String())
	return []string{
		m.render.Palette.Subtle().Render("provider") + " " + m.render.Palette.Emphasis().Render(provider),
		m.render.Palette.Subtle().Render("range") + " " + m.render.Palette.Emphasis().Render(strings.ToUpper(m.request.Range.String())),
		m.render.Palette.Subtle().Render("status") + " " + m.render.Palette.Subtle().Render(syncStatusText(m.syncing, m.syncFresh)),
	}
}

func (m Model) railProjectRows() []string {
	projects := m.snapshot.Sessions.Projects
	if len(projects) == 0 {
		return []string{m.render.Palette.Subtle().Render("all projects")}
	}
	rows := make([]string, 0, min(4, len(projects))+1)
	for index, project := range projects {
		if index >= 4 {
			break
		}
		label := project.Label
		if label == "" {
			label = tuipages.ProjectLabel(project.Key, projects)
		}
		style := m.render.Palette.Subtle()
		if m.request.SessionProjectActive && project.Key == m.request.SessionProject {
			style = m.render.Palette.Emphasis().Bold(true)
		}
		rows = append(rows, style.Render("  "+label))
	}
	if len(projects) > 4 {
		rows = append(rows, m.render.Palette.Subtle().Render(fmt.Sprintf("  +%d more", len(projects)-4)))
	}
	return rows
}
