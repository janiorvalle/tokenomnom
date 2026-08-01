package tui

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

func TestUpdateNavigationFiltersAndHelp(t *testing.T) {
	model := loadedTestModel()
	model = updateKeyForTest(t, model, "tab")
	if model.router.ActiveIndex() != int(LedgerTab) {
		t.Fatalf("active page index = %d", model.router.ActiveIndex())
	}
	model = updateKeyForTest(t, model, "4")
	if model.router.ActiveIndex() != int(HeatmapTab) {
		t.Fatalf("number page index = %d", model.router.ActiveIndex())
	}
	model = updateKeyForTest(t, model, "p")
	if model.request.Provider != CodexProvider {
		t.Fatalf("provider = %v", model.request.Provider)
	}
	model = updateKeyForTest(t, model, "r")
	if model.request.Range != Range90Days {
		t.Fatalf("range = %v", model.request.Range)
	}
	model = updateKeyForTest(t, model, "?")
	if !model.help || !strings.Contains(model.View(), "shift+tab") {
		t.Fatalf("help state/view = %v, %q", model.help, model.View())
	}
	model = updateKeyForTest(t, model, "?")
	if model.help {
		t.Fatal("help did not close")
	}
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if command == nil {
		t.Fatal("quit key returned no command")
	}
}

func TestUpdatePanningSortingAndSizing(t *testing.T) {
	model := loadedTestModel()
	model = updateKeyForTest(t, model, "left")
	if model.request.DailyCursor != 1 {
		t.Fatalf("daily cursor after left = %d", model.request.DailyCursor)
	}
	model = updateKeyForTest(t, model, "right")
	if model.request.DailyCursor != 0 {
		t.Fatalf("daily cursor after right = %d", model.request.DailyCursor)
	}
	model.request.DailyCursor = 1_000_000
	request := model.request
	currentSnapshot := model.snapshot
	updated, command := model.Update(loadedMessage(model, request, Snapshot{Views: currentSnapshot.Views, DailyCursor: 2, DailyWindowStart: 2, DailyDetailOffset: 3, DailyDetailMaxOffset: 5}))
	model = updated.(Model)
	if model.request.DailyCursor != 2 || model.request.DailyWindowStart != 2 || model.request.DailyDetailOffset != 3 {
		t.Fatalf("daily state was not normalized by the loaded snapshot: %+v", model.request)
	}
	model.request.DailyCursor = 1_000_000
	model.request.DailyDetailOffset = 1_000_000
	model.syncing = true
	model.syncGeneration = model.loadGeneration
	model.syncInFlight = true
	syncRequest := model.request
	syncRequest.Sync = true
	updated, command = model.Update(loadedMessage(model, syncRequest, Snapshot{Views: currentSnapshot.Views, DailyCursor: 4, DailyWindowStart: 3, DailyDetailOffset: 5, DailyDetailMaxOffset: 5}))
	model = updated.(Model)
	if command != nil || model.syncing || model.request.DailyCursor != 4 || model.request.DailyWindowStart != 3 || model.request.DailyDetailOffset != 5 {
		t.Fatalf("sync daily state was not normalized: request=%+v syncing=%v command=%v", model.request, model.syncing, command != nil)
	}
	staleRequest := model.request
	staleRequest.DailyCursor = 1
	updated, command = model.Update(loadedMessage(model, staleRequest, Snapshot{Views: [4]string{"stale"}}))
	model = updated.(Model)
	if command != nil || model.snapshot.Views[0] != currentSnapshot.Views[0] || model.request.DailyCursor != 4 {
		t.Fatalf("stale daily load was applied: request=%+v snapshot=%+v command=%v", model.request, model.snapshot, command != nil)
	}
	model.syncing = true
	model.syncGeneration = model.loadGeneration
	model.syncInFlight = true
	staleSyncRequest := model.request
	staleSyncRequest.DailyCursor = 1
	staleSyncRequest.Sync = true
	updated, command = model.Update(loadedMessage(model, staleSyncRequest, Snapshot{Views: [4]string{"stale sync"}}))
	model = updated.(Model)
	if command == nil || model.syncing || !model.loading || model.snapshot.Views[0] != currentSnapshot.Views[0] {
		t.Fatalf("stale sync was not handed back to the current request: request=%+v snapshot=%+v syncing=%v loading=%v command=%v", model.request, model.snapshot, model.syncing, model.loading, command != nil)
	}
	model.request.DailyDetailOffset = 1
	model = updateKeyForTest(t, model, "down")
	if model.request.DailyDetailOffset != 2 {
		t.Fatalf("daily detail offset after down = %d", model.request.DailyDetailOffset)
	}
	model = updateKeyForTest(t, model, "up")
	if model.request.DailyDetailOffset != 1 {
		t.Fatalf("daily detail offset after up = %d", model.request.DailyDetailOffset)
	}
	model.request.DailyDetailOffset = 2
	model.snapshot.DailyDetailMaxOffset = 2
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	model = updated.(Model)
	if command != nil || model.request.DailyDetailOffset != 2 {
		t.Fatalf("daily detail moved beyond the loaded viewport: offset=%d command=%v", model.request.DailyDetailOffset, command != nil)
	}
	model.router.SelectIndex(int(LedgerTab))
	previousRequest := model.request
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = updated.(Model)
	if command != nil || model.request != previousRequest {
		t.Fatalf("ledger horizontal navigation changed state: request=%+v command=%v", model.request, command != nil)
	}
	model.router.SelectIndex(int(ModelsTab))
	model = updateKeyForTest(t, model, "s")
	model = updateKeyForTest(t, model, "down")
	if model.request.ModelSort != 1 || model.request.ModelOffset != 1 {
		t.Fatalf("model navigation = %+v", model.request)
	}
	model.router.SelectIndex(int(HeatmapTab))
	model = updateKeyForTest(t, model, "y")
	model = updateKeyForTest(t, model, "right")
	if model.request.HeatmapYear || model.request.HeatmapOffset != 1 {
		t.Fatalf("heatmap navigation = %+v", model.request)
	}

	updated, command = model.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	model = updated.(Model)
	if command != nil || !strings.Contains(model.View(), "terminal too small") {
		t.Fatalf("small terminal state = command %v, view %q", command != nil, model.View())
	}
	updated, command = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	if command == nil || model.request.Width != 100 || model.request.Height != 30 {
		t.Fatalf("resize state = %+v, command %v", model.request, command != nil)
	}
}

func TestDailyPageStopsAtOldestActiveDay(t *testing.T) {
	model := loadedTestModel()
	model.request.DailyCursor = model.snapshot.DailyCursorMax
	before := model.request
	updated, command := model.Update(keyMsg("left"))
	model = updated.(Model)
	if command != nil || model.request != before {
		t.Fatalf("left moved past oldest active day: request=%+v command=%v", model.request, command != nil)
	}

	model.request.DailyCursor--
	updated, command = model.Update(keyMsg("left"))
	model = updated.(Model)
	if command == nil || model.request.DailyCursor != model.snapshot.DailyCursorMax {
		t.Fatalf("left did not reach oldest active day: request=%+v command=%v", model.request, command != nil)
	}
}

func TestSyncRefreshInvalidatesPreSyncLoads(t *testing.T) {
	model := loadedTestModel()
	model.syncing = true
	syncRequest := model.request
	syncRequest.Sync = true
	syncCommand := model.loadCmd(syncRequest)
	if syncCommand == nil {
		t.Fatal("sync command was not created")
	}
	activeSyncGeneration := model.syncGeneration
	model = updateKeyForTest(t, model, "p")
	preSyncRequest := model.request
	preSyncGeneration := model.loadGeneration
	updated, command := model.Update(loadedMsg{
		request:    syncRequest,
		generation: activeSyncGeneration,
		snapshot:   Snapshot{Views: [4]string{"synced"}},
	})
	model = updated.(Model)
	if command == nil || model.loadGeneration == preSyncGeneration || model.syncing {
		t.Fatalf("sync handoff did not create a new load: generation=%d previous=%d syncing=%v command=%v", model.loadGeneration, preSyncGeneration, model.syncing, command != nil)
	}
	postSyncGeneration := model.loadGeneration
	updated, _ = model.Update(loadedMsg{
		request:    preSyncRequest,
		generation: preSyncGeneration,
		snapshot:   Snapshot{Views: [4]string{"stale pre-sync"}},
	})
	model = updated.(Model)
	if model.snapshot.Views[0] == "stale pre-sync" {
		t.Fatal("pre-sync load overwrote the post-sync reload")
	}
	updated, _ = model.Update(loadedMsg{
		request:    preSyncRequest,
		generation: postSyncGeneration,
		snapshot:   Snapshot{Views: [4]string{"fresh post-sync"}},
	})
	model = updated.(Model)
	if model.snapshot.Views[0] != "fresh post-sync" {
		t.Fatalf("post-sync reload was not applied: %q", model.snapshot.Views[0])
	}
}

func TestDashboardRequestMatchIgnoresHistoryDetailOffset(t *testing.T) {
	left := Request{Width: 100, Height: 30, SessionDetailOffset: 0}
	right := left
	right.SessionDetailOffset = 7
	if !sameRequestIgnoringSync(left, right) {
		t.Fatal("history detail scrolling invalidated the dashboard request match")
	}
}

func TestSyncSurvivesInterveningLoadError(t *testing.T) {
	model := loadedTestModel()
	model.syncing = true
	syncRequest := model.request
	syncRequest.Sync = true
	model.loadCmd(syncRequest)
	activeSyncGeneration := model.syncGeneration

	currentRequest := model.request
	currentGeneration := model.loadGeneration
	model.loadCmd(currentRequest)
	updated, command := model.Update(loadedMsg{
		request:    currentRequest,
		generation: currentGeneration + 1,
		err:        errors.New("current load failed"),
	})
	model = updated.(Model)
	if command != nil || !model.syncInFlight || !model.syncing {
		t.Fatalf("intervening load error canceled sync: syncing=%v inFlight=%v command=%v", model.syncing, model.syncInFlight, command != nil)
	}

	updated, command = model.Update(loadedMsg{
		request:    syncRequest,
		generation: activeSyncGeneration,
		snapshot:   Snapshot{Views: [4]string{"synced"}},
	})
	model = updated.(Model)
	if command == nil || model.syncInFlight || model.syncing || !model.loading {
		t.Fatalf("successful sync did not hand off after load error: syncing=%v inFlight=%v loading=%v command=%v", model.syncing, model.syncInFlight, model.loading, command != nil)
	}
	handoffRequest := model.request
	handoffGeneration := model.loadGeneration
	updated, command = model.Update(loadedMsg{
		request:    handoffRequest,
		generation: handoffGeneration,
		err:        errors.New("post-sync reload failed"),
	})
	model = updated.(Model)
	if command != nil || model.syncCompletionPending {
		t.Fatalf("failed post-sync reload leaked completion state: pending=%v command=%v", model.syncCompletionPending, command != nil)
	}
}

func TestSyncProgressLoadedAndFailureTransitions(t *testing.T) {
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{})
	updated, command := model.Update(loadedMessage(model, model.request, Snapshot{Empty: true, FilesScanned: 12}))
	model = updated.(Model)
	if !model.loading || !model.syncing || command == nil || !strings.Contains(model.View(), "Syncing Codex + Claude · discovering files") || strings.Contains(model.View(), "0 files scanned") {
		t.Fatalf("empty initial transition = %+v, command %v", model, command != nil)
	}

	wanted := Snapshot{Views: [4]string{"daily", "monthly", "models", "heatmap"}}
	syncRequest := model.request
	syncRequest.Sync = true
	updated, _ = model.Update(loadedMessage(model, syncRequest, wanted))
	model = updated.(Model)
	if model.loading || model.syncing || model.snapshot.Views[0] != "daily" || !strings.Contains(model.status, "synced") {
		t.Fatalf("loaded transition = %+v", model)
	}

	syncRequest = model.request
	syncRequest.Sync = true
	updated, _ = model.Update(loadedErrorMessage(model, syncRequest, errors.New("sync failed")))
	model = updated.(Model)
	if model.warning != "sync failed" || model.snapshot.Views[0] != "daily" || !strings.Contains(model.View(), "sync failed") {
		t.Fatalf("failure transition = %+v", model)
	}
}

func TestSyncProgressViewRendersLiveIngestCount(t *testing.T) {
	model := loadedTestModel()
	model.loading = true
	model.progress = LoadProgress{Phase: "ingesting files", FilesFound: 3712, FilesProcessed: 128}
	view := model.View()
	if !strings.Contains(view, "Syncing Codex + Claude · ingesting 128/3,712 files") {
		t.Fatalf("live progress missing from loading view:\n%s", view)
	}
	if strings.Contains(view, "0 files scanned") {
		t.Fatalf("loading view still rendered a frozen zero:\n%s", view)
	}
	t.Log("\n" + view)
}

func TestSyncProgressViewRendersHonestPhaseCopy(t *testing.T) {
	model := loadedTestModel()
	model.loading = true
	for _, progress := range []LoadProgress{
		{Phase: "discovering files"},
		{Phase: "preparing sync", FilesFound: 3712},
	} {
		model.progress = progress
		view := model.View()
		if strings.Contains(view, "0 files scanned") {
			t.Fatalf("phase view still rendered a frozen zero:\n%s", view)
		}
		t.Log("\n" + view)
	}
}

func TestSyncLoaderStreamsProgressThroughSink(t *testing.T) {
	var received []LoadProgress
	model := New(testRender(), func(request Request) (Snapshot, error) {
		if request.Progress == nil {
			t.Fatal("sync loader did not receive a progress reporter")
		}
		report := *request.Progress
		report(LoadProgress{Phase: "ingesting files", FilesFound: 4, FilesProcessed: 2})
		return Snapshot{}, nil
	}, SkillOffer{})
	model.SetProgressSink(func(_ Request, _ uint64, progress LoadProgress) {
		received = append(received, progress)
	})
	request := model.request
	request.Sync = true
	message := model.loadCmd(request)()
	if _, ok := message.(loadedMsg); !ok {
		t.Fatalf("sync command returned %T, want loadedMsg", message)
	}
	if len(received) != 1 || received[0].FilesProcessed != 2 {
		t.Fatalf("received progress = %+v", received)
	}
	updated, _ := model.Update(ProgressMsg{
		Request:    request,
		Generation: model.loadGeneration,
		Progress:   received[0],
	})
	model = updated.(Model)
	if model.progress.FilesProcessed != 2 {
		t.Fatalf("model progress = %+v", model.progress)
	}
}

func TestInitialSnapshotShowsPendingOptionalSegments(t *testing.T) {
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{})
	model.request.Width, model.request.Height = 100, 30
	initialRequest := model.request
	if !initialRequest.Initial {
		t.Fatal("new dashboard request was not marked initial")
	}
	pending := Snapshot{
		Sessions: tuipages.SessionPageData{Pending: true},
		StatusBar: StatusBar{
			History: HistoryStatus{Hint: "pending"},
			Vault:   VaultStatus{Hint: "pending"},
		},
	}
	updated, command := model.Update(loadedMsg{request: initialRequest, generation: model.loadGeneration, snapshot: pending})
	model = updated.(Model)
	if command == nil || model.request.Initial || !model.loaded || model.loading || !model.syncing {
		t.Fatalf("initial snapshot transition = initial=%v loaded=%v loading=%v syncing=%v command=%v", model.request.Initial, model.loaded, model.loading, model.syncing, command != nil)
	}
	view := model.View()
	if !strings.Contains(view, "index pending") || !strings.Contains(view, "vault pending") {
		t.Fatalf("pending optional segments missing from first frame:\n%s", view)
	}
	model.router.Select(SessionsPageID)
	sessionsView := model.View()
	if !strings.Contains(sessionsView, "Loading sessions…") || strings.Contains(sessionsView, "No history index is available.") {
		t.Fatalf("pending sessions page made a false availability claim:\n%s", sessionsView)
	}
	t.Log("\n" + view)
}

func TestInitialSnapshotSurvivesResizeBeforeLoadCompletes(t *testing.T) {
	model := New(testRender(), func(Request) (Snapshot, error) {
		return Snapshot{}, nil
	}, SkillOffer{})
	initialRequest := model.request

	updated, command := model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	if command != nil || model.request.Height != 30 {
		t.Fatalf("resize while initial load is busy = request=%+v command=%v", model.request, command != nil)
	}

	updated, command = model.Update(loadedMsg{
		request:    initialRequest,
		generation: model.loadGeneration,
		snapshot:   Snapshot{Empty: true},
	})
	model = updated.(Model)
	if command == nil || !model.loaded || !model.loading || !model.syncing {
		t.Fatalf("initial load after resize = loaded=%v loading=%v syncing=%v command=%v", model.loaded, model.loading, model.syncing, command != nil)
	}
	message := command()
	loaded, ok := message.(loadedMsg)
	if !ok || loaded.request.Height != 30 {
		t.Fatalf("initial sync used stale dimensions: message=%T request=%+v", message, loaded.request)
	}
}

func TestEveryViewRendersStructure(t *testing.T) {
	model := loadedTestModel()
	for tab := Tab(0); tab < tabCount; tab++ {
		model.router.SelectIndex(int(tab))
		page := model.router.ActivePage()
		view := model.View()
		for _, fragment := range []string{"TOTAL", "TOKENS", "ACTIVE DAYS", "AVG/DAY", "PEAK", page.Title(), model.snapshot.Views[tab], "API list-price equivalents"} {
			if !strings.Contains(view, fragment) {
				t.Errorf("%s view missing %q:\n%s", page.Title(), fragment, view)
			}
		}
	}
}

func TestQuest107AfterSnapshot(t *testing.T) {
	model := loadedTestModel()
	view := model.View()
	if !strings.Contains(view, "SPEND") {
		t.Fatalf("snapshot omitted the registered sidebar section:\n%s", view)
	}
	t.Log("\n" + view)
}

func TestCockpitFillsTheWindow(t *testing.T) {
	for _, size := range []struct{ width, height int }{{100, 30}, {160, 40}} {
		model := loadedTestModel()
		model.request.Width, model.request.Height = size.width, size.height
		lines := strings.Split(model.View(), "\n")
		if len(lines) != size.height {
			t.Fatalf("size %dx%d rendered %d lines", size.width, size.height, len(lines))
		}
		for index, line := range lines {
			if width := lipgloss.Width(line); width != size.width {
				t.Fatalf("size %dx%d line %d has width %d:\n%s", size.width, size.height, index+1, width, model.View())
			}
		}

		terminal, dark := true, true
		model.render = theme.Resolve(theme.ResolveOptions{
			Output: &bytes.Buffer{}, ForceTerminal: &terminal, Width: size.width,
			ForceColor: true, Dark: &dark, LookupEnv: func(string) (string, bool) { return "", false },
		})
		styledLines := strings.Split(model.View(), "\n")
		for index, line := range styledLines {
			if width := lipgloss.Width(line); width != size.width {
				t.Fatalf("styled size %dx%d line %d has width %d", size.width, size.height, index+1, width)
			}
		}
	}

	if ContentWidth(160) <= ContentWidth(100) {
		t.Fatalf("content width does not grow with the terminal: 100=%d 160=%d", ContentWidth(100), ContentWidth(160))
	}
}

func TestEveryDashboardPageFitsAtEightyByTwentyFour(t *testing.T) {
	model := realisticEvidenceModel()
	model.request.Width, model.request.Height = 80, 24
	model.render.Width = 80
	model.snapshot.Sessions = testSessionPageData()
	model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07"}
	historyPage := NewHistorySearchPage(HistorySearchOptions{})
	model.router = newRouter(historyPage)
	model = updateKeyForTest(t, model, "?")
	assertDashboardFrameFits(t, model, "Help")
	model = updateKeyForTest(t, model, "?")

	for index, page := range model.router.Pages() {
		model.router.SelectIndex(index)
		assertDashboardFrameFits(t, model, page.Title())
	}

	model.router.Select(SessionsPageID)
	model.request.SessionDetailID = "ses_second"
	model.snapshot.Sessions = testSessionPageData()
	assertDashboardFrameFits(t, model, "Sessions detail")

	model.router.Select(LedgerPageID)
	model.request.SessionDetailID = ""
	model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14", DetailID: "ses_second"}
	first := "2026-07-14T09:30:00Z"
	model.snapshot.Ledger.SessionDay = "2026-07-14"
	model.snapshot.Ledger.SessionIndexAvailable = true
	model.snapshot.Ledger.Sessions = []tuipages.LedgerSession{{CatalogSession: historystore.CatalogSession{
		SessionID: "ses_second", Provider: history.ProviderClaude, Project: "alpha", Preview: strings.Repeat("a long prompt ", 20), FirstTimestamp: &first,
	}}}
	assertDashboardFrameFits(t, model, "Ledger detail")

	model.router.Select(HistorySearchPageID)
	historyPage.query = "prompt"
	historyPage.searched = true
	historyPage.sessionID = "ses_second"
	historyPage.detail = &SessionDetail{SessionID: "ses_second", Provider: "codex", Project: "tokenomnom", Preview: strings.Repeat("a long prompt ", 20)}
	model.request.HistoryQuery = "prompt"
	model.request.HistorySessionID = "ses_second"
	assertDashboardFrameFits(t, model, "History search detail")
}

func assertDashboardFrameFits(t *testing.T, model Model, state string) {
	t.Helper()
	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) != 24 {
		t.Fatalf("%s rendered %d lines at 80x24:\n%s", state, len(lines), view)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 80 {
			t.Fatalf("%s line %d rendered width %d at 80x24:\n%s", state, index+1, width, view)
		}
	}
}

func TestKeyboardOnlyWalkReachesAndExitsEveryPage(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{
		Load: func(request Request) (HistorySearchData, error) {
			if request.HistorySessionID != "" {
				return HistorySearchData{Session: &SessionDetail{SessionID: request.HistorySessionID, Provider: "codex", Project: "tokenomnom", Preview: "prompt"}}, nil
			}
			return HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_walk", Provider: "codex", Project: "tokenomnom", Snippet: "walk prompt"}}}}, nil
		},
	})
	model := loadedTestModel()
	model.router = newRouter(page)
	model.snapshot.Sessions = testSessionPageData()

	expected := []PageID{DailyPageID, LedgerPageID, ModelsPageID, HeatmapPageID, SessionsPageID, HistorySearchPageID}
	for index, pageID := range expected {
		if got := model.router.ActivePage().ID(); got != pageID {
			t.Fatalf("page %d = %q, want %q", index, got, pageID)
		}
		updated, command := model.Update(tea.KeyMsg{Type: tea.KeyTab})
		model = updated.(Model)
		if command != nil {
			updated, _ = model.Update(command())
			model = updated.(Model)
		}
	}
	if model.router.ActivePage().ID() != DailyPageID {
		t.Fatalf("tab did not wrap to daily page: %q", model.router.ActivePage().ID())
	}

	model.router.Select(SessionsPageID)
	model = updateKeyForTest(t, model, "enter")
	if model.request.SessionDetailID != "ses_first" {
		t.Fatalf("sessions enter did not open the selected session: %+v", model.request)
	}
	model = updateKeyForTest(t, model, "esc")
	if model.request.SessionDetailID != "" {
		t.Fatalf("sessions escape did not return to the list: %+v", model.request)
	}

	model.router.Select(HistorySearchPageID)
	if model.router.ActivePage().ID() != HistorySearchPageID {
		t.Fatalf("history search page was not selected: %q", model.router.ActivePage().ID())
	}
	model = updateKeyForTest(t, model, "/")
	model = updateKeyForTest(t, model, "p")
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if command == nil {
		t.Fatalf("history search did not schedule a load: request=%+v editing=%v loading=%v", model.request, page.Editing(), page.loading)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.request.HistorySessionID != "ses_walk" {
		t.Fatalf("history search enter did not open the selected result: %+v", model.request)
	}
	if command == nil {
		t.Fatal("history detail did not schedule a load")
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	model = updated.(Model)
	if model.request.HistorySessionID != "" {
		t.Fatalf("history search escape did not return to results: %+v", model.request)
	}
	if command == nil {
		t.Fatal("history search escape did not schedule the result reload")
	}

	updated, command = model.Update(keyMsg("q"))
	if command == nil {
		t.Fatal("keyboard-only walk did not expose quit")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("quit command returned %T", command())
	}
}

func TestQuest117KeyboardWalkEvidence(t *testing.T) {
	model := realisticEvidenceModel()
	model.request.Width, model.request.Height = 80, 24
	model.render.Width = 80
	model.snapshot.Sessions = testSessionPageData()
	historyPage := NewHistorySearchPage(HistorySearchOptions{})
	model.router = newRouter(historyPage)

	frames := make([]string, 0, len(model.router.Pages())+3)
	model = updateKeyForTest(t, model, "?")
	frames = append(frames, "FRAME: help overlay · 80x24\n"+model.View())
	model = updateKeyForTest(t, model, "?")
	for index, page := range model.router.Pages() {
		model.router.SelectIndex(index)
		frames = append(frames, "FRAME: "+page.Title()+" list · 80x24\n"+model.View())
	}

	model.router.Select(SessionsPageID)
	model.request.SessionDetailID = "ses_second"
	frames = append(frames, "FRAME: Sessions detail · 80x24\n"+model.View())

	model.router.Select(LedgerPageID)
	model.request.SessionDetailID = ""
	model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14", DetailID: "ses_second"}
	first := "2026-07-14T09:30:00Z"
	model.snapshot.Ledger.SessionDay = "2026-07-14"
	model.snapshot.Ledger.SessionIndexAvailable = true
	model.snapshot.Ledger.Sessions = []tuipages.LedgerSession{{CatalogSession: historystore.CatalogSession{
		SessionID: "ses_second", Provider: history.ProviderClaude, Project: "alpha", ProjectSource: history.ProjectSourceGit,
		Preview: "Prepare the migration rollout plan", FirstTimestamp: &first, LastTimestamp: &first,
		LogicalPromptCount: 4, OccurrenceCount: 6, ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
		PreferredRetrievalSource: "provider-live", Availability: historystore.Availability{ProviderLive: 1, ProviderArchive: 1, Vault: 1},
	}}}
	frames = append(frames, "FRAME: Ledger session detail · 80x24\n"+model.View())

	model.router.Select(HistorySearchPageID)
	historyPage.query = "do not implement"
	historyPage.searched = true
	historyPage.hits = []SearchHit{{SessionID: "ses_search", Provider: "codex", Date: "2026-08-01", Project: "tokenomnom", Snippet: "said do not implement until the provenance view is ready"}}
	model.request.HistoryQuery = historyPage.query
	model.request.HistorySessionID = ""
	frames = append(frames, "FRAME: History search results · 80x24\n"+model.View())
	historyPage.sessionID = "ses_search"
	historyPage.detail = &SessionDetail{
		CatalogSession: historystore.CatalogSession{
			SessionID: "ses_search", Provider: history.ProviderCodex, Project: "tokenomnom", ProjectSource: history.ProjectSourceGit,
			FirstTimestamp: &first, LastTimestamp: &first, LogicalPromptCount: 3, OccurrenceCount: 5,
			ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
			PreferredRetrievalSource: "provider-live", Availability: historystore.Availability{ProviderLive: 1, Vault: 1},
		},
		SessionID: "ses_search", Provider: "codex", Project: "tokenomnom", Preview: "do not implement until the provenance view is ready",
	}
	model.request.HistorySessionID = historyPage.sessionID
	frames = append(frames, "FRAME: History session detail · 80x24\n"+model.View())

	t.Log("Source: internal/tui/app_test.go::TestQuest117KeyboardWalkEvidence\nCommand: go test -v ./internal/tui -run TestQuest117KeyboardWalkEvidence -count=1\n\n" + strings.Join(frames, "\n\n"))
}

func TestRouterRegistersSpendPagesAndHidesEmptySections(t *testing.T) {
	router := newRouter()
	groups := router.groups()
	if len(groups) != 2 || groups[0].section != SpendSection || groups[1].section != HistorySection {
		t.Fatalf("default sidebar groups = %+v, want spend and history", groups)
	}
	if len(groups[0].pages) != int(tabCount) {
		t.Fatalf("spend pages = %d, want %d", len(groups[0].pages), tabCount)
	}
	if len(groups[1].pages) != 1 || groups[1].pages[0].ID() != SessionsPageID {
		t.Fatalf("history pages = %+v, want sessions", groups[1].pages)
	}

	model := loadedTestModel()
	view := model.View()
	for _, section := range []string{"SPEND", "HISTORY"} {
		if !strings.Contains(view, section) {
			t.Fatalf("sidebar omitted %s section:\n%s", section, view)
		}
	}
	for _, section := range []string{"HISTORY", "VAULT", "SYSTEM"} {
		if section != "HISTORY" && strings.Contains(view, section) {
			t.Errorf("empty section %q was rendered:\n%s", section, view)
		}
	}
}

func TestRouterAddsLaterSectionsWithoutChangingModel(t *testing.T) {
	pages := newRouter().Pages()
	pages = append(pages, testPage{id: "history-search", section: HistorySection, title: "Search"})
	router := newPageRouter(pages...)
	groups := router.groups()
	if len(groups) != 2 || groups[1].section != HistorySection || len(groups[1].pages) != 2 {
		t.Fatalf("later section groups = %+v", groups)
	}
	if router.IndexOf("history-search") != int(tabCount+1) {
		t.Fatalf("later page index = %d, want %d", router.IndexOf("history-search"), tabCount+1)
	}
	if !router.Select("history-search") || router.ActivePage().Title() != "Search" {
		t.Fatalf("router did not select later page: active=%v", router.ActivePage())
	}

	model := loadedTestModel()
	model.router = newPageRouter(pages...)
	model = updateKeyForTest(t, model, "6")
	model = updateKeyForTest(t, model, "?")
	if model.router.ActivePage().ID() != "history-search" || !strings.Contains(model.View(), "tab / shift+tab / 1-6") {
		t.Fatalf("numeric later-page navigation failed: active=%v\n%s", model.router.ActivePage(), model.View())
	}
}

func TestAdditionalPageOwnsAsyncLoads(t *testing.T) {
	page := &asyncTestPage{id: "history-search", section: HistorySection, title: "Search"}
	model := NewWithProviderAndPages(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{}, AllProviders, page)
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded, model.dashboardLoadBusy = false, true, false
	updated, command := model.Update(keyMsg("6"))
	model = updated.(Model)
	if model.activePage().ID() != page.id || command == nil {
		t.Fatalf("page selection active=%v command=%v", model.activePage(), command != nil)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if page.applied != "loaded" || page.loadedRequest.HistoryQuery != "" {
		t.Fatalf("page load result=%q request=%+v", page.applied, page.loadedRequest)
	}
	updated, command = model.Update(keyMsg("R"))
	model = updated.(Model)
	if command == nil {
		t.Fatal("page refresh did not schedule a page load")
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("page refresh commands = %T %v, want two commands", message, batch)
	}
	dashboardLoaded := false
	dashboardRequest := Request{}
	for _, refreshCommand := range batch {
		message := refreshCommand()
		updated, _ = model.Update(message)
		model = updated.(Model)
		if _, ok := message.(loadedMsg); ok {
			dashboardLoaded = true
			dashboardRequest = message.(loadedMsg).request
		}
		if _, ok := message.(pageLoadedMsg); ok && !dashboardLoaded && !model.syncing {
			t.Fatal("page load cleared global refresh state before dashboard load completed")
		}
	}
	if page.loads != 2 || model.syncing || model.request.Sync || !page.loadedRequest.Sync {
		t.Fatalf("page refresh loads=%d syncing=%v model_sync=%v load_sync=%v", page.loads, model.syncing, model.request.Sync, page.loadedRequest.Sync)
	}
	if dashboardRequest.PageLoadToken != "" || !dashboardRequest.Sync || page.loadedRequest.PageLoadToken == "" {
		t.Fatalf("dashboard refresh request leaked page token: dashboard=%+v page=%+v", dashboardRequest, page.loadedRequest)
	}
	updated, command = model.Update(keyMsg("p"))
	model = updated.(Model)
	message = command()
	batch, ok = message.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("provider change commands = %T %v, want two commands", message, batch)
	}
	dashboardLoaded = false
	dashboardRequest = Request{}
	for _, providerCommand := range batch {
		message := providerCommand()
		if loaded, ok := message.(loadedMsg); ok {
			dashboardLoaded = true
			dashboardRequest = loaded.request
		}
		updated, _ = model.Update(message)
		model = updated.(Model)
	}
	if page.loads != 3 || page.loadedRequest.Provider != CodexProvider {
		t.Fatalf("provider change loads=%d request=%+v", page.loads, page.loadedRequest)
	}
	if !dashboardLoaded || dashboardRequest.PageLoadToken != "" || page.loadedRequest.PageLoadToken == "" {
		t.Fatalf("dashboard provider request leaked page token: dashboard=%+v page=%+v", dashboardRequest, page.loadedRequest)
	}
}

func TestPageOnlyLoadDoesNotInvalidateDashboardResponse(t *testing.T) {
	page := &asyncTestPage{id: "history-search", section: HistorySection, title: "Search"}
	model := NewWithProviderAndPages(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{}, AllProviders, page)
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded, model.dashboardLoadBusy = false, true, false
	model.syncing = true
	dashboardRequest := model.request
	dashboardRequest.Sync = true
	model.loadCmd(dashboardRequest)
	dashboardGeneration := model.loadGeneration
	model, _ = model.startPageLoad(page, model.request)
	updated, _ := model.Update(loadedMsg{
		request:    dashboardRequest,
		generation: dashboardGeneration,
		snapshot:   Snapshot{Views: [4]string{"fresh dashboard"}},
	})
	model = updated.(Model)
	if model.snapshot.Views[0] != "fresh dashboard" || model.syncing {
		t.Fatalf("page-only load invalidated dashboard response: snapshot=%+v syncing=%v request=%+v", model.snapshot, model.syncing, model.request)
	}
}

func TestHelpStillAllowsQuit(t *testing.T) {
	model := loadedTestModel()
	updated, _ := model.Update(keyMsg("?"))
	model = updated.(Model)
	if !model.help {
		t.Fatal("help did not open")
	}
	updated, command := model.Update(keyMsg("q"))
	if command == nil {
		t.Fatalf("quit was blocked by help: help=%v command=%v", updated.(Model).help, command != nil)
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatalf("quit command returned %T", command())
	}
}

func TestEditingPageDoesNotLoseGlobalNavigation(t *testing.T) {
	page := &interactiveTestPage{id: "history-search", section: HistorySection, title: "Search"}
	model := loadedTestModel()
	model.router = newPageRouter(append(model.router.Pages(), page)...)
	if !model.router.Select(page.id) {
		t.Fatal("could not select interactive page")
	}

	updated, command := model.Update(keyMsg("p"))
	model = updated.(Model)
	if page.lastKey != "" || model.request.Provider != CodexProvider || command == nil {
		t.Fatalf("global key was not preserved: last=%q provider=%v command=%v", page.lastKey, model.request.Provider, command != nil)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)

	page.editing = true
	before := model.request
	updated, command = model.Update(keyMsg("p"))
	model = updated.(Model)
	if page.lastKey != "p" || model.request != before || command != nil {
		t.Fatalf("editing key escaped page: last=%q request=%+v command=%v", page.lastKey, model.request, command != nil)
	}
	updated, command = model.Update(keyMsg("?"))
	model = updated.(Model)
	if page.lastKey != "?" || model.help || model.request != before || command != nil {
		t.Fatalf("question mark escaped editor: last=%q help=%v request=%+v command=%v", page.lastKey, model.help, model.request, command != nil)
	}

	page.editing = false
	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	model = updated.(Model)
	if page.lastKey != "left" || command != nil {
		t.Fatalf("page-local special key was not delivered: last=%q command=%v", page.lastKey, command != nil)
	}
}

func TestKeyRegistryDrivesFooterAndHelp(t *testing.T) {
	model := loadedTestModel()
	footer := model.footerHintsView(newCockpitLayout(model.request.Width, model.request.Height).innerWidth)
	model = updateKeyForTest(t, model, "?")
	help := model.View()
	for _, binding := range helpBindings() {
		if binding.Footer != "" && !strings.Contains(footer, binding.FooterKey+" "+binding.Footer) {
			t.Errorf("footer missing %q binding: %s", binding.FooterKey, footer)
		}
		display := keyBindingDisplay(binding, len(model.router.Pages()))
		if !strings.Contains(help, display) || !strings.Contains(help, binding.Description) {
			t.Errorf("help missing registry binding %q / %q:\n%s", display, binding.Description, help)
		}
	}
	if !strings.Contains(help, "e export") {
		t.Fatalf("help omitted the export shortcut:\n%s", help)
	}
}

func TestHelpFitsMinimumTerminal(t *testing.T) {
	model := loadedTestModel()
	model.request.Width, model.request.Height = minimumWidth, minimumHeight
	model = updateKeyForTest(t, model, "?")
	lines := strings.Split(strings.TrimSuffix(model.View(), "\n"), "\n")
	if len(lines) > minimumHeight {
		t.Fatalf("help rendered %d lines, want at most %d:\n%s", len(lines), minimumHeight, model.View())
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width > minimumWidth {
			t.Fatalf("help line %d rendered width %d, want at most %d:\n%s", index+1, width, minimumWidth, model.View())
		}
	}
}

func TestActivePageOwnsPageSpecificKeys(t *testing.T) {
	model := loadedTestModel()
	model = updateKeyForTest(t, model, "3")
	if model.router.ActivePage().ID() != ModelsPageID || model.router.ActiveIndex() != int(ModelsTab) {
		t.Fatalf("models selection = page %v index %v", model.router.ActivePage().ID(), model.router.ActiveIndex())
	}
	model = updateKeyForTest(t, model, "s")
	if model.request.ModelSort != 1 {
		t.Fatalf("models page did not handle sort key: %+v", model.request)
	}
	model = updateKeyForTest(t, model, "4")
	model = updateKeyForTest(t, model, "y")
	if model.router.ActivePage().ID() != HeatmapPageID || !model.request.HeatmapYear {
		t.Fatalf("heatmap page did not handle year key: page=%v request=%+v", model.router.ActivePage().ID(), model.request)
	}
}

func TestVaultPageAdapterLaunchesOneShotVerification(t *testing.T) {
	page := NewVaultPage()
	context := PageContext{
		Render:   testRender(),
		Snapshot: Snapshot{Vault: tuipages.VaultPageData{Directory: "/tmp/vault", Format: "v1"}},
		Request:  Request{},
		Width:    60,
		Height:   10,
	}
	request, changed := page.Update(context, "v")
	if !changed || request.Action != VerifyVaultAction || !page.NeedsReload(context, request) {
		t.Fatalf("verification update = changed %v request %#v", changed, request)
	}
	context.Request = request
	if _, changed := page.Update(context, "v"); changed {
		t.Fatal("vault verification was allowed to queue twice")
	}
}

func TestPageActionReportsProgressAndCompletion(t *testing.T) {
	model := loadedTestModel()
	model.loader = func(request Request) (Snapshot, error) {
		if request.Action != VerifyVaultAction {
			return Snapshot{}, errors.New("vault action was not passed to the loader")
		}
		return Snapshot{ActionStatus: "vault verified · 4 files checked"}, nil
	}
	model.router = newPageRouter(NewVaultPage())

	updated, command := model.Update(keyMsg("v"))
	model = updated.(Model)
	if command == nil || !model.syncing || model.status != "verifying vault…" {
		t.Fatalf("action start = command %v syncing %v status %q", command != nil, model.syncing, model.status)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if model.syncing || model.actionInFlight != "" || model.request.Action != "" || model.status != "vault verified · 4 files checked" {
		t.Fatalf("action completion = syncing %v in-flight %q action %q status %q", model.syncing, model.actionInFlight, model.request.Action, model.status)
	}

	model = loadedTestModel()
	model.router = newPageRouter(NewVaultPage())
	model.loader = func(Request) (Snapshot, error) { return Snapshot{}, errors.New("verification failed") }
	updated, command = model.Update(keyMsg("v"))
	model = updated.(Model)
	if command == nil {
		t.Fatal("failed action returned no command")
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if model.syncing || model.actionInFlight != "" || model.request.Action != "" || model.status != "" || model.warning != "verification failed" {
		t.Fatalf("action failure = syncing %v in-flight %q action %q status %q warning %q", model.syncing, model.actionInFlight, model.request.Action, model.status, model.warning)
	}
}

func TestSessionsPageSupportsFiltersSelectionAndDetail(t *testing.T) {
	model := loadedTestModel()
	model.request.Width, model.request.Height = 80, 18
	model.snapshot.Sessions = testSessionPageData()
	model.router.Select(SessionsPageID)
	if !strings.Contains(model.View(), "SESSIONS") || !strings.Contains(model.View(), "FIRST PROMPT") {
		t.Fatalf("sessions page did not render:\n%s", model.View())
	}
	model = updateKeyForTest(t, model, "down")
	if model.request.SessionOffset != 1 {
		t.Fatalf("session selection offset = %d", model.request.SessionOffset)
	}
	model = updateKeyForTest(t, model, "enter")
	if model.request.SessionDetailID != "ses_second" || !strings.Contains(model.View(), "SESSION DETAIL") {
		t.Fatalf("session detail did not open: request=%+v\n%s", model.request, model.View())
	}
	model = updateKeyForTest(t, model, "down")
	if model.request.SessionDetailOffset == 0 || !strings.Contains(model.View(), "more") {
		t.Fatalf("session detail did not scroll: request=%+v\n%s", model.request, model.View())
	}
	model = updateKeyForTest(t, model, "home")
	if model.request.SessionDetailOffset != 0 {
		t.Fatalf("session detail home offset = %d", model.request.SessionDetailOffset)
	}
	model = updateKeyForTest(t, model, "esc")
	if model.request.SessionDetailID != "" || !strings.Contains(model.View(), "SESSIONS") {
		t.Fatalf("session detail did not close: request=%+v\n%s", model.request, model.View())
	}
	model = updateKeyForTest(t, model, "f")
	if model.request.SessionProject != "alpha" || !model.request.SessionProjectActive || model.request.SessionOffset != 0 || model.request.SessionCursor != "" {
		t.Fatalf("project filter state = %+v", model.request)
	}
	model.request.SessionCursor = "stale-cursor"
	model.request.SessionCursorStack = "\x00previous-cursor"
	model.request.SessionDetailID = "ses_second"
	model = updateKeyForTest(t, model, "R")
	if model.request.SessionCursor != "" || model.request.SessionCursorStack != "" || model.request.SessionDetailID != "" || model.request.SessionProjectActive {
		t.Fatalf("refresh did not reset session navigation: %+v", model.request)
	}
}

func TestSessionsPageCanReturnToPreviousCursorPage(t *testing.T) {
	model := loadedTestModel()
	model.snapshot.Sessions = testSessionPageData()
	model.snapshot.Sessions.HasMore = true
	model.snapshot.Sessions.NextCursor = "cursor-page-two"
	model.router.Select(SessionsPageID)
	model.request.SessionOffset = len(model.snapshot.Sessions.Sessions) - 1
	model = updateKeyForTest(t, model, "down")
	if model.request.SessionCursor != "cursor-page-two" || model.request.SessionCursorStack != "\x00" {
		t.Fatalf("forward cursor state = %+v", model.request)
	}
	model.snapshot.Sessions = tuipages.SessionPageData{
		IndexAvailable: true,
		Sessions:       []historystore.CatalogSession{{SessionID: "ses_third", Provider: history.ProviderCodex, Project: "gamma"}},
	}
	model = updateKeyForTest(t, model, "up")
	if model.request.SessionCursor != "" || model.request.SessionCursorStack != "" || !model.request.SessionReturnToEnd {
		t.Fatalf("backward cursor state = %+v", model.request)
	}
}

func TestSessionsProjectFilterResetsCursorHistory(t *testing.T) {
	model := loadedTestModel()
	model.snapshot.Sessions = testSessionPageData()
	model.snapshot.Sessions.Projects = []tuipages.ProjectOption{{Key: "alpha", Label: "alpha"}}
	model.router.Select(SessionsPageID)
	model.request.SessionCursor = "cursor-page-two"
	model.request.SessionCursorStack = "\x00cursor-page-one"
	model.request.SessionOffset = 3
	model = updateKeyForTest(t, model, "f")
	if model.request.SessionCursor != "" || model.request.SessionCursorStack != "" || model.request.SessionOffset != 0 {
		t.Fatalf("project filter retained cursor history: %+v", model.request)
	}
}

func TestSessionsPageShowsIndexHintWhenHistoryIsAbsent(t *testing.T) {
	model := loadedTestModel()
	model.router.Select(SessionsPageID)
	view := model.View()
	if !strings.Contains(view, "No history index is available.") || !strings.Contains(view, "tokenomnom history index") {
		t.Fatalf("empty history hint missing:\n%s", view)
	}
}

func TestLedgerPageHandlesContextualZoomAndSelectionKeys(t *testing.T) {
	model := loadedTestModel()
	model.snapshot.Ledger = tuipages.Data{Available: true, Zoom: tuipages.ZoomYear, Rows: []tuipages.Row{
		{Key: "2026", Label: "2026"},
		{Key: "2025", Label: "2025"},
	}}
	model.router.SelectIndex(int(LedgerTab))
	model.snapshot.Views[LedgerTab] = "stale ledger view"
	if strings.Contains(model.View(), "stale ledger view") {
		t.Fatal("ledger view used the stale pre-rendered snapshot view")
	}

	updated, command := model.Update(keyMsg("l"))
	model = updated.(Model)
	if command == nil || model.request.Ledger.Zoom != tuipages.ZoomMonth || model.request.Ledger.Year != 2026 {
		t.Fatalf("ledger year zoom = %+v, command=%v", model.request.Ledger, command != nil)
	}

	model.snapshot.Ledger = tuipages.Data{Available: true, Zoom: tuipages.ZoomMonth, Year: 2026, Rows: []tuipages.Row{
		{Key: "2026-07", Label: "Jul 2026"},
		{Key: "2026-06", Label: "Jun 2026"},
	}}
	updated, command = model.Update(keyMsg("l"))
	model = updated.(Model)
	if command == nil || model.request.Ledger.Zoom != tuipages.ZoomDay || model.request.Ledger.Month != "2026-07" {
		t.Fatalf("ledger month zoom = %+v, command=%v", model.request.Ledger, command != nil)
	}

	model.snapshot.Ledger = tuipages.Data{Available: true, Zoom: tuipages.ZoomDay, Month: "2026-07", Rows: []tuipages.Row{
		{Key: "2026-07-14", Label: "Jul 14"},
		{Key: "2026-07-13", Label: "Jul 13"},
	}}
	updated, command = model.Update(keyMsg("j"))
	model = updated.(Model)
	if command != nil || model.request.Ledger.Cursor != 1 {
		t.Fatalf("ledger j selection = %+v, command=%v", model.request.Ledger, command != nil)
	}
	updated, command = model.Update(keyMsg("home"))
	model = updated.(Model)
	if command != nil || model.request.Ledger.Cursor != 0 {
		t.Fatalf("ledger home selection = %+v, command=%v", model.request.Ledger, command != nil)
	}
	updated, command = model.Update(keyMsg("end"))
	model = updated.(Model)
	if command != nil || model.request.Ledger.Cursor != 1 {
		t.Fatalf("ledger end selection = %+v, command=%v", model.request.Ledger, command != nil)
	}
}

func TestLedgerPageExpandsDayNavigatesSessionsAndReturnsFromDetail(t *testing.T) {
	model := loadedTestModel()
	model.router.Select(LedgerPageID)
	first := "2026-07-14T09:30:00Z"
	day := tuipages.Row{Key: "2026-07-14", Label: "Jul 14", Codex: tuipages.ProviderTotals{Tokens: 300, PricedTokens: 300}}
	model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07", Cursor: -1}
	model.snapshot.Ledger = tuipages.Data{Available: true, Zoom: tuipages.ZoomDay, Month: "2026-07", Rows: []tuipages.Row{day}, Total: day}

	updated, command := model.Update(keyMsg("l"))
	model = updated.(Model)
	if command == nil || model.request.Ledger.ExpandedDay != day.Key {
		t.Fatalf("ledger expansion = %+v command=%v", model.request.Ledger, command != nil)
	}
	model.snapshot.Ledger.SessionDay = day.Key
	model.snapshot.Ledger.SessionIndexAvailable = true
	model.snapshot.Ledger.Sessions = []tuipages.LedgerSession{
		{CatalogSession: historystore.CatalogSession{SessionID: "ses_first", Provider: history.ProviderCodex, Project: "alpha", Preview: strings.Repeat("first prompt ", 40), FirstTimestamp: &first}},
		{CatalogSession: historystore.CatalogSession{SessionID: "ses_second", Provider: history.ProviderClaude, Project: "beta", Preview: strings.Repeat("second prompt ", 40), FirstTimestamp: &first}},
	}
	model = updateKeyForTest(t, model, "down")
	if model.request.Ledger.SessionCursor != 1 {
		t.Fatalf("ledger session cursor = %+v", model.request.Ledger)
	}
	model = updateKeyForTest(t, model, "enter")
	if model.request.Ledger.DetailID != "ses_second" || !strings.Contains(model.View(), "SESSION DETAIL") {
		t.Fatalf("ledger session detail = %+v\n%s", model.request.Ledger, model.View())
	}
	model = updateKeyForTest(t, model, "end")
	if model.request.Ledger.DetailOffset == 0 {
		t.Fatalf("ledger detail did not scroll: %+v", model.request.Ledger)
	}
	model = updateKeyForTest(t, model, "esc")
	if model.request.Ledger.DetailID != "" || model.request.Ledger.ExpandedDay == "" {
		t.Fatalf("ledger detail back = %+v", model.request.Ledger)
	}
	model = updateKeyForTest(t, model, "esc")
	if model.request.Ledger.ExpandedDay != "" {
		t.Fatalf("ledger collapse = %+v", model.request.Ledger)
	}
}

func TestProviderChangeRestartsExpandedLedgerSessionPaging(t *testing.T) {
	model := loadedTestModel()
	model.request.Ledger = tuipages.State{
		Zoom: tuipages.ZoomDay, ExpandedDay: "2026-07-14", SessionCursor: 7,
		SessionPageCursor: "codex-page-two", SessionCursorStack: "\x00", DetailID: "ses_codex", DetailOffset: 3,
	}
	updated, command := model.Update(keyMsg("p"))
	model = updated.(Model)
	if command == nil || model.request.Provider != CodexProvider || model.request.Ledger.ExpandedDay != "2026-07-14" || model.request.Ledger.SessionPageCursor != "" || model.request.Ledger.SessionCursorStack != "" || model.request.Ledger.DetailID != "" {
		t.Fatalf("provider change retained incompatible ledger page state: %+v command=%v", model.request, command != nil)
	}
}

func TestFooterKeepsHintsUnderLongWarning(t *testing.T) {
	model := loadedTestModel()
	model.request.Width, model.request.Height = 60, 18
	model.warning = "history index is stale: run 'tokenomnom history index' to refresh before searching"

	view := model.View()
	if !strings.Contains(view, "q quit") {
		t.Fatalf("quit hint lost under long warning:\n%s", view)
	}
	if !strings.Contains(view, "history index is stale") || !strings.Contains(view, "…") {
		t.Fatalf("long warning was not preserved with truncation:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 18 {
		t.Fatalf("warning view rendered %d lines, want 18", len(lines))
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 60 {
			t.Fatalf("warning view line %d has width %d, want 60:\n%s", index+1, width, view)
		}
	}
}

func TestSkillOfferAcceptInstallsRecordsAndShowsResults(t *testing.T) {
	var choices []SkillOfferChoice
	model := offerTestModel(func() ([]string, error) {
		return []string{"Codex: installed vdev", "Claude: up to date vdev"}, nil
	}, func(choice SkillOfferChoice) error {
		choices = append(choices, choice)
		return nil
	})
	updated, _ := model.Update(skillOfferCheckedMsg{check: SkillOfferCheck{HasRoots: true}})
	model = updated.(Model)
	if model.offerState != skillOfferPrompt || !strings.Contains(model.View(), "Teach your agents to use tokenomnom?") {
		t.Fatalf("offer did not appear: state=%v\n%s", model.offerState, model.View())
	}

	updated, installCmd := model.Update(keyMsg("y"))
	model = updated.(Model)
	if model.offerState != skillOfferInstalling || installCmd == nil {
		t.Fatalf("accept state=%v command=%v", model.offerState, installCmd != nil)
	}
	updated, recordCmd := model.Update(installCmd())
	model = updated.(Model)
	if model.offerState != skillOfferResult || recordCmd == nil || !strings.Contains(model.View(), "Codex: installed vdev") {
		t.Fatalf("result state=%v command=%v\n%s", model.offerState, recordCmd != nil, model.View())
	}
	recordCmd()
	if !reflect.DeepEqual(choices, []SkillOfferChoice{SkillOfferAccepted}) {
		t.Fatalf("recorded choices = %v", choices)
	}
	updated, _ = model.Update(keyMsg("x"))
	if updated.(Model).offerState != skillOfferHidden {
		t.Fatal("result overlay did not dismiss on a key")
	}
}

func TestSkillOfferDeclineKeysRecordAndShowHint(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "n", key: keyMsg("n")},
		{name: "N", key: keyMsg("N")},
		{name: "escape", key: tea.KeyMsg{Type: tea.KeyEsc}},
		{name: "enter", key: tea.KeyMsg{Type: tea.KeyEnter}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var choices []SkillOfferChoice
			model := offerTestModel(nil, func(choice SkillOfferChoice) error {
				choices = append(choices, choice)
				return nil
			})
			model.syncing = true
			model.offerState = skillOfferPrompt
			updated, command := model.Update(test.key)
			model = updated.(Model)
			if command == nil || model.offerState != skillOfferHidden || !strings.Contains(model.View(), "skill not installed — run 'tokenomnom install-skill' anytime") {
				t.Fatalf("decline state=%v command=%v\n%s", model.offerState, command != nil, model.View())
			}
			command()
			if !reflect.DeepEqual(choices, []SkillOfferChoice{SkillOfferDeclined}) {
				t.Fatalf("recorded choices = %v", choices)
			}
		})
	}
}

func TestSkillOfferQuitRecordsDeclineAndQuits(t *testing.T) {
	var choice SkillOfferChoice
	model := offerTestModel(nil, func(value SkillOfferChoice) error {
		choice = value
		return nil
	})
	model.offerState = skillOfferPrompt
	_, command := model.Update(keyMsg("q"))
	if command == nil {
		t.Fatal("quit returned no command")
	}
	if _, ok := command().(tea.QuitMsg); !ok || choice != SkillOfferDeclined {
		t.Fatalf("quit result choice=%v", choice)
	}
}

func TestSkillOfferEligibilityAndInertKeys(t *testing.T) {
	tests := []struct {
		name       string
		message    skillOfferCheckedMsg
		wantChoice SkillOfferChoice
	}{
		{name: "installed", message: skillOfferCheckedMsg{check: SkillOfferCheck{HasRoots: true, Installed: true}}, wantChoice: SkillOfferPreinstalled},
		{name: "no roots", message: skillOfferCheckedMsg{check: SkillOfferCheck{}}, wantChoice: 0},
		{name: "meta error", message: skillOfferCheckedMsg{err: errors.New("broken meta")}, wantChoice: 0},
		{name: "accepted", message: skillOfferCheckedMsg{check: SkillOfferCheck{Answered: true, HasRoots: true}}, wantChoice: 0},
		{name: "declined", message: skillOfferCheckedMsg{check: SkillOfferCheck{Answered: true, HasRoots: true}}, wantChoice: 0},
		{name: "preinstalled", message: skillOfferCheckedMsg{check: SkillOfferCheck{Answered: true, HasRoots: true, Installed: true}}, wantChoice: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var choice SkillOfferChoice
			model := offerTestModel(nil, func(value SkillOfferChoice) error {
				choice = value
				return nil
			})
			updated, command := model.Update(test.message)
			model = updated.(Model)
			if command != nil {
				command()
			}
			if model.offerState != skillOfferHidden || choice != test.wantChoice {
				t.Fatalf("state=%v choice=%v", model.offerState, choice)
			}
		})
	}

	model := offerTestModel(nil, nil)
	model.offerState = skillOfferPrompt
	before := model.request
	for _, key := range []tea.KeyMsg{{Type: tea.KeyTab}, keyMsg("p"), keyMsg("r"), keyMsg("4"), tea.KeyMsg{Type: tea.KeyRight}} {
		updated, command := model.Update(key)
		model = updated.(Model)
		if command != nil || model.request != before || model.offerState != skillOfferPrompt {
			t.Fatalf("overlay leaked key %q: request=%+v command=%v", key.String(), model.request, command != nil)
		}
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	model = updated.(Model)
	if model.offerState != skillOfferPrompt || !strings.Contains(model.View(), "Teach your agents") {
		t.Fatal("resize lost or obscured offer")
	}
}

func TestSkillOfferCheckWaitsForInitialData(t *testing.T) {
	offer := SkillOffer{Check: func() (SkillOfferCheck, error) { return SkillOfferCheck{}, nil }}
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, offer)
	updated, _ := model.Update(loadedMessage(model, model.request, Snapshot{Empty: true}))
	model = updated.(Model)
	if model.offerChecked {
		t.Fatal("empty first-run store checked offer before initial sync")
	}
	syncRequest := model.request
	syncRequest.Sync = true
	updated, command := model.Update(loadedMessage(model, syncRequest, Snapshot{}))
	model = updated.(Model)
	if !model.offerChecked || command == nil {
		t.Fatalf("offer was not checked after initial sync: checked=%v command=%v", model.offerChecked, command != nil)
	}

	populatedOffer := SkillOffer{Check: func() (SkillOfferCheck, error) { return SkillOfferCheck{HasRoots: true}, nil }}
	model = New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, populatedOffer)
	updated, command = model.Update(loadedMessage(model, model.request, Snapshot{Empty: false}))
	model = updated.(Model)
	if !model.offerChecked || !model.pendingSync || command == nil {
		t.Fatalf("populated store startup = checked %v, pending sync %v, command %v", model.offerChecked, model.pendingSync, command != nil)
	}
	updated, command = model.Update(command())
	model = updated.(Model)
	if model.offerState != skillOfferPrompt || !model.pendingSync || command != nil {
		t.Fatalf("offer did not precede background sync: state=%v pending=%v command=%v", model.offerState, model.pendingSync, command != nil)
	}
	updated, command = model.Update(keyMsg("n"))
	model = updated.(Model)
	if command == nil {
		t.Fatal("decline did not issue metadata write")
	}
	updated, command = model.Update(command())
	model = updated.(Model)
	if model.pendingSync || command == nil {
		t.Fatalf("background sync did not resume after metadata write: pending=%v command=%v", model.pendingSync, command != nil)
	}
}

func TestHistorySearchArrowKeysStayInInput(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	model := loadedTestModel()
	model.router = newRouter(page)
	if !model.router.Select(HistorySearchPageID) {
		t.Fatal("history search page was not registered")
	}

	model = updateKeyForTest(t, model, "/")
	model = updateKeyForTest(t, model, "d")
	model = updateKeyForTest(t, model, "left")
	if model.request.HistoryQuery != "d" || !page.Editing() {
		t.Fatalf("arrow key changed query or focus: query=%q editing=%v", model.request.HistoryQuery, page.Editing())
	}
}

func TestSkillOfferFitsMinimumTerminal(t *testing.T) {
	model := offerTestModel(nil, nil)
	model.request.Width, model.request.Height = minimumWidth, minimumHeight
	for _, state := range []skillOfferState{skillOfferPrompt, skillOfferInstalling, skillOfferResult} {
		model.offerState = state
		model.offerResults = []string{"Codex: installed vdev · /a/very/long/provider/root/that/must/wrap/without/overflowing/the/modal/skills/tokenomnom/SKILL.md"}
		view := model.View()
		lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
		if len(lines) > minimumHeight {
			t.Fatalf("state %v rendered %d lines, want at most %d:\n%s", state, len(lines), minimumHeight, view)
		}
		for _, line := range lines {
			if width := lipgloss.Width(line); width > minimumWidth {
				t.Fatalf("state %v rendered width %d, want at most %d:\n%s", state, width, minimumWidth, view)
			}
		}
	}
}

func loadedTestModel() Model {
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{})
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded, model.dashboardLoadBusy = false, true, false
	model.snapshot = Snapshot{
		Summary: Summary{Metrics: [5]SummaryMetric{
			{Value: "$1.00", Kind: MetricMoney},
			{Value: "100"},
			{Value: "2"},
			{Value: "$0.50", Kind: MetricMoney},
			{Value: "$1.00", Kind: MetricMoney},
		}},
		DailyCursorMax: 2,
		Views:          [4]string{"daily body", "", "models body", "heatmap body"},
	}
	ledgerRow := tuipages.Row{Key: "2026", Label: "2026", Codex: tuipages.ProviderTotals{Tokens: 100, PricedTokens: 100}}
	model.snapshot.Ledger = tuipages.Data{Available: true, Zoom: tuipages.ZoomYear, Rows: []tuipages.Row{ledgerRow}, Total: ledgerRow}
	return model
}

func realisticEvidenceModel() Model {
	model := loadedTestModel()
	model.snapshot.Summary = Summary{Metrics: [5]SummaryMetric{
		{Value: "$3,033.35", Kind: MetricMoney},
		{Value: "178,000,000"},
		{Value: "2"},
		{Value: "$1,516.68", Kind: MetricMoney},
		{Value: "$2,209.23", Kind: MetricMoney},
	}}
	model.snapshot.Views = [4]string{
		realisticDailyEvidenceView(),
		"",
		"PROVIDER  MODEL                 TOKENS       COST\n" +
			"codex     gpt-5.2             143,200,000  $2,408.35\n" +
			"claude    claude-sonnet         34,800,000    $625.00",
		"2026\nJul  ·······································\n" +
			"Less ·░▒▓█ More\n2 active days · total cost $3,033.35",
	}
	model.snapshot.Sessions = testSessionPageData()
	model.snapshot.Sessions.Sessions[0].Preview = "Investigate the production latency regression"
	model.snapshot.Sessions.Sessions[1].Preview = "Prepare the migration rollout plan"
	july14 := tuipages.Row{
		Key: "2026-07-14", Label: "Jul 14",
		Codex:  tuipages.ProviderTotals{Cost: pricing.Money(1_584_230_000_000), Tokens: 91_200_000, PricedTokens: 91_200_000},
		Claude: tuipages.ProviderTotals{Cost: pricing.Money(625_000_000_000), Tokens: 34_800_000, PricedTokens: 34_800_000},
	}
	july13 := tuipages.Row{
		Key: "2026-07-13", Label: "Jul 13",
		Codex: tuipages.ProviderTotals{Cost: pricing.Money(824_120_000_000), Tokens: 52_000_000, PricedTokens: 52_000_000},
	}
	model.snapshot.Ledger = tuipages.Data{
		Available: true, Zoom: tuipages.ZoomDay, Month: "2026-07", Rows: []tuipages.Row{july14, july13},
		Total: july14.Add(july13),
	}
	model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07"}
	return model
}

func realisticDailyEvidenceView() string {
	return strings.Join([]string{
		"■ Codex  ■ Claude  cost/day",
		" $1,584.23       █████████████████████████████",
		"   $824.12       ████████████",
		"                 13       14       ^",
		"                 Jul 2026",
		"----------------------------------------------------------",
		"DAY DETAIL",
		"2026-07-14",
		"TOTAL $2,209.23",
		"",
		"PROVIDER SPLIT · COST",
		"Codex  72% ################ $1,584.23",
		"Claude 28% ##########         $625.00",
		"",
		"TOP MODELS BY COST",
		"gpt-5.2                  $1,584.23",
		"claude-sonnet              $625.00",
	}, "\n")
}

func loadedMessage(model Model, request Request, snapshot Snapshot) loadedMsg {
	return loadedMsg{request: request, generation: model.loadGeneration, snapshot: snapshot}
}

func loadedErrorMessage(model Model, request Request, err error) loadedMsg {
	return loadedMsg{request: request, generation: model.loadGeneration, err: err}
}

func offerTestModel(install func() ([]string, error), record func(SkillOfferChoice) error) Model {
	model := loadedTestModel()
	model.offer = SkillOffer{Install: install, Record: record}
	return model
}

func keyMsg(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}

func testRender() theme.Context {
	return theme.Context{Mode: theme.Plain, Width: 100, Palette: theme.NewPalette(nil)}
}

func updateKeyForTest(t *testing.T, model Model, key string) Model {
	t.Helper()
	var message tea.KeyMsg
	switch key {
	case "tab":
		message = tea.KeyMsg{Type: tea.KeyTab}
	case "left":
		message = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		message = tea.KeyMsg{Type: tea.KeyRight}
	case "up":
		message = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		message = tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		message = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		message = tea.KeyMsg{Type: tea.KeyEsc}
	case "home":
		message = tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		message = tea.KeyMsg{Type: tea.KeyEnd}
	default:
		message = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, command := model.Update(message)
	model = updated.(Model)
	if command != nil {
		result := command()
		if _, quit := result.(tea.QuitMsg); !quit {
			if loaded, ok := result.(loadedMsg); ok && loaded.err == nil {
				// Keep fixture data while completing synthetic reload commands.
				loaded.snapshot = model.snapshot
				loaded.snapshot.DailyCursor = loaded.request.DailyCursor
				loaded.snapshot.DailyWindowStart = loaded.request.DailyWindowStart
				loaded.snapshot.DailyDetailOffset = loaded.request.DailyDetailOffset
				result = loaded
			}
			updated, _ = model.Update(result)
			model = updated.(Model)
		}
	}
	return model
}

type testPage struct {
	id      PageID
	section PageSection
	title   string
}

func (p testPage) ID() PageID           { return p.id }
func (p testPage) Section() PageSection { return p.section }
func (p testPage) Title() string        { return p.title }
func (p testPage) View(PageContext) string {
	return p.title
}
func (p testPage) Update(context PageContext, _ string) (Request, bool) {
	return context.Request, false
}
func (p testPage) NeedsReload(PageContext, Request) bool { return true }

func testSessionPageData() tuipages.SessionPageData {
	first, last := "2026-07-31T09:30:00Z", "2026-07-31T10:15:00Z"
	return tuipages.SessionPageData{
		IndexAvailable: true,
		Projects:       []tuipages.ProjectOption{{Key: "alpha", Label: "alpha"}, {Key: "beta", Label: "beta"}},
		Sessions: []historystore.CatalogSession{
			{
				SessionID: "ses_first", Provider: history.ProviderCodex, Project: "beta", ProjectSource: history.ProjectSourceGit,
				FirstTimestamp: &first, LastTimestamp: &last, Preview: "first prompt", LogicalPromptCount: 2, OccurrenceCount: 3,
				ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
				PreferredRetrievalSource: "provider-live", Availability: historystore.Availability{ProviderLive: 1, Vault: 1},
			},
			{
				SessionID: "ses_second", Provider: history.ProviderClaude, Project: "alpha", ProjectSource: history.ProjectSourceGit,
				FirstTimestamp: &first, LastTimestamp: &last, Preview: "second prompt", LogicalPromptCount: 4, OccurrenceCount: 6,
				ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
				PreferredRetrievalSource: "provider-live", Availability: historystore.Availability{ProviderLive: 1, ProviderArchive: 1, Vault: 1},
			},
		},
	}
}

type asyncTestPage struct {
	id            PageID
	section       PageSection
	title         string
	loadedRequest Request
	applied       string
	loads         int
}

func (p *asyncTestPage) ID() PageID           { return p.id }
func (p *asyncTestPage) Section() PageSection { return p.section }
func (p *asyncTestPage) Title() string        { return p.title }
func (p *asyncTestPage) View(PageContext) string {
	return p.title
}
func (p *asyncTestPage) Update(context PageContext, _ string) (Request, bool) {
	return context.Request, false
}
func (p *asyncTestPage) NeedsReload(PageContext, Request) bool { return true }
func (p *asyncTestPage) Load(request Request) (any, error) {
	p.loads++
	p.loadedRequest = request
	return "loaded", nil
}
func (p *asyncTestPage) Apply(_ Request, value any, _ error) {
	p.applied, _ = value.(string)
}

type interactiveTestPage struct {
	id      PageID
	section PageSection
	title   string
	editing bool
	lastKey string
}

func (p *interactiveTestPage) ID() PageID           { return p.id }
func (p *interactiveTestPage) Section() PageSection { return p.section }
func (p *interactiveTestPage) Title() string        { return p.title }
func (p *interactiveTestPage) View(PageContext) string {
	return p.title
}
func (p *interactiveTestPage) Update(context PageContext, _ string) (Request, bool) {
	return context.Request, false
}
func (p *interactiveTestPage) NeedsReload(PageContext, Request) bool { return true }
func (p *interactiveTestPage) HandleKey(request Request, key tea.KeyMsg) PageKeyResult {
	p.lastKey = key.String()
	return PageKeyResult{Request: request, Handled: true}
}
func (p *interactiveTestPage) Editing() bool { return p.editing }
