package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const paletteVisibleRows = 7

type paletteState struct {
	active    bool
	input     textinput.Model
	commands  []paletteCommand
	matches   []paletteMatch
	selection int
}

type paletteEvent uint8

const (
	paletteNoEvent paletteEvent = iota
	paletteClosed
	paletteSelected
)

type paletteSelection struct {
	event   paletteEvent
	command paletteCommand
}

func newPalette(render lipgloss.Style) paletteState {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "Search pages and actions"
	input.PromptStyle = render
	input.TextStyle = render
	input.PlaceholderStyle = render
	input.Cursor.Style = render
	return paletteState{input: input}
}

func (p *paletteState) open(router PageRouter, registry CommandRegistry, width int) tea.Cmd {
	p.active = true
	p.commands = paletteCommands(router, registry)
	p.input.Reset()
	p.input.Width = paletteInputWidth(width)
	p.selection = 0
	p.refreshMatches()
	return p.input.Focus()
}

func (p *paletteState) close() {
	p.active = false
	p.input.Blur()
}

func (p *paletteState) resize(width int) {
	p.input.Width = paletteInputWidth(width)
}

func paletteInputWidth(windowWidth int) int {
	return max(1, paletteWidth(windowWidth)-8)
}

func paletteWidth(windowWidth int) int {
	return min(76, max(46, windowWidth-8))
}

func (p *paletteState) refreshMatches() {
	p.matches = filterPaletteCommands(p.commands, p.input.Value())
	if p.selection >= len(p.matches) {
		p.selection = max(0, len(p.matches)-1)
	}
}

func (p *paletteState) moveSelection(direction int) {
	if len(p.matches) == 0 || direction == 0 {
		return
	}
	p.selection = (p.selection + direction) % len(p.matches)
	if p.selection < 0 {
		p.selection += len(p.matches)
	}
}

func (p *paletteState) update(key tea.KeyMsg) (paletteSelection, tea.Cmd) {
	value := key.String()
	switch value {
	case "esc", "ctrl+k":
		p.close()
		return paletteSelection{event: paletteClosed}, nil
	case "up", "ctrl+p":
		p.moveSelection(-1)
		return paletteSelection{}, nil
	case "down", "ctrl+n":
		p.moveSelection(1)
		return paletteSelection{}, nil
	case "enter":
		if len(p.matches) == 0 {
			return paletteSelection{}, nil
		}
		command := p.matches[p.selection].command
		p.close()
		return paletteSelection{event: paletteSelected, command: command}, nil
	}
	previous := p.input.Value()
	var command tea.Cmd
	p.input, command = p.input.Update(key)
	if previous != p.input.Value() {
		p.selection = 0
		p.refreshMatches()
	}
	return paletteSelection{}, command
}

func (m Model) paletteView() string {
	layout := newCockpitLayout(m.request.Width, m.request.Height)
	background := m.baseView()
	dimmed := lipgloss.NewStyle().Faint(true).Render(background)
	modal := m.paletteModal(layout)
	return overlayBlock(dimmed, modal, layout.width, layout.height)
}

func (m Model) commandOutputView() string {
	layout := newCockpitLayout(m.request.Width, m.request.Height)
	background := lipgloss.NewStyle().Faint(true).Render(m.baseView())
	width := min(84, max(46, layout.width-8))
	contentWidth := max(1, width-6)
	lines := strings.Split(strings.TrimSpace(m.commandOutput), "\n")
	maxLines := max(1, layout.height-8)
	truncated := len(lines) > maxLines
	if truncated {
		lines = lines[:maxLines]
	}
	for index, line := range lines {
		lines[index] = truncate(strings.ReplaceAll(line, "\t", "  "), contentWidth)
	}
	if truncated && len(lines) > 0 {
		lines[len(lines)-1] = truncate(lines[len(lines)-1], max(1, contentWidth-1)) + "…"
	}
	title := m.render.Palette.Header().Render("COMMAND RESULT")
	footer := m.render.Palette.Subtle().Render("Press any key to return")
	if m.commandOutputFailure {
		title = m.render.Palette.Warning().Render("COMMAND FAILED")
		footer = m.render.Palette.Warning().Render(wrapText(m.commandOutputHint, contentWidth)) + "\n" + footer
	}
	body := title + "\n\n" + strings.Join(lines, "\n") + "\n\n" + footer
	modal := m.render.Palette.Surface().
		Width(width-2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.AccentBorderColor()).
		Padding(0, 2).
		Render(body)
	return overlayBlock(background, modal, layout.width, layout.height)
}

func (m Model) paletteModal(layout cockpitLayout) string {
	width := paletteWidth(layout.width)
	rows := min(paletteVisibleRows, len(m.palette.matches))
	if rows == 0 {
		rows = 1
	}
	maxRows := max(1, layout.height-9)
	rows = min(rows, maxRows)

	start := 0
	if m.palette.selection >= rows {
		start = m.palette.selection - rows + 1
	}
	end := min(len(m.palette.matches), start+rows)

	var body strings.Builder
	input := m.palette.input
	input.Width = max(1, width-8)
	title := m.render.Palette.Header().Render("COMMAND PALETTE")
	body.WriteString(title)
	body.WriteString("  ")
	body.WriteString(m.render.Palette.Subtle().Render("ctrl+k to close"))
	body.WriteString("\n\n")
	body.WriteString(input.View())
	body.WriteByte('\n')
	body.WriteString(m.render.Palette.Border().Render(strings.Repeat("-", max(1, width-4))))
	body.WriteByte('\n')

	if len(m.palette.matches) == 0 {
		body.WriteString(m.render.Palette.Subtle().Render("No matching commands"))
	} else {
		for index := start; index < end; index++ {
			match := m.palette.matches[index]
			prefix := "  "
			style := m.render.Palette.Subtle()
			if index == m.palette.selection {
				prefix = "> "
				style = m.render.Palette.Emphasis().Bold(true)
			}
			label := truncate(match.command.title, width-8)
			descriptionWidth := max(1, width-lipgloss.Width(label)-8)
			description := truncate(match.command.description, descriptionWidth)
			line := prefix + style.Render(label)
			if description != "" {
				line += "  " + m.render.Palette.Subtle().Render(description)
			}
			body.WriteString(line)
			if index < end-1 {
				body.WriteByte('\n')
			}
		}
	}
	body.WriteString("\n\n")
	body.WriteString(m.render.Palette.Subtle().Render("up/down select  enter run  esc close"))

	return m.render.Palette.Surface().
		Width(width-2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.AccentBorderColor()).
		Padding(0, 2).
		Render(body.String())
}

func overlayBlock(background, foreground string, width, height int) string {
	background = fitBlock(background, width, height)
	backgroundLines := strings.Split(background, "\n")
	foregroundLines := strings.Split(strings.TrimSuffix(foreground, "\n"), "\n")
	foregroundWidth := 0
	for _, line := range foregroundLines {
		foregroundWidth = max(foregroundWidth, ansi.StringWidth(line))
	}
	foregroundWidth = min(foregroundWidth, width)
	startY := max(0, (height-len(foregroundLines))/2)
	startX := max(0, (width-foregroundWidth)/2)
	for index, foregroundLine := range foregroundLines {
		row := startY + index
		if row < 0 || row >= len(backgroundLines) {
			continue
		}
		foregroundLine = ansi.Truncate(foregroundLine, foregroundWidth, "")
		left := ansi.Cut(backgroundLines[row], 0, startX)
		right := ansi.Cut(backgroundLines[row], startX+foregroundWidth, width)
		backgroundLines[row] = left + foregroundLine + right
	}
	return strings.Join(backgroundLines, "\n")
}

func paletteActionFailure(command paletteCommand, err error) string {
	if err == nil {
		return ""
	}
	if command.invocation == "" {
		return fmt.Sprintf("%s failed: %v", command.title, err)
	}
	return fmt.Sprintf("%s failed; run `%s` for details", command.title, command.invocation)
}
