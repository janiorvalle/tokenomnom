package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	"github.com/janiorvalle/tokenomnom/internal/tui"
	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

func TestDashboardSnapshotRendersAllViewsAndFilteredCards(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	render := styledRenderContext(120)
	request := tui.Request{Range: tui.RangeAll, Width: 120, Height: 35}
	snapshot, err := dashboardSnapshot(database, request, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	metrics := snapshot.Summary.Metrics
	if metrics[0].Value != "$0.18" || metrics[1].Value != "206,910" || metrics[2].Value != "3" || metrics[3].Value != "$0.06" || metrics[4].Value != "$0.18" {
		t.Fatalf("dashboard summary = %+v", metrics)
	}
	for index, metric := range metrics {
		if metric.Label != "" {
			t.Errorf("dashboard summary metric %d supplies label %q; TUI owns summary labels", index, metric.Label)
		}
	}
	viewFragments := map[int][]string{
		int(tui.DailyTab):   {"cost/day", "DAY DETAIL", "PROVIDER SPLIT", "TOP MODELS BY"},
		int(tui.ModelsTab):  {"PROVIDER", "MODEL"},
		int(tui.HeatmapTab): {"Less", "active days"},
	}
	for index, fragments := range viewFragments {
		for _, fragment := range fragments {
			if !strings.Contains(snapshot.Views[index], fragment) {
				t.Errorf("view %d missing %q:\n%s", index, fragment, snapshot.Views[index])
			}
		}
	}
	ledgerView := renderDashboardLedger(snapshot, request, render)
	for _, fragment := range []string{"PERIOD", "CODEX", "CLAUDE", "ACTIVITY", "▓"} {
		if !strings.Contains(ledgerView, fragment) {
			t.Errorf("ledger view missing %q:\n%s", fragment, ledgerView)
		}
	}

	codex, err := dashboardSnapshot(database, tui.Request{Provider: tui.CodexProvider, Range: tui.RangeAll, Width: 120, Height: 35}, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	if codex.Summary.Metrics[1].Value != "206,100" || strings.Contains(codex.Views[tui.ModelsTab], "Claude") {
		t.Fatalf("provider filter did not apply: summary=%+v\n%s", codex.Summary.Metrics, codex.Views[tui.ModelsTab])
	}
}

func TestDashboardStatusBarHintsMissingOptionalStores(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cmd := &cobra.Command{}
	cmd.SetContext(appconfig.WithContext(context.Background(), appconfig.Loaded{Config: appconfig.Defaults()}))
	status := dashboardStatusBar(cmd, database, stateDir, root, []discover.Root{
		{Provider: discover.ProviderCodex, Path: filepath.Join(root, "codex")},
		{Provider: discover.ProviderClaude, Path: filepath.Join(root, "claude")},
	})
	if status.History.Exists || status.History.Hint != "not indexed" || status.Sessions != 0 {
		t.Fatalf("missing history status = %+v", status.History)
	}
	if status.Vault.Exists || status.Vault.Hint != "not initialized" {
		t.Fatalf("missing vault status = %+v", status.Vault)
	}
}

func TestDashboardStatusBarReportsFreshIndexedHistory(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "status.jsonl"), historyCodexFixture("status", "status prompt"))
	if _, err := executeReport([]string{"history", "index", "--source", "provider"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cmd := &cobra.Command{}
	cmd.SetContext(appconfig.WithContext(context.Background(), appconfig.Loaded{Config: appconfig.Defaults()}))
	status := dashboardStatusBar(cmd, database, stateDir, root, []discover.Root{
		{Provider: discover.ProviderCodex, Path: codexDir, Exists: true},
		{Provider: discover.ProviderClaude, Path: claudeDir},
	})
	if !status.History.Exists || !status.History.Fresh || status.History.Hint != "" || status.Sessions != 1 {
		t.Fatalf("fresh history status = %+v sessions=%d", status.History, status.Sessions)
	}

	historyDatabase, err := sql.Open("sqlite", filepath.Join(stateDir, historystore.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := historyDatabase.Exec(`UPDATE source_heads SET extractor_version = 0`); err != nil {
		historyDatabase.Close()
		t.Fatal(err)
	}
	if err := historyDatabase.Close(); err != nil {
		t.Fatal(err)
	}
	status = dashboardStatusBar(cmd, database, stateDir, root, []discover.Root{
		{Provider: discover.ProviderCodex, Path: codexDir, Exists: true},
		{Provider: discover.ProviderClaude, Path: claudeDir},
	})
	if status.History.Fresh || status.History.Hint != "stale" {
		t.Fatalf("stale health status = %+v", status.History)
	}
}

func TestDashboardAmbientCacheRefreshesOnlyForSyncLoads(t *testing.T) {
	cache := dashboardAmbientCache{}
	calls := 0
	refresh := func() (tui.StatusBar, int) {
		calls++
		return tui.StatusBar{Sessions: calls}, calls
	}

	status, files := cache.snapshot(tui.Request{}, refresh)
	if status.Sessions != 1 || files != 1 || calls != 1 {
		t.Fatalf("initial ambient snapshot = status=%+v files=%d calls=%d", status, files, calls)
	}
	status, files = cache.snapshot(tui.Request{}, refresh)
	if status.Sessions != 1 || files != 1 || calls != 1 {
		t.Fatalf("cached ambient snapshot = status=%+v files=%d calls=%d", status, files, calls)
	}
	status, files = cache.snapshot(tui.Request{Sync: true}, refresh)
	if status.Sessions != 2 || files != 2 || calls != 2 {
		t.Fatalf("sync ambient snapshot = status=%+v files=%d calls=%d", status, files, calls)
	}
	status, files = cache.snapshot(tui.Request{}, refresh)
	if status.Sessions != 2 || files != 2 || calls != 2 {
		t.Fatalf("post-sync cached snapshot = status=%+v files=%d calls=%d", status, files, calls)
	}
}

func TestDashboardSessionCacheRefreshesWhenQueryChanges(t *testing.T) {
	cache := dashboardSessionCache{}
	calls := 0
	refresh := func() tuipages.SessionPageData {
		calls++
		return tuipages.SessionPageData{Warning: []string{"", "one", "two", "three", "four"}[calls]}
	}
	request := tui.Request{Provider: tui.CodexProvider, Range: tui.Range30Days}

	data := cache.snapshot(request, refresh)
	if data.Warning != "one" || calls != 1 {
		t.Fatalf("initial session snapshot = %+v calls=%d", data, calls)
	}
	request.SessionOffset = 1
	data = cache.snapshot(request, refresh)
	if data.Warning != "one" || calls != 1 {
		t.Fatalf("selection-only session snapshot = %+v calls=%d", data, calls)
	}
	request.SessionCursor = "next"
	data = cache.snapshot(request, refresh)
	if data.Warning != "two" || calls != 2 {
		t.Fatalf("cursor session snapshot = %+v calls=%d", data, calls)
	}
	request.SessionProject, request.SessionProjectActive = "tokenomnom", true
	data = cache.snapshot(request, refresh)
	if data.Warning != "three" || calls != 3 {
		t.Fatalf("project-filter session snapshot = %+v calls=%d", data, calls)
	}
	request.Sync = true
	data = cache.snapshot(request, refresh)
	if data.Warning != "four" || calls != 4 {
		t.Fatalf("sync session snapshot = %+v calls=%d", data, calls)
	}
	request.Sync = false
	data = cache.snapshot(request, refresh)
	if data.Warning != "four" || calls != 4 {
		t.Fatalf("post-sync session snapshot = %+v calls=%d", data, calls)
	}
}

func TestLoadDashboardHistoryReadsCatalogAndProjectOptions(t *testing.T) {
	path := filepath.Join(t.TempDir(), historystore.DatabaseName)
	database, err := historystore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationProviderLive, Path: "/history/session.jsonl"}
	prompt := history.Prompt{
		LogicalKey: "prompt-1", Role: history.RoleUser, CleanText: "show the session detail", Classification: history.ClassificationHuman,
		PromptKind: history.PromptKindHuman, Searchable: true, Timestamp: &when,
	}
	_, err = database.ApplySource(history.Extraction{
		Provider: history.ProviderCodex, Source: source,
		Session: history.Session{
			IdentityKey: "session-1", NativeSessionID: "native-session-1", CWD: "/workspace/tokenomnom",
			RepositoryName: "tokenomnom", FirstTimestamp: &when, LastTimestamp: &when,
		},
		Prompts:     []history.Prompt{prompt},
		Occurrences: []history.Occurrence{{PromptKey: prompt.LogicalKey, Variant: prompt, LineNumber: 1, EndOffset: 10}},
	}, history.SourceHead{Source: source, ContentSHA256: "session-hash", Size: 10, CompleteOffset: 10, LineCount: 1, Available: true}, historystore.ApplyReplace)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	data := loadDashboardHistory(path, tui.Request{Range: tui.RangeAll}, time.UTC)
	if !data.IndexAvailable || len(data.Sessions) != 1 || data.Sessions[0].Project != "tokenomnom" {
		t.Fatalf("dashboard history = %+v", data)
	}
	if len(data.Projects) != 1 || data.Projects[0].Key != "tokenomnom" || data.Projects[0].Label != "tokenomnom" {
		t.Fatalf("dashboard project options = %v", data.Projects)
	}
}

func TestLoadDashboardHistoryMissingIndexShowsNoFalseWarning(t *testing.T) {
	data := loadDashboardHistory(filepath.Join(t.TempDir(), historystore.DatabaseName), tui.Request{}, time.UTC)
	if data.IndexAvailable || data.Warning != "" || len(data.Sessions) != 0 {
		t.Fatalf("missing history index = %+v", data)
	}
}

func TestDashboardHistoryWindowUsesInclusiveDateBounds(t *testing.T) {
	now := time.Date(2026, time.August, 1, 14, 30, 0, 0, time.UTC)
	since, until := dashboardHistoryWindow(tui.Range30Days, time.UTC, now)
	if since == nil || until == nil || !since.Equal(time.Date(2026, time.July, 3, 0, 0, 0, 0, time.UTC)) || !until.Equal(time.Date(2026, time.August, 1, 23, 59, 59, 999999999, time.UTC)) {
		t.Fatalf("history window = %v, %v", since, until)
	}
	allSince, allUntil := dashboardHistoryWindow(tui.RangeAll, time.UTC, now)
	if allSince != nil || allUntil != nil {
		t.Fatalf("all history window = %v, %v", allSince, allUntil)
	}
	springForwardNow := time.Date(2026, time.March, 8, 14, 30, 0, 0, time.UTC)
	springForwardLocation, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	_, springForwardUntil := dashboardHistoryWindow(tui.Range30Days, springForwardLocation, springForwardNow)
	wantSpringForwardUntil := time.Date(2026, time.March, 9, 0, 0, 0, 0, springForwardLocation).Add(-time.Nanosecond)
	if springForwardUntil == nil || !springForwardUntil.Equal(wantSpringForwardUntil) {
		t.Fatalf("spring-forward history window until = %v, want %v", springForwardUntil, wantSpringForwardUntil)
	}
	leapDayNow := time.Date(2024, time.February, 29, 14, 30, 0, 0, time.UTC)
	leapSince, _ := dashboardHistoryWindow(tui.RangeYear, time.UTC, leapDayNow)
	if leapSince == nil || !leapSince.Equal(time.Date(2023, time.March, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("leap-day history window since = %v", leapSince)
	}
}

func TestDashboardLedgerZoomsFromYearToMonthToDay(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	render := styledRenderContext(120)

	yearRequest := tui.Request{Range: tui.RangeAll, Width: 120, Height: 30}
	year, err := dashboardSnapshot(database, yearRequest, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	yearView := renderDashboardLedger(year, yearRequest, render)
	for _, fragment := range []string{"ALL YEARS", "CODEX", "CLAUDE", "ACTIVITY", "TOTAL"} {
		if !strings.Contains(yearView, fragment) {
			t.Errorf("year ledger missing %q:\n%s", fragment, yearView)
		}
	}

	monthRequest := tui.Request{Range: tui.RangeAll, Width: 120, Height: 30, Ledger: tuipages.State{Zoom: tuipages.ZoomMonth, Year: 2026, Cursor: -1}}
	month, err := dashboardSnapshot(database, monthRequest, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	monthView := renderDashboardLedger(month, monthRequest, render)
	if !strings.Contains(monthView, "Jan 2026") || !strings.Contains(monthView, "Feb 2026") {
		t.Fatalf("month ledger did not show 2026 periods:\n%s", monthView)
	}

	dayRequest := tui.Request{Range: tui.RangeAll, Width: 120, Height: 30, Ledger: tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-02", Cursor: -1}}
	day, err := dashboardSnapshot(database, dayRequest, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	dayView := renderDashboardLedger(day, dayRequest, render)
	if !strings.Contains(dayView, "Feb 03") || !strings.Contains(dayView, "TOTAL") {
		t.Fatalf("day ledger did not show selected month:\n%s", dayView)
	}
}

func TestDashboardLedgerUsesFullHistoryOutsideDashboardRange(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Transaction(func(tx *store.Tx) error {
		return tx.ApplyUsage(store.Usage{
			Date: "2020-01-15", Provider: discover.ProviderCodex, Model: "gpt-5.2", Input: 100, Output: 10,
		}, "")
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := dashboardSnapshot(database, tui.Request{
		Range: tui.Range30Days, Width: 120, Height: 30,
		Ledger: tuipages.State{Zoom: tuipages.ZoomMonth, Year: 2020, Cursor: -1},
	}, styledRenderContext(120), time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Empty {
		t.Fatal("full-history ledger was marked empty outside dashboard range")
	}
	view := renderDashboardLedger(snapshot, tui.Request{
		Range: tui.Range30Days, Width: 120, Height: 30,
		Ledger: tuipages.State{Zoom: tuipages.ZoomMonth, Year: 2020, Cursor: -1},
	}, styledRenderContext(120))
	for _, fragment := range []string{"Jan 2020"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("full-history ledger missing %q outside dashboard range:\n%s", fragment, view)
		}
	}
}

func TestDashboardLedgerEmptyAnchorDoesNotHideExistingUsage(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Transaction(func(tx *store.Tx) error {
		return tx.ApplyUsage(store.Usage{
			Date: "2020-01-15", Provider: discover.ProviderClaude, Model: "claude-sonnet", Input: 100, Output: 10,
		}, "")
	}); err != nil {
		t.Fatal(err)
	}

	snapshot, err := dashboardSnapshot(database, tui.Request{
		Provider: tui.CodexProvider, Range: tui.Range30Days, Width: 120, Height: 30,
		Ledger: tuipages.State{Zoom: tuipages.ZoomMonth, Year: 2019, Cursor: -1},
	}, styledRenderContext(120), time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Ledger.Rows) != 0 {
		t.Fatalf("empty ledger anchor produced rows = %+v", snapshot.Ledger.Rows)
	}
	if snapshot.Empty {
		t.Fatal("existing full-history usage was marked empty for an empty ledger anchor")
	}
}

func TestQuest115AfterSnapshot(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	request := tui.Request{Range: tui.RangeAll, Width: 120, Height: 30}
	render := styledRenderContext(120)
	snapshot, err := dashboardSnapshot(database, request, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + renderDashboardLedger(snapshot, request, render))
}

func renderDashboardLedger(snapshot tui.Snapshot, request tui.Request, render theme.Context) string {
	render.Width = tui.ContentWidth(request.Width)
	return tuipages.Render(render, snapshot.Ledger, request.Ledger, request.Height)
}

func TestDashboardDailyCursorSelectsDetailAndCollapsesBelowChart(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	render := styledRenderContext(120)
	wide, err := dashboardSnapshot(database, tui.Request{DailyCursor: 1, Range: tui.RangeAll, Width: 120, Height: 35}, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	daily := wide.Views[tui.DailyTab]
	for _, fragment := range []string{"DAY DETAIL", "2026-02-01", "PROVIDER SPLIT", "TOP MODELS BY COST", "^"} {
		if !strings.Contains(daily, fragment) {
			t.Errorf("wide daily view missing %q:\n%s", fragment, daily)
		}
	}
	if strings.Contains(daily, "DATE") {
		t.Fatalf("daily detail retained the redundant date table:\n%s", daily)
	}

	narrow, err := dashboardSnapshot(database, tui.Request{DailyCursor: 1, Range: tui.RangeAll, Width: 100, Height: 35}, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	wideLines := len(strings.Split(strings.TrimRight(daily, "\n"), "\n"))
	narrowDaily := narrow.Views[tui.DailyTab]
	narrowLines := len(strings.Split(strings.TrimRight(narrowDaily, "\n"), "\n"))
	if narrowLines <= wideLines || !strings.Contains(narrowDaily, "PROVIDER SPLIT") {
		t.Fatalf("narrow daily view did not collapse below chart (wide=%d narrow=%d):\n%s", wideLines, narrowLines, narrowDaily)
	}
}

func TestBareStyledInvocationLaunchesDashboard(t *testing.T) {
	original := runDashboardProgram
	defer func() { runDashboardProgram = original }()
	called := false
	runDashboardProgram = func(_ *cobra.Command, _ tui.Model) error {
		called = true
		return nil
	}
	var output bytes.Buffer
	terminal := true
	dark := true
	cmd := newRootCommand(theme.ResolveOptions{
		ForceTerminal: &terminal, Width: 100, ForceColor: true, Dark: &dark,
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called || strings.Contains(output.String(), "Your agents nom tokens") {
		t.Fatalf("styled bare launch = called %v, output %q", called, output.String())
	}
}

func TestBarePlainInvocationRemainsHelp(t *testing.T) {
	plain, err := executeCLI()
	if err != nil {
		t.Fatal(err)
	}
	noColor, err := executeCLI("--no-color")
	if err != nil {
		t.Fatal(err)
	}
	if plain != noColor || !strings.Contains(plain, "Your agents nom tokens") {
		t.Fatalf("plain bare output changed:\nplain:\n%s\nno-color:\n%s", plain, noColor)
	}
}

func TestForcedColorDoesNotLaunchDashboardWithoutTerminal(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	withoutEnv(t, "NO_COLOR")
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[reports]\ncolor = \"always\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := runDashboardProgram
	defer func() { runDashboardProgram = original }()
	called := false
	runDashboardProgram = func(_ *cobra.Command, _ tui.Model) error {
		called = true
		return nil
	}
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(output.String(), "Your agents nom tokens") {
		t.Fatalf("redirected forced-color launch = called %t, output %q", called, output.String())
	}
}

func syncSummaryForTest() syncer.Summary {
	return syncer.Summary{}
}
