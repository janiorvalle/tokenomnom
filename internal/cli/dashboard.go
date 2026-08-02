package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	program := tea.NewProgram(&model, tea.WithAltScreen(), tea.WithInput(cmd.InOrStdin()), tea.WithOutput(cmd.OutOrStdout()))
	model.SetProgressSink(func(request tui.Request, generation uint64, progress tui.LoadProgress) {
		program.Send(tui.ProgressMsg{Request: request, Generation: generation, Progress: progress})
	})
	_, err := program.Run()
	return err
}

func runDashboard(cmd *cobra.Command, codexDir, claudeDir, timezone *string) error {
	render := theme.FromContext(cmd.Context())
	loader := newDashboardLoader(cmd, *codexDir, *claudeDir, *timezone, render)
	offer := newDashboardSkillOffer(*codexDir, *claudeDir)
	commands := newDashboardCommandRegistry(cmd, *codexDir, *claudeDir)
	provider := tui.AllProviders
	switch appconfig.FromContext(cmd.Context()).Config.Reports.DefaultProvider {
	case "codex":
		provider = tui.CodexProvider
	case "claude":
		provider = tui.ClaudeProvider
	}
	historyPage := newHistorySearchPage(cmd, *codexDir, *claudeDir)
	return runDashboardProgram(cmd, tui.NewWithProviderAndPagesAndCommands(render, loader, offer, provider, commands, historyPage, tui.NewVaultPage(), tui.NewSystemPage()))
}

func newDashboardCommandRegistry(parent *cobra.Command, codexDir, claudeDir string) tui.CommandRegistry {
	return tui.CommandRegistry{Actions: []tui.CommandAction{
		{ID: tui.CommandVaultVerifyID, Run: func() (tui.CommandResult, error) {
			return runDashboardSubcommand(parent, "vault", "verify", "--codex-dir", codexDir, "--claude-dir", claudeDir)
		}},
		{ID: tui.CommandHistoryIndexID, Run: func() (tui.CommandResult, error) {
			return runDashboardSubcommand(parent, "history", "index", "--codex-dir", codexDir, "--claude-dir", claudeDir)
		}},
		{ID: tui.CommandPricingID, Run: func() (tui.CommandResult, error) {
			return runDashboardSubcommand(parent, "pricing")
		}},
	}}
}

func runDashboardSubcommand(parent *cobra.Command, args ...string) (tui.CommandResult, error) {
	command := NewRootCommand()
	command.SetContext(parent.Context())
	command.SetIn(parent.InOrStdin())
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(args)
	err := command.Execute()
	return tui.CommandResult{Output: output.String()}, err
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
	var pageCache dashboardPageCache

	return func(request tui.Request) (tui.Snapshot, error) {
		home, err := os.UserHomeDir()
		if err != nil {
			return tui.Snapshot{}, fmt.Errorf("find user home directory: %w", err)
		}
		stateDir, err := xdg.StateDir(xdg.Options{Home: home, Getenv: os.Getenv})
		if err != nil {
			return tui.Snapshot{}, err
		}
		databasePath := filepath.Join(stateDir, store.DatabaseName)
		location, timezoneName, err := dashboardTimezone(timezone)
		if err != nil {
			return tui.Snapshot{}, err
		}
		var database *store.Store
		var release func()
		if request.Initial && !request.Sync {
			// Existing usage is safe to read while a sync owns the writer
			// lock. A missing or uninitialized store is handed to the writer
			// before the sync can create its first snapshot.
			database, err = store.OpenReadOnly(databasePath)
			if errors.Is(err, os.ErrNotExist) {
				release, err = store.Lock(databasePath)
				if err == nil {
					database, err = store.Open(databasePath)
				}
			} else if errors.Is(err, store.ErrStoreNeedsMigration) || errors.Is(err, store.ErrStoreNeedsInitialization) {
				release, err = store.Lock(databasePath)
				if err == nil {
					database, err = store.Open(databasePath)
				}
			}
		} else {
			if request.Sync {
				release, err = store.Lock(databasePath)
				if err != nil {
					return tui.Snapshot{}, err
				}
			}
			database, err = store.Open(databasePath)
		}
		if err != nil {
			if release != nil {
				release()
			}
			return tui.Snapshot{}, err
		}
		if release != nil {
			defer release()
		}
		defer database.Close()
		if request.Initial && !request.Sync {
			snapshot, err := dashboardSnapshot(database, request, render, location, syncer.Summary{})
			if err != nil {
				return tui.Snapshot{}, err
			}
			snapshot.Sessions.Pending = true
			snapshot.StatusBar = dashboardPendingStatusBar()
			return snapshot, nil
		}

		roots, err := resolveRoots(cmd, codexDir, claudeDir, home)
		if err != nil {
			return tui.Snapshot{}, err
		}
		var syncSummary syncer.Summary
		var backupWarning string
		if request.Sync {
			syncSummary, err = syncer.Sync(syncer.Options{
				Store: database, Roots: roots, Location: location, Timezone: timezoneName,
				TimezoneFingerprint: timezoneFingerprint(location), Full: request.FullSync, LockHeld: true,
				Progress: func(progress syncer.Progress) {
					if request.Progress == nil {
						return
					}
					report := *request.Progress
					report(tui.LoadProgress{
						Phase:          string(progress.Phase),
						FilesFound:     progress.FilesFound,
						FilesProcessed: progress.FilesProcessed,
					})
				},
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
		var actionStatus, actionWarning string
		if request.Action == tui.VerifyVaultAction {
			result, verifyErr := verifyDashboardVault(cmd, codexDir, claudeDir)
			actionStatus, actionWarning, err = dashboardVaultVerificationStatus(result, verifyErr)
			if err != nil {
				return tui.Snapshot{}, err
			}
		}
		snapshot, err := dashboardSnapshot(database, request, render, location, syncSummary)
		snapshot.Sessions = sessions.snapshot(request, func() tuipages.SessionPageData {
			return loadDashboardHistory(filepath.Join(stateDir, historystore.DatabaseName), request, location)
		})
		if request.Ledger.ExpandedDay != "" {
			snapshot.Ledger = loadDashboardLedgerSessions(cmd, filepath.Join(stateDir, historystore.DatabaseName), snapshot.Ledger, request, location, codexDir, claudeDir)
		}
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
		pageCache.Lock()
		if request.RefreshPages || request.Action != "" || !pageCache.loaded {
			pageCache.vault, pageCache.system = dashboardPageData(cmd, roots, databasePath, timezone)
			pageCache.loaded = true
		}
		snapshot.Vault, snapshot.System = pageCache.vault, pageCache.system
		pageCache.Unlock()
		snapshot.ActionStatus = actionStatus
		snapshot.ActionWarning = actionWarning
		return snapshot, err
	}
}

func dashboardPendingStatusBar() tui.StatusBar {
	return tui.StatusBar{
		History: tui.HistoryStatus{Hint: "pending"},
		Vault:   tui.VaultStatus{Hint: "pending"},
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

func loadDashboardLedgerSessions(cmd *cobra.Command, path string, data tuipages.Data, request tui.Request, location *time.Location, codexDir, claudeDir string) tuipages.Data {
	day := request.Ledger.ExpandedDay
	data.SessionDay = day
	data.SessionPageCursor = request.Ledger.SessionPageCursor
	data.Location = location
	info, err := historystore.Inspect(path)
	if err != nil {
		data.SessionWarning = "History index unavailable; run tokenomnom history index to rebuild it."
		data.SessionDataUnavailable = true
		return data
	}
	if !info.Exists {
		return data
	}
	database, err := historystore.OpenReadOnly(path)
	if err != nil {
		data.SessionWarning = "History index unavailable; run tokenomnom history index to rebuild it."
		data.SessionDataUnavailable = true
		return data
	}
	defer database.Close()
	data.SessionIndexAvailable = true

	if location == nil {
		location = time.Local
	}
	start, err := time.ParseInLocation("2006-01-02", day, location)
	if err != nil {
		data.SessionWarning = fmt.Sprintf("The selected ledger day %q is invalid; collapse the row and select another day.", day)
		data.SessionDataUnavailable = true
		return data
	}
	end := start.AddDate(0, 0, 1).Add(-time.Nanosecond)
	page, err := database.ListSessionCostSources(historystore.SessionCostQuery{Catalog: historystore.CatalogQuery{
		Provider: historyProvider(request.Provider), Since: &start, Until: &end,
		Source: historystore.CatalogSourceAny, Limit: historystore.DefaultSessionCostPageSize, Cursor: request.Ledger.SessionPageCursor,
	}})
	if err != nil {
		data.SessionWarning = "Indexed sessions for this day could not be read; press R to retry or run tokenomnom history index."
		data.SessionDataUnavailable = true
		return data
	}
	table, err := loadPricingTable()
	if err != nil {
		data.SessionWarning = "Session costs could not be priced; check the pricing override and press R to retry."
		data.SessionDataUnavailable = true
		return data
	}

	data.SessionsHaveMore = page.Page.HasMore
	data.SessionsNextCursor = page.Page.NextCursor
	data.Sessions = make([]tuipages.LedgerSession, 0, len(page.Sessions))
	unavailable := 0
	for _, session := range page.Sessions {
		row, priceErr := priceHistorySessionForWindow(cmd, session, table, codexDir, claudeDir, start, end)
		if priceErr != nil {
			row = historySessionCostRow{CatalogSession: session.CatalogSession, AttributionStatus: "unavailable"}
		}
		if row.AttributionStatus == "unavailable" {
			unavailable++
		}
		data.Sessions = append(data.Sessions, tuipages.LedgerSession{
			CatalogSession: row.CatalogSession,
			Tokens:         row.Tokens.TotalTokens, Cost: row.Tokens.cost,
			PricedTokens: row.Tokens.PricedTokens, UnpricedTokens: row.Tokens.UnpricedTokens,
			AttributionStatus: row.AttributionStatus, ActivityTimestamp: row.attributionTimestamp,
			Warning: strings.Join(uniqueStrings(row.Warnings), "; "),
		})
	}
	warnings := uniqueStrings(page.Warnings)
	if unavailable > 0 {
		warnings = append(warnings, fmt.Sprintf("Cost attribution is unavailable for %d of %d sessions.", unavailable, len(data.Sessions)))
	}
	data.SessionWarning = strings.Join(warnings, "; ")
	return data
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

// dashboardHistorySearchCache keeps repeated page visits from reopening the
// history index. Sync is the refresh boundary so a search can still see newly
// indexed prompts after an explicit refresh.
type dashboardHistorySearchCache struct {
	mu          sync.Mutex
	key         dashboardHistorySearchCacheKey
	data        tuipages.HistorySearchData
	err         error
	initialized bool
}

// dashboardHistoryFreshnessCache keeps the metadata-only source walk on the
// session boundary instead of repeating it for every new search query.
type dashboardHistoryFreshnessCache struct {
	mu          sync.Mutex
	warnings    []string
	initialized bool
}

func (cache *dashboardHistoryFreshnessCache) snapshot(force bool, probe func() []string) []string {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.initialized && !force {
		return append([]string(nil), cache.warnings...)
	}
	cache.warnings = append([]string(nil), probe()...)
	cache.initialized = true
	return append([]string(nil), cache.warnings...)
}

type dashboardHistorySearchCacheKey struct {
	query     string
	sessionID string
	provider  tui.Provider
	dateRange tui.Range
}

func (cache *dashboardHistorySearchCache) snapshot(request tui.Request, refresh func() (tuipages.HistorySearchData, error)) (tuipages.HistorySearchData, error) {
	key := dashboardHistorySearchCacheKey{
		query: request.HistoryQuery, sessionID: request.HistorySessionID,
		provider: request.Provider, dateRange: request.Range,
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.initialized && !request.Sync && cache.key == key {
		return cache.data, cache.err
	}
	data, err := refresh()
	if err != nil {
		return data, err
	}
	if data.NotIndexed {
		cache.data, cache.err, cache.key, cache.initialized = tuipages.HistorySearchData{}, nil, dashboardHistorySearchCacheKey{}, false
		return data, nil
	}
	cache.data, cache.err, cache.key, cache.initialized = data, nil, key, true
	return cache.data, nil
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

type dashboardPageCache struct {
	sync.Mutex
	loaded bool
	vault  tuipages.VaultPageData
	system tuipages.SystemPageData
}

func verifyDashboardVault(cmd *cobra.Command, codexDir, claudeDir string) (vault.VerifyResult, error) {
	instance, database, err := openVault(cmd, codexDir, claudeDir)
	if err != nil {
		return vault.VerifyResult{}, err
	}
	defer database.Close()
	return instance.Verify(true)
}

func dashboardVaultVerificationStatus(result vault.VerifyResult, verifyErr error) (string, string, error) {
	if verifyErr != nil && len(result.Failures) == 0 {
		return "", "", fmt.Errorf("verify vault: %w", verifyErr)
	}
	if len(result.Failures) > 0 {
		warning := fmt.Sprintf("vault verification failed for %d file(s)", len(result.Failures))
		if verifyErr != nil && verifyErr.Error() != warning {
			warning += ": " + verifyErr.Error()
		}
		return fmt.Sprintf("vault verification failed · %d/%d verified", result.Verified, result.Checked), warning, nil
	}
	return fmt.Sprintf("vault verified · %d files checked", result.Checked), "", nil
}

func dashboardPageData(cmd *cobra.Command, roots []discover.Root, databasePath, timezone string) (tuipages.VaultPageData, tuipages.SystemPageData) {
	doctor, _, warnings, doctorErr := collectDoctorData(cmd, roots, databasePath, timezone)
	table, pricingErr := loadPricingTable()
	if doctorErr != nil {
		warnings = append(warnings, "System health data could not be loaded; spend pages remain available.")
		if pricingErr != nil {
			warnings = append(warnings, "Pricing data could not be loaded; other dashboard pages remain available.")
		}
		page := dashboardSystemPageData(doctor, table, warnings)
		if pricingErr != nil {
			page.Pricing = nil
			page.PricingDisclaimer = ""
		}
		vaultPage := tuipages.VaultPageData{}
		if doctor.Vault.Dir != "" {
			vaultPage = dashboardVaultPageData(doctor.Vault)
		}
		return vaultPage, page
	}
	if pricingErr != nil {
		warnings = append(warnings, "Pricing data could not be loaded; other dashboard pages remain available.")
	}
	return dashboardVaultPageData(doctor.Vault), dashboardSystemPageData(doctor, table, warnings)
}

func dashboardVaultPageData(data jsonDoctorVault) tuipages.VaultPageData {
	ratio := "-"
	if data.StoredBytes > 0 {
		ratio = fmt.Sprintf("%.2fx", float64(data.RawBytes)/float64(data.StoredBytes))
	}
	verified := "not checked"
	verificationState := tuipages.FindingMuted
	if data.LastDeepVerification != nil {
		verificationState = tuipages.FindingOK
		verified = "yes · " + *data.LastDeepVerification
		if data.LastArchive != nil && verificationIsOlderThanArchive(*data.LastDeepVerification, *data.LastArchive) {
			verificationState = tuipages.FindingWarning
			verified = "stale · " + *data.LastDeepVerification
		}
		if data.KnownBrokenBundles > 0 {
			verificationState = tuipages.FindingWarning
			verified = fmt.Sprintf("issues · %s", *data.LastDeepVerification)
		}
	}
	format := "not initialized"
	if data.Format != nil {
		format = fmt.Sprintf("v%d, %s", *data.Format, stringValue(data.Encryption))
	}
	return tuipages.VaultPageData{
		Directory:          data.Dir,
		Initialized:        data.Initialized,
		Format:             format,
		Files:              data.Files,
		RawSize:            humanBytes(data.RawBytes),
		StoredSize:         humanBytes(data.StoredBytes),
		Ratio:              ratio,
		Verified:           verified,
		VerificationState:  verificationState,
		LastArchive:        stringValue(data.LastArchive),
		LastVerification:   stringValue(data.LastDeepVerification),
		KnownBrokenBundles: data.KnownBrokenBundles,
		Reclaimable:        humanBytes(data.ReclaimableBytes),
	}
}

func verificationIsOlderThanArchive(verification, archive string) bool {
	verifiedAt, verifiedErr := time.Parse(time.RFC3339, verification)
	archivedAt, archiveErr := time.Parse(time.RFC3339, archive)
	return verifiedErr == nil && archiveErr == nil && archivedAt.After(verifiedAt)
}

func dashboardSystemPageData(data jsonDoctorData, table pricing.Table, warnings []string) tuipages.SystemPageData {
	findings := make([]tuipages.SystemFinding, 0, len(data.Providers)+5)
	for _, provider := range data.Providers {
		state := tuipages.FindingMuted
		value := "not found"
		if provider.Exists {
			state = tuipages.FindingOK
			value = fmt.Sprintf("ready · %d files · %s", provider.JSONLFiles, humanBytes(provider.TotalBytes))
			if len(provider.WalkErrors) > 0 {
				state = tuipages.FindingWarning
				value = fmt.Sprintf("%d walk error(s)", len(provider.WalkErrors))
			}
		}
		findings = append(findings, tuipages.SystemFinding{Name: providerName(discover.Provider(provider.Provider)), Value: value, State: state})
	}

	storeState := tuipages.FindingMuted
	storeValue := "not created"
	if data.Store.Exists {
		storeState = tuipages.FindingOK
		storeValue = fmt.Sprintf("ready · %d rows · %d models · %s", data.Store.UsageRows, data.Store.DistinctModels, humanBytes(data.Store.SizeBytes))
		if data.Store.MissingFiles > 0 {
			storeState = tuipages.FindingWarning
			storeValue = fmt.Sprintf("%d missing transcript(s)", data.Store.MissingFiles)
		}
	}
	findings = append(findings, tuipages.SystemFinding{Name: "Store", Value: storeValue, State: storeState})

	historyState := tuipages.FindingMuted
	historyValue := "not indexed"
	if data.History.Status != "" {
		historyState = dashboardFindingState(data.History.Status)
		historyValue = fmt.Sprintf("%s · %d sessions · %d prompts", data.History.Status, data.History.LogicalSessions, data.History.LogicalPrompts)
	}
	findings = append(findings, tuipages.SystemFinding{Name: "History", Value: historyValue, State: historyState})

	vaultState := tuipages.FindingMuted
	vaultValue := "not initialized"
	if data.Vault.Initialized {
		vaultState = tuipages.FindingOK
		vaultValue = fmt.Sprintf("ready · %d files · %s raw · %s stored", data.Vault.Files, humanBytes(data.Vault.RawBytes), humanBytes(data.Vault.StoredBytes))
		if data.Vault.KnownBrokenBundles > 0 || data.Vault.SettledUnvaultedSources > 0 {
			vaultState = tuipages.FindingWarning
			vaultValue = fmt.Sprintf("%d broken · %d settled unvaulted", data.Vault.KnownBrokenBundles, data.Vault.SettledUnvaultedSources)
		}
	}
	findings = append(findings, tuipages.SystemFinding{Name: "Vault", Value: vaultValue, State: vaultState})

	scheduleState := tuipages.FindingMuted
	scheduleValue := "not installed"
	if data.Schedule.Installed {
		scheduleState = tuipages.FindingOK
		scheduleValue = "installed"
		if data.Schedule.IntervalDrift || !data.Schedule.DefinitionExists || !data.Schedule.BinaryExists {
			scheduleState = tuipages.FindingWarning
			scheduleValue = "installed · needs refresh"
		}
	}
	findings = append(findings, tuipages.SystemFinding{Name: "Schedule", Value: scheduleValue, State: scheduleState})

	return tuipages.SystemPageData{Findings: findings, Warnings: append([]string(nil), warnings...), Pricing: dashboardPricingRows(table), PricingDisclaimer: pricingDisclaimer}
}

func dashboardPricingRows(table pricing.Table) []tuipages.PricingRow {
	rows := make([]tuipages.PricingRow, 0, len(table.Entries()))
	for _, entry := range table.Entries() {
		override := ""
		if table.IsOverridden(entry.Model) {
			override = "override"
		}
		rows = append(rows, tuipages.PricingRow{
			Model: entry.Model, BaseInput: pricing.FormatRate(entry.BaseInput), CacheRead: pricing.FormatRate(entry.CacheRead),
			Write5m: pricing.FormatRate(entry.Write5m), Write1h: pricing.FormatRate(entry.Write1h), Output: pricing.FormatRate(entry.Output),
			Status: entry.Status, Effective: effectiveWindow(entry), Source: entry.Source, Override: override,
		})
	}
	return rows
}

func dashboardFindingState(value string) tuipages.FindingState {
	lower := strings.ToLower(value)
	switch {
	case strings.Contains(lower, "warn"), strings.Contains(lower, "stale"), strings.Contains(lower, "error"):
		return tuipages.FindingWarning
	case strings.Contains(lower, "not"), lower == "missing":
		return tuipages.FindingMuted
	default:
		return tuipages.FindingOK
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
	pricingTable, err := loadPricingTable()
	if err != nil {
		return tui.Snapshot{}, err
	}
	costs, err := loadReportCostsWithTable(database, filter, nil, pricingTable)
	if err != nil {
		return tui.Snapshot{}, err
	}
	dailyRows, err := database.Daily(filter)
	if err != nil {
		return tui.Snapshot{}, err
	}

	ledgerFilter := filter
	ledgerFilter.Since = ""
	ledgerFilter.Until = ""
	ledgerCosts, err := loadReportCosts(database, ledgerFilter, nil)
	if err != nil {
		return tui.Snapshot{}, err
	}

	render.Width = tui.ContentWidth(request.Width)
	snapshot := tui.Snapshot{
		Empty: info.UsageRows == 0, FilesScanned: syncSummary.FilesScanned, SyncDuration: syncSummary.Duration,
		DailyCursor:    normalizedDailyCursor(dailyRows, request.DailyCursor),
		DailyCursorMax: max(0, len(dailyRows)-1),
	}
	snapshot.Summary = dashboardSummary(totals, costs)
	dailyView, err := dashboardDailyView(database, dailyRows, filter, costs, pricingTable, request, render)
	if err != nil {
		return tui.Snapshot{}, err
	}
	snapshot.Views[tui.DailyTab] = dailyView.view
	snapshot.DailyWindowStart = dailyView.windowStart
	snapshot.DailyDetailOffset = dailyView.detailOffset
	snapshot.DailyDetailMaxOffset = dailyView.detailMaxOffset
	snapshot.Ledger, err = dashboardLedgerData(database, ledgerFilter, ledgerCosts, request)
	if err != nil {
		return tui.Snapshot{}, err
	}
	snapshot.Ledger.Location = location
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

const (
	dailyDetailSideBySideMinWidth = 90
	dailyDetailMinWidth           = 32
	dailyDetailGap                = 2
)

type dashboardDailyDetail struct {
	breakdown store.DailyBreakdown
	costs     reportCosts
}

type dashboardDailyViewResult struct {
	view            string
	windowStart     int
	detailOffset    int
	detailMaxOffset int
}

func dashboardDailyView(database *store.Store, allRows []store.DailyRow, filter store.Filter, costs reportCosts, pricingTable pricing.Table, request tui.Request, render theme.Context) (dashboardDailyViewResult, error) {
	selectedIndex := dailyCursorIndex(allRows, request.DailyCursor)
	capacity := dashboardRowCapacity(request.Width, request.Height)
	windowStart := normalizedDailyWindowStart(allRows, selectedIndex, capacity, request.DailyWindowStart)
	rows := windowDailyRows(allRows, windowStart, capacity)
	selectedDate := ""
	if selectedIndex >= 0 {
		selectedDate = allRows[selectedIndex].Date
	}
	periods := make([]chartPeriod, 0, len(rows))
	for _, row := range rows {
		periods = append(periods, chartPeriod{
			label: row.Date, values: costs.ByDateProvider[row.Date], selected: row.Date == selectedDate,
		})
	}
	chartRender := render
	if dailyDetailSideBySide(render.Width) {
		chartRender.Width = dailyChartWidth(render.Width)
	}
	chart := ""
	if len(periods) > 0 {
		height := chartHeight
		if !dailyDetailSideBySide(render.Width) {
			height = dailyStackedChartHeight(chartRender, periods, chartUsesTokens(costs), tui.ContentHeightFor(request.Width, request.Height))
		}
		if height > 0 {
			chart = renderPeriodChartWithHeight(chartRender, periods, "day", "days", chartUsesTokens(costs), height)
		}
	}
	if selectedDate == "" {
		view, detailOffset, detailMaxOffset := composeDailyView(render, request.Width, chart, renderDailyEmptyDetail(render, "No active days in this range."), request.Height, request.DailyDetailOffset)
		return dashboardDailyViewResult{view: view, windowStart: windowStart, detailOffset: detailOffset, detailMaxOffset: detailMaxOffset}, nil
	}
	detail, err := loadDashboardDailyDetail(database, filter, selectedDate, pricingTable)
	if err != nil {
		return dashboardDailyViewResult{}, err
	}
	detailWidth := dailyDetailRenderWidth(render.Width)
	view, detailOffset, detailMaxOffset := composeDailyView(render, request.Width, chart, renderDailyDetail(render, detail, detailWidth), request.Height, request.DailyDetailOffset)
	return dashboardDailyViewResult{view: view, windowStart: windowStart, detailOffset: detailOffset, detailMaxOffset: detailMaxOffset}, nil
}

func loadDashboardDailyDetail(database *store.Store, filter store.Filter, date string, pricingTable pricing.Table) (dashboardDailyDetail, error) {
	breakdown, err := database.DailyBreakdown(date, filter.Provider)
	if err != nil {
		return dashboardDailyDetail{}, err
	}
	detailFilter := filter
	detailFilter.Since, detailFilter.Until = date, date
	costs, err := loadReportCostsWithTable(database, detailFilter, nil, pricingTable)
	if err != nil {
		return dashboardDailyDetail{}, err
	}
	return dashboardDailyDetail{breakdown: breakdown, costs: costs}, nil
}

func dailyStackedChartHeight(render theme.Context, periods []chartPeriod, tokens bool, contentHeight int) int {
	minimumDetailHeight := min(6, max(1, contentHeight-1))
	for height := chartHeight; height >= 1; height-- {
		chart := renderPeriodChartWithHeight(render, periods, "day", "days", tokens, height)
		chartLines := dashboardBlockLineCount(chart)
		if contentHeight-chartLines-1 >= minimumDetailHeight {
			return height
		}
	}
	return 0
}

func composeDailyView(render theme.Context, terminalWidth int, chart, detail string, height, detailOffset int) (string, int, int) {
	contentHeight := max(1, tui.ContentHeightFor(terminalWidth, height))
	if !dailyDetailSideBySide(render.Width) {
		parts := make([]string, 0, 3)
		if chart != "" {
			chart = fitDashboardBlock(chart, render.Width)
			parts = append(parts, chart)
		}
		parts = append(parts, render.Palette.Border().Render(strings.Repeat("-", max(1, render.Width))))
		chartLines := dashboardBlockLineCount(chart)
		detailHeight := max(1, contentHeight-chartLines-1)
		if chartLines > 0 {
			detailHeight = max(min(6, max(1, contentHeight-1)), detailHeight)
		}
		detailView, normalizedOffset, detailMaxOffset := renderDailyDetailWindow(render, detail, render.Width, detailHeight, detailOffset)
		parts = append(parts, detailView)
		return strings.Join(parts, "\n"), normalizedOffset, detailMaxOffset
	}
	chartWidth := dailyChartWidth(render.Width)
	detailWidth := dailyDetailWidth(render.Width)
	detailView, normalizedOffset, detailMaxOffset := renderDailyDetailWindow(render, detail, detailWidth, contentHeight, detailOffset)
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		fitDashboardBlock(chart, chartWidth),
		render.Palette.Border().Render("│")+" ",
		detailView,
	), normalizedOffset, detailMaxOffset
}

func dailyDetailSideBySide(width int) bool {
	if width < dailyDetailSideBySideMinWidth {
		return false
	}
	return dailyChartWidth(width) >= minimumChartWidth
}

func dailyDetailWidth(width int) int {
	return min(38, max(dailyDetailMinWidth, width/3))
}

func dailyDetailRenderWidth(width int) int {
	if !dailyDetailSideBySide(width) {
		return width
	}
	return dailyDetailWidth(width)
}

func dailyChartWidth(width int) int {
	return width - dailyDetailWidth(width) - dailyDetailGap
}

func fitDashboardBlock(value string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	for index, line := range lines {
		line = lipgloss.NewStyle().Inline(true).MaxWidth(width).Render(line)
		lines[index] = line + strings.Repeat(" ", max(0, width-lipgloss.Width(line)))
	}
	return strings.Join(lines, "\n")
}

func renderDailyDetailWindow(render theme.Context, value string, width, height, offset int) (string, int, int) {
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	windowHeight := max(1, height)
	maxOffset := max(0, len(lines)-windowHeight)
	offset = min(max(0, offset), maxOffset)
	end := min(len(lines), offset+windowHeight)
	viewport := append([]string(nil), lines[offset:end]...)
	if windowHeight >= 3 {
		if offset > 0 && len(viewport) > 0 {
			viewport[0] = render.Palette.Subtle().Render("↑ more above")
		}
		if end < len(lines) && len(viewport) > 0 {
			viewport[len(viewport)-1] = render.Palette.Subtle().Render("↓ more below")
		}
	}
	return fitDashboardBlock(strings.Join(viewport, "\n"), width), offset, maxOffset
}

func dashboardBlockLineCount(value string) int {
	if value == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSuffix(value, "\n"), "\n"))
}

func renderDailyEmptyDetail(render theme.Context, message string) string {
	return strings.Join([]string{
		render.Palette.Header().Render("DAY DETAIL"),
		render.Palette.Subtle().Render(message),
	}, "\n")
}

func renderDailyDetail(render theme.Context, detail dashboardDailyDetail, width int) string {
	useTokenShares := chartUsesTokens(detail.costs) || detail.costs.Grand.Total == 0
	dayIsUnpriced := useTokenShares && detail.breakdown.Total > 0 && detail.costs.Grand.PricedTokens == 0
	metricLabel := "COST"
	if useTokenShares {
		metricLabel = "TOKENS"
	}
	totalValue := formatCost(detail.costs.Grand)
	valueStyle := render.Palette.Money()
	if useTokenShares {
		totalValue = formatNumber(detail.breakdown.Total) + " tokens"
		valueStyle = render.Palette.Emphasis()
	}
	lines := []string{
		render.Palette.Header().Render("DAY DETAIL"),
		render.Palette.Emphasis().Render(detail.breakdown.Date),
		render.Palette.Subtle().Render("TOTAL ") + valueStyle.Render(totalValue),
		"",
		render.Palette.Header().Render("PROVIDER SPLIT · " + metricLabel),
	}
	if dayIsUnpriced {
		lines = append(lines, render.Palette.Subtle().Render("UNPRICED DAY · TOKEN SHARES"))
	}
	providerValues := make([]string, len(detail.breakdown.Providers))
	maxProviderValueWidth := 0
	for index, provider := range detail.breakdown.Providers {
		cost := detail.costs.ByProvider[provider.Provider]
		value := formatCost(cost)
		if useTokenShares {
			value = formatNumber(provider.Total)
		}
		providerValues[index] = value
		maxProviderValueWidth = max(maxProviderValueWidth, lipgloss.Width(value))
	}
	barWidth := max(3, min(16, width-13-maxProviderValueWidth))
	for index, provider := range detail.breakdown.Providers {
		cost := detail.costs.ByProvider[provider.Provider]
		value := providerValues[index]
		if useTokenShares {
			value = formatNumber(provider.Total)
		}
		percent := dailyDetailPercent(provider.Total, detail.breakdown.Total)
		if !useTokenShares {
			percent = dailyDetailCostPercent(cost.Total, detail.costs.Grand.Total)
		}
		bar := dailyDetailBar(percent, barWidth)
		line := fmt.Sprintf("%-6s %3d%% %s %s", providerName(provider.Provider), percent, bar, value)
		lines = append(lines, render.Palette.Provider(string(provider.Provider), index).Render(line))
	}
	if len(detail.breakdown.Providers) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No usage recorded."))
	}

	lines = append(lines, "")
	modelHeading := "TOP MODELS BY COST"
	if useTokenShares {
		modelHeading = "TOP MODELS BY TOKENS"
	}
	lines = append(lines, render.Palette.Header().Render(modelHeading))
	models := append([]store.DailyModelRow(nil), detail.breakdown.Models...)
	sort.SliceStable(models, func(i, j int) bool {
		left := detail.costs.ByModel[modelCostKey{Provider: models[i].Provider, Model: models[i].Model}]
		right := detail.costs.ByModel[modelCostKey{Provider: models[j].Provider, Model: models[j].Model}]
		if left.Total != right.Total {
			return left.Total > right.Total
		}
		if models[i].Total != models[j].Total {
			return models[i].Total > models[j].Total
		}
		return models[i].Model < models[j].Model
	})
	modelLimit := min(5, len(models))
	for index, model := range models[:modelLimit] {
		cost := detail.costs.ByModel[modelCostKey{Provider: model.Provider, Model: model.Model}]
		value := formatCost(cost)
		if useTokenShares {
			value = formatNumber(model.Total)
		}
		nameWidth := max(8, width-lipgloss.Width(value)-1)
		name := truncateDashboardText(model.Model, nameWidth)
		line := render.Palette.Provider(string(model.Provider), index).Render(name)
		padding := max(1, width-lipgloss.Width(name)-lipgloss.Width(value))
		line += strings.Repeat(" ", padding) + valueStyle.Render(value)
		lines = append(lines, line)
	}
	if len(models) > modelLimit {
		lines = append(lines, render.Palette.Subtle().Render(fmt.Sprintf("+%d more models", len(models)-modelLimit)))
	}
	if len(models) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No models recorded."))
	}
	return strings.Join(lines, "\n")
}

func dailyDetailPercent(value, total int64) int {
	if value <= 0 || total <= 0 {
		return 0
	}
	percent := int(float64(value)/float64(total)*100 + 0.5)
	return min(100, percent)
}

func dailyDetailCostPercent(value, total pricing.Money) int {
	if value <= 0 || total <= 0 {
		return 0
	}
	percent := int(float64(value)/float64(total)*100 + 0.5)
	return min(100, percent)
}

func dailyDetailBar(percent, width int) string {
	filled := (percent*width + 50) / 100
	if percent > 0 {
		filled = max(1, filled)
	}
	filled = min(width, filled)
	return strings.Repeat("#", filled) + strings.Repeat("-", width-filled)
}

func truncateDashboardText(value string, width int) string {
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

func dashboardLedgerData(database *store.Store, filter store.Filter, costs reportCosts, request tui.Request) (tuipages.Data, error) {
	daily, err := database.Daily(filter)
	if err != nil {
		return tuipages.Data{}, err
	}

	zoom := request.Ledger.Zoom
	if zoom > tuipages.ZoomDay {
		zoom = tuipages.ZoomYear
	}
	effectiveYear := request.Ledger.Year
	if zoom == tuipages.ZoomMonth && effectiveYear == 0 {
		effectiveYear = latestLedgerYear(daily)
	}
	effectiveMonth := request.Ledger.Month
	if zoom == tuipages.ZoomDay && effectiveMonth == "" {
		effectiveMonth = latestLedgerMonth(daily)
	}

	grouped := make(map[string]tuipages.Row)
	order := make([]string, 0, len(daily))
	for _, row := range daily {
		key, include := ledgerPeriodKey(row.Date, zoom, effectiveYear, effectiveMonth)
		if !include {
			continue
		}
		period, exists := grouped[key]
		if !exists {
			period = tuipages.Row{Key: key, Label: ledgerPeriodLabel(key, zoom)}
			order = append(order, key)
		}
		for provider, value := range costs.ByDateProvider[row.Date] {
			addLedgerProvider(&period, provider, value)
		}
		grouped[key] = period
	}

	rows := make([]tuipages.Row, 0, len(order))
	for _, key := range order {
		rows = append(rows, grouped[key])
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}

	total := tuipages.Row{Key: "total", Label: "TOTAL"}
	for _, row := range rows {
		total = total.Add(row)
	}
	return tuipages.Data{Available: true, Zoom: zoom, Year: effectiveYear, Month: effectiveMonth, Rows: rows, Total: total}, nil
}

func ledgerPeriodKey(date string, zoom tuipages.Zoom, year int, month string) (string, bool) {
	if len(date) < len("2006-01-02") {
		return "", false
	}
	switch zoom {
	case tuipages.ZoomMonth:
		if year > 0 && date[:4] != fmt.Sprintf("%04d", year) {
			return "", false
		}
		return date[:7], true
	case tuipages.ZoomDay:
		if month != "" && date[:7] != month {
			return "", false
		}
		return date, true
	default:
		return date[:4], true
	}
}

func ledgerPeriodLabel(key string, zoom tuipages.Zoom) string {
	if zoom == tuipages.ZoomYear || len(key) < len("2006-01") {
		return key
	}
	date := key
	if zoom == tuipages.ZoomMonth {
		date += "-01"
	}
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return key
	}
	if zoom == tuipages.ZoomMonth {
		return parsed.Format("Jan 2006")
	}
	return parsed.Format("Jan 02")
}

func latestLedgerYear(rows []store.DailyRow) int {
	if len(rows) == 0 || len(rows[len(rows)-1].Date) < 4 {
		return 0
	}
	year, _ := strconv.Atoi(rows[len(rows)-1].Date[:4])
	return year
}

func latestLedgerMonth(rows []store.DailyRow) string {
	if len(rows) == 0 || len(rows[len(rows)-1].Date) < 7 {
		return ""
	}
	return rows[len(rows)-1].Date[:7]
}

func addLedgerProvider(row *tuipages.Row, provider discover.Provider, value providerChartValue) {
	totals := tuipages.ProviderTotals{
		Cost:           value.Cost.Total,
		Tokens:         value.Tokens,
		PricedTokens:   value.Cost.PricedTokens,
		UnpricedTokens: value.Cost.UnpricedTokens,
	}
	switch provider {
	case discover.ProviderCodex:
		row.Codex = row.Codex.Add(totals)
	case discover.ProviderClaude:
		row.Claude = row.Claude.Add(totals)
	}
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
	capacity := dashboardRowCapacity(request.Width, request.Height) + 5
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

func dashboardRowCapacity(width, height int) int {
	if height <= 0 {
		return 8
	}
	return max(3, min(10, tui.ContentHeightFor(width, height)-10))
}

func dailyCursorIndex(rows []store.DailyRow, cursor int) int {
	if len(rows) == 0 {
		return -1
	}
	return max(0, len(rows)-1-max(0, cursor))
}

func normalizedDailyCursor(rows []store.DailyRow, cursor int) int {
	selectedIndex := dailyCursorIndex(rows, cursor)
	if selectedIndex < 0 {
		return 0
	}
	return len(rows) - 1 - selectedIndex
}

func normalizedDailyWindowStart(rows []store.DailyRow, selectedIndex, capacity, requestedStart int) int {
	if len(rows) == 0 {
		return 0
	}
	capacity = max(1, capacity)
	selectedIndex = min(len(rows)-1, max(0, selectedIndex))
	maxStart := max(0, len(rows)-capacity)
	start := min(max(0, requestedStart), maxStart)
	if selectedIndex < start {
		return selectedIndex
	}
	if selectedIndex >= start+capacity {
		return min(maxStart, selectedIndex-capacity+1)
	}
	return start
}

func windowDailyRows(rows []store.DailyRow, start, capacity int) []store.DailyRow {
	if len(rows) == 0 {
		return rows
	}
	capacity = max(1, capacity)
	start = min(max(0, start), max(0, len(rows)-capacity))
	end := min(len(rows), start+capacity)
	return rows[start:end]
}
