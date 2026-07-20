// Package tui owns the interactive dashboard state machine.
package tui

import (
	"fmt"
	"os"
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

// CardKind selects the value treatment for one header card.
type CardKind uint8

const (
	CardPlain CardKind = iota
	CardMoney
	CardModel
)

// Card is one header value.
type Card struct {
	Label    string
	Value    string
	Kind     CardKind
	Provider string // provider hue for CardModel values
}

// contentMaxWidth bounds the dashboard column: wide enough for a full-year
// heatmap at two-column cells, narrow enough to stay scannable on wide
// terminals instead of smearing content across them.
const contentMaxWidth = 112

// ContentWidth returns the bounded column width for a terminal width.
func ContentWidth(width int) int {
	if width <= 0 {
		return contentMaxWidth
	}
	return max(minimumWidth-4, min(width-2, contentMaxWidth))
}

// Snapshot is a fully rendered, immutable dashboard data result.
type Snapshot struct {
	Cards        [4]Card
	Views        [4]string
	Empty        bool
	FilesScanned int
	SyncDuration time.Duration
	Warning      string
}

// Loader performs all store and sync I/O outside the Bubble Tea update loop.
type Loader func(Request) (Snapshot, error)

// SkillOfferCheck describes whether the one-time skill offer is relevant.
type SkillOfferCheck struct {
	Answered  bool
	HasRoots  bool
	Installed bool
}

// SkillOfferChoice is a persisted answer to the one-time skill offer.
type SkillOfferChoice uint8

const (
	SkillOfferAccepted SkillOfferChoice = iota + 1
	SkillOfferDeclined
	SkillOfferPreinstalled
)

// SkillOffer keeps dashboard interaction pure while the CLI adapter owns I/O.
type SkillOffer struct {
	Check   func() (SkillOfferCheck, error)
	Install func() ([]string, error)
	Record  func(SkillOfferChoice) error
}

type loadedMsg struct {
	request  Request
	snapshot Snapshot
	err      error
}

type skillOfferCheckedMsg struct {
	check SkillOfferCheck
	err   error
}

type skillOfferInstalledMsg struct {
	results []string
	err     error
}

type skillOfferRecordedMsg struct{ err error }

type skillOfferState uint8

const (
	skillOfferHidden skillOfferState = iota
	skillOfferPrompt
	skillOfferInstalling
	skillOfferResult
)

// Model is the pure dashboard state machine.
type Model struct {
	render       theme.Context
	loader       Loader
	offer        SkillOffer
	spinner      spinner.Model
	request      Request
	snapshot     Snapshot
	tab          Tab
	help         bool
	loading      bool
	syncing      bool
	loaded       bool
	started      time.Time
	status       string
	warning      string
	offerState   skillOfferState
	offerChecked bool
	offerResults []string
	pendingSync  bool
}

// New creates a dashboard model. The first snapshot loads in Init.
func New(render theme.Context, loader Loader, offer SkillOffer) Model {
	return NewWithProvider(render, loader, offer, AllProviders)
}

// NewWithProvider creates a dashboard model with an initial provider filter.
func NewWithProvider(render theme.Context, loader Loader, offer SkillOffer, provider Provider) Model {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = render.Palette.Emphasis()
	return Model{
		render: render, loader: loader, offer: offer, spinner: spin,
		request: Request{Provider: provider, Range: Range30Days, Width: render.Width},
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

func (m Model) checkSkillOfferCmd() tea.Cmd {
	return func() tea.Msg {
		if m.offer.Check == nil {
			return skillOfferCheckedMsg{}
		}
		check, err := m.offer.Check()
		return skillOfferCheckedMsg{check: check, err: err}
	}
}

func (m Model) installSkillCmd() tea.Cmd {
	return func() tea.Msg {
		if m.offer.Install == nil {
			return skillOfferInstalledMsg{err: fmt.Errorf("skill installer is unavailable")}
		}
		results, err := m.offer.Install()
		return skillOfferInstalledMsg{results: results, err: err}
	}
}

func (m Model) recordSkillOfferCmd(choice SkillOfferChoice) tea.Cmd {
	return func() tea.Msg {
		if m.offer.Record == nil {
			return skillOfferRecordedMsg{}
		}
		return skillOfferRecordedMsg{err: m.offer.Record(choice)}
	}
}

func (m *Model) maybeCheckSkillOffer() tea.Cmd {
	if m.offerChecked || m.offer.Check == nil {
		return nil
	}
	m.offerChecked = true
	return m.checkSkillOfferCmd()
}

func (m *Model) resumeInitialSync() tea.Cmd {
	if !m.pendingSync {
		return nil
	}
	m.pendingSync = false
	next := m.request
	next.Sync = true
	return m.loadCmd(next)
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
		m.warning = msg.snapshot.Warning
		if msg.request.Sync {
			m.syncing = false
			m.status = fmt.Sprintf("synced · %s ago", shortAge(0))
			return m, m.maybeCheckSkillOffer()
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
		if msg.snapshot.Empty {
			return m, m.loadCmd(next)
		}
		checkCommand := m.maybeCheckSkillOffer()
		if checkCommand == nil {
			return m, m.loadCmd(next)
		}
		m.pendingSync = true
		return m, checkCommand
	case skillOfferCheckedMsg:
		if msg.err != nil || msg.check.Answered || !msg.check.HasRoots {
			return m, m.resumeInitialSync()
		}
		if msg.check.Installed {
			return m, m.recordSkillOfferCmd(SkillOfferPreinstalled)
		}
		m.offerState = skillOfferPrompt
		return m, nil
	case skillOfferInstalledMsg:
		m.offerState = skillOfferResult
		m.offerResults = append([]string(nil), msg.results...)
		if msg.err != nil {
			m.offerResults = append(m.offerResults, "Install failed: "+msg.err.Error())
		}
		if len(m.offerResults) == 0 {
			m.offerResults = []string{"No agent skills were changed."}
		}
		return m, m.recordSkillOfferCmd(SkillOfferAccepted)
	case skillOfferRecordedMsg:
		// Offer bookkeeping is intentionally best effort and never blocks the TUI.
		return m, m.resumeInitialSync()
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	value := key.String()
	if m.offerState != skillOfferHidden {
		return m.updateSkillOfferKey(value)
	}
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

func (m Model) updateSkillOfferKey(value string) (tea.Model, tea.Cmd) {
	switch m.offerState {
	case skillOfferPrompt:
		switch value {
		case "y", "Y":
			m.offerState = skillOfferInstalling
			return m, m.installSkillCmd()
		case "n", "N", "esc", "enter":
			m.offerState = skillOfferHidden
			m.status = "skill not installed — run 'tokenomnom install-skill' anytime"
			return m, m.recordSkillOfferCmd(SkillOfferDeclined)
		case "q", "ctrl+c":
			return m, m.declineSkillOfferAndQuitCmd()
		}
	case skillOfferInstalling:
		return m, nil
	case skillOfferResult:
		m.offerState = skillOfferHidden
		return m, nil
	}
	return m, nil
}

func (m Model) declineSkillOfferAndQuitCmd() tea.Cmd {
	return func() tea.Msg {
		if m.offer.Record != nil {
			_ = m.offer.Record(SkillOfferDeclined)
		}
		return tea.Quit()
	}
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
		return m.place(m.render.Palette.Subtle().Render("terminal too small") + "\n")
	}
	if m.offerState != skillOfferHidden {
		return m.skillOfferView()
	}
	if m.help {
		return m.helpView()
	}
	if m.loading {
		elapsed := time.Since(m.started).Round(time.Second)
		line := fmt.Sprintf("%s Syncing Codex + Claude · %d files scanned · %s\n", m.spinner.View(), m.snapshot.FilesScanned, elapsed)
		return m.place(line)
	}
	body := m.snapshot.Views[m.tab]
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	column := m.cardsView() + m.tabsView() + body
	footer := m.footerView()
	return m.compose(column, footer)
}

// compose pins the footer to the bottom edge and centers the bounded column.
func (m Model) compose(column, footer string) string {
	width := max(m.request.Width, minimumWidth)
	if m.request.Height > 0 {
		filler := m.request.Height - lipgloss.Height(column) - lipgloss.Height(footer)
		if filler > 0 {
			column += strings.Repeat("\n", filler)
		}
	}
	view := column + footer
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, lipgloss.NewStyle().Width(ContentWidth(m.request.Width)).Render(view))
}

// place centers transient states (loading, too-small) in the full window.
func (m Model) place(content string) string {
	return lipgloss.Place(max(m.request.Width, minimumWidth), max(m.request.Height, minimumHeight), lipgloss.Center, lipgloss.Center, content)
}

func (m Model) skillOfferView() string {
	width := min(68, max(40, m.request.Width-8))
	contentWidth := width - 4
	var body strings.Builder
	switch m.offerState {
	case skillOfferPrompt:
		body.WriteString(m.render.Palette.Header().Render("Teach your agents to use tokenomnom?"))
		body.WriteString("\n\n")
		body.WriteString(wrapText("Installs an agent skill into the skills directory of your detected coding agents (~/.claude, ~/.codex) so they can answer token-spend questions themselves.", contentWidth))
		body.WriteString("\n\n")
		body.WriteString(wrapText("Opt-in either way: install later anytime with `tokenomnom install-skill`, remove anytime with `tokenomnom install-skill --remove`.", contentWidth))
		body.WriteString("\n\n")
		body.WriteString(m.render.Palette.Emphasis().Render("[y] install   [n] not now"))
		body.WriteByte('\n')
		body.WriteString(m.render.Palette.Subtle().Render("(this prompt appears only once)"))
	case skillOfferInstalling:
		body.WriteString(m.render.Palette.Header().Render("Installing agent skill"))
		body.WriteString("\n\n")
		body.WriteString(m.spinner.View() + " Checking detected coding agents...")
	case skillOfferResult:
		body.WriteString(m.render.Palette.Header().Render("Agent skill results"))
		body.WriteString("\n\n")
		for index, result := range m.offerResults {
			if index > 0 {
				body.WriteByte('\n')
			}
			body.WriteString(m.skillResultView(result, contentWidth))
		}
		body.WriteString("\n\n")
		body.WriteString(m.render.Palette.Subtle().Render("Press any key to return to the dashboard"))
	}
	modal := lipgloss.NewStyle().Width(width).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.AccentBorderColor()).
		Padding(0, 2).Render(body.String())
	return m.place(modal)
}

// skillResultView styles one "Provider: action · path" install-result line,
// falling back to a plain wrap for anything shaped differently.
func (m Model) skillResultView(result string, width int) string {
	provider, rest, hasProvider := strings.Cut(result, ": ")
	if !hasProvider || (provider != "Codex" && provider != "Claude") {
		return wrapText(result, width)
	}
	action, path, hasPath := strings.Cut(rest, " · ")
	actionStyle := m.render.Palette.Success()
	switch {
	case strings.HasPrefix(action, "skipped"), strings.HasPrefix(action, "refused"):
		actionStyle = m.render.Palette.Warning()
	case strings.HasPrefix(action, "removed"):
		actionStyle = m.render.Palette.Subtle()
	}
	line := m.render.Palette.Provider(strings.ToLower(provider), 0).Bold(true).Render(provider) +
		m.render.Palette.Subtle().Render(": ") + actionStyle.Render(action)
	if !hasPath {
		return line
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, home) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	return line + "\n" + m.render.Palette.Subtle().Render("  "+truncate(path, width-2))
}

func wrapText(value string, width int) string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return ""
	}
	lines := []string{}
	current := ""
	for _, word := range words {
		for lipgloss.Width(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			chunk, rest := splitTextWidth(word, width)
			lines = append(lines, chunk)
			word = rest
		}
		if current == "" {
			current = word
		} else if lipgloss.Width(current)+1+lipgloss.Width(word) <= width {
			current += " " + word
		} else {
			lines = append(lines, current)
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return strings.Join(lines, "\n")
}

func splitTextWidth(value string, width int) (string, string) {
	runes := []rune(value)
	end := 0
	for end < len(runes) && lipgloss.Width(string(runes[:end+1])) <= width {
		end++
	}
	return string(runes[:end]), string(runes[end:])
}

const cardGap = 2

func (m Model) cardsView() string {
	content := ContentWidth(m.request.Width)
	width := max(14, (content-(len(m.snapshot.Cards)-1)*cardGap)/len(m.snapshot.Cards))
	parts := make([]string, 0, len(m.snapshot.Cards))
	for index, card := range m.snapshot.Cards {
		inner := width - 4 // border + padding
		value := truncate(card.Value, inner)
		label := m.render.Palette.Subtle().Render(truncate(card.Label, inner))
		body := label + "\n" + m.cardValueStyle(card).Render(value)
		style := m.render.Palette.Border().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(m.render.Palette.BorderColor()).
			Padding(0, 1).Width(width - 2)
		if index < len(m.snapshot.Cards)-1 {
			style = style.MarginRight(cardGap)
		}
		parts = append(parts, style.Render(body))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, parts...) + "\n" + m.filtersView() + "\n\n"
}

func (m Model) cardValueStyle(card Card) lipgloss.Style {
	switch card.Kind {
	case CardMoney:
		return m.render.Palette.Money().Bold(true)
	case CardModel:
		if card.Provider != "" {
			return m.render.Palette.Provider(card.Provider, 0).Bold(true)
		}
	}
	return m.render.Palette.Header()
}

// filtersView dims default filter values and lifts active ones.
func (m Model) filtersView() string {
	provider := m.render.Palette.Subtle().Render(m.request.Provider.String())
	if m.request.Provider != AllProviders {
		provider = m.render.Palette.Provider(m.request.Provider.String(), 0).Bold(true).Render(m.request.Provider.String())
	}
	dateRange := m.render.Palette.Subtle().Render(m.request.Range.String())
	if m.request.Range != RangeAll && m.request.Range != Range30Days {
		dateRange = m.render.Palette.Emphasis().Render(m.request.Range.String())
	}
	subtle := m.render.Palette.Subtle()
	return subtle.Render("provider ") + provider + subtle.Render("  ·  range ") + dateRange
}

func (m Model) tabsView() string {
	parts := make([]string, 0, tabCount)
	for tab := Tab(0); tab < tabCount; tab++ {
		style := m.render.Palette.Subtle().Padding(0, 1)
		if tab == m.tab {
			style = m.render.Palette.Emphasis().Underline(true).Bold(true).Padding(0, 1)
		}
		parts = append(parts, style.Render(tabNames[tab]))
	}
	rule := m.render.Palette.Border().Render(strings.Repeat("─", ContentWidth(m.request.Width)))
	return strings.Join(parts, " ") + "\n" + rule + "\n\n"
}

var footerHints = [...][2]string{
	{"tab", "views"}, {"p", "provider"}, {"r", "range"},
	{"R", "refresh"}, {"?", "help"}, {"q", "quit"},
}

func (m Model) footerView() string {
	subtle := m.render.Palette.Subtle()
	parts := make([]string, 0, len(footerHints))
	for _, hint := range footerHints {
		parts = append(parts, m.render.Palette.Header().Bold(false).Render(hint[0])+" "+subtle.Render(hint[1]))
	}
	line := strings.Join(parts, subtle.Render(" · "))
	status := m.statusView()
	switch {
	case status == "":
	case lipgloss.Width(line)+lipgloss.Width(status)+3 <= ContentWidth(m.request.Width):
		line += subtle.Render(" · ") + status
	default:
		line = status + "\n" + line
	}
	return "\n" + line + "\n" + subtle.Render("API list-price equivalents, not actual bills") + "\n"
}

func (m Model) statusView() string {
	if m.warning != "" {
		return m.render.Palette.Warning().Render(m.warning)
	}
	status := ""
	if m.status != "" {
		status = m.render.Palette.Success().Render(m.status)
	}
	if m.syncing {
		syncing := m.spinner.View() + m.render.Palette.Subtle().Render(" syncing")
		if status == "" {
			return syncing
		}
		return status + m.render.Palette.Subtle().Render(" · ") + syncing
	}
	return status
}

var helpRows = [...][2]string{
	{"tab / shift+tab / 1-4", "switch view"},
	{"← / →", "pan active timeline"},
	{"home / end", "jump to range edge"},
	{"↑ / ↓", "scroll models"},
	{"s", "sort models"},
	{"y", "calendar-year heatmap"},
	{"p", "cycle provider"},
	{"r", "cycle range"},
	{"R", "refresh now"},
	{"?", "close help"},
	{"q / ctrl+c", "quit"},
}

func (m Model) helpView() string {
	keyWidth := 0
	for _, row := range helpRows {
		keyWidth = max(keyWidth, lipgloss.Width(row[0]))
	}
	var body strings.Builder
	body.WriteString(m.render.Palette.Header().Render("Keys"))
	body.WriteString("\n\n")
	for _, row := range helpRows {
		key := row[0] + strings.Repeat(" ", keyWidth-lipgloss.Width(row[0]))
		body.WriteString(m.render.Palette.Emphasis().Render(key))
		body.WriteString("   ")
		body.WriteString(m.render.Palette.Subtle().Render(row[1]))
		body.WriteByte('\n')
	}
	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.render.Palette.BorderColor()).
		Padding(0, 2).Render(strings.TrimRight(body.String(), "\n"))
	return m.place(modal)
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
