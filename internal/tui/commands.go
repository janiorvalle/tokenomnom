package tui

import (
	"sort"
	"strings"
	"unicode"
)

const (
	CommandSyncFullID     = "sync-full"
	CommandVaultVerifyID  = "vault-verify"
	CommandHistoryIndexID = "history-index"
	CommandPricingID      = "pricing"
	CommandQuitID         = "quit"
)

// CommandResult is the user-visible output produced by a command action.
type CommandResult struct {
	Output string
}

// CommandAction is a maintenance command exposed by the command palette.
// Run is called in a Bubble Tea command, never during Update or View.
type CommandAction struct {
	ID          string
	Title       string
	Description string
	Invocation  string
	Run         func() (CommandResult, error)
}

// CommandRegistry contains application actions that the TUI cannot own.
// Pages are added automatically from the page router.
type CommandRegistry struct {
	Actions []CommandAction
}

type paletteCommand struct {
	id          string
	title       string
	description string
	invocation  string
	page        PageID
	action      CommandAction
}

type paletteMatch struct {
	command paletteCommand
	score   int
}

func defaultCommandActions() []CommandAction {
	return []CommandAction{
		{ID: CommandSyncFullID, Title: "Sync --full", Description: "Re-ingest every usage file", Invocation: "tokenomnom sync --full"},
		{ID: CommandVaultVerifyID, Title: "Vault verify", Description: "Check archived transcript integrity", Invocation: "tokenomnom vault verify"},
		{ID: CommandHistoryIndexID, Title: "History index", Description: "Refresh the transcript search index", Invocation: "tokenomnom history index"},
		{ID: CommandPricingID, Title: "Pricing", Description: "Show effective API and user rates", Invocation: "tokenomnom pricing"},
		{ID: CommandQuitID, Title: "Quit", Description: "Exit tokenomnom", Invocation: "q"},
	}
}

func mergedCommandActions(registry CommandRegistry) []CommandAction {
	actions := defaultCommandActions()
	for _, override := range registry.Actions {
		if strings.TrimSpace(override.ID) == "" {
			continue
		}
		for index := range actions {
			if actions[index].ID != override.ID {
				continue
			}
			if override.Title != "" {
				actions[index].Title = override.Title
			}
			if override.Description != "" {
				actions[index].Description = override.Description
			}
			if override.Invocation != "" {
				actions[index].Invocation = override.Invocation
			}
			actions[index].Run = override.Run
			break
		}
		if !containsCommandAction(actions, override.ID) {
			actions = append(actions, normalizeCommandAction(override))
		}
	}
	return actions
}

func containsCommandAction(actions []CommandAction, id string) bool {
	for _, action := range actions {
		if action.ID == id {
			return true
		}
	}
	return false
}

func normalizeCommandAction(action CommandAction) CommandAction {
	if action.Title == "" {
		action.Title = titleFromCommandID(action.ID)
	}
	if action.Description == "" {
		action.Description = "Run " + action.Title
	}
	return action
}

func titleFromCommandID(id string) string {
	words := strings.Fields(strings.ReplaceAll(id, "-", " "))
	for index, word := range words {
		runes := []rune(word)
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[index] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

func paletteCommands(router PageRouter, registry CommandRegistry) []paletteCommand {
	commands := make([]paletteCommand, 0, len(router.Pages())+len(registry.Actions)+len(defaultCommandActions()))
	for _, page := range router.Pages() {
		commands = append(commands, paletteCommand{
			id:          string(page.ID()),
			title:       page.Title(),
			description: "Open " + page.Title(),
			page:        page.ID(),
		})
	}
	for _, action := range mergedCommandActions(registry) {
		action = normalizeCommandAction(action)
		commands = append(commands, paletteCommand{
			id:          action.ID,
			title:       action.Title,
			description: action.Description,
			invocation:  action.Invocation,
			action:      action,
		})
	}
	return commands
}

func filterPaletteCommands(commands []paletteCommand, query string) []paletteMatch {
	query = normalizeCommandText(query)
	if query == "" {
		matches := make([]paletteMatch, 0, len(commands))
		for _, command := range commands {
			matches = append(matches, paletteMatch{command: command})
		}
		return matches
	}

	matches := make([]paletteMatch, 0, len(commands))
	for _, command := range commands {
		target := normalizeCommandText(command.title + " " + command.description + " " + command.id)
		score, ok := fuzzyCommandScore(query, target)
		if ok {
			matches = append(matches, paletteMatch{command: command, score: score})
		}
	}
	sort.SliceStable(matches, func(left, right int) bool {
		return matches[left].score > matches[right].score
	})
	return matches
}

func normalizeCommandText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func fuzzyCommandScore(query, target string) (int, bool) {
	queryRunes, targetRunes := []rune(query), []rune(target)
	if len(queryRunes) == 0 {
		return 0, true
	}
	position := 0
	previous := -2
	score := 0
	for _, wanted := range queryRunes {
		found := -1
		for index := position; index < len(targetRunes); index++ {
			if targetRunes[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		score += 10
		if found == position {
			score += 5
		}
		if found == previous+1 {
			score += 8
		}
		if found == 0 {
			score += 12
		}
		previous, position = found, found+1
	}
	return score - (len(targetRunes) - len(queryRunes)), true
}
