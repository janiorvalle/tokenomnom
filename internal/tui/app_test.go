package tui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

func TestDroppedLoadedSnapshotResumesPendingResize(t *testing.T) {
	model := loadedTestModel()
	model.pendingResize = true
	model.dashboardLoadBusy = true
	staleRequest := model.request
	staleRequest.DailyCursor = 1

	updated, command := model.Update(loadedMsg{
		request:    staleRequest,
		generation: model.loadGeneration,
		snapshot:   Snapshot{Views: [4]string{"stale"}},
	})
	model = updated.(Model)
	if command == nil || !model.dashboardLoadBusy || model.pendingResize {
		t.Fatalf("stale loaded snapshot stranded pending resize: command=%v busy=%v pending=%v", command != nil, model.dashboardLoadBusy, model.pendingResize)
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

func TestQuest143HelpEscClosesAndRestoresPageNavigation(t *testing.T) {
	model := loadedTestModel()
	model = updateKeyForTest(t, model, "?")
	if !model.help {
		t.Fatal("help did not open")
	}
	helpView := model.View()

	model = updateKeyForTest(t, model, "esc")
	helpAfterEsc := model.help
	pageAfterEsc := model.activePageID()
	afterEscView := model.View()
	model = updateKeyForTest(t, model, "2")
	pageTwoView := model.View()
	t.Logf("Source: internal/tui/app_test.go::TestQuest143HelpEscClosesAndRestoresPageNavigation\nCommand: go test -v ./internal/tui -run TestQuest143HelpEscClosesAndRestoresPageNavigation -count=1\n\n-- help before esc --\n%s\n\n-- after esc --\nhelp=%v page=%s\n%s\n\n-- after page 2 --\nhelp=%v page=%s\n%s", helpView, helpAfterEsc, pageAfterEsc, afterEscView, model.help, model.activePageID(), pageTwoView)

	if helpAfterEsc {
		t.Fatal("esc did not close help")
	}
	if model.router.ActiveIndex() != 1 {
		t.Fatalf("page number was swallowed after esc: active index=%d page=%s", model.router.ActiveIndex(), model.activePageID())
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

func TestFloorFooterKeepsDisclaimerAndQuitHint(t *testing.T) {
	model := realisticEvidenceModel()
	model.request.Width, model.request.Height = 80, 24
	view := model.View()
	if !strings.Contains(view, "API list-price equivalents, not actual bills") || !strings.Contains(view, "q quit") {
		t.Fatalf("floor footer omitted required copy:\n%s", view)
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

func TestQuest146DailyFullWindowFrames(t *testing.T) {
	for _, size := range []struct{ width, height int }{{192, 66}, {120, 40}, {80, 24}} {
		model := realisticEvidenceModel()
		model.request.Width, model.request.Height = size.width, size.height
		model.render.Width = size.width
		model.snapshot.StatusBar.LastSyncUnix = 0
		layout := newCockpitLayout(size.width, size.height)
		render := model.render
		render.Width = layout.paneWidth
		model.snapshot.Views[0] = tuipages.RenderDaily(render, quest146DailyFrameData(), size.width, size.height, layout.bodyHeight, 0)
		view := model.View()
		lines := strings.Split(view, "\n")
		if len(lines) != size.height {
			t.Fatalf("%dx%d rendered %d rows, want %d:\n%s", size.width, size.height, len(lines), size.height, view)
		}
		for index, line := range lines {
			if width := lipgloss.Width(line); width != size.width {
				t.Fatalf("%dx%d row %d width=%d, want %d:\n%s", size.width, size.height, index+1, width, size.width, view)
			}
		}
		fragments := []string{"tokenomnom", "COST / DAY", "idle"}
		if size.width >= 100 {
			fragments = append(fragments, "DAILY", "SESSIONS")
		} else {
			fragments = append(fragments, "DAY DETAIL")
		}
		for _, fragment := range fragments {
			if !strings.Contains(view, fragment) {
				t.Fatalf("%dx%d full window missing %q:\n%s", size.width, size.height, fragment, view)
			}
		}
		t.Logf("FRAME: Daily full window %dx%d\nSource: internal/tui/app_test.go::TestQuest146DailyFullWindowFrames\nCommand: go test ./internal/tui -run TestQuest146DailyFullWindowFrames -count=1 -v\n\n%s", size.width, size.height, view)
	}
}

func TestQuest147LedgerFullWindowFrames(t *testing.T) {
	for _, size := range []struct{ width, height int }{{192, 66}, {120, 40}, {80, 24}} {
		model := realisticEvidenceModel()
		model.router.Select(LedgerPageID)
		model.request.Width, model.request.Height = size.width, size.height
		model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomMonth, Year: 2026, Cursor: -1}
		model.render.Width = size.width
		model.snapshot.StatusBar.LastSyncUnix = 0
		model.snapshot.Ledger = quest147LedgerPeriodsFrameData()

		view := model.View()
		assertFullWindowFrame(t, view, size.width, size.height, "Ledger periods")
		fragments := []string{"tokenomnom", "LEDGER"}
		if size.width >= 96 {
			fragments = append(fragments, "PERIODS", "SPEND BY MONTH")
		} else {
			fragments = append(fragments, "PERIOD", "TOTAL")
		}
		if size.width >= 160 {
			fragments = append(fragments, "PERIOD DETAIL", "PROJECT × MONTH", "WEEKDAY PROFILE", "HOUR OF DAY")
		}
		for _, fragment := range fragments {
			if !strings.Contains(view, fragment) {
				t.Fatalf("%dx%d full window missing %q:\n%s", size.width, size.height, fragment, view)
			}
		}
		t.Logf("FRAME: Ledger periods full window %dx%d\nSource: internal/tui/app_test.go::TestQuest147LedgerFullWindowFrames\nCommand: GOFLAGS=-buildvcs=false go test ./internal/tui -run TestQuest147LedgerFullWindowFrames -count=1 -v\n\n%s", size.width, size.height, view)
	}
}

func TestQuest147LedgerDayFullWindowFrame(t *testing.T) {
	const width, height = 192, 66
	model := realisticEvidenceModel()
	model.router.Select(LedgerPageID)
	model.request.Width, model.request.Height = width, height
	model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomDay, Month: "2026-07", ExpandedDay: "2026-07-14"}
	model.render.Width = width
	model.snapshot.StatusBar.LastSyncUnix = 0
	model.snapshot.Ledger = quest147LedgerDayFrameData()

	view := model.View()
	assertFullWindowFrame(t, view, width, height, "Ledger day")
	for _, fragment := range []string{"tokenomnom", "LEDGER", "TIME", "PROVIDER", "SESSION", "FIRST PROMPT", "MODELS ON THIS DAY", "PROJECTS ON THIS DAY", "SESSION STARTS BY HOUR", "OVERVIEW", "COST & TOKENS", "Investigate the production latency regression"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("day full window missing %q:\n%s", fragment, view)
		}
	}
	t.Logf("FRAME: Ledger day full window %dx%d\nSource: internal/tui/app_test.go::TestQuest147LedgerDayFullWindowFrame\nCommand: GOFLAGS=-buildvcs=false go test ./internal/tui -run TestQuest147LedgerDayFullWindowFrame -count=1 -v\n\n%s", width, height, view)
}

func TestQuest148ModelsFullWindowFrames(t *testing.T) {
	for _, size := range []struct{ width, height int }{{192, 66}, {120, 40}, {80, 24}} {
		model := realisticEvidenceModel()
		model.router.Select(ModelsPageID)
		model.request.Width, model.request.Height = size.width, size.height
		model.render.Width = size.width
		model.snapshot.StatusBar.LastSyncUnix = 0
		layout := newCockpitLayout(size.width, size.height)
		render := model.render
		render.Width = layout.paneWidth
		model.snapshot.Views[ModelsTab] = tuipages.RenderModels(render, quest148ModelsFrameData(), tuipages.ModelsViewport{
			Width:    layout.paneWidth,
			Height:   layout.bodyHeight,
			Wide:     layout.tiers.Width == WidthWide,
			Tall:     layout.tiers.Height == HeightTall,
			Standard: layout.tiers.Width == WidthStandard,
		})

		view := model.View()
		assertFullWindowFrame(t, view, size.width, size.height, "Models")
		fragments := []string{"tokenomnom", "MODELS", "TOKENS", "COST"}
		if size.width >= 160 {
			fragments = append(fragments, "MODEL × DAY", "RANK", "30-DAY COST")
		} else if size.width >= 96 {
			fragments = append(fragments, "ANALYSIS", "COST PER 1M TOKENS")
		} else {
			fragments = append(fragments, "PROVIDER", "MODEL")
		}
		for _, fragment := range fragments {
			if !strings.Contains(view, fragment) {
				t.Fatalf("%dx%d full window missing %q:\n%s", size.width, size.height, fragment, view)
			}
		}
		if size.width >= 160 {
			assertNoBlankBandRuns(t, model.snapshot.Views[ModelsTab], 3)
			if strings.Contains(model.snapshot.Views[ModelsTab], "↓ more models") {
				t.Fatalf("wide master table still pages a populated fixture:\n%s", model.snapshot.Views[ModelsTab])
			}
		}
		if evidenceDir := os.Getenv("QUEST_EVIDENCE_DIR"); evidenceDir != "" {
			if err := writeQuest148FrameEvidence(evidenceDir, size.width, size.height, view); err != nil {
				t.Fatalf("write %dx%d frame evidence: %v", size.width, size.height, err)
			}
		}
		t.Logf("FRAME: Models full window %dx%d\nSource: internal/tui/app_test.go::TestQuest148ModelsFullWindowFrames\nCommand: GOFLAGS=-buildvcs=false go test ./internal/tui -run TestQuest148ModelsFullWindowFrames -count=1 -v\n\n%s", size.width, size.height, view)
	}
}

func assertNoBlankBandRuns(t *testing.T, body string, maximum int) {
	t.Helper()
	band := 1
	blankRun := 0
	check := func() {
		if blankRun > maximum {
			t.Fatalf("Models band %d has %d consecutive blank rows, maximum %d:\n%s", band, blankRun, maximum, body)
		}
		blankRun = 0
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) != "" && strings.Trim(line, "─ ") == "" {
			check()
			band++
			continue
		}
		if strings.TrimSpace(line) == "" {
			blankRun++
		} else {
			check()
		}
	}
	check()
}

func writeQuest148FrameEvidence(directory string, width, height int, view string) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	filename := fmt.Sprintf("frame-a-models-rendered-%dx%d.txt", width, height)
	content := fmt.Sprintf("Source: internal/tui/app_test.go::TestQuest148ModelsFullWindowFrames\nCommand: QUEST_EVIDENCE_DIR=%s go test ./internal/tui -run TestQuest148ModelsFullWindowFrames -count=1 -v\n\n%s\n", directory, view)
	return os.WriteFile(filepath.Join(directory, filename), []byte(content), 0o644)
}

func assertFullWindowFrame(t *testing.T, view string, width, height int, state string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("%s %dx%d rendered %d rows, want %d:\n%s", state, width, height, len(lines), height, view)
	}
	for index, line := range lines {
		if renderedWidth := lipgloss.Width(line); renderedWidth != width {
			t.Fatalf("%s %dx%d row %d width=%d, want %d:\n%s", state, width, height, index+1, renderedWidth, width, view)
		}
	}
}

func quest148ModelsFrameData() tuipages.ModelPageData {
	data := tuipages.ModelPageData{ScopeLabel: "ALL TIME"}
	var totalTokens int64
	var totalCost pricing.Money
	for index := 0; index < 12; index++ {
		provider := "codex"
		if index%2 == 1 {
			provider = "claude"
		}
		tokens := int64(120-index*6) * 1_000_000
		cost := pricing.Money(int64(index+2) * 1_500_000_000)
		row := tuipages.ModelPageRow{
			Provider: provider, Model: fmt.Sprintf("model-%02d", index), Tokens: tokens,
			Cost: cost, PricedTokens: tokens, TokenShare: float64(tokens) / 1_044_000_000,
			CostShare: float64(index+2) / 90, Pricing: "live", Sessions: index + 2,
			Days: 30 - index, FirstDate: "2026-07-03", LastDate: "2026-08-01",
		}
		for spark := 0; spark < 10; spark++ {
			value := float64((index + 1) * (spark + 1))
			switch index {
			case 0:
				if spark == 5 {
					value *= 4
				}
			case 1:
				value = float64((index + 1) * (10 - spark))
			case 2:
				if spark == 4 || spark == 5 {
					value = 0
				}
			}
			row.Sparkline = append(row.Sparkline, value)
		}
		data.Rows = append(data.Rows, row)
		totalTokens += tokens
		totalCost += cost
	}
	data.Total = tuipages.ModelPageRow{
		Provider: "TOTAL", Model: "12 models", Tokens: totalTokens, Cost: totalCost,
		PricedTokens: totalTokens, TokenShare: 1, CostShare: 1, Pricing: "12 priced",
		Sessions: 90, Days: 30, FirstDate: "2026-07-03", LastDate: "2026-08-01",
	}
	data.Providers = []tuipages.ModelProviderRow{
		{Provider: "codex", Models: 6, Tokens: 540_000_000, Cost: pricing.Money(63_000_000_000), PricedTokens: 540_000_000, TokenShare: .52, CostShare: .47},
		{Provider: "claude", Models: 6, Tokens: 504_000_000, Cost: pricing.Money(72_000_000_000), PricedTokens: 504_000_000, TokenShare: .48, CostShare: .53},
	}
	data.Pricing = []tuipages.ModelPricingRow{
		{Label: "live rates", Models: 10, Tokens: totalTokens - 90_000_000, Cost: totalCost, PricedTokens: totalTokens - 90_000_000},
		{Label: "unpriced", Models: 2, Tokens: 90_000_000, UnpricedTokens: 90_000_000},
	}
	for index, row := range data.Rows {
		data.Rates = append(data.Rates, tuipages.ModelRateRow{Model: row.Model, Cost: row.Cost, PricedTokens: row.PricedTokens})
		data.PerSession = append(data.PerSession, tuipages.ModelPerSessionRow{Model: row.Model, Tokens: row.Tokens, Sessions: row.Sessions, TokensPerSession: row.Tokens / int64(row.Sessions)})
		data.Recency = append(data.Recency, tuipages.ModelRecencyRow{Model: row.Model, Days: index})
		matrix := tuipages.ModelMatrixRow{Model: row.Model, Cost: row.Cost}
		for day := 0; day < 30; day++ {
			value := float64((index + 1) * (day + 1))
			switch index {
			case 0:
				if day == 10 {
					value *= 8
				}
			case 1:
				value = float64((index + 1) * (30 - day))
			case 2:
				if day >= 10 && day <= 14 {
					value = 0
				}
			}
			matrix.Values = append(matrix.Values, value)
		}
		data.Matrix.Rows = append(data.Matrix.Rows, matrix)
	}
	for day := 0; day < 30; day++ {
		data.Matrix.Dates = append(data.Matrix.Dates, time.Date(2026, time.July, 3+day, 0, 0, 0, 0, time.UTC).Format("2006-01-02"))
	}
	data.Unpriced = []tuipages.ModelUnpricedRow{{Model: "model-10", Tokens: 50_000_000}, {Model: "model-11", Tokens: 40_000_000}}
	return data
}

func quest147LedgerPeriodsFrameData() tuipages.Data {
	months := make([]tuipages.LedgerMonth, 0, 12)
	rows := make([]tuipages.Row, 0, 12)
	for month := 1; month <= 12; month++ {
		key := fmt.Sprintf("2026-%02d", month)
		codex := tuipages.ProviderTotals{
			Cost: pricing.Money(int64(month+2) * 7_000_000_000), Tokens: int64(month+2) * 1_400_000, PricedTokens: int64(month+2) * 1_400_000,
		}
		claude := tuipages.ProviderTotals{
			Cost: pricing.Money(int64(month%4+1) * 3_000_000_000), Tokens: int64(month%4+1) * 900_000, PricedTokens: int64(month%4+1) * 900_000,
		}
		value := tuipages.LedgerMonth{
			Key: key, Label: time.Date(2026, time.Month(month), 1, 0, 0, 0, 0, time.UTC).Format("Jan 2006"),
			Sessions: month + 4, Codex: codex, Claude: claude, ActiveDays: month + 2,
			PeakCost: codex.Cost, PeakDay: fmt.Sprintf("2026-%02d-%02d", month, min(28, month+8)),
		}
		value.AverageCost = value.Total().Cost / pricing.Money(value.ActiveDays)
		months = append(months, value)
		rows = append(rows, tuipages.Row{Key: key, Label: value.Label, Sessions: value.Sessions, Codex: codex, Claude: claude})
	}
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
	total := tuipages.Row{Key: "total", Label: "TOTAL"}
	for _, row := range rows {
		total = total.Add(row)
	}
	analytics := tuipages.LedgerAnalytics{
		Months: months,
		Models: []tuipages.LedgerModel{
			{Provider: "codex", Model: "gpt-5.2", Tokens: 48_000_000, Cost: pricing.Money(240_000_000_000), PricedTokens: 48_000_000, CostPerMillion: pricing.Rate(5_000), HasRate: true},
			{Provider: "claude", Model: "claude-sonnet", Tokens: 22_000_000, Cost: pricing.Money(110_000_000_000), PricedTokens: 22_000_000, CostPerMillion: pricing.Rate(5_000), HasRate: true},
			{Provider: "codex", Model: "gpt-mini", Tokens: 8_000_000, Cost: pricing.Money(16_000_000_000), PricedTokens: 8_000_000, CostPerMillion: pricing.Rate(2_000), HasRate: true},
		},
		Weekdays: []tuipages.LedgerProfile{
			{Label: "Mon", Value: 8}, {Label: "Tue", Value: 12}, {Label: "Wed", Value: 6}, {Label: "Thu", Value: 10},
			{Label: "Fri", Value: 7}, {Label: "Sat", Value: 3}, {Label: "Sun", Value: 2},
		},
		Projects: []tuipages.LedgerProject{
			{Label: "tokenomnom", Sessions: 20, Share: 0.50}, {Label: "billing-api", Sessions: 12, Share: 0.30}, {Label: "release-tools", Sessions: 8, Share: 0.20},
		},
		Provenance: tuipages.LedgerProvenance{
			PublishedModels: 2, PublishedCost: pricing.Money(350_000_000_000), PublishedTokens: 70_000_000,
			ProxyModels: 1, ProxyCost: pricing.Money(16_000_000_000), ProxyTokens: 8_000_000,
		},
		ActiveDays: 42, AverageCost: pricing.Money(9_000_000_000), PeakCost: pricing.Money(45_000_000_000), PeakDay: "2026-07-14",
	}
	for hour := 0; hour < 24; hour++ {
		value := 0
		if hour == 9 {
			value = 12
		} else if hour == 15 {
			value = 10
		} else if hour%5 == 0 {
			value = 2
		}
		analytics.Hours = append(analytics.Hours, tuipages.LedgerProfile{Label: fmt.Sprintf("%02d", hour), Value: value})
	}
	for _, project := range analytics.Projects {
		for month := 1; month <= 12; month++ {
			count := (month + len(project.Label)) % 4
			if count > 0 {
				analytics.ProjectMonths = append(analytics.ProjectMonths, tuipages.LedgerProjectMonth{Project: project.Label, Month: fmt.Sprintf("2026-%02d", month), Sessions: count})
			}
		}
	}
	for _, month := range months {
		analytics.ProviderMonths = append(analytics.ProviderMonths,
			tuipages.LedgerProviderMonth{Provider: "codex", Month: month.Key, Cost: month.Codex.Cost, Tokens: month.Codex.Tokens},
			tuipages.LedgerProviderMonth{Provider: "claude", Month: month.Key, Cost: month.Claude.Cost, Tokens: month.Claude.Tokens},
		)
	}
	return tuipages.Data{Available: true, Zoom: tuipages.ZoomMonth, Year: 2026, Rows: rows, Total: total, Analytics: analytics}
}

func quest147LedgerDayFrameData() tuipages.Data {
	day := tuipages.Row{
		Key: "2026-07-14", Label: "Jul 14",
	}
	previous := tuipages.Row{Key: "2026-07-13", Label: "Jul 13", Sessions: 8, Codex: tuipages.ProviderTotals{Cost: pricing.Money(60_000_000_000), Tokens: 6_000_000, PricedTokens: 6_000_000}}
	data := quest147LedgerPeriodsFrameData()
	data.Available, data.Zoom, data.Month = true, tuipages.ZoomDay, "2026-07"
	data.SessionDay, data.SessionIndexAvailable, data.Location = day.Key, true, time.UTC
	for index := 0; index < 20; index++ {
		provider := history.ProviderCodex
		project := "tokenomnom"
		if index%2 == 1 {
			provider, project = history.ProviderClaude, "billing-api"
		}
		stamp := fmt.Sprintf("2026-07-14T%02d:%02d:00Z", 9+index%10, (index*7)%60)
		data.Sessions = append(data.Sessions, tuipages.LedgerSession{
			CatalogSession: historystore.CatalogSession{
				SessionID: fmt.Sprintf("ses_ledger_%02d", index+1), Provider: provider, Project: project,
				ProjectSource: history.ProjectSourceGit, FirstTimestamp: &stamp, LastTimestamp: &stamp,
				Preview:            []string{"Investigate the production latency regression", "Prepare the migration rollout plan", "Review the query budget before release"}[index%3],
				LogicalPromptCount: index + 2, OccurrenceCount: index + 3,
			},
			Tokens: int64(900_000 + index*10_000), Cost: pricing.Money(int64(5_000_000_000 + index*250_000_000)), PricedTokens: int64(900_000 + index*10_000),
			ActivityTimestamp: stamp, AttributionStatus: "complete",
		})
	}
	for _, session := range data.Sessions {
		value := tuipages.ProviderTotals{Cost: session.Cost, Tokens: session.Tokens, PricedTokens: session.PricedTokens}
		if session.Provider == history.ProviderClaude {
			day.Claude = day.Claude.Add(value)
		} else {
			day.Codex = day.Codex.Add(value)
		}
	}
	data.DayModels = []tuipages.LedgerModel{
		{Provider: "codex", Model: "gpt-5.2", Tokens: 9_900_000, Cost: pricing.Money(72_500_000_000), PricedTokens: 9_900_000},
		{Provider: "claude", Model: "claude-sonnet", Tokens: 10_000_000, Cost: pricing.Money(75_000_000_000), PricedTokens: 10_000_000},
	}
	data.DayProjects = []tuipages.LedgerProject{
		{Label: "tokenomnom", Sessions: 10, Share: 0.5},
		{Label: "billing-api", Sessions: 10, Share: 0.5},
	}
	data.DayProjectCount = len(data.DayProjects)
	day.Sessions = len(data.Sessions)
	data.Rows, data.Total = []tuipages.Row{day, previous}, day.Add(previous)
	return data
}

func quest146DailyFrameData() tuipages.DailyPageData {
	rows := make([]tuipages.DailyPoint, 0, 30)
	for index := 0; index < 30; index++ {
		day := index + 1
		rows = append(rows, tuipages.DailyPoint{
			Date:   fmt.Sprintf("2026-07-%02d", day),
			Total:  tuipages.DailyValue{Cost: pricing.Money((index + 1) * 10_000_000_000), Tokens: int64((index + 1) * 1_000_000), PricedTokens: 1},
			Codex:  tuipages.DailyValue{Cost: pricing.Money((index + 1) * 6_000_000_000), Tokens: int64((index + 1) * 600_000), PricedTokens: 1},
			Claude: tuipages.DailyValue{Cost: pricing.Money((index + 1) * 4_000_000_000), Tokens: int64((index + 1) * 400_000), PricedTokens: 1},
		})
	}
	rows[len(rows)-1].Selected = true
	models := []tuipages.DailyModel{
		{Provider: "codex", Model: "gpt-test", Value: tuipages.DailyValue{Cost: pricing.Money(120_000_000_000), Tokens: 12_000_000, PricedTokens: 1}},
		{Provider: "codex", Model: "gpt-mini", Value: tuipages.DailyValue{Cost: pricing.Money(45_000_000_000), Tokens: 4_500_000, PricedTokens: 1}},
		{Provider: "claude", Model: "claude-sonnet", Value: tuipages.DailyValue{Cost: pricing.Money(35_000_000_000), Tokens: 3_500_000, PricedTokens: 1}},
	}
	providers := []string{"codex", "claude", "codex", "claude", "codex"}
	projects := []string{"project-a", "project-b", "project-c", "project-a", "project-b"}
	sessionModels := []string{"gpt-test", "claude-sonnet", "gpt-mini", "gpt-test", "claude-sonnet"}
	times := []string{"09:15", "10:40", "12:00", "14:45", "16:20"}
	prompts := []string{"Investigate latency regression", "Prepare rollout plan", "Review query budget", "Compare provider mix", "Summarize release notes"}
	sessions := make([]tuipages.DailySession, 0, 20)
	for index := 0; index < 20; index++ {
		sessions = append(sessions, tuipages.DailySession{
			Time: times[index%len(times)], Provider: providers[index%len(providers)], Project: projects[index%len(projects)],
			SessionID: fmt.Sprintf("ses_demo%02d", index+1), Model: sessionModels[index%len(sessionModels)],
			Tokens: int64(10_000_000 - index*150_000), Cost: pricing.Money(1_000_000_000 - int64(index)*20_000_000), PricedTokens: 1,
			Prompt: prompts[index%len(prompts)], PromptCount: index + 1, AttributionStatus: "complete",
		})
	}
	sessions[18].AttributionStatus = "incomplete"
	sessions[19].AttributionStatus = "incomplete"
	return tuipages.DailyPageData{
		Rows: rows, TrendRows: rows, SelectedDate: rows[len(rows)-1].Date,
		Detail: tuipages.DailyDetail{
			Date:  rows[len(rows)-1].Date,
			Value: tuipages.DailyValue{Cost: pricing.Money(200_000_000_000), Tokens: 20_000_000, PricedTokens: 1, Sessions: 20},
			Providers: []tuipages.DailyProvider{
				{Provider: "codex", Value: tuipages.DailyValue{Cost: pricing.Money(120_000_000_000), Tokens: 12_000_000, PricedTokens: 1, Sessions: 14}},
				{Provider: "claude", Value: tuipages.DailyValue{Cost: pricing.Money(80_000_000_000), Tokens: 8_000_000, PricedTokens: 1, Sessions: 6}},
			},
			Models: models,
		},
		Sessions: tuipages.DailySessionData{Rows: sessions, Total: 20, TotalKnown: true, Warning: "Cost attribution unavailable for 2 of 20 sessions; restore the source and rerun history index."},
		Average:  tuipages.DailyValue{Cost: pricing.Money(100_000_000_000), Tokens: 10_000_000, PricedTokens: 1}, AverageSessions: 10,
		Peak: tuipages.DailyValue{Cost: pricing.Money(300_000_000_000), Tokens: 30_000_000, PricedTokens: 1}, PeakDate: "2026-07-09",
		RangeStart: "2026-07-03", RangeEnd: "2026-08-01",
	}
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
	model.snapshot.Rail = RailData{
		Snapshot: RailSnapshot{Today: "$2,209.23", SevenDays: "$3,033.35", ThirtyDays: "$3,033.35", Peak: "$2,209.23", PeakDate: "Jul 14"},
		Mix:      RailMix{Codex: 0.72, Claude: 0.28},
		Projects: []RailProject{{Label: "alpha", Share: 0.5}, {Label: "beta", Share: 0.3}, {Label: "other", Share: 0.2}},
	}
	model.snapshot.StatusBar = StatusBar{LastSyncUnix: time.Now().Unix(), Sources: 2, Models: 2}
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
