package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestUpdateNavigationFiltersAndHelp(t *testing.T) {
	model := loadedTestModel()
	model = updateKeyForTest(t, model, "tab")
	if model.tab != MonthlyTab {
		t.Fatalf("tab = %v", model.tab)
	}
	model = updateKeyForTest(t, model, "4")
	if model.tab != HeatmapTab {
		t.Fatalf("number tab = %v", model.tab)
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
	if model.request.DailyOffset != -7 {
		t.Fatalf("daily offset = %d", model.request.DailyOffset)
	}
	model.tab = MonthlyTab
	model = updateKeyForTest(t, model, "left")
	if model.request.MonthlyOffset != -1 {
		t.Fatalf("monthly offset = %d", model.request.MonthlyOffset)
	}
	model.tab = ModelsTab
	model = updateKeyForTest(t, model, "s")
	model = updateKeyForTest(t, model, "down")
	if model.request.ModelSort != 1 || model.request.ModelOffset != 1 {
		t.Fatalf("model navigation = %+v", model.request)
	}
	model.tab = HeatmapTab
	model = updateKeyForTest(t, model, "y")
	model = updateKeyForTest(t, model, "right")
	if model.request.HeatmapYear || model.request.HeatmapOffset != 1 {
		t.Fatalf("heatmap navigation = %+v", model.request)
	}

	updated, command := model.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
	model = updated.(Model)
	if command != nil || model.View() != "terminal too small\n" {
		t.Fatalf("small terminal state = command %v, view %q", command != nil, model.View())
	}
	updated, command = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model = updated.(Model)
	if command == nil || model.request.Width != 100 || model.request.Height != 30 {
		t.Fatalf("resize state = %+v, command %v", model.request, command != nil)
	}
}

func TestSyncProgressLoadedAndFailureTransitions(t *testing.T) {
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil })
	updated, command := model.Update(loadedMsg{snapshot: Snapshot{Empty: true}})
	model = updated.(Model)
	if !model.loading || !model.syncing || command == nil || !strings.Contains(model.View(), "Syncing Codex + Claude") {
		t.Fatalf("empty initial transition = %+v, command %v", model, command != nil)
	}

	wanted := Snapshot{Views: [4]string{"daily", "monthly", "models", "heatmap"}}
	updated, _ = model.Update(loadedMsg{request: Request{Sync: true}, snapshot: wanted})
	model = updated.(Model)
	if model.loading || model.syncing || model.snapshot.Views[0] != "daily" || !strings.Contains(model.status, "synced") {
		t.Fatalf("loaded transition = %+v", model)
	}

	updated, _ = model.Update(loadedMsg{request: Request{Sync: true}, err: errors.New("sync failed")})
	model = updated.(Model)
	if model.warning != "sync failed" || model.snapshot.Views[0] != "daily" || !strings.Contains(model.View(), "sync failed") {
		t.Fatalf("failure transition = %+v", model)
	}
}

func TestEveryViewRendersStructure(t *testing.T) {
	model := loadedTestModel()
	for tab := Tab(0); tab < tabCount; tab++ {
		model.tab = tab
		view := model.View()
		for _, fragment := range []string{"TOTAL COST", tabNames[tab], model.snapshot.Views[tab], "API list-price equivalents"} {
			if !strings.Contains(view, fragment) {
				t.Errorf("%s view missing %q:\n%s", tabNames[tab], fragment, view)
			}
		}
	}
}

func loadedTestModel() Model {
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil })
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded = false, true
	model.snapshot = Snapshot{
		Cards: [4]Card{{"TOTAL COST", "$1.00"}, {"TOTAL TOKENS", "100"}, {"ACTIVE DAYS", "2"}, {"TOP MODEL", "model"}},
		Views: [4]string{"daily body", "monthly body", "models body", "heatmap body"},
	}
	return model
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
	case "down":
		message = tea.KeyMsg{Type: tea.KeyDown}
	default:
		message = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, _ := model.Update(message)
	return updated.(Model)
}
