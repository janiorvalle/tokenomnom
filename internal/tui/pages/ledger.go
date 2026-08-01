package pages

import (
	"fmt"
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
	Key    string
	Label  string
	Codex  ProviderTotals
	Claude ProviderTotals
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
		Key:    row.Key,
		Label:  row.Label,
		Codex:  row.Codex.Add(other.Codex),
		Claude: row.Claude.Add(other.Claude),
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
		return ledgerMin(state.Cursor, len(data.Rows)-1)
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
		sessionIndex := ledgerMin(ledgerMax(0, state.SessionCursor), len(data.Sessions)-1)
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

// Render draws the ledger with a dense table at normal widths and a two-line
// row layout when the cockpit's content pane is narrow.
func Render(render theme.Context, data Data, state State, height int) string {
	width := ledgerMax(1, render.Width)
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
		return renderExpandedDay(render, data, state, width, height)
	}
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
		capacity := ledgerMax(1, (height-len(lines)-2)/2)
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
		capacity := ledgerMax(1, height-len(lines)-1)
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

func renderExpandedDay(render theme.Context, data Data, state State, width, height int) string {
	lines := []string{fitLine(render.Palette.Subtle().Render(breadcrumb(state)+"  /  "+state.ExpandedDay), width)}
	if data.SessionDay != state.ExpandedDay || data.SessionPageCursor != state.SessionPageCursor {
		lines = append(lines, render.Palette.Subtle().Render(ledgerTruncate("Loading indexed sessions…", width)))
		return strings.Join(lines, "\n")
	}
	if data.SessionWarning != "" {
		lines = append(lines, render.Palette.Warning().Render(ledgerTruncate(data.SessionWarning, width)))
	}
	if data.SessionDataUnavailable {
		return strings.Join(lines, "\n")
	}
	if !data.SessionIndexAvailable {
		lines = append(lines,
			render.Palette.Warning().Render(ledgerTruncate("No history index is available.", width)),
			render.Palette.Subtle().Render(ledgerTruncate("Run tokenomnom history index to inspect this day’s sessions.", width)),
		)
		return strings.Join(lines, "\n")
	}
	day, ok := rowByKey(data.Rows, state.ExpandedDay)
	if ok {
		dayTotal := day.Total()
		lines = append(lines, render.Palette.Emphasis().Bold(true).Render(ledgerTruncate(fmt.Sprintf("%s  %s  %s tokens", day.Label, formatMoney(dayTotal.Cost, dayTotal.PricedTokens, false, dayTotal.UnpricedTokens > 0), commaInteger(dayTotal.Tokens)), width)))
	}
	if len(data.Sessions) == 0 {
		lines = append(lines,
			render.Palette.Warning().Render(ledgerTruncate("No indexed sessions match this day.", width)),
			render.Palette.Subtle().Render(ledgerTruncate("Run tokenomnom history index to refresh the history index.", width)),
		)
		return strings.Join(lines, "\n")
	}
	lines = append(lines, expandedSessionHeader(render, width))
	selected := ledgerMin(ledgerMax(0, state.SessionCursor), len(data.Sessions)-1)
	if state.SessionSelectLast {
		selected = len(data.Sessions) - 1
	}
	selectedWarning := data.Sessions[selected].Warning
	rowHeight := 1
	if width < 72 {
		rowHeight = 2
	}
	reservedLines := 2
	if selectedWarning != "" {
		reservedLines++
	}
	capacity := ledgerMax(1, (height-len(lines)-reservedLines)/rowHeight)
	start, end := visibleWindow(len(data.Sessions), selected, capacity)
	for index := start; index < end; index++ {
		lines = append(lines, expandedSessionRow(render, data.Sessions[index], index == selected, width, data.Location)...)
	}
	if data.SessionsHaveMore {
		lines = append(lines, render.Palette.Subtle().Render(ledgerTruncate("↓ more sessions", width)))
	}
	if selectedWarning != "" {
		lines = append(lines, render.Palette.Warning().Render(ledgerTruncate("~ "+selectedWarning, width)))
	}
	lines = append(lines, render.Palette.Subtle().Render(ledgerTruncate("↑/↓ select  ·  enter open  ·  esc collapse", width)))
	return strings.Join(lines, "\n")
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
		return []string{fitLine(primary, width), fitLine(ledgerTruncate(secondary, width), width)}
	}
	timeWidth, providerWidth, projectWidth, tokensWidth, costWidth := expandedSessionColumns(width)
	promptWidth := ledgerMax(1, width-(timeWidth+2)-providerWidth-projectWidth-tokensWidth-costWidth-10)
	line := marker + padRight(clock, timeWidth) + "  " +
		render.Palette.Provider(provider, 0).Render(padRight(provider, providerWidth)) + "  " +
		padRight(project, projectWidth) + "  " + padLeft(commaInteger(session.Tokens), tokensWidth) + "  " +
		padLeft(cost, costWidth) + "  " + ledgerTruncate(preview, promptWidth)
	return []string{fitLine(line, width)}
}

func expandedSessionColumns(width int) (int, int, int, int, int) {
	return 5, 8, ledgerMin(22, ledgerMax(12, width/5)), 10, 9
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
	selected = ledgerMin(ledgerMax(0, selected), length-1)
	start := ledgerMin(ledgerMax(0, selected-capacity+1), length-capacity)
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
			month := state.Month[:ledgerMin(len(state.Month), 7)]
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
	return periodWidth, moneyWidth, deltaWidth, ledgerMax(4, ledgerMin(18, barWidth))
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
	labelWidth := ledgerMax(1, periodWidth-2)
	labelText := ledgerTruncate(row.Label, labelWidth)
	if selected {
		labelText = "› " + labelText
		labelText = render.Palette.Emphasis().Bold(true).Render(labelText) + strings.Repeat(" ", ledgerMax(0, periodWidth-lipgloss.Width(labelText)))
	} else {
		labelText = "  " + labelText
		labelText += strings.Repeat(" ", ledgerMax(0, periodWidth-lipgloss.Width(labelText)))
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
	labelWidth := ledgerMax(6, width-22)
	return fitLine(strings.Join([]string{
		headerCell(render, "PERIOD", labelWidth+2, false),
		headerCell(render, "TOTAL", 8, true),
		headerCell(render, "DELTA", 8, true),
	}, "  "), width)
}

func compactRow(render theme.Context, row Row, selected bool, delta pricing.Money, deltaPriced int64, deltaPartial bool, width int, maxTokens int64) []string {
	labelWidth := ledgerMax(6, width-22)
	marker := "  "
	label := ledgerTruncate(row.Label, labelWidth)
	if selected {
		marker = render.Palette.Emphasis().Bold(true).Render("› ")
	}
	labelCell := aligned(marker+fitText(label, labelWidth), labelWidth+2, false)
	primary := labelCell + "  " + moneyCell(render, row.Total(), 8) + "  " + deltaCell(render, delta, deltaPriced, deltaPartial, 8)
	barWidth := ledgerMax(4, width-28)
	secondary := "  C " + moneyCell(render, row.Codex, 8) + "  L " + moneyCell(render, row.Claude, 8) + "  " + activityBar(render, row, barWidth, maxTokens, false)
	return []string{fitLine(primary, width), fitLine(secondary, width)}
}

func compactTotal(render theme.Context, row Row, width int, maxTokens int64) []string {
	labelWidth := ledgerMax(6, width-22)
	primary := headerCell(render, "TOTAL", labelWidth+2, false) + "  " + moneyCell(render, row.Total(), 8) + "  " + strings.Repeat(" ", 8)
	secondary := "  C " + moneyCell(render, row.Codex, 8) + "  L " + moneyCell(render, row.Claude, 8) + "  " + activityBar(render, row, ledgerMax(4, width-28), maxTokens, true)
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
		width = ledgerMax(1, int(float64(total)/float64(maxTokens)*float64(width)))
	}
	codexWidth, claudeWidth := 0, 0
	switch {
	case codex == 0:
		claudeWidth = width
	case claude == 0:
		codexWidth = width
	default:
		codexWidth = int(float64(codex) / float64(total) * float64(width))
		codexWidth = ledgerMin(width-1, ledgerMax(1, codexWidth))
		claudeWidth = width - codexWidth
	}
	return render.Palette.Provider("codex", 0).Render(strings.Repeat("█", codexWidth)) +
		render.Palette.Provider("claude", 0).Render(strings.Repeat("▓", claudeWidth))
}

func aligned(value string, width int, right bool) string {
	value = ansi.Truncate(value, width, "")
	padding := ledgerMax(0, width-lipgloss.Width(value))
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

func ledgerTruncate(value string, width int) string {
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

func ledgerMin(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func ledgerMax(left, right int) int {
	if left > right {
		return left
	}
	return right
}
