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
	keyActionOpenPalette
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
	{Display: "← / →", Keys: []string{"left", "right"}, Description: "move active page / day cursor", Action: keyActionPageCommand},
	{Display: "home / end", Keys: []string{"home", "end"}, Description: "jump to page edge", Action: keyActionPageCommand},
	{Display: "h / l", Keys: []string{"h", "l"}, Description: "zoom ledger period", Action: keyActionPageCommand},
	{Display: "j / k", Keys: []string{"j", "k"}, Description: "move ledger row", Action: keyActionPageCommand},
	{Display: "↑ / ↓", Keys: []string{"up", "down"}, Description: "move active page / day detail", Action: keyActionPageCommand},
	{Display: "enter", Keys: []string{"enter"}, Description: "open selected item", Action: keyActionPageCommand},
	{Display: "esc", Keys: []string{"esc"}, Description: "back to list", Action: keyActionPageCommand},
	{Display: "f", Keys: []string{"f"}, Description: "cycle project filter", Action: keyActionPageCommand},
	{Display: "s", Keys: []string{"s"}, Description: "sort models", Action: keyActionPageCommand},
	{Display: "y", Keys: []string{"y"}, Description: "calendar-year heatmap", Action: keyActionPageCommand},
	{Display: "e", Keys: []string{"e"}, Description: "export session", Action: keyActionPageCommand},
	{Display: "p", Keys: []string{"p"}, Description: "cycle provider", FooterKey: "p", Footer: "provider", Action: keyActionProvider},
	{Display: "r", Keys: []string{"r"}, Description: "cycle range", FooterKey: "r", Footer: "range", Action: keyActionRange},
	{Display: "R", Keys: []string{"R"}, Description: "refresh now", FooterKey: "R", Footer: "refresh", Action: keyActionRefresh},
	{Display: "v", Keys: []string{"v", "V"}, Description: "verify vault", Action: keyActionPageCommand},
	{Display: "ctrl+k", Keys: []string{"ctrl+k"}, Description: "open command palette", Action: keyActionOpenPalette},
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
	badge := subtle.Render(sizeBadgeLabel(layout))
	if layout.tiers.Width == WidthFloor {
		disclaimer := subtle.Render("API list-price equivalents, not actual bills")
		shortDisclaimer := subtle.Render("API prices, not actual bills")
		helpAndQuit := m.footerHint(KeyBinding{FooterKey: "?", Footer: "help"}) + subtle.Render(" · ") + m.footerHint(KeyBinding{FooterKey: "q", Footer: "quit"})
		quitHint := m.footerHint(KeyBinding{FooterKey: "q", Footer: "quit"})
		for _, left := range []string{
			disclaimer + subtle.Render(" · ") + helpAndQuit,
			shortDisclaimer + subtle.Render(" · ") + helpAndQuit,
			disclaimer + subtle.Render(" · ") + quitHint,
			shortDisclaimer + subtle.Render(" · ") + quitHint,
			disclaimer,
			shortDisclaimer,
		} {
			if lipgloss.Width(left)+1+lipgloss.Width(badge) <= layout.innerWidth {
				return fitLine(joinFooterSegments(left, badge, layout.innerWidth), layout.innerWidth)
			}
		}
		available := max(0, layout.innerWidth-lipgloss.Width(badge)-1)
		return fitLine(joinFooterSegments(fitLine(disclaimer, available), badge, layout.innerWidth), layout.innerWidth)
	}
	disclaimer := subtle.Render("API list-price equivalents, not actual bills")
	gap := layout.innerWidth - lipgloss.Width(disclaimer) - lipgloss.Width(badge)
	disclaimerRow := fitRight(badge, layout.innerWidth)
	if gap >= 1 {
		disclaimerRow = fitLine(joinFooterSegments(disclaimer, badge, layout.innerWidth), layout.innerWidth)
	}
	return strings.Join([]string{
		fitLine(hints, layout.innerWidth),
		disclaimerRow,
	}, "\n")
}

func joinFooterSegments(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return fitRight(right, width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) helpView() string {
	entries := m.helpEntries()
	keyWidth := 0
	for _, entry := range entries {
		keyWidth = max(keyWidth, lipgloss.Width(entry.display))
	}
	var body strings.Builder
	body.WriteString(m.render.Palette.Header().Render("Keys"))
	if m.request.Height > minimumHeight {
		body.WriteString("\n\n")
	} else {
		body.WriteByte('\n')
	}
	for _, entry := range entries {
		key := entry.display + strings.Repeat(" ", keyWidth-lipgloss.Width(entry.display))
		body.WriteString(m.render.Palette.Emphasis().Render(key))
		body.WriteString("   ")
		body.WriteString(m.render.Palette.Subtle().Render(entry.description))
		body.WriteByte('\n')
	}
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.BorderColor()).
		Padding(0, 2).Render(strings.TrimRight(body.String(), "\n"))
	return m.place(modal)
}

type helpEntry struct {
	display     string
	description string
}

func (m Model) helpEntries() []helpEntry {
	bindings := helpBindings()
	entries := make([]helpEntry, 0, len(bindings))
	for _, binding := range bindings {
		entries = append(entries, helpEntry{
			display:     keyBindingDisplay(binding, len(m.router.Pages())),
			description: binding.Description,
		})
	}
	if len(entries)+4 <= max(m.request.Height, minimumHeight) {
		return entries
	}

	compact := make([]helpEntry, 0, len(entries)-2)
	for index := 0; index < len(entries); index++ {
		if index+1 < len(entries) && ((entries[index].display == "h / l" && entries[index+1].display == "j / k") || (entries[index].display == "enter" && entries[index+1].display == "esc") || (entries[index].display == "p" && entries[index+1].display == "r")) {
			description := "open / back"
			if entries[index].display == "h / l" {
				description = "ledger zoom / row"
			} else if entries[index].display == "p" {
				description = "cycle provider / range"
			}
			compact = append(compact, helpEntry{
				display:     entries[index].display + " · " + entries[index+1].display,
				description: description,
			})
			index++
			continue
		}
		compact = append(compact, entries[index])
	}
	return compact
}

func helpBindings() []KeyBinding {
	bindings := make([]KeyBinding, 0, len(keyRegistry)-1)
	for _, binding := range keyRegistry {
		if binding.Display == "e" {
			continue
		}
		if binding.Display == "enter" {
			binding.Description += " · e export"
		}
		bindings = append(bindings, binding)
	}
	return bindings
}
