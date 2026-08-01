package tui

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestCommandPaletteOpensFiltersAndNavigates(t *testing.T) {
	model := loadedTestModel()
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	model = updated.(Model)
	if command == nil || !model.palette.active {
		t.Fatalf("palette open state=%v command=%v", model.palette.active, command != nil)
	}
	t.Log("\n" + model.View())
	for _, fragment := range []string{"COMMAND PALETTE", "Daily", "Heatmap", "Sync --full", "Vault verify", "History index"} {
		if !strings.Contains(model.View(), fragment) {
			t.Errorf("palette missing %q:\n%s", fragment, model.View())
		}
	}

	for _, runeValue := range []rune("mod") {
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeValue}})
		model = updated.(Model)
	}
	if len(model.palette.matches) != 1 || model.palette.matches[0].command.page != ModelsPageID {
		t.Fatalf("filtered matches = %+v", model.palette.matches)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.palette.active || model.activePageID() != ModelsPageID {
		t.Fatalf("palette navigation active=%v page=%q", model.palette.active, model.activePageID())
	}
	for _, query := range []string{"price", "quit"} {
		model = openPaletteForTest(t, model)
		for _, runeValue := range []rune(query) {
			updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeValue}})
			model = updated.(Model)
		}
		if len(model.palette.matches) != 1 || model.palette.matches[0].command.title == "" {
			t.Fatalf("query %q matches = %+v", query, model.palette.matches)
		}
		if !strings.Contains(model.View(), model.palette.matches[0].command.title) {
			t.Fatalf("query %q did not render selected command:\n%s", query, model.View())
		}
		updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
		model = updated.(Model)
	}
}

func TestCommandPaletteRunsActionOffUpdateLoop(t *testing.T) {
	called := false
	model := loadedTestModelWithCommands(CommandRegistry{Actions: []CommandAction{{
		ID: CommandVaultVerifyID,
		Run: func() (CommandResult, error) {
			called = true
			return CommandResult{Output: "verified 3 archived files"}, nil
		},
	}}})
	model = openPaletteForTest(t, model)
	for _, runeValue := range []rune("vault") {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeValue}})
		model = updated.(Model)
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if command == nil || model.palette.active || !model.commandBusy || model.syncing || model.status != "running vault verify" {
		t.Fatalf("action start state=%+v command=%v", model, command != nil)
	}
	if called {
		t.Fatal("action ran during Update")
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if !called || model.commandBusy || model.syncing || model.status != "vault verify complete" || model.commandOutput != "verified 3 archived files" || !strings.Contains(model.View(), "verified 3 archived files") {
		t.Fatalf("action completion state=%+v called=%v", model, called)
	}
	updated, _ = model.Update(keyMsg("x"))
	model = updated.(Model)
	if model.commandOutput != "" {
		t.Fatal("command result did not dismiss")
	}
}

func TestCommandPaletteBlocksRefreshWhileActionRuns(t *testing.T) {
	model := loadedTestModelWithCommands(CommandRegistry{Actions: []CommandAction{{
		ID:  CommandVaultVerifyID,
		Run: func() (CommandResult, error) { return CommandResult{}, nil },
	}}})
	model.commandBusy = true

	updated, command := model.Update(keyMsg("R"))
	model = updated.(Model)
	if command != nil || !model.commandBusy || model.syncing {
		t.Fatalf("refresh was not ignored while command ran: command=%v busy=%v syncing=%v", command != nil, model.commandBusy, model.syncing)
	}
	status := model.statusBarView(newCockpitLayout(model.request.Width, model.request.Height))
	if !strings.Contains(status, "working") {
		t.Fatalf("busy status disappeared after ignored refresh: %q", status)
	}
	updated, command = model.Update(keyMsg("q"))
	model = updated.(Model)
	if command != nil || !model.quitAfterCommand {
		t.Fatalf("quit was not deferred while command ran: command=%v pending=%v", command != nil, model.quitAfterCommand)
	}
}

func TestCommandPaletteQuitsAfterActionCompletes(t *testing.T) {
	model := loadedTestModelWithCommands(CommandRegistry{Actions: []CommandAction{{
		ID:  CommandVaultVerifyID,
		Run: func() (CommandResult, error) { return CommandResult{}, nil },
	}}})
	model.commandBusy = true
	model.quitAfterCommand = true

	updated, command := model.Update(commandFinishedMsg{command: paletteCommand{title: "Vault verify"}})
	model = updated.(Model)
	if command == nil || model.commandBusy || model.quitAfterCommand {
		t.Fatalf("quit did not wait for action completion: command=%v busy=%v pending=%v", command != nil, model.commandBusy, model.quitAfterCommand)
	}
	message := command()
	if _, ok := message.(tea.QuitMsg); !ok {
		t.Fatalf("completion command=%T, want tea.QuitMsg", message)
	}
}

func TestCommandPaletteDoesNotOpenDuringDashboardWork(t *testing.T) {
	for _, state := range []struct {
		name string
		set  func(*Model)
	}{
		{name: "loading", set: func(model *Model) { model.loading = true }},
		{name: "syncing", set: func(model *Model) { model.syncing = true }},
		{name: "pending startup sync", set: func(model *Model) { model.pendingSync = true }},
	} {
		t.Run(state.name, func(t *testing.T) {
			model := loadedTestModel()
			state.set(&model)

			updated, command := model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
			model = updated.(Model)
			if command != nil || model.palette.active {
				t.Fatalf("palette opened during %s: active=%v command=%v", state.name, model.palette.active, command != nil)
			}
		})
	}
}

func TestCommandPaletteDefersToSkillOffer(t *testing.T) {
	model := loadedTestModel()
	model = openPaletteForTest(t, model)

	updated, _ := model.Update(skillOfferCheckedMsg{check: SkillOfferCheck{HasRoots: true}})
	model = updated.(Model)
	if model.offerState != skillOfferPrompt || model.palette.active || !strings.Contains(model.View(), "Teach your agents") {
		t.Fatalf("skill offer did not take precedence: offer=%v palette=%v\n%s", model.offerState, model.palette.active, model.View())
	}
	updated, _ = model.Update(keyMsg("x"))
	model = updated.(Model)
	if model.offerState != skillOfferPrompt {
		t.Fatalf("offer lost key ownership: state=%v", model.offerState)
	}
}

func TestCommandPaletteDefersStartupSyncUntilActionCompletes(t *testing.T) {
	model := loadedTestModelWithCommands(CommandRegistry{Actions: []CommandAction{{
		ID:  CommandVaultVerifyID,
		Run: func() (CommandResult, error) { return CommandResult{}, nil },
	}}})
	model.commandBusy = true
	model.pendingSync = true

	updated, command := model.Update(skillOfferRecordedMsg{})
	model = updated.(Model)
	if command != nil || !model.pendingSync {
		t.Fatalf("startup sync was not deferred: command=%v pending=%v", command != nil, model.pendingSync)
	}
	updated, command = model.Update(commandFinishedMsg{command: paletteCommand{title: "Vault verify"}})
	model = updated.(Model)
	if command == nil || model.pendingSync || !model.syncing {
		t.Fatalf("startup sync did not resume after action: command=%v pending=%v syncing=%v", command != nil, model.pendingSync, model.syncing)
	}
}

func TestCommandPaletteReloadsAfterBusyResize(t *testing.T) {
	for _, state := range []struct {
		name       string
		busy       bool
		syncing    bool
		completion tea.Msg
	}{
		{name: "command", busy: true, completion: commandFinishedMsg{command: paletteCommand{title: "Vault verify"}}},
		{name: "sync", syncing: true, completion: loadedMsg{request: Request{Sync: true}, snapshot: Snapshot{}}},
	} {
		t.Run(state.name, func(t *testing.T) {
			var got Request
			model := loadedTestModel()
			model.loader = func(request Request) (Snapshot, error) {
				got = request
				return Snapshot{}, nil
			}
			model.commandBusy, model.syncing = state.busy, state.syncing

			updated, command := model.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
			model = updated.(Model)
			if command != nil || !model.pendingResize {
				t.Fatalf("resize was not deferred during %s: command=%v pending=%v", state.name, command != nil, model.pendingResize)
			}
			updated, command = model.Update(state.completion)
			model = updated.(Model)
			if command == nil || model.pendingResize {
				t.Fatalf("resize did not resume after %s: command=%v pending=%v", state.name, command != nil, model.pendingResize)
			}
			command()
			if got.Width != 120 || got.Height != 35 {
				t.Fatalf("deferred resize request=%+v", got)
			}
		})
	}
}

func TestCommandPaletteKeepsSuccessfulResultAfterResizeFailure(t *testing.T) {
	model := loadedTestModelWithCommands(CommandRegistry{Actions: []CommandAction{{
		ID:  CommandVaultVerifyID,
		Run: func() (CommandResult, error) { return CommandResult{Output: "verified 3 archived files"}, nil },
	}}})
	model = openPaletteForTest(t, model)
	for _, runeValue := range []rune("vault") {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeValue}})
		model = updated.(Model)
	}
	updated, action := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	model.pendingResize = true
	model.loader = func(Request) (Snapshot, error) { return Snapshot{}, errors.New("resize reload failed") }
	updated, reload := model.Update(action())
	model = updated.(Model)
	if reload == nil {
		t.Fatal("successful command did not schedule deferred resize reload")
	}
	updated, _ = model.Update(reload())
	model = updated.(Model)
	view := model.View()
	if !strings.Contains(view, "COMMAND RESULT") || strings.Contains(view, "COMMAND FAILED") || !strings.Contains(view, "verified 3 archived files") || model.warning != "resize reload failed" {
		t.Fatalf("resize failure corrupted command result: warning=%q\n%s", model.warning, view)
	}
}

func TestCommandPaletteTranslatesActionFailure(t *testing.T) {
	model := loadedTestModelWithCommands(CommandRegistry{Actions: []CommandAction{{
		ID: CommandPricingID,
		Run: func() (CommandResult, error) {
			return CommandResult{Output: "raw pricing parser detail"}, errors.New("pricing command failed")
		},
	}}})
	model = openPaletteForTest(t, model)
	for _, runeValue := range []rune("price") {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeValue}})
		model = updated.(Model)
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	view := model.View()
	if model.warning != "Pricing failed; run `tokenomnom pricing` for details" || model.status != "" || model.commandOutput != "raw pricing parser detail" || !strings.Contains(view, "COMMAND FAILED") || !strings.Contains(view, "raw pricing parser detail") || !strings.Contains(view, "tokenomnom pricing") {
		t.Fatalf("failure state warning=%q status=%q output=%q view=%s", model.warning, model.status, model.commandOutput, view)
	}
	updated, _ = model.Update(keyMsg("x"))
	model = updated.(Model)
	if model.commandOutput != "" || model.warning != "" {
		t.Fatalf("failure overlay did not dismiss cleanly: output=%q warning=%q", model.commandOutput, model.warning)
	}
}

func TestCommandPaletteFullSyncPassesFullRequest(t *testing.T) {
	var got Request
	model := New(testRender(), func(request Request) (Snapshot, error) {
		got = request
		return Snapshot{}, nil
	}, SkillOffer{})
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded = false, true
	model = openPaletteForTest(t, model)
	for _, runeValue := range []rune("sync") {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{runeValue}})
		model = updated.(Model)
	}
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if command == nil || !model.commandBusy || model.status != "running sync --full" {
		t.Fatalf("full sync start state=%+v command=%v", model, command != nil)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if !got.Sync || !got.FullSync || model.status != "full sync complete" || model.commandBusy {
		t.Fatalf("full sync request=%+v state=%+v", got, model)
	}
}

func TestCommandPaletteFitsMinimumTerminal(t *testing.T) {
	model := loadedTestModel()
	model.request.Width, model.request.Height = minimumWidth, minimumHeight
	model = openPaletteForTest(t, model)
	view := model.View()
	lines := strings.Split(view, "\n")
	if len(lines) != minimumHeight {
		t.Fatalf("palette rendered %d lines, want %d:\n%s", len(lines), minimumHeight, view)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != minimumWidth {
			t.Fatalf("palette line %d width=%d, want %d:\n%s", index+1, width, minimumWidth, view)
		}
	}
}

func TestCommandPaletteStyledFitsWindow(t *testing.T) {
	terminal, dark := true, true
	render := theme.Resolve(theme.ResolveOptions{
		Output: &bytes.Buffer{}, ForceTerminal: &terminal, Width: 100,
		ForceColor: true, Dark: &dark, LookupEnv: func(string) (string, bool) { return "", false },
	})
	model := New(render, func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{})
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded = false, true
	model.snapshot = Snapshot{Views: [4]string{"daily body", "monthly body", "models body", "heatmap body"}}
	model = openPaletteForTest(t, model)
	for index, line := range strings.Split(model.View(), "\n") {
		if width := lipgloss.Width(line); width != 100 {
			t.Fatalf("styled palette line %d width=%d, want 100:\n%s", index+1, width, model.View())
		}
	}
}

func loadedTestModelWithCommands(registry CommandRegistry) Model {
	model := New(testRender(), func(Request) (Snapshot, error) { return Snapshot{}, nil }, SkillOffer{}, registry)
	model.request.Width, model.request.Height = 100, 30
	model.loading, model.loaded = false, true
	model.snapshot = Snapshot{Views: [4]string{"daily body", "monthly body", "models body", "heatmap body"}}
	return model
}

func openPaletteForTest(t *testing.T, model Model) Model {
	t.Helper()
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyCtrlK})
	return updated.(Model)
}
