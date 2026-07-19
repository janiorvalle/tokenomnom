package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

const noProviderHint = "No coding-agent data was found. Use --codex-dir, --claude-dir, or the TOKENOMNOM_*_DIR environment variables to point tokenomnom at it."
const noUsageMessage = "No usage found for the requested range."

type reportFlags struct {
	provider string
	model    string
	since    string
	until    string
	noSync   bool
}

func newSummaryCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var flags reportFlags
	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Summarize token usage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			return runReport(cmd, codexDir, claudeDir, timezone, flags.noSync, func(database *store.Store) error {
				return writeSummaryReport(cmd, database, filter)
			})
		},
	}
	addReportFlags(cmd, &flags)
	return cmd
}

func newDailyCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var flags reportFlags
	last := 30
	cmd := &cobra.Command{
		Use:   "daily",
		Short: "Show token usage by day",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			if last <= 0 {
				return fmt.Errorf("--last must be greater than zero")
			}
			dateRangeSet := cmd.Flags().Changed("since") || cmd.Flags().Changed("until")
			if cmd.Flags().Changed("last") && dateRangeSet {
				return fmt.Errorf("--last cannot be combined with --since or --until")
			}
			return runReport(cmd, codexDir, claudeDir, timezone, flags.noSync, func(database *store.Store) error {
				rows, err := database.Daily(filter)
				if err != nil {
					return err
				}
				if !dateRangeSet && len(rows) > last {
					rows = rows[len(rows)-last:]
				}
				visibleDates := make(map[string]bool, len(rows))
				for _, row := range rows {
					visibleDates[row.Date] = true
				}
				costs, err := loadReportCosts(database, filter, func(row store.Usage) bool { return visibleDates[row.Date] })
				if err != nil {
					return err
				}
				return writeDailyReport(cmd, rows, costs)
			})
		},
	}
	addReportFlags(cmd, &flags)
	cmd.Flags().IntVar(&last, "last", 30, "show the most recent N active days")
	return cmd
}

func newMonthlyCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var flags reportFlags
	cmd := &cobra.Command{
		Use:   "monthly",
		Short: "Show token usage by month",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			return runReport(cmd, codexDir, claudeDir, timezone, flags.noSync, func(database *store.Store) error {
				rows, err := database.Monthly(filter)
				if err != nil {
					return err
				}
				costs, err := loadReportCosts(database, filter, nil)
				if err != nil {
					return err
				}
				return writeMonthlyReport(cmd, rows, costs)
			})
		},
	}
	addReportFlags(cmd, &flags)
	return cmd
}

func newModelsCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var flags reportFlags
	cmd := &cobra.Command{
		Use:   "models",
		Short: "Show token usage by model",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			return runReport(cmd, codexDir, claudeDir, timezone, flags.noSync, func(database *store.Store) error {
				rows, err := database.ByModel(filter)
				if err != nil {
					return err
				}
				totals, err := database.Totals(filter)
				if err != nil {
					return err
				}
				costs, err := loadReportCosts(database, filter, nil)
				if err != nil {
					return err
				}
				return writeModelsReport(cmd, rows, totals.Total, costs)
			})
		},
	}
	addReportFlags(cmd, &flags)
	return cmd
}

func addReportFlags(cmd *cobra.Command, flags *reportFlags) {
	cmd.Flags().StringVar(&flags.provider, "provider", "", "filter by provider (codex or claude)")
	cmd.Flags().StringVar(&flags.model, "model", "", "filter by exact model name")
	cmd.Flags().StringVar(&flags.since, "since", "", "include usage on or after YYYY-MM-DD")
	cmd.Flags().StringVar(&flags.until, "until", "", "include usage on or before YYYY-MM-DD")
	cmd.Flags().BoolVar(&flags.noSync, "no-sync", false, "report stored data without syncing first")
}

func (flags reportFlags) filter() (store.Filter, error) {
	var provider discover.Provider
	switch flags.provider {
	case "":
	case string(discover.ProviderCodex):
		provider = discover.ProviderCodex
	case string(discover.ProviderClaude):
		provider = discover.ProviderClaude
	default:
		return store.Filter{}, fmt.Errorf("invalid --provider %q (expected codex or claude)", flags.provider)
	}
	if err := validateDateFlag("since", flags.since); err != nil {
		return store.Filter{}, err
	}
	if err := validateDateFlag("until", flags.until); err != nil {
		return store.Filter{}, err
	}
	if flags.since != "" && flags.until != "" && flags.until < flags.since {
		return store.Filter{}, fmt.Errorf("--until must be on or after --since")
	}
	return store.Filter{Since: flags.since, Until: flags.until, Provider: provider, Model: flags.model}, nil
}

func validateDateFlag(name, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("invalid --%s %q (expected YYYY-MM-DD)", name, value)
	}
	return nil
}

func runReport(cmd *cobra.Command, codexDir, claudeDir, timezone *string, noSync bool, render func(*store.Store) error) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find user home directory: %w", err)
	}
	roots, err := discover.Resolve(discover.ResolveOptions{CodexDir: *codexDir, ClaudeDir: *claudeDir, Home: home, Getenv: os.Getenv})
	if err != nil {
		return err
	}
	stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
	if err != nil {
		return err
	}
	databasePath := filepath.Join(stateDir, store.DatabaseName)
	if _, err := os.Stat(databasePath); os.IsNotExist(err) && !anyRootExists(roots) {
		fmt.Fprintln(cmd.OutOrStdout(), noProviderHint)
		return nil
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat usage store: %w", err)
	}

	database, err := openReportStore(cmd, databasePath, roots, *timezone, noSync)
	if err != nil {
		return err
	}
	defer database.Close()
	return render(database)
}

func openReportStore(cmd *cobra.Command, databasePath string, roots []discover.Root, timezone string, noSync bool) (*store.Store, error) {
	if noSync {
		return store.Open(databasePath)
	}
	release, err := store.Lock(databasePath)
	if err != nil {
		writeSyncWarning(cmd, err)
		return store.Open(databasePath)
	}
	database, err := store.Open(databasePath)
	if err != nil {
		release()
		return nil, err
	}

	location := time.Local
	name := location.String()
	if timezone != "" {
		location, err = time.LoadLocation(timezone)
		if err != nil {
			release()
			database.Close()
			return nil, fmt.Errorf("load timezone %q: %w", timezone, err)
		}
		name = timezone
	}
	_, syncErr := syncer.Sync(syncer.Options{
		Store: database, Roots: roots, Location: location, Timezone: name,
		TimezoneFingerprint: timezoneFingerprint(location), LockHeld: true,
	})
	release()
	if syncErr != nil {
		writeSyncWarning(cmd, fmt.Errorf("sync usage: %w", syncErr))
	}
	return database, nil
}

func writeSyncWarning(cmd *cobra.Command, err error) {
	fmt.Fprintf(cmd.OutOrStdout(), "WARNING: %v; showing stored data.\n", err)
}

func anyRootExists(roots []discover.Root) bool {
	for _, root := range roots {
		if root.Exists {
			return true
		}
	}
	return false
}

func writeSummaryReport(cmd *cobra.Command, database *store.Store, filter store.Filter) error {
	totals, err := database.Totals(filter)
	if err != nil {
		return err
	}
	if totals.ActiveDays == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), noUsageMessage)
		return nil
	}
	models, err := database.ByModel(filter)
	if err != nil {
		return err
	}
	costs, err := loadReportCosts(database, filter, nil)
	if err != nil {
		return err
	}

	writer := cmd.OutOrStdout()
	fmt.Fprintln(writer, "Summary")
	fmt.Fprintf(writer, "Date range:  %s to %s\n", totals.FirstDate, totals.LastDate)
	fmt.Fprintf(writer, "Active days: %s\n", formatNumber(int64(totals.ActiveDays)))
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Tokens")
	writeTable(writer,
		[]string{"INPUT", "CACHE READ", "CACHE WRITE", "OUTPUT", "TOTAL"},
		[][]string{{formatNumber(totals.Input), formatNumber(totals.CacheRead), formatNumber(totals.CacheWrite), formatNumber(totals.Output), formatNumber(totals.Total)}},
		[]bool{true, true, true, true, true},
	)
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Providers")
	providerRows := make([][]string, 0, len(totals.Providers))
	for _, provider := range totals.Providers {
		providerRows = append(providerRows, []string{providerName(provider.Provider), formatNumber(provider.Input), formatNumber(provider.CacheRead), formatNumber(provider.CacheWrite), formatNumber(provider.Output), formatNumber(provider.Total)})
	}
	writeTable(writer, []string{"PROVIDER", "INPUT", "CACHE READ", "CACHE WRITE", "OUTPUT", "TOTAL"}, providerRows, []bool{false, true, true, true, true, true})
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Top models")
	topCount := 5
	if len(models) < topCount {
		topCount = len(models)
	}
	modelRows := make([][]string, 0, topCount)
	var unknownTokens int64
	for index, model := range models {
		if model.Model == "unknown" {
			unknownTokens += model.Total
		}
		if index < topCount {
			modelRows = append(modelRows, []string{providerName(model.Provider), model.Model, formatNumber(model.Total)})
		}
	}
	writeTable(writer, []string{"PROVIDER", "MODEL", "TOTAL"}, modelRows, []bool{false, false, true})
	if unknownTokens > 0 {
		fmt.Fprintf(writer, "Note: %s tokens are attributed to the unknown model.\n", formatNumber(unknownTokens))
	}

	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Cost")
	fmt.Fprintf(writer, "Total: %s\n", formatCost(costs.Grand))
	fmt.Fprintln(writer)
	providerCostRows := make([][]string, 0, len(totals.Providers))
	for _, provider := range totals.Providers {
		providerCostRows = append(providerCostRows, []string{providerName(provider.Provider), formatCost(costs.ByProvider[provider.Provider])})
	}
	writeTable(writer, []string{"PROVIDER", "COST"}, providerCostRows, []bool{false, true})
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Top models by cost")
	keys := make([]modelCostKey, 0, len(costs.ByModel))
	for key := range costs.ByModel {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		left, right := costs.ByModel[keys[i]], costs.ByModel[keys[j]]
		if left.Total != right.Total {
			return left.Total > right.Total
		}
		if keys[i].Provider != keys[j].Provider {
			return keys[i].Provider < keys[j].Provider
		}
		return keys[i].Model < keys[j].Model
	})
	if len(keys) > 5 {
		keys = keys[:5]
	}
	topCostRows := make([][]string, 0, len(keys))
	for _, key := range keys {
		topCostRows = append(topCostRows, []string{providerName(key.Provider), key.Model, formatCost(costs.ByModel[key])})
	}
	writeTable(writer, []string{"PROVIDER", "MODEL", "COST"}, topCostRows, []bool{false, false, true})
	writeCostNotes(cmd, costs)
	return nil
}

func writeDailyReport(cmd *cobra.Command, rows []store.DailyRow, costs reportCosts) error {
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), noUsageMessage)
		return nil
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, append(tokenRow(row.Date, row.TokenTotals), formatCost(costs.ByDate[row.Date])))
	}
	writeTable(cmd.OutOrStdout(), []string{"DATE", "INPUT", "CACHE READ", "CACHE WRITE", "OUTPUT", "TOTAL", "COST"}, tableRows, []bool{false, true, true, true, true, true, true})
	writeCostNotes(cmd, costs)
	return nil
}

func writeMonthlyReport(cmd *cobra.Command, rows []store.MonthlyRow, costs reportCosts) error {
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), noUsageMessage)
		return nil
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, append(tokenRow(row.Month, row.TokenTotals), formatCost(costs.ByMonth[row.Month])))
	}
	writeTable(cmd.OutOrStdout(), []string{"MONTH", "INPUT", "CACHE READ", "CACHE WRITE", "OUTPUT", "TOTAL", "COST"}, tableRows, []bool{false, true, true, true, true, true, true})
	writeCostNotes(cmd, costs)
	return nil
}

func writeModelsReport(cmd *cobra.Command, rows []store.ModelRow, grandTotal int64, costs reportCosts) error {
	if len(rows) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), noUsageMessage)
		return nil
	}
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		share := 0.0
		if grandTotal > 0 {
			share = float64(row.Total) / float64(grandTotal) * 100
		}
		modelCost := costs.ByModel[modelCostKey{Provider: row.Provider, Model: row.Model}]
		costShare := "—"
		if modelCost.PricedTokens > 0 && costs.Grand.Total > 0 {
			costShare = fmt.Sprintf("%.1f%%", float64(modelCost.Total)/float64(costs.Grand.Total)*100)
		}
		tableRows = append(tableRows, []string{
			providerName(row.Provider), row.Model, formatNumber(row.Input), formatNumber(row.CacheRead), formatNumber(row.CacheWrite), formatNumber(row.Output), formatNumber(row.Total),
			fmt.Sprintf("%.1f%%", share), formatNumber(int64(row.ActiveDays)), row.FirstDate + " to " + row.LastDate, formatCost(modelCost), costShare,
		})
	}
	writeTable(cmd.OutOrStdout(),
		[]string{"PROVIDER", "MODEL", "INPUT", "CACHE READ", "CACHE WRITE", "OUTPUT", "TOTAL", "SHARE", "DAYS", "DATE RANGE", "COST", "COST SHARE"},
		tableRows, []bool{false, false, true, true, true, true, true, true, true, false, true, true},
	)
	writeCostNotes(cmd, costs)
	return nil
}

func tokenRow(period string, totals store.TokenTotals) []string {
	return []string{period, formatNumber(totals.Input), formatNumber(totals.CacheRead), formatNumber(totals.CacheWrite), formatNumber(totals.Output), formatNumber(totals.Total)}
}

func writeTable(writer io.Writer, headers []string, rows [][]string, rightAligned []bool) {
	widths := make([]int, len(headers))
	for index, header := range headers {
		widths[index] = len(header)
	}
	for _, row := range rows {
		for index, value := range row {
			if len(value) > widths[index] {
				widths[index] = len(value)
			}
		}
	}
	writeTableRow(writer, headers, widths, rightAligned)
	for _, row := range rows {
		writeTableRow(writer, row, widths, rightAligned)
	}
}

func writeTableRow(writer io.Writer, row []string, widths []int, rightAligned []bool) {
	for index, value := range row {
		if index > 0 {
			fmt.Fprint(writer, "  ")
		}
		if rightAligned[index] {
			fmt.Fprintf(writer, "%*s", widths[index], value)
		} else if index == len(row)-1 {
			fmt.Fprint(writer, value)
		} else {
			fmt.Fprintf(writer, "%-*s", widths[index], value)
		}
	}
	fmt.Fprintln(writer)
}

func formatNumber(value int64) string {
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
