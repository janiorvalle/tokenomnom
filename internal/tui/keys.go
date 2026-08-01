package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type keyAction uint8

const (
	keyActionNavigatePages keyAction = iota
	keyActionPageCommand
	keyActionProvider
	keyActionRange
	keyActionRefresh
	keyActionToggleHelp
	keyActionQuit
)

// KeyBinding is the single source for keyboard behavior and its visible copy.
// Display is used for static help copy; PageNumbers derives page navigation copy.
type KeyBinding struct {
	Display     string
	Keys        []string
	Description string
	FooterKey   string
	Footer      string
	Action      keyAction
	PageNumbers bool
}

var keyRegistry = [...]KeyBinding{
	{Keys: []string{"tab", "shift+tab"}, Description: "switch view", FooterKey: "tab", Footer: "views", Action: keyActionNavigatePages, PageNumbers: true},
	{Display: "← / →", Keys: []string{"left", "right"}, Description: "pan active timeline", Action: keyActionPageCommand},
	{Display: "home / end", Keys: []string{"home", "end"}, Description: "jump to range edge", Action: keyActionPageCommand},
	{Display: "↑ / ↓", Keys: []string{"up", "down"}, Description: "scroll models", Action: keyActionPageCommand},
	{Display: "s", Keys: []string{"s"}, Description: "sort models", Action: keyActionPageCommand},
	{Display: "y", Keys: []string{"y"}, Description: "calendar-year heatmap", Action: keyActionPageCommand},
	{Display: "p", Keys: []string{"p"}, Description: "cycle provider", FooterKey: "p", Footer: "provider", Action: keyActionProvider},
	{Display: "r", Keys: []string{"r"}, Description: "cycle range", FooterKey: "r", Footer: "range", Action: keyActionRange},
	{Display: "R", Keys: []string{"R"}, Description: "refresh now", FooterKey: "R", Footer: "refresh", Action: keyActionRefresh},
	{Display: "?", Keys: []string{"?"}, Description: "close help", FooterKey: "?", Footer: "help", Action: keyActionToggleHelp},
	{Display: "q / ctrl+c", Keys: []string{"q", "ctrl+c"}, Description: "quit", FooterKey: "q", Footer: "quit", Action: keyActionQuit},
}

// KeyBindings returns the registered bindings for tests and future page chrome.
func KeyBindings() []KeyBinding {
	return append([]KeyBinding(nil), keyRegistry[:]...)
}

func keyBindingFor(key string, pageCount int) (KeyBinding, bool) {
	for _, binding := range keyRegistry {
		for _, alias := range binding.Keys {
			if alias == key {
				return binding, true
			}
		}
		if binding.PageNumbers && isPageNumber(key) && int(key[0]-'0') <= min(pageCount, 9) {
			return binding, true
		}
	}
	return KeyBinding{}, false
}

func isPageNumber(key string) bool {
	return len(key) == 1 && key[0] >= '1' && key[0] <= '9'
}

func pageNavigationDisplay(pageCount int) string {
	pageCount = min(max(1, pageCount), 9)
	return fmt.Sprintf("tab / shift+tab / 1-%d", pageCount)
}

func keyBindingDisplay(binding KeyBinding, pageCount int) string {
	if binding.PageNumbers {
		return pageNavigationDisplay(pageCount)
	}
	return binding.Display
}

func footerBindings() []KeyBinding {
	bindings := make([]KeyBinding, 0, len(keyRegistry))
	for _, binding := range keyRegistry {
		if binding.Footer != "" {
			bindings = append(bindings, binding)
		}
	}
	return bindings
}

func (m Model) footerHint(binding KeyBinding) string {
	key := m.render.Palette.Header().Bold(false).Render(binding.FooterKey)
	return key + " " + m.render.Palette.Subtle().Render(binding.Footer)
}

func (m Model) footerHintsView(width int) string {
	subtle := m.render.Palette.Subtle()
	bindings := footerBindings()
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		parts = append(parts, m.footerHint(binding))
	}
	line := strings.Join(parts, subtle.Render(" · "))
	if lipgloss.Width(line) <= width {
		return line
	}

	compact := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Footer == "provider" {
			binding.Footer = "prov"
		}
		compact = append(compact, m.footerHint(binding))
	}
	return strings.Join(compact, subtle.Render(" · "))
}

func (m Model) footerView(layout cockpitLayout) string {
	subtle := m.render.Palette.Subtle()
	hints := m.footerHintsView(layout.innerWidth)
	return strings.Join([]string{
		m.render.Palette.Border().Render(strings.Repeat("─", layout.innerWidth)),
		fitLine(hints, layout.innerWidth),
		fitLine(subtle.Render("API list-price equivalents, not actual bills"), layout.innerWidth),
	}, "\n")
}

func (m Model) helpView() string {
	keyWidth := 0
	for _, binding := range keyRegistry {
		display := keyBindingDisplay(binding, len(m.router.Pages()))
		keyWidth = max(keyWidth, lipgloss.Width(display))
	}
	var body strings.Builder
	body.WriteString(m.render.Palette.Header().Render("Keys"))
	body.WriteString("\n\n")
	for _, binding := range keyRegistry {
		display := keyBindingDisplay(binding, len(m.router.Pages()))
		key := display + strings.Repeat(" ", keyWidth-lipgloss.Width(display))
		body.WriteString(m.render.Palette.Emphasis().Render(key))
		body.WriteString("   ")
		body.WriteString(m.render.Palette.Subtle().Render(binding.Description))
		body.WriteByte('\n')
	}
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.BorderColor()).
		Padding(0, 2).Render(strings.TrimRight(body.String(), "\n"))
	return m.place(modal)
}
