package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{})
	updated, command := model.Update(loadedMsg{snapshot: Snapshot{Empty: true, FilesScanned: 12}})
	model = updated.(Model)
	if !model.loading || !model.syncing || command == nil || !strings.Contains(model.View(), "Syncing Codex + Claude · 12 files scanned") {
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
	updated, _ := model.Update(loadedMsg{snapshot: Snapshot{Empty: true}})
	model = updated.(Model)
	if model.offerChecked {
		t.Fatal("empty first-run store checked offer before initial sync")
	}
	updated, command := model.Update(loadedMsg{request: Request{Sync: true}, snapshot: Snapshot{}})
	model = updated.(Model)
	if !model.offerChecked || command == nil {
		t.Fatalf("offer was not checked after initial sync: checked=%v command=%v", model.offerChecked, command != nil)
	}

	populatedOffer := SkillOffer{Check: func() (SkillOfferCheck, error) { return SkillOfferCheck{HasRoots: true}, nil }}
	model = New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, populatedOffer)
	updated, command = model.Update(loadedMsg{snapshot: Snapshot{Empty: false}})
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
	model.loading, model.loaded = false, true
	model.snapshot = Snapshot{
		Cards: [4]Card{{"TOTAL COST", "$1.00"}, {"TOTAL TOKENS", "100"}, {"ACTIVE DAYS", "2"}, {"TOP MODEL", "model"}},
		Views: [4]string{"daily body", "monthly body", "models body", "heatmap body"},
	}
	return model
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
	case "down":
		message = tea.KeyMsg{Type: tea.KeyDown}
	default:
		message = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, _ := model.Update(message)
	return updated.(Model)
}
