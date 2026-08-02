package pages

import (
	"fmt"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// Zoom is the period granularity shown by the ledger.
type Zoom uint8

const (
	ZoomYear Zoom = iota
	ZoomMonth
	ZoomDay
)

// State is the keyboard navigation state for the ledger page.
type State struct {
	Zoom               Zoom
	Year               int
	Month              string
	Cursor             int
	ExpandedDay        string
	SessionCursor      int
	SessionPageCursor  string
	SessionCursorStack string
	SessionSelectLast  bool
	DetailID           string
	DetailOffset       int
}

// ProviderTotals contains one provider's usage and priced cost for a period.
type ProviderTotals struct {
	Cost           pricing.Money
	Tokens         int64
	PricedTokens   int64
	UnpricedTokens int64
}

// Add returns the combined provider values.
func (value ProviderTotals) Add(other ProviderTotals) ProviderTotals {
	return ProviderTotals{
		Cost:           value.Cost + other.Cost,
		Tokens:         value.Tokens + other.Tokens,
		PricedTokens:   value.PricedTokens + other.PricedTokens,
		UnpricedTokens: value.UnpricedTokens + other.UnpricedTokens,
	}
}

// Row is one year, month, or day in the ledger.
type Row struct {
	Key      string
	Label    string
	Sessions int
	Codex    ProviderTotals
	Claude   ProviderTotals
}

// LedgerMonth is one month in the fixed twelve-cell spend chart.
type LedgerMonth struct {
	Key         string
	Label       string
	Sessions    int
	Codex       ProviderTotals
	Claude      ProviderTotals
	ActiveDays  int
	AverageCost pricing.Money
	PeakCost    pricing.Money
	PeakDay     string
	PeakPartial bool
}

func (month LedgerMonth) Total() ProviderTotals { return month.Codex.Add(month.Claude) }

// LedgerModel is the all-time model rollup shown in the period detail pane.
type LedgerModel struct {
	Provider       string
	Model          string
	Tokens         int64
	Cost           pricing.Money
	PricedTokens   int64
	UnpricedTokens int64
	CostPerMillion pricing.Rate
	HasRate        bool
	Status         string
	Source         string
}

// LedgerProfile is one bounded weekday or hour bucket.
type LedgerProfile struct {
	Label    string
	Value    int
	Cost     pricing.Money
	Sessions int
}

// LedgerProject is a bounded project population from the history catalog.
type LedgerProject struct {
	Label    string
	Sessions int
	Share    float64
}

// LedgerProjectMonth is one project/month intensity cell. The catalog stores
// sessions, so the matrix deliberately reports session intensity rather than
// inventing a cost attribution that the existing tables do not contain.
type LedgerProjectMonth struct {
	Project  string
	Month    string
	Sessions int
}

// LedgerProviderMonth is the priced usage split used by the period detail pane.
type LedgerProviderMonth struct {
	Provider string
	Month    string
	Cost     pricing.Money
	Tokens   int64
}

// LedgerProvenance summarizes how the model costs in the pane were priced.
type LedgerProvenance struct {
	PublishedModels int
	ProxyModels     int
	EstimatedModels int
	UnpricedModels  int
	PublishedCost   pricing.Money
	PublishedTokens int64
	ProxyCost       pricing.Money
	ProxyTokens     int64
	EstimatedCost   pricing.Money
	EstimatedTokens int64
	Unpriced        []string
}

// LedgerAnalytics contains the bounded side-pane and profile facts loaded
// alongside the ordinary period rows.
type LedgerAnalytics struct {
	Months         []LedgerMonth
	Models         []LedgerModel
	Weekdays       []LedgerProfile
	Hours          []LedgerProfile
	Projects       []LedgerProject
	ProjectMonths  []LedgerProjectMonth
	ProviderMonths []LedgerProviderMonth
	Provenance     LedgerProvenance
	AverageCost    pricing.Money
	ActiveDays     int
	PeakCost       pricing.Money
	PeakDay        string
	Warning        string
}

// LedgerSession is one indexed session attributed to the expanded day.
type LedgerSession struct {
	historystore.CatalogSession
	Tokens            int64
	Cost              pricing.Money
	PricedTokens      int64
	UnpricedTokens    int64
	AttributionStatus string
	ActivityTimestamp string
	Warning           string
}

// Total returns the combined provider values for a row.
func (row Row) Total() ProviderTotals {
	return row.Codex.Add(row.Claude)
}

// Add returns the combined period rows.
func (row Row) Add(other Row) Row {
	return Row{
		Key: row.Key, Label: row.Label, Sessions: row.Sessions + other.Sessions,
		Codex: row.Codex.Add(other.Codex), Claude: row.Claude.Add(other.Claude),
	}
}

// Data is the immutable ledger result for the current zoom level.
type Data struct {
	Available              bool
	Zoom                   Zoom
	Year                   int
	Month                  string
	Rows                   []Row
	Total                  Row
	SessionDay             string
	SessionPageCursor      string
	Sessions               []LedgerSession
	SessionsHaveMore       bool
	SessionsNextCursor     string
	SessionIndexAvailable  bool
	SessionDataUnavailable bool
	SessionWarning         string
	Location               *time.Location
	// DayModels and DayProjects are bounded display rollups; count fields preserve full totals.
	DayModels           []LedgerModel
	DayModelTotalCost   pricing.Money
	DayModelTotalTokens int64
	DayModelCount       int
	DayProjects         []LedgerProject
	DayProjectCount     int
	DaySessionCount     int
	DayHours            []LedgerProfile
	Analytics           LedgerAnalytics
}

// Matches reports whether data was loaded for the requested zoom and anchor.
func (data Data) Matches(state State) bool {
	if !data.Available || data.Zoom != state.Zoom {
		return false
	}
	switch state.Zoom {
	case ZoomMonth:
		return data.Year == state.Year
	case ZoomDay:
		return data.Month == state.Month
	default:
		return true
	}
}

// SelectedIndex returns the row selected by state, preferring the current
// anchor when a zoom transition has just reset the cursor.
func SelectedIndex(data Data, state State) int {
	if len(data.Rows) == 0 {
		return -1
	}
	if state.Cursor >= 0 {
		return min(state.Cursor, len(data.Rows)-1)
	}
	anchor := stateAnchor(state)
	if anchor != "" {
		for index, row := range data.Rows {
			if row.Key == anchor {
				return index
			}
		}
	}
	return 0
}

// Update applies ledger navigation. Rows are newest first, so j moves toward
// older periods while k moves back toward the latest period.
func Update(state State, data Data, key string) (State, bool) {
	if state.DetailID != "" {
		if key == "esc" || key == "h" || key == "left" {
			state.DetailID = ""
			state.DetailOffset = 0
			return state, true
		}
		return state, false
	}
	if state.ExpandedDay != "" {
		if key == "esc" || key == "h" || key == "left" {
			state.ExpandedDay = ""
			state.SessionCursor = 0
			state.SessionPageCursor = ""
			state.SessionCursorStack = ""
			state.SessionSelectLast = false
			return state, true
		}
		if data.SessionDay != state.ExpandedDay || data.SessionPageCursor != state.SessionPageCursor || len(data.Sessions) == 0 {
			return state, false
		}
		next := state
		sessionIndex := min(max(0, state.SessionCursor), len(data.Sessions)-1)
		if state.SessionSelectLast {
			sessionIndex = len(data.Sessions) - 1
		}
		switch key {
		case "j", "down":
			if sessionIndex >= len(data.Sessions)-1 {
				if !data.SessionsHaveMore || data.SessionsNextCursor == "" {
					return state, false
				}
				next.SessionCursorStack += "\x00" + state.SessionPageCursor
				next.SessionPageCursor = data.SessionsNextCursor
				next.SessionCursor = 0
				next.SessionSelectLast = false
				return next, true
			}
			next.SessionCursor = sessionIndex + 1
		case "k", "up":
			if sessionIndex <= 0 {
				if state.SessionCursorStack == "" {
					return state, false
				}
				separator := strings.LastIndexByte(state.SessionCursorStack, '\x00')
				if separator < 0 {
					return state, false
				}
				next.SessionPageCursor = state.SessionCursorStack[separator+1:]
				next.SessionCursorStack = state.SessionCursorStack[:separator]
				next.SessionCursor = 0
				next.SessionSelectLast = true
				return next, true
			}
			next.SessionCursor = sessionIndex - 1
		case "home":
			next.SessionCursor = 0
		case "end":
			next.SessionCursor = len(data.Sessions) - 1
		case "enter", "l":
			next.SessionCursor = sessionIndex
			next.DetailID = data.Sessions[sessionIndex].SessionID
			next.DetailOffset = 0
		default:
			return state, false
		}
		next.SessionSelectLast = false
		return next, next != state
	}
	if len(data.Rows) == 0 {
		if key == "h" {
			switch state.Zoom {
			case ZoomMonth:
				state.Zoom = ZoomYear
				state.Cursor = -1
				return state, true
			case ZoomDay:
				state.Zoom = ZoomMonth
				state.Cursor = -1
				return state, true
			}
		}
		return state, false
	}
	if key != "h" && !data.Matches(state) {
		return state, false
	}
	selected := SelectedIndex(data, state)
	next := state
	switch key {
	case "j", "down":
		if selected >= len(data.Rows)-1 {
			return state, false
		}
		next.Cursor = selected + 1
	case "k", "up":
		if selected <= 0 {
			return state, false
		}
		next.Cursor = selected - 1
	case "home":
		next.Cursor = 0
	case "end":
		next.Cursor = len(data.Rows) - 1
	case "l":
		switch state.Zoom {
		case ZoomYear:
			year, err := strconv.Atoi(data.Rows[selected].Key)
			if err != nil {
				return state, false
			}
			next.Zoom = ZoomMonth
			next.Year = year
			next.Month = ""
			next.Cursor = -1
		case ZoomMonth:
			next.Zoom = ZoomDay
			next.Month = data.Rows[selected].Key
			next.Cursor = -1
		case ZoomDay:
			next.ExpandedDay = data.Rows[selected].Key
			next.SessionCursor = 0
			next.SessionPageCursor = ""
			next.SessionCursorStack = ""
			next.SessionSelectLast = false
		default:
			return state, false
		}
	case "enter":
		if state.Zoom != ZoomDay {
			return state, false
		}
		next.ExpandedDay = data.Rows[selected].Key
		next.SessionCursor = 0
		next.SessionPageCursor = ""
		next.SessionCursorStack = ""
		next.SessionSelectLast = false
	case "h":
		switch state.Zoom {
		case ZoomMonth:
			next.Zoom = ZoomYear
			next.Cursor = -1
		case ZoomDay:
			next.Zoom = ZoomMonth
			next.Cursor = -1
		default:
			return state, false
		}
	default:
		return state, false
	}
	return next, next != state
}

// Render selects the contract layout from the page viewport. The floor path is
// intentionally kept as the original v0.5 ledger so small terminals do not
// lose the established zoom and pricing behavior.
func Render(render theme.Context, data Data, state State, height int) string {
	width := max(1, render.Width)
	if height <= 0 {
		height = 24
	}
	if state.DetailID != "" && data.SessionDay == state.ExpandedDay {
		for _, session := range data.Sessions {
			if session.SessionID == state.DetailID {
				return RenderLedgerSessionDetail(render, session.CatalogSession, width, height, data.Location, state.DetailOffset)
			}
		}
	}
	if state.ExpandedDay != "" {
		if width >= ledgerPreviewWidth {
			return renderExpandedDayMasterDetail(render, data, state, width, height)
		}
		return renderExpandedDayList(render, data, state, width, height)
	}
	if width >= ledgerWideWidth && height >= ledgerTallHeight {
		return renderWidePeriods(render, data, state, width, height)
	}
	if width >= ledgerStandardWidth {
		return renderStandardPeriods(render, data, state, width, height)
	}
	return renderLegacyPeriods(render, data, state, width, height)
}

const (
	ledgerStandardWidth = 96
	ledgerWideWidth     = 150
	ledgerPreviewWidth  = 110
	ledgerTallHeight    = 45
)

func renderLegacyPeriods(render theme.Context, data Data, state State, width, height int) string {
	selected := SelectedIndex(data, state)
	lines := []string{
		fitLine(render.Palette.Subtle().Render(breadcrumb(state)), width),
	}
	if len(data.Rows) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No usage found for this period."))
		return strings.Join(lines, "\n")
	}
	maxTokens := maxRowTokens(data.Rows)

	compact := width < 72
	if compact {
		lines = append(lines, compactHeader(render, width))
		capacity := max(1, (height-len(lines)-2)/2)
		start, end := visibleWindow(len(data.Rows), selected, capacity)
		for index := start; index < end; index++ {
			delta, pricedTokens, valid, partial := rowDelta(data.Rows, index)
			if !valid {
				partial = false
			}
			lines = append(lines, compactRow(render, data.Rows[index], index == selected, delta, pricedTokens, partial, width, maxTokens)...)
		}
		lines = append(lines, compactTotal(render, data.Total, width, maxTokens)...)
	} else {
		periodWidth, moneyWidth, deltaWidth, barWidth := wideColumns(width)
		lines = append(lines, wideHeader(render, periodWidth, moneyWidth, deltaWidth, barWidth))
		capacity := max(1, height-len(lines)-1)
		start, end := visibleWindow(len(data.Rows), selected, capacity)
		for index := start; index < end; index++ {
			delta, pricedTokens, valid, partial := rowDelta(data.Rows, index)
			if !valid {
				partial = false
			}
			lines = append(lines, wideRow(render, data.Rows[index], index == selected, delta, pricedTokens, partial, periodWidth, moneyWidth, deltaWidth, barWidth, maxTokens))
		}
		lines = append(lines, wideTotal(render, data.Total, periodWidth, moneyWidth, deltaWidth, barWidth, maxTokens))
	}
	return strings.Join(lines, "\n")
}

func renderExpandedDayList(render theme.Context, data Data, state State, width, height int) string {
	lines := []string{fitLine(render.Palette.Subtle().Render(breadcrumb(state)+"  /  "+state.ExpandedDay), width)}
	if data.SessionDay != state.ExpandedDay || data.SessionPageCursor != state.SessionPageCursor {
		lines = append(lines, render.Palette.Subtle().Render(truncate("Loading indexed sessions…", width)))
		return strings.Join(lines, "\n")
	}
	if data.SessionWarning != "" {
		lines = append(lines, render.Palette.Warning().Render(truncate(data.SessionWarning, width)))
	}
	if data.SessionDataUnavailable {
		return strings.Join(lines, "\n")
	}
	if !data.SessionIndexAvailable {
		lines = append(lines,
			render.Palette.Warning().Render(truncate("No history index is available.", width)),
			render.Palette.Subtle().Render(truncate("Run tokenomnom history index to inspect this day’s sessions.", width)),
		)
		return strings.Join(lines, "\n")
	}
	day, ok := rowByKey(data.Rows, state.ExpandedDay)
	if ok {
		dayTotal := day.Total()
		lines = append(lines, render.Palette.Emphasis().Bold(true).Render(truncate(fmt.Sprintf("%s  %s  %s tokens", day.Label, formatMoney(dayTotal.Cost, dayTotal.PricedTokens, false, dayTotal.UnpricedTokens > 0), commaInteger(dayTotal.Tokens)), width)))
	}
	if len(data.Sessions) == 0 {
		lines = append(lines,
			render.Palette.Warning().Render(truncate("No indexed sessions match this day.", width)),
			render.Palette.Subtle().Render(truncate("Run tokenomnom history index to refresh the history index.", width)),
		)
		return strings.Join(lines, "\n")
	}
	lines = append(lines, expandedSessionHeader(render, width))
	selected := min(max(0, state.SessionCursor), len(data.Sessions)-1)
	if state.SessionSelectLast {
		selected = len(data.Sessions) - 1
	}
	selectedWarning := data.Sessions[selected].Warning
	rowHeight := 1
	if width < 72 {
		rowHeight = 2
	}
	controlLines := 1
	if data.SessionsHaveMore {
		controlLines++
	}
	if selectedWarning != "" {
		controlLines++
	}
	rollupHeight := 0
	if width >= 72 && height >= ledgerTallHeight {
		rollupHeight = min(35, max(0, height-len(lines)-controlLines))
	}
	capacity := max(1, (height-len(lines)-controlLines-rollupHeight)/rowHeight)
	start, end := visibleWindow(len(data.Sessions), selected, capacity)
	for index := start; index < end; index++ {
		rows := expandedSessionRow(render, data.Sessions[index], index == selected, width, data.Location)
		lines = append(lines, rows...)
	}
	if width >= 72 && height >= ledgerTallHeight {
		rollupHeight = max(0, height-len(lines)-controlLines)
	}
	if data.SessionsHaveMore {
		lines = append(lines, render.Palette.Subtle().Render(truncate("↓ more sessions", width)))
	}
	if rollupHeight > 0 {
		lines = append(lines, strings.Split(renderLedgerDayRollups(render, data, width, rollupHeight), "\n")...)
	}
	if selectedWarning != "" {
		lines = append(lines, render.Palette.Warning().Render(truncate("~ "+selectedWarning, width)))
	}
	lines = append(lines, render.Palette.Subtle().Render(truncate("↑/↓ select  ·  enter open  ·  esc collapse", width)))
	return strings.Join(lines, "\n")
}

func renderLedgerDayRollups(render theme.Context, data Data, width, height int) string {
	models := append([]LedgerModel(nil), data.DayModels...)
	projects := ledgerDayProjects(data)
	leftWidth := max(1, (width-2)/2)
	rightWidth := max(1, width-leftWidth-2)

	modelLines := []string{
		fitLine(render.Palette.Header().Render("MODELS ON THIS DAY"), leftWidth),
		fitLine(render.Palette.Subtle().Render(ledgerDayModelHeader(leftWidth)), leftWidth),
	}
	totalModelCost, totalModelTokens := data.DayModelTotalCost, data.DayModelTotalTokens
	if totalModelCost == 0 && totalModelTokens == 0 {
		for _, model := range models {
			totalModelCost += model.Cost
			totalModelTokens += model.Tokens
		}
	}
	modelCount := data.DayModelCount
	if modelCount == 0 {
		modelCount = len(models)
	}
	projectCount := data.DayProjectCount
	if projectCount == 0 {
		projectCount = len(projects)
	}
	for _, model := range models {
		modelLines = append(modelLines, renderLedgerDayModel(render, model, leftWidth, totalModelCost, totalModelTokens))
	}
	if len(models) == 0 {
		message := "no model attribution"
		if data.SessionsHaveMore {
			message = "model rollup unavailable"
		}
		modelLines = append(modelLines, fitLine(render.Palette.Subtle().Render(message), leftWidth))
	}

	projectLines := []string{
		fitLine(render.Palette.Header().Render("PROJECTS ON THIS DAY"), rightWidth),
		fitLine(render.Palette.Subtle().Render(ledgerDayProjectHeader(rightWidth)), rightWidth),
	}
	for _, project := range projects {
		projectLines = append(projectLines, renderLedgerDayProject(render, project, rightWidth))
	}
	if len(projects) == 0 {
		message := "no indexed project activity"
		if data.SessionsHaveMore {
			message = "project rollup unavailable"
		}
		projectLines = append(projectLines, fitLine(render.Palette.Subtle().Render(message), rightWidth))
	}
	pairHeight := max(len(modelLines), len(projectLines))
	lines := strings.Split(joinLedgerColumns(strings.Join(modelLines, "\n"), strings.Join(projectLines, "\n"), leftWidth, rightWidth, pairHeight), "\n")
	lines = append(lines, ledgerRule(render, "SESSION STARTS BY HOUR  ·  LOCAL TIME", width))

	hours := append([]LedgerProfile(nil), data.DayHours...)
	if len(hours) == 0 {
		if data.SessionsHaveMore {
			hours = defaultHourProfiles()
		} else {
			hours = ledgerDayHourProfiles(data.Sessions, data.Location)
		}
	}
	hours = completeHourProfiles(hours)
	maximum := maxProfileValue(hours)
	for _, hour := range hours {
		lines = append(lines, renderLedgerDayHour(render, hour, maximum, width))
	}
	profileStatus := "complete"
	if data.SessionsHaveMore && len(data.DayHours) == 0 {
		profileStatus = "unavailable · page is incomplete"
	}
	facts := []string{
		fmt.Sprintf("loaded page sessions %s", formatCount(len(data.Sessions))),
		fmt.Sprintf("models represented   %s", formatCount(modelCount)),
		fmt.Sprintf("projects represented %s", formatCount(projectCount)),
		fmt.Sprintf("sessions on day      %s", ledgerDaySessionCount(data)),
		fmt.Sprintf("hour profile         %s", profileStatus),
		"session starts        local time",
		"source                history catalog",
		"rollups               selected day",
	}
	for _, fact := range facts {
		lines = append(lines, render.Palette.Subtle().Render(fitText(fact, width)))
	}
	return fitLedgerBlock(strings.Join(lines, "\n"), width, height)
}

func ledgerDayModelLabelWidth(width int) int {
	return max(8, width-28)
}

func ledgerDayModelHeader(width int) string {
	labelWidth := ledgerDayModelLabelWidth(width)
	return padRight("MODEL", labelWidth) + " " + padLeft("COST", 9) + " " + padLeft("SHARE", 5) + " " + padLeft("TOKENS", 9)
}

func renderLedgerDayModel(render theme.Context, model LedgerModel, width int, totalCost pricing.Money, totalTokens int64) string {
	label := cleanInline(model.Model)
	if model.Provider != "" {
		label = cleanInline(model.Provider + "/" + label)
	}
	labelWidth := ledgerDayModelLabelWidth(width)
	cost := fitMoney(formatMoney(model.Cost, model.PricedTokens, false, model.UnpricedTokens > 0), 9)
	share := ledgerDayShare(model.Cost, totalCost, model.Tokens, totalTokens)
	line := padRight(truncate(label, labelWidth), labelWidth) + " " + padLeft(cost, 9) + " " + padLeft(share, 5) + " " + padLeft(fitText(compactTokens(model.Tokens), 9), 9)
	return fitLine(render.Palette.Subtle().Render(line), width)
}

func ledgerDayProjectLabelWidth(width int) int {
	return max(8, width-18)
}

func ledgerDayProjectHeader(width int) string {
	labelWidth := ledgerDayProjectLabelWidth(width)
	return padRight("PROJECT", labelWidth) + " " + padLeft("SESSIONS", 8) + " " + padLeft("SHARE", 6)
}

func renderLedgerDayProject(render theme.Context, project LedgerProject, width int) string {
	labelWidth := ledgerDayProjectLabelWidth(width)
	line := padRight(truncate(cleanInline(project.Label), labelWidth), labelWidth) + " " + padLeft(formatCount(project.Sessions), 8) + " " + padLeft(fmt.Sprintf("%3.0f%%", project.Share*100), 6)
	return fitLine(render.Palette.Subtle().Render(line), width)
}

func ledgerDayShare(value pricing.Money, totalCost pricing.Money, tokens, totalTokens int64) string {
	if totalCost > 0 {
		return fmt.Sprintf("%3.0f%%", float64(value)/float64(totalCost)*100)
	}
	if totalTokens > 0 && tokens > 0 {
		return fmt.Sprintf("%3.0f%%", float64(tokens)/float64(totalTokens)*100)
	}
	return "—"
}

func ledgerDaySessionCount(data Data) string {
	if data.DaySessionCount > 0 {
		return formatCount(data.DaySessionCount)
	}
	if data.SessionsHaveMore {
		if len(data.Sessions) > 0 {
			return "≥ " + formatCount(len(data.Sessions))
		}
		return "unavailable"
	}
	return formatCount(len(data.Sessions))
}

func ledgerDayProjects(data Data) []LedgerProject {
	if len(data.DayProjects) > 0 {
		return append([]LedgerProject(nil), data.DayProjects...)
	}
	if data.SessionsHaveMore {
		return nil
	}
	counts := map[string]int{}
	for _, session := range data.Sessions {
		label := cleanInline(session.Project)
		if label == "" {
			label = "unknown"
		}
		counts[label]++
	}
	total := 0
	for _, count := range counts {
		total += count
	}
	projects := make([]LedgerProject, 0, len(counts))
	for label, count := range counts {
		share := 0.0
		if total > 0 {
			share = float64(count) / float64(total)
		}
		projects = append(projects, LedgerProject{Label: label, Sessions: count, Share: share})
	}
	sort.SliceStable(projects, func(left, right int) bool {
		if projects[left].Sessions != projects[right].Sessions {
			return projects[left].Sessions > projects[right].Sessions
		}
		return projects[left].Label < projects[right].Label
	})
	if len(projects) > 8 {
		projects = projects[:8]
	}
	return projects
}

func ledgerDayHourProfiles(sessions []LedgerSession, location *time.Location) []LedgerProfile {
	profiles := defaultHourProfiles()
	for _, session := range sessions {
		timestamp := ""
		if session.FirstTimestamp != nil {
			timestamp = *session.FirstTimestamp
		} else {
			timestamp = session.ActivityTimestamp
		}
		parsed, err := time.Parse(time.RFC3339Nano, timestamp)
		if err != nil {
			continue
		}
		if location != nil {
			parsed = parsed.In(location)
		}
		profiles[parsed.Hour()].Value++
		profiles[parsed.Hour()].Sessions++
	}
	return profiles
}

func renderLedgerDayHour(render theme.Context, hour LedgerProfile, maximum, width int) string {
	barWidth := max(8, min(48, width-30))
	bar := profileBlock(render, hour.Value, maximum, barWidth)
	return fitLine(fmt.Sprintf("%s:00  %3d sessions  %s", hour.Label, hour.Value, bar), width)
}

type ledgerDisplayRow struct {
	Row       Row
	Selected  bool
	Indent    bool
	Delta     pricing.Money
	DeltaBase int64
	DeltaOK   bool
	Partial   bool
}

func renderWidePeriods(render theme.Context, data Data, state State, width, height int) string {
	b1, b2, b3 := ledgerBandHeights(height)
	periodBand := renderWidePeriodBand(render, data, state, width, b1)
	chartBand := renderSpendByMonth(render, data, state, width, b2)
	profileBand := renderLedgerProfiles(render, data, state, width, b3)
	return joinLedgerBlocks(width, []string{periodBand, chartBand, profileBand}, []int{b1, b2, b3})
}

func renderStandardPeriods(render theme.Context, data Data, state State, width, height int) string {
	if height < 3 {
		return fitLedgerBlock(renderLegacyPeriods(render, data, state, width, height), width, height)
	}
	usable := height - 1
	b1 := min(17, max(1, (usable+1)/2))
	b2 := max(1, usable-b1)
	// Keep the provider columns visible at the standard dashboard width. The
	// standard layout is shorter than the wide layout, not a different ledger
	// contract.
	periodBand := renderPeriodTableBand(render, data, state, width, b1, true)
	chartBand := renderSpendByMonth(render, data, state, width, b2)
	return joinLedgerBlocks(width, []string{periodBand, chartBand}, []int{b1, b2})
}

func ledgerBandHeights(height int) (int, int, int) {
	height = max(1, height)
	usable := max(1, height-2)
	first := min(17, usable)
	remaining := usable - first
	second := min(16, max(0, remaining))
	third := max(0, remaining-second)
	return first, second, third
}

func renderWidePeriodBand(render theme.Context, data Data, state State, width, height int) string {
	sideWidth := min(57, max(36, width/3))
	leftWidth := max(1, width-sideWidth-2)
	left := renderPeriodTableBand(render, data, state, leftWidth, height, true)
	right := renderPeriodDetail(render, data, state, sideWidth, height)
	return joinLedgerColumns(left, right, leftWidth, sideWidth, height)
}

func renderPeriodTableBand(render theme.Context, data Data, state State, width, height int, wide bool) string {
	rows := ledgerDisplayRows(data, state)
	if !wide {
		lines := []string{fitLine(render.Palette.Subtle().Render(breadcrumb(state)+"  ·  l zoom in · h zoom out · enter open"), width)}
		lines = append(lines, render.Palette.Header().Render(fitText("PERIODS", width)))
		return fitLedgerBlock(strings.Join(append(lines, renderSimplePeriodRows(render, rows, data, width, height-len(lines))...), "\n"), width, height)
	}
	lines := []string{fitLine(render.Palette.Subtle().Render(breadcrumb(state)+"  ·  l zoom in · h zoom out · enter open"), width)}
	lines = append(lines, ledgerRule(render, periodTitle(data, state), width))
	columns := periodColumnsForWidth(width)
	lines = append(lines, ledgerPeriodHeader(render, columns))
	maxTokens := maxDisplayTokens(rows)
	available := max(1, height-len(lines))
	if len(rows) > available {
		selected := displaySelectedIndex(rows)
		start, end := visibleWindow(len(rows), selected, available)
		rows = rows[start:end]
	}
	for _, item := range rows {
		lines = append(lines, renderLedgerPeriodRow(render, item, columns, maxTokens))
	}
	return fitLedgerBlock(strings.Join(lines, "\n"), width, height)
}

func periodTitle(data Data, state State) string {
	if state.Zoom == ZoomYear {
		return fmt.Sprintf("PERIODS  ·  %d year · 12 months", max(1, len(data.Rows)))
	}
	if state.Zoom == ZoomMonth {
		year := state.Year
		if year == 0 {
			year = data.Year
		}
		return fmt.Sprintf("PERIODS  ·  %04d · 12 months", year)
	}
	return "PERIODS  ·  days"
}

func renderSimplePeriodRows(render theme.Context, rows []ledgerDisplayRow, data Data, width, height int) []string {
	if height <= 0 {
		return nil
	}
	columns := simplePeriodColumns(width)
	maxTokens := maxDisplayTokens(rows)
	if len(rows) > height {
		selected := displaySelectedIndex(rows)
		start, end := visibleWindow(len(rows), selected, height)
		rows = rows[start:end]
	}
	lines := []string{ledgerSimpleHeader(render, columns)}
	for _, item := range rows {
		lines = append(lines, renderSimplePeriodRow(render, item, columns, maxTokens))
	}
	return lines
}

type ledgerPeriodColumns struct {
	Period, Sessions, Tokens, Money, Delta, Activity int
}

func periodColumnsForWidth(width int) ledgerPeriodColumns {
	columns := ledgerPeriodColumns{Period: 14, Sessions: 8, Tokens: 10, Money: 10, Delta: 9}
	const separators = 14
	columns.Activity = max(4, width-columns.Period-columns.Sessions-columns.Tokens-columns.Money*3-columns.Delta-separators)
	if columns.Activity < 6 {
		columns.Period, columns.Sessions, columns.Tokens, columns.Money, columns.Delta = 12, 7, 9, 9, 8
		columns.Activity = max(4, width-columns.Period-columns.Sessions-columns.Tokens-columns.Money*3-columns.Delta-separators)
	}
	return columns
}

func simplePeriodColumns(width int) ledgerPeriodColumns {
	return ledgerPeriodColumns{Period: max(10, width-37), Sessions: 7, Tokens: 9, Money: 9, Delta: 8, Activity: 0}
}

func ledgerPeriodHeader(render theme.Context, columns ledgerPeriodColumns) string {
	return strings.Join([]string{
		headerCell(render, "PERIOD", columns.Period, false),
		headerCell(render, "SESSIONS", columns.Sessions, true),
		headerCell(render, "TOKENS", columns.Tokens, true),
		headerCell(render, "CODEX", columns.Money, true),
		headerCell(render, "CLAUDE", columns.Money, true),
		headerCell(render, "TOTAL", columns.Money, true),
		headerCell(render, "Δ PRIOR", columns.Delta, true),
		headerCell(render, "ACTIVITY", columns.Activity, false),
	}, "  ")
}

func ledgerSimpleHeader(render theme.Context, columns ledgerPeriodColumns) string {
	return strings.Join([]string{
		headerCell(render, "PERIOD", columns.Period, false),
		headerCell(render, "SESSIONS", columns.Sessions, true),
		headerCell(render, "TOKENS", columns.Tokens, true),
		headerCell(render, "TOTAL", columns.Money, true),
		headerCell(render, "Δ PRIOR", columns.Delta, true),
	}, "  ")
}

func renderLedgerPeriodRow(render theme.Context, item ledgerDisplayRow, columns ledgerPeriodColumns, maxTokens int64) string {
	row := item.Row
	label := strings.Repeat("  ", boolInt(item.Indent)) + row.Label
	if item.Selected {
		label = "› " + label
		label = render.Palette.Emphasis().Bold(true).Render(truncate(label, columns.Period))
	} else {
		label = "  " + truncate(label, max(1, columns.Period-2))
	}
	label = aligned(label, columns.Period, false)
	sessions := "—"
	if row.Sessions > 0 {
		sessions = commaInteger(int64(row.Sessions))
	}
	return fitLine(strings.Join([]string{
		label,
		aligned(render.Palette.Subtle().Render(fitText(sessions, columns.Sessions)), columns.Sessions, true),
		aligned(render.Palette.Emphasis().Render(fitText(compactTokens(row.Total().Tokens), columns.Tokens)), columns.Tokens, true),
		moneyCell(render, row.Codex, columns.Money),
		moneyCell(render, row.Claude, columns.Money),
		moneyCell(render, row.Total(), columns.Money),
		deltaCell(render, item.Delta, item.DeltaBase, item.Partial, columns.Delta),
		activityBar(render, row, columns.Activity, maxTokens, false),
	}, "  "), columns.Period+columns.Sessions+columns.Tokens+columns.Money*3+columns.Delta+columns.Activity+14)
}

func renderSimplePeriodRow(render theme.Context, item ledgerDisplayRow, columns ledgerPeriodColumns, maxTokens int64) string {
	row := item.Row
	label := "  " + truncate(row.Label, max(1, columns.Period-2))
	if item.Selected {
		label = render.Palette.Emphasis().Bold(true).Render("› " + truncate(row.Label, max(1, columns.Period-2)))
	}
	sessions := "—"
	if row.Sessions > 0 {
		sessions = commaInteger(int64(row.Sessions))
	}
	return fitLine(strings.Join([]string{
		aligned(label, columns.Period, false),
		aligned(render.Palette.Subtle().Render(fitText(sessions, columns.Sessions)), columns.Sessions, true),
		aligned(render.Palette.Emphasis().Render(fitText(compactTokens(row.Total().Tokens), columns.Tokens)), columns.Tokens, true),
		moneyCell(render, row.Total(), columns.Money),
		deltaCell(render, item.Delta, item.DeltaBase, item.Partial, columns.Delta),
	}, "  "), columns.Period+columns.Sessions+columns.Tokens+columns.Money+columns.Delta+8)
}

func ledgerDisplayRows(data Data, state State) []ledgerDisplayRow {
	selectedIndex := SelectedIndex(data, state)
	base := data.Rows
	items := []ledgerDisplayRow{}
	if state.Zoom == ZoomYear {
		items = append(items, ledgerDisplayRow{Row: data.Total})
	}
	for index, row := range base {
		item := ledgerDisplayRow{Row: row, Selected: index == selectedIndex}
		if index+1 < len(base) {
			current, previous := row.Total(), base[index+1].Total()
			if current.PricedTokens > 0 && previous.PricedTokens > 0 {
				item.Delta, item.DeltaBase, item.DeltaOK = current.Cost-previous.Cost, current.PricedTokens, true
				item.Partial = current.UnpricedTokens > 0 || previous.UnpricedTokens > 0
			}
		}
		items = append(items, item)
		if state.Zoom == ZoomYear && item.Selected {
			year := 0
			if len(row.Key) == 4 {
				year, _ = strconv.Atoi(row.Key)
			}
			months := ledgerMonthRows(ledgerMonthsForYear(data.Analytics.Months, year))
			for monthIndex, month := range months {
				monthItem := ledgerDisplayRow{Row: month, Indent: true}
				if monthIndex > 0 {
					current, previous := month.Total(), months[monthIndex-1].Total()
					if current.PricedTokens > 0 && previous.PricedTokens > 0 {
						monthItem.Delta, monthItem.DeltaBase, monthItem.DeltaOK = current.Cost-previous.Cost, current.PricedTokens, true
						monthItem.Partial = current.UnpricedTokens > 0 || previous.UnpricedTokens > 0
					}
				}
				items = append(items, monthItem)
			}
		}
	}
	return items
}

func ledgerMonthRows(months []LedgerMonth) []Row {
	rows := make([]Row, 0, len(months))
	for _, month := range months {
		rows = append(rows, Row{Key: month.Key, Label: month.Label, Sessions: month.Sessions, Codex: month.Codex, Claude: month.Claude})
	}
	return rows
}

func ledgerMonthRowsForYear(months []LedgerMonth, year int) []Row {
	rows := ledgerMonthRows(ledgerMonthsForYear(months, year))
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	return rows
}

func ledgerMonthsForYear(months []LedgerMonth, year int) []LedgerMonth {
	if year == 0 {
		return append([]LedgerMonth(nil), months...)
	}
	want := fmt.Sprintf("%04d-", year)
	filtered := make([]LedgerMonth, 0, 12)
	for _, month := range months {
		if strings.HasPrefix(month.Key, want) {
			filtered = append(filtered, month)
		}
	}
	return filtered
}

func displaySelectedIndex(rows []ledgerDisplayRow) int {
	for index, row := range rows {
		if row.Selected {
			return index
		}
	}
	return 0
}

func maxDisplayTokens(rows []ledgerDisplayRow) int64 {
	var maximum int64
	for _, row := range rows {
		if tokens := row.Row.Total().Tokens; tokens > maximum {
			maximum = tokens
		}
	}
	return maximum
}

func renderPeriodDetail(render theme.Context, data Data, state State, width, height int) string {
	selected := selectedLedgerRow(data, state)
	total := selected.Total()
	activeDays, averageCost, peakCost, peakDay, partial, peakPartial := selectedLedgerMetrics(data, state, selected)
	lines := []string{
		ledgerRule(render, "PERIOD DETAIL", width),
		fitLine(render.Palette.Emphasis().Bold(true).Render(truncate(selected.Label+"  selected", width)), width),
		fitLine(fmt.Sprintf("SPEND %s  CODEX %s  CLAUDE %s", formatMoney(total.Cost, total.PricedTokens, false, partial), formatMoney(selected.Codex.Cost, selected.Codex.PricedTokens, false, selected.Codex.UnpricedTokens > 0), formatMoney(selected.Claude.Cost, selected.Claude.PricedTokens, false, selected.Claude.UnpricedTokens > 0)), width),
		fitLine(fmt.Sprintf("TOKENS %s  SESSIONS %s", commaInteger(total.Tokens), formatCount(selected.Sessions)), width),
		fitLine(fmt.Sprintf("ACTIVE %s  AVG/DAY %s  PEAK %s %s", formatCount(activeDays), formatMoney(averageCost, total.PricedTokens, false, partial), formatMoney(peakCost, total.PricedTokens, false, peakPartial), displayLedgerDay(peakDay)), width),
		ledgerRule(render, "MODELS · ALL TIME", width),
		ledgerModelSummary(render, data.Analytics.Models, width),
		ledgerRule(render, "PRICING PROVENANCE", width),
		ledgerProvenanceSummary(render, data.Analytics.Provenance, width),
		ledgerRule(render, "COST PER 1M", width),
		ledgerRateSummary(render, data.Analytics.Models, width),
		ledgerRule(render, "PROVIDER × MONTH", width),
		renderProviderMonthSummary(render, selectedLedgerProviderMonths(data, state, selected), width),
		ledgerRule(render, "ZOOM STACK", width),
		fitLine(render.Palette.Subtle().Render("all years  ·  h out"), width),
		fitLine(render.Palette.Emphasis().Render(breadcrumbZoomLabel(state)+"  ·  l in"), width),
		fitLine(render.Palette.Subtle().Render("day  ·  enter open  ·  sessions  ·  esc collapse"), width),
	}
	return fitLedgerBlock(strings.Join(lines, "\n"), width, height)
}

func ledgerModelSummary(render theme.Context, models []LedgerModel, width int) string {
	parts := []string{}
	for _, model := range models {
		parts = append(parts, fmt.Sprintf("%s %s", truncate(cleanInline(model.Model), 20), formatMoney(model.Cost, model.PricedTokens, false, model.UnpricedTokens > 0)))
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return fitLine(render.Palette.Subtle().Render("no model pricing data"), width)
	}
	return fitLine(render.Palette.Subtle().Render(strings.Join(parts, "  ")), width)
}

func ledgerProvenanceSummary(render theme.Context, provenance LedgerProvenance, width int) string {
	value := fmt.Sprintf("published %d %s  proxy %d %s  est %d %s  unpriced %d", provenance.PublishedModels, formatMoney(provenance.PublishedCost, provenance.PublishedTokens, false, false), provenance.ProxyModels, formatMoney(provenance.ProxyCost, provenance.ProxyTokens, false, false), provenance.EstimatedModels, formatMoney(provenance.EstimatedCost, provenance.EstimatedTokens, false, false), provenance.UnpricedModels)
	return fitLine(render.Palette.Subtle().Render(value), width)
}

func ledgerRateSummary(render theme.Context, models []LedgerModel, width int) string {
	parts := []string{}
	for _, model := range models {
		if !model.HasRate {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s $%.2f", truncate(cleanInline(model.Model), 18), float64(model.CostPerMillion)/1000))
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return fitLine(render.Palette.Subtle().Render("no published rates"), width)
	}
	return fitLine(render.Palette.Subtle().Render(strings.Join(parts, "  ")), width)
}

func selectedLedgerMetrics(data Data, state State, selected Row) (int, pricing.Money, pricing.Money, string, bool, bool) {
	if state.Zoom == ZoomDay {
		total := selected.Total()
		partial := total.UnpricedTokens > 0
		return 1, total.Cost, total.Cost, selected.Key, partial, partial
	}
	prefix := selected.Key
	if state.Zoom == ZoomYear && len(selected.Key) == 4 {
		prefix = selected.Key + "-"
	}
	var activeDays int
	var totalCost, peakCost pricing.Money
	peakDay := ""
	partial, peakPartial, matched := false, false, false
	for _, month := range data.Analytics.Months {
		if prefix == "" || !strings.HasPrefix(month.Key, prefix) {
			continue
		}
		matched = true
		activeDays += month.ActiveDays
		total := month.Total()
		totalCost += total.Cost
		partial = partial || total.UnpricedTokens > 0
		if month.PeakCost > peakCost {
			peakCost, peakDay, peakPartial = month.PeakCost, month.PeakDay, month.PeakPartial
		}
	}
	if !matched {
		return data.Analytics.ActiveDays, data.Analytics.AverageCost, data.Analytics.PeakCost, data.Analytics.PeakDay, false, false
	}
	if activeDays == 0 {
		return 0, 0, 0, "", partial, false
	}
	return activeDays, totalCost / pricing.Money(activeDays), peakCost, peakDay, partial, peakPartial
}

func displayLedgerDay(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func selectedLedgerRow(data Data, state State) Row {
	if index := SelectedIndex(data, state); index >= 0 && index < len(data.Rows) {
		return data.Rows[index]
	}
	if len(data.Analytics.Months) > 0 {
		month := data.Analytics.Months[len(data.Analytics.Months)-1]
		return Row{Key: month.Key, Label: month.Label, Sessions: month.Sessions, Codex: month.Codex, Claude: month.Claude}
	}
	return data.Total
}

func renderLedgerModel(render theme.Context, model LedgerModel, width int) string {
	left := truncate(model.Model, max(8, width-28))
	cost := formatMoney(model.Cost, model.PricedTokens, false, model.UnpricedTokens > 0)
	share := ""
	if model.Tokens > 0 {
		share = compactTokens(model.Tokens)
	}
	return fitLine(render.Palette.Provider(model.Provider, 0).Render(padRight(left, max(8, width-28)))+"  "+padLeft(share, 8)+"  "+padLeft(cost, 10), width)
}

func renderProviderMonthSummary(render theme.Context, values []LedgerProviderMonth, width int) string {
	if len(values) == 0 {
		return fitLine(render.Palette.Subtle().Render("no provider month data"), width)
	}
	parts := []string{}
	for _, value := range values {
		if value.Cost == 0 && value.Tokens == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", value.Provider, value.Month[5:], formatMoney(value.Cost, 1, false, false)))
		if len(parts) == 3 {
			break
		}
	}
	if len(parts) == 0 {
		return fitLine(render.Palette.Subtle().Render("no provider month data"), width)
	}
	return fitLine(render.Palette.Subtle().Render(strings.Join(parts, "  ")), width)
}

func selectedLedgerProviderMonths(data Data, state State, selected Row) []LedgerProviderMonth {
	prefix := selected.Key
	switch state.Zoom {
	case ZoomYear:
		if len(selected.Key) == 4 {
			prefix = selected.Key + "-"
		}
	case ZoomMonth, ZoomDay:
		if len(selected.Key) >= 7 {
			prefix = selected.Key[:7]
		} else if len(state.Month) >= 7 {
			prefix = state.Month[:7]
		}
	}
	values := []LedgerProviderMonth{}
	for _, value := range data.Analytics.ProviderMonths {
		if prefix != "" && !strings.HasPrefix(value.Month, prefix) {
			continue
		}
		if value.Cost == 0 && value.Tokens == 0 {
			continue
		}
		values = append(values, value)
	}
	return values
}

func renderSpendByMonth(render theme.Context, data Data, state State, width, height int) string {
	months := selectedLedgerChartMonths(data, state)
	if len(months) == 0 {
		months = make([]LedgerMonth, 0, len(data.Rows))
		for _, row := range data.Rows {
			months = append(months, LedgerMonth{Key: row.Key, Label: row.Label, Sessions: row.Sessions, Codex: row.Codex, Claude: row.Claude})
		}
	}
	if len(months) == 0 {
		return fitLedgerBlock(ledgerRule(render, "SPEND BY MONTH", width)+"\n"+render.Palette.Subtle().Render("no monthly usage"), width, height)
	}
	maxCost := pricing.Money(0)
	var totalCost pricing.Money
	for _, month := range months {
		value := month.Total()
		maxCost = maxMoney(maxCost, value.Cost)
		totalCost += value.Cost
	}
	chartRows := max(3, min(9, height-4))
	lines := []string{ledgerRule(render, "SPEND BY MONTH  ·  "+chartYear(months, state), width)}
	plotWidth := max(12, width-12)
	cellWidth := max(1, min(4, (plotWidth-max(0, len(months)-1))/max(1, len(months))))
	chartWidth := len(months)*cellWidth + max(0, len(months)-1)
	avg := pricing.Money(0)
	if len(months) > 0 {
		avg = totalCost / pricing.Money(len(months))
	}
	for row := chartRows; row >= 1; row-- {
		label := "     "
		if row == chartRows || row == (chartRows+1)/2 || row == 1 {
			tick := scaleMoney(maxCost, int64(row-1), int64(max(1, chartRows-1)))
			label = padLeft(fitMoney(formatMoney(tick, 1, false, false), 6), 6)
		}
		cells := []string{}
		for index, month := range months {
			value := month.Total().Cost
			level := 0
			if maxCost > 0 {
				level = int(scaleMoney(value, int64(chartRows), int64(maxCost)))
			}
			cell := strings.Repeat(" ", cellWidth)
			if value <= 0 {
				cell = strings.Repeat("·", cellWidth)
			} else if level >= row {
				cell = strings.Repeat("█", cellWidth)
			} else if level+1 == row {
				cell = strings.Repeat("▂", cellWidth)
			}
			if month.Codex.Tokens == 0 && month.Claude.Tokens > 0 {
				cell = render.Palette.Provider("claude", 0).Render(cell)
			} else {
				cell = render.Palette.Provider("codex", 0).Render(cell)
			}
			cells = append(cells, cell)
			if index < len(months)-1 {
				cells = append(cells, " ")
			}
		}
		lines = append(lines, fitLine(label+"┤"+strings.Join(cells, ""), width))
	}
	avgLabel := "avg " + fitMoney(formatMoney(avg, 1, false, false), 10)
	lines = append(lines, fitLine("     "+render.Palette.Subtle().Render(strings.Repeat("┈", max(1, chartWidth)))+"  "+avgLabel, width))
	labels := []string{}
	for _, month := range months {
		label := month.Key
		if len(label) >= 7 {
			label = strings.ToUpper(label[5:7])
		}
		labels = append(labels, padLeft(label, cellWidth))
	}
	lines = append(lines, fitLine("      "+strings.Join(labels, " "), width))
	caption := monthlyCaption(months, width)
	lines = append(lines, fitLine(render.Palette.Subtle().Render(caption), width))
	return fitLedgerBlock(strings.Join(lines, "\n"), width, height)
}

func scaleMoney(value pricing.Money, numerator, denominator int64) pricing.Money {
	if value <= 0 || numerator <= 0 || denominator <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(value), uint64(numerator))
	divisor := uint64(denominator)
	if hi >= divisor {
		return pricing.Money(1<<63 - 1)
	}
	quotient, _ := bits.Div64(hi, lo, divisor)
	if quotient > uint64(1<<63-1) {
		return pricing.Money(1<<63 - 1)
	}
	return pricing.Money(quotient)
}

func selectedLedgerChartMonths(data Data, state State) []LedgerMonth {
	months := data.Analytics.Months
	if len(months) == 0 {
		return nil
	}
	selected := selectedLedgerRow(data, state)
	prefix := ""
	switch state.Zoom {
	case ZoomYear:
		if len(selected.Key) == 4 {
			prefix = selected.Key + "-"
		}
	case ZoomMonth:
		year := state.Year
		if year == 0 {
			year = data.Year
		}
		if year > 0 {
			prefix = fmt.Sprintf("%04d-", year)
		}
	case ZoomDay:
		month := state.Month
		if month == "" {
			month = data.Month
		}
		if len(month) < 7 && len(selected.Key) >= 7 {
			month = selected.Key[:7]
		}
		if len(month) >= 7 {
			prefix = month[:7]
		}
	}
	if prefix == "" {
		return months
	}
	filtered := make([]LedgerMonth, 0, 12)
	for _, month := range months {
		if strings.HasPrefix(month.Key, prefix) {
			filtered = append(filtered, month)
		}
	}
	return filtered
}

func chartYear(months []LedgerMonth, state State) string {
	if state.Year > 0 {
		return strconv.Itoa(state.Year)
	}
	if len(months) > 0 && len(months[0].Key) >= 4 {
		return months[0].Key[:4]
	}
	return "all time"
}

func monthlyCaption(months []LedgerMonth, width int) string {
	active := []string{}
	for _, month := range months {
		if month.Total().Tokens == 0 {
			continue
		}
		active = append(active, fmt.Sprintf("%s %s", month.Label, formatMoney(month.Total().Cost, month.Total().PricedTokens, false, month.Total().UnpricedTokens > 0)))
	}
	if len(active) == 0 {
		return "no indexed activity in this period"
	}
	const separator = "  ·  "
	selected := []string{}
	used := 0
	for index := len(active) - 1; index >= 0 && len(selected) < 3; index-- {
		entryWidth := lipgloss.Width(active[index])
		separatorWidth := 0
		if len(selected) > 0 {
			separatorWidth = lipgloss.Width(separator)
		}
		if len(selected) > 0 && used+separatorWidth+entryWidth > width {
			break
		}
		if len(selected) == 0 && entryWidth > width {
			break
		}
		selected = append([]string{active[index]}, selected...)
		used += separatorWidth + entryWidth
	}
	if len(selected) == 0 {
		return "activity details exceed this width"
	}
	return strings.Join(selected, separator)
}

func renderLedgerProfiles(render theme.Context, data Data, state State, width, height int) string {
	if height <= 0 {
		return ""
	}
	leftWidth := max(1, width/2)
	rightWidth := max(1, width-leftWidth-2)
	left := renderProjectProfile(render, data, state, leftWidth, height)
	right := renderTimeProfiles(render, data, rightWidth, height)
	return joinLedgerColumns(left, right, leftWidth, rightWidth, height)
}

func renderProjectProfile(render theme.Context, data Data, state State, width, height int) string {
	months := selectedLedgerChartMonths(data, state)
	lines := []string{ledgerRule(render, "PROJECTS  ·  "+chartYear(months, state), width), ledgerSimpleProfileHeader(render, "PROJECT", "SESSIONS", "SHARE", width)}
	for _, project := range data.Analytics.Projects {
		label := cleanInline(project.Label)
		share := fmt.Sprintf("%3.0f%%", project.Share*100)
		lines = append(lines, fitLine(padRight(truncate(label, max(8, width-24)), max(8, width-24))+padLeft(fmt.Sprintf("%d", project.Sessions), 9)+"  "+padLeft(share, 6), width))
	}
	if len(data.Analytics.Projects) == 0 {
		lines = append(lines, fitLine(render.Palette.Subtle().Render("no indexed project activity"), width))
	}
	lines = append(lines, ledgerRule(render, "PROJECT × MONTH  ·  INTENSITY", width))
	monthLabels := analyticsMonthLabels(months)
	lines = append(lines, fitLine("          "+strings.Join(monthLabels, " "), width))
	maxSessions := 0
	for _, value := range data.Analytics.ProjectMonths {
		maxSessions = max(maxSessions, value.Sessions)
	}
	for _, project := range data.Analytics.Projects {
		label := cleanInline(project.Label)
		cells := make([]string, 0, len(monthLabels))
		for _, month := range monthLabels {
			count := projectMonthCount(data.Analytics.ProjectMonths, project.Label, month)
			cells = append(cells, ledgerIntensity(count, maxSessions))
		}
		line := padRight(truncate(label, 9), 9) + " " + strings.Join(cells, " ")
		lines = append(lines, fitLine(render.Palette.Subtle().Render(line), width))
	}
	if len(data.Analytics.ProjectMonths) == 0 {
		lines = append(lines, fitLine(render.Palette.Subtle().Render("matrix needs indexed sessions"), width))
	}
	return fitLedgerBlock(strings.Join(lines, "\n"), width, height)
}

func renderTimeProfiles(render theme.Context, data Data, width, height int) string {
	lines := []string{ledgerRule(render, "WEEKDAY PROFILE  ·  BY SESSIONS", width)}
	maxValue := maxProfileValue(data.Analytics.Weekdays)
	weekdays := data.Analytics.Weekdays
	if len(weekdays) == 0 {
		weekdays = defaultWeekdayProfiles()
	}
	for _, profile := range weekdays {
		lines = append(lines, profileBarLine(render, profile.Label, profile.Value, maxValue, width))
	}
	lines = append(lines, ledgerRule(render, "HOUR OF DAY  ·  SESSIONS STARTED", width))
	hours := data.Analytics.Hours
	hours = completeHourProfiles(hours)
	if height-len(lines) >= 12 {
		maxValue = maxProfileValue(hours)
		for index := 0; index < 12; index++ {
			left, right := hours[index], hours[index+12]
			lines = append(lines, hourPairLine(render, left, right, maxValue, width))
		}
	} else {
		lines = append(lines, hourHistogramLine(render, hours, width))
	}
	lines = appendTimeProfileFacts(render, lines, weekdays, hours, width, height)
	return fitLedgerBlock(strings.Join(lines, "\n"), width, height)
}

func hourPairLine(render theme.Context, left, right LedgerProfile, maximum, width int) string {
	leftBlock := profileBlock(render, left.Value, maximum, 8)
	rightBlock := profileBlock(render, right.Value, maximum, 8)
	return fitLine(fmt.Sprintf("%02s %3d %s   %02s %3d %s", left.Label, left.Value, leftBlock, right.Label, right.Value, rightBlock), width)
}

func hourHistogramLine(render theme.Context, hours []LedgerProfile, width int) string {
	maximum := maxProfileValue(hours)
	parts := make([]string, 0, 24)
	for hour := 0; hour < 24; hour++ {
		value := hours[hour]
		glyph := profileGlyph(value.Value, maximum)
		parts = append(parts, fmt.Sprintf("%02s%s", value.Label, render.Palette.Emphasis().Render(glyph)))
	}
	return fitLine(strings.Join(parts, ""), width)
}

func profileBlock(render theme.Context, value, maximum, width int) string {
	if width <= 0 {
		return ""
	}
	glyph := profileGlyph(value, maximum)
	filled := 0
	if maximum > 0 && value > 0 {
		filled = max(1, value*width/maximum)
	}
	return render.Palette.Emphasis().Render(strings.Repeat(glyph, filled)) + render.Palette.Subtle().Render(strings.Repeat("·", width-filled))
}

func profileGlyph(value, maximum int) string {
	if value <= 0 || maximum <= 0 {
		return "·"
	}
	level := min(4, max(1, (value*4+maximum-1)/maximum))
	return []string{"·", "░", "▒", "▓", "█"}[level]
}

func appendTimeProfileFacts(render theme.Context, lines []string, weekdays, hours []LedgerProfile, width, height int) []string {
	peakWeekday := peakProfile(weekdays)
	peakHour := peakProfile(hours)
	facts := []string{
		"local time · session start buckets",
		fmt.Sprintf("indexed sessions  %d", profileTotal(hours)),
		fmt.Sprintf("peak weekday     %s", profileLabelOrDash(peakWeekday)),
		fmt.Sprintf("peak hour        %s", profileLabelOrDash(peakHour)),
		"empty buckets stay visible for comparison",
		"source           history catalog",
	}
	for len(lines) < height && len(facts) > 0 {
		lines = append(lines, render.Palette.Subtle().Render(fitText(facts[0], width)))
		facts = facts[1:]
	}
	return lines
}

func peakProfile(values []LedgerProfile) LedgerProfile {
	var peak LedgerProfile
	for _, value := range values {
		if value.Value > peak.Value {
			peak = value
		}
	}
	return peak
}

func profileLabelOrDash(value LedgerProfile) string {
	if value.Value <= 0 || value.Label == "" {
		return "—"
	}
	return fmt.Sprintf("%s (%d)", value.Label, value.Value)
}

func profileTotal(values []LedgerProfile) int {
	total := 0
	for _, value := range values {
		total += value.Value
	}
	return total
}

func profileBarLine(render theme.Context, label string, value, maximum, width int) string {
	barWidth := max(4, width-18)
	filled := 0
	if maximum > 0 {
		filled = min(barWidth, value*barWidth/maximum)
		if value > 0 && filled == 0 {
			filled = 1
		}
	}
	bar := render.Palette.Emphasis().Render(strings.Repeat("█", filled)) + render.Palette.Subtle().Render(strings.Repeat("·", barWidth-filled))
	return fitLine(padRight(label, 7)+" "+bar+" "+padLeft(fmt.Sprintf("%d", value), 5), width)
}

func defaultWeekdayProfiles() []LedgerProfile {
	return []LedgerProfile{{Label: "Mon"}, {Label: "Tue"}, {Label: "Wed"}, {Label: "Thu"}, {Label: "Fri"}, {Label: "Sat"}, {Label: "Sun"}}
}

func defaultHourProfiles() []LedgerProfile {
	profiles := make([]LedgerProfile, 24)
	for index := range profiles {
		profiles[index].Label = fmt.Sprintf("%02d", index)
	}
	return profiles
}

func completeHourProfiles(values []LedgerProfile) []LedgerProfile {
	profiles := defaultHourProfiles()
	for _, value := range values {
		hour, err := strconv.Atoi(value.Label)
		if err != nil || hour < 0 || hour >= len(profiles) {
			continue
		}
		profiles[hour] = value
	}
	return profiles
}

func maxProfileValue(values []LedgerProfile) int {
	maximum := 0
	for _, value := range values {
		maximum = max(maximum, value.Value)
	}
	return maximum
}

func analyticsMonthLabels(months []LedgerMonth) []string {
	labels := make([]string, 0, len(months))
	for _, month := range months {
		if len(month.Key) >= 7 {
			labels = append(labels, month.Key[5:7])
		} else {
			labels = append(labels, month.Label)
		}
	}
	if len(labels) == 0 {
		for index := 1; index <= 12; index++ {
			labels = append(labels, fmt.Sprintf("%02d", index))
		}
	}
	return labels
}

func projectMonthCount(values []LedgerProjectMonth, project, month string) int {
	for _, value := range values {
		if value.Project == project && (value.Month == month || strings.HasSuffix(value.Month, "-"+month)) {
			return value.Sessions
		}
	}
	return 0
}

func ledgerIntensity(value, maximum int) string {
	if value <= 0 || maximum <= 0 {
		return "·"
	}
	levels := []string{"░", "▒", "▓", "█"}
	index := min(len(levels)-1, max(0, (value*len(levels)-1)/maximum))
	return levels[index]
}

func ledgerSimpleProfileHeader(render theme.Context, left, middle, right string, width int) string {
	return fitLine(render.Palette.Header().Render(padRight(left, max(1, width-17))+padLeft(middle, 9)+"  "+padLeft(right, 6)), width)
}

func joinLedgerBlocks(width int, blocks []string, heights []int) string {
	lines := []string{}
	for index, block := range blocks {
		if heights[index] <= 0 {
			continue
		}
		if len(lines) > 0 {
			lines = append(lines, strings.Repeat("─", width))
		}
		lines = append(lines, strings.Split(fitLedgerBlock(block, width, heights[index]), "\n")...)
	}
	return strings.Join(lines, "\n")
}

func joinLedgerColumns(left, right string, leftWidth, rightWidth, height int) string {
	leftLines := strings.Split(fitLedgerBlock(left, leftWidth, height), "\n")
	rightLines := strings.Split(fitLedgerBlock(right, rightWidth, height), "\n")
	rows := make([]string, height)
	for index := range rows {
		rows[index] = leftLines[index] + "  " + rightLines[index]
	}
	return strings.Join(rows, "\n")
}

func fitLedgerBlock(value string, width, height int) string {
	width, height = max(1, width), max(1, height)
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return strings.Join(lines, "\n")
}

func ledgerRule(render theme.Context, title string, width int) string {
	label := render.Palette.Header().Render(strings.ToUpper(title))
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitLine(label, width)
	}
	return fitLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func ledgerKeyValue(render theme.Context, key, value string, width int) string {
	return fitLine(render.Palette.Subtle().Render(padRight(key, 18))+" "+truncate(value, max(1, width-19)), width)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func compactTokens(value int64) string {
	if value < 1_000 {
		return commaInteger(value)
	}
	for _, suffix := range []struct {
		threshold float64
		label     string
	}{{1_000_000_000, "B"}, {1_000_000, "M"}, {1_000, "k"}} {
		if float64(value) < suffix.threshold {
			continue
		}
		amount := float64(value) / suffix.threshold
		return fmt.Sprintf("%.2f%s", amount, suffix.label)
	}
	return commaInteger(value)
}

func formatCount(value int) string {
	if value <= 0 {
		return "—"
	}
	return commaInteger(int64(value))
}

func maxMoney(left, right pricing.Money) pricing.Money {
	if left > right {
		return left
	}
	return right
}

func breadcrumbZoomLabel(state State) string {
	switch state.Zoom {
	case ZoomMonth:
		return fmt.Sprintf("%04d", state.Year)
	case ZoomDay:
		if state.Month != "" {
			return state.Month
		}
		return "month"
	default:
		return "all years"
	}
}

func renderExpandedDayMasterDetail(render theme.Context, data Data, state State, width, height int) string {
	sideWidth := min(57, max(36, width/3))
	leftWidth := max(1, width-sideWidth-2)
	left := renderExpandedDayList(render, data, state, leftWidth, height)
	preview := ""
	if data.SessionDay != state.ExpandedDay || data.SessionPageCursor != state.SessionPageCursor {
		preview = strings.Join([]string{previewRule(render, "SESSION", sideWidth), render.Palette.Subtle().Render("Loading indexed sessions…")}, "\n")
	} else if len(data.Sessions) == 0 {
		preview = RenderSessionPreview(render, SessionPreview{Warning: data.SessionWarning}, sideWidth, height)
	} else {
		selected := min(max(0, state.SessionCursor), len(data.Sessions)-1)
		if state.SessionSelectLast {
			selected = len(data.Sessions) - 1
		}
		sessionPreview := SessionPreviewFromLedger(data.Sessions[selected])
		sessionPreview.Location = data.Location
		preview = RenderSessionPreview(render, sessionPreview, sideWidth, height)
	}
	return joinLedgerColumns(left, preview, leftWidth, sideWidth, height)
}

func rowByKey(rows []Row, key string) (Row, bool) {
	for _, row := range rows {
		if row.Key == key {
			return row, true
		}
	}
	return Row{}, false
}

func expandedSessionHeader(render theme.Context, width int) string {
	if width < 72 {
		return fitLine(render.Palette.Header().Render("  TIME  PROVIDER  TOKENS      COST  PROJECT / FIRST PROMPT"), width)
	}
	timeWidth, providerWidth, projectWidth, tokensWidth, costWidth := expandedSessionColumns(width)
	return strings.Join([]string{
		headerCell(render, "TIME", timeWidth+2, false),
		headerCell(render, "PROVIDER", providerWidth, false),
		headerCell(render, "PROJECT", projectWidth, false),
		headerCell(render, "TOKENS", tokensWidth, true),
		headerCell(render, "COST", costWidth, true),
		render.Palette.Header().Render("FIRST PROMPT"),
	}, "  ")
}

func expandedSessionRow(render theme.Context, session LedgerSession, selected bool, width int, location *time.Location) []string {
	marker := "  "
	if selected {
		marker = render.Palette.Emphasis().Bold(true).Render("› ")
	}
	clock := sessionClock(session.ActivityTimestamp, session.FirstTimestamp, session.LastTimestamp, location)
	provider := string(session.Provider)
	project := cleanInline(session.Project)
	preview := cleanInline(session.Preview)
	cost := formatMoney(session.Cost, session.PricedTokens, false, session.UnpricedTokens > 0 || session.AttributionStatus == "incomplete")
	if width < 72 {
		primary := marker + padRight(clock, 5) + "  " + padRight(provider, 8) + "  " + padLeft(commaInteger(session.Tokens), 9) + "  " + padLeft(cost, 9)
		secondary := "    " + project + "  ·  " + preview
		return []string{fitLine(primary, width), fitLine(truncate(secondary, width), width)}
	}
	timeWidth, providerWidth, projectWidth, tokensWidth, costWidth := expandedSessionColumns(width)
	promptWidth := max(1, width-(timeWidth+2)-providerWidth-projectWidth-tokensWidth-costWidth-10)
	line := marker + padRight(clock, timeWidth) + "  " +
		render.Palette.Provider(provider, 0).Render(padRight(provider, providerWidth)) + "  " +
		padRight(project, projectWidth) + "  " + padLeft(commaInteger(session.Tokens), tokensWidth) + "  " +
		padLeft(cost, costWidth) + "  " + truncate(preview, promptWidth)
	return []string{fitLine(line, width)}
}

func expandedSessionColumns(width int) (int, int, int, int, int) {
	return 5, 8, min(22, max(12, width/5)), 10, 9
}

func sessionClock(activity string, primary, fallback *string, location *time.Location) string {
	if activity != "" {
		primary = &activity
	}
	value := primary
	if value == nil || *value == "" {
		value = fallback
	}
	if value == nil || *value == "" {
		return "??:??"
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return "??:??"
	}
	if location != nil {
		parsed = parsed.In(location)
	}
	return parsed.Format("15:04")
}

func stateAnchor(state State) string {
	switch state.Zoom {
	case ZoomMonth:
		if state.Month != "" {
			return state.Month
		}
	case ZoomDay:
		return ""
	default:
		if state.Year > 0 {
			return strconv.Itoa(state.Year)
		}
	}
	return ""
}

func rowDelta(rows []Row, index int) (pricing.Money, int64, bool, bool) {
	if index < 0 || index+1 >= len(rows) {
		return 0, 0, false, false
	}
	current, previous := rows[index].Total(), rows[index+1].Total()
	if current.PricedTokens == 0 || previous.PricedTokens == 0 {
		return 0, 0, false, false
	}
	partial := current.UnpricedTokens > 0 || previous.UnpricedTokens > 0
	return current.Cost - previous.Cost, current.PricedTokens, true, partial
}

func visibleWindow(length, selected, capacity int) (int, int) {
	if capacity <= 0 {
		capacity = 1
	}
	if length <= capacity {
		return 0, length
	}
	selected = min(max(0, selected), length-1)
	start := min(max(0, selected-capacity+1), length-capacity)
	return start, start + capacity
}

func maxRowTokens(rows []Row) int64 {
	var maximum int64
	for _, row := range rows {
		if tokens := row.Total().Tokens; tokens > maximum {
			maximum = tokens
		}
	}
	return maximum
}

func breadcrumb(state State) string {
	switch state.Zoom {
	case ZoomMonth:
		if state.Year > 0 {
			return fmt.Sprintf("ALL YEARS  /  %04d", state.Year)
		}
		return "ALL YEARS  /  YEAR"
	case ZoomDay:
		if state.Month != "" {
			month := state.Month[:min(len(state.Month), 7)]
			if len(month) >= 4 {
				return "ALL YEARS  /  " + month[:4] + "  /  " + month
			}
			return "ALL YEARS  /  " + month
		}
		return "ALL YEARS  /  YEAR  /  MONTH"
	default:
		return "ALL YEARS"
	}
}

func wideColumns(width int) (int, int, int, int) {
	periodWidth, moneyWidth, deltaWidth := 10, 9, 9
	const separators = 10
	barWidth := width - periodWidth - moneyWidth*3 - deltaWidth - separators
	if barWidth < 8 {
		periodWidth, moneyWidth, deltaWidth = 8, 8, 8
		barWidth = width - periodWidth - moneyWidth*3 - deltaWidth - separators
	}
	return periodWidth, moneyWidth, deltaWidth, max(4, min(18, barWidth))
}

func wideHeader(render theme.Context, periodWidth, moneyWidth, deltaWidth, barWidth int) string {
	return strings.Join([]string{
		headerCell(render, "PERIOD", periodWidth, false),
		headerCell(render, "CODEX", moneyWidth, true),
		headerCell(render, "CLAUDE", moneyWidth, true),
		headerCell(render, "TOTAL", moneyWidth, true),
		headerCell(render, "DELTA", deltaWidth, true),
		headerCell(render, "ACTIVITY", barWidth, false),
	}, "  ")
}

func wideRow(render theme.Context, row Row, selected bool, delta pricing.Money, deltaPriced int64, deltaPartial bool, periodWidth, moneyWidth, deltaWidth, barWidth int, maxTokens int64) string {
	labelWidth := max(1, periodWidth-2)
	labelText := truncate(row.Label, labelWidth)
	if selected {
		labelText = "› " + labelText
		labelText = render.Palette.Emphasis().Bold(true).Render(labelText) + strings.Repeat(" ", max(0, periodWidth-lipgloss.Width(labelText)))
	} else {
		labelText = "  " + labelText
		labelText += strings.Repeat(" ", max(0, periodWidth-lipgloss.Width(labelText)))
	}
	return strings.Join([]string{
		labelText,
		moneyCell(render, row.Codex, moneyWidth),
		moneyCell(render, row.Claude, moneyWidth),
		moneyCell(render, row.Total(), moneyWidth),
		deltaCell(render, delta, deltaPriced, deltaPartial, deltaWidth),
		activityBar(render, row, barWidth, maxTokens, false),
	}, "  ")
}

func wideTotal(render theme.Context, row Row, periodWidth, moneyWidth, deltaWidth, barWidth int, maxTokens int64) string {
	return strings.Join([]string{
		headerCell(render, "TOTAL", periodWidth, false),
		moneyCell(render, row.Codex, moneyWidth),
		moneyCell(render, row.Claude, moneyWidth),
		moneyCell(render, row.Total(), moneyWidth),
		strings.Repeat(" ", deltaWidth),
		activityBar(render, row, barWidth, maxTokens, true),
	}, "  ")
}

func compactHeader(render theme.Context, width int) string {
	labelWidth := max(6, width-22)
	return fitLine(strings.Join([]string{
		headerCell(render, "PERIOD", labelWidth+2, false),
		headerCell(render, "TOTAL", 8, true),
		headerCell(render, "DELTA", 8, true),
	}, "  "), width)
}

func compactRow(render theme.Context, row Row, selected bool, delta pricing.Money, deltaPriced int64, deltaPartial bool, width int, maxTokens int64) []string {
	labelWidth := max(6, width-22)
	marker := "  "
	label := truncate(row.Label, labelWidth)
	if selected {
		marker = render.Palette.Emphasis().Bold(true).Render("› ")
	}
	labelCell := aligned(marker+fitText(label, labelWidth), labelWidth+2, false)
	primary := labelCell + "  " + moneyCell(render, row.Total(), 8) + "  " + deltaCell(render, delta, deltaPriced, deltaPartial, 8)
	barWidth := max(4, width-28)
	secondary := "  C " + moneyCell(render, row.Codex, 8) + "  L " + moneyCell(render, row.Claude, 8) + "  " + activityBar(render, row, barWidth, maxTokens, false)
	return []string{fitLine(primary, width), fitLine(secondary, width)}
}

func compactTotal(render theme.Context, row Row, width int, maxTokens int64) []string {
	labelWidth := max(6, width-22)
	primary := headerCell(render, "TOTAL", labelWidth+2, false) + "  " + moneyCell(render, row.Total(), 8) + "  " + strings.Repeat(" ", 8)
	secondary := "  C " + moneyCell(render, row.Codex, 8) + "  L " + moneyCell(render, row.Claude, 8) + "  " + activityBar(render, row, max(4, width-28), maxTokens, true)
	return []string{fitLine(primary, width), fitLine(secondary, width)}
}

func headerCell(render theme.Context, value string, width int, right bool) string {
	return aligned(render.Palette.Header().Render(value), width, right)
}

func moneyCell(render theme.Context, value ProviderTotals, width int) string {
	partial := value.PricedTokens > 0 && value.UnpricedTokens > 0
	style := render.Palette.Money()
	if partial {
		style = render.Palette.Warning()
	}
	return aligned(style.Render(fitMoney(formatMoney(value.Cost, value.PricedTokens, false, partial), width)), width, true)
}

func deltaCell(render theme.Context, value pricing.Money, pricedTokens int64, partial bool, width int) string {
	text := fitMoney(formatMoney(value, pricedTokens, true, partial), width)
	style := render.Palette.Subtle()
	if pricedTokens > 0 {
		style = render.Palette.Success()
		if partial {
			style = render.Palette.Warning()
		}
		if value < 0 {
			style = render.Palette.Warning()
		}
	}
	return aligned(style.Render(text), width, true)
}

func activityBar(render theme.Context, row Row, width int, maxTokens int64, fullWidth bool) string {
	if width <= 0 {
		return ""
	}
	codex, claude := row.Codex.Tokens, row.Claude.Tokens
	total := codex + claude
	if total <= 0 {
		return render.Palette.Subtle().Render(strings.Repeat("·", width))
	}
	if !fullWidth && maxTokens > 0 && total < maxTokens {
		width = max(1, int(float64(total)/float64(maxTokens)*float64(width)))
	}
	codexWidth, claudeWidth := 0, 0
	switch {
	case codex == 0:
		claudeWidth = width
	case claude == 0:
		codexWidth = width
	default:
		codexWidth = int(float64(codex) / float64(total) * float64(width))
		codexWidth = min(width-1, max(1, codexWidth))
		claudeWidth = width - codexWidth
	}
	return render.Palette.Provider("codex", 0).Render(strings.Repeat("█", codexWidth)) +
		render.Palette.Provider("claude", 0).Render(strings.Repeat("▓", claudeWidth))
}

func aligned(value string, width int, right bool) string {
	value = ansi.Truncate(value, width, "")
	padding := max(0, width-lipgloss.Width(value))
	if right {
		return strings.Repeat(" ", padding) + value
	}
	return value + strings.Repeat(" ", padding)
}

func fitLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	return aligned(value, width, false)
}

func fitText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "")
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	return ansi.Truncate(value, width, "…")
}

func fitMoney(value string, width int) string {
	if width <= 0 || lipgloss.Width(value) <= width || value == "—" {
		return value
	}
	sign := ""
	if strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		sign, value = value[:1], value[1:]
	}
	partialPrefix := ""
	if strings.HasPrefix(value, "~") {
		partialPrefix, value = "~", value[1:]
	}
	if !strings.HasPrefix(value, "$") {
		return ansi.Truncate(sign+partialPrefix+value, width, "…")
	}
	value = strings.TrimPrefix(value, "$")
	value = strings.ReplaceAll(value, ",", "")
	whole, _, _ := strings.Cut(value, ".")
	amount, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return ansi.Truncate(sign+"$"+value, width, "…")
	}
	for _, suffix := range []struct {
		threshold int64
		text      string
	}{
		{threshold: 1_000_000_000, text: "B"},
		{threshold: 1_000_000, text: "M"},
		{threshold: 1_000, text: "k"},
	} {
		if amount < suffix.threshold {
			continue
		}
		abbreviated := amount / suffix.threshold
		if amount%suffix.threshold >= (suffix.threshold+1)/2 {
			abbreviated++
		}
		candidate := fmt.Sprintf("%s%s$%d%s", sign, partialPrefix, abbreviated, suffix.text)
		if lipgloss.Width(candidate) <= width {
			return candidate
		}
	}
	return ansi.Truncate(sign+partialPrefix+"$"+whole, width, "…")
}

func formatMoney(value pricing.Money, pricedTokens int64, signed, partial bool) string {
	if pricedTokens == 0 {
		return "—"
	}
	cents := value.RoundedCents()
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	} else if signed && cents > 0 {
		sign = "+"
	}
	partialPrefix := ""
	if partial {
		partialPrefix = "~"
	}
	return fmt.Sprintf("%s%s$%s.%02d", sign, partialPrefix, commaInteger(cents/100), cents%100)
}

func commaInteger(value int64) string {
	text := strconv.FormatInt(value, 10)
	for index := len(text) - 3; index > 0; index -= 3 {
		text = text[:index] + "," + text[index:]
	}
	return text
}
