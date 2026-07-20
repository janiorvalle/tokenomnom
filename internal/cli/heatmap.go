package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

const heatmapDateLayout = "2006-01-02"

type heatmapWindow struct {
	From time.Time
	To   time.Time
}

type heatmapDay struct {
	Date        time.Time
	Cost        pricing.Money
	TotalTokens int64
	Level       int
}

type heatmapStats struct {
	ActiveDays    int
	TotalCost     pricing.Money
	BusiestDate   string
	BusiestCost   pricing.Money
	BusiestTokens int64
	LongestStreak int
}

type heatmapReport struct {
	Window     heatmapWindow
	Metric     string
	Days       []heatmapDay
	Stats      heatmapStats
	UsesTokens bool
}

type jsonHeatmapWindow struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type jsonHeatmapDay struct {
	Date        string  `json:"date"`
	CostUSD     float64 `json:"cost_usd"`
	TotalTokens int64   `json:"total_tokens"`
	Level       int     `json:"level"`
}

type jsonHeatmapBusiestDay struct {
	Date        *string `json:"date"`
	CostUSD     float64 `json:"cost_usd"`
	TotalTokens int64   `json:"total_tokens"`
}

type jsonHeatmapStats struct {
	ActiveDays    int                   `json:"active_days"`
	TotalCostUSD  float64               `json:"total_cost_usd"`
	BusiestDay    jsonHeatmapBusiestDay `json:"busiest_day"`
	LongestStreak int                   `json:"longest_streak"`
}

type jsonHeatmapData struct {
	Window jsonHeatmapWindow `json:"window"`
	Metric string            `json:"metric"`
	Days   []jsonHeatmapDay  `json:"days"`
	Stats  jsonHeatmapStats  `json:"stats"`
}

func newHeatmapCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var flags reportFlags
	var year int
	cmd := &cobra.Command{
		Use:   "heatmap",
		Short: "Show a calendar heatmap of daily spend",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags.applyConfig(cmd)
			if cmd.Flags().Changed("year") && (year < 1000 || year > 9999) {
				return fmt.Errorf("invalid --year %d (expected YYYY)", year)
			}
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			return runReport(cmd, codexDir, claudeDir, timezone, flags.noSync, func(database *store.Store, context jsonReportContext) error {
				window, err := heatmapDateWindow(year, heatmapToday(context.Timezone))
				if err != nil {
					return err
				}
				filter.Since = window.From.Format(heatmapDateLayout)
				filter.Until = window.To.Format(heatmapDateLayout)
				report, costs, err := loadHeatmapReport(database, filter, window)
				if err != nil {
					return err
				}
				return writeHeatmapReport(cmd, report, costs, flags, context)
			})
		},
	}
	cmd.Flags().IntVar(&year, "year", 0, "show calendar year YYYY")
	cmd.Flags().StringVar(&flags.provider, "provider", "", "filter by provider (codex or claude)")
	cmd.Flags().StringVar(&flags.model, "model", "", "filter by exact model name")
	cmd.Flags().BoolVar(&flags.noSync, "no-sync", false, "report stored data without syncing first")
	return cmd
}

func heatmapToday(timezone string) time.Time {
	location := time.Local
	if timezone != "" && timezone != "Local" {
		if loaded, err := time.LoadLocation(timezone); err == nil {
			location = loaded
		}
	}
	now := time.Now().In(location)
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, location)
}

func heatmapDateWindow(year int, today time.Time) (heatmapWindow, error) {
	location := today.Location()
	if year != 0 {
		if year < 1000 || year > 9999 {
			return heatmapWindow{}, fmt.Errorf("invalid --year %d (expected YYYY)", year)
		}
		return heatmapWindow{
			From: time.Date(year, time.January, 1, 0, 0, 0, 0, location),
			To:   time.Date(year, time.December, 31, 0, 0, 0, 0, location),
		}, nil
	}
	today = dateOnly(today)
	return heatmapWindow{From: today.AddDate(-1, 0, 1), To: today}, nil
}

func loadHeatmapReport(database *store.Store, filter store.Filter, window heatmapWindow) (heatmapReport, reportCosts, error) {
	var rows []store.DailyRow
	costs := reportCosts{ByDate: make(map[string]aggregateCost)}
	var err error
	if database != nil {
		rows, err = database.Daily(filter)
		if err != nil {
			return heatmapReport{}, reportCosts{}, err
		}
		costs, err = loadReportCosts(database, filter, nil)
		if err != nil {
			return heatmapReport{}, reportCosts{}, err
		}
	}
	return buildHeatmapReport(window, rows, costs), costs, nil
}

func buildHeatmapReport(window heatmapWindow, rows []store.DailyRow, costs reportCosts) heatmapReport {
	tokensByDate := make(map[string]int64, len(rows))
	for _, row := range rows {
		tokensByDate[row.Date] = row.Total
	}
	report := heatmapReport{Window: window, Metric: "cost_usd", UsesTokens: costs.Grand.PricedTokens == 0}
	if report.UsesTokens {
		report.Metric = "tokens"
	}
	for day := window.From; !day.After(window.To); day = day.AddDate(0, 0, 1) {
		date := day.Format(heatmapDateLayout)
		report.Days = append(report.Days, heatmapDay{
			Date: day, Cost: costs.ByDate[date].Total, TotalTokens: tokensByDate[date],
		})
	}
	assignHeatmapLevels(report.Days, report.UsesTokens)
	report.Stats = calculateHeatmapStats(report.Days, report.UsesTokens)
	return report
}

func assignHeatmapLevels(days []heatmapDay, tokens bool) {
	values := make([]int64, 0, len(days))
	for _, day := range days {
		if value := heatmapValue(day, tokens); value > 0 {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return
	}
	if len(values) == 1 {
		for index := range days {
			if heatmapValue(days[index], tokens) > 0 {
				days[index].Level = 4
			}
		}
		return
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	// Nearest-rank quartiles are the values at ceil(N/4), ceil(N/2), and
	// ceil(3N/4). A day goes in the first bucket whose boundary contains it;
	// collapsed equal boundaries are skipped so tied values share the higher
	// intensity rather than being split arbitrarily.
	boundaries := []int64{
		values[(len(values)+3)/4-1],
		values[(len(values)+1)/2-1],
		values[(3*len(values)+3)/4-1],
	}
	for index := range days {
		value := heatmapValue(days[index], tokens)
		if value == 0 {
			continue
		}
		if value == values[len(values)-1] {
			days[index].Level = 4
			continue
		}
		level := 4
		for boundaryIndex, boundary := range boundaries {
			if value <= boundary {
				level = boundaryIndex + 1
				for boundaryIndex+1 < len(boundaries) && boundaries[boundaryIndex+1] == boundary {
					level++
					boundaryIndex++
				}
				break
			}
		}
		days[index].Level = level
	}
}

func heatmapValue(day heatmapDay, tokens bool) int64 {
	if tokens {
		return day.TotalTokens
	}
	return int64(day.Cost)
}

func calculateHeatmapStats(days []heatmapDay, tokens bool) heatmapStats {
	var stats heatmapStats
	streak := 0
	for _, day := range days {
		stats.TotalCost = stats.TotalCost.Add(day.Cost)
		value := heatmapValue(day, tokens)
		if value == 0 {
			streak = 0
			continue
		}
		stats.ActiveDays++
		streak++
		if streak > stats.LongestStreak {
			stats.LongestStreak = streak
		}
		busiestValue := stats.BusiestCost
		if tokens {
			busiestValue = pricing.Money(stats.BusiestTokens)
		}
		if stats.BusiestDate == "" || value > int64(busiestValue) {
			stats.BusiestDate = day.Date.Format(heatmapDateLayout)
			stats.BusiestCost = day.Cost
			stats.BusiestTokens = day.TotalTokens
		}
	}
	return stats
}

func writeHeatmapReport(cmd *cobra.Command, report heatmapReport, costs reportCosts, flags reportFlags, context jsonReportContext) error {
	if currentFormat(cmd) == "json" {
		data := heatmapJSON(report)
		return writeJSONEnvelope(cmd, "heatmap", context.Timezone, filtersJSON(flags), reportWarnings(context.Warnings, costs), data)
	}
	if context.NoStore {
		writeWarningLine(cmd, noProviderHint)
		return nil
	}
	fmt.Fprint(cmd.OutOrStdout(), renderHeatmap(theme.FromContext(cmd.Context()), report))
	writeCostNotes(cmd, costs)
	return nil
}

func heatmapJSON(report heatmapReport) jsonHeatmapData {
	days := make([]jsonHeatmapDay, 0, len(report.Days))
	for _, day := range report.Days {
		days = append(days, jsonHeatmapDay{
			Date: day.Date.Format(heatmapDateLayout), CostUSD: moneyUSD(day.Cost),
			TotalTokens: day.TotalTokens, Level: day.Level,
		})
	}
	var busiestDate *string
	if report.Stats.BusiestDate != "" {
		value := report.Stats.BusiestDate
		busiestDate = &value
	}
	return jsonHeatmapData{
		Window: jsonHeatmapWindow{From: report.Window.From.Format(heatmapDateLayout), To: report.Window.To.Format(heatmapDateLayout)},
		Metric: report.Metric,
		Days:   days,
		Stats: jsonHeatmapStats{
			ActiveDays: report.Stats.ActiveDays, TotalCostUSD: moneyUSD(report.Stats.TotalCost),
			BusiestDay:    jsonHeatmapBusiestDay{Date: busiestDate, CostUSD: moneyUSD(report.Stats.BusiestCost), TotalTokens: report.Stats.BusiestTokens},
			LongestStreak: report.Stats.LongestStreak,
		},
	}
}

func renderHeatmap(render theme.Context, report heatmapReport) string {
	displayWindow, cellWidth, truncated := fitHeatmapWindow(report.Window, render.Width)
	gridStart := weekStart(displayWindow.From)
	gridEnd := weekEnd(displayWindow.To)
	weekCount := calendarDaysBetween(gridStart, gridEnd)/7 + 1
	daysByDate := make(map[string]heatmapDay, len(report.Days))
	for _, day := range report.Days {
		daysByDate[day.Date.Format(heatmapDateLayout)] = day
	}

	var output strings.Builder
	output.WriteString("    ")
	monthLine := make([]byte, weekCount*cellWidth+2)
	for index := range monthLine {
		monthLine[index] = ' '
	}
	for month := monthStart(displayWindow.From); !month.After(displayWindow.To); month = month.AddDate(0, 1, 0) {
		week := calendarDaysBetween(gridStart, weekStart(month)) / 7
		label := month.Format("Jan")
		position := week * cellWidth
		if position >= 0 && position+len(label) <= len(monthLine) {
			copy(monthLine[position:], label)
		}
	}
	monthText := strings.TrimRight(string(monthLine), " ")
	if render.Mode == theme.Styled {
		monthText = render.Palette.Subtle().Render(monthText)
	}
	output.WriteString(monthText)
	output.WriteByte('\n')

	labels := map[time.Weekday]string{time.Monday: "Mon ", time.Wednesday: "Wed ", time.Friday: "Fri "}
	plainGlyphs := []string{"·", "░", "▒", "▓", "█"}
	for weekday := time.Sunday; weekday <= time.Saturday; weekday++ {
		label := labels[weekday]
		output.WriteString(fmt.Sprintf("%-4s", label))
		for week := 0; week < weekCount; week++ {
			date := gridStart.AddDate(0, 0, week*7+int(weekday))
			if date.Before(displayWindow.From) || date.After(displayWindow.To) || date.Before(report.Window.From) || date.After(report.Window.To) {
				output.WriteString(strings.Repeat(" ", cellWidth))
				continue
			}
			day := daysByDate[date.Format(heatmapDateLayout)]
			glyph := plainGlyphs[day.Level]
			if render.Mode == theme.Styled {
				glyph = render.Palette.Heatmap(day.Level).Render("▪")
			}
			output.WriteString(glyph)
			output.WriteString(strings.Repeat(" ", cellWidth-1))
		}
		output.WriteByte('\n')
	}
	if truncated {
		line := fmt.Sprintf("showing %s–%s of %s–%s", displayWindow.From.Format("Jan"), displayWindow.To.Format("Jan"), report.Window.From.Format("Jan 2006"), report.Window.To.Format("Jan 2006"))
		if render.Mode == theme.Styled {
			line = render.Palette.Subtle().Render(line)
		}
		output.WriteString(line)
		output.WriteByte('\n')
	}
	output.WriteString(renderHeatmapLegend(render))
	output.WriteByte('\n')
	caption := heatmapCaption(report)
	if render.Mode == theme.Styled {
		caption = renderStyledHeatmapCaption(render, report)
	}
	output.WriteString(caption)
	output.WriteByte('\n')
	return output.String()
}

// renderStyledHeatmapCaption lifts the caption's values above their labels;
// the Plain caption in heatmapCaption stays byte-stable.
func renderStyledHeatmapCaption(render theme.Context, report heatmapReport) string {
	subtle := render.Palette.Subtle()
	emphasis := render.Palette.Header()
	money := render.Palette.Money()
	busiest := subtle.Render("none")
	if report.Stats.BusiestDate != "" {
		if report.UsesTokens {
			busiest = emphasis.Render(report.Stats.BusiestDate) + subtle.Render(" · ") +
				emphasis.Render(formatNumber(report.Stats.BusiestTokens)) + subtle.Render(" tokens")
		} else {
			busiest = emphasis.Render(report.Stats.BusiestDate) + subtle.Render(" · ") +
				money.Render(formatUSD(report.Stats.BusiestCost))
		}
	}
	totalCost := money.Render(formatUSD(report.Stats.TotalCost))
	metricNote := ""
	if report.UsesTokens {
		totalCost = subtle.Render("unpriced")
		metricNote = subtle.Render(" · showing tokens (unpriced)")
	}
	return emphasis.Render(formatNumber(int64(report.Stats.ActiveDays))) + subtle.Render(" active days · total cost ") +
		totalCost + subtle.Render(" · busiest ") + busiest + subtle.Render(" · longest streak ") +
		emphasis.Render(pluralCount(report.Stats.LongestStreak, "day", "days")) + metricNote
}

func renderHeatmapLegend(render theme.Context) string {
	var cells strings.Builder
	for level := 0; level <= 4; level++ {
		glyph := []string{"·", "░", "▒", "▓", "█"}[level]
		if render.Mode == theme.Styled {
			glyph = render.Palette.Heatmap(level).Render("▪")
		}
		cells.WriteString(glyph)
	}
	return "Less " + cells.String() + " More"
}

func heatmapCaption(report heatmapReport) string {
	busiest := "none"
	if report.Stats.BusiestDate != "" {
		if report.UsesTokens {
			busiest = fmt.Sprintf("%s · %s tokens", report.Stats.BusiestDate, formatNumber(report.Stats.BusiestTokens))
		} else {
			busiest = fmt.Sprintf("%s · %s", report.Stats.BusiestDate, formatUSD(report.Stats.BusiestCost))
		}
	}
	totalCost := formatUSD(report.Stats.TotalCost)
	metricNote := ""
	if report.UsesTokens {
		totalCost = "unpriced"
		metricNote = " · showing tokens (unpriced)"
	}
	return fmt.Sprintf("%s active days · total cost %s · busiest %s · longest streak %s%s",
		formatNumber(int64(report.Stats.ActiveDays)), totalCost, busiest,
		pluralCount(report.Stats.LongestStreak, "day", "days"), metricNote)
}

func pluralCount(value int, singular, plural string) string {
	unit := plural
	if value == 1 {
		unit = singular
	}
	return fmt.Sprintf("%s %s", formatNumber(int64(value)), unit)
}

func fitHeatmapWindow(window heatmapWindow, width int) (heatmapWindow, int, bool) {
	for _, cellWidth := range []int{2, 1} {
		weeks := heatmapWeekCount(window)
		if 4+weeks*cellWidth+2 <= width {
			return window, cellWidth, false
		}
	}
	capacity := max(1, width-6)
	end := monthEnd(window.To)
	start := monthStart(window.To)
	for {
		candidate := start.AddDate(0, -1, 0)
		if candidate.Before(window.From) || heatmapWeekCount(heatmapWindow{From: candidate, To: end}) > capacity {
			break
		}
		start = candidate
	}
	return heatmapWindow{From: start, To: end}, 1, true
}

func heatmapWeekCount(window heatmapWindow) int {
	return calendarDaysBetween(weekStart(window.From), weekEnd(window.To))/7 + 1
}

func calendarDaysBetween(from, to time.Time) int {
	fromUTC := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC)
	toUTC := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC)
	return int(toUTC.Sub(fromUTC).Hours() / 24)
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

func monthEnd(value time.Time) time.Time {
	return monthStart(value).AddDate(0, 1, -1)
}
