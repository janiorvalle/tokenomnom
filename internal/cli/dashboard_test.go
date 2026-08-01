package cli

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
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
	"github.com/janiorvalle/tokenomnom/internal/pricing"
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

func TestDashboardHistorySearchCacheRefreshesByQueryAndSync(t *testing.T) {
	cache := dashboardHistorySearchCache{}
	calls := 0
	refresh := func() (tuipages.HistorySearchData, error) {
		calls++
		return tuipages.HistorySearchData{Search: tuipages.SearchResult{Warnings: []string{fmt.Sprintf("load-%d", calls)}}}, nil
	}
	request := tui.Request{HistoryQuery: "do not implement", Provider: tui.AllProviders, Range: tui.Range30Days}

	data, err := cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-1" || calls != 1 {
		t.Fatalf("initial search snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-1" || calls != 1 {
		t.Fatalf("cached search snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	request.Provider = tui.CodexProvider
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-2" || calls != 2 {
		t.Fatalf("provider-filtered search snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	request.Range = tui.Range90Days
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-3" || calls != 3 {
		t.Fatalf("range-filtered search snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	request.HistorySessionID = "ses_example"
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-4" || calls != 4 {
		t.Fatalf("session detail snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	request.Sync = true
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-5" || calls != 5 {
		t.Fatalf("sync search snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	request.Sync = false
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.Search.Warnings[0] != "load-5" || calls != 5 {
		t.Fatalf("post-sync cached search snapshot = %+v err=%v calls=%d", data, err, calls)
	}
}

func TestDashboardHistorySearchCacheInvalidatesMissingIndex(t *testing.T) {
	cache := dashboardHistorySearchCache{}
	indexed := false
	calls := 0
	refresh := func() (tuipages.HistorySearchData, error) {
		calls++
		if !indexed {
			return tuipages.HistorySearchData{NotIndexed: true}, nil
		}
		return tuipages.HistorySearchData{Search: tuipages.SearchResult{Hits: []tuipages.SearchHit{{SessionID: "ses_ready"}}}}, nil
	}
	request := tui.Request{HistoryQuery: "prompt", Provider: tui.AllProviders, Range: tui.Range30Days}

	data, err := cache.snapshot(request, refresh)
	if err != nil || !data.NotIndexed || calls != 1 {
		t.Fatalf("missing-index snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	indexed = true
	data, err = cache.snapshot(request, refresh)
	if err != nil || data.NotIndexed || len(data.Search.Hits) != 1 || calls != 2 {
		t.Fatalf("post-index snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	indexed = false
	request.Sync = true
	data, err = cache.snapshot(request, refresh)
	if err != nil || !data.NotIndexed || calls != 3 {
		t.Fatalf("removed-index snapshot = %+v err=%v calls=%d", data, err, calls)
	}
	request.Sync = false
	data, err = cache.snapshot(request, refresh)
	if err != nil || !data.NotIndexed || calls != 4 {
		t.Fatalf("post-removal snapshot resurrected cached data = %+v err=%v calls=%d", data, err, calls)
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

func TestLoadDashboardLedgerSessionsPricesSelectedDay(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	codexDir, claudeDir := filepath.Join(root, "codex"), filepath.Join(root, "claude")
	fixture := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"ledger-cost","thread_source":"user","cwd":"/repo"}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"turn_context","payload":{"model":"gpt-5.2"}}`,
		`{"timestamp":"2026-07-20T12:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100000,"cached_input_tokens":20000,"output_tokens":30000,"reasoning_output_tokens":4000,"total_tokens":130000},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":20000,"output_tokens":30000,"reasoning_output_tokens":4000,"total_tokens":130000}}}}`,
		`{"timestamp":"2026-07-20T12:00:03Z","type":"event_msg","payload":{"type":"user_message","message":"find the costly session"}}`,
		`{"timestamp":"2026-07-21T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":200000,"cached_input_tokens":40000,"output_tokens":60000,"reasoning_output_tokens":8000,"total_tokens":260000},"last_token_usage":{"input_tokens":100000,"cached_input_tokens":20000,"output_tokens":30000,"reasoning_output_tokens":4000,"total_tokens":130000}}}}`,
	}, "\n") + "\n"
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "ledger-cost.jsonl"), fixture)
	if _, err := executeReport([]string{"history", "index", "--source", "provider", "--format", "json"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	request := tui.Request{Provider: tui.CodexProvider, Ledger: tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-20"}}
	data := loadDashboardLedgerSessions(NewRootCommand(), filepath.Join(stateDir, historystore.DatabaseName), tuipages.Data{}, request, time.UTC, codexDir, claudeDir)
	if !data.SessionIndexAvailable || data.SessionDay != "2026-07-20" || len(data.Sessions) != 1 {
		t.Fatalf("ledger sessions = %+v", data)
	}
	session := data.Sessions[0]
	if session.SessionID == "" || session.Provider != history.ProviderCodex || session.Project != "repo" || session.Preview != "find the costly session" || session.Tokens != 130000 || session.Cost != pricing.Money(563_500_000) || session.PricedTokens != 130000 || session.ActivityTimestamp != "2026-07-20T12:00:02Z" {
		t.Fatalf("priced ledger session = %+v", session)
	}
}

func TestLoadDashboardLedgerSessionsMissingIndexKeepsDayAndHintState(t *testing.T) {
	request := tui.Request{Ledger: tuipages.State{ExpandedDay: "2026-07-20"}}
	data := loadDashboardLedgerSessions(NewRootCommand(), filepath.Join(t.TempDir(), historystore.DatabaseName), tuipages.Data{}, request, time.UTC, "", "")
	if data.SessionDay != "2026-07-20" || data.SessionIndexAvailable || data.SessionWarning != "" {
		t.Fatalf("missing ledger history = %+v", data)
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

func TestQuest108AfterSnapshot(t *testing.T) {
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
	if wide.DailyCursorMax != 2 {
		t.Fatalf("daily cursor max = %d, want 2 for three active days", wide.DailyCursorMax)
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
	t.Logf("wide daily cockpit:\n%s\n\nnarrow daily cockpit:\n%s", daily, narrowDaily)
}

func TestDashboardDailyShortViewportAndUnpricedMode(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	short, err := dashboardSnapshot(database, tui.Request{Range: tui.RangeAll, Width: 60, Height: 18}, styledRenderContext(60), time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	shortDaily := short.Views[tui.DailyTab]
	for _, fragment := range []string{"DAY DETAIL", "PROVIDER SPLIT", "↓ more below"} {
		if !strings.Contains(shortDaily, fragment) {
			t.Errorf("short daily view missing %q:\n%s", fragment, shortDaily)
		}
	}
	if short.DailyDetailMaxOffset == 0 {
		t.Fatalf("short daily view reported no detail overflow:\n%s", shortDaily)
	}

	unpriced, err := dashboardSnapshot(database, tui.Request{DailyCursor: 0, Range: tui.RangeAll, Width: 120, Height: 35}, styledRenderContext(120), time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	unpricedDaily := unpriced.Views[tui.DailyTab]
	for _, fragment := range []string{"PROVIDER SPLIT · TOKENS", "TOP MODELS BY TOKENS", "UNPRICED DAY · TOKEN SHARES"} {
		if !strings.Contains(unpricedDaily, fragment) {
			t.Errorf("unpriced daily view missing %q:\n%s", fragment, unpricedDaily)
		}
	}
}

func TestDailyWindowMovesOnlyWhenCursorExits(t *testing.T) {
	rows := []store.DailyRow{
		{Date: "one"}, {Date: "two"}, {Date: "three"}, {Date: "four"}, {Date: "five"},
	}
	if got := normalizedDailyWindowStart(rows, 4, 3, 0); got != 2 {
		t.Fatalf("initial window start = %d, want 2", got)
	}
	if got := normalizedDailyWindowStart(rows, 3, 3, 2); got != 2 {
		t.Fatalf("window moved while cursor was visible: start=%d", got)
	}
	if got := normalizedDailyWindowStart(rows, 1, 3, 2); got != 1 {
		t.Fatalf("window did not follow cursor past the left edge: start=%d", got)
	}
	window := windowDailyRows(rows, 2, 3)
	if len(window) != 3 || window[0].Date != "three" || window[2].Date != "five" {
		t.Fatalf("daily window = %#v", window)
	}
	if got := dailyDetailRenderWidth(tui.ContentWidth(100)); got != tui.ContentWidth(100) {
		t.Fatalf("stacked detail width = %d, want %d", got, tui.ContentWidth(100))
	}
}

func TestDailyDetailWindowKeepsContentAtTinyHeights(t *testing.T) {
	render := styledRenderContext(20)
	for _, height := range []int{1, 2} {
		view, _, _ := renderDailyDetailWindow(render, "one\ntwo\nthree", 20, height, 1)
		if strings.Contains(view, "more") || !strings.Contains(view, "two") {
			t.Fatalf("detail window at height %d hid content: %q", height, view)
		}
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
