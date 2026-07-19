// Package tui owns the interactive dashboard state machine.
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

const (
	minimumWidth  = 60
	minimumHeight = 18
)

// Tab identifies a dashboard view.
type Tab uint8

const (
	DailyTab Tab = iota
	MonthlyTab
	ModelsTab
	HeatmapTab
	tabCount
)

var tabNames = [...]string{"Daily", "Monthly", "Models", "Heatmap"}

// Provider is the dashboard-wide provider filter.
type Provider uint8

const (
	AllProviders Provider = iota
	CodexProvider
	ClaudeProvider
)

func (p Provider) String() string {
	return [...]string{"all", "codex", "claude"}[p]
}

// Range is the dashboard-wide date preset.
type Range uint8

const (
	Range30Days Range = iota
	Range90Days
	RangeYear
	RangeAll
)

func (r Range) String() string {
	return [...]string{"30d", "90d", "1y", "all"}[r]
}

// Request describes the data and render state needed for one snapshot.
type Request struct {
	Provider      Provider
	Range         Range
	Width         int
	Height        int
	DailyOffset   int
	MonthlyOffset int
	ModelOffset   int
	ModelSort     int
	HeatmapOffset int
	HeatmapYear   bool
	Sync          bool
}

// Card is one header value.
type Card struct {
	Label string
	Value string
}

// Snapshot is a fully rendered, immutable dashboard data result.
type Snapshot struct {
	Cards        [4]Card
	Views        [4]string
	Empty        bool
	FilesScanned int
	SyncDuration time.Duration
}

// Loader performs all store and sync I/O outside the Bubble Tea update loop.
type Loader func(Request) (Snapshot, error)

type loadedMsg struct {
	request  Request
	snapshot Snapshot
	err      error
}

// Model is the pure dashboard state machine.
type Model struct {
	render   theme.Context
	loader   Loader
	spinner  spinner.Model
	request  Request
	snapshot Snapshot
	tab      Tab
	help     bool
	loading  bool
	syncing  bool
	loaded   bool
	started  time.Time
	status   string
	warning  string
}

// New creates a dashboard model. The first snapshot loads in Init.
func New(render theme.Context, loader Loader) Model {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = render.Palette.Emphasis()
	return Model{
		render: render, loader: loader, spinner: spin,
		request: Request{Provider: AllProviders, Range: Range30Days, Width: render.Width},
		loading: true, started: time.Now(),
	}
}

// Init starts the initial store load.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCmd(m.request))
}

func (m Model) loadCmd(request Request) tea.Cmd {
	return func() tea.Msg {
		if m.loader == nil {
			return loadedMsg{request: request, err: fmt.Errorf("dashboard loader is unavailable")}
		}
		snapshot, err := m.loader(request)
		return loadedMsg{request: request, snapshot: snapshot, err: err}
	}
}

// Update handles navigation and background snapshot results.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.request.Width, m.request.Height = msg.Width, msg.Height
		m.render.Width = msg.Width
		if msg.Width >= minimumWidth && msg.Height >= minimumHeight {
			return m, m.loadCmd(m.request)
		}
		return m, nil
	case spinner.TickMsg:
		var command tea.Cmd
		m.spinner, command = m.spinner.Update(msg)
		return m, command
	case loadedMsg:
		if msg.err != nil {
			m.loading, m.syncing = false, false
			m.warning = msg.err.Error()
			return m, nil
		}
		initial := !m.loaded
		m.snapshot = msg.snapshot
		m.loading = false
		m.loaded = true
		m.warning = ""
		if msg.request.Sync {
			m.syncing = false
			m.status = fmt.Sprintf("synced · %s ago", shortAge(0))
			return m, nil
		}
		if !initial {
			return m, nil
		}
		// Render stored data immediately, then quietly refresh it. Empty stores
		// keep the progress view up until this initial sync completes.
		m.syncing = true
		if msg.snapshot.Empty {
			m.loading = true
		}
		next := m.request
		next.Sync = true
		return m, m.loadCmd(next)
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	value := key.String()
	if value == "ctrl+c" || value == "q" {
		return m, tea.Quit
	}
	if value == "?" {
		m.help = !m.help
		return m, nil
	}
	if m.help {
		return m, nil
	}
	switch value {
	case "tab":
		m.tab = (m.tab + 1) % tabCount
		return m, nil
	case "shift+tab":
		m.tab = (m.tab + tabCount - 1) % tabCount
		return m, nil
	case "1", "2", "3", "4":
		m.tab = Tab(value[0] - '1')
		return m, nil
	case "p":
		m.request.Provider = (m.request.Provider + 1) % 3
		return m, m.loadCmd(m.request)
	case "r":
		m.request.Range = (m.request.Range + 1) % 4
		return m, m.loadCmd(m.request)
	case "R":
		m.syncing = true
		request := m.request
		request.Sync = true
		return m, m.loadCmd(request)
	case "s":
		if m.tab == ModelsTab {
			m.request.ModelSort = (m.request.ModelSort + 1) % 3
			m.request.ModelOffset = 0
			return m, m.loadCmd(m.request)
		}
	case "y":
		if m.tab == HeatmapTab {
			m.request.HeatmapYear = !m.request.HeatmapYear
			return m, m.loadCmd(m.request)
		}
	case "left":
		m.pan(-1)
		return m, m.loadCmd(m.request)
	case "right":
		m.pan(1)
		return m, m.loadCmd(m.request)
	case "up":
		if m.tab == ModelsTab && m.request.ModelOffset > 0 {
			m.request.ModelOffset--
			return m, m.loadCmd(m.request)
		}
	case "down":
		if m.tab == ModelsTab {
			m.request.ModelOffset++
			return m, m.loadCmd(m.request)
		}
	case "home":
		m.setOffset(-1000000)
		return m, m.loadCmd(m.request)
	case "end":
		m.setOffset(0)
		return m, m.loadCmd(m.request)
	}
	return m, nil
}

func (m *Model) pan(direction int) {
	switch m.tab {
	case DailyTab:
		m.request.DailyOffset += direction * 7
	case MonthlyTab:
		m.request.MonthlyOffset += direction
	case HeatmapTab:
		m.request.HeatmapYear = false
		m.request.HeatmapOffset += direction
	}
}

func (m *Model) setOffset(value int) {
	switch m.tab {
	case DailyTab:
		m.request.DailyOffset = value
	case MonthlyTab:
		m.request.MonthlyOffset = value
	case ModelsTab:
		m.request.ModelOffset = max(0, value)
	}
}

// View renders the current immutable model state.
func (m Model) View() string {
	if m.request.Width > 0 && m.request.Height > 0 && (m.request.Width < minimumWidth || m.request.Height < minimumHeight) {
		return "terminal too small\n"
	}
	if m.help {
		return m.helpView()
	}
	if m.loading {
		elapsed := time.Since(m.started).Round(time.Second)
		return fmt.Sprintf("%s Syncing Codex + Claude · %d files scanned · %s\n", m.spinner.View(), m.snapshot.FilesScanned, elapsed)
	}
	var output strings.Builder
	output.WriteString(m.cardsView())
	output.WriteString(m.tabsView())
	output.WriteString(m.snapshot.Views[m.tab])
	if !strings.HasSuffix(m.snapshot.Views[m.tab], "\n") {
		output.WriteByte('\n')
	}
	output.WriteString(m.footerView())
	return output.String()
}

func (m Model) cardsView() string {
	width := max(12, (m.request.Width-8)/4)
	parts := make([]string, 0, len(m.snapshot.Cards))
	for _, card := range m.snapshot.Cards {
		value := truncate(card.Value, width-2)
		parts = append(parts, m.render.Palette.Header().Width(width).Border(lipgloss.NormalBorder()).Render(card.Label+"\n"+value))
	}
	filters := m.render.Palette.Subtle().Render(fmt.Sprintf("provider: %s · range: %s", m.request.Provider, m.request.Range))
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...) + "\n" + filters + "\n\n"
}

func (m Model) tabsView() string {
	parts := make([]string, 0, tabCount)
	for tab := Tab(0); tab < tabCount; tab++ {
		style := m.render.Palette.Subtle().Padding(0, 1)
		if tab == m.tab {
			style = m.render.Palette.Emphasis().Underline(true).Padding(0, 1)
		}
		parts = append(parts, style.Render(tabNames[tab]))
	}
	return strings.Join(parts, "") + "\n\n"
}

func (m Model) footerView() string {
	status := m.status
	if m.syncing {
		status = "syncing"
	}
	if m.warning != "" {
		status = m.render.Palette.Warning().Render(m.warning)
	}
	line := "tab views · p provider · r range · R refresh · ? help · q quit"
	if status != "" {
		line += " · " + status
	}
	return "\n" + m.render.Palette.Subtle().Render(line) + "\n" +
		m.render.Palette.Subtle().Render("API list-price equivalents, not actual bills") + "\n"
}

func (m Model) helpView() string {
	return "Keys\n\n" +
		"tab / shift+tab / 1-4  switch view\n" +
		"left / right            pan active timeline\n" +
		"home / end              jump to range edge\n" +
		"up / down               scroll models\n" +
		"s                       sort models\n" +
		"y                       calendar-year heatmap\n" +
		"p                       cycle provider\n" +
		"r                       cycle range\n" +
		"R                       refresh now\n" +
		"?                       close help\n" +
		"q / ctrl+c              quit\n"
}

func truncate(value string, width int) string {
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func shortAge(duration time.Duration) string {
	if duration < time.Minute {
		return "0s"
	}
	return duration.Round(time.Minute).String()
}
