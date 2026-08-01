package tui

import (
	"fmt"
	"strings"

	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

func (m Model) railView(layout cockpitLayout) string {
	rows := make([]string, 0, len(m.router.Pages())+10)
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
				label = "› " + label
				style = m.render.Palette.Emphasis().Bold(true)
			}
			rows = append(rows, style.Render(label))
		}
	}

	rows = append(rows, "", m.render.Palette.Header().Render("FILTERS"))
	rows = append(rows,
		m.render.Palette.Subtle().Render("provider"),
		m.filterProviderView(),
		m.render.Palette.Subtle().Render("range"),
		m.filterRangeView(),
	)
	if m.activePageID() == SessionsPageID {
		rows = append(rows,
			m.render.Palette.Subtle().Render("project"),
			m.filterProjectView(),
		)
	}

	innerWidth := max(1, layout.railWidth-1)
	content := fitBlock(strings.Join(rows, "\n"), innerWidth, layout.bodyHeight)
	lines := strings.Split(content, "\n")
	divider := m.render.Palette.Border().Render("│")
	for index := range lines {
		lines[index] += divider
	}
	return strings.Join(lines, "\n")
}

func (m Model) activePageID() PageID {
	page := m.activePage()
	if page == nil {
		return ""
	}
	return page.ID()
}

func (m Model) filterProviderView() string {
	value := m.request.Provider.String()
	if m.request.Provider == AllProviders {
		return m.render.Palette.Subtle().Render("  " + value)
	}
	return m.render.Palette.Provider(value, 0).Bold(true).Render("  " + value)
}

func (m Model) filterRangeView() string {
	value := m.request.Range.String()
	style := m.render.Palette.Subtle()
	if m.request.Range != Range30Days {
		style = m.render.Palette.Emphasis().Bold(true)
	}
	return style.Render("  " + value)
}

func (m Model) filterProjectView() string {
	if !m.request.SessionProjectActive {
		return m.render.Palette.Subtle().Render("  all")
	}
	value := tuipages.ProjectLabel(m.request.SessionProject, m.snapshot.Sessions.Projects)
	return m.render.Palette.Emphasis().Bold(true).Render("  " + value)
}
