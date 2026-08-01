package tui

import "github.com/janiorvalle/tokenomnom/internal/theme"

// PageID is the stable identity used by the sidebar and page router.
type PageID string

const (
	DailyPageID   PageID = "daily"
	MonthlyPageID PageID = "monthly"
	ModelsPageID  PageID = "models"
	HeatmapPageID PageID = "heatmap"
)

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
		snapshotPage{id: MonthlyPageID, section: SpendSection, title: "Monthly", viewIndex: int(MonthlyTab), keyHandler: updateMonthlyPage},
		snapshotPage{id: ModelsPageID, section: SpendSection, title: "Models", viewIndex: int(ModelsTab), keyHandler: updateModelsPage},
		snapshotPage{id: HeatmapPageID, section: SpendSection, title: "Heatmap", viewIndex: int(HeatmapTab), keyHandler: updateHeatmapPage},
	)
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
	previous := request.DailyOffset
	switch key {
	case "left":
		request.DailyOffset -= 7
	case "right":
		request.DailyOffset += 7
	case "home":
		request.DailyOffset = -1000000
	case "end":
		request.DailyOffset = 0
	default:
		return request, false
	}
	return request, previous != request.DailyOffset
}

func updateMonthlyPage(request Request, key string) (Request, bool) {
	previous := request.MonthlyOffset
	switch key {
	case "left":
		request.MonthlyOffset--
	case "right":
		request.MonthlyOffset++
	case "home":
		request.MonthlyOffset = -1000000
	case "end":
		request.MonthlyOffset = 0
	default:
		return request, false
	}
	return request, previous != request.MonthlyOffset
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
