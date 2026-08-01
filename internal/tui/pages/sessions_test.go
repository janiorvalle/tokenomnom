package pages

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestRenderSessionsEmptyIndexProvidesActionableHint(t *testing.T) {
	view := RenderSessions(testRender(), SessionPageData{}, SessionViewState{Provider: "all", DateRange: "30d"}, 70, 20)
	if !strings.Contains(view, "SESSIONS") || !strings.Contains(view, "No history index is available.") || !strings.Contains(view, "tokenomnom history index") {
		t.Fatalf("empty index view =\n%s", view)
	}
}

func TestRenderSessionsListBoundsRowsAndProjectOptions(t *testing.T) {
	data := SessionPageData{
		IndexAvailable: true,
		Sessions: []historystore.CatalogSession{
			{SessionID: "ses_a", Provider: history.ProviderCodex, Project: "zeta", Preview: "build the first thing", LogicalPromptCount: 2},
			{SessionID: "ses_b", Provider: history.ProviderClaude, Project: "alpha", Preview: "find the second thing", LogicalPromptCount: 4},
		},
		Projects: []ProjectOption{{Key: "alpha", Label: "alpha"}, {Key: "zeta", Label: "zeta"}},
	}
	view := RenderSessions(testRender(), data, SessionViewState{SelectedIndex: 1, Provider: "all", DateRange: "30d"}, 70, 20)
	if !strings.Contains(view, "build the first") && !strings.Contains(view, "find the second") {
		t.Fatalf("session rows missing:\n%s", view)
	}
	if !strings.Contains(view, "alpha") || !strings.Contains(view, "zeta") || !strings.Contains(view, "↑/↓ select") {
		t.Fatalf("session controls/filter copy missing:\n%s", view)
	}
	for index, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 70 {
			t.Fatalf("line %d exceeds page width: %d\n%s", index+1, lipgloss.Width(line), view)
		}
	}
	options := ProjectOptions(data.Sessions)
	if len(options) != 2 || options[0].Key != "alpha" || options[1].Key != "zeta" {
		t.Fatalf("project options = %v", options)
	}
}

func TestProjectOptionsPreserveQueryKeysWhileCleaningLabels(t *testing.T) {
	options := ProjectOptionsFromKeys([]string{"  spaced  ", ""})
	if len(options) != 2 || options[0].Key != "  spaced  " || options[0].Label != "spaced" || options[1].Key != "" || options[1].Label != "unknown" {
		t.Fatalf("project options = %+v", options)
	}
}

func testRender() theme.Context {
	return theme.Context{Mode: theme.Plain, Width: 70, Palette: theme.NewPalette(nil)}
}
