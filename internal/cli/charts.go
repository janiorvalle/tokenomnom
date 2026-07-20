package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/NimbleMarkets/ntcharts/canvas"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

const (
	chartHeight = 6
	// Below 32 columns even one two-column bar plus readable axes cannot scan well.
	minimumChartWidth = 32
	minimumBarWidth   = 2
	// Bars widen up to this when few periods share a wide plot.
	maximumBarWidth = 6
	barGap          = 1
)

type chartPeriod struct {
	label  string
	values map[discover.Provider]providerChartValue
}

func writeDailyChart(cmd *cobra.Command, rows []store.DailyRow, costs reportCosts) {
	periods := make([]chartPeriod, 0, len(rows))
	for _, row := range rows {
		periods = append(periods, chartPeriod{label: row.Date, values: costs.ByDateProvider[row.Date]})
	}
	writePeriodChart(cmd, periods, "day", "days", chartUsesTokens(costs))
}

func writeMonthlyChart(cmd *cobra.Command, rows []store.MonthlyRow, costs reportCosts) {
	periods := make([]chartPeriod, 0, len(rows))
	for _, row := range rows {
		periods = append(periods, chartPeriod{label: row.Month, values: costs.ByMonthProvider[row.Month]})
	}
	writePeriodChart(cmd, periods, "month", "months", chartUsesTokens(costs))
}

func chartUsesTokens(costs reportCosts) bool {
	return costs.Grand.PricedTokens == 0
}

func writePeriodChart(cmd *cobra.Command, periods []chartPeriod, singular, plural string, tokens bool) {
	render := theme.FromContext(cmd.Context())
	if render.Mode != theme.Styled || len(periods) == 0 {
		return
	}
	chart := renderPeriodChart(render, periods, singular, plural, tokens)
	if chart == "" {
		return
	}
	fmt.Fprint(cmd.OutOrStdout(), chart)
	fmt.Fprintln(cmd.OutOrStdout())
}

// renderPeriodChart is pure string generation; terminal probing happens in theme.Resolve.
func renderPeriodChart(render theme.Context, periods []chartPeriod, singular, plural string, tokens bool) string {
	unit := "cost/" + singular
	if tokens {
		unit = "tokens/" + singular + " (unpriced)"
	}
	legend := render.Palette.Provider("codex", 0).Render("■") + " Codex  " +
		render.Palette.Provider("claude", 0).Render("■") + " Claude  " +
		render.Palette.Subtle().Render(unit)
	if render.Width < minimumChartWidth || lipgloss.Width(legend) > render.Width {
		return ""
	}
	maxValue := maxChartValue(periods, tokens)
	if maxValue == 0 {
		maxValue = 1
	}

	maxLabel := formatChartAxis(maxValue, tokens)
	zeroLabel := formatChartAxis(0, tokens)
	axisWidth := max(lipgloss.Width(maxLabel), lipgloss.Width(zeroLabel)) + 1
	plotWidth := max(render.Width-axisWidth-1, minimumBarWidth)
	periodCapacity := max(1, (plotWidth+barGap)/(minimumBarWidth+barGap))
	originalCount := len(periods)
	if len(periods) > periodCapacity {
		periods = periods[len(periods)-periodCapacity:]
	}
	maxValue = maxChartValue(periods, tokens)
	if maxValue == 0 {
		maxValue = 1
	}
	maxLabel = formatChartAxis(maxValue, tokens)

	barWidth := minimumBarWidth
	if len(periods) > 0 {
		barWidth = max(minimumBarWidth, min(maximumBarWidth, (plotWidth-(len(periods)-1)*barGap)/len(periods)))
	}
	chart := barchart.New(plotWidth, chartHeight,
		barchart.WithNoAxis(),
		barchart.WithNoAutoBarWidth(),
		barchart.WithBarWidth(barWidth),
		barchart.WithBarGap(barGap),
	)
	for _, period := range periods {
		chart.Push(barchart.BarData{Values: []barchart.BarValue{
			{Name: "Codex", Value: providerValue(period.values[discover.ProviderCodex], tokens), Style: render.Palette.Provider("codex", 0)},
			{Name: "Claude", Value: providerValue(period.values[discover.ProviderClaude], tokens), Style: render.Palette.Provider("claude", 0)},
		}})
	}
	chart.SetMax(maxValue)
	chart.Draw()
	bindChartRenderer(&chart, render.Renderer)

	var output strings.Builder
	output.WriteString(legend)
	output.WriteByte('\n')
	if originalCount > len(periods) {
		output.WriteString(render.Palette.Subtle().Render(fmt.Sprintf("showing last %d of %d %s", len(periods), originalCount, plural)))
		output.WriteByte('\n')
	}
	chartLines := strings.Split(strings.TrimSuffix(chart.View(), "\n"), "\n")
	for index, line := range chartLines {
		axis := ""
		switch index {
		case 0:
			axis = maxLabel
		case len(chartLines) - 1:
			axis = zeroLabel
		}
		axis = strings.Repeat(" ", axisWidth-lipgloss.Width(axis)) + axis
		output.WriteString(render.Palette.Subtle().Render(axis))
		output.WriteString(strings.TrimRight(line, " "))
		output.WriteByte('\n')
	}
	ticks := make([]string, 0, len(periods))
	for _, period := range periods {
		label := shortPeriodLabel(period.label)
		leftPad := max(0, (barWidth-lipgloss.Width(label))/2)
		rightPad := max(0, barWidth-lipgloss.Width(label)-leftPad)
		ticks = append(ticks, strings.Repeat(" ", leftPad)+label+strings.Repeat(" ", rightPad))
	}
	output.WriteString(strings.Repeat(" ", axisWidth))
	output.WriteString(render.Palette.Subtle().Render(strings.TrimRight(strings.Join(ticks, strings.Repeat(" ", barGap)), " ")))
	output.WriteByte('\n')
	if caption := periodCaption(periods); caption != "" {
		output.WriteString(strings.Repeat(" ", axisWidth))
		output.WriteString(render.Palette.Subtle().Render(caption))
		output.WriteByte('\n')
	}
	return output.String()
}

// periodCaption names the charted span in one line — "Jul 2026" or
// "Dec 2025 – Jan 2026" for days, "2026" or "2025 – 2026" for months —
// replacing the old per-column month/year fragment rows.
func periodCaption(periods []chartPeriod) string {
	if len(periods) == 0 {
		return ""
	}
	first, last := periods[0].label, periods[len(periods)-1].label
	daily := len(first) == len("2006-01-02")
	layout := "2006-01"
	if daily {
		layout = "2006-01-02"
	}
	from, errFrom := time.Parse(layout, first)
	to, errTo := time.Parse(layout, last)
	if errFrom != nil || errTo != nil {
		return ""
	}
	if daily {
		if from.Year() == to.Year() && from.Month() == to.Month() {
			return from.Format("Jan 2006")
		}
		return from.Format("Jan 2006") + " – " + to.Format("Jan 2006")
	}
	if from.Year() == to.Year() {
		return from.Format("2006")
	}
	return from.Format("2006") + " – " + to.Format("2006")
}

func bindChartRenderer(chart *barchart.Model, renderer *lipgloss.Renderer) {
	// ntcharts initializes empty cells with lipgloss's global style. Binding all
	// cells avoids a second renderer and its synchronous background probe.
	for y := 0; y < chart.Height(); y++ {
		for x := 0; x < chart.Width(); x++ {
			point := canvas.Point{X: x, Y: y}
			cell := chart.Canvas.Cell(point)
			cell.Style = cell.Style.Renderer(renderer)
			chart.Canvas.SetCell(point, cell)
		}
	}
}

func maxChartValue(periods []chartPeriod, tokens bool) float64 {
	maximum := 0.0
	for _, period := range periods {
		if value := chartValue(period.values, tokens); value > maximum {
			maximum = value
		}
	}
	return maximum
}

func chartValue(values map[discover.Provider]providerChartValue, tokens bool) float64 {
	return providerValue(values[discover.ProviderCodex], tokens) + providerValue(values[discover.ProviderClaude], tokens)
}

func providerValue(value providerChartValue, tokens bool) float64 {
	if tokens {
		return float64(value.Tokens)
	}
	return float64(value.Cost.Total)
}

func formatChartAxis(value float64, tokens bool) string {
	if tokens {
		return formatNumber(int64(value))
	}
	return formatUSD(pricing.Money(value))
}

func shortPeriodLabel(value string) string {
	if len(value) >= 2 {
		return value[len(value)-2:]
	}
	return value
}
