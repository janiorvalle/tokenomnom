package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historyfreshness "github.com/janiorvalle/tokenomnom/internal/history/freshness"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/skill"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	"github.com/janiorvalle/tokenomnom/internal/tui"
	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
	"github.com/janiorvalle/tokenomnom/internal/vault"
	"github.com/janiorvalle/tokenomnom/internal/version"
	"github.com/janiorvalle/tokenomnom/internal/xdg"
)

var runDashboardProgram = func(cmd *cobra.Command, model tui.Model) error {
	program := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	_, err := program.Run()
	return err
}

func runDashboard(cmd *cobra.Command, codexDir, claudeDir, timezone *string) error {
	render := theme.FromContext(cmd.Context())
	loader := newDashboardLoader(cmd, *codexDir, *claudeDir, *timezone, render)
	offer := newDashboardSkillOffer(*codexDir, *claudeDir)
	provider := tui.AllProviders
	switch appconfig.FromContext(cmd.Context()).Config.Reports.DefaultProvider {
	case "codex":
		provider = tui.CodexProvider
	case "claude":
		provider = tui.ClaudeProvider
	}
	return runDashboardProgram(cmd, tui.NewWithProvider(render, loader, offer, provider))
}

func newDashboardSkillOffer(codexDir, claudeDir string) tui.SkillOffer {
	return tui.SkillOffer{
		Check: func() (tui.SkillOfferCheck, error) {
			home, roots, err := resolveSkillRoots(codexDir, claudeDir)
			if err != nil {
				return tui.SkillOfferCheck{}, err
			}
			databasePath, err := skillOfferDatabasePath(home)
			if err != nil {
				return tui.SkillOfferCheck{}, err
			}
			database, err := store.Open(databasePath)
			if err != nil {
				return tui.SkillOfferCheck{}, err
			}
			defer database.Close()
			info, err := database.Info()
			if err != nil {
				return tui.SkillOfferCheck{}, err
			}
			check := tui.SkillOfferCheck{Answered: info.SkillOffer != ""}
			if check.Answered {
				return check, nil
			}
			for _, root := range roots {
				if !root.Exists {
					continue
				}
				check.HasRoots = true
				_, owned, exists, inspectErr := skill.Inspect(skill.Path(root.Path))
				if inspectErr != nil {
					return tui.SkillOfferCheck{}, inspectErr
				}
				check.Installed = check.Installed || owned && exists
			}
			return check, nil
		},
		Install: func() ([]string, error) {
			_, roots, err := resolveSkillRoots(codexDir, claudeDir)
			if err != nil {
				return nil, err
			}
			results, err := applySkills(roots, version.Version, false, false)
			if err != nil {
				return nil, err
			}
			lines := make([]string, 0, len(results))
			for _, result := range results {
				lines = append(lines, formatSkillResult(result))
			}
			return lines, nil
		},
		Record: func(choice tui.SkillOfferChoice) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("find user home directory: %w", err)
			}
			value := skill.OfferDeclined
			switch choice {
			case tui.SkillOfferAccepted:
				value = skill.OfferAccepted
			case tui.SkillOfferPreinstalled:
				value = skill.OfferPreinstalled
			}
			return setSkillOffer(home, value)
		},
	}
}

func newDashboardLoader(cmd *cobra.Command, codexDir, claudeDir, timezone string, render theme.Context) tui.Loader {
	var ambient dashboardAmbientCache
	var sessions dashboardSessionCache

	return func(request tui.Request) (tui.Snapshot, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return tui.Snapshot{}, fmt.Errorf("find user home directory: %w", err)
		}
		roots, err := resolveRoots(cmd, codexDir, claudeDir, home)
		if err != nil {
			return tui.Snapshot{}, err
		}
		stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
		if err != nil {
			return tui.Snapshot{}, err
		}
		databasePath := filepath.Join(stateDir, store.DatabaseName)
		var release func()
		if request.Sync {
			release, err = store.Lock(databasePath)
			if err != nil {
				return tui.Snapshot{}, err
			}
			defer release()
		}
		database, err := store.Open(databasePath)
		if err != nil {
			return tui.Snapshot{}, err
		}
		defer database.Close()

		location, timezoneName, err := dashboardTimezone(timezone)
		if err != nil {
			return tui.Snapshot{}, err
		}
		var syncSummary syncer.Summary
		var backupWarning string
		if request.Sync {
			syncSummary, err = syncer.Sync(syncer.Options{
				Store: database, Roots: roots, Location: location, Timezone: timezoneName,
				TimezoneFingerprint: timezoneFingerprint(location), LockHeld: true,
			})
			if err != nil {
				return tui.Snapshot{}, fmt.Errorf("sync usage: %w", err)
			}
			if err := runDueBackup(cmd, database); err != nil {
				backupWarning = fmt.Sprintf("backup usage: %v", err)
			}
			autoResult, autoErr := runDueAutoVault(cmd, database, roots)
			maintenance := autoVaultWarnings(autoResult, autoErr)
			if summary := autoVaultSummary(autoResult); summary != "" {
				maintenance = append([]string{summary}, maintenance...)
			}
			if len(maintenance) > 0 {
				if backupWarning != "" {
					backupWarning += "; "
				}
				backupWarning += strings.Join(maintenance, "; ")
			}
		}
		snapshot, err := dashboardSnapshot(database, request, render, location, syncSummary)
		snapshot.Sessions = sessions.snapshot(request, func() tuipages.SessionPageData {
			return loadDashboardHistory(filepath.Join(stateDir, historystore.DatabaseName), request, location)
		})
		warnings := []string{}
		if backupWarning != "" {
			warnings = append(warnings, backupWarning)
		}
		if snapshot.Sessions.Warning != "" {
			warnings = append(warnings, snapshot.Sessions.Warning)
		}
		snapshot.Warning = strings.Join(warnings, "; ")
		if err != nil {
			return snapshot, err
		}
		snapshot.StatusBar, snapshot.FilesScanned = ambient.snapshot(request, func() (tui.StatusBar, int) {
			filesScanned := syncSummary.FilesScanned
			if !request.Sync {
				filesScanned = countDashboardFiles(roots)
			}
			return dashboardStatusBar(cmd, database, stateDir, home, roots), filesScanned
		})
		return snapshot, err
	}
}

const (
	dashboardHistoryPageSize = 100
)

func loadDashboardHistory(path string, request tui.Request, location *time.Location) tuipages.SessionPageData {
	info, err := historystore.Inspect(path)
	if err != nil {
		return tuipages.SessionPageData{Warning: "History index unavailable; run tokenomnom history index to rebuild it."}
	}
	if !info.Exists {
		return tuipages.SessionPageData{}
	}
	database, err := historystore.OpenReadOnly(path)
	if err != nil {
		return tuipages.SessionPageData{Warning: "History index unavailable; run tokenomnom history index to rebuild it."}
	}
	defer database.Close()

	since, until := dashboardHistoryWindow(request.Range, location, time.Now())
	baseQuery := historystore.CatalogQuery{
		Provider: historyProvider(request.Provider),
		Since:    since, Until: until, Source: historystore.CatalogSourceAny,
	}
	query := baseQuery
	query.Project = request.SessionProject
	query.ProjectSet = request.SessionProjectActive
	query.Limit = dashboardHistoryPageSize
	query.Cursor = request.SessionCursor
	page, err := database.ListCatalog(query)
	if err != nil {
		return tuipages.SessionPageData{Warning: "History sessions could not be read; press R to retry or run tokenomnom history index."}
	}
	projects, err := database.ListCatalogProjects(baseQuery)
	if err != nil {
		return tuipages.SessionPageData{Warning: "History project filters could not be read; press R to retry or run tokenomnom history index."}
	}
	return tuipages.SessionPageData{
		Sessions: page.Sessions, Projects: tuipages.ProjectOptionsFromKeys(projects),
		HasMore: page.HasMore, NextCursor: page.NextCursor, IndexAvailable: true,
		Warning: strings.Join(uniqueStrings(page.Warnings), "; "), Location: location,
	}
}

func historyProvider(provider tui.Provider) history.Provider {
	switch provider {
	case tui.CodexProvider:
		return history.ProviderCodex
	case tui.ClaudeProvider:
		return history.ProviderClaude
	default:
		return ""
	}
}

func dashboardHistoryWindow(value tui.Range, location *time.Location, now time.Time) (*time.Time, *time.Time) {
	if value == tui.RangeAll {
		return nil, nil
	}
	if location == nil {
		location = time.Local
	}
	today := now.In(location)
	start := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, location)
	until := start.AddDate(0, 0, 1).Add(-time.Nanosecond)
	var since time.Time
	switch value {
	case tui.Range90Days:
		since = start.AddDate(0, 0, -89)
	case tui.RangeYear:
		since = start.AddDate(0, 0, 1).AddDate(-1, 0, 0)
	default:
		since = start.AddDate(0, 0, -29)
	}
	return &since, &until
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

// dashboardAmbientCache keeps facts that change on sync out of keypress loads.
// The mutex also serializes overlapping Bubble Tea commands during a refresh.
type dashboardAmbientCache struct {
	mu           sync.Mutex
	status       tui.StatusBar
	filesScanned int
	initialized  bool
}

func (cache *dashboardAmbientCache) snapshot(request tui.Request, refresh func() (tui.StatusBar, int)) (tui.StatusBar, int) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.initialized && !request.Sync {
		return cache.status, cache.filesScanned
	}
	cache.status, cache.filesScanned = refresh()
	cache.initialized = true
	return cache.status, cache.filesScanned
}

// dashboardSessionCache keeps history I/O out of loads that only change a
// report page or a selection. Sync is a refresh boundary: it bypasses the
// cached query and replaces it so the next keypress sees freshly indexed data.
type dashboardSessionCache struct {
	mu          sync.Mutex
	key         dashboardSessionCacheKey
	data        tuipages.SessionPageData
	initialized bool
}

type dashboardSessionCacheKey struct {
	provider      tui.Provider
	dateRange     tui.Range
	project       string
	projectActive bool
	cursor        string
}

func (cache *dashboardSessionCache) snapshot(request tui.Request, refresh func() tuipages.SessionPageData) tuipages.SessionPageData {
	key := dashboardSessionCacheKey{
		provider:      request.Provider,
		dateRange:     request.Range,
		project:       request.SessionProject,
		projectActive: request.SessionProjectActive,
		cursor:        request.SessionCursor,
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.initialized && !request.Sync && cache.key == key {
		return cache.data
	}
	cache.data = refresh()
	cache.key = key
	cache.initialized = true
	return cache.data
}

func countDashboardFiles(roots []discover.Root) int {
	count := 0
	for _, root := range roots {
		files, _ := discover.ListSourceFiles(root)
		count += len(files)
	}
	return count
}

func dashboardStatusBar(cmd *cobra.Command, database *store.Store, stateDir, home string, roots []discover.Root) tui.StatusBar {
	history := dashboardHistoryStatus(cmd, filepath.Join(stateDir, historystore.DatabaseName), roots)
	vaultStatus := dashboardVaultStatus(cmd, database, home, roots)
	return tui.StatusBar{History: history.Status, Vault: vaultStatus, Sessions: history.Sessions}
}

type dashboardHistorySnapshot struct {
	Status   tui.HistoryStatus
	Sessions int
}

func dashboardHistoryStatus(cmd *cobra.Command, path string, roots []discover.Root) dashboardHistorySnapshot {
	health, err := inspectHistoryHealth(path)
	if err != nil {
		return dashboardHistorySnapshot{Status: tui.HistoryStatus{Hint: "unavailable"}}
	}
	if !health.Exists {
		return dashboardHistorySnapshot{Status: tui.HistoryStatus{Hint: "not indexed"}}
	}

	drift := historyfreshness.Probe(path, configuredHistoryRoots(cmd, roots), nil)
	status := tui.HistoryStatus{Exists: true}
	switch {
	case health.InspectionError != "" || health.ErrorSources > 0 || health.LastRunErrorCount > 0 || len(drift.Warnings) > 0:
		status.Hint = "needs attention"
	case health.LastIndexUnix == 0:
		status.Hint = "pending"
	case health.StaleSources > 0 || drift.SettledChangedSources > 0:
		status.Hint = "stale"
	default:
		status.Fresh = true
	}
	return dashboardHistorySnapshot{Status: status, Sessions: health.Sessions}
}

func dashboardVaultStatus(cmd *cobra.Command, database *store.Store, home string, roots []discover.Root) tui.VaultStatus {
	cfg := appconfig.FromContext(cmd.Context()).Config
	dir, err := configuredVaultDir(cfg, home)
	if err != nil {
		return tui.VaultStatus{Hint: "unavailable"}
	}
	providers := make([]discover.Provider, 0, len(cfg.Vault.Providers))
	for _, provider := range cfg.Vault.Providers {
		providers = append(providers, discover.Provider(provider))
	}
	minAge, err := time.ParseDuration(cfg.Vault.MinAge)
	if err != nil {
		return tui.VaultStatus{Hint: "unavailable"}
	}
	instance, err := vault.New(vault.Options{Dir: dir, Store: database, Roots: roots, Providers: providers, MinAge: minAge})
	if err != nil {
		return tui.VaultStatus{Hint: "unavailable"}
	}
	readiness, err := instance.Readiness()
	if err != nil {
		return tui.VaultStatus{Hint: "unavailable"}
	}
	if !readiness.Initialized {
		return tui.VaultStatus{Hint: "not initialized"}
	}
	return tui.VaultStatus{
		Exists:           true,
		Files:            readiness.Status.Files,
		RawBytes:         readiness.Status.RawBytes,
		StoredBytes:      readiness.Status.StoredBytes,
		CompressionRatio: readiness.Status.Ratio,
	}
}

func dashboardTimezone(value string) (*time.Location, string, error) {
	if value == "" {
		return time.Local, localTimezoneName(), nil
	}
	location, err := time.LoadLocation(value)
	if err != nil {
		return nil, "", fmt.Errorf("load timezone %q: %w", value, err)
	}
	return location, value, nil
}

func dashboardSnapshot(database *store.Store, request tui.Request, render theme.Context, location *time.Location, syncSummary syncer.Summary) (tui.Snapshot, error) {
	info, err := database.Info()
	if err != nil {
		return tui.Snapshot{}, err
	}
	filter := dashboardFilter(request, time.Now().In(location))
	totals, err := database.Totals(filter)
	if err != nil {
		return tui.Snapshot{}, err
	}
	models, err := database.ByModel(filter)
	if err != nil {
		return tui.Snapshot{}, err
	}
	costs, err := loadReportCosts(database, filter, nil)
	if err != nil {
		return tui.Snapshot{}, err
	}

	render.Width = tui.ContentWidth(request.Width)
	snapshot := tui.Snapshot{Empty: info.UsageRows == 0, FilesScanned: syncSummary.FilesScanned, SyncDuration: syncSummary.Duration}
	snapshot.Summary = dashboardSummary(totals, costs)
	snapshot.Views[tui.DailyTab], err = dashboardDailyView(database, filter, costs, request, render)
	if err != nil {
		return tui.Snapshot{}, err
	}
	snapshot.Views[tui.MonthlyTab], err = dashboardMonthlyView(database, filter, costs, request, render)
	if err != nil {
		return tui.Snapshot{}, err
	}
	snapshot.Views[tui.ModelsTab] = dashboardModelsView(models, costs, request, render)
	snapshot.Views[tui.HeatmapTab], err = dashboardHeatmapView(database, filter, request, render, location)
	if err != nil {
		return tui.Snapshot{}, err
	}
	return snapshot, nil
}

func dashboardSummary(totals store.TotalsResult, costs reportCosts) tui.Summary {
	average, peak := "—", "—"
	if totals.ActiveDays > 0 && costs.Grand.PricedTokens > 0 {
		average = formatUSD(costs.Grand.Total / pricing.Money(totals.ActiveDays))
		if peakCost, ok := peakDailyCost(costs.ByDate); ok {
			peak = formatUSD(peakCost)
		}
	}
	return tui.Summary{Metrics: [5]tui.SummaryMetric{
		{Value: formatCost(costs.Grand), Kind: tui.MetricMoney},
		{Value: formatNumber(totals.Total)},
		{Value: formatNumber(int64(totals.ActiveDays))},
		{Value: average, Kind: tui.MetricMoney},
		{Value: peak, Kind: tui.MetricMoney},
	}}
}

func peakDailyCost(byDate map[string]aggregateCost) (pricing.Money, bool) {
	var peak pricing.Money
	found := false
	for _, cost := range byDate {
		if cost.PricedTokens == 0 {
			continue
		}
		if !found || cost.Total > peak {
			peak, found = cost.Total, true
		}
	}
	return peak, found
}

func dashboardFilter(request tui.Request, now time.Time) store.Filter {
	filter := store.Filter{}
	switch request.Provider {
	case tui.CodexProvider:
		filter.Provider = discover.ProviderCodex
	case tui.ClaudeProvider:
		filter.Provider = discover.ProviderClaude
	}
	today := dateOnly(now)
	switch request.Range {
	case tui.Range30Days:
		filter.Since = today.AddDate(0, 0, -29).Format(heatmapDateLayout)
	case tui.Range90Days:
		filter.Since = today.AddDate(0, 0, -89).Format(heatmapDateLayout)
	case tui.RangeYear:
		filter.Since = today.AddDate(-1, 0, 1).Format(heatmapDateLayout)
	}
	filter.Until = today.Format(heatmapDateLayout)
	return filter
}

func dashboardDailyView(database *store.Store, filter store.Filter, costs reportCosts, request tui.Request, render theme.Context) (string, error) {
	rows, err := database.Daily(filter)
	if err != nil {
		return "", err
	}
	rows = windowDailyRows(rows, request.DailyOffset, dashboardRowCapacity(request.Height))
	periods := make([]chartPeriod, 0, len(rows))
	for _, row := range rows {
		periods = append(periods, chartPeriod{label: row.Date, values: costs.ByDateProvider[row.Date]})
	}
	chart := renderPeriodChart(render, periods, "day", "days", chartUsesTokens(costs))
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, []string{row.Date, formatNumber(row.Total), formatCost(costs.ByDate[row.Date])})
	}
	return chart + renderStyledTable(render, []string{"DATE", "TOKENS", "COST"}, tableRows, []bool{false, true, true}, tableStyle{moneyColumns: map[int]bool{2: true}}), nil
}

func dashboardMonthlyView(database *store.Store, filter store.Filter, costs reportCosts, request tui.Request, render theme.Context) (string, error) {
	rows, err := database.Monthly(filter)
	if err != nil {
		return "", err
	}
	rows = windowMonthlyRows(rows, request.MonthlyOffset, dashboardRowCapacity(request.Height))
	periods := make([]chartPeriod, 0, len(rows))
	for _, row := range rows {
		periods = append(periods, chartPeriod{label: row.Month, values: costs.ByMonthProvider[row.Month]})
	}
	chart := renderPeriodChart(render, periods, "month", "months", chartUsesTokens(costs))
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, []string{row.Month, formatNumber(row.Total), formatCost(costs.ByMonth[row.Month])})
	}
	return chart + renderStyledTable(render, []string{"MONTH", "TOKENS", "COST"}, tableRows, []bool{false, true, true}, tableStyle{moneyColumns: map[int]bool{2: true}}), nil
}

func dashboardModelsView(rows []store.ModelRow, costs reportCosts, request tui.Request, render theme.Context) string {
	rows = append([]store.ModelRow(nil), rows...)
	sort.SliceStable(rows, func(i, j int) bool {
		leftKey := modelCostKey{Provider: rows[i].Provider, Model: rows[i].Model}
		rightKey := modelCostKey{Provider: rows[j].Provider, Model: rows[j].Model}
		switch request.ModelSort {
		case 1:
			return costs.ByModel[leftKey].Total > costs.ByModel[rightKey].Total
		case 2:
			return strings.ToLower(rows[i].Model) < strings.ToLower(rows[j].Model)
		default:
			return rows[i].Total > rows[j].Total
		}
	})
	capacity := dashboardRowCapacity(request.Height) + 5
	start := min(max(0, request.ModelOffset), max(0, len(rows)-1))
	end := min(len(rows), start+capacity)
	rows = rows[start:end]
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		model := row.Model
		if len([]rune(model)) > 28 {
			model = string([]rune(model)[:27]) + "…"
		}
		tableRows = append(tableRows, []string{providerName(row.Provider), model, formatNumber(row.Total), formatCost(costs.ByModel[modelCostKey{Provider: row.Provider, Model: row.Model}])})
	}
	return renderStyledTable(render, []string{"PROVIDER", "MODEL", "TOKENS", "COST"}, tableRows, []bool{false, false, true, true}, tableStyle{hasProvider: true, providerCol: 0, moneyColumns: map[int]bool{3: true}})
}

func dashboardHeatmapView(database *store.Store, globalFilter store.Filter, request tui.Request, render theme.Context, location *time.Location) (string, error) {
	today := dateOnly(time.Now().In(location)).AddDate(0, request.HeatmapOffset, 0)
	window, err := heatmapDateWindow(0, today)
	if request.HeatmapYear {
		window, err = heatmapDateWindow(today.Year(), today)
	}
	if err != nil {
		return "", err
	}
	rows, err := database.Daily(globalFilter)
	if err != nil {
		return "", err
	}
	costs, err := loadReportCosts(database, globalFilter, func(row store.Usage) bool {
		return row.Date >= window.From.Format(heatmapDateLayout) && row.Date <= window.To.Format(heatmapDateLayout)
	})
	if err != nil {
		return "", err
	}
	filteredRows := rows[:0]
	for _, row := range rows {
		if row.Date >= window.From.Format(heatmapDateLayout) && row.Date <= window.To.Format(heatmapDateLayout) {
			filteredRows = append(filteredRows, row)
		}
	}
	return renderHeatmap(render, buildHeatmapReport(window, filteredRows, costs)), nil
}

func dashboardRowCapacity(height int) int {
	if height <= 0 {
		return 8
	}
	return max(3, min(10, tui.ContentHeight(height)-10))
}

func windowDailyRows(rows []store.DailyRow, offset, capacity int) []store.DailyRow {
	if len(rows) == 0 {
		return rows
	}
	end := min(len(rows), max(min(capacity, len(rows)), len(rows)+offset))
	start := max(0, end-capacity)
	return rows[start:end]
}

func windowMonthlyRows(rows []store.MonthlyRow, offset, capacity int) []store.MonthlyRow {
	if len(rows) == 0 {
		return rows
	}
	end := min(len(rows), max(min(capacity, len(rows)), len(rows)+offset))
	start := max(0, end-capacity)
	return rows[start:end]
}
