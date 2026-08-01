package pages

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

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
	Zoom   Zoom
	Year   int
	Month  string
	Cursor int
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
	Available bool
	Zoom      Zoom
	Year      int
	Month     string
	Rows      []Row
	Total     Row
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
		default:
			return state, false
		}
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
	width := max(1, render.Width)
	if height <= 0 {
		height = 24
	}
	selected := SelectedIndex(data, state)
	lines := []string{
		fitLine(render.Palette.Subtle().Render(breadcrumb(state)), width),
	}
	if len(data.Rows) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No usage found for this period."))
		return strings.Join(lines, "\n")
	}

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
			lines = append(lines, compactRow(render, data.Rows[index], index == selected, delta, pricedTokens, partial, width)...)
		}
		lines = append(lines, compactTotal(render, data.Total, width)...)
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
			lines = append(lines, wideRow(render, data.Rows[index], index == selected, delta, pricedTokens, partial, periodWidth, moneyWidth, deltaWidth, barWidth))
		}
		lines = append(lines, wideTotal(render, data.Total, periodWidth, moneyWidth, deltaWidth, barWidth))
	}
	return strings.Join(lines, "\n")
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

func wideRow(render theme.Context, row Row, selected bool, delta pricing.Money, deltaPriced int64, deltaPartial bool, periodWidth, moneyWidth, deltaWidth, barWidth int) string {
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
		activityBar(render, row, barWidth),
	}, "  ")
}

func wideTotal(render theme.Context, row Row, periodWidth, moneyWidth, deltaWidth, barWidth int) string {
	return strings.Join([]string{
		headerCell(render, "TOTAL", periodWidth, false),
		moneyCell(render, row.Codex, moneyWidth),
		moneyCell(render, row.Claude, moneyWidth),
		moneyCell(render, row.Total(), moneyWidth),
		strings.Repeat(" ", deltaWidth),
		activityBar(render, row, barWidth),
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

func compactRow(render theme.Context, row Row, selected bool, delta pricing.Money, deltaPriced int64, deltaPartial bool, width int) []string {
	labelWidth := max(6, width-22)
	marker := "  "
	label := truncate(row.Label, labelWidth)
	if selected {
		marker = render.Palette.Emphasis().Bold(true).Render("› ")
	}
	labelCell := aligned(marker+fitText(label, labelWidth), labelWidth+2, false)
	primary := labelCell + "  " + moneyCell(render, row.Total(), 8) + "  " + deltaCell(render, delta, deltaPriced, deltaPartial, 8)
	barWidth := max(4, width-28)
	secondary := "  C " + moneyCell(render, row.Codex, 8) + "  L " + moneyCell(render, row.Claude, 8) + "  " + activityBar(render, row, barWidth)
	return []string{fitLine(primary, width), fitLine(secondary, width)}
}

func compactTotal(render theme.Context, row Row, width int) []string {
	labelWidth := max(6, width-22)
	primary := headerCell(render, "TOTAL", labelWidth+2, false) + "  " + moneyCell(render, row.Total(), 8) + "  " + strings.Repeat(" ", 8)
	secondary := "  C " + moneyCell(render, row.Codex, 8) + "  L " + moneyCell(render, row.Claude, 8) + "  " + activityBar(render, row, max(4, width-28))
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

func activityBar(render theme.Context, row Row, width int) string {
	if width <= 0 {
		return ""
	}
	codex, claude := row.Codex.Tokens, row.Claude.Tokens
	total := codex + claude
	if total <= 0 {
		return render.Palette.Subtle().Render(strings.Repeat("·", width))
	}
	codexWidth, claudeWidth := 0, 0
	switch {
	case codex == 0:
		claudeWidth = width
	case claude == 0:
		codexWidth = width
	default:
		codexWidth = int(codex * int64(width) / total)
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

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
