package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	tuirender "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

func TestQuest150Frames(t *testing.T) {
	for _, size := range []struct{ width, height int }{{192, 66}, {120, 40}, {80, 24}} {
		model := realisticEvidenceModel()
		model.request.Width, model.request.Height = size.width, size.height
		model.render.Width = size.width
		model.snapshot.Sessions = quest150Sessions()
		model.request.SessionOffset = 7
		model.router.Select(SessionsPageID)
		sessions := model.View()
		assertQuest150Frame(t, sessions, size.width, size.height, "sessions")

		searchPage := NewHistorySearchPage(HistorySearchOptions{})
		searchPage.query, searchPage.searched = "do not implement", true
		searchPage.hits = quest150SearchHits()
		searchPage.preview = &tuirender.SearchPreview{
			PromptID: "prm_04",
			Detail: &tuirender.SessionDetail{
				SessionID: "ses_preview", Provider: "codex", Project: "tokenomnom",
				FirstDate: "2026-07-20", LastDate: "2026-07-21", Preview: "first prompt for the selected session",
				Prompts: quest150Prompts(),
			},
		}
		model.router = newRouter(searchPage)
		model.router.Select(HistorySearchPageID)
		model.request.HistoryQuery = searchPage.query
		model.request.HistorySelect = 4
		search := model.View()
		assertQuest150Frame(t, search, size.width, size.height, "search")

		t.Logf("FRAME: sessions %dx%d\nSource: internal/tui/quest150_test.go::TestQuest150Frames\nCommand: go test ./internal/tui -run TestQuest150Frames -count=1 -v\n\n%s\n\nFRAME: search %dx%d\nSource: internal/tui/quest150_test.go::TestQuest150Frames\nCommand: go test ./internal/tui -run TestQuest150Frames -count=1 -v\n\n%s", size.width, size.height, sessions, size.width, size.height, search)
	}
}

func TestQuest150WideViewsDoNotLeaveLongBlankRuns(t *testing.T) {
	model := realisticEvidenceModel()
	model.request.Width, model.request.Height = 192, 66
	model.render.Width = 192
	model.snapshot.Sessions = quest150Sessions()
	model.router.Select(SessionsPageID)
	assertNoLongBlankRun(t, model.View(), "sessions")

	page := NewHistorySearchPage(HistorySearchOptions{})
	page.query, page.searched, page.hits = "prompt", true, quest150SearchHits()
	page.preview = &tuirender.SearchPreview{PromptID: "prm_04", Detail: &tuirender.SessionDetail{
		SessionID: "ses_preview", Provider: "codex", Project: "tokenomnom", Preview: "first prompt", Prompts: quest150Prompts(),
	}}
	model.router = newRouter(page)
	model.router.Select(HistorySearchPageID)
	model.request.HistoryQuery = page.query
	assertNoLongBlankRun(t, model.View(), "search")
}

func TestQuest150SessionRowsAreHeightDerived(t *testing.T) {
	data := quest150Sessions()
	render := evidenceRender(120)
	short := strings.Split(tuirender.RenderSessions(render, data, tuirender.SessionViewState{}, 120, 20), "\n")
	tall := strings.Split(tuirender.RenderSessions(render, data, tuirender.SessionViewState{}, 120, 35), "\n")
	if len(tall) <= len(short) {
		t.Fatalf("height-derived session view did not grow: short=%d tall=%d", len(short), len(tall))
	}
}

func TestQuest150SessionDetailKeepsPromptAndCostColumns(t *testing.T) {
	data := quest150BaseSessionData()
	prompts := quest150Prompts()
	for index := len(prompts); index < 80; index++ {
		prompts = append(prompts, tuirender.SessionPrompt{
			PromptID: fmt.Sprintf("prm_%02d", index), Date: "2026-07-21",
			Snippet: fmt.Sprintf("long prompt row %d keeps the detail viewport scrollable", index),
		})
	}
	data.PromptPages = map[string]tuirender.SessionPromptPage{
		"ses_00": {Prompts: prompts, HasMore: true},
	}
	data.Costs = map[string]tuirender.SessionCost{
		"ses_00": {
			Status: "complete", TotalTokens: 123456, PricedTokens: 120000, UnpricedTokens: 3456, CostUSD: 1.23,
			Models: []tuirender.SessionModel{{Date: "2026-07-21", Provider: "codex", Model: "gpt-5.2", TotalTokens: 123456, CostUSD: 1.23}},
		},
	}
	render := evidenceRender(168)
	state := tuirender.SessionViewState{
		DetailID: "ses_00", Costs: data.Costs, PromptPages: data.PromptPages,
		ViewportWidth: 192, ViewportHeight: 66,
	}
	view := tuirender.RenderSessions(render, data, state, 168, 59)
	assertQuest150Frame(t, view, 168, 59, "session detail")
	for _, fragment := range []string{"FIRST PROMPT", "PROMPTS", "OVERVIEW", "PROVENANCE", "COST & TOKENS", "MODELS", "gpt-5.2"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("wide session detail missing %q:\n%s", fragment, view)
		}
	}
	offset := tuirender.SessionDetailMaxOffsetForViewportWithPrompts(render, data.Sessions[0], data.Costs["ses_00"], prompts, true, 168, 59, nil, 192, 66)
	if offset <= 0 {
		t.Fatalf("prompt-aware wide session detail did not expose a scrollable offset: %d", offset)
	}
	standard := tuirender.RenderSessionDetailForViewportWithPrompts(render, data.Sessions[0], data.Costs["ses_00"], prompts[:2], false, 97, 33, nil, 0, 120, 40)
	if !strings.Contains(standard, "COST & TOKENS") || !strings.Contains(standard, "$1.23") {
		t.Fatalf("standard session detail omitted attribution:\n%s", standard)
	}
}

func quest150Sessions() tuirender.SessionPageData {
	data := quest150BaseSessionData()
	for index := len(data.Sessions); index < 80; index++ {
		provider := history.ProviderCodex
		if index%2 == 1 {
			provider = history.ProviderClaude
		}
		data.Sessions = append(data.Sessions, historystore.CatalogSession{
			SessionID: fmt.Sprintf("ses_%02d", index), Provider: provider,
			Project: fmt.Sprintf("project-%d", index%6), ProjectSource: history.ProjectSourceGit,
			Preview:            fmt.Sprintf("prompt %02d: keep the selected work visible in the session desk", index),
			LogicalPromptCount: index%12 + 1, OccurrenceCount: index%15 + 2,
			ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
		})
	}
	return data
}

func quest150BaseSessionData() tuirender.SessionPageData {
	first, last := "2026-07-21T09:30:00Z", "2026-07-21T10:15:00Z"
	return tuirender.SessionPageData{
		IndexAvailable: true,
		Projects:       []tuirender.ProjectOption{{Key: "project-0", Label: "project-0"}},
		Sessions: []historystore.CatalogSession{{
			SessionID: "ses_00", Provider: history.ProviderCodex, Project: "project-0", ProjectSource: history.ProjectSourceGit,
			FirstTimestamp: &first, LastTimestamp: &last, Preview: "first prompt for the session desk", LogicalPromptCount: 5, OccurrenceCount: 7,
			ThreadKind: history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
		}},
	}
}

func quest150SearchHits() []SearchHit {
	hits := make([]SearchHit, 0, 8)
	for index := 0; index < 8; index++ {
		hits = append(hits, SearchHit{
			PromptID: fmt.Sprintf("prm_%02d", index), SessionID: "ses_preview", Provider: "codex",
			Date: "2026-07-21", Project: "tokenomnom", Snippet: "said do not implement prompt context " + fmt.Sprint(index),
		})
	}
	return hits
}

func quest150Prompts() []tuirender.SessionPrompt {
	prompts := make([]tuirender.SessionPrompt, 0, 8)
	for index := 0; index < 8; index++ {
		prompts = append(prompts, tuirender.SessionPrompt{
			PromptID: fmt.Sprintf("prm_%02d", index), Date: "2026-07-21", Snippet: fmt.Sprintf("surrounding prompt %d for the selected session", index),
		})
	}
	return prompts
}

func assertQuest150Frame(t *testing.T, view string, width, height int, name string) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("%s %dx%d rendered %d rows", name, width, height, len(lines))
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("%s %dx%d row %d width=%d", name, width, height, index+1, got)
		}
	}
}

func assertNoLongBlankRun(t *testing.T, view, name string) {
	t.Helper()
	run := 0
	for index, line := range strings.Split(view, "\n") {
		if strings.TrimSpace(stripANSIForQuest150(line)) == "" {
			run++
			if run > 3 {
				t.Fatalf("%s has a blank run at row %d", name, index+1)
			}
			continue
		}
		run = 0
	}
}

func stripANSIForQuest150(value string) string {
	return ansi.Strip(value)
}
