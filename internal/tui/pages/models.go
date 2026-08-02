package pages

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// ModelsViewport describes the already allocated page rectangle. The shell
// owns tier classification; keeping the flags here avoids making the pages
// package import its parent tui package.
type ModelsViewport struct {
	Width    int
	Height   int
	Wide     bool
	Tall     bool
	Standard bool
	Sort     int
	Offset   int
}

// ModelPageData is the view-ready model report. The dashboard loader owns all
// store and pricing work; this package only formats the supplied snapshot.
type ModelPageData struct {
	ScopeLabel string
	Rows       []ModelPageRow
	Total      ModelPageRow
	Providers  []ModelProviderRow
	Pricing    []ModelPricingRow
	Rates      []ModelRateRow
	Unpriced   []ModelUnpricedRow
	Recency    []ModelRecencyRow
	PerSession []ModelPerSessionRow
	Matrix     ModelMatrix
}

type ModelPageRow struct {
	Provider       string
	Model          string
	Tokens         int64
	Cost           pricing.Money
	PricedTokens   int64
	UnpricedTokens int64
	TokenShare     float64
	CostShare      float64
	Pricing        string
	Sessions       int
	Days           int
	FirstDate      string
	LastDate       string
	Sparkline      []float64
}

type ModelProviderRow struct {
	Provider       string
	Models         int
	Tokens         int64
	Cost           pricing.Money
	PricedTokens   int64
	UnpricedTokens int64
	TokenShare     float64
	CostShare      float64
}

type ModelPricingRow struct {
	Label          string
	Models         int
	Tokens         int64
	Cost           pricing.Money
	PricedTokens   int64
	UnpricedTokens int64
}

type ModelRateRow struct {
	Model        string
	Cost         pricing.Money
	PricedTokens int64
}

type ModelUnpricedRow struct {
	Model  string
	Tokens int64
}

type ModelRecencyRow struct {
	Model string
	Days  int
}

type ModelPerSessionRow struct {
	Model            string
	Tokens           int64
	Sessions         int
	TokensPerSession int64
}

type ModelMatrix struct {
	Dates []string
	Rows  []ModelMatrixRow
}

type ModelMatrixRow struct {
	Model  string
	Values []float64
	Cost   pricing.Money
}

// RenderModels renders the models page into the exact rectangle supplied by
// the shell. Wide+tall uses the full three-band composition; standard keeps
// the master table and two analysis panes; floor stays intentionally compact.
func RenderModels(render theme.Context, data ModelPageData, viewport ModelsViewport) string {
	width := max(1, viewport.Width)
	if viewport.Width == 0 {
		width = max(1, render.Width)
	}
	height := max(1, viewport.Height)
	if viewport.Height == 0 {
		height = 1
	}

	switch {
	case viewport.Wide && viewport.Tall:
		return renderModelsWideTall(render, data, viewport, width, height)
	case viewport.Wide || viewport.Standard:
		return renderModelsStandard(render, data, viewport, width, height)
	default:
		return renderModelsFloor(render, data, viewport, width, height)
	}
}

func renderModelsWideTall(render theme.Context, data ModelPageData, viewport ModelsViewport, width, height int) string {
	b1Height := min(13, max(5, min(10, len(data.Rows))+3))
	remaining := max(1, height-b1Height-2)
	b2Height := min(24, max(8, remaining-4))
	b3Height := max(1, height-b1Height-b2Height-2)

	parts := []string{
		renderModelMaster(render, data, viewport, width, b1Height, true),
		modelRule(render, width),
		renderModelAnalysis(render, data, width, b2Height, true),
		modelRule(render, width),
		renderModelMatrix(render, data, width, b3Height),
	}
	return modelFitBlock(strings.Join(parts, "\n"), width, height)
}

func renderModelsStandard(render theme.Context, data ModelPageData, viewport ModelsViewport, width, height int) string {
	masterRows := min(10, max(1, len(data.Rows)))
	b1Height := min(max(5, masterRows+3), max(5, height-9))
	b2Height := max(1, height-b1Height-1)
	parts := []string{
		renderModelMaster(render, data, viewport, width, b1Height, false),
		modelRule(render, width),
		renderModelAnalysis(render, data, width, b2Height, false),
	}
	return modelFitBlock(strings.Join(parts, "\n"), width, height)
}

func renderModelsFloor(render theme.Context, data ModelPageData, viewport ModelsViewport, width, height int) string {
	return renderModelMaster(render, data, viewport, width, height, false)
}

func renderModelMaster(render theme.Context, data ModelPageData, viewport ModelsViewport, width, height int, wide bool) string {
	sparkline := wide && width >= 150
	floor := !viewport.Standard && !viewport.Wide
	title := modelMasterTitle(render, data, viewport, width)
	header := modelMasterHeader(render, width, sparkline, wide, floor)
	lines := []string{title, header}

	capacity := min(10, max(1, height-3))
	if len(data.Rows) > capacity {
		start := min(max(0, viewport.Offset), max(0, len(data.Rows)-capacity))
		if start+capacity < len(data.Rows) {
			capacity = max(1, capacity-1)
		}
	}
	rows, _, more := modelVisibleRows(data.Rows, viewport.Offset, capacity)
	if len(data.Rows) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No model usage is available yet."))
	} else {
		for _, row := range rows {
			lines = append(lines, renderModelMasterRow(render, row, data, width, sparkline, wide, floor))
		}
		if more {
			lines = append(lines, render.Palette.Subtle().Render("↓ more models"))
		}
	}

	total := data.Total
	if total.Model == "" {
		total.Model = fmt.Sprintf("%d models", len(data.Rows))
	}
	if total.Provider == "" {
		total.Provider = "TOTAL"
	}
	lines = append(lines, renderModelMasterRow(render, total, data, width, sparkline, wide, floor))
	return modelFitBlock(strings.Join(lines, "\n"), width, height)
}

func modelMasterTitle(render theme.Context, data ModelPageData, viewport ModelsViewport, width int) string {
	scope := data.ScopeLabel
	if scope == "" {
		scope = "ALL TIME"
	}
	left := render.Palette.Header().Render("MODELS · " + strings.ToUpper(scope))
	sortLabel := "tokens"
	switch viewport.Sort {
	case 1:
		sortLabel = "cost"
	case 2:
		sortLabel = "name"
	}
	right := render.Palette.Subtle().Render(fmt.Sprintf("%d models · %d providers · sorted by %s", len(data.Rows), len(data.Providers), sortLabel))
	space := max(1, width-lipgloss.Width(left)-lipgloss.Width(right))
	return fitLine(left+strings.Repeat(" ", space)+right, width)
}

func modelMasterHeader(render theme.Context, width int, sparkline, wide, floor bool) string {
	if floor {
		return fitLine("  "+modelColumnsLine(render, []modelColumn{
			{text: "PROVIDER", width: 8}, {text: "MODEL", width: 25}, {text: "TOKENS", width: 14, right: true}, {text: "COST", width: 11, right: true},
		}, width, true), width)
	}
	if !wide {
		return fitLine("  "+modelColumnsLine(render, []modelColumn{
			{text: "PROVIDER", width: 8}, {text: "MODEL", width: 25}, {text: "TOKENS", width: 14, right: true},
			{text: "COST", width: 11, right: true}, {text: "PRICING", width: 8}, {text: "DAYS", width: 4, right: true},
			{text: "LAST", width: 10},
		}, width, true), width)
	}
	columns := []modelColumn{
		{text: "PROV", width: 6}, {text: "MODEL", width: 24}, {text: "TOKENS", width: 14, right: true},
		{text: "TOK%", width: 5, right: true}, {text: "SHARE", width: 10}, {text: "COST", width: 10, right: true},
		{text: "COST%", width: 6, right: true}, {text: "SHARE", width: 10}, {text: "$/1M", width: 6, right: true},
		{text: "PRICING", width: 8}, {text: "SESSIONS", width: 8, right: true}, {text: "DAYS", width: 4, right: true},
		{text: "FIRST", width: 10}, {text: "LAST", width: 10},
	}
	if sparkline {
		columns = append(columns, modelColumn{text: "COST 30D", width: 10})
	}
	return modelColumnsLine(render, columns, width, true)
}

type modelColumn struct {
	text  string
	width int
	right bool
}

func modelColumnsLine(render theme.Context, columns []modelColumn, width int, header bool) string {
	parts := make([]string, 0, len(columns))
	for _, column := range columns {
		value := truncate(column.text, column.width)
		if header {
			value = render.Palette.Header().Render(value)
		}
		parts = append(parts, aligned(value, column.width, column.right))
	}
	return fitLine(strings.Join(parts, " "), width)
}

func renderModelMasterRow(render theme.Context, row ModelPageRow, data ModelPageData, width int, sparkline, wide, floor bool) string {
	provider := row.Provider
	if provider == "" {
		provider = "TOTAL"
	}
	model := row.Model
	if model == "" {
		model = fmt.Sprintf("%d models", len(data.Rows))
	}
	marker := "  "
	providerText := render.Palette.Provider(strings.ToLower(provider), 0).Render(truncate(modelProviderLabel(provider), 6))
	if provider == "TOTAL" {
		providerText = render.Palette.Header().Render(truncate(provider, 6))
	}
	modelText := truncate(model, 24)
	if row.Model == "" && row.Provider == "TOTAL" {
		modelText = truncate(model, 24)
	}
	if floor {
		return fitLine(marker+strings.Join([]string{
			aligned(providerText, 8, false), aligned(modelText, 25, false), aligned(commaInteger(row.Tokens), 14, true), aligned(modelMoney(row), 11, true),
		}, " "), width)
	}
	if !wide {
		return fitLine(marker+strings.Join([]string{
			aligned(providerText, 8, false), aligned(modelText, 25, false), aligned(commaInteger(row.Tokens), 14, true),
			aligned(modelMoney(row), 11, true), aligned(modelPricingText(render, row), 8, false),
			aligned(modelCount(row.Days), 4, true), aligned(row.LastDate, 10, false),
		}, " "), width)
	}

	columns := []string{
		aligned(providerText, 6, false), aligned(modelText, 24, false), aligned(commaInteger(row.Tokens), 14, true),
		aligned(modelPercent(row.TokenShare, 1), 5, true), modelShareBar(render, row.TokenShare, 10),
		aligned(modelMoney(row), 10, true), aligned(modelPercent(row.CostShare, 2), 6, true), modelShareBar(render, row.CostShare, 10),
		aligned(modelRate(row), 6, true), aligned(modelPricingText(render, row), 8, false),
		aligned(modelSessions(row), 8, true), aligned(modelCount(row.Days), 4, true), aligned(row.FirstDate, 10, false), aligned(row.LastDate, 10, false),
	}
	if sparkline {
		columns = append(columns, aligned(modelSparkline(row.Sparkline, 10), 10, false))
	}
	return fitLine(marker+strings.Join(columns, " "), width)
}

func modelVisibleRows(rows []ModelPageRow, offset, capacity int) ([]ModelPageRow, int, bool) {
	if len(rows) == 0 {
		return nil, 0, false
	}
	capacity = max(1, capacity)
	start := min(max(0, offset), max(0, len(rows)-capacity))
	end := min(len(rows), start+capacity)
	return rows[start:end], start, end < len(rows)
}

func modelMoney(row ModelPageRow) string {
	return formatMoney(row.Cost, row.PricedTokens, false, row.UnpricedTokens > 0)
}

func modelRate(row ModelPageRow) string {
	if row.PricedTokens <= 0 {
		return "—"
	}
	rate := float64(row.Cost) / float64(row.PricedTokens) / 1000
	return fmt.Sprintf("$%.2f", rate)
}

func modelPricingText(render theme.Context, row ModelPageRow) string {
	value := row.Pricing
	if value == "" {
		value = "unpriced"
	}
	if row.Provider == "TOTAL" && strings.HasSuffix(value, " priced") {
		value = strings.TrimSuffix(value, " priced") + " rate"
	}
	if value == "proxy" || value == "estimated" {
		return render.Palette.Badge(value).Render(value)
	}
	if value == "unpriced" || value == "partial" {
		return render.Palette.Warning().Render(value)
	}
	return render.Palette.Subtle().Render(value)
}

func modelProviderLabel(value string) string {
	switch strings.ToLower(value) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "total":
		return "TOTAL"
	default:
		if value == "" {
			return ""
		}
		return strings.ToUpper(value[:1]) + value[1:]
	}
}

func modelSessions(row ModelPageRow) string {
	if row.Sessions <= 0 {
		return "—"
	}
	return commaInteger(int64(row.Sessions))
}

func modelCount(value int) string {
	if value <= 0 {
		return "—"
	}
	return strconv.Itoa(value)
}

func modelPercent(value float64, decimals int) string {
	if value <= 0 {
		return "0"
	}
	return fmt.Sprintf("%."+strconv.Itoa(decimals)+"f", value*100)
}

func modelShareBar(render theme.Context, value float64, width int) string {
	width = max(1, width)
	value = minFloat(1, maxFloat(0, value))
	filled := int(math.Round(value * float64(width)))
	filled = min(width, max(0, filled))
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return render.Palette.Emphasis().Render(bar)
}

func modelSparkline(values []float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return strings.Repeat("·", max(0, width))
	}
	if len(values) > width {
		values = values[len(values)-width:]
	}
	minimum, maximum := values[0], values[0]
	for _, value := range values[1:] {
		minimum = minFloat(minimum, value)
		maximum = maxFloat(maximum, value)
	}
	levels := []rune("▁▂▃▄▅▆▇█")
	result := make([]rune, len(values))
	for index, value := range values {
		level := 0
		if maximum > minimum {
			level = int(math.Round((value - minimum) / (maximum - minimum) * float64(len(levels)-1)))
		}
		result[index] = levels[min(len(levels)-1, max(0, level))]
	}
	return string(result) + strings.Repeat("·", max(0, width-len(result)))
}

func renderModelAnalysis(render theme.Context, data ModelPageData, width, height int, wide bool) string {
	if wide {
		panes := []string{
			renderModelPane(render, "TOKENS ↔ COST", modelTokenCostLines(render, data, modelPaneWidth(width, 3, 0)), modelPaneWidth(width, 3, 0), height),
			renderModelPane(render, "PROVIDER ROLLUP", modelProviderLines(render, data), modelPaneWidth(width, 3, 1), height),
			renderModelPane(render, "COST PER 1M", modelRatesLines(render, data), modelPaneWidth(width, 3, 2), height),
		}
		return modelJoinPanes(panes, width, height)
	}
	panes := []string{
		renderModelPane(render, "ANALYSIS", modelStandardAnalysisLines(render, data), modelPaneWidth(width, 2, 0), height),
		renderModelPane(render, "COST & RECENCY", modelStandardCostLines(render, data), modelPaneWidth(width, 2, 1), height),
	}
	return modelJoinPanes(panes, width, height)
}

func modelPaneWidth(width, count, index int) int {
	available := max(count, width-(count-1))
	if count == 2 {
		left := available / 2
		if index == 0 {
			return max(1, left)
		}
		return max(1, available-left)
	}
	first := available * 42 / 100
	second := available * 29 / 100
	switch index {
	case 0:
		return max(1, first)
	case 1:
		return max(1, second)
	default:
		return max(1, available-first-second)
	}
}

func renderModelPane(render theme.Context, title string, content []string, width, height int) string {
	lines := []string{modelRuleTitle(render, title, width)}
	lines = append(lines, content...)
	for len(lines) < height {
		lines = append(lines, render.Palette.Subtle().Render(strings.Repeat("·", max(1, width))))
	}
	return modelFitBlock(strings.Join(lines, "\n"), width, height)
}

func modelJoinPanes(panes []string, width, height int) string {
	if len(panes) == 0 {
		return modelFitBlock("", width, height)
	}
	lines := make([]string, height)
	for row := range lines {
		parts := make([]string, 0, len(panes)*2-1)
		for index, pane := range panes {
			if index > 0 {
				parts = append(parts, " ")
			}
			paneLines := strings.Split(pane, "\n")
			if row < len(paneLines) {
				parts = append(parts, paneLines[row])
			} else {
				parts = append(parts, "")
			}
		}
		lines[row] = fitLine(strings.Join(parts, ""), width)
	}
	return strings.Join(lines, "\n")
}

func modelRuleTitle(render theme.Context, title string, width int) string {
	label := render.Palette.Header().Render(title)
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitLine(label, width)
	}
	return fitLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func modelRule(render theme.Context, width int) string {
	return fitLine(render.Palette.Border().Render(strings.Repeat("─", max(1, width))), width)
}

func modelTokenCostLines(render theme.Context, data ModelPageData, width int) []string {
	lines := []string{render.Palette.Header().Render("TOKENS ↔ MODEL ↔ COST")}
	rows := append([]ModelPageRow(nil), data.Rows...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Tokens > rows[j].Tokens })
	barWidth := max(8, min(24, (width-25)/2))
	maximumTokens, maximumCost := int64(0), pricing.Money(0)
	for _, row := range rows[:min(6, len(rows))] {
		if row.Tokens > maximumTokens {
			maximumTokens = row.Tokens
		}
		if row.Cost > maximumCost {
			maximumCost = row.Cost
		}
	}
	for index, row := range rows[:min(6, len(rows))] {
		model := truncate(row.Model, 21)
		tokenShare, costShare := float64(0), float64(0)
		if maximumTokens > 0 {
			tokenShare = float64(row.Tokens) / float64(maximumTokens)
		}
		if maximumCost > 0 {
			costShare = float64(row.Cost) / float64(maximumCost)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s %s", modelShareBar(render, tokenShare, barWidth), aligned(model, 21, false), modelShareBar(render, costShare, barWidth), modelRank(index)))
	}
	lines = append(lines, render.Palette.Header().Render("COST CONCENTRATION · CUMULATIVE"))
	cumulative := float64(0)
	for index, row := range dataRowsByCost(data.Rows)[:min(5, len(data.Rows))] {
		cumulative += row.CostShare
		lines = append(lines, fmt.Sprintf("top %-2d %-21s %5s %s", index+1, truncate(row.Model, 21), modelPercent(cumulative, 1), modelMoney(row)))
	}
	return lines
}

func modelProviderLines(render theme.Context, data ModelPageData) []string {
	lines := []string{render.Palette.Header().Render("PROVIDER  MODELS  TOKENS      COST   SHARE")}
	for _, row := range data.Providers {
		lines = append(lines, fmt.Sprintf("%-8s %6d %9s %10s %5s", truncate(modelProviderLabel(row.Provider), 8), row.Models, commaShort(row.Tokens), formatMoney(row.Cost, row.PricedTokens, false, row.UnpricedTokens > 0), modelPercent(row.CostShare, 1)))
	}
	lines = append(lines, render.Palette.Header().Render("PRICING PROVENANCE"))
	for _, row := range data.Pricing {
		lines = append(lines, fmt.Sprintf("%-14s %2d %10s %s", truncate(row.Label, 14), row.Models, commaShort(row.Tokens), formatMoney(row.Cost, row.PricedTokens, false, row.UnpricedTokens > 0)))
	}
	lines = append(lines, render.Palette.Header().Render("TOKENS PER SESSION · INDEXED"))
	for _, row := range data.PerSession[:min(6, len(data.PerSession))] {
		value := "—"
		if row.Sessions > 0 {
			value = commaShort(row.TokensPerSession)
		}
		lines = append(lines, fmt.Sprintf("%-22s %9s", truncate(row.Model, 22), value))
	}
	return lines
}

func modelRatesLines(render theme.Context, data ModelPageData) []string {
	lines := []string{}
	if len(data.Rates) > 0 {
		for _, row := range data.Rates[:min(7, len(data.Rates))] {
			lines = append(lines, fmt.Sprintf("%-22s %6s %s", truncate(row.Model, 22), modelRateFor(row), modelShareBar(render, rateShare(row, data.Rates), 12)))
		}
	} else {
		lines = append(lines, render.Palette.Subtle().Render("No priced models."))
	}
	lines = append(lines, render.Palette.Header().Render(fmt.Sprintf("UNPRICED MODELS · %d", len(data.Unpriced))))
	for _, row := range data.Unpriced[:min(4, len(data.Unpriced))] {
		lines = append(lines, fmt.Sprintf("%-22s %10s  no rate", truncate(row.Model, 22), commaShort(row.Tokens)))
	}
	lines = append(lines, render.Palette.Header().Render("RECENCY · DAYS SINCE LAST USE"))
	for _, row := range data.Recency[:min(7, len(data.Recency))] {
		label := "today"
		if row.Days > 0 {
			label = fmt.Sprintf("%dd ago", row.Days)
		}
		lines = append(lines, fmt.Sprintf("%-22s %-8s", truncate(row.Model, 22), label))
	}
	return lines
}

func modelStandardAnalysisLines(render theme.Context, data ModelPageData) []string {
	lines := []string{render.Palette.Header().Render("COST CONCENTRATION")}
	cumulative := float64(0)
	for index, row := range dataRowsByCost(data.Rows)[:min(6, len(data.Rows))] {
		cumulative += row.CostShare
		lines = append(lines, fmt.Sprintf("top %-2d %-22s %5s", index+1, truncate(row.Model, 22), modelPercent(cumulative, 1)))
	}
	lines = append(lines, render.Palette.Header().Render("PROVIDER ROLLUP"))
	for _, row := range data.Providers {
		lines = append(lines, fmt.Sprintf("%-9s %2d models  %s", truncate(modelProviderLabel(row.Provider), 9), row.Models, modelPercent(row.CostShare, 1)))
	}
	lines = append(lines, render.Palette.Header().Render("TOKENS PER SESSION · INDEXED"))
	for _, row := range data.PerSession[:min(6, len(data.PerSession))] {
		value := "—"
		if row.Sessions > 0 {
			value = commaShort(row.TokensPerSession)
		}
		lines = append(lines, fmt.Sprintf("%-22s %9s", truncate(row.Model, 22), value))
	}
	return lines
}

func modelStandardCostLines(render theme.Context, data ModelPageData) []string {
	lines := []string{render.Palette.Header().Render("COST PER 1M TOKENS")}
	for _, row := range data.Rates[:min(7, len(data.Rates))] {
		lines = append(lines, fmt.Sprintf("%-22s %s", truncate(row.Model, 22), modelRateFor(row)))
	}
	lines = append(lines, render.Palette.Header().Render("UNPRICED MODELS"))
	for _, row := range data.Unpriced[:min(4, len(data.Unpriced))] {
		lines = append(lines, fmt.Sprintf("%-22s %s", truncate(row.Model, 22), commaShort(row.Tokens)))
	}
	lines = append(lines, render.Palette.Header().Render("RECENCY"))
	for _, row := range data.Recency[:min(7, len(data.Recency))] {
		label := "today"
		if row.Days > 0 {
			label = fmt.Sprintf("%dd ago", row.Days)
		}
		lines = append(lines, fmt.Sprintf("%-22s %s", truncate(row.Model, 22), label))
	}
	return lines
}

func dataRowsByCost(rows []ModelPageRow) []ModelPageRow {
	result := append([]ModelPageRow(nil), rows...)
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Cost != result[j].Cost {
			return result[i].Cost > result[j].Cost
		}
		return result[i].Model < result[j].Model
	})
	return result
}

func modelRank(index int) string {
	return fmt.Sprintf("%-2d", index+1)
}

func modelRateFor(row ModelRateRow) string {
	if row.PricedTokens <= 0 {
		return "—"
	}
	return fmt.Sprintf("$%.2f", modelRateValue(row))
}

func rateShare(row ModelRateRow, rows []ModelRateRow) float64 {
	maximum := float64(0)
	for _, value := range rows {
		if rate := modelRateValue(value); rate > maximum {
			maximum = rate
		}
	}
	if maximum <= 0 {
		return 0
	}
	return modelRateValue(row) / maximum
}

func modelRateValue(row ModelRateRow) float64 {
	if row.PricedTokens <= 0 {
		return 0
	}
	return float64(row.Cost) / float64(row.PricedTokens) / 1000
}

func commaShort(value int64) string {
	if value == 0 {
		return "0"
	}
	abs := value
	if abs < 0 {
		abs = -abs
	}
	for _, unit := range []struct {
		threshold int64
		suffix    string
	}{
		{1_000_000_000, "B"}, {1_000_000, "M"}, {1_000, "k"},
	} {
		if abs < unit.threshold {
			continue
		}
		return fmt.Sprintf("%.1f%s", float64(value)/float64(unit.threshold), unit.suffix)
	}
	return commaInteger(value)
}

func renderModelMatrix(render theme.Context, data ModelPageData, width, height int) string {
	lines := []string{modelRuleTitle(render, "MODEL × DAY · 30 DAYS", width)}
	labelWidth := min(28, max(16, width-42))
	costWidth := 14
	cellArea := max(30, width-labelWidth-costWidth-2)
	cellWidth := 1
	if cellArea >= 119 {
		cellWidth = 3
	} else if cellArea >= 89 {
		cellWidth = 2
	}
	lines = append(lines, modelMatrixHeader(data.Matrix.Dates, labelWidth, cellWidth, costWidth, width))
	rowCapacity := max(0, height-4)
	maxValues := modelMatrixDailyMaximum(data.Matrix.Rows)
	for _, row := range data.Matrix.Rows[:min(rowCapacity, len(data.Matrix.Rows))] {
		lines = append(lines, modelMatrixRow(row, labelWidth, cellWidth, costWidth, width, maxValues))
	}
	if len(data.Matrix.Rows) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No model activity is available in the latest 30 days."))
	}
	if len(data.Matrix.Rows) > rowCapacity {
		lines = append(lines, render.Palette.Subtle().Render(fmt.Sprintf("↓ %d more models", len(data.Matrix.Rows)-rowCapacity)))
	}
	lines = append(lines, render.Palette.Subtle().Render("less ·░▒▓█ more  ·  each cell is one model on one day"))
	for len(lines) < height {
		lines = append(lines, render.Palette.Subtle().Render(strings.Repeat("·", max(1, width))))
	}
	return modelFitBlock(strings.Join(lines, "\n"), width, height)
}

func modelMatrixHeader(dates []string, labelWidth, cellWidth, costWidth, width int) string {
	parts := []string{aligned("MODEL", labelWidth, false)}
	for index := 0; index < 30; index++ {
		label := ""
		if index < len(dates) && (index%5 == 0 || index == len(dates)-1) && len(dates[index]) >= 10 {
			label = dates[index][8:10]
		}
		parts = append(parts, aligned(label, cellWidth, false))
	}
	parts = append(parts, aligned("30-DAY COST", costWidth, true))
	return fitLine(strings.Join(parts, " "), width)
}

func modelMatrixRow(row ModelMatrixRow, labelWidth, cellWidth, costWidth, width int, maximum []float64) string {
	parts := []string{aligned(truncate(row.Model, labelWidth), labelWidth, false)}
	for index := 0; index < 30; index++ {
		value := 0.0
		if index < len(row.Values) {
			value = row.Values[index]
		}
		dayMaximum := 0.0
		if index < len(maximum) {
			dayMaximum = maximum[index]
		}
		level := 0
		if dayMaximum > 0 && value > 0 {
			level = int(math.Ceil(value / dayMaximum * 4))
		}
		cell := string([]rune("·░▒▓█")[min(4, max(0, level))])
		if cellWidth > 1 {
			cell = strings.Repeat(cell, cellWidth)
		}
		parts = append(parts, cell)
	}
	cost := "—"
	if row.Cost > 0 {
		cost = formatMoney(row.Cost, 1, false, false)
	}
	parts = append(parts, aligned(cost, costWidth, true))
	return fitLine(strings.Join(parts, " "), width)
}

func modelMatrixDailyMaximum(rows []ModelMatrixRow) []float64 {
	maximum := make([]float64, 30)
	for _, row := range rows {
		for index, value := range row.Values {
			if index >= len(maximum) {
				break
			}
			maximum[index] = maxFloat(maximum[index], value)
		}
	}
	return maximum
}

func modelFitBlock(value string, width, height int) string {
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
		lines[index] = fitLine(line, width)
	}
	return strings.Join(lines, "\n")
}
