package pages

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// DailyValue keeps the two units that a Daily page can show together. A zero
// PricedTokens value means Cost is not a meaningful display value, rather than
// that the day was free.
type DailyValue struct {
	Cost           pricing.Money
	Tokens         int64
	PricedTokens   int64
	UnpricedTokens int64
	Sessions       int
}

// DailyPoint is one date in the chart and trend series.
type DailyPoint struct {
	Date     string
	Total    DailyValue
	Codex    DailyValue
	Claude   DailyValue
	Selected bool
}

// DailyProvider is one provider's contribution to the selected day.
type DailyProvider struct {
	Provider string
	Value    DailyValue
}

// DailyModel is one model's contribution to the selected day.
type DailyModel struct {
	Provider string
	Model    string
	Value    DailyValue
}

// DailyDetail contains the selected day's analysis rows.
type DailyDetail struct {
	Date      string
	Value     DailyValue
	Providers []DailyProvider
	Models    []DailyModel
}

// DailySession is the display-safe projection of a history cost row. Raw
// transcript locations never cross into the page layer.
type DailySession struct {
	Time              string
	Provider          string
	Project           string
	SessionID         string
	Model             string
	Tokens            int64
	Cost              pricing.Money
	PricedTokens      int64
	Prompt            string
	PromptCount       int
	AttributionStatus string
}

// DailySessionData is a bounded cursor-day session query result.
type DailySessionData struct {
	Rows           []DailySession
	Total          int
	HasMore        bool
	TotalKnown     bool
	SessionCounts  map[string]int
	ProviderCounts map[string]int
	SessionTimes   map[string]string
	Warning        string
}

// DailyPageData is the pure input for RenderDaily. Rows is the explicit chart
// series; TrendRows retains the unrolled daily aggregates for sparklines.
type DailyPageData struct {
	Rows             []DailyPoint
	TrendRows        []DailyPoint
	SelectedDate     string
	Detail           DailyDetail
	Sessions         DailySessionData
	Average          DailyValue
	AverageSessions  float64
	Peak             DailyValue
	PeakDate         string
	RangeStart       string
	RangeEnd         string
	UsesTokens       bool
	DetailUsesTokens bool
	ChartNotice      string
}

type dailyTier uint8

const (
	dailyFloor dailyTier = iota
	dailyStandard
	dailyWideTall
)

const (
	dailyWideWidth      = 160
	dailyStandardWidth  = 100
	dailyTallHeight     = 50
	dailyChartPoints    = 30
	dailyChartMinHeight = 12
	dailyChartMaxHeight = 18
	dailyAnalysisHeight = 17
	dailyGap            = 2
)

// RenderDaily renders the Daily page into the exact body rectangle supplied by
// the cockpit. The terminal dimensions select a density tier while the page
// width remains the content width after shell and rail arithmetic.
func RenderDaily(render theme.Context, data DailyPageData, terminalWidth, terminalHeight, bodyHeight, detailOffset int) string {
	width := max(1, render.Width)
	bodyHeight = max(1, bodyHeight)
	for index := range data.Rows {
		data.Rows[index].Selected = data.Rows[index].Selected || data.Rows[index].Date == data.SelectedDate
	}
	if len(data.Rows) > dailyChartPoints {
		data.ChartNotice = fmt.Sprintf("%d-day rollup", len(data.Rows))
		data.Rows = compressDailyPoints(data.Rows, dailyChartPoints)
	}
	tier := dailyTierFor(terminalWidth, terminalHeight)
	var view string
	switch tier {
	case dailyWideTall:
		view = renderDailyWideTall(render, data, width, bodyHeight, detailOffset)
	case dailyStandard:
		view = renderDailyStandard(render, data, width, bodyHeight, detailOffset)
	default:
		view = renderDailyFloor(render, data, width, bodyHeight, detailOffset)
	}
	return fitDailyBlock(view, width, bodyHeight)
}

// DailyDetailMaxOffset returns the scroll range used by Daily's existing up /
// down detail bindings. The wide tier allocates the complete analysis band;
// shorter tiers keep the old bounded scrolling behavior.
func DailyDetailMaxOffset(data DailyPageData, contentWidth, terminalWidth, terminalHeight, bodyHeight int) int {
	if dailyTierFor(terminalWidth, terminalHeight) == dailyWideTall {
		return 0
	}
	_, dayHeight := dailyCompactHeights(terminalWidth, terminalHeight, bodyHeight)
	visible := max(1, dayHeight-1)
	return max(0, len(dailyDetailLines(data, contentWidth, false))-visible)
}

func dailyTierFor(width, height int) dailyTier {
	if width >= dailyWideWidth && height >= dailyTallHeight {
		return dailyWideTall
	}
	if width >= dailyStandardWidth {
		return dailyStandard
	}
	return dailyFloor
}

func renderDailyWideTall(render theme.Context, data DailyPageData, width, bodyHeight, detailOffset int) string {
	chartHeight := clampDaily(int(float64(bodyHeight)*0.30), dailyChartMinHeight+2, dailyChartMaxHeight)
	analysisHeight := min(dailyAnalysisHeight, max(1, bodyHeight-chartHeight-2))
	sessionsHeight := max(1, bodyHeight-chartHeight-analysisHeight-2)
	parts := []string{
		renderDailyChart(render, data, width, chartHeight),
		dailyRule(render, width),
		renderDailyWideAnalysis(render, data, width, analysisHeight, detailOffset),
		dailyRule(render, width),
		renderDailySessions(render, data, width, sessionsHeight, true),
	}
	return strings.Join(parts, "\n")
}

func renderDailyStandard(render theme.Context, data DailyPageData, width, bodyHeight, detailOffset int) string {
	chartHeight := min(14, max(1, bodyHeight-7))
	sessionsHeight := min(7, max(4, bodyHeight-chartHeight-1-8))
	analysisHeight := max(1, bodyHeight-chartHeight-sessionsHeight-1)
	parts := []string{
		renderDailyChart(render, data, width, chartHeight),
		dailyRule(render, width),
		renderDailyStandardAnalysis(render, data, width, analysisHeight, detailOffset),
		renderDailySessions(render, data, width, sessionsHeight, false),
	}
	return strings.Join(parts, "\n")
}

func renderDailyFloor(render theme.Context, data DailyPageData, width, bodyHeight, detailOffset int) string {
	chartHeight, dayHeight := dailyCompactHeightsForBody(bodyHeight)
	return strings.Join([]string{
		renderDailyChart(render, data, width, chartHeight),
		dailyRule(render, width),
		renderDailyFloorDetail(render, data, width, dayHeight, detailOffset),
	}, "\n")
}

func dailyCompactHeights(terminalWidth, terminalHeight, bodyHeight int) (int, int) {
	if dailyTierFor(terminalWidth, terminalHeight) == dailyStandard {
		chartHeight := min(14, max(1, bodyHeight-7))
		sessionsHeight := min(7, max(4, bodyHeight-chartHeight-1-8))
		return chartHeight, max(1, bodyHeight-chartHeight-sessionsHeight-1)
	}
	return dailyCompactHeightsForBody(bodyHeight)
}

func dailyCompactHeightsForBody(bodyHeight int) (int, int) {
	chartHeight := min(dailyChartMinHeight, max(1, bodyHeight-6))
	return chartHeight, max(1, bodyHeight-chartHeight-1)
}

func renderDailyChart(render theme.Context, data DailyPageData, width, height int) string {
	height = max(1, height)
	if len(data.Rows) == 0 {
		return fitDailyBlock("COST / DAY · "+dailyRangeCountLabel(data)+"\nNo active days in this range.", width, height)
	}

	maxValue := 0.0
	for _, point := range data.Rows {
		maxValue = maxFloat(maxValue, dailyPointAmount(point, data.UsesTokens))
	}
	if maxValue <= 0 {
		maxValue = 1
	}
	average := dailyValueAmount(data.Average, data.UsesTokens)
	peak := formatDailyValue(data.Peak, data.UsesTokens)
	if peak == "—" && data.UsesTokens {
		peak = formatDailyNumber(data.Peak.Tokens)
	}
	unit := "cost/day"
	if data.UsesTokens {
		unit = "tokens/day (unpriced)"
	}
	span := dailyRangeLabel(data.RangeStart, data.RangeEnd)
	if span == "" {
		span = dailyRangeLabel(data.Rows[0].Date, data.Rows[len(data.Rows)-1].Date)
	}
	title := fmt.Sprintf("COST / DAY · %d DAYS · %s", len(data.TrendRows), span)
	if data.UsesTokens {
		title = fmt.Sprintf("TOKENS / DAY · %d DAYS · %s", len(data.TrendRows), span)
	}
	if data.ChartNotice != "" {
		title += " · " + data.ChartNotice
	}
	if data.PeakDate != "" {
		title += " · peak " + peak + " " + shortDailyDate(data.PeakDate)
	}
	for _, point := range data.Rows {
		if point.Selected {
			title += " · cursor ^"
			break
		}
	}
	// Keep the legacy machine-readable unit in the title while the visible
	// heading follows the reference frame's spaced typography.
	title += " · " + unit

	plotHeight := max(1, height-2)
	axisWidth := max(5, lipgloss.Width(formatDailyAxis(maxValue, data.UsesTokens))+1)
	plotWidth := max(1, width-axisWidth)
	pointNotice := ""
	pointLimit := max(1, (plotWidth+1)/2)
	if len(data.Rows) > pointLimit {
		pointNotice = fmt.Sprintf("%d-point view", pointLimit)
		data.Rows = compressDailyPoints(data.Rows, pointLimit)
	}
	if pointNotice != "" {
		title += " · " + pointNotice
	}
	columnWidth := dailyChartColumnWidth(plotWidth, len(data.Rows))
	chartWidth := dailyFullRangeChartWidth(len(data.Rows), columnWidth)
	plotWidth = min(plotWidth, max(1, chartWidth))
	avgRow := -1
	if average > 0 {
		avgRow = int(math.Round((maxValue - average) / maxValue * float64(plotHeight-1)))
		avgRow = min(max(0, avgRow), plotHeight-1)
	}
	legend := render.Palette.Provider("codex", 0).Render("■ Codex") + " " + render.Palette.Provider("claude", 0).Render("■ Claude")
	lines := []string{fitDailyLine(render.Palette.Header().Render(title)+"  "+legend, width)}
	codexStyle := render.Palette.Provider("codex", 0)
	claudeStyle := render.Palette.Provider("claude", 0)
	for row := 0; row < plotHeight; row++ {
		label := dailyAxisLabel(maxValue, plotHeight, row, data.UsesTokens)
		axis := strings.Repeat(" ", max(0, axisWidth-lipgloss.Width(label)-1)) + label
		axisGlyph := "┤"
		if row == plotHeight-1 {
			axisGlyph = "└"
		}
		line := axis + axisGlyph
		for index, point := range data.Rows {
			amount := dailyPointAmount(point, data.UsesTokens)
			cellHeight := int(math.Ceil(amount / maxValue * float64(plotHeight)))
			cell := strings.Repeat(" ", columnWidth)
			if amount > 0 && row >= plotHeight-cellHeight {
				// Providers stack: Codex fills from the baseline, Claude sits
				// on top. The glyph difference keeps the split readable when
				// color is stripped (NO_COLOR, copied text, committed frames).
				claudeRows := int(math.Round(point.ClaudeValue(data.UsesTokens) / amount * float64(cellHeight)))
				char, style := "█", codexStyle
				if row < plotHeight-cellHeight+claudeRows {
					char, style = "▓", claudeStyle
				}
				cell = style.Render(strings.Repeat(char, columnWidth))
			} else if row == avgRow {
				cell = strings.Repeat("┈", columnWidth)
			}
			line += cell
			if index < len(data.Rows)-1 {
				line += " "
			}
		}
		if row == avgRow {
			line += "  avg " + formatDailyValue(data.Average, data.UsesTokens)
		}
		lines = append(lines, fitDailyLine(line, width))
	}
	ticks := make([]string, 0, len(data.Rows))
	step := max(1, len(data.Rows)/8)
	for index, point := range data.Rows {
		label := ""
		if point.Selected {
			label = "^"
		} else if index%step == 0 || index == len(data.Rows)-1 {
			label = shortDailyDate(point.Date)
		}
		left := max(0, (columnWidth-lipgloss.Width(label))/2)
		right := max(0, columnWidth-lipgloss.Width(label)-left)
		ticks = append(ticks, strings.Repeat(" ", left)+label+strings.Repeat(" ", right))
	}
	lines = append(lines, fitDailyLine(strings.Repeat(" ", axisWidth)+strings.Join(ticks, " "), width))
	return strings.Join(lines, "\n")
}

func renderDailyWideAnalysis(render theme.Context, data DailyPageData, width, height, detailOffset int) string {
	left, middle, right := dailyWidePaneWidths(width)
	return joinDailyPanes(
		[]string{
			renderDailyPane(render, dailyDetailTitle(data), strings.Join(dailyDetailLines(data, left, false), "\n"), left, height),
			renderDailyPane(render, dailyProjectsTitle(data), strings.Join(dailyProjectsAndTrends(render, data, middle), "\n"), middle, height),
			renderDailyPane(render, "LAST 10 DAYS · RESCALED", strings.Join(dailyMiniChartLines(render, data, right), "\n"), right, height),
		},
		[]int{left, middle, right}, dailyGap, height,
	)
}

func renderDailyStandardAnalysis(render theme.Context, data DailyPageData, width, height, detailOffset int) string {
	left := max(1, (width-dailyGap)/2)
	right := max(1, width-left-dailyGap)
	leftLines := dailyDetailLines(data, left, false)
	if detailOffset > 0 {
		leftLines = dailyWindow(leftLines, detailOffset, max(1, height-1))
	}
	rightLines := dailyProjectsAndTrends(render, data, right)
	return joinDailyPanes(
		[]string{
			renderDailyPane(render, dailyDetailTitle(data), strings.Join(leftLines, "\n"), left, height),
			renderDailyPane(render, "PROJECTS · "+dailyTrendRangeLabel(data), strings.Join(rightLines, "\n"), right, height),
		},
		[]int{left, right}, dailyGap, height,
	)
}

func renderDailyFloorDetail(render theme.Context, data DailyPageData, width, height, detailOffset int) string {
	usesTokens := dailyDetailUsesTokens(data)
	lines := []string{dailyDetailTitle(data) + " · PROVIDER SPLIT · " + dailyMetricLabel(usesTokens)}
	for _, provider := range data.Detail.Providers[:min(2, len(data.Detail.Providers))] {
		lines = append(lines, fmt.Sprintf("%-8s %s %3d%%", cleanDaily(provider.Provider), formatDailyValue(provider.Value, usesTokens), dailyShare(provider.Value, data.Detail.Value, usesTokens)))
	}
	if len(data.Detail.Providers) == 0 {
		lines = append(lines, "No provider usage recorded.")
	}
	modelTitle := "TOP MODELS BY COST"
	if usesTokens {
		modelTitle = "TOP MODELS BY TOKENS"
	}
	model := topDailyModel(data.Detail.Models, usesTokens)
	if model.Model == "" {
		lines = append(lines, modelTitle+" · none")
	} else {
		lines = append(lines, modelTitle+" · "+truncateDaily(cleanDaily(model.Model), max(8, width-24))+" "+formatDailyValue(model.Value, usesTokens))
	}
	fullLines := dailyDetailLines(data, width, false)
	if data.Sessions.Warning != "" {
		lines = append(lines, compactDailyWarning(data.Sessions.Warning, width))
	} else if len(fullLines) > height {
		lines = append(lines, render.Palette.Subtle().Render("↓ more below"))
	}
	if detailOffset > 0 {
		lines = dailyWindow(fullLines, detailOffset, max(1, height))
	}
	return fitDailyBlock(strings.Join(lines, "\n"), width, height)
}

func compactDailyWarning(warning string, width int) string {
	message := strings.TrimSpace(cleanDaily(warning))
	switch {
	case strings.Contains(message, "Cost attribution unavailable"):
		message = "Cost attribution unavailable · restore source · rerun history index"
	case strings.Contains(message, "History index unavailable"):
		message = "History index unavailable · run tokenomnom history index"
	}
	return truncateDaily("~ "+message, width)
}

func topDailyModel(models []DailyModel, tokens bool) DailyModel {
	if len(models) == 0 {
		return DailyModel{}
	}
	result := models[0]
	for _, model := range models[1:] {
		if dailyValueAmount(model.Value, tokens) > dailyValueAmount(result.Value, tokens) {
			result = model
		}
	}
	return result
}

func dailyDetailTitle(data DailyPageData) string {
	date := data.Detail.Date
	if date == "" {
		date = data.SelectedDate
	}
	if date == "" {
		return "DAY DETAIL"
	}
	return "DAY DETAIL · " + date + " · " + formatDailyValue(data.Detail.Value, dailyDetailUsesTokens(data))
}

func dailyDetailLines(data DailyPageData, width int, compact bool) []string {
	detail := data.Detail
	usesTokens := dailyDetailUsesTokens(data)
	if detail.Date == "" {
		return []string{"DAY DETAIL", "No active days in this range."}
	}
	lines := []string{
		formatDailyDetailTotal(detail, usesTokens),
		"PROVIDER SPLIT · " + dailyMetricLabel(usesTokens),
	}
	if usesTokens && detail.Value.Tokens > 0 {
		lines = append(lines, "UNPRICED DAY · TOKEN SHARES")
	}
	providerWidth := max(6, min(10, width/5))
	for _, provider := range detail.Providers {
		providerName := cleanDaily(provider.Provider)
		value := formatDailyValue(provider.Value, usesTokens)
		share := dailyShare(provider.Value, detail.Value, usesTokens)
		barWidth := max(4, min(18, width-providerWidth-17-lipgloss.Width(value)))
		line := fmt.Sprintf("%-*s %s %3d%% %s", providerWidth, providerName, value, share, dailyShareBar(share, barWidth))
		if !compact {
			line += " " + formatDailyNumber(provider.Value.Tokens)
		}
		lines = append(lines, line)
	}
	if len(detail.Providers) == 0 {
		lines = append(lines, "No provider usage recorded.")
	}
	modelTitle := "TOP MODELS BY COST"
	if usesTokens {
		modelTitle = "TOP MODELS BY TOKENS"
	}
	unpricedModels, partialModels := 0, 0
	for _, model := range detail.Models {
		if usesTokens || model.Value.Tokens == 0 {
			continue
		}
		if model.Value.PricedTokens == 0 {
			unpricedModels++
		} else if model.Value.PricedTokens < model.Value.Tokens {
			partialModels++
		}
	}
	countLabel := formatDailyNumber(int64(len(detail.Models)))
	switch {
	case unpricedModels > 0 && partialModels > 0:
		countLabel += fmt.Sprintf(" · %d not fully priced", unpricedModels+partialModels)
	case unpricedModels > 0:
		countLabel += fmt.Sprintf(" · %d unpriced", unpricedModels)
	case partialModels > 0:
		countLabel += fmt.Sprintf(" · %d partially priced", partialModels)
	}
	lines = append(lines, modelTitle+" · "+countLabel)
	models := append([]DailyModel(nil), detail.Models...)
	sort.SliceStable(models, func(i, j int) bool {
		left, right := dailyValueAmount(models[i].Value, usesTokens), dailyValueAmount(models[j].Value, usesTokens)
		if left != right {
			return left > right
		}
		return models[i].Model < models[j].Model
	})
	limit := len(models)
	if compact {
		limit = min(2, limit)
	} else {
		limit = min(5, limit)
	}
	for _, model := range models[:limit] {
		value := formatDailyValue(model.Value, usesTokens)
		nameWidth := max(8, width-lipgloss.Width(value)-8)
		name := truncateDaily(cleanDaily(model.Model), nameWidth)
		lines = append(lines, padDailyRight(name, nameWidth)+" "+value+" "+formatDailyShare(model.Value, detail.Value, usesTokens))
	}
	if hidden := len(models) - limit; hidden > 0 {
		lines = append(lines, fmt.Sprintf("+%d more models", hidden))
	}
	if len(models) == 0 {
		lines = append(lines, "No models recorded.")
	}
	if !compact {
		lines = append(lines, dailyAverageRangeLabel(data))
		if usesTokens {
			lines = append(lines, dailyUnavailableComparisonLine("cost"), dailyComparisonLine("tokens", detail.Value, data.Average, true))
		} else {
			lines = append(lines, dailyComparisonLine("cost", detail.Value, data.Average, false), dailyComparisonLine("tokens", detail.Value, data.Average, true))
		}
		lines = append(lines, dailySessionsComparisonLine(detail.Value, dailyAverageSessions(data)))
	}
	return lines
}

func dailyDetailUsesTokens(data DailyPageData) bool {
	return data.DetailUsesTokens || data.Detail.Value.PricedTokens == 0 && data.Detail.Value.Tokens > 0
}

func dailySessionMetricUsesTokens(data DailyPageData) bool {
	if data.UsesTokens || dailyDetailUsesTokens(data) {
		return true
	}
	for _, row := range data.Sessions.Rows {
		if dailySessionCostIsComplete(row) && row.PricedTokens > 0 {
			return false
		}
	}
	return len(data.Sessions.Rows) > 0
}

func dailySessionCostIsComplete(row DailySession) bool {
	return row.AttributionStatus == "" || row.AttributionStatus == "complete"
}

func dailySessionCostAmount(row DailySession) pricing.Money {
	if !dailySessionCostIsComplete(row) || row.PricedTokens == 0 {
		return 0
	}
	return row.Cost
}

func formatDailyDetailTotal(detail DailyDetail, tokens bool) string {
	return "TOTAL " + formatDailyValue(detail.Value, tokens)
}

func dailyProjectsTitle(data DailyPageData) string {
	count := dailySessionTotalLabel(data.Sessions)
	if data.Detail.Date == "" {
		return "PROJECTS"
	}
	return fmt.Sprintf("PROJECTS · %s · %s sessions", data.Detail.Date, count)
}

func dailyTrendRangeLabel(data DailyPageData) string {
	if data.RangeStart == "" || data.RangeEnd == "" {
		return "ALL-TIME TRENDS"
	}
	start, startErr := time.Parse("2006-01-02", data.RangeStart)
	end, endErr := time.Parse("2006-01-02", data.RangeEnd)
	if startErr != nil || endErr != nil || end.Before(start) {
		return "TRENDS"
	}
	days := int(end.Sub(start)/(24*time.Hour)) + 1
	return fmt.Sprintf("%d-DAY TRENDS", days)
}

func dailyRangeCountLabel(data DailyPageData) string {
	if data.RangeStart == "" || data.RangeEnd == "" {
		return "ALL-TIME"
	}
	start, startErr := time.Parse("2006-01-02", data.RangeStart)
	end, endErr := time.Parse("2006-01-02", data.RangeEnd)
	if startErr != nil || endErr != nil || end.Before(start) {
		return "RANGE"
	}
	return fmt.Sprintf("%d DAYS", int(end.Sub(start)/(24*time.Hour))+1)
}

func dailyAverageRangeLabel(data DailyPageData) string {
	trendLabel := dailyTrendRangeLabel(data)
	trendLabel = strings.TrimSuffix(trendLabel, " TRENDS")
	return "DAY vs " + trendLabel + " AVERAGE"
}

func dailyProjectsAndTrends(render theme.Context, data DailyPageData, width int) []string {
	lines := []string{"PROJECTS"}
	if data.Sessions.HasMore {
		lines = append(lines, "Project ranking unavailable; session page is bounded.")
	} else {
		projectTokens := dailySessionMetricUsesTokens(data)
		projects := dailyProjectSummaries(data.Sessions.Rows, projectTokens)
		projectLimit := min(6, len(projects))
		for _, project := range projects[:projectLimit] {
			share := dailyProjectShare(project, projects, projectTokens)
			value := formatDailyValue(project.Value, projectTokens)
			nameWidth := max(8, min(18, width-lipgloss.Width(value)-12))
			lines = append(lines, padDailyRight(truncateDaily(project.Name, nameWidth), nameWidth)+" "+value+" "+formatDailyNumber(int64(project.Sessions))+" "+dailyShareBar(share, max(4, width-nameWidth-12)))
		}
		if len(projects) == 0 {
			lines = append(lines, "No indexed sessions for this day.")
		}
		if len(projects) > projectLimit {
			lines = append(lines, fmt.Sprintf("+%d more projects", len(projects)-projectLimit))
		}
	}
	lines = append(lines, "", dailyTrendRangeLabel(data)+" · 7d Δ")
	trendRows := data.TrendRows
	if len(trendRows) == 0 {
		trendRows = data.Rows
	}
	metricLabel := "cost/day"
	if data.UsesTokens {
		metricLabel = "cost/day · unavailable"
	}
	trends := []struct {
		label  string
		values []float64
	}{
		{metricLabel, dailyTrendValues(trendRows, func(point DailyPoint) float64 {
			if data.UsesTokens {
				return 0
			}
			return dailyPointAmount(point, false)
		})},
		{"tokens/day", dailyTrendValues(trendRows, func(point DailyPoint) float64 { return float64(point.Total.Tokens) })},
		{"sessions/day", dailyTrendValues(trendRows, func(point DailyPoint) float64 { return float64(point.Total.Sessions) })},
		{"claude share", dailyTrendValues(trendRows, func(point DailyPoint) float64 {
			codex, claude := point.CodexValue(data.UsesTokens), point.ClaudeValue(data.UsesTokens)
			if codex+claude == 0 {
				return 0
			}
			return claude / (codex + claude)
		})},
	}
	for _, trend := range trends {
		lineWidth := max(1, width-lipgloss.Width(trend.label)-9)
		spark := dailySparkline(trend.values, lineWidth)
		switch {
		case strings.HasPrefix(trend.label, "cost"):
			spark = render.Palette.Money().Render(spark)
		case strings.HasPrefix(trend.label, "claude"):
			spark = render.Palette.Provider("claude", 0).Render(spark)
		default:
			spark = render.Palette.Emphasis().Render(spark)
		}
		lines = append(lines, padDailyRight(trend.label, lipgloss.Width(trend.label))+" "+spark+" "+dailyDelta(trend.values))
	}
	return lines
}

func dailyMiniChartLines(render theme.Context, data DailyPageData, width int) []string {
	rows := data.TrendRows
	if len(rows) == 0 {
		rows = data.Rows
	}
	if len(rows) > 10 {
		rows = rows[len(rows)-10:]
	}
	values := make([]float64, 0, len(rows))
	for _, row := range rows {
		values = append(values, dailyPointAmount(row, data.UsesTokens))
	}
	maximum := 0.0
	for _, value := range values {
		maximum = maxFloat(maximum, value)
	}
	lines := []string{fmt.Sprintf("ymax %s", formatDailyAxis(maximum, data.UsesTokens))}
	miniStyle := render.Palette.Money()
	if data.UsesTokens {
		miniStyle = render.Palette.Emphasis()
	}
	for _, chartLine := range dailyMiniChart(values, max(1, width), 8) {
		lines = append(lines, miniStyle.Render(chartLine))
	}
	lines = append(lines, "", "last 10 days", "avg "+formatDailyValue(dailyAverageValue(rows, data.UsesTokens), data.UsesTokens))
	if data.PeakDate != "" {
		lines = append(lines, "peak "+shortDailyDate(data.PeakDate)+" "+formatDailyValue(data.Peak, data.UsesTokens))
	}
	if data.SelectedDate != "" {
		lines = append(lines, "cursor "+shortDailyDate(data.SelectedDate))
	}
	return lines
}

func renderDailySessions(render theme.Context, data DailyPageData, width, height int, wide bool) string {
	heading := "SESSIONS"
	date := data.Detail.Date
	if date != "" {
		heading += " · " + date
	}
	rows := append([]DailySession(nil), data.Sessions.Rows...)
	sessionTokens := dailySessionMetricUsesTokens(data)
	total := data.Sessions.Total
	if total == 0 {
		total = len(rows)
	}
	more := false
	if !wide {
		if !data.Sessions.HasMore {
			sort.SliceStable(rows, func(i, j int) bool {
				if sessionTokens {
					return rows[i].Tokens > rows[j].Tokens
				}
				leftComplete, rightComplete := dailySessionCostIsComplete(rows[i]), dailySessionCostIsComplete(rows[j])
				if leftComplete != rightComplete {
					return leftComplete
				}
				return dailySessionCostAmount(rows[i]) > dailySessionCostAmount(rows[j])
			})
		}
		if len(rows) > 3 {
			rows = rows[:3]
			more = true
		} else if total > len(rows) || data.Sessions.HasMore {
			more = true
		}
	} else {
		more = data.Sessions.HasMore
	}
	warning := data.Sessions.Warning
	reserved := 2
	if more {
		reserved++
	}
	if warning != "" {
		reserved++
	}
	rowLimit := max(0, height-reserved)
	if len(rows) > rowLimit {
		more = true
		reserved++
		rowLimit = max(0, height-reserved)
		rows = rows[:min(len(rows), rowLimit)]
	}
	if more && height-reserved <= 0 {
		more = false
	}
	if more && !wide {
		// Standard tier always explains the omitted rows, even when the compact
		// band had to reduce its row budget by one.
		reserved = min(height-1, reserved)
	}
	title := heading
	metric := "cost"
	if sessionTokens {
		metric = "tokens"
	}
	if wide {
		title += " · all " + dailySessionTotalLabel(data.Sessions)
	} else if data.Sessions.HasMore {
		title += " · first 3 loaded of " + dailySessionTotalLabel(data.Sessions)
	} else {
		title += " · top 3 by " + metric + " of " + dailySessionTotalLabel(data.Sessions)
	}
	lines := []string{title, dailySessionHeader(width, wide)}
	for _, row := range rows {
		lines = append(lines, dailySessionRow(row, width, wide))
	}
	if more {
		if !data.Sessions.TotalKnown && data.Sessions.HasMore {
			lines = append(lines, "↓ more sessions · enter opens detail")
		} else {
			remaining := max(0, total-len(rows))
			if remaining > 0 {
				lines = append(lines, fmt.Sprintf("↓ %d more sessions · enter opens detail", remaining))
			} else {
				lines = append(lines, "↓ more sessions · enter opens detail")
			}
		}
	}
	if warning != "" {
		lines = append(lines, "~ "+cleanDaily(warning))
	}
	if len(rows) == 0 && warning == "" {
		lines = append(lines, "No indexed sessions for this day.")
	}
	return fitDailyBlock(strings.Join(lines, "\n"), width, height)
}

func dailySessionTotalLabel(data DailySessionData) string {
	total := data.Total
	if total == 0 && len(data.Rows) > 0 {
		total = len(data.Rows)
	}
	label := strconv.Itoa(total)
	if !data.TotalKnown && data.HasMore {
		label += "+"
	}
	return label
}

func dailySessionHeader(width int, wide bool) string {
	if wide {
		return truncateDaily("TIME  PROV   PROJECT       SESSION       MODEL              TOKENS       COST  PR  FIRST PROMPT", width)
	}
	return truncateDaily("TIME  PROV   PROJECT             TOKENS       COST  FIRST PROMPT", width)
}

func dailySessionRow(row DailySession, width int, wide bool) string {
	timeValue := padDailyRight(cleanDaily(row.Time), 5)
	provider := padDailyRight(cleanDaily(row.Provider), 6)
	project := padDailyRight(truncateDaily(cleanDaily(row.Project), 14), 14)
	tokens := padDailyLeft(formatDailyNumber(row.Tokens), 12)
	cost := padDailyLeft(formatDailySessionCost(row), 9)
	prompt := truncateDaily(cleanDaily(row.Prompt), max(1, width-55))
	if !wide {
		return truncateDaily(timeValue+"  "+provider+" "+project+" "+tokens+" "+cost+"  "+prompt, width)
	}
	model := padDailyRight(truncateDaily(cleanDaily(row.Model), 17), 17)
	session := padDailyRight(truncateDaily(cleanDaily(row.SessionID), 12), 12)
	return truncateDaily(timeValue+" "+provider+" "+project+" "+session+" "+model+" "+tokens+" "+cost+"  "+padDailyLeft(formatDailyNumber(int64(row.PromptCount)), 2)+"  "+prompt, width)
}

func formatDailySessionCost(row DailySession) string {
	if row.PricedTokens == 0 || !dailySessionCostIsComplete(row) {
		return "—"
	}
	return formatDailyMoney(row.Cost)
}

func joinDailyPanes(panes []string, widths []int, gap, height int) string {
	if len(panes) == 0 || len(panes) != len(widths) {
		return ""
	}
	parts := make([]string, 0, len(panes)*2-1)
	for index, value := range panes {
		if index > 0 {
			parts = append(parts, fitDailyBlock("", gap, height))
		}
		parts = append(parts, fitDailyBlock(value, widths[index], height))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...)
}

func renderDailyPane(render theme.Context, title, content string, width, height int) string {
	lines := []string{dailyHeading(render, title, width)}
	lines = append(lines, strings.Split(strings.TrimSuffix(content, "\n"), "\n")...)
	return fitDailyBlock(strings.Join(lines, "\n"), width, height)
}

func dailyHeading(render theme.Context, title string, width int) string {
	title = strings.TrimSpace(title)
	label := render.Palette.Header().Render(title)
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitDailyLine(label, width)
	}
	return fitDailyLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func dailyRule(render theme.Context, width int) string {
	return fitDailyLine(render.Palette.Border().Render(strings.Repeat("─", max(1, width))), width)
}

func fitDailyBlock(value string, width, height int) string {
	width, height = max(1, width), max(1, height)
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index, line := range lines {
		lines[index] = fitDailyLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func fitDailyLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	value = lipgloss.NewStyle().Inline(true).MaxWidth(width).Render(value)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func dailyWidePaneWidths(width int) (int, int, int) {
	available := max(3, width-dailyGap*2)
	left := min(72, max(40, available*42/100))
	middle := min(54, max(32, available*30/100))
	right := available - left - middle
	for right < 20 && left > 30 {
		left--
		right++
	}
	for right < 20 && middle > 24 {
		middle--
		right++
	}
	return max(1, left), max(1, middle), max(1, right)
}

func dailyPointAmount(point DailyPoint, tokens bool) float64 {
	return dailyValueAmount(point.Total, tokens)
}

func dailyValueAmount(value DailyValue, tokens bool) float64 {
	if tokens {
		return float64(value.Tokens)
	}
	return float64(value.Cost)
}

func (point DailyPoint) CodexValue(tokens bool) float64 {
	return dailyValueAmount(point.Codex, tokens)
}

func (point DailyPoint) ClaudeValue(tokens bool) float64 {
	return dailyValueAmount(point.Claude, tokens)
}

func dailyChartColumnWidth(contentWidth, pointCount int) int {
	if contentWidth <= 0 || pointCount <= 0 {
		return 1
	}
	return max(1, min(4, (contentWidth-max(0, pointCount-1))/pointCount))
}

func dailyFullRangeChartWidth(pointCount, columnWidth int) int {
	if pointCount <= 0 {
		return 0
	}
	return pointCount*max(1, columnWidth) + max(0, pointCount-1)
}

func dailyAxisLabel(maximum float64, plotHeight, row int, tokens bool) string {
	if plotHeight <= 1 {
		return formatDailyAxis(0, tokens)
	}
	for tick := 0; tick < 5; tick++ {
		position := tick * (plotHeight - 1) / 4
		if row == position {
			value := maximum * float64(4-tick) / 4
			return formatDailyAxis(value, tokens)
		}
	}
	return ""
}

func formatDailyAxis(value float64, tokens bool) string {
	if tokens {
		return formatDailyCompactNumber(int64(value))
	}
	return formatDailyCompactMoney(pricing.Money(value))
}

func formatDailyValue(value DailyValue, tokens bool) string {
	if tokens {
		return formatDailyCompactNumber(value.Tokens)
	}
	if value.PricedTokens == 0 {
		return "—"
	}
	return formatDailyMoney(value.Cost)
}

func formatDailyMoney(value pricing.Money) string {
	cents := value.RoundedCents()
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return sign + "$" + formatDailyNumber(cents/100) + "." + fmt.Sprintf("%02d", cents%100)
}

func formatDailyCompactMoney(value pricing.Money) string {
	amount := float64(value) / 1_000_000_000
	if math.Abs(amount) >= 1000 {
		return fmt.Sprintf("$%.0fk", amount/1000)
	}
	if math.Abs(amount) >= 10 {
		return fmt.Sprintf("$%.0f", amount)
	}
	return fmt.Sprintf("$%.1f", amount)
}

func formatDailyCompactNumber(value int64) string {
	amount := float64(value)
	if math.Abs(amount) >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", amount/1_000_000_000)
	}
	if math.Abs(amount) >= 1_000_000 {
		return fmt.Sprintf("%.1fM", amount/1_000_000)
	}
	if math.Abs(amount) >= 1_000 {
		return fmt.Sprintf("%.1fk", amount/1_000)
	}
	return formatDailyNumber(value)
}

func formatDailyNumber(value int64) string {
	negative := value < 0
	if negative {
		value = -value
	}
	digits := strconv.FormatInt(value, 10)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func dailyMetricLabel(tokens bool) string {
	if tokens {
		return "TOKENS"
	}
	return "COST"
}

func dailyShare(value, total DailyValue, tokens bool) int {
	amount, denominator := dailyValueAmount(value, tokens), dailyValueAmount(total, tokens)
	if amount <= 0 || denominator <= 0 {
		return 0
	}
	return min(100, int(amount/denominator*100+0.5))
}

func dailyShareBar(share, width int) string {
	width = max(1, width)
	filled := int(math.Round(float64(share) / 100 * float64(width)))
	if share > 0 {
		filled = max(1, filled)
	}
	return strings.Repeat("█", min(width, filled)) + strings.Repeat("░", max(0, width-filled))
}

func formatDailyShare(value, total DailyValue, tokens bool) string {
	return fmt.Sprintf("%d%%", dailyShare(value, total, tokens))
}

func dailyComparisonLine(label string, value, average DailyValue, tokens bool) string {
	amount, averageAmount := dailyValueAmount(value, tokens), dailyValueAmount(average, tokens)
	return fmt.Sprintf("%-8s %10s vs %10s %+.1f%%", label, formatDailyValue(value, tokens), formatDailyValue(average, tokens), dailyDeltaPercent(amount, averageAmount))
}

func dailySessionsComparisonLine(value DailyValue, average float64) string {
	amount := float64(value.Sessions)
	return fmt.Sprintf("%-8s %10s vs %10s %+.1f%%", "sessions", formatDailyNumber(int64(value.Sessions)), formatDailySessionAverage(average), dailyDeltaPercent(amount, average))
}

func dailyAverageSessions(data DailyPageData) float64 {
	if data.AverageSessions != 0 || data.Average.Sessions == 0 {
		return data.AverageSessions
	}
	return float64(data.Average.Sessions)
}

func formatDailySessionAverage(value float64) string {
	if value == math.Trunc(value) {
		return formatDailyNumber(int64(value))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

func dailyUnavailableComparisonLine(label string) string {
	return fmt.Sprintf("%-8s %10s vs %10s %s", label, "—", "—", "—")
}

func dailyDeltaPercent(value, baseline float64) float64 {
	if baseline == 0 {
		return 0
	}
	return (value - baseline) / baseline * 100
}

func dailyProjectSummaries(rows []DailySession, tokens bool) []dailyProjectSummary {
	byName := make(map[string]dailyProjectSummary)
	for _, row := range rows {
		name := strings.TrimSpace(cleanDaily(row.Project))
		if name == "" {
			name = "unattributed"
		}
		value := byName[name]
		value.Name = name
		value.Value.Tokens += row.Tokens
		if dailySessionCostIsComplete(row) {
			value.Value.Cost += row.Cost
			value.Value.PricedTokens += row.PricedTokens
		}
		value.Sessions++
		byName[name] = value
	}
	result := make([]dailyProjectSummary, 0, len(byName))
	for _, value := range byName {
		result = append(result, value)
	}
	sort.SliceStable(result, func(i, j int) bool {
		left, right := dailyValueAmount(result[i].Value, tokens), dailyValueAmount(result[j].Value, tokens)
		if left != right {
			return left > right
		}
		if result[i].Sessions != result[j].Sessions {
			return result[i].Sessions > result[j].Sessions
		}
		return result[i].Name < result[j].Name
	})
	return result
}

type dailyProjectSummary struct {
	Name     string
	Value    DailyValue
	Sessions int
}

func dailyProjectShare(project dailyProjectSummary, projects []dailyProjectSummary, tokens bool) int {
	total := 0.0
	for _, value := range projects {
		total += dailyValueAmount(value.Value, tokens)
	}
	return dailyShare(project.Value, DailyValue{Cost: pricing.Money(total), Tokens: int64(total), PricedTokens: 1}, tokens)
}

func dailyTrendValues(rows []DailyPoint, value func(DailyPoint) float64) []float64 {
	result := make([]float64, 0, len(rows))
	for _, row := range rows {
		result = append(result, value(row))
	}
	return result
}

func dailySparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return strings.Repeat("·", max(1, width))
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum, maximum = minFloat(minimum, value), maxFloat(maximum, value)
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	result := make([]rune, len(values))
	for index, value := range values {
		level := 0
		if maximum > minimum {
			level = int(math.Round((value - minimum) / (maximum - minimum) * float64(len(levels)-1)))
		}
		result[index] = levels[min(max(0, level), len(levels)-1)]
	}
	for len(result) < width {
		result = append(result, '·')
	}
	return string(result)
}

func dailyDelta(values []float64) string {
	if len(values) < 2 {
		return "—"
	}
	start := max(0, len(values)-14)
	middle := max(start+1, len(values)-7)
	before, after := averageFloat(values[start:middle]), averageFloat(values[middle:])
	if before == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.1f%%", dailyDeltaPercent(after, before))
}

func dailyMiniChart(values []float64, width, height int) []string {
	if len(values) == 0 {
		return []string{strings.Repeat("·", width)}
	}
	maximum := 0.0
	for _, value := range values {
		maximum = maxFloat(maximum, value)
	}
	maximum = maxFloat(maximum, 1)
	columnWidth := dailyChartColumnWidth(width, len(values))
	lines := make([]string, 0, height)
	for row := 0; row < max(1, height); row++ {
		line := ""
		for index, value := range values {
			level := int(math.Ceil(value / maximum * float64(height)))
			cell := " "
			if value > 0 && row >= height-level {
				cell = "█"
			}
			line += strings.Repeat(cell, columnWidth)
			if index < len(values)-1 {
				line += " "
			}
		}
		lines = append(lines, fitDailyLine(line, width))
	}
	return lines
}

func dailyAverageValue(rows []DailyPoint, tokens bool) DailyValue {
	var total DailyValue
	for _, row := range rows {
		total.Cost += row.Total.Cost
		total.Tokens += row.Total.Tokens
		total.PricedTokens += row.Total.PricedTokens
		total.Sessions += row.Total.Sessions
	}
	count := max(1, len(rows))
	total.Cost /= pricing.Money(count)
	total.Tokens /= int64(count)
	if total.PricedTokens > 0 {
		total.PricedTokens = 1
	}
	total.Sessions /= count
	if tokens {
		total.PricedTokens = 0
	}
	return total
}

func compressDailyPoints(points []DailyPoint, limit int) []DailyPoint {
	if len(points) <= limit || limit <= 0 {
		return points
	}
	bucketSize := (len(points) + limit - 1) / limit
	result := make([]DailyPoint, 0, limit)
	for start := 0; start < len(points); start += bucketSize {
		end := min(len(points), start+bucketSize)
		bucket := DailyPoint{Date: points[start].Date}
		for _, point := range points[start:end] {
			bucket.Total = addDailyValues(bucket.Total, point.Total)
			bucket.Codex = addDailyValues(bucket.Codex, point.Codex)
			bucket.Claude = addDailyValues(bucket.Claude, point.Claude)
			bucket.Selected = bucket.Selected || point.Selected
		}
		bucket.Total = averageDailyValue(bucket.Total, end-start)
		bucket.Codex = averageDailyValue(bucket.Codex, end-start)
		bucket.Claude = averageDailyValue(bucket.Claude, end-start)
		result = append(result, bucket)
	}
	return result
}

func addDailyValues(left, right DailyValue) DailyValue {
	left.Cost += right.Cost
	left.Tokens += right.Tokens
	left.PricedTokens += right.PricedTokens
	left.UnpricedTokens += right.UnpricedTokens
	left.Sessions += right.Sessions
	return left
}

func averageDailyValue(value DailyValue, count int) DailyValue {
	if count <= 1 {
		return value
	}
	value.Cost /= pricing.Money(count)
	value.Tokens /= int64(count)
	value.UnpricedTokens /= int64(count)
	value.Sessions /= count
	if value.PricedTokens > 0 {
		value.PricedTokens = 1
	}
	return value
}

func dailyWindow(lines []string, offset, height int) []string {
	if len(lines) == 0 {
		return nil
	}
	offset = min(max(0, offset), max(0, len(lines)-max(1, height)))
	end := min(len(lines), offset+max(1, height))
	result := append([]string(nil), lines[offset:end]...)
	if offset > 0 && len(result) > 0 {
		result[0] = "↑ more above"
	}
	if end < len(lines) && len(result) > 0 {
		result[len(result)-1] = "↓ more below"
	}
	return result
}

func dailyRangeLabel(start, end string) string {
	from, fromErr := time.Parse("2006-01-02", start)
	to, toErr := time.Parse("2006-01-02", end)
	if fromErr != nil || toErr != nil || start == "" || end == "" {
		return ""
	}
	if from == to {
		return from.Format("Jan 02")
	}
	return from.Format("Jan 02") + " – " + to.Format("Jan 02")
}

func shortDailyDate(value string) string {
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed.Format("02")
	}
	if len(value) >= 2 {
		return value[len(value)-2:]
	}
	return value
}

func cleanDaily(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != '\u007f' && (r < '\u0080' || r > '\u009f') {
			return r
		}
		return -1
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

func truncateDaily(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	if width <= 3 {
		runes := []rune(value)
		for len(runes) > 0 && lipgloss.Width(string(runes)) > width {
			runes = runes[:len(runes)-1]
		}
		return string(runes)
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"...") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "..."
}

func padDailyRight(value string, width int) string {
	value = truncateDaily(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func padDailyLeft(value string, width int) string {
	value = truncateDaily(value, width)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func averageFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	total := 0.0
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func clampDaily(value, minimum, maximum int) int {
	return min(maximum, max(minimum, value))
}
