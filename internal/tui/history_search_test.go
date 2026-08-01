package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestHistorySearchPageInputResultsDetailAndExport(t *testing.T) {
	var exported string
	page := NewHistorySearchPage(HistorySearchOptions{
		Export: func(request Request) (string, error) {
			exported = request.HistoryExportID
			return "/tmp/tokenomnom-history", nil
		},
	})
	request := Request{Width: 100, Height: 30}
	edit := page.HandleKey(request, keyMsg("/"))
	if !edit.Handled || !edit.Changed {
		t.Fatalf("edit result = %+v", edit)
	}
	request = edit.Request
	for _, key := range []string{"d", "e", " ", "n", "o", "t", "?"} {
		result := page.HandleKey(request, keyMsg(key))
		if !result.Handled || !result.Changed || result.Action != PageActionNone {
			t.Fatalf("input %q result = %+v", key, result)
		}
		request = result.Request
	}
	if request.HistoryQuery != "de not?" {
		t.Fatalf("query = %q", request.HistoryQuery)
	}
	slash := page.HandleKey(request, keyMsg("/"))
	if slash.Request.HistoryQuery != "de not?/" || !slash.Changed {
		t.Fatalf("slash was not added while editing: %+v", slash)
	}
	request = slash.Request
	search := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter})
	if !search.Handled || !search.Changed || search.Action != PageActionLoad {
		t.Fatalf("search result = %+v", search)
	}
	request = search.Request
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{
		{SessionID: "ses_1", Provider: "codex", Date: "2026-07-01", Project: "tokenomnom", Snippet: "said " + marked("do") + " " + marked("not") + " implement"},
		{SessionID: "ses_2", Provider: "claude", Date: "2026-07-02", Project: "other", Snippet: "another " + marked("do") + " " + marked("not") + " implement"},
	}}}, nil)
	browsing := page.HandleKey(request, keyMsg("x"))
	if browsing.Changed || browsing.Request.HistoryQuery != request.HistoryQuery {
		t.Fatalf("text key changed submitted search: %+v", browsing)
	}
	view := page.View(pageContext(request))
	for _, fragment := range []string{"FIND IN HISTORY", "de not", "2026-07-01", "codex", "tokenomnom", "do not", "enter open", "e export"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("search view missing %q:\n%s", fragment, view)
		}
	}
	if strings.Contains(view, "[do]") {
		t.Fatalf("highlight delimiters leaked into search view:\n%s", view)
	}
	t.Log("\n-- search --\n" + view)

	move := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyDown})
	if !move.Handled || !move.Changed || move.Request.HistorySelect != 1 {
		t.Fatalf("selection move = %+v", move)
	}
	request = move.Request
	open := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter})
	if !open.Handled || !open.Changed || open.Action != PageActionLoad || open.Request.HistorySessionID != "ses_2" {
		t.Fatalf("open result = %+v", open)
	}
	request = open.Request
	page.Apply(request, HistorySearchData{Session: &SessionDetail{
		SessionID: "ses_2", Provider: "claude", Project: "other", FirstDate: "2026-07-02", LastDate: "2026-07-03",
		Preview: "another prompt", Prompts: []SessionPrompt{{Date: "2026-07-02", Snippet: "another prompt"}},
	}}, nil)
	view = page.View(pageContext(request))
	for _, fragment := range []string{"SESSION DETAIL", "ses_2", "claude", "other", "PROMPTS", "esc back"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("detail view missing %q:\n%s", fragment, view)
		}
	}

	export := page.HandleKey(request, keyMsg("e"))
	if !export.Handled || !export.Changed || export.Action != PageActionExport || export.Request.HistoryExportID != "ses_2" {
		t.Fatalf("export result = %+v", export)
	}
	request = export.Request
	path, err := page.Export(request)
	if err != nil || path != "/tmp/tokenomnom-history" || exported != "ses_2" {
		t.Fatalf("export callback path=%q id=%q err=%v", path, exported, err)
	}
	page.ApplyExport(request, path, nil)
	if !strings.Contains(page.View(pageContext(request)), "Exported to /tmp/tokenomnom-history") {
		t.Fatal("export receipt missing")
	}
	t.Log("\n-- detail --\n" + page.View(pageContext(request)))

	back := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEsc})
	if !back.Handled || !back.Changed || back.Action != PageActionLoad || back.Request.HistorySessionID != "" {
		t.Fatalf("back result = %+v", back)
	}
	request = back.Request
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{
		{SessionID: "ses_1", Provider: "codex", Date: "2026-07-01", Project: "tokenomnom", Snippet: "said " + marked("do") + " " + marked("not") + " implement"},
		{SessionID: "ses_2", Provider: "claude", Date: "2026-07-02", Project: "other", Snippet: "another " + marked("do") + " " + marked("not") + " implement"},
	}}}, nil)
	previous := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyUp})
	if previous.Request.HistorySelect != 0 {
		t.Fatalf("selection after returning from detail = %+v", previous)
	}
	otherExport := page.HandleKey(previous.Request, keyMsg("e"))
	if otherExport.Request.HistoryExportID != "ses_1" {
		t.Fatalf("export reused previous session: %+v", otherExport)
	}
	if _, err := page.Export(otherExport.Request); err != nil || exported != "ses_1" {
		t.Fatalf("second export id=%q err=%v", exported, err)
	}
}

func TestHistorySearchPageShowsIndexHint(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{NotIndexed: true}, nil)
	view := page.View(pageContext(request))
	if !strings.Contains(view, "run `tokenomnom history index`") {
		t.Fatalf("missing index hint:\n%s", view)
	}
	t.Log("\n-- missing index --\n" + view)
}

func TestHistorySearchPageKeepsMissingIndexHintWhileEditing(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{NotIndexed: true}, nil)
	request = page.HandleKey(request, keyMsg("/")).Request
	request = page.HandleKey(request, keyMsg("x")).Request
	view := page.View(pageContext(request))
	if !strings.Contains(view, "run `tokenomnom history index`") || strings.Contains(view, "No matching prompts") {
		t.Fatalf("missing-index state was lost while editing:\n%s", view)
	}
}

func TestHistorySearchPageReportsLoaderErrors(t *testing.T) {
	var reported error
	page := NewHistorySearchPage(HistorySearchOptions{
		Load: func(Request) (HistorySearchData, error) { return HistorySearchData{}, errors.New("database is locked") },
		ReportError: func(err error) {
			reported = err
		},
	})
	if _, err := page.Load(Request{}); err == nil || reported == nil || reported.Error() != "database is locked" {
		t.Fatalf("loader error report = err %v reported %v", err, reported)
	}
}

func TestHistorySearchPageTruncatesProvenanceToPane(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 60, Height: 18}
	request = page.HandleKey(request, keyMsg("/")).Request
	request = page.HandleKey(request, keyMsg("x")).Request
	request = page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter}).Request
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{
		SessionID: "ses_long", Date: "2026-07-01", Provider: "codex", Project: "/a/very/long/project/path/that/should/not/overflow/the/pane",
		Snippet: "said " + marked("x") + " implement",
	}}}}, nil)
	for index, line := range strings.Split(page.View(pageContext(request)), "\n") {
		if width := lipgloss.Width(line); width > ContentWidth(request.Width) {
			t.Fatalf("line %d width=%d exceeds pane=%d:\n%s", index+1, width, ContentWidth(request.Width), page.View(pageContext(request)))
		}
	}
}

func TestHistorySearchSelectedHighlightKeepsBoldSuffix(t *testing.T) {
	terminal, dark := true, true
	render := theme.Resolve(theme.ResolveOptions{
		Output: &bytes.Buffer{}, ForceTerminal: &terminal, Width: 80, ForceColor: true,
		Dark: &dark, LookupEnv: func(string) (string, bool) { return "", false },
	})
	plain := render.Palette.Emphasis().Bold(true)
	match := plain.Underline(true)
	value := highlightSnippet("before "+marked("match")+" after", "", "", plain, match)
	if !strings.Contains(value, plain.Render(" after")) {
		t.Fatalf("selected suffix lost its bold style: %q", value)
	}
}

func TestHistorySearchMarkedSnippetTruncationPreservesMarkers(t *testing.T) {
	start := string(history.SearchSnippetMatchStart) + "nonce"
	end := "nonce" + string(history.SearchSnippetMatchEnd)
	value := "before " + start + "matched text" + end + " after"
	truncated := truncateMarkedSnippet(value, 16, start, end)
	if lipgloss.Width(strings.NewReplacer(start, "", end, "").Replace(truncated)) > 16 {
		t.Fatalf("visible snippet width exceeds pane: %q", truncated)
	}
	if strings.Contains(truncated, start) != strings.Contains(truncated, end) {
		t.Fatalf("truncated snippet split marker pair: %q", truncated)
	}
	rendered := highlightSnippet(truncated, start, end, lipgloss.NewStyle(), lipgloss.NewStyle())
	if strings.Contains(rendered, "nonce") {
		t.Fatalf("highlighted snippet exposed marker payload: %q", rendered)
	}
}

func TestHistorySearchEvidenceFrames(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	model := loadedTestModel()
	model.router = newRouter(page)
	if !model.router.Select(HistorySearchPageID) {
		t.Fatal("history search page was not registered")
	}
	page.query = "do not implement"
	page.searched = true
	page.warnings = []string{"History index is stale; run `tokenomnom history index` to refresh it."}
	page.hits = []SearchHit{
		{
			SessionID: "ses_7f2a", Provider: "codex", Date: "2026-08-01",
			Project: "/Users/janiorvalle/Documents/github/tokenomnom",
			Snippet: "said " + marked("do not implement") + " until the provenance view is ready",
		},
		{
			SessionID: "ses_4b19", Provider: "claude", Date: "2026-07-31",
			Project: "/Users/janiorvalle/Documents/github/another-long-project-name",
			Snippet: "another " + marked("do not implement") + " result",
		},
	}
	page.hasMore = true
	model.request.HistoryQuery = page.query

	model.request.Width, model.request.Height = 100, 30
	model.render = evidenceRender(100)
	full := evidenceText(model.View())
	if !strings.Contains(full, "<ESC>") || !strings.Contains(full, "do not implement") {
		t.Fatalf("styled full cockpit evidence is missing ANSI output or highlighted text:\n%s", full)
	}

	model.request.Width, model.request.Height = 60, 18
	model.render = evidenceRender(60)
	narrow := evidenceText(model.View())
	if !strings.Contains(narrow, "FIND IN HISTORY") || !strings.Contains(narrow, "History index is stale") {
		t.Fatalf("narrow cockpit evidence is incomplete:\n%s", narrow)
	}

	stateRequest := Request{Width: 60, Height: 18, HistoryQuery: "do not implement", PageLoadToken: "1"}
	loadingPage := NewHistorySearchPage(HistorySearchOptions{})
	loadingPage.query, loadingPage.searched, loadingPage.loading = stateRequest.HistoryQuery, true, true
	loading := evidenceText(loadingPage.View(PageContext{
		Render: model.render, Request: stateRequest, Width: ContentWidth(stateRequest.Width), Height: ContentHeight(stateRequest.Height),
	}))
	errorPage := NewHistorySearchPage(HistorySearchOptions{})
	errorPage.query, errorPage.searched = stateRequest.HistoryQuery, true
	errorPage.BeginLoad(stateRequest)
	errorPage.Apply(stateRequest, nil, errors.New("database is locked"))
	error := evidenceText(errorPage.View(PageContext{
		Render: model.render, Request: stateRequest, Width: ContentWidth(stateRequest.Width), Height: ContentHeight(stateRequest.Height),
	}))
	if !strings.Contains(loading, "Searching local history") || !strings.Contains(error, "History search is unavailable") {
		t.Fatalf("loading/error evidence is incomplete:\nloading=%s\nerror=%s", loading, error)
	}
	t.Logf("Command: go test -v ./internal/tui -run TestHistorySearch -count=1\n\n-- full cockpit 100x30 --\n%s\n\n-- narrow cockpit 60x18 --\n%s\n\n-- loading state --\n%s\n\n-- error state --\n%s", full, narrow, loading, error)
}

func TestHistorySearchPageCancelsStaleExportWhenOpeningDetail(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_1", Snippet: "prompt"}}}}, nil)
	export := page.HandleKey(request, keyMsg("e"))
	if !export.Changed || export.Request.HistoryExportID != "ses_1" {
		t.Fatalf("export start = %+v", export)
	}
	duplicate := page.HandleKey(export.Request, keyMsg("e"))
	if duplicate.Changed || duplicate.Action != PageActionNone {
		t.Fatalf("duplicate export was scheduled: %+v", duplicate)
	}
	open := page.HandleKey(export.Request, tea.KeyMsg{Type: tea.KeyEnter})
	if !open.Changed || open.Request.HistorySessionID != "ses_1" {
		t.Fatalf("open after export = %+v", open)
	}
	slash := page.HandleKey(open.Request, keyMsg("/"))
	if slash.Changed || page.Editing() {
		t.Fatalf("slash entered search mode from detail: %+v", slash)
	}
	page.ApplyExport(export.Request, "/tmp/stale", nil)
	if strings.Contains(page.View(pageContext(open.Request)), "Exporting session") {
		t.Fatal("stale export left the page loading")
	}
}

func TestHistorySearchPagePreservesInFlightExportForRepeatedSession(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{
		{SessionID: "ses_1", Snippet: "first"},
		{SessionID: "ses_1", Snippet: "second"},
	}}}, nil)
	first := page.HandleKey(request, keyMsg("e"))
	moved := page.HandleKey(first.Request, tea.KeyMsg{Type: tea.KeyDown})
	if moved.Request.HistoryExportID != first.Request.HistoryExportID || moved.Request.HistoryExportToken != first.Request.HistoryExportToken {
		t.Fatalf("selection lost the in-flight same-session export: first=%+v moved=%+v", first.Request, moved.Request)
	}
	second := page.HandleKey(moved.Request, keyMsg("e"))
	if second.Changed || second.Action != PageActionNone || second.Request.HistoryExportToken != first.Request.HistoryExportToken {
		t.Fatalf("duplicate export was scheduled for the same session: %+v", second)
	}
	page.ApplyExport(first.Request, "/tmp/current", nil)
	if !strings.Contains(page.View(pageContext(moved.Request)), "Exported to /tmp/current") {
		t.Fatal("in-flight export receipt missing after same-session selection move")
	}
}

func TestHistorySearchPageRejectsOlderLoadForSameRequest(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	request = page.HandleKey(request, keyMsg("/")).Request
	for _, current := range "do" {
		request = page.HandleKey(request, keyMsg(string(current))).Request
	}
	request = page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter}).Request
	older := request
	older.PageLoadToken = "1"
	newer := request
	newer.PageLoadToken = "2"
	page.BeginLoad(newer)
	page.Apply(older, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "old", Snippet: "old result"}}}}, nil)
	if strings.Contains(page.View(pageContext(newer)), "old result") {
		t.Fatal("older same-query load replaced the current state")
	}
	page.Apply(newer, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "new", Snippet: "new result"}}}}, nil)
	if !strings.Contains(page.View(pageContext(newer)), "new result") {
		t.Fatal("newer same-query load was not applied")
	}
}

func TestHistorySearchEscapeStartsReplacementLoadWhileDetailIsLoading(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_1", Snippet: "prompt"}}}}, nil)
	open := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter})
	if !open.Changed || open.Action != PageActionLoad || open.Request.HistorySessionID != "ses_1" {
		t.Fatalf("open detail result = %+v", open)
	}
	page.BeginLoad(Request{Width: request.Width, Height: request.Height, HistorySessionID: "ses_1", PageLoadToken: "detail"})
	back := page.HandleKey(Request{
		Width: request.Width, Height: request.Height, HistorySessionID: "ses_1", PageLoadToken: "detail",
	}, tea.KeyMsg{Type: tea.KeyEsc})
	if !back.Handled || !back.Changed || back.Action != PageActionLoad || back.Request.HistorySessionID != "" {
		t.Fatalf("escape during detail load = %+v", back)
	}
}

func TestHistorySearchBeginLoadHidesStaleResults(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30, HistoryQuery: "prompt"}
	page.query = request.HistoryQuery
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_old", Snippet: "stale result"}}}}, nil)
	page.BeginLoad(Request{HistoryQuery: request.HistoryQuery, PageLoadToken: "2"})
	view := page.View(pageContext(request))
	if !strings.Contains(view, "Searching local history") || strings.Contains(view, "stale result") {
		t.Fatalf("stale results remained actionable during load:\n%s", view)
	}
	key := page.HandleKey(request, keyMsg("e"))
	if !key.Handled || key.Changed || key.Action != PageActionNone {
		t.Fatalf("loading page accepted export action: %+v", key)
	}
}

func TestHistorySearchDetailEndStopsAtBottom(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 60, Height: 18}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_long", Snippet: "prompt"}}}}, nil)
	request = page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter}).Request
	detail := &SessionDetail{SessionID: "ses_long", Provider: "codex", Project: "tokenomnom", Preview: "first prompt"}
	for index := 0; index < 30; index++ {
		detail.Prompts = append(detail.Prompts, SessionPrompt{Date: "2026-08-01", Snippet: "prompt with enough text to make the detail view scroll"})
	}
	page.Apply(request, HistorySearchData{Session: detail}, nil)
	end := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnd})
	if !end.Handled || !end.Changed || end.Request.SessionDetailOffset <= 0 {
		t.Fatalf("end offset = %+v", end)
	}
	down := page.HandleKey(end.Request, tea.KeyMsg{Type: tea.KeyDown})
	if !down.Handled || down.Changed || down.Request.SessionDetailOffset != end.Request.SessionDetailOffset {
		t.Fatalf("down past detail end = %+v", down)
	}
	up := page.HandleKey(end.Request, tea.KeyMsg{Type: tea.KeyUp})
	if !up.Handled || !up.Changed || up.Request.SessionDetailOffset != end.Request.SessionDetailOffset-1 {
		t.Fatalf("up from detail end = %+v", up)
	}
}

func TestHistorySearchSelectionNormalizesAfterShorterResults(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30, HistorySelect: 99}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{
		{SessionID: "ses_1", Snippet: "first"}, {SessionID: "ses_2", Snippet: "second"},
	}}}, nil)
	up := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyUp})
	if !up.Handled || !up.Changed || up.Request.HistorySelect != 0 {
		t.Fatalf("stale selection was not normalized before moving: %+v", up)
	}
}

func TestHistorySearchDetailPreservesCatalogProvenance(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	detail := SessionDetail{
		CatalogSession: historystore.CatalogSession{
			SessionID: "ses_provenance", Provider: history.ProviderCodex, Project: "tokenomnom",
			ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
			Availability:             historystore.Availability{ProviderLive: 3, ProviderArchive: 2, Vault: 1},
			PreferredRetrievalSource: "provider-live",
			Relationships:            []historystore.SessionRelationship{{ChildSessionID: "ses_provenance"}},
		},
		SessionID: "ses_provenance", Provider: "codex", Project: "tokenomnom", Preview: "prompt",
	}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: detail.SessionID, Snippet: "prompt"}}}}, nil)
	request = page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter}).Request
	page.Apply(request, HistorySearchData{Session: &detail}, nil)
	view := page.View(pageContext(request))
	for _, fragment := range []string{"root (exact confidence)", "provider-live", "3 live · 2 archive", "1 available", "1 recorded"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("provenance detail missing %q:\n%s", fragment, view)
		}
	}
}

func TestHistorySearchExportFailureKeepsLoadedContent(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30, HistoryQuery: "prompt"}
	page.query = request.HistoryQuery
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_1", Snippet: "keep this result"}}}}, nil)
	export := page.HandleKey(request, keyMsg("e"))
	page.ApplyExport(export.Request, "", errors.New("permission denied"))
	view := page.View(pageContext(export.Request))
	if !strings.Contains(view, "keep this result") || !strings.Contains(view, "Export failed") {
		t.Fatalf("export failure replaced loaded content:\n%s", view)
	}
}

func TestHistorySearchPageRejectsSpecialKeysAndSanitizesDisplayValues(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := Request{Width: 100, Height: 30}
	edit := page.HandleKey(request, keyMsg("/"))
	request = edit.Request
	batched := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	if batched.Action != PageActionNone || batched.Request.HistoryQuery != "enter" || !page.Editing() {
		t.Fatalf("batched text became a command: %+v", batched)
	}
	spaced := page.HandleKey(batched.Request, tea.KeyMsg{Type: tea.KeySpace})
	if spaced.Request.HistoryQuery != "enter " || !spaced.Changed {
		t.Fatalf("space key was discarded: %+v", spaced)
	}
	request = spaced.Request
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyLeft}, {Type: tea.KeyDelete}, {Type: tea.KeyPgDown},
	} {
		result := page.HandleKey(request, key)
		if result.Changed || result.Request.HistoryQuery != request.HistoryQuery {
			t.Fatalf("special key %q changed query: %+v", key.String(), result)
		}
	}
	edit = page.HandleKey(request, keyMsg("/"))
	request = edit.Request
	for _, current := range "prompt" {
		request = page.HandleKey(request, keyMsg(string(current))).Request
	}
	submit := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter})
	request = submit.Request
	data := HistorySearchData{Search: SearchResult{Warnings: []string{"stale\x1b]52;c;clipboard\a"}, Hits: []SearchHit{{
		SessionID: "session\x1b]52;c;clipboard\a", Provider: "codex\x1b]52;c;clipboard\a", Date: "2026-07-01\x1b]52;c;clipboard\a",
		Project: "project\x1b]52;c;clipboard\a", Snippet: "items[0] " + marked("prompt") + "\x1b]52;c;clipboard\a",
	}}}}
	page.Apply(request, data, nil)
	view := page.View(pageContext(request))
	if strings.ContainsAny(view, "\x1b\a") {
		t.Fatalf("search view contains terminal controls: %q", view)
	}
	if !strings.Contains(view, "items[0]") {
		t.Fatalf("literal brackets were lost: %q", view)
	}
	export := page.HandleKey(request, keyMsg("e"))
	page.ApplyExport(export.Request, "out\x1b]52;c;clipboard\a", nil)
	view = page.View(pageContext(export.Request))
	if strings.ContainsAny(view, "\x1b\a") {
		t.Fatalf("export receipt contains terminal controls: %q", view)
	}
}

func pageContext(request Request) PageContext {
	return PageContext{Render: theme.Context{Mode: theme.Plain, Width: request.Width, Palette: theme.NewPalette(nil)}, Request: request}
}

func marked(value string) string {
	return string(history.SearchSnippetMatchStart) + value + string(history.SearchSnippetMatchEnd)
}

func evidenceRender(width int) theme.Context {
	terminal, dark := true, true
	return theme.Resolve(theme.ResolveOptions{
		Output: &bytes.Buffer{}, ForceTerminal: &terminal, Width: width, ForceColor: true,
		Dark: &dark, LookupEnv: func(string) (string, bool) { return "", false },
	})
}

func evidenceText(value string) string {
	return strings.ReplaceAll(value, "\x1b", "<ESC>")
}
