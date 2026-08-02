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
	HelpGroup   string
	FooterKey   string
	Footer      string
	Action      keyAction
	PageNumbers bool
}

var keyRegistry = [...]KeyBinding{
	{Keys: []string{"tab", "shift+tab"}, Description: "switch view", HelpGroup: "NAVIGATE", FooterKey: "tab", Footer: "views", Action: keyActionNavigatePages, PageNumbers: true},
	{Display: "← / →", Keys: []string{"left", "right"}, Description: "move active page / day cursor", HelpGroup: "NAVIGATE", Action: keyActionPageCommand},
	{Display: "home / end", Keys: []string{"home", "end"}, Description: "jump to page edge", HelpGroup: "NAVIGATE", Action: keyActionPageCommand},
	{Display: "h / l", Keys: []string{"h", "l"}, Description: "zoom ledger period", HelpGroup: "PAGES", Action: keyActionPageCommand},
	{Display: "j / k", Keys: []string{"j", "k"}, Description: "move ledger row", HelpGroup: "PAGES", Action: keyActionPageCommand},
	{Display: "↑ / ↓", Keys: []string{"up", "down"}, Description: "move active page / day detail", HelpGroup: "NAVIGATE", Action: keyActionPageCommand},
	{Display: "enter", Keys: []string{"enter"}, Description: "open selected item", HelpGroup: "NAVIGATE", Action: keyActionPageCommand},
	{Display: "esc", Keys: []string{"esc"}, Description: "back to list", HelpGroup: "NAVIGATE", Action: keyActionPageCommand},
	{Display: "f", Keys: []string{"f"}, Description: "cycle project filter", HelpGroup: "PAGES", Action: keyActionPageCommand},
	{Display: "s", Keys: []string{"s"}, Description: "sort models", HelpGroup: "PAGES", Action: keyActionPageCommand},
	{Display: "y", Keys: []string{"y"}, Description: "calendar-year heatmap", HelpGroup: "PAGES", Action: keyActionPageCommand},
	{Display: "e", Keys: []string{"e"}, Description: "export session", HelpGroup: "ACTIONS", Action: keyActionPageCommand},
	{Display: "p", Keys: []string{"p"}, Description: "cycle provider", HelpGroup: "ACTIONS", FooterKey: "p", Footer: "provider", Action: keyActionProvider},
	{Display: "r", Keys: []string{"r"}, Description: "cycle range", HelpGroup: "ACTIONS", FooterKey: "r", Footer: "range", Action: keyActionRange},
	{Display: "R", Keys: []string{"R"}, Description: "refresh now", HelpGroup: "ACTIONS", FooterKey: "R", Footer: "refresh", Action: keyActionRefresh},
	{Display: "v", Keys: []string{"v", "V"}, Description: "verify vault", HelpGroup: "ACTIONS", Action: keyActionPageCommand},
	{Display: "ctrl+k", Keys: []string{"ctrl+k"}, Description: "open command palette", HelpGroup: "SYSTEM", Action: keyActionOpenPalette},
	{Display: "?", Keys: []string{"?"}, Description: "close help", HelpGroup: "SYSTEM", FooterKey: "?", Footer: "help", Action: keyActionToggleHelp},
	{Display: "q / ctrl+c", Keys: []string{"q", "ctrl+c"}, Description: "quit", HelpGroup: "SYSTEM", FooterKey: "q", Footer: "quit", Action: keyActionQuit},
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
		disclaimer := subtle.Render("API list-price equivalents, not actual bills; user rate estimates")
		shortText := "user rate est.; not bills"
		if layout.innerWidth >= 72 {
			shortText = "user rate estimates; not actual bills"
		}
		shortDisclaimer := subtle.Render(shortText)
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
	disclaimer := subtle.Render("API list-price equivalents, not actual bills; user rate estimates")
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
	layout := newCockpitLayout(m.request.Width, m.request.Height)
	dimmed := lipgloss.NewStyle().Faint(true).Render(m.baseView())
	return overlayBlock(dimmed, m.helpModal(layout), layout.width, layout.height)
}

type helpGroup struct {
	title   string
	entries []helpEntry
}

func helpWidth(windowWidth int) int {
	return max(1, min(110, windowWidth-8))
}

func (m Model) helpModal(layout cockpitLayout) string {
	width := helpWidth(layout.width)
	contentWidth := max(1, width-6)
	groups := m.helpGroups()
	compact := layout.tiers.Height == HeightShort
	if compact {
		groups = compactHelpGroups(groups)
	}
	heading := m.render.Palette.Header().Render("HELP") + "  " + m.render.Palette.Subtle().Render("? / esc close")
	body := heading + "\n"
	if !compact {
		body += "\n"
	}
	if layout.tiers.Width == WidthWide || compact {
		body += renderHelpColumns(m, groups, contentWidth, compact)
	} else {
		body += renderHelpGroups(m, groups, contentWidth, false)
	}
	if !compact || layout.height > minimumHeight {
		body += "\n"
		if !compact {
			body += "\n"
		}
		body += m.render.Palette.Subtle().Render("Keyboard-first controls; page keys stay in their group.")
	}
	return m.render.Palette.Surface().
		Width(max(1, width-2)).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.AccentBorderColor()).
		Padding(0, 2).
		Render(body)
}

func (m Model) helpGroups() []helpGroup {
	groups := []helpGroup{
		{title: "NAVIGATE"},
		{title: "PAGES"},
		{title: "ACTIONS"},
		{title: "SYSTEM"},
	}
	groupIndex := map[string]int{
		"NAVIGATE": 0,
		"PAGES":    1,
		"ACTIONS":  2,
		"SYSTEM":   3,
	}
	for _, binding := range helpBindings() {
		index, ok := groupIndex[binding.HelpGroup]
		if !ok {
			index = len(groups) - 1
		}
		groups[index].entries = append(groups[index].entries, helpEntry{
			display:     keyBindingDisplay(binding, len(m.router.Pages())),
			description: binding.Description,
		})
	}
	return groups
}

func renderHelpColumns(model Model, groups []helpGroup, width int, compact bool) string {
	if len(groups) < 4 {
		return renderHelpGroups(model, groups, width, compact)
	}
	gap := 4
	if compact {
		gap = 2
	}
	leftWidth := max(1, (width-gap)/2)
	rightWidth := max(1, width-gap-leftWidth)
	left := renderHelpGroups(model, groups[:2], leftWidth, compact)
	right := renderHelpGroups(model, groups[2:], rightWidth, compact)
	height := max(strings.Count(left, "\n")+1, strings.Count(right, "\n")+1)
	return lipgloss.JoinHorizontal(lipgloss.Top,
		fitBlock(left, leftWidth, height),
		fitBlock("", gap, height),
		fitBlock(right, rightWidth, height),
	)
}

func renderHelpGroups(model Model, groups []helpGroup, width int, compact bool) string {
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		if len(group.entries) == 0 {
			continue
		}
		parts = append(parts, renderHelpGroup(model, group, width))
	}
	separator := "\n\n"
	if compact {
		separator = "\n"
	}
	return strings.Join(parts, separator)
}

func compactHelpGroups(groups []helpGroup) []helpGroup {
	compactGroups := make([]helpGroup, 0, len(groups))
	for _, group := range groups {
		group.entries = compactHelpEntries(group.entries)
		compactGroups = append(compactGroups, group)
	}
	return compactGroups
}

func compactHelpEntries(entries []helpEntry) []helpEntry {
	compact := make([]helpEntry, 0, len(entries))
	for index := 0; index < len(entries); index++ {
		if index+1 < len(entries) {
			pair := entries[index].display + " / " + entries[index+1].display
			if pair == "h / l / j / k" {
				compact = append(compact, helpEntry{display: "h / l · j / k", description: "ledger zoom / row"})
				index++
				continue
			}
			if pair == "enter / esc" {
				compact = append(compact, helpEntry{display: "enter · esc", description: "open / back"})
				index++
				continue
			}
			if pair == "p / r" {
				compact = append(compact, helpEntry{display: "p · r", description: "cycle provider / range"})
				index++
				continue
			}
		}
		compact = append(compact, entries[index])
	}
	return compact
}

func renderHelpGroup(model Model, group helpGroup, width int) string {
	keyWidth := 0
	for _, entry := range group.entries {
		keyWidth = max(keyWidth, lipgloss.Width(entry.display))
	}
	lines := []string{model.render.Palette.Header().Render(group.title)}
	for _, entry := range group.entries {
		key := entry.display + strings.Repeat(" ", max(0, keyWidth-lipgloss.Width(entry.display)))
		line := model.render.Palette.Emphasis().Render(key) + "  " + model.render.Palette.Subtle().Render(entry.description)
		lines = append(lines, fitLine(line, width))
	}
	return strings.Join(lines, "\n")
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

	return compactHelpEntries(entries)
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
