package pages

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	"github.com/janiorvalle/tokenomnom/internal/tui"
)

func TestHistorySearchPageInputResultsDetailAndExport(t *testing.T) {
	var exported string
	page := NewHistorySearchPage(HistorySearchOptions{
		Export: func(request tui.Request) (string, error) {
			exported = request.HistoryExportID
			return "/tmp/tokenomnom-history", nil
		},
	})
	request := tui.Request{Width: 100, Height: 30}
	edit := page.HandleKey(request, keyMsg("/"))
	if !edit.Handled || !edit.Changed {
		t.Fatalf("edit result = %+v", edit)
	}
	request = edit.Request
	for _, key := range []string{"d", "e", " ", "n", "o", "t", "?"} {
		result := page.HandleKey(request, keyMsg(key))
		if !result.Handled || !result.Changed || result.Action != tui.PageActionNone {
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
	if !search.Handled || !search.Changed || search.Action != tui.PageActionLoad {
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
	if !open.Handled || !open.Changed || open.Action != tui.PageActionLoad || open.Request.HistorySessionID != "ses_2" {
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
	if !export.Handled || !export.Changed || export.Action != tui.PageActionExport || export.Request.HistoryExportID != "ses_2" {
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
	if !back.Handled || !back.Changed || back.Action != tui.PageActionLoad || back.Request.HistorySessionID != "" {
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
	request := tui.Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{NotIndexed: true}, nil)
	view := page.View(pageContext(request))
	if !strings.Contains(view, "run `tokenomnom history index`") {
		t.Fatalf("missing index hint:\n%s", view)
	}
	t.Log("\n-- missing index --\n" + view)
}

func TestHistorySearchPageCancelsStaleExportWhenOpeningDetail(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := tui.Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{{SessionID: "ses_1", Snippet: "prompt"}}}}, nil)
	export := page.HandleKey(request, keyMsg("e"))
	if !export.Changed || export.Request.HistoryExportID != "ses_1" {
		t.Fatalf("export start = %+v", export)
	}
	duplicate := page.HandleKey(export.Request, keyMsg("e"))
	if duplicate.Changed || duplicate.Action != tui.PageActionNone {
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

func TestHistorySearchPageRejectsStaleCompletionForRepeatedSessionExport(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := tui.Request{Width: 100, Height: 30}
	page.Apply(request, HistorySearchData{Search: SearchResult{Hits: []SearchHit{
		{SessionID: "ses_1", Snippet: "first"},
		{SessionID: "ses_1", Snippet: "second"},
	}}}, nil)
	first := page.HandleKey(request, keyMsg("e"))
	moved := page.HandleKey(first.Request, tea.KeyMsg{Type: tea.KeyDown})
	second := page.HandleKey(moved.Request, keyMsg("e"))
	if first.Request.HistoryExportToken == second.Request.HistoryExportToken {
		t.Fatalf("export attempts reused token: first=%+v second=%+v", first.Request, second.Request)
	}
	page.ApplyExport(first.Request, "/tmp/stale", nil)
	if !strings.Contains(page.View(pageContext(second.Request)), "Exporting session") {
		t.Fatal("stale completion replaced the active export")
	}
	page.ApplyExport(second.Request, "/tmp/current", nil)
	if !strings.Contains(page.View(pageContext(second.Request)), "Exported to /tmp/current") {
		t.Fatal("current export receipt missing")
	}
}

func TestHistorySearchPageRejectsOlderLoadForSameRequest(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := tui.Request{Width: 100, Height: 30}
	request = page.HandleKey(request, keyMsg("/")).Request
	for _, current := range "do" {
		request = page.HandleKey(request, keyMsg(string(current))).Request
	}
	request = page.HandleKey(request, tea.KeyMsg{Type: tea.KeyEnter}).Request
	older := request
	older.HistoryLoadToken = "1"
	newer := request
	newer.HistoryLoadToken = "2"
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

func TestHistorySearchPageRejectsSpecialKeysAndSanitizesDisplayValues(t *testing.T) {
	page := NewHistorySearchPage(HistorySearchOptions{})
	request := tui.Request{Width: 100, Height: 30}
	edit := page.HandleKey(request, keyMsg("/"))
	request = edit.Request
	batched := page.HandleKey(request, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("enter")})
	if batched.Action != tui.PageActionNone || batched.Request.HistoryQuery != "enter" || !page.Editing() {
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

func pageContext(request tui.Request) tui.PageContext {
	return tui.PageContext{Render: theme.Context{Mode: theme.Plain, Width: request.Width, Palette: theme.NewPalette(nil)}, Request: request}
}

func keyMsg(value string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)}
}

func marked(value string) string {
	return string(history.SearchSnippetMatchStart) + value + string(history.SearchSnippetMatchEnd)
}
