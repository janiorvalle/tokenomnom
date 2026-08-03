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
	var dailyCounts dashboardDailyCountsCache
	var dailyPageSessions dashboardDailySessionCache
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
			modelSessions := loadDashboardModelSessionData(filepath.Join(stateDir, historystore.DatabaseName), request)
			snapshot, err := dashboardSnapshotWithModelSessions(database, request, render, location, syncer.Summary{}, modelSessions)
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
			dailyCounts.clear()
			dailyPageSessions.clear()
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
		modelSessions := loadDashboardModelSessionData(filepath.Join(stateDir, historystore.DatabaseName), request)
		snapshot, err := dashboardSnapshotWithDailySessionsAndModelSessions(database, request, render, location, syncSummary, func(date string) tuipages.DailySessionData {
			return loadDashboardDailySessions(cmd, filepath.Join(stateDir, historystore.DatabaseName), request, location, date, codexDir, claudeDir, &dailyCounts, &dailyPageSessions)
		}, modelSessions)
		snapshot.Ledger = loadDashboardLedgerHistory(filepath.Join(stateDir, historystore.DatabaseName), snapshot.Ledger, request, location)
		snapshot.Sessions = sessions.snapshot(request, func() tuipages.SessionPageData {
			return loadDashboardHistoryWithCost(cmd, filepath.Join(stateDir, historystore.DatabaseName), request, location, codexDir, claudeDir)
		})
		for _, project := range snapshot.Sessions.ProjectStats {
			snapshot.Rail.Projects = append(snapshot.Rail.Projects, tui.RailProject{Label: project.Label, Share: project.Share})
		}
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
		if snapshot.Ledger.Analytics.Warning != "" {
			warnings = append(warnings, snapshot.Ledger.Analytics.Warning)
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
	return loadDashboardHistoryWithCost(nil, path, request, location, "", "")
}

func loadDashboardHistoryWithCost(cmd *cobra.Command, path string, request tui.Request, location *time.Location, codexDir, claudeDir string) tuipages.SessionPageData {
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

	now := time.Now()
	since, until := dashboardHistoryWindow(request.Range, location, now)
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
	projectSince, projectUntil := dashboardHistoryWindow(tui.Range30Days, location, now)
	projectQuery := historystore.CatalogQuery{
		Provider: historyProvider(request.Provider),
		Since:    projectSince, Until: projectUntil, Source: historystore.CatalogSourceAny,
	}
	projectStats, err := database.ListCatalogProjectStats(projectQuery, 4)
	projectOptions := tuipages.ProjectOptionsFromKeys(projects)
	projectWarning := ""
	projectData := []tuipages.ProjectStat{}
	if err != nil {
		projectWarning = "History project summaries could not be read; press R to retry or run tokenomnom history index."
	} else {
		projectTotal := 0
		for _, stat := range projectStats {
			projectTotal = max(projectTotal, stat.TotalSessions)
		}
		projectData = make([]tuipages.ProjectStat, 0, len(projectStats))
		for _, stat := range projectStats {
			share := 0.0
			if projectTotal > 0 {
				share = float64(stat.Sessions) / float64(projectTotal)
			}
			projectData = append(projectData, tuipages.ProjectStat{
				Label: tuipages.ProjectLabel(stat.Project, projectOptions), Sessions: stat.Sessions, Share: share,
			})
		}
	}
	warnings := append([]string(nil), page.Warnings...)
	if projectWarning != "" {
		warnings = append(warnings, projectWarning)
	}
	var costs map[string]tuipages.SessionCost
	var promptPages map[string]tuipages.SessionPromptPage
	if request.SessionDetailID != "" {
		prompts, promptErr := database.SessionPrompts(request.SessionDetailID, historystore.PromptQuery{
			Role: "user", Source: historystore.CatalogSourceAny,
		})
		if promptErr != nil {
			promptWarning := "Session prompts are unavailable; the indexed session overview is still available."
			warnings = append(warnings, promptWarning)
			promptPages = map[string]tuipages.SessionPromptPage{request.SessionDetailID: {Warning: promptWarning}}
		} else {
			presentHistoryPromptPage(&prompts, location)
			promptPage := tuipages.SessionPromptPage{
				Prompts: make([]tuipages.SessionPrompt, 0, len(prompts.Prompts)), HasMore: prompts.Page.HasMore,
			}
			for _, prompt := range prompts.Prompts {
				promptPage.Prompts = append(promptPage.Prompts, tuipages.SessionPrompt{
					PromptID: prompt.PromptID, Date: historyPageDate(prompt.Timestamp), Snippet: safePrettyPreview(prompt.Snippet),
				})
			}
			promptPages = map[string]tuipages.SessionPromptPage{request.SessionDetailID: promptPage}
		}
	}
	costSessionID := request.SessionDetailID
	if costSessionID == "" && tui.WidthTierFor(request.Width) == tui.WidthWide && tui.HeightTierFor(request.Height) == tui.HeightTall && len(page.Sessions) > 0 {
		selectedIndex := min(max(request.SessionOffset, 0), len(page.Sessions)-1)
		if request.SessionReturnToEnd {
			selectedIndex = len(page.Sessions) - 1
		}
		costSessionID = page.Sessions[selectedIndex].SessionID
	}
	if cmd != nil && costSessionID != "" {
		cost, warning := loadDashboardSessionCost(cmd, database, costSessionID, codexDir, claudeDir)
		if cost.Status != "" || len(cost.Models) > 0 {
			costs = map[string]tuipages.SessionCost{costSessionID: cost}
		}
		if warning != "" {
			warnings = append(warnings, warning)
		}
	}
	return tuipages.SessionPageData{
		Sessions: page.Sessions, Costs: costs, PromptPages: promptPages, Projects: projectOptions, ProjectStats: projectData,
		HasMore: page.HasMore, NextCursor: page.NextCursor, IndexAvailable: true,
		Warning: strings.Join(uniqueStrings(warnings), "; "), Location: location,
	}
}

func loadDashboardDailySessions(cmd *cobra.Command, path string, request tui.Request, location *time.Location, date, codexDir, claudeDir string, dailyCounts *dashboardDailyCountsCache, sessionCache *dashboardDailySessionCache) tuipages.DailySessionData {
	if strings.TrimSpace(date) == "" {
		return tuipages.DailySessionData{}
	}
	if location == nil {
		location = time.Local
	}
	if sessionCache != nil {
		key := dashboardDailySessionCacheKey{provider: request.Provider, date: date, dateRange: request.Range, zone: location.String(), codexDir: codexDir, claudeDir: claudeDir}
		if data, ok := sessionCache.get(key); ok {
			return data
		}
	}
	start, err := time.ParseInLocation(heatmapDateLayout, date, location)
	if err != nil {
		return tuipages.DailySessionData{Warning: fmt.Sprintf("The selected day %q could not be read; move the Daily cursor to another day.", date)}
	}
	end := start.AddDate(0, 0, 1).Add(-time.Nanosecond)
	info, err := historystore.Inspect(path)
	if err != nil || !info.Exists {
		return tuipages.DailySessionData{Warning: "History index unavailable; run tokenomnom history index to populate the Daily sessions band."}
	}
	database, err := historystore.OpenReadOnly(path)
	if err != nil {
		return tuipages.DailySessionData{Warning: "History index unavailable; run tokenomnom history index to populate the Daily sessions band."}
	}
	defer database.Close()
	page, err := database.ListSessionCostSources(historystore.SessionCostQuery{Catalog: historystore.CatalogQuery{
		Provider: historyProvider(request.Provider), Since: &start, Until: &end, ActivitySince: &start, ActivityUntil: &end,
		Source: historystore.CatalogSourceAny, Limit: historystore.MaxSessionCostPageSize,
	}})
	if err != nil {
		return tuipages.DailySessionData{Warning: "Indexed sessions for this day could not be read; press R to retry or run tokenomnom history index."}
	}
	trendSince, trendUntil := dashboardHistoryWindow(request.Range, location, time.Now())
	if dailyCounts == nil {
		dailyCounts = &dashboardDailyCountsCache{}
	}
	sessionCounts, _, _, _, trendCountErr := dailyCounts.load(database, historyProvider(request.Provider), trendSince, trendUntil, location)
	_, providerCounts, selectedTotal, selectedTimes, selectedCountErr := dailyCounts.load(database, historyProvider(request.Provider), &start, &end, location)
	displaySessionTimes := make(map[string]string, len(selectedTimes))
	for key, timestamp := range selectedTimes {
		displaySessionTimes[key] = dashboardDailyActivityTime(timestamp, location)
	}
	table, err := loadPricingTable()
	if err != nil {
		return tuipages.DailySessionData{Warning: "Session costs could not be priced; check the pricing override and press R to retry."}
	}
	result := tuipages.DailySessionData{
		Total: len(page.Sessions), HasMore: page.Page.HasMore,
		SessionCounts: sessionCounts, ProviderCounts: providerCounts, SessionTimes: displaySessionTimes,
	}
	warnings := uniqueStrings(page.Warnings)
	if selectedCountErr == nil {
		result.Total, result.TotalKnown = selectedTotal, true
	}
	if trendCountErr != nil || selectedCountErr != nil {
		warnings = append(warnings, "Daily session counts could not be read; press R to retry or run tokenomnom history index.")
	}
	unavailable := 0
	for _, session := range page.Sessions {
		row, priceErr := priceHistorySessionForWindow(cmd, session, table, codexDir, claudeDir, start, end)
		if priceErr != nil {
			row = historySessionCostRow{CatalogSession: session.CatalogSession, AttributionStatus: "unavailable"}
		}
		if row.AttributionStatus != "complete" && row.AttributionStatus != "settled_missing" {
			unavailable++
		}
		model := ""
		if len(row.Models) > 0 {
			model = row.Models[0].Model
		}
		sessionTime := dashboardDailySessionTime(row.CatalogSession, location)
		if activityTime := result.SessionTimes[dashboardDailySessionKey(row.Provider, row.SessionID)]; activityTime != "" {
			sessionTime = activityTime
		}
		result.Rows = append(result.Rows, tuipages.DailySession{
			Time: sessionTime, Provider: string(row.Provider),
			Project: row.Project, SessionID: row.SessionID, Model: model, Tokens: row.Tokens.TotalTokens,
			Cost: row.Tokens.cost, PricedTokens: row.Tokens.PricedTokens, Prompt: row.Preview,
			PromptCount: row.LogicalPromptCount, AttributionStatus: row.AttributionStatus,
		})
	}
	if unavailable > 0 {
		if page.Page.HasMore {
			warnings = append(warnings, fmt.Sprintf("Cost attribution unavailable for at least %d of %d loaded sessions; more sessions were not priced. Restore the source or vault snapshot, then rerun `tokenomnom history index`.", unavailable, len(page.Sessions)))
		} else {
			warnings = append(warnings, fmt.Sprintf("Cost attribution unavailable for %d of %d sessions; restore the source or vault snapshot, then rerun `tokenomnom history index`.", unavailable, result.Total))
		}
	}
	result.Warning = strings.Join(uniqueStrings(warnings), "; ")
	if sessionCache != nil {
		key := dashboardDailySessionCacheKey{provider: request.Provider, date: date, dateRange: request.Range, zone: location.String(), codexDir: codexDir, claudeDir: claudeDir}
		sessionCache.set(key, result)
	}
	return result
}

func dashboardDailySessionCounts(database *historystore.Store, provider history.Provider, since, until *time.Time, location *time.Location) (map[string]int, map[string]int, int, map[string]string, error) {
	values, err := database.ListCatalogSessionTimestamps(historystore.CatalogQuery{
		Provider: provider, Since: since, Until: until,
		Source: historystore.CatalogSourceAny,
	})
	if err != nil {
		return nil, nil, 0, nil, err
	}
	sessionCounts := make(map[string]int)
	providerCounts := make(map[string]int)
	sessionTimes := make(map[string]string)
	seenSessionDays := make(map[string]struct{})
	seenProviderSessions := make(map[string]struct{})
	for _, value := range values {
		date := dashboardDailySessionDate(value.Timestamp, location)
		if date == "" || value.SessionID == "" {
			continue
		}
		providerKey := dashboardDailySessionKey(value.Provider, value.SessionID)
		dayKey := providerKey + "\x00" + date
		if _, ok := seenSessionDays[dayKey]; !ok {
			seenSessionDays[dayKey] = struct{}{}
			sessionCounts[date]++
		}
		if _, ok := seenProviderSessions[providerKey]; !ok {
			seenProviderSessions[providerKey] = struct{}{}
			providerCounts[string(value.Provider)]++
		}
		if existing := sessionTimes[providerKey]; existing == "" || dashboardDailyTimestampBefore(value.Timestamp, existing) {
			sessionTimes[providerKey] = value.Timestamp
		}
	}
	return sessionCounts, providerCounts, len(seenProviderSessions), sessionTimes, nil
}

func dashboardDailyTimestampBefore(candidate, existing string) bool {
	candidateTime, candidateErr := time.Parse(time.RFC3339Nano, candidate)
	existingTime, existingErr := time.Parse(time.RFC3339Nano, existing)
	if candidateErr == nil && existingErr == nil {
		return candidateTime.Before(existingTime)
	}
	return candidate < existing
}

func dashboardDailySessionKey(provider history.Provider, sessionID string) string {
	return string(provider) + "\x00" + sessionID
}

func dashboardDailySessionTime(session historystore.CatalogSession, location *time.Location) string {
	value := session.LastTimestamp
	if value == nil || *value == "" {
		value = session.FirstTimestamp
	}
	if value == nil || *value == "" {
		return "--:--"
	}
	return dashboardDailyActivityTime(*value, location)
}

func dashboardDailyActivityTime(timestamp string, location *time.Location) string {
	if timestamp == "" {
		return "--:--"
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return "--:--"
	}
	if location != nil {
		parsed = parsed.In(location)
	}
	return parsed.Format("15:04")
}

func dashboardDailySessionDate(timestamp string, location *time.Location) string {
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return ""
	}
	if location != nil {
		parsed = parsed.In(location)
	}
	return parsed.Format(heatmapDateLayout)
}

func loadDashboardSessionCost(cmd *cobra.Command, database *historystore.Store, sessionID, codexDir, claudeDir string) (tuipages.SessionCost, string) {
	page, err := database.ListSessionCostSources(historystore.SessionCostQuery{SessionID: sessionID})
	if err != nil || len(page.Sessions) == 0 {
		return tuipages.SessionCost{Status: "unavailable"}, "Session cost attribution is unavailable; enter still opens the indexed detail."
	}
	table, err := loadPricingTable()
	if err != nil {
		return tuipages.SessionCost{Status: "unavailable"}, "Session cost attribution is unavailable; check pricing and retry."
	}
	row, err := priceHistorySession(cmd, page.Sessions[0], table, codexDir, claudeDir)
	if err != nil {
		return tuipages.SessionCost{Status: "unavailable"}, "Session cost attribution is unavailable; enter still opens the indexed detail."
	}
	cost := tuipages.SessionCost{
		Status: row.AttributionStatus, TotalTokens: row.Tokens.TotalTokens, PricedTokens: row.Tokens.PricedTokens,
		UnpricedTokens: row.Tokens.UnpricedTokens, CostUSD: row.Tokens.CostUSD,
		Models: make([]tuipages.SessionModel, 0, len(row.Models)),
	}
	for _, model := range row.Models {
		cost.Models = append(cost.Models, tuipages.SessionModel{
			Date: model.Date, Provider: string(model.Provider), Model: model.Model,
			InputTokens: model.InputTokens, CacheTokens: model.CacheReadTokens + model.CacheWriteTokens,
			OutputTokens: model.OutputTokens, TotalTokens: model.TotalTokens, CostUSD: model.CostUSD,
			PricedTokens: model.PricedTokens, UnpricedTokens: model.UnpricedTokens,
		})
	}
	return cost, strings.Join(uniqueStrings(row.Warnings), "; ")
}

func loadDashboardLedgerSessions(cmd *cobra.Command, path string, data tuipages.Data, request tui.Request, location *time.Location, codexDir, claudeDir string) tuipages.Data {
	day := request.Ledger.ExpandedDay
	data.SessionDay = day
	data.SessionPageCursor = request.Ledger.SessionPageCursor
	data.Location = location
	if request.SessionProjectActive {
		// The usage store has no project dimension, so discard its unscoped model
		// rollup and let the filtered history query own this day view.
		data.DayModels = nil
		data.DayModelTotalCost, data.DayModelTotalTokens, data.DayModelCount = 0, 0, 0
		data.DayProjects, data.DayHours, data.DayProjectCount, data.DaySessionCount = nil, nil, 0, 0
	}
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
	dayQuery := historystore.CatalogQuery{
		Provider: historyProvider(request.Provider), Since: &start, Until: &end, Source: historystore.CatalogSourceAny,
		Project: request.SessionProject, ProjectSet: request.SessionProjectActive,
	}
	dayProfile, profileErr := database.LedgerAnalytics(dayQuery, location)
	if profileErr == nil {
		data.DayHours = ledgerDayHours(dayProfile.Hours)
		data.DaySessionCount = ledgerProfileSessionCount(dayProfile.Hours)
		projectCounts := make(map[string]int)
		for _, value := range dayProfile.ProjectMonths {
			project := strings.TrimSpace(value.Project)
			if project == "" {
				project = "unknown"
			}
			projectCounts[project] += value.Sessions
		}
		data.DayProjects = dashboardLedgerDayProjects(projectCounts, ledgerProjectSessionCount(projectCounts))
		data.DayProjectCount = len(projectCounts)
	}
	sessionQuery := dayQuery
	// Page size follows the viewport so tall windows are not paged at 20 rows
	// above blank space; the store maximum still bounds transcript reads.
	sessionQuery.Limit = historystore.DefaultSessionCostPageSize
	if request.Height > 0 {
		sessionQuery.Limit = min(historystore.MaxSessionCostPageSize,
			max(historystore.DefaultSessionCostPageSize, request.Height-20))
	}
	sessionQuery.Cursor = request.Ledger.SessionPageCursor
	page, err := database.ListSessionCostSources(historystore.SessionCostQuery{Catalog: sessionQuery})
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
	modelTotals := make(map[ledgerDayModelKey]tuipages.LedgerModel)
	projectCounts := make(map[string]int)
	unavailable := 0
	incomplete := 0
	for _, session := range page.Sessions {
		row, priceErr := priceHistorySessionForWindow(cmd, session, table, codexDir, claudeDir, start, end)
		if priceErr != nil {
			row = historySessionCostRow{CatalogSession: session.CatalogSession, AttributionStatus: "unavailable"}
		}
		if row.AttributionStatus == "unavailable" {
			unavailable++
		}
		if row.AttributionStatus != "complete" {
			incomplete++
		}
		project := strings.TrimSpace(row.Project)
		if project == "" {
			project = "unknown"
		}
		projectCounts[project]++
		for _, model := range row.Models {
			key := ledgerDayModelKey{provider: string(model.Provider), model: model.Model}
			value := modelTotals[key]
			value.Provider, value.Model = key.provider, key.model
			value.Tokens += model.TotalTokens
			value.Cost += model.cost
			value.PricedTokens += model.PricedTokens
			value.UnpricedTokens += model.UnpricedTokens
			value.CostPerMillion = weightedRate(value.Cost, value.PricedTokens)
			modelTotals[key] = value
		}
		data.Sessions = append(data.Sessions, tuipages.LedgerSession{
			CatalogSession: row.CatalogSession,
			Tokens:         row.Tokens.TotalTokens, Cost: row.Tokens.cost,
			PricedTokens: row.Tokens.PricedTokens, UnpricedTokens: row.Tokens.UnpricedTokens,
			AttributionStatus: row.AttributionStatus, ActivityTimestamp: row.attributionTimestamp,
			Warning: strings.Join(uniqueStrings(row.Warnings), "; "),
		})
	}
	pageModels := dashboardLedgerDayModels(modelTotals)
	completeDayPage := request.Ledger.SessionPageCursor == "" && !page.Page.HasMore && len(data.Sessions) == len(page.Sessions) && incomplete == 0
	// A partial catalog page is not a valid day-wide model denominator. When the
	// usage aggregate is unavailable, leave the model band honest instead of
	// presenting the first page as the whole selected day.
	if len(data.DayModels) == 0 && completeDayPage {
		data.DayModels = boundedLedgerModels(pageModels)
		data.DayModelTotalCost, data.DayModelTotalTokens = dashboardLedgerModelTotals(pageModels)
		data.DayModelCount = len(pageModels)
	}
	if len(data.DayProjects) == 0 && completeDayPage {
		data.DayProjects = dashboardLedgerDayProjects(projectCounts, len(data.Sessions))
		data.DayProjectCount = len(projectCounts)
	}
	if data.DaySessionCount == 0 && completeDayPage {
		data.DaySessionCount = len(data.Sessions)
	}
	warnings := uniqueStrings(page.Warnings)
	if unavailable > 0 {
		warnings = append(warnings, fmt.Sprintf("Cost attribution is unavailable for %d of %d sessions.", unavailable, len(data.Sessions)))
	}
	data.SessionWarning = strings.Join(warnings, "; ")
	return data
}

type ledgerDayModelKey struct {
	provider string
	model    string
}

func dashboardLedgerDayModels(values map[ledgerDayModelKey]tuipages.LedgerModel) []tuipages.LedgerModel {
	models := make([]tuipages.LedgerModel, 0, len(values))
	for _, value := range values {
		models = append(models, value)
	}
	sort.SliceStable(models, func(left, right int) bool {
		if models[left].Cost != models[right].Cost {
			return models[left].Cost > models[right].Cost
		}
		if models[left].Tokens != models[right].Tokens {
			return models[left].Tokens > models[right].Tokens
		}
		if models[left].Provider != models[right].Provider {
			return models[left].Provider < models[right].Provider
		}
		return models[left].Model < models[right].Model
	})
	return models
}

func boundedLedgerModels(models []tuipages.LedgerModel) []tuipages.LedgerModel {
	if len(models) <= 8 {
		return models
	}
	return models[:8]
}

func dashboardLedgerModelTotals(models []tuipages.LedgerModel) (pricing.Money, int64) {
	var cost pricing.Money
	var tokens int64
	for _, model := range models {
		cost += model.Cost
		tokens += model.Tokens
	}
	return cost, tokens
}

func ledgerDayHours(values []historystore.LedgerProfileStat) []tuipages.LedgerProfile {
	profiles := make([]tuipages.LedgerProfile, 0, len(values))
	for _, value := range values {
		profiles = append(profiles, tuipages.LedgerProfile{Label: fmt.Sprintf("%02d", value.Bucket), Value: value.Sessions, Sessions: value.Sessions})
	}
	return profiles
}

func ledgerProfileSessionCount(values []historystore.LedgerProfileStat) int {
	total := 0
	for _, value := range values {
		total += value.Sessions
	}
	return total
}

func ledgerProjectSessionCount(values map[string]int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func dashboardLedgerDayProjects(counts map[string]int, total int) []tuipages.LedgerProject {
	projects := make([]tuipages.LedgerProject, 0, len(counts))
	for label, sessions := range counts {
		share := 0.0
		if total > 0 {
			share = float64(sessions) / float64(total)
		}
		projects = append(projects, tuipages.LedgerProject{Label: label, Sessions: sessions, Share: share})
	}
	sort.SliceStable(projects, func(left, right int) bool {
		if projects[left].Sessions != projects[right].Sessions {
			return projects[left].Sessions > projects[right].Sessions
		}
		return projects[left].Label < projects[right].Label
	})
	if len(projects) > 8 {
		projects = projects[:8]
	}
	return projects
}

func loadDashboardLedgerHistory(path string, data tuipages.Data, request tui.Request, location *time.Location) tuipages.Data {
	info, err := historystore.Inspect(path)
	if err != nil || !info.Exists {
		return data
	}
	database, err := historystore.OpenReadOnly(path)
	if err != nil {
		data.Analytics.Warning = "Ledger history profiles are unavailable; run tokenomnom history index to rebuild them."
		return data
	}
	defer database.Close()
	since, until := ledgerHistoryWindow(data, request.Ledger, location)
	baseQuery := historystore.CatalogQuery{Provider: historyProvider(request.Provider), Source: historystore.CatalogSourceAny}
	profileQuery := baseQuery
	profileQuery.Since, profileQuery.Until = since, until
	profile, counts, err := database.LedgerAnalyticsWithCounts(profileQuery, baseQuery, location)
	if err != nil {
		data.Analytics.Warning = "Ledger history profiles could not be read; press R to retry or run tokenomnom history index."
		return data
	}

	monthSessions := make(map[string]int, len(counts.Months))
	for _, month := range counts.Months {
		monthSessions[month.Month] = month.Sessions
	}
	daySessions := make(map[string]int, len(counts.Days))
	for _, day := range counts.Days {
		daySessions[day.Day] = day.Sessions
	}
	for index := range data.Analytics.Months {
		data.Analytics.Months[index].Sessions = monthSessions[data.Analytics.Months[index].Key]
	}
	for index := range data.Rows {
		if len(data.Rows[index].Key) == 7 {
			data.Rows[index].Sessions = monthSessions[data.Rows[index].Key]
			continue
		}
		if len(data.Rows[index].Key) == 10 {
			data.Rows[index].Sessions = daySessions[data.Rows[index].Key]
			continue
		}
		if len(data.Rows[index].Key) == 4 {
			for month, sessions := range monthSessions {
				if strings.HasPrefix(month, data.Rows[index].Key+"-") {
					data.Rows[index].Sessions += sessions
				}
			}
		}
	}
	data.Total.Sessions = 0
	for _, row := range data.Rows {
		data.Total.Sessions += row.Sessions
	}

	weekdayLabels := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	weekdayCounts := make(map[int]int, len(profile.Weekdays))
	for _, value := range profile.Weekdays {
		weekdayCounts[value.Bucket] = value.Sessions
	}
	data.Analytics.Weekdays = make([]tuipages.LedgerProfile, 0, 7)
	for bucket := 1; bucket <= 6; bucket++ {
		data.Analytics.Weekdays = append(data.Analytics.Weekdays, tuipages.LedgerProfile{Label: weekdayLabels[bucket], Value: weekdayCounts[bucket], Sessions: weekdayCounts[bucket]})
	}
	data.Analytics.Weekdays = append(data.Analytics.Weekdays, tuipages.LedgerProfile{Label: weekdayLabels[0], Value: weekdayCounts[0], Sessions: weekdayCounts[0]})

	hourCounts := make(map[int]int, len(profile.Hours))
	for _, value := range profile.Hours {
		hourCounts[value.Bucket] = value.Sessions
	}
	data.Analytics.Hours = make([]tuipages.LedgerProfile, 0, 24)
	for hour := 0; hour < 24; hour++ {
		data.Analytics.Hours = append(data.Analytics.Hours, tuipages.LedgerProfile{Label: fmt.Sprintf("%02d", hour), Value: hourCounts[hour], Sessions: hourCounts[hour]})
	}

	projectCounts := make(map[string]int)
	projectMonths := make([]tuipages.LedgerProjectMonth, 0, len(profile.ProjectMonths))
	for _, value := range profile.ProjectMonths {
		projectCounts[value.Project] += value.Sessions
		projectMonths = append(projectMonths, tuipages.LedgerProjectMonth{Project: value.Project, Month: value.Month, Sessions: value.Sessions})
	}
	projects := make([]tuipages.LedgerProject, 0, len(projectCounts))
	totalProjects := 0
	for _, count := range projectCounts {
		totalProjects += count
	}
	for project, count := range projectCounts {
		share := 0.0
		if totalProjects > 0 {
			share = float64(count) / float64(totalProjects)
		}
		projects = append(projects, tuipages.LedgerProject{Label: project, Sessions: count, Share: share})
	}
	sort.SliceStable(projects, func(left, right int) bool {
		if projects[left].Sessions != projects[right].Sessions {
			return projects[left].Sessions > projects[right].Sessions
		}
		return projects[left].Label < projects[right].Label
	})
	if len(projects) > 8 {
		projects = projects[:8]
	}
	data.Analytics.Projects = projects
	data.Analytics.ProjectMonths = projectMonths
	return data
}

func ledgerHistoryWindow(data tuipages.Data, state tuipages.State, location *time.Location) (*time.Time, *time.Time) {
	if location == nil {
		location = time.Local
	}
	if state.Zoom == tuipages.ZoomYear {
		index := tuipages.SelectedIndex(data, state)
		if index >= 0 && index < len(data.Rows) && len(data.Rows[index].Key) == 4 {
			if year, err := strconv.Atoi(data.Rows[index].Key); err == nil {
				start := time.Date(year, time.January, 1, 0, 0, 0, 0, location)
				return &start, ledgerWindowEnd(start.AddDate(1, 0, 0))
			}
		}
	}
	if state.Zoom == tuipages.ZoomMonth {
		year := state.Year
		if year == 0 {
			year = data.Year
		}
		if year > 0 {
			start := time.Date(year, time.January, 1, 0, 0, 0, 0, location)
			return &start, ledgerWindowEnd(start.AddDate(1, 0, 0))
		}
	}
	if state.Zoom == tuipages.ZoomDay {
		month := state.Month
		if month == "" {
			month = data.Month
		}
		if len(month) >= len("2006-01") {
			start, err := time.ParseInLocation("2006-01", month[:len("2006-01")], location)
			if err == nil {
				return &start, ledgerWindowEnd(start.AddDate(0, 1, 0))
			}
		}
	}
	return nil, nil
}

func ledgerWindowEnd(next time.Time) *time.Time {
	end := next.Add(-time.Nanosecond)
	return &end
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

type dashboardDailyCountsCache struct {
	mu     sync.Mutex
	values map[dashboardDailyCountsCacheKey]dashboardDailyCountsResult
}

type dashboardDailyCountsCacheKey struct {
	provider string
	since    string
	until    string
	zone     string
}

type dashboardDailyCountsResult struct {
	sessionCounts  map[string]int
	providerCounts map[string]int
	total          int
	sessionTimes   map[string]string
}

type dashboardDailySessionCache struct {
	mu     sync.Mutex
	values map[dashboardDailySessionCacheKey]tuipages.DailySessionData
}

type dashboardDailySessionCacheKey struct {
	provider  tui.Provider
	date      string
	dateRange tui.Range
	zone      string
	codexDir  string
	claudeDir string
}

func (cache *dashboardDailySessionCache) clear() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.values = nil
}

func (cache *dashboardDailySessionCache) get(key dashboardDailySessionCacheKey) (tuipages.DailySessionData, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	value, ok := cache.values[key]
	return value, ok
}

func (cache *dashboardDailySessionCache) set(key dashboardDailySessionCacheKey, value tuipages.DailySessionData) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.values == nil {
		cache.values = make(map[dashboardDailySessionCacheKey]tuipages.DailySessionData)
	}
	cache.values[key] = value
}

func (cache *dashboardDailyCountsCache) clear() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.values = nil
}

func (cache *dashboardDailyCountsCache) load(database *historystore.Store, provider history.Provider, since, until *time.Time, location *time.Location) (map[string]int, map[string]int, int, map[string]string, error) {
	key := dashboardDailyCountsCacheKey{
		provider: string(provider), since: dashboardDailyCacheTime(since), until: dashboardDailyCacheTime(until),
	}
	if location != nil {
		key.zone = location.String()
	}
	cache.mu.Lock()
	if value, ok := cache.values[key]; ok {
		cache.mu.Unlock()
		return value.sessionCounts, value.providerCounts, value.total, value.sessionTimes, nil
	}
	cache.mu.Unlock()

	sessionCounts, providerCounts, total, sessionTimes, err := dashboardDailySessionCounts(database, provider, since, until, location)
	if err != nil {
		return nil, nil, 0, nil, err
	}
	cache.mu.Lock()
	if cache.values == nil {
		cache.values = make(map[dashboardDailyCountsCacheKey]dashboardDailyCountsResult)
	}
	cache.values[key] = dashboardDailyCountsResult{
		sessionCounts: sessionCounts, providerCounts: providerCounts, total: total, sessionTimes: sessionTimes,
	}
	cache.mu.Unlock()
	return sessionCounts, providerCounts, total, sessionTimes, nil
}

func dashboardDailyCacheTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
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
	query       string
	sessionID   string
	selectIndex int
	provider    tui.Provider
	dateRange   tui.Range
	widthTier   tui.WidthTier
	heightTier  tui.HeightTier
}

func (cache *dashboardHistorySearchCache) snapshot(request tui.Request, refresh func() (tuipages.HistorySearchData, error)) (tuipages.HistorySearchData, error) {
	key := dashboardHistorySearchCacheKey{
		query: request.HistoryQuery, sessionID: request.HistorySessionID, selectIndex: request.HistorySelect,
		provider: request.Provider, dateRange: request.Range,
		widthTier: tui.WidthTierFor(request.Width), heightTier: tui.HeightTierFor(request.Height),
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
	provider        tui.Provider
	dateRange       tui.Range
	project         string
	projectActive   bool
	cursor          string
	selectedIndex   int
	returnToEnd     bool
	detailSessionID string
	widthTier       tui.WidthTier
	heightTier      tui.HeightTier
}

func (cache *dashboardSessionCache) snapshot(request tui.Request, refresh func() tuipages.SessionPageData) tuipages.SessionPageData {
	key := dashboardSessionCacheKey{
		provider:        request.Provider,
		dateRange:       request.Range,
		project:         request.SessionProject,
		projectActive:   request.SessionProjectActive,
		cursor:          request.SessionCursor,
		selectedIndex:   request.SessionOffset,
		returnToEnd:     request.SessionReturnToEnd,
		detailSessionID: request.SessionDetailID,
		widthTier:       tui.WidthTierFor(request.Width),
		heightTier:      tui.HeightTierFor(request.Height),
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
	info, _ := database.Info()
	return tui.StatusBar{
		History: history.Status, Vault: vaultStatus, Sessions: history.Sessions,
		LastSyncUnix: info.LastSyncUnix, Sources: len(roots), Models: info.DistinctModels,
	}
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

type dashboardModelSessionData struct {
	ByModel map[modelCostKey]int
	Total   int
}

func loadDashboardModelSessionData(path string, request tui.Request) dashboardModelSessionData {
	info, err := historystore.Inspect(path)
	if err != nil || !info.Exists {
		return dashboardModelSessionData{}
	}
	database, err := historystore.OpenReadOnly(path)
	if err != nil {
		return dashboardModelSessionData{}
	}
	defer database.Close()
	stats, err := database.ListCatalogModelSessions(historystore.CatalogQuery{
		Provider: historyProvider(request.Provider),
		Source:   historystore.CatalogSourceAny,
	})
	if err != nil {
		return dashboardModelSessionData{}
	}
	data := dashboardModelSessionData{ByModel: make(map[modelCostKey]int, len(stats.Rows)), Total: stats.Total}
	for _, row := range stats.Rows {
		data.ByModel[modelCostKey{Provider: discover.Provider(row.Provider), Model: row.Model}] = row.Sessions
	}
	return data
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
	vaultPage := tuipages.VaultPageData{}
	if doctorErr == nil || doctor.Vault.Dir != "" {
		vaultPage = dashboardVaultPageData(doctor.Vault)
		if vaultPage.Directory != "" {
			bundles, bundleErr := dashboardVaultBundles(databasePath, vaultPage.Directory)
			if bundleErr != nil {
				warnings = append(warnings, "Vault bundle data could not be loaded; archive health remains available.")
			} else {
				vaultPage.Bundles = bundles
			}
		}
	}
	systemPage := dashboardSystemPageData(doctor, table, warnings)
	if doctorErr != nil {
		warnings = append(warnings, "System health data could not be loaded; spend pages remain available.")
		if pricingErr != nil {
			warnings = append(warnings, "Pricing data could not be loaded; other dashboard pages remain available.")
		}
		systemPage = dashboardSystemPageData(doctor, table, warnings)
		if pricingErr != nil {
			systemPage.Pricing = nil
			systemPage.PricingDisclaimer = ""
		}
		return vaultPage, systemPage
	}
	if pricingErr != nil {
		warnings = append(warnings, "Pricing data could not be loaded; other dashboard pages remain available.")
		systemPage = dashboardSystemPageData(doctor, table, warnings)
	}
	return vaultPage, systemPage
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
		RawBytes:           data.RawBytes,
		StoredBytes:        data.StoredBytes,
		ReclaimableBytes:   data.ReclaimableBytes,
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

func dashboardVaultBundles(databasePath, vaultDir string) ([]tuipages.VaultBundle, error) {
	database, err := store.OpenReadOnly(databasePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open vault manifest: %w", err)
	}
	defer database.Close()
	files, err := database.VaultFiles()
	if err != nil {
		return nil, fmt.Errorf("read vault manifest: %w", err)
	}
	type bundleSummary struct {
		archive  string
		files    int
		rawBytes int64
		vaulted  int64
	}
	summaries := make(map[string]*bundleSummary)
	for _, file := range files {
		if file.Archive == "" {
			continue
		}
		summary := summaries[file.Archive]
		if summary == nil {
			summary = &bundleSummary{archive: file.Archive}
			summaries[file.Archive] = summary
		}
		summary.files++
		summary.rawBytes += file.Size
		summary.vaulted = max(summary.vaulted, file.VaultedAt)
	}
	values := make([]bundleSummary, 0, len(summaries))
	for _, summary := range summaries {
		values = append(values, *summary)
	}
	sort.SliceStable(values, func(left, right int) bool {
		if values[left].vaulted != values[right].vaulted {
			return values[left].vaulted > values[right].vaulted
		}
		return values[left].archive < values[right].archive
	})
	result := make([]tuipages.VaultBundle, 0, len(values))
	for _, summary := range values {
		storedBytes := int64(0)
		status := "present"
		info, statErr := os.Stat(filepath.Join(vaultDir, filepath.FromSlash(summary.archive)))
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				status = "missing"
			} else {
				return nil, fmt.Errorf("stat vault archive %q: %w", summary.archive, statErr)
			}
		} else {
			storedBytes = info.Size()
		}
		date := "unknown"
		if summary.vaulted > 0 {
			date = time.Unix(summary.vaulted, 0).UTC().Format("2006-01-02")
		}
		result = append(result, tuipages.VaultBundle{
			Date: date, Files: summary.files, RawSize: humanBytes(summary.rawBytes),
			StoredSize: humanBytes(storedBytes), Status: status,
		})
	}
	return result, nil
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

	scheduleAvailable := dashboardScheduleDataAvailable(data.Schedule)
	scheduleState := tuipages.FindingMuted
	scheduleValue := "unavailable"
	if scheduleAvailable {
		scheduleValue = "not installed"
	}
	if data.Schedule.Installed {
		scheduleState = tuipages.FindingOK
		scheduleValue = "installed"
		if data.Schedule.IntervalDrift || !data.Schedule.DefinitionExists || !data.Schedule.BinaryExists {
			scheduleState = tuipages.FindingWarning
			scheduleValue = "installed · needs refresh"
		}
	}
	findings = append(findings, tuipages.SystemFinding{Name: "Schedule", Value: scheduleValue, State: scheduleState})

	sources := make([]tuipages.SystemSource, 0, len(data.Providers))
	for _, provider := range data.Providers {
		sources = append(sources, tuipages.SystemSource{
			Name: providerName(discover.Provider(provider.Provider)), Files: provider.JSONLFiles,
			Size: humanBytes(provider.TotalBytes), Exists: provider.Exists, Warning: len(provider.WalkErrors) > 0,
		})
	}
	installedInterval := ""
	if data.Schedule.InstalledIntervalText != nil {
		installedInterval = *data.Schedule.InstalledIntervalText
	}
	return tuipages.SystemPageData{
		Findings: findings, Warnings: append([]string(nil), warnings...), Pricing: dashboardPricingRows(table), PricingDisclaimer: pricingDisclaimer,
		Schedule: tuipages.SystemSchedule{
			Available: scheduleAvailable, Installed: data.Schedule.Installed, DefinitionExists: data.Schedule.DefinitionExists, BinaryExists: data.Schedule.BinaryExists,
			IntervalDrift: data.Schedule.IntervalDrift, Mechanism: data.Schedule.Mechanism,
			ConfiguredInterval: data.Schedule.ConfiguredInterval, InstalledInterval: installedInterval,
		},
		Sources: sources,
	}
}

func dashboardScheduleDataAvailable(data jsonScheduleData) bool {
	return data.Installed || data.DefinitionExists || data.BinaryExists || data.Mechanism != "" || data.ConfiguredInterval != "" || data.InstalledIntervalText != nil
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
			Status: entry.ProvenanceLabel(), Effective: effectiveWindow(entry), Source: entry.Source, Override: override,
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
	return dashboardSnapshotWithDailySessionsAndModelSessions(database, request, render, location, syncSummary, nil, dashboardModelSessionData{})
}

func dashboardSnapshotWithDailySessions(database *store.Store, request tui.Request, render theme.Context, location *time.Location, syncSummary syncer.Summary, loadDailySessions func(string) tuipages.DailySessionData) (tui.Snapshot, error) {
	return dashboardSnapshotWithDailySessionsAndModelSessions(database, request, render, location, syncSummary, loadDailySessions, dashboardModelSessionData{})
}

func dashboardSnapshotWithModelSessions(database *store.Store, request tui.Request, render theme.Context, location *time.Location, syncSummary syncer.Summary, modelSessions dashboardModelSessionData) (tui.Snapshot, error) {
	return dashboardSnapshotWithDailySessionsAndModelSessions(database, request, render, location, syncSummary, nil, modelSessions)
}

func dashboardSnapshotWithDailySessionsAndModelSessions(database *store.Store, request tui.Request, render theme.Context, location *time.Location, syncSummary syncer.Summary, loadDailySessions func(string) tuipages.DailySessionData, modelSessions dashboardModelSessionData) (tui.Snapshot, error) {
	info, err := database.Info()
	if err != nil {
		return tui.Snapshot{}, err
	}
	now := time.Now().In(location)
	filter := dashboardFilter(request, now)
	totals, err := database.Totals(filter)
	if err != nil {
		return tui.Snapshot{}, err
	}
	modelFilter := filter
	// Models is an explicitly all-time attribution view; the page labels that scope.
	modelFilter.Since = ""
	modelFilter.Until = ""
	models, err := database.ByModel(modelFilter)
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
	modelUsage, err := database.FilteredUsageRows(modelFilter)
	if err != nil {
		return tui.Snapshot{}, err
	}
	modelCosts := calculateReportCosts(pricingTable, modelUsage)
	dailyRows, err := database.Daily(filter)
	if err != nil {
		return tui.Snapshot{}, err
	}
	railRows, railCosts := dailyRows, costs
	if request.Range != tui.Range30Days {
		railRequest := request
		railRequest.Range = tui.Range30Days
		railFilter := dashboardFilter(railRequest, now)
		railRows, err = database.Daily(railFilter)
		if err != nil {
			return tui.Snapshot{}, err
		}
		railCosts, err = loadReportCostsWithTable(database, railFilter, nil, pricingTable)
		if err != nil {
			return tui.Snapshot{}, err
		}
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
	snapshot.Rail = dashboardRailData(railRows, railCosts, now)
	dailyView, err := dashboardDailyView(database, dailyRows, filter, costs, pricingTable, request, render, loadDailySessions)
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
	snapshot.Views[tui.ModelsTab] = dashboardModelsView(models, modelCosts, modelUsage, pricingTable, request, dateOnly(now).Format(heatmapDateLayout), modelSessions, render)
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

func dashboardRailData(rows []store.DailyRow, costs reportCosts, now time.Time) tui.RailData {
	today := dateOnly(now)
	sevenStart := today.AddDate(0, 0, -6).Format(heatmapDateLayout)
	thirtyStart := today.AddDate(0, 0, -29).Format(heatmapDateLayout)
	todayKey := today.Format(heatmapDateLayout)
	sevenDays := railWindowCost(rows, costs.ByDate, sevenStart, todayKey)
	thirtyByDate := railWindowValues(rows, costs.ByDate, thirtyStart, todayKey)
	thirtyDays := sumAggregateCosts(thirtyByDate)
	mixCosts := railProviderCosts(rows, costs.ByDateProvider, thirtyStart, todayKey)
	peak, peakDate := peakDailyCostWithDate(thirtyByDate)
	return tui.RailData{
		Snapshot: tui.RailSnapshot{
			Today:     formatCost(costs.ByDate[todayKey]),
			SevenDays: formatCost(sevenDays), ThirtyDays: formatCost(thirtyDays),
			Peak: formatCost(peak), PeakDate: peakDate,
		},
		Mix: tui.RailMix{
			Codex:  railProviderShare(mixCosts[discover.ProviderCodex], mixCosts[discover.ProviderClaude]),
			Claude: railProviderShare(mixCosts[discover.ProviderClaude], mixCosts[discover.ProviderCodex]),
		},
	}
}

func railWindowCost(rows []store.DailyRow, byDate map[string]aggregateCost, since, until string) aggregateCost {
	return sumAggregateCosts(railWindowValues(rows, byDate, since, until))
}

func railWindowValues(rows []store.DailyRow, byDate map[string]aggregateCost, since, until string) map[string]aggregateCost {
	values := make(map[string]aggregateCost)
	for _, row := range rows {
		if row.Date < since || row.Date > until {
			continue
		}
		values[row.Date] = byDate[row.Date]
	}
	return values
}

func sumAggregateCosts(values map[string]aggregateCost) aggregateCost {
	var total aggregateCost
	for _, value := range values {
		total = addAggregateCost(total, value)
	}
	return total
}

func railProviderCosts(rows []store.DailyRow, byDateProvider map[string]map[discover.Provider]providerChartValue, since, until string) map[discover.Provider]aggregateCost {
	result := make(map[discover.Provider]aggregateCost)
	for _, row := range rows {
		if row.Date < since || row.Date > until {
			continue
		}
		for provider, value := range byDateProvider[row.Date] {
			result[provider] = addAggregateCost(result[provider], value.Cost)
		}
	}
	return result
}

func railProviderShare(value, other aggregateCost) float64 {
	valueAmount, otherAmount := float64(value.UnpricedTokens), float64(other.UnpricedTokens)
	if value.PricedTokens > 0 || other.PricedTokens > 0 {
		valueAmount, otherAmount = float64(value.Total), float64(other.Total)
	}
	if valueAmount+otherAmount == 0 {
		return 0
	}
	return valueAmount / (valueAmount + otherAmount)
}

func peakDailyCost(byDate map[string]aggregateCost) (pricing.Money, bool) {
	peak, peakDate := peakDailyCostWithDate(byDate)
	return peak.Total, peakDate != ""
}

func peakDailyCostWithDate(byDate map[string]aggregateCost) (aggregateCost, string) {
	var peak aggregateCost
	peakDate := ""
	for date, cost := range byDate {
		if cost.PricedTokens == 0 {
			continue
		}
		if peakDate == "" || cost.Total > peak.Total || cost.Total == peak.Total && date < peakDate {
			peak, peakDate = cost, date
		}
	}
	return peak, peakDate
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
	dashboardDailyChartPoints     = 30
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

func dashboardDailyView(database *store.Store, allRows []store.DailyRow, filter store.Filter, costs reportCosts, pricingTable pricing.Table, request tui.Request, render theme.Context, loadDailySessions func(string) tuipages.DailySessionData) (dashboardDailyViewResult, error) {
	selectedIndex := dailyCursorIndex(allRows, request.DailyCursor)
	capacity := dashboardRowCapacity(request.Width, request.Height)
	windowStart := normalizedDailyWindowStart(allRows, selectedIndex, capacity, request.DailyWindowStart)
	selectedDate := ""
	if selectedIndex >= 0 {
		selectedDate = allRows[selectedIndex].Date
	}
	detail := dashboardDailyDetail{}
	if selectedDate != "" {
		var err error
		detail, err = loadDashboardDailyDetail(database, filter, selectedDate, pricingTable)
		if err != nil {
			return dashboardDailyViewResult{}, err
		}
	}
	sessions := tuipages.DailySessionData{}
	if selectedDate != "" {
		switch {
		case loadDailySessions != nil:
			sessions = loadDailySessions(selectedDate)
		case request.Initial:
			// The first frame deliberately skips the ambient history index. Keep that
			// absence distinct from an indexed day with no sessions.
			sessions.Pending = true
		}
	}
	data := dashboardDailyPageData(allRows, filter, costs, detail, selectedDate, sessions)
	bodyHeight := tui.ContentHeightFor(request.Width, request.Height)
	detailMaxOffset := tuipages.DailyDetailMaxOffset(data, render.Width, request.Width, request.Height, bodyHeight)
	detailOffset := min(max(0, request.DailyDetailOffset), detailMaxOffset)
	view := tuipages.RenderDaily(render, data, request.Width, request.Height, bodyHeight, detailOffset)
	return dashboardDailyViewResult{view: view, windowStart: windowStart, detailOffset: detailOffset, detailMaxOffset: detailMaxOffset}, nil
}

func dashboardDailyPageData(allRows []store.DailyRow, filter store.Filter, costs reportCosts, detail dashboardDailyDetail, selectedDate string, sessions tuipages.DailySessionData) tuipages.DailyPageData {
	trendRows := dashboardDailyPoints(allRows, filter, costs, sessions.SessionCounts)
	for index := range trendRows {
		trendRows[index].Selected = trendRows[index].Date == selectedDate
	}
	chartRows := trendRows
	chartNotice := ""
	if len(chartRows) > dashboardDailyChartPoints {
		chartNotice = fmt.Sprintf("%d-day rollup", len(chartRows))
		chartRows = compressDashboardDailyPoints(chartRows, dashboardDailyChartPoints)
	}

	var totalTokens int64
	var totalSessions int
	for _, row := range trendRows {
		totalTokens += row.Total.Tokens
		totalSessions += row.Total.Sessions
	}
	average := dashboardDailyPageValue(costs.Grand, totalTokens, totalSessions)
	averageSessions := 0.0
	if len(trendRows) > 0 {
		averageSessions = float64(totalSessions) / float64(len(trendRows))
		average.Cost /= pricing.Money(len(trendRows))
		average.Tokens /= int64(len(trendRows))
		average.UnpricedTokens /= int64(len(trendRows))
		average.Sessions /= len(trendRows)
		if costs.Grand.PricedTokens > 0 {
			average.PricedTokens = 1
		}
	}
	usesTokens := chartUsesTokens(costs)
	peak, peakDate := peakDailyCostWithDate(costs.ByDate)
	peakValue := dashboardDailyPageValue(peak, 0, 0)
	if usesTokens {
		peakValue, peakDate = tuipages.DailyValue{}, ""
		for _, row := range trendRows {
			if row.Total.Tokens > peakValue.Tokens {
				peakValue, peakDate = row.Total, row.Date
			}
		}
	} else if peakDate == "" {
		for _, row := range trendRows {
			if row.Total.Tokens > peakValue.Tokens {
				peakValue, peakDate = row.Total, row.Date
			}
		}
	}

	page := tuipages.DailyPageData{
		Rows: chartRows, TrendRows: trendRows, SelectedDate: selectedDate, Sessions: sessions,
		Average: average, AverageSessions: averageSessions, Peak: peakValue, PeakDate: peakDate, UsesTokens: usesTokens, ChartNotice: chartNotice,
		RangeStart: filter.Since, RangeEnd: filter.Until,
	}
	if detail.breakdown.Date != "" {
		page.Detail = tuipages.DailyDetail{
			Date:  detail.breakdown.Date,
			Value: dashboardDailyPageValue(detail.costs.Grand, detail.breakdown.Total, sessions.Total),
		}
		page.DetailUsesTokens = chartUsesTokens(detail.costs) || detail.costs.Grand.Total == 0 && detail.breakdown.Total > 0
		for _, provider := range detail.breakdown.Providers {
			value := detail.costs.ByProvider[provider.Provider]
			sessionCount := sessions.ProviderCounts[string(provider.Provider)]
			if sessionCount == 0 {
				sessionCount = dailySessionProviderCount(sessions.Rows, string(provider.Provider))
			}
			page.Detail.Providers = append(page.Detail.Providers, tuipages.DailyProvider{
				Provider: string(provider.Provider),
				Value:    dashboardDailyPageValue(value, provider.Total, sessionCount),
			})
		}
		for _, model := range detail.breakdown.Models {
			value := detail.costs.ByModel[modelCostKey{Provider: model.Provider, Model: model.Model}]
			page.Detail.Models = append(page.Detail.Models, tuipages.DailyModel{
				Provider: string(model.Provider), Model: model.Model,
				Value: dashboardDailyPageValue(value, model.Total, 0),
			})
		}
	}
	return page
}

func dashboardDailyPoints(allRows []store.DailyRow, filter store.Filter, costs reportCosts, sessionCounts map[string]int) []tuipages.DailyPoint {
	if len(allRows) == 0 {
		return nil
	}
	byDate := make(map[string]store.DailyRow, len(allRows))
	for _, row := range allRows {
		byDate[row.Date] = row
	}
	dates := make([]string, 0, len(allRows))
	if filter.Since != "" && filter.Until != "" {
		start, startErr := time.Parse(heatmapDateLayout, filter.Since)
		end, endErr := time.Parse(heatmapDateLayout, filter.Until)
		if startErr == nil && endErr == nil && !end.Before(start) && end.Sub(start) <= 366*24*time.Hour {
			for date := start; !date.After(end); date = date.AddDate(0, 0, 1) {
				dates = append(dates, date.Format(heatmapDateLayout))
			}
		}
	}
	if len(dates) == 0 {
		for _, row := range allRows {
			dates = append(dates, row.Date)
		}
	}
	points := make([]tuipages.DailyPoint, 0, len(dates))
	for _, date := range dates {
		row := byDate[date]
		point := tuipages.DailyPoint{Date: date, Total: dashboardDailyPageValue(costs.ByDate[date], row.Total, sessionCounts[date])}
		providerValues := costs.ByDateProvider[date]
		codex := providerValues[discover.ProviderCodex]
		claude := providerValues[discover.ProviderClaude]
		point.Codex = dashboardDailyPageValue(codex.Cost, codex.Tokens, 0)
		point.Codex.PricedTokens = codex.Cost.PricedTokens
		point.Codex.UnpricedTokens = codex.Cost.UnpricedTokens
		point.Claude = dashboardDailyPageValue(claude.Cost, claude.Tokens, 0)
		point.Claude.PricedTokens = claude.Cost.PricedTokens
		point.Claude.UnpricedTokens = claude.Cost.UnpricedTokens
		points = append(points, point)
	}
	return points
}

func compressDashboardDailyPoints(points []tuipages.DailyPoint, limit int) []tuipages.DailyPoint {
	if len(points) <= limit || limit <= 0 {
		return points
	}
	bucketSize := (len(points) + limit - 1) / limit
	result := make([]tuipages.DailyPoint, 0, limit)
	for start := 0; start < len(points); start += bucketSize {
		end := min(len(points), start+bucketSize)
		bucket := tuipages.DailyPoint{Date: points[start].Date}
		for _, point := range points[start:end] {
			bucket.Total = addDashboardDailyPageValues(bucket.Total, point.Total)
			bucket.Codex = addDashboardDailyPageValues(bucket.Codex, point.Codex)
			bucket.Claude = addDashboardDailyPageValues(bucket.Claude, point.Claude)
			bucket.Selected = bucket.Selected || point.Selected
		}
		bucket.Total = averageDashboardDailyPageValue(bucket.Total, end-start)
		bucket.Codex = averageDashboardDailyPageValue(bucket.Codex, end-start)
		bucket.Claude = averageDashboardDailyPageValue(bucket.Claude, end-start)
		result = append(result, bucket)
	}
	return result
}

func averageDashboardDailyPageValue(value tuipages.DailyValue, count int) tuipages.DailyValue {
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

func addDashboardDailyPageValues(left, right tuipages.DailyValue) tuipages.DailyValue {
	left.Cost += right.Cost
	left.Tokens += right.Tokens
	left.PricedTokens += right.PricedTokens
	left.UnpricedTokens += right.UnpricedTokens
	left.Sessions += right.Sessions
	return left
}

func dashboardDailyPageValue(cost aggregateCost, tokens int64, sessions int) tuipages.DailyValue {
	return tuipages.DailyValue{Cost: cost.Total, Tokens: tokens, PricedTokens: cost.PricedTokens, UnpricedTokens: cost.UnpricedTokens, Sessions: sessions}
}

func dailySessionProviderCount(rows []tuipages.DailySession, provider string) int {
	count := 0
	for _, row := range rows {
		if row.Provider == provider {
			count++
		}
	}
	return count
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
	pricingTable, err := loadPricingTable()
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
	analytics, err := dashboardLedgerAnalytics(database, filter, costs, pricingTable, daily, effectiveYear)
	if err != nil {
		return tuipages.Data{}, err
	}
	if zoom == tuipages.ZoomMonth {
		if hasLedgerYearData(daily, effectiveYear) {
			rows = ledgerMonthRowsForYear(analytics.Months, effectiveYear)
		} else {
			rows = nil
		}
		total = tuipages.Row{Key: "total", Label: "TOTAL"}
		for _, row := range rows {
			total = total.Add(row)
		}
	}
	data := tuipages.Data{Available: true, Zoom: zoom, Year: effectiveYear, Month: effectiveMonth, Rows: rows, Total: total, Analytics: analytics}
	if zoom == tuipages.ZoomDay && request.Ledger.ExpandedDay != "" {
		dayFilter := filter
		dayFilter.Since, dayFilter.Until = request.Ledger.ExpandedDay, request.Ledger.ExpandedDay
		dayRows, err := database.FilteredUsageRows(dayFilter)
		if err != nil {
			return tuipages.Data{}, err
		}
		dayCosts := calculateReportCosts(pricingTable, dayRows)
		dayModels, err := database.ByModel(dayFilter)
		if err != nil {
			return tuipages.Data{}, err
		}
		allDayModels := dashboardLedgerModels(dayModels, dayCosts, pricingTable)
		data.DayModels = boundedLedgerModels(allDayModels)
		data.DayModelTotalCost, data.DayModelTotalTokens = dashboardLedgerModelTotals(allDayModels)
		data.DayModelCount = len(allDayModels)
	}
	return data, nil
}

func dashboardLedgerModels(models []store.ModelRow, costs reportCosts, pricingTable pricing.Table) []tuipages.LedgerModel {
	result := make([]tuipages.LedgerModel, 0, len(models))
	for _, model := range models {
		value := costs.ByModel[modelCostKey{Provider: model.Provider, Model: model.Model}]
		row := tuipages.LedgerModel{
			Provider: string(model.Provider), Model: model.Model, Tokens: model.Total,
			Cost: value.Total, PricedTokens: value.PricedTokens, UnpricedTokens: value.UnpricedTokens,
			CostPerMillion: weightedRate(value.Total, value.PricedTokens),
		}
		entry, found := pricingTable.RateFor(model.Model, model.LastDate)
		if found {
			row.HasRate, row.Status, row.Source = true, entry.ProvenanceLabel(), entry.Source
		}
		result = append(result, row)
	}
	return result
}

func dashboardLedgerAnalytics(database *store.Store, filter store.Filter, costs reportCosts, pricingTable pricing.Table, daily []store.DailyRow, selectedYear int) (tuipages.LedgerAnalytics, error) {
	if selectedYear == 0 {
		selectedYear = time.Now().Year()
	}
	years := map[int]bool{selectedYear: true}
	for key := range costs.ByMonthProvider {
		if len(key) >= 4 {
			if year, err := strconv.Atoi(key[:4]); err == nil {
				years[year] = true
			}
		}
	}
	for _, row := range daily {
		if len(row.Date) >= 4 {
			if year, err := strconv.Atoi(row.Date[:4]); err == nil {
				years[year] = true
			}
		}
	}
	orderedYears := make([]int, 0, len(years))
	for year := range years {
		orderedYears = append(orderedYears, year)
	}
	sort.Ints(orderedYears)
	activeDaysByMonth := make(map[string]int)
	for _, row := range daily {
		if len(row.Date) >= 7 {
			activeDaysByMonth[row.Date[:7]]++
		}
	}
	months := make([]tuipages.LedgerMonth, 0, len(orderedYears)*12)
	for _, year := range orderedYears {
		for month := 1; month <= 12; month++ {
			key := fmt.Sprintf("%04d-%02d", year, month)
			value := tuipages.LedgerMonth{Key: key, Label: ledgerPeriodLabel(key, tuipages.ZoomMonth), ActiveDays: activeDaysByMonth[key]}
			for provider, chart := range costs.ByMonthProvider[key] {
				addLedgerProviderMonth(&value, provider, chart)
			}
			if value.ActiveDays > 0 {
				value.AverageCost = value.Total().Cost / pricing.Money(value.ActiveDays)
			}
			for date, cost := range costs.ByDate {
				if len(date) >= 7 && date[:7] == key && cost.Total > value.PeakCost {
					value.PeakCost, value.PeakDay, value.PeakPartial = cost.Total, date, cost.UnpricedTokens > 0
				}
			}
			months = append(months, value)
		}
	}
	models, err := database.ByModel(filter)
	if err != nil {
		return tuipages.LedgerAnalytics{}, err
	}
	analytics := tuipages.LedgerAnalytics{Months: months, Models: dashboardLedgerModels(models, costs, pricingTable)}
	analytics.Provenance, err = ledgerPricingProvenance(database, filter, pricingTable)
	if err != nil {
		return tuipages.LedgerAnalytics{}, err
	}
	// Store.Daily returns one aggregate row per active calendar date.
	analytics.ActiveDays = len(daily)
	if analytics.ActiveDays > 0 {
		analytics.AverageCost = costs.Grand.Total / pricing.Money(analytics.ActiveDays)
	}
	for date, value := range costs.ByDate {
		if value.Total > analytics.PeakCost {
			analytics.PeakCost, analytics.PeakDay = value.Total, date
		}
	}
	for _, month := range months {
		for _, provider := range []discover.Provider{discover.ProviderCodex, discover.ProviderClaude} {
			value := costs.ByMonthProvider[month.Key][provider]
			analytics.ProviderMonths = append(analytics.ProviderMonths, tuipages.LedgerProviderMonth{
				Provider: string(provider), Month: month.Key,
				Cost: value.Cost.Total, Tokens: value.Tokens,
			})
		}
	}
	return analytics, nil
}

func ledgerMonthRowsForYear(months []tuipages.LedgerMonth, year int) []tuipages.Row {
	rows := make([]tuipages.Row, 0, len(months))
	for _, month := range months {
		if year > 0 && (len(month.Key) < 4 || month.Key[:4] != fmt.Sprintf("%04d", year)) {
			continue
		}
		rows = append(rows, tuipages.Row{Key: month.Key, Label: month.Label, Sessions: month.Sessions, Codex: month.Codex, Claude: month.Claude})
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	return rows
}

func addLedgerProviderMonth(month *tuipages.LedgerMonth, provider discover.Provider, chart providerChartValue) {
	value := tuipages.ProviderTotals{Cost: chart.Cost.Total, Tokens: chart.Tokens, PricedTokens: chart.Cost.PricedTokens, UnpricedTokens: chart.Cost.UnpricedTokens}
	switch provider {
	case discover.ProviderCodex:
		month.Codex = month.Codex.Add(value)
	case discover.ProviderClaude:
		month.Claude = month.Claude.Add(value)
	}
}

func weightedRate(cost pricing.Money, pricedTokens int64) pricing.Rate {
	if pricedTokens <= 0 {
		return 0
	}
	return pricing.Rate(int64(cost) / pricedTokens)
}

func ledgerPricingProvenance(database *store.Store, filter store.Filter, table pricing.Table) (tuipages.LedgerProvenance, error) {
	rows, err := database.FilteredUsageRows(filter)
	if err != nil {
		return tuipages.LedgerProvenance{}, err
	}
	type modelPricing struct {
		unpriced bool
		statuses map[string]bool
		costs    map[string]pricing.Money
		tokens   map[string]int64
	}
	type modelKey struct {
		provider discover.Provider
		model    string
	}
	byModel := make(map[modelKey]*modelPricing)
	for _, row := range rows {
		key := modelKey{provider: row.Provider, model: row.Model}
		pricingInfo := byModel[key]
		if pricingInfo == nil {
			pricingInfo = &modelPricing{statuses: map[string]bool{}, costs: map[string]pricing.Money{}, tokens: map[string]int64{}}
			byModel[key] = pricingInfo
		}
		breakdown := table.Cost(row)
		if breakdown.UnpricedTokens > 0 {
			pricingInfo.unpriced = true
		}
		if breakdown.PricedTokens == 0 {
			continue
		}
		entry, found := table.RateFor(row.Model, row.Date)
		if !found {
			pricingInfo.unpriced = true
			continue
		}
		pricingInfo.statuses[entry.Status] = true
		pricingInfo.costs[entry.Status] += breakdown.Total
		pricingInfo.tokens[entry.Status] += breakdown.PricedTokens
	}
	models := make([]modelKey, 0, len(byModel))
	for model := range byModel {
		models = append(models, model)
	}
	sort.Slice(models, func(left, right int) bool {
		if models[left].model != models[right].model {
			return models[left].model < models[right].model
		}
		return models[left].provider < models[right].provider
	})
	provenance := tuipages.LedgerProvenance{}
	for _, model := range models {
		pricingInfo := byModel[model]
		if pricingInfo.unpriced {
			provenance.UnpricedModels++
			provenance.Unpriced = append(provenance.Unpriced, string(model.provider)+"/"+model.model)
		}
		for status := range pricingInfo.statuses {
			switch status {
			case "proxy":
				provenance.ProxyModels++
				provenance.ProxyCost += pricingInfo.costs[status]
				provenance.ProxyTokens += pricingInfo.tokens[status]
			case "estimated":
				provenance.EstimatedModels++
				provenance.EstimatedCost += pricingInfo.costs[status]
				provenance.EstimatedTokens += pricingInfo.tokens[status]
			case "published":
				provenance.PublishedModels++
				provenance.PublishedCost += pricingInfo.costs[status]
				provenance.PublishedTokens += pricingInfo.tokens[status]
			case "user":
				provenance.UserModels++
				provenance.UserCost += pricingInfo.costs[status]
				provenance.UserTokens += pricingInfo.tokens[status]
			}
		}
	}
	return provenance, nil
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

func hasLedgerYearData(rows []store.DailyRow, year int) bool {
	if year == 0 {
		return len(rows) > 0
	}
	want := fmt.Sprintf("%04d", year)
	for _, row := range rows {
		if len(row.Date) >= 4 && row.Date[:4] == want {
			return true
		}
	}
	return false
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

func dashboardModelsView(rows []store.ModelRow, costs reportCosts, usage []store.Usage, table pricing.Table, request tui.Request, reportDate string, modelSessions dashboardModelSessionData, render theme.Context) string {
	pageWidth := render.Width
	if pageWidth <= 0 {
		pageWidth = tui.ContentWidth(request.Width)
	}
	data := dashboardModelPageData(rows, costs, usage, table, request.ModelSort, reportDate, modelSessions)
	return tuipages.RenderModels(render, data, tuipages.ModelsViewport{
		Width: pageWidth, Height: tui.ContentHeightFor(request.Width, request.Height),
		Wide:     tui.WidthTierFor(request.Width) == tui.WidthWide,
		Standard: tui.WidthTierFor(request.Width) == tui.WidthStandard,
		Tall:     tui.HeightTierFor(request.Height) == tui.HeightTall,
		Sort:     request.ModelSort, Offset: request.ModelOffset,
	})
}

type dashboardModelPricingStats struct {
	PricedTokens int64
	Unpriced     bool
	Statuses     map[string]int64
}

func dashboardModelPageData(rows []store.ModelRow, costs reportCosts, usage []store.Usage, table pricing.Table, sortMode int, reportDate string, modelSessions dashboardModelSessionData) tuipages.ModelPageData {
	data := tuipages.ModelPageData{ScopeLabel: "ALL TIME"}
	if len(rows) == 0 {
		return data
	}

	dailyCosts := make(map[modelCostKey]map[string]pricing.Money, len(rows))
	pricingStats := make(map[modelCostKey]dashboardModelPricingStats, len(rows))
	dateSet := make(map[string]bool)
	for _, value := range usage {
		key := modelCostKey{Provider: value.Provider, Model: value.Model}
		if dailyCosts[key] == nil {
			dailyCosts[key] = make(map[string]pricing.Money)
		}
		breakdown := table.Cost(value)
		dailyCosts[key][value.Date] += breakdown.Total
		dateSet[value.Date] = true
		stats := pricingStats[key]
		if stats.Statuses == nil {
			stats.Statuses = make(map[string]int64)
		}
		stats.PricedTokens += breakdown.PricedTokens
		if breakdown.UnpricedTokens > 0 {
			stats.Unpriced = true
		}
		if breakdown.PricedTokens > 0 {
			entry, found := table.RateFor(value.Model, value.Date)
			if found {
				stats.Statuses[modelPricingStatus(entry.Status)] += breakdown.PricedTokens
			}
		}
		pricingStats[key] = stats
	}

	dates := make([]string, 0, len(dateSet))
	for date := range dateSet {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	recentDates := modelCalendarDates(dates, reportDate)

	var totalTokens int64
	var totalCost pricing.Money
	var totalPriced, totalUnpriced int64
	for _, row := range rows {
		key := modelCostKey{Provider: row.Provider, Model: row.Model}
		value := costs.ByModel[key]
		totalTokens += row.Total
		totalCost += value.Total
		totalPriced += value.PricedTokens
		totalUnpriced += value.UnpricedTokens
		stats := pricingStats[key]
		modelRow := tuipages.ModelPageRow{
			Provider: string(row.Provider), Model: row.Model, Tokens: row.Total,
			Cost: value.Total, PricedTokens: value.PricedTokens, UnpricedTokens: value.UnpricedTokens,
			Pricing: modelPricingLabel(stats), Sessions: modelSessions.ByModel[key],
			Days: row.ActiveDays, FirstDate: row.FirstDate, LastDate: row.LastDate,
		}
		modelRow.Sparkline = modelSparklineValues(dailyCosts[key], recentDates)
		data.Rows = append(data.Rows, modelRow)
	}

	for index := range data.Rows {
		if totalTokens > 0 {
			data.Rows[index].TokenShare = float64(data.Rows[index].Tokens) / float64(totalTokens)
		}
		if totalCost > 0 {
			data.Rows[index].CostShare = float64(data.Rows[index].Cost) / float64(totalCost)
		}
	}
	sort.SliceStable(data.Rows, func(left, right int) bool {
		switch sortMode {
		case 1:
			if data.Rows[left].Cost != data.Rows[right].Cost {
				return data.Rows[left].Cost > data.Rows[right].Cost
			}
		case 2:
			if strings.ToLower(data.Rows[left].Model) != strings.ToLower(data.Rows[right].Model) {
				return strings.ToLower(data.Rows[left].Model) < strings.ToLower(data.Rows[right].Model)
			}
		default:
			if data.Rows[left].Tokens != data.Rows[right].Tokens {
				return data.Rows[left].Tokens > data.Rows[right].Tokens
			}
		}
		return data.Rows[left].Provider < data.Rows[right].Provider
	})

	totalTokenShare, totalCostShare := float64(0), float64(0)
	if totalTokens > 0 {
		totalTokenShare = 1
	}
	if totalCost > 0 {
		totalCostShare = 1
	}
	data.Total = tuipages.ModelPageRow{
		Provider: "TOTAL", Model: fmt.Sprintf("%d models", len(data.Rows)), Tokens: totalTokens,
		Cost: totalCost, PricedTokens: totalPriced, UnpricedTokens: totalUnpriced,
		TokenShare: totalTokenShare, CostShare: totalCostShare, Pricing: fmt.Sprintf("%d priced", countPricedModels(data.Rows)),
		// This is the distinct logical-session count; per-model rows can overlap
		// when one session uses more than one model.
		Sessions: modelSessions.Total,
		Days:     len(dates),
	}
	if len(dates) > 0 {
		data.Total.FirstDate, data.Total.LastDate = dates[0], dates[len(dates)-1]
	}

	providerTotals := make(map[string]*tuipages.ModelProviderRow)
	pricingTotals := make(map[string]*tuipages.ModelPricingRow)
	for _, row := range data.Rows {
		provider := providerTotals[row.Provider]
		if provider == nil {
			provider = &tuipages.ModelProviderRow{Provider: row.Provider}
			providerTotals[row.Provider] = provider
		}
		provider.Models++
		provider.Tokens += row.Tokens
		provider.Cost += row.Cost
		provider.PricedTokens += row.PricedTokens
		provider.UnpricedTokens += row.UnpricedTokens
		label := pricingProvenanceLabel(row.Pricing)
		provenance := pricingTotals[label]
		if provenance == nil {
			provenance = &tuipages.ModelPricingRow{Label: label}
			pricingTotals[label] = provenance
		}
		provenance.Models++
		provenance.Tokens += row.Tokens
		provenance.Cost += row.Cost
		provenance.PricedTokens += row.PricedTokens
		provenance.UnpricedTokens += row.UnpricedTokens
		if row.PricedTokens > 0 {
			data.Rates = append(data.Rates, tuipages.ModelRateRow{Model: row.Model, Cost: row.Cost, PricedTokens: row.PricedTokens})
		}
		if row.UnpricedTokens > 0 {
			data.Unpriced = append(data.Unpriced, tuipages.ModelUnpricedRow{Model: row.Model, Tokens: row.UnpricedTokens})
		}
		data.PerSession = append(data.PerSession, tuipages.ModelPerSessionRow{Model: row.Model, Tokens: row.Tokens, Sessions: row.Sessions})
	}
	for _, value := range providerTotals {
		if totalTokens > 0 {
			value.TokenShare = float64(value.Tokens) / float64(totalTokens)
		}
		if totalCost > 0 {
			value.CostShare = float64(value.Cost) / float64(totalCost)
		}
		data.Providers = append(data.Providers, *value)
	}
	for _, value := range pricingTotals {
		data.Pricing = append(data.Pricing, *value)
	}
	sort.SliceStable(data.Providers, func(left, right int) bool { return data.Providers[left].Tokens > data.Providers[right].Tokens })
	sort.SliceStable(data.Pricing, func(left, right int) bool { return data.Pricing[left].Label < data.Pricing[right].Label })
	sort.SliceStable(data.Rates, func(left, right int) bool {
		return modelRateValue(data.Rates[left]) > modelRateValue(data.Rates[right])
	})
	sort.SliceStable(data.Unpriced, func(left, right int) bool { return data.Unpriced[left].Tokens > data.Unpriced[right].Tokens })
	latestDataDate := ""
	if len(dates) > 0 {
		latestDataDate = dates[len(dates)-1]
	}
	if reportDate == "" {
		reportDate = latestDataDate
	}
	for _, row := range data.Rows {
		data.Recency = append(data.Recency, tuipages.ModelRecencyRow{Model: row.Model, Days: dateDistance(reportDate, row.LastDate)})
	}
	sort.SliceStable(data.Recency, func(left, right int) bool { return data.Recency[left].Days < data.Recency[right].Days })
	for index := range data.PerSession {
		if data.PerSession[index].Sessions > 0 {
			data.PerSession[index].TokensPerSession = data.PerSession[index].Tokens / int64(data.PerSession[index].Sessions)
		}
	}

	for _, row := range data.Rows {
		key := modelCostKey{Provider: discover.Provider(row.Provider), Model: row.Model}
		var matrixCost pricing.Money
		for _, date := range recentDates {
			matrixCost += dailyCosts[key][date]
		}
		matrixRow := tuipages.ModelMatrixRow{Model: row.Model, Cost: matrixCost}
		for _, date := range recentDates {
			matrixRow.Values = append(matrixRow.Values, float64(dailyCosts[key][date]))
		}
		data.Matrix.Rows = append(data.Matrix.Rows, matrixRow)
	}
	data.Matrix.Dates = append([]string(nil), recentDates...)
	return data
}

func modelCalendarDates(activeDates []string, reportDate string) []string {
	end, err := time.Parse(heatmapDateLayout, reportDate)
	if err != nil && len(activeDates) > 0 {
		end, err = time.Parse(heatmapDateLayout, activeDates[len(activeDates)-1])
	}
	if err != nil {
		if len(activeDates) == 0 {
			return nil
		}
		start := max(0, len(activeDates)-30)
		return append([]string(nil), activeDates[start:]...)
	}
	start := end.AddDate(0, 0, -29)
	dates := make([]string, 0, 30)
	for index := 0; index < 30; index++ {
		dates = append(dates, start.AddDate(0, 0, index).Format(heatmapDateLayout))
	}
	return dates
}

func modelSparklineValues(daily map[string]pricing.Money, dates []string) []float64 {
	if len(dates) == 0 {
		return nil
	}
	values := make([]float64, 10)
	for index, date := range dates {
		bucket := index * len(values) / len(dates)
		values[bucket] += float64(daily[date])
	}
	return values
}

func modelPricingStatus(status string) string {
	switch status {
	case "published":
		return "live"
	case "proxy":
		return "proxy"
	case "estimated":
		return "estimated"
	case "user":
		return "user rate"
	default:
		return "unpriced"
	}
}

func modelPricingLabel(stats dashboardModelPricingStats) string {
	if stats.PricedTokens == 0 {
		return "unpriced"
	}
	if stats.Unpriced {
		return "partial"
	}
	best, bestTokens := "live", int64(0)
	for status, tokens := range stats.Statuses {
		if tokens > bestTokens {
			best, bestTokens = status, tokens
		} else if tokens == bestTokens && modelPricingPriority(status) < modelPricingPriority(best) {
			best = status
		}
	}
	return best
}

func modelPricingPriority(status string) int {
	switch status {
	case "live":
		return 1
	case "user rate":
		return 0
	case "proxy":
		return 2
	case "estimated":
		return 3
	default:
		return 4
	}
}

func pricingProvenanceLabel(status string) string {
	switch status {
	case "live":
		return "live rates"
	case "proxy":
		return "proxy rates"
	case "estimated":
		return "estimated"
	case "user":
		return "user rate"
	case "user rate":
		return "user rate"
	case "partial":
		return "partial"
	default:
		return "unpriced"
	}
}

func modelRateValue(row tuipages.ModelRateRow) float64 {
	if row.PricedTokens <= 0 {
		return 0
	}
	return float64(row.Cost) / float64(row.PricedTokens)
}

func countPricedModels(rows []tuipages.ModelPageRow) int {
	count := 0
	for _, row := range rows {
		if row.PricedTokens > 0 {
			count++
		}
	}
	return count
}

func dateDistance(latest, previous string) int {
	if latest == "" || previous == "" {
		return 0
	}
	left, leftErr := time.Parse("2006-01-02", latest)
	right, rightErr := time.Parse("2006-01-02", previous)
	if leftErr != nil || rightErr != nil || right.After(left) {
		return 0
	}
	return int(left.Sub(right).Hours() / 24)
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
	report := buildHeatmapReport(window, filteredRows, costs)
	pageRender := render
	pageRender.Width = request.Width
	return tuipages.RenderHeatmap(pageRender, dashboardHeatmapPageData(report, costs), tui.ContentWidth(request.Width), tui.ContentHeightFor(request.Width, request.Height)), nil
}

func dashboardHeatmapPageData(report heatmapReport, costs reportCosts) tuipages.HeatmapData {
	days := make([]tuipages.HeatmapDay, 0, len(report.Days))
	for _, day := range report.Days {
		key := day.Date.Format(heatmapDateLayout)
		cost := costs.ByDate[key]
		days = append(days, tuipages.HeatmapDay{
			Date: day.Date, Cost: day.Cost, TotalTokens: day.TotalTokens,
			PricedTokens: cost.PricedTokens, Level: day.Level,
		})
	}
	return tuipages.HeatmapData{
		Window: tuipages.HeatmapWindow{From: report.Window.From, To: report.Window.To},
		Days:   days, UsesTokens: report.UsesTokens,
	}
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
