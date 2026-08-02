package pages

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

const heatmapDateLayout = "2006-01-02"

// HeatmapWindow identifies the inclusive calendar range shown by the page.
type HeatmapWindow struct {
	From time.Time
	To   time.Time
}

// HeatmapDay is one calendar cell. The dashboard loader supplies one entry
// per date, including zero-usage dates, so the grid keeps a stable shape.
type HeatmapDay struct {
	Date         time.Time
	Cost         pricing.Money
	TotalTokens  int64
	PricedTokens int64
	Level        int
}

// HeatmapData is the view-only snapshot for the Heatmap page. Month totals,
// weekday profiles, and streaks are deliberately derived here from these
// daily rows rather than introducing another loader or store contract.
type HeatmapData struct {
	Window     HeatmapWindow
	Days       []HeatmapDay
	UsesTokens bool
}

// HeatmapPageData is retained as a descriptive alias for callers that name
// snapshots after the page rather than the underlying visualization.
type HeatmapPageData = HeatmapData

type heatmapAggregate struct {
	Cost         pricing.Money
	Tokens       int64
	PricedTokens int64
	ActiveDays   int
}

type heatmapStats struct {
	TotalCost   pricing.Money
	TotalTokens int64
	ActiveDays  int
	Busiest     HeatmapDay
	HasBusiest  bool
	Longest     int
	Current     int
	TotalDays   int
}

type heatmapMonth struct {
	Date time.Time
	heatmapAggregate
}

type heatmapWeekday struct {
	Name string
	heatmapAggregate
}

type heatmapStreak struct {
	Start time.Time
	End   time.Time
	Days  int
}

// RenderHeatmap renders the heatmap page into an exact width and height. The
// render context keeps the terminal width so this package can distinguish the
// shell's floor, standard, and wide+tall compositions without importing the
// parent tui package and creating an import cycle.
func RenderHeatmap(render theme.Context, data HeatmapData, width, height int) string {
	width, height = max(1, width), max(1, height)
	window, days := materializeHeatmapDays(data)
	stats := calculateHeatmapStats(days, data.UsesTokens)
	rawWidth := render.Width
	if rawWidth <= 0 {
		rawWidth = width
	}
	if rawWidth < 100 || (render.Width == 0 && width < 80) {
		return renderHeatmapFloor(render, data, window, days, stats, width, height)
	}

	wideTall := rawWidth >= 160 && height >= 43
	cellWidth := 2
	if wideTall {
		cellWidth = 3
	}
	b1Height := min(12, height)
	if height >= 14 {
		b1Height = 12
	}
	b2Height := height - b1Height

	grid := renderHeatmapGrid(render, data, window, days, stats, width, cellWidth, true)
	b1 := renderHeatmapBand(render, width, b1Height,
		fmt.Sprintf("YEAR GRID · %s", heatmapWindowLabel(window)),
		heatmapPane{Content: grid})
	if b2Height <= 0 {
		return b1
	}

	panes := []heatmapPane{
		{Title: "WEEKDAY PROFILE", Content: renderWeekdayProfile(data, days, max(1, b2Height-1))},
		{Title: "STREAKS & RECORDS", Content: renderStreaksAndRecords(data, days, stats, max(1, b2Height-1), !wideTall)},
	}
	if wideTall {
		panes = append(panes, heatmapPane{Title: "MONTH TABLE", Content: renderMonthTable(data, days, max(1, b2Height-1))})
	}
	b2 := renderHeatmapBand(render, width, b2Height, "ANALYSIS", panes...)
	return strings.Join([]string{b1, b2}, "\n")
}

func renderHeatmapFloor(render theme.Context, data HeatmapData, window HeatmapWindow, days []HeatmapDay, stats heatmapStats, width, height int) string {
	grid := renderHeatmapGrid(render, data, window, days, stats, width, 1, false)
	return renderHeatmapBand(render, width, height, "", heatmapPane{Content: grid})
}

func materializeHeatmapDays(data HeatmapData) (HeatmapWindow, []HeatmapDay) {
	source := append([]HeatmapDay(nil), data.Days...)
	sort.SliceStable(source, func(i, j int) bool { return source[i].Date.Before(source[j].Date) })
	window := data.Window
	if window.From.IsZero() && len(source) > 0 {
		window.From = dateOnly(source[0].Date)
	}
	if window.To.IsZero() && len(source) > 0 {
		window.To = dateOnly(source[len(source)-1].Date)
	}
	if window.From.IsZero() || window.To.IsZero() || window.To.Before(window.From) {
		today := dateOnly(time.Now())
		window = HeatmapWindow{From: today.AddDate(-1, 0, 1), To: today}
	}
	window.From, window.To = dateOnly(window.From), dateOnly(window.To)

	byDate := make(map[string]HeatmapDay, len(source))
	for _, day := range source {
		day.Date = dateOnly(day.Date)
		byDate[day.Date.Format(heatmapDateLayout)] = day
	}
	days := make([]HeatmapDay, 0, daysBetween(window.From, window.To)+1)
	for date := window.From; !date.After(window.To); date = date.AddDate(0, 0, 1) {
		day, ok := byDate[date.Format(heatmapDateLayout)]
		if !ok {
			day = HeatmapDay{Date: date}
		} else {
			day.Date = date
		}
		days = append(days, day)
	}
	return window, days
}

func calculateHeatmapStats(days []HeatmapDay, usesTokens bool) heatmapStats {
	stats := heatmapStats{TotalDays: len(days)}
	streak := 0
	for _, day := range days {
		stats.TotalCost += day.Cost
		stats.TotalTokens += day.TotalTokens
		if !heatmapActive(day) {
			streak = 0
			continue
		}
		stats.ActiveDays++
		streak++
		if streak > stats.Longest {
			stats.Longest = streak
		}
		value := heatmapMetricValue(day, usesTokens)
		if value > 0 && (!stats.HasBusiest || value > heatmapMetricValue(stats.Busiest, usesTokens)) {
			stats.Busiest = day
			stats.HasBusiest = true
		}
	}
	for index := len(days) - 1; index >= 0 && heatmapActive(days[index]); index-- {
		stats.Current++
	}
	return stats
}

func renderHeatmapGrid(render theme.Context, data HeatmapData, window HeatmapWindow, days []HeatmapDay, stats heatmapStats, width, cellWidth int, withSummaryColumn bool) string {
	labelWidth := 3
	summaryWidth := 0
	if withSummaryColumn {
		summaryWidth = heatmapSummaryWidth(data, days, stats)
	}
	weekStartDate := weekStart(window.From)
	weekCount := daysBetween(weekStartDate, weekEnd(window.To))/7 + 1
	gridWidth := max(cellWidth, width-labelWidth-1-summaryWidth-1)
	if cellWidth > 2 && weekCount*cellWidth > gridWidth {
		cellWidth = 2
		gridWidth = max(cellWidth, width-labelWidth-1-summaryWidth-1)
	}
	visibleWeeks := max(1, gridWidth/cellWidth)
	startWeek := max(0, weekCount-visibleWeeks)
	visibleWeeks = min(weekCount, visibleWeeks)
	gridWidth = visibleWeeks * cellWidth
	daysByDate := make(map[string]HeatmapDay, len(days))
	for _, day := range days {
		daysByDate[day.Date.Format(heatmapDateLayout)] = day
	}

	monthHeader := heatmapMonthHeader(window, weekStartDate, startWeek, visibleWeeks, cellWidth)
	header := strings.Repeat(" ", labelWidth) + " " + monthHeader
	if withSummaryColumn {
		header += " " + fitHeatmapLine("MONTH Σ", summaryWidth)
	}
	lines := []string{fitHeatmapLine(header, width)}
	labels := [...]string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}
	summary := heatmapGridSummary(data, days, stats)
	for weekday, label := range labels {
		line := padHeatmapRight(label, labelWidth) + " "
		for week := 0; week < visibleWeeks; week++ {
			date := weekStartDate.AddDate(0, 0, (startWeek+week)*7+weekday)
			if date.Before(window.From) || date.After(window.To) {
				line += strings.Repeat(" ", cellWidth)
				continue
			}
			day := daysByDate[date.Format(heatmapDateLayout)]
			line += heatmapCell(render, day, data.UsesTokens, cellWidth)
		}
		if withSummaryColumn {
			value := ""
			if weekday < len(summary) {
				value = summary[weekday]
			}
			line += " " + fitHeatmapLine(value, summaryWidth)
		}
		lines = append(lines, fitHeatmapLine(line, width))
	}

	legend := render.Palette.Subtle().Render("Less ·░▒▓█ More")
	if data.UsesTokens {
		legend += render.Palette.Subtle().Render("  ·  tokens")
	} else {
		legend += render.Palette.Subtle().Render("  ·  cost")
	}
	lines = append(lines, fitHeatmapLine(legend, width))
	rangeLine := fmt.Sprintf("range %s - %s", window.From.Format("Jan 2006"), window.To.Format("Jan 2006"))
	if startWeek > 0 {
		displayFrom := weekStartDate.AddDate(0, 0, startWeek*7)
		rangeLine = fmt.Sprintf("showing %s - %s of %s - %s", displayFrom.Format("Jan 02 2006"), window.To.Format("Jan 02 2006"), window.From.Format("Jan 02 2006"), window.To.Format("Jan 02 2006"))
	}
	lines = append(lines, render.Palette.Subtle().Render(fitHeatmapLine(rangeLine, width)))
	lines = append(lines, fitHeatmapLine(heatmapSummaryLine(data, stats), width))
	return strings.Join(lines, "\n")
}

func heatmapMonthHeader(window HeatmapWindow, gridStart time.Time, startWeek, weekCount, cellWidth int) string {
	header := []rune(strings.Repeat(" ", weekCount*cellWidth))
	for month := monthStart(window.From); !month.After(window.To); month = month.AddDate(0, 1, 0) {
		week := daysBetween(gridStart, weekStart(month))/7 - startWeek
		position := week * cellWidth
		if position < 0 || position >= len(header) {
			continue
		}
		label := []rune(month.Format("Jan"))
		for index, value := range label {
			if position+index >= len(header) {
				break
			}
			if header[position+index] == ' ' {
				header[position+index] = value
			}
		}
	}
	return string(header)
}

func heatmapGridSummary(data HeatmapData, days []HeatmapDay, stats heatmapStats) []string {
	months := calculateHeatmapMonths(days)
	month := heatmapAggregate{Cost: stats.TotalCost, Tokens: stats.TotalTokens, PricedTokens: pricedTokensFor(days)}
	monthActiveDays, monthLabel := stats.ActiveDays, "range"
	if len(months) > 0 {
		month = months[len(months)-1].heatmapAggregate
		monthActiveDays = month.ActiveDays
		monthLabel = months[len(months)-1].Date.Format("Jan 2006")
	}
	value := heatmapAggregateValue(month, data.UsesTokens)
	return []string{
		value,
		fmt.Sprintf("%d active", monthActiveDays),
		monthLabel,
		fmt.Sprintf("%d-day streak", stats.Longest),
		"",
		"",
		"",
	}
}

func heatmapSummaryWidth(data HeatmapData, days []HeatmapDay, stats heatmapStats) int {
	width := lipgloss.Width("6-day streak")
	for _, value := range append([]string{"MONTH Σ"}, heatmapGridSummary(data, days, stats)...) {
		width = max(width, lipgloss.Width(value))
	}
	return max(13, width)
}

func heatmapCell(render theme.Context, day HeatmapDay, usesTokens bool, cellWidth int) string {
	level := day.Level
	if level == 0 && heatmapMetricValue(day, usesTokens) > 0 {
		level = 1
	}
	level = min(4, max(0, level))
	glyph := []rune{'·', '░', '▒', '▓', '█'}[level]
	if render.Mode == theme.Styled {
		return render.Palette.Heatmap(level).Render(string(glyph)) + strings.Repeat(" ", max(0, cellWidth-1))
	}
	return string(glyph) + strings.Repeat(" ", max(0, cellWidth-1))
}

func renderWeekdayProfile(data HeatmapData, days []HeatmapDay, height int) string {
	weekdays := make([]heatmapWeekday, 7)
	for index, name := range []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"} {
		weekdays[index].Name = name
	}
	for _, day := range days {
		index := int(day.Date.Weekday())
		weekdays[index].Tokens += day.TotalTokens
		weekdays[index].Cost += day.Cost
		weekdays[index].PricedTokens += day.PricedTokens
		if heatmapActive(day) {
			weekdays[index].ActiveDays++
		}
	}
	var totalMetric int64
	for _, day := range days {
		totalMetric += heatmapMetricValue(day, data.UsesTokens)
	}
	lines := []string{"DAY  ACTIVE  " + heatmapMetricLabel(data.UsesTokens)}
	for _, weekday := range weekdays {
		lines = append(lines, fmt.Sprintf("%-3s %3d  %s", weekday.Name, weekday.ActiveDays, heatmapAggregateValue(weekday.heatmapAggregate, data.UsesTokens)))
	}
	if height >= 24 {
		lines = append(lines, "", "WEEKDAY SHARE", "DAY  SHARE  "+heatmapMetricLabel(data.UsesTokens))
		for _, weekday := range weekdays {
			metric := heatmapAggregateMetricValue(weekday.heatmapAggregate, data.UsesTokens)
			lines = append(lines, fmt.Sprintf("%-3s %6s  %s", weekday.Name, heatmapPercent(metric, totalMetric), heatmapAggregateValue(weekday.heatmapAggregate, data.UsesTokens)))
		}
	}
	lines = append(lines, "", "RECENT DAILY ACTIVITY")
	return heatmapPaneContent(lines, recentHeatmapLines(days, data.UsesTokens, max(0, height)), height,
		[]string{"  no additional indexed activity", "  recent daily rows are listed above"})
}

func renderStreaksAndRecords(data HeatmapData, days []HeatmapDay, stats heatmapStats, height int, includeCompactMonthTable bool) string {
	lines := []string{
		"BUSIEST DAY     " + heatmapBusiestText(stats, data.UsesTokens),
		fmt.Sprintf("LONGEST STREAK  %s", pluralHeatmapCount(stats.Longest, "day", "days")),
		fmt.Sprintf("CURRENT STREAK   %s", pluralHeatmapCount(stats.Current, "day", "days")),
		fmt.Sprintf("ACTIVE / TOTAL   %d / %d days", stats.ActiveDays, stats.TotalDays),
		"METRIC           " + heatmapMetricLabel(data.UsesTokens),
		"",
		"RECENT STREAK HISTORY",
		"START - END          LENGTH",
	}
	streakLines := make([]string, 0)
	for _, streak := range calculateHeatmapStreaks(days) {
		streakLines = append(streakLines, fmt.Sprintf("%s - %-8s  %s", streak.Start.Format("Jan 02"), streak.End.Format("Jan 02"), pluralHeatmapCount(streak.Days, "day", "days")))
	}
	if includeCompactMonthTable {
		contentHeight := max(1, height-1)
		months := calculateHeatmapMonths(days)
		monthRows := min(3, len(months))
		monthBlockHeight := 3 + monthRows
		historyHeight := max(len(lines), contentHeight-monthBlockHeight)
		lines = appendLatestHeatmapLines(lines, streakLines, historyHeight)
		lines = append(lines, "", "MONTH TABLE", fmt.Sprintf("MONTH       %-16s ACTIVE", heatmapMetricLabel(data.UsesTokens)))
		start := max(0, len(months)-monthRows)
		for _, month := range months[start:] {
			lines = append(lines, fmt.Sprintf("%-10s %-16s %3d", month.Date.Format("Jan 2006"), heatmapAggregateValue(month.heatmapAggregate, data.UsesTokens), month.ActiveDays))
		}
		return heatmapPaneContent(lines, nil, height,
			[]string{"  no additional streaks in this window", "  records above cover the selected range"})
	}
	return heatmapPaneContent(lines, streakLines, height,
		[]string{"  no additional streaks in this window", "  records above cover the selected range"})
}

func renderMonthTable(data HeatmapData, days []HeatmapDay, height int) string {
	lines := []string{
		fmt.Sprintf("MONTH       %-16s ACTIVE DAYS", heatmapMetricLabel(data.UsesTokens)),
	}
	months := calculateHeatmapMonths(days)
	for _, month := range months {
		value := heatmapAggregateValue(month.heatmapAggregate, data.UsesTokens)
		lines = append(lines, fmt.Sprintf("%-10s %-16s %3d", month.Date.Format("Jan 2006"), value, month.ActiveDays))
	}
	lines = append(lines, "", "MONTH SHARE", "MONTH       SHARE   "+heatmapMetricLabel(data.UsesTokens))
	var totalMetric int64
	for _, month := range months {
		totalMetric += heatmapAggregateMetricValue(month.heatmapAggregate, data.UsesTokens)
	}
	for _, month := range months {
		metric := heatmapAggregateMetricValue(month.heatmapAggregate, data.UsesTokens)
		lines = append(lines, fmt.Sprintf("%-10s %6s  %s", month.Date.Format("Jan 2006"), heatmapPercent(metric, totalMetric), heatmapAggregateValue(month.heatmapAggregate, data.UsesTokens)))
	}
	lines = append(lines, "", "MONTH PEAK", "MONTH       DAY        "+heatmapMetricLabel(data.UsesTokens))
	for _, month := range months {
		peak, ok := heatmapMonthPeak(days, month.Date, data.UsesTokens)
		if !ok {
			lines = append(lines, fmt.Sprintf("%-10s %-10s  —", month.Date.Format("Jan 2006"), "none"))
			continue
		}
		lines = append(lines, fmt.Sprintf("%-10s %-10s  %s", month.Date.Format("Jan 2006"), peak.Date.Format("Jan 02"), heatmapMetricValueText(peak, data.UsesTokens)))
	}
	if len(months) > 0 {
		lines = append(lines, "", fmt.Sprintf("WINDOW         %d months · %s - %s", len(months), months[0].Date.Format("Jan 2006"), months[len(months)-1].Date.Format("Jan 2006")))
	}
	return heatmapPaneContent(lines, nil, height,
		[]string{"  all indexed months are shown above", "  month share and peaks use the active metric"})
}

func heatmapPaneContent(lines, supplemental []string, height int, fillLines []string) string {
	contentHeight := max(1, height-1)
	lines = appendLatestHeatmapLines(lines, supplemental, contentHeight)
	if len(lines) > contentHeight {
		lines = lines[:contentHeight]
	}
	if len(fillLines) == 0 {
		fillLines = []string{"  no additional indexed activity"}
	}
	fillIndex := 0
	for len(lines) < contentHeight {
		lines = append(lines, fillLines[fillIndex%len(fillLines)])
		fillIndex++
	}
	return strings.Join(lines, "\n")
}

func appendLatestHeatmapLines(lines, candidates []string, height int) []string {
	room := max(0, height-len(lines))
	if room == 0 || len(candidates) == 0 {
		return lines
	}
	if len(candidates) > room {
		candidates = candidates[len(candidates)-room:]
	}
	return append(lines, candidates...)
}

func recentHeatmapLines(days []HeatmapDay, usesTokens bool, limit int) []string {
	if limit <= 0 {
		return nil
	}
	start := max(0, len(days)-limit)
	lines := make([]string, 0, len(days)-start)
	for _, day := range days[start:] {
		value := heatmapMetricValueText(day, usesTokens)
		marker := " "
		if heatmapActive(day) {
			marker = "·"
		}
		lines = append(lines, fmt.Sprintf("%s %s  %s", marker, day.Date.Format("Jan 02"), value))
	}
	return lines
}

func calculateHeatmapMonths(days []HeatmapDay) []heatmapMonth {
	months := make(map[string]*heatmapMonth)
	order := make([]string, 0, 12)
	for _, day := range days {
		key := day.Date.Format("2006-01")
		month, ok := months[key]
		if !ok {
			month = &heatmapMonth{Date: monthStart(day.Date)}
			months[key] = month
			order = append(order, key)
		}
		month.Cost += day.Cost
		month.Tokens += day.TotalTokens
		month.PricedTokens += day.PricedTokens
		if heatmapActive(day) {
			month.ActiveDays++
		}
	}
	result := make([]heatmapMonth, 0, len(order))
	for _, key := range order {
		result = append(result, *months[key])
	}
	return result
}

func calculateHeatmapStreaks(days []HeatmapDay) []heatmapStreak {
	streaks := make([]heatmapStreak, 0)
	var current heatmapStreak
	for _, day := range days {
		if !heatmapActive(day) {
			if current.Days > 0 {
				streaks = append(streaks, current)
				current = heatmapStreak{}
			}
			continue
		}
		if current.Days == 0 || !day.Date.Equal(current.End.AddDate(0, 0, 1)) {
			if current.Days > 0 {
				streaks = append(streaks, current)
			}
			current = heatmapStreak{Start: day.Date, End: day.Date, Days: 1}
			continue
		}
		current.End = day.Date
		current.Days++
	}
	if current.Days > 0 {
		streaks = append(streaks, current)
	}
	return streaks
}

func heatmapMonthPeak(days []HeatmapDay, month time.Time, usesTokens bool) (HeatmapDay, bool) {
	var peak HeatmapDay
	found := false
	for _, day := range days {
		if day.Date.Year() != month.Year() || day.Date.Month() != month.Month() {
			continue
		}
		value := heatmapMetricValue(day, usesTokens)
		if value > 0 && (!found || value > heatmapMetricValue(peak, usesTokens)) {
			peak = day
			found = true
		}
	}
	return peak, found
}

func pricedTokensFor(days []HeatmapDay) int64 {
	var total int64
	for _, day := range days {
		total += day.PricedTokens
	}
	return total
}

func heatmapAggregateValue(value heatmapAggregate, usesTokens bool) string {
	if usesTokens {
		return formatHeatmapNumber(value.Tokens)
	}
	if value.PricedTokens == 0 && value.Cost == 0 {
		return "—"
	}
	return formatHeatmapUSD(value.Cost)
}

func heatmapAggregateMetricValue(value heatmapAggregate, usesTokens bool) int64 {
	if usesTokens {
		return value.Tokens
	}
	return int64(value.Cost)
}

func heatmapPercent(value, total int64) string {
	if total <= 0 {
		return "—"
	}
	return fmt.Sprintf("%5.1f%%", float64(value)*100/float64(total))
}

func heatmapMetricValueText(day HeatmapDay, usesTokens bool) string {
	return heatmapAggregateValue(heatmapAggregate{Cost: day.Cost, Tokens: day.TotalTokens, PricedTokens: day.PricedTokens}, usesTokens)
}

func heatmapMetricValue(day HeatmapDay, usesTokens bool) int64 {
	if usesTokens {
		if day.TotalTokens > 0 {
			return day.TotalTokens
		}
		return 0
	}
	if day.Cost > 0 {
		return int64(day.Cost)
	}
	return 0
}

func heatmapActive(day HeatmapDay) bool {
	return day.TotalTokens > 0 || day.Cost > 0
}

func heatmapMetricLabel(usesTokens bool) string {
	if usesTokens {
		return "TOKENS"
	}
	return "COST"
}

func heatmapBusiestText(stats heatmapStats, usesTokens bool) string {
	if !stats.HasBusiest {
		return "none"
	}
	return stats.Busiest.Date.Format(heatmapDateLayout) + "  " + heatmapMetricValueText(stats.Busiest, usesTokens)
}

func heatmapSummaryLine(data HeatmapData, stats heatmapStats) string {
	total := heatmapAggregateValue(heatmapAggregate{Cost: stats.TotalCost, Tokens: stats.TotalTokens, PricedTokens: pricedTokensFor(data.Days)}, data.UsesTokens)
	busiest := heatmapBusiestText(stats, data.UsesTokens)
	return fmt.Sprintf("%d active days · total %s · busiest %s · longest streak %s", stats.ActiveDays, total, busiest, pluralHeatmapCount(stats.Longest, "day", "days"))
}

func heatmapWindowLabel(window HeatmapWindow) string {
	if window.From.Year() == window.To.Year() {
		return strconv.Itoa(window.From.Year())
	}
	return fmt.Sprintf("%d-%d", window.From.Year(), window.To.Year())
}

func formatHeatmapUSD(value pricing.Money) string {
	cents := value.RoundedCents()
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s$%s.%02d", sign, formatHeatmapNumber(cents/100), cents%100)
}

func formatHeatmapNumber(value int64) string {
	digits := strconv.FormatInt(value, 10)
	start := 0
	if strings.HasPrefix(digits, "-") {
		start = 1
	}
	for index := len(digits) - 3; index > start; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}

func pluralHeatmapCount(value int, singular, plural string) string {
	unit := plural
	if value == 1 {
		unit = singular
	}
	return fmt.Sprintf("%s %s", formatHeatmapNumber(int64(value)), unit)
}

type heatmapPane struct {
	Title   string
	Content string
}

func renderHeatmapBand(render theme.Context, width, height int, title string, panes ...heatmapPane) string {
	width, height = max(1, width), max(1, height)
	if len(panes) == 0 {
		return fitHeatmapBlock(heatmapRuleTitle(render, title, width), width, height)
	}
	contentHeight := height
	lines := make([]string, 0, height)
	if strings.TrimSpace(title) != "" {
		lines = append(lines, heatmapRuleTitle(render, title, width))
		contentHeight--
	}
	contentHeight = max(1, contentHeight)
	gap := 2
	if len(panes) == 1 {
		gap = 0
	}
	available := max(len(panes), width-gap*(len(panes)-1))
	base := available / len(panes)
	remainder := available % len(panes)
	parts := make([]string, 0, len(panes))
	for index, pane := range panes {
		paneWidth := base
		if index == len(panes)-1 {
			paneWidth += remainder
		}
		parts = append(parts, renderHeatmapPane(render, pane, paneWidth, contentHeight))
	}
	lines = append(lines, strings.Split(joinHeatmapPanes(parts, gap, contentHeight), "\n")...)
	return fitHeatmapBlock(strings.Join(lines, "\n"), width, height)
}

func joinHeatmapPanes(panes []string, gap, height int) string {
	if len(panes) == 0 || height <= 0 {
		return ""
	}
	rows := make([]strings.Builder, height)
	for index, pane := range panes {
		if index > 0 {
			for row := range rows {
				rows[row].WriteString(strings.Repeat(" ", gap))
			}
		}
		paneRows := strings.Split(pane, "\n")
		for row := range rows {
			if row < len(paneRows) {
				rows[row].WriteString(paneRows[row])
			}
		}
	}
	joined := make([]string, len(rows))
	for index := range rows {
		joined[index] = rows[index].String()
	}
	return strings.Join(joined, "\n")
}

func renderHeatmapPane(render theme.Context, pane heatmapPane, width, height int) string {
	contentHeight := height
	lines := make([]string, 0, height)
	if strings.TrimSpace(pane.Title) != "" {
		lines = append(lines, heatmapRuleTitle(render, pane.Title, width))
		contentHeight--
	}
	contentHeight = max(1, contentHeight)
	content := fitHeatmapBlock(pane.Content, width, contentHeight)
	lines = append(lines, strings.Split(content, "\n")...)
	return fitHeatmapBlock(strings.Join(lines, "\n"), width, height)
}

func heatmapRuleTitle(render theme.Context, title string, width int) string {
	title = strings.ToUpper(strings.TrimSpace(title))
	if title == "" {
		return strings.Repeat("─", width)
	}
	label := render.Palette.Header().Render(title)
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitHeatmapLine(label, width)
	}
	return fitHeatmapLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func fitHeatmapBlock(value string, width, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index, line := range lines {
		lines[index] = fitHeatmapLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func fitHeatmapLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = lipgloss.NewStyle().Inline(true).MaxWidth(width).Render(value)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func padHeatmapRight(value string, width int) string {
	value = fitHeatmapLine(value, width)
	return value
}

func dateOnly(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func weekStart(value time.Time) time.Time {
	value = dateOnly(value)
	return value.AddDate(0, 0, -int(value.Weekday()))
}

func weekEnd(value time.Time) time.Time {
	return weekStart(value).AddDate(0, 0, 6)
}

func monthStart(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), 1, 0, 0, 0, 0, value.Location())
}

func daysBetween(from, to time.Time) int {
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(to.Sub(from).Hours() / 24)
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}
