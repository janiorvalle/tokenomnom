package tui

import (
	"strings"

	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

// PageID is the stable identity used by the sidebar and page router.
type PageID string

const (
	DailyPageID    PageID = "daily"
	LedgerPageID   PageID = "ledger"
	ModelsPageID   PageID = "models"
	HeatmapPageID  PageID = "heatmap"
	SessionsPageID PageID = "sessions"
)

// MonthlyPageID is retained for callers that still use the old page name.
const MonthlyPageID PageID = LedgerPageID

// PageSection is a navigation group in the sidebar.
type PageSection string

const (
	SpendSection   PageSection = "SPEND"
	HistorySection PageSection = "HISTORY"
	VaultSection   PageSection = "VAULT"
	SystemSection  PageSection = "SYSTEM"
)

// PageContext contains the immutable state a page needs to render itself.
// A page can use the same context whether it is backed by a loaded snapshot
// today or by its own loader in a later dashboard iteration.
type PageContext struct {
	Render   theme.Context
	Snapshot Snapshot
	Request  Request
	Width    int
	Height   int
}

// Page is one isolated destination in the dashboard.
type Page interface {
	ID() PageID
	Section() PageSection
	Title() string
	View(PageContext) string
	Update(Request, string) (Request, bool)
}

type pageKeyHandler func(Request, string) (Request, bool)

// snapshotPage adapts one of the existing report views to the page contract.
// New pages can implement Page in their own file without changing Model.
type snapshotPage struct {
	id         PageID
	section    PageSection
	title      string
	viewIndex  int
	keyHandler pageKeyHandler
}

// contextualPage can inspect the current snapshot before handling a key. The
// original Page.Update contract stays small for existing report pages, while
// interactive pages can open detail views without duplicating data elsewhere.
type contextualPage interface {
	UpdateContext(PageContext, string) (Request, bool)
}

func (p snapshotPage) ID() PageID           { return p.id }
func (p snapshotPage) Section() PageSection { return p.section }
func (p snapshotPage) Title() string        { return p.title }

func (p snapshotPage) View(context PageContext) string {
	if p.viewIndex < 0 || p.viewIndex >= len(context.Snapshot.Views) {
		return ""
	}
	return context.Snapshot.Views[p.viewIndex]
}

func (p snapshotPage) Update(request Request, key string) (Request, bool) {
	if p.keyHandler == nil {
		return request, false
	}
	return p.keyHandler(request, key)
}

type ledgerPage struct{}

func (ledgerPage) ID() PageID           { return LedgerPageID }
func (ledgerPage) Section() PageSection { return SpendSection }
func (ledgerPage) Title() string        { return "Ledger" }

func (ledgerPage) View(context PageContext) string {
	render := context.Render
	render.Width = context.Width
	return tuipages.Render(render, context.Snapshot.Ledger, context.Request.Ledger, context.Height)
}

func (ledgerPage) Update(request Request, _ string) (Request, bool) {
	return request, false
}

func (ledgerPage) UpdateContext(context PageContext, key string) (Request, bool) {
	if key == "left" || key == "right" {
		return context.Request, false
	}
	state, changed := tuipages.Update(context.Request.Ledger, context.Snapshot.Ledger, key)
	if !changed {
		return context.Request, false
	}
	context.Request.Ledger = state
	return context.Request, true
}

// PageRouter keeps page order and selection separate from the dashboard
// state machine. Registration order is also the order used by tab navigation.
type PageRouter struct {
	pages  []Page
	active int
}

type pageGroup struct {
	section PageSection
	pages   []Page
}

func newRouter() PageRouter {
	return newPageRouter(
		snapshotPage{id: DailyPageID, section: SpendSection, title: "Daily", viewIndex: int(DailyTab), keyHandler: updateDailyPage},
		ledgerPage{},
		snapshotPage{id: ModelsPageID, section: SpendSection, title: "Models", viewIndex: int(ModelsTab), keyHandler: updateModelsPage},
		snapshotPage{id: HeatmapPageID, section: SpendSection, title: "Heatmap", viewIndex: int(HeatmapTab), keyHandler: updateHeatmapPage},
		sessionsPage{},
	)
}

type sessionsPage struct{}

func (sessionsPage) ID() PageID           { return SessionsPageID }
func (sessionsPage) Section() PageSection { return HistorySection }
func (sessionsPage) Title() string        { return "Sessions" }

func (sessionsPage) View(context PageContext) string {
	return tuipages.RenderSessions(context.Render, context.Snapshot.Sessions, tuipages.SessionViewState{
		SelectedIndex: context.Request.SessionOffset,
		DetailID:      context.Request.SessionDetailID,
		DetailOffset:  context.Request.SessionDetailOffset,
		Provider:      context.Request.Provider.String(),
		Project:       context.Request.SessionProject,
		ProjectActive: context.Request.SessionProjectActive,
		DateRange:     context.Request.Range.String(),
		SelectLast:    context.Request.SessionReturnToEnd,
	}, context.Width, context.Height)
}

func (sessionsPage) Update(request Request, key string) (Request, bool) {
	return updateSessionsRequest(request, tuipages.SessionPageData{}, key)
}

func (sessionsPage) UpdateContext(context PageContext, key string) (Request, bool) {
	return updateSessionsRequestWithContext(context, key)
}

func updateSessionsRequest(request Request, data tuipages.SessionPageData, key string) (Request, bool) {
	return updateSessionsRequestWithContext(PageContext{Request: request, Snapshot: Snapshot{Sessions: data}}, key)
}

func updateSessionsRequestWithContext(context PageContext, key string) (Request, bool) {
	request := context.Request
	data := context.Snapshot.Sessions
	if request.SessionDetailID != "" {
		switch key {
		case "esc", "left":
			request.SessionDetailID = ""
			request.SessionDetailOffset = 0
			return request, true
		case "up", "down", "home", "end":
			if context.Width <= 0 || context.Height <= 0 {
				return request, false
			}
			session, ok := sessionByID(data.Sessions, request.SessionDetailID)
			if !ok {
				return request, false
			}
			maxOffset := tuipages.SessionDetailMaxOffset(context.Render, session, context.Width, context.Height, data.Location)
			nextOffset := request.SessionDetailOffset
			switch key {
			case "up":
				nextOffset = max(0, nextOffset-1)
			case "down":
				nextOffset = min(maxOffset, nextOffset+1)
			case "home":
				nextOffset = 0
			case "end":
				nextOffset = maxOffset
			}
			if nextOffset != request.SessionDetailOffset {
				request.SessionDetailOffset = nextOffset
				return request, true
			}
		}
		return request, false
	}
	normalizedReturn := false
	if request.SessionReturnToEnd {
		switch key {
		case "up", "down", "home", "end", "enter", "f":
			request.SessionOffset = max(0, len(data.Sessions)-1)
			request.SessionReturnToEnd = false
			normalizedReturn = true
		}
	}

	switch key {
	case "up":
		if request.SessionOffset > 0 {
			request.SessionOffset--
			return request, true
		}
		if request.SessionCursorStack != "" {
			last := strings.LastIndexByte(request.SessionCursorStack, '\x00')
			if last < 0 {
				return request, false
			}
			request.SessionCursor = request.SessionCursorStack[last+1:]
			request.SessionCursorStack = request.SessionCursorStack[:last]
			request.SessionOffset = 0
			request.SessionReturnToEnd = true
			return request, true
		}
	case "down":
		if request.SessionOffset+1 < len(data.Sessions) {
			request.SessionOffset++
			return request, true
		}
		if data.HasMore && data.NextCursor != "" {
			request.SessionCursorStack += "\x00" + request.SessionCursor
			request.SessionCursor = data.NextCursor
			request.SessionOffset = 0
			request.SessionReturnToEnd = false
			return request, true
		}
	case "home":
		if request.SessionOffset != 0 {
			request.SessionOffset = 0
			return request, true
		}
	case "end":
		last := max(0, len(data.Sessions)-1)
		if request.SessionOffset != last {
			request.SessionOffset = last
			return request, true
		}
	case "enter":
		if len(data.Sessions) == 0 {
			return request, false
		}
		index := min(max(request.SessionOffset, 0), len(data.Sessions)-1)
		request.SessionOffset = index
		request.SessionDetailID = data.Sessions[index].SessionID
		request.SessionDetailOffset = 0
		return request, true
	case "f":
		next, active := nextProject(request.SessionProject, request.SessionProjectActive, data.Projects)
		if next == request.SessionProject && active == request.SessionProjectActive {
			return request, false
		}
		request.SessionProject = next
		request.SessionProjectActive = active
		request.SessionCursor = ""
		request.SessionCursorStack = ""
		request.SessionOffset = 0
		request.SessionReturnToEnd = false
		return request, true
	case "esc":
		return request, false
	}
	return request, normalizedReturn
}

func sessionByID(sessions []historystore.CatalogSession, id string) (historystore.CatalogSession, bool) {
	for _, session := range sessions {
		if session.SessionID == id {
			return session, true
		}
	}
	return historystore.CatalogSession{}, false
}

func nextProject(current string, active bool, projects []tuipages.ProjectOption) (string, bool) {
	if len(projects) == 0 {
		return "", false
	}
	if !active {
		return projects[0].Key, true
	}
	for index, project := range projects {
		if project.Key != current {
			continue
		}
		if index+1 < len(projects) {
			return projects[index+1].Key, true
		}
		return "", false
	}
	return projects[0].Key, true
}

func newPageRouter(pages ...Page) PageRouter {
	registered := make([]Page, 0, len(pages))
	for _, page := range pages {
		if page != nil {
			registered = append(registered, page)
		}
	}
	return PageRouter{pages: registered}
}

// Pages returns the registered pages in navigation order.
func (r PageRouter) Pages() []Page {
	return append([]Page(nil), r.pages...)
}

// ActivePage returns the current page, or nil when the router has no pages.
func (r PageRouter) ActivePage() Page {
	if r.active < 0 || r.active >= len(r.pages) {
		return nil
	}
	return r.pages[r.active]
}

// ActiveIndex returns the current page index, or -1 when there are no pages.
func (r PageRouter) ActiveIndex() int {
	if r.active < 0 || r.active >= len(r.pages) {
		return -1
	}
	return r.active
}

// PageAt returns the page at a navigation index, or nil when it is out of range.
func (r PageRouter) PageAt(index int) Page {
	if index < 0 || index >= len(r.pages) {
		return nil
	}
	return r.pages[index]
}

// IndexOf returns a page's navigation index, or -1 when it is not registered.
func (r PageRouter) IndexOf(id PageID) int {
	for index, page := range r.pages {
		if page.ID() == id {
			return index
		}
	}
	return -1
}

// Select activates a registered page and reports whether selection changed.
func (r *PageRouter) Select(id PageID) bool {
	return r.SelectIndex(r.IndexOf(id))
}

// SelectIndex activates a page by navigation index.
func (r *PageRouter) SelectIndex(index int) bool {
	if index < 0 || index >= len(r.pages) || index == r.active {
		return false
	}
	r.active = index
	return true
}

// Move advances through all registered pages, wrapping at either end.
func (r *PageRouter) Move(direction int) bool {
	if len(r.pages) == 0 || direction == 0 {
		return false
	}
	index := (r.active + direction) % len(r.pages)
	if index < 0 {
		index += len(r.pages)
	}
	return r.SelectIndex(index)
}

func (r PageRouter) groups() []pageGroup {
	groups := make([]pageGroup, 0, 4)
	for _, page := range r.pages {
		groupIndex := -1
		for index := range groups {
			if groups[index].section == page.Section() {
				groupIndex = index
				break
			}
		}
		if groupIndex < 0 {
			groups = append(groups, pageGroup{section: page.Section(), pages: []Page{page}})
			continue
		}
		groups[groupIndex].pages = append(groups[groupIndex].pages, page)
	}
	return groups
}

func updateDailyPage(request Request, key string) (Request, bool) {
	previousCursor, previousDetailOffset := request.DailyCursor, request.DailyDetailOffset
	switch key {
	case "left":
		request.DailyCursor++
		request.DailyDetailOffset = 0
	case "right":
		request.DailyCursor = max(0, request.DailyCursor-1)
		request.DailyDetailOffset = 0
	case "up":
		if request.DailyDetailOffset == 0 {
			return request, false
		}
		request.DailyDetailOffset--
	case "down":
		request.DailyDetailOffset++
	case "home":
		request.DailyCursor = 1_000_000
		request.DailyDetailOffset = 0
	case "end":
		request.DailyCursor = 0
		request.DailyDetailOffset = 0
	default:
		return request, false
	}
	return request, previousCursor != request.DailyCursor || previousDetailOffset != request.DailyDetailOffset
}

func updateModelsPage(request Request, key string) (Request, bool) {
	previousSort, previousOffset := request.ModelSort, request.ModelOffset
	switch key {
	case "s":
		request.ModelSort = (request.ModelSort + 1) % 3
		request.ModelOffset = 0
	case "up":
		if request.ModelOffset > 0 {
			request.ModelOffset--
		}
	case "down":
		request.ModelOffset++
	case "home", "end":
		request.ModelOffset = 0
	default:
		return request, false
	}
	return request, previousSort != request.ModelSort || previousOffset != request.ModelOffset
}

func updateHeatmapPage(request Request, key string) (Request, bool) {
	previousOffset, previousYear := request.HeatmapOffset, request.HeatmapYear
	switch key {
	case "y":
		request.HeatmapYear = !request.HeatmapYear
	case "left":
		request.HeatmapYear = false
		request.HeatmapOffset--
	case "right":
		request.HeatmapYear = false
		request.HeatmapOffset++
	default:
		return request, false
	}
	return request, previousOffset != request.HeatmapOffset || previousYear != request.HeatmapYear
}
