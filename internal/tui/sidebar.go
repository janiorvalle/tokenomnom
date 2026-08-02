package tui

import tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"

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
