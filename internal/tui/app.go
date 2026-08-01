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

// MetricKind selects the value treatment for one summary metric.
type MetricKind uint8

const (
	MetricPlain MetricKind = iota
	MetricMoney
)

// SummaryMetric is one value in the one-line dashboard summary strip.
type SummaryMetric struct {
	Label string
	Value string
	Kind  MetricKind
}

// Summary contains the five values that orient every dashboard view.
type Summary struct {
	Metrics [5]SummaryMetric
}

// Snapshot is a fully rendered, immutable dashboard data result.
type Snapshot struct {
	Summary      Summary
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
	return m.cockpitView()
}

func (m Model) cockpitView() string {
	layout := newCockpitLayout(m.request.Width, m.request.Height)
	content := lipgloss.JoinHorizontal(lipgloss.Top,
		m.railView(layout),
		strings.Repeat(" ", gridGap),
		m.contentView(layout),
	)
	view := strings.Join([]string{
		m.topBarView(layout),
		m.summaryView(layout),
		content,
		m.footerView(layout),
	}, "\n")
	return frameBlock(view, layout)
}

func (m Model) topBarView(layout cockpitLayout) string {
	active := tabNames[min(int(m.tab), len(tabNames)-1)]
	left := m.render.Palette.Header().Render("tokenomnom") +
		m.render.Palette.Subtle().Render("  /  ") +
		m.render.Palette.Emphasis().Render(strings.ToUpper(active))
	right := m.render.Palette.Subtle().Render("LOCAL  ·  " + strings.ToUpper(m.request.Provider.String()) + "  ·  " + strings.ToUpper(m.request.Range.String()))
	space := max(2, layout.innerWidth-lipgloss.Width(left)-lipgloss.Width(right))
	return fitLine(left+strings.Repeat(" ", space)+right, layout.innerWidth)
}

func (m Model) summaryView(layout cockpitLayout) string {
	labels := [...]string{"TOTAL", "TOKENS", "ACTIVE DAYS", "AVG/DAY", "PEAK"}
	parts := make([]string, 0, len(labels))
	separator := m.render.Palette.Subtle().Render("  ·  ")
	for index, label := range labels {
		metric := m.snapshot.Summary.Metrics[index]
		if metric.Label == "" {
			metric.Label = label
		}
		if metric.Value == "" {
			metric.Value = "—"
		}
		parts = append(parts, m.render.Palette.Subtle().Render(metric.Label+" ")+m.summaryValueStyle(metric).Render(metric.Value))
	}
	return fitLine(strings.Join(parts, separator), layout.innerWidth)
}

func (m Model) summaryValueStyle(metric SummaryMetric) lipgloss.Style {
	if metric.Kind == MetricMoney {
		return m.render.Palette.Money().Bold(true)
	}
	return m.render.Palette.Emphasis().Bold(true)
}

func (m Model) railView(layout cockpitLayout) string {
	rows := []string{m.render.Palette.Header().Render("VIEWS"), ""}
	for tab := Tab(0); tab < tabCount; tab++ {
		label := fmt.Sprintf("%d  %s", tab+1, tabNames[tab])
		style := m.render.Palette.Subtle()
		if tab == m.tab {
			label = "› " + label
			style = m.render.Palette.Emphasis().Bold(true)
		}
		rows = append(rows, style.Render(label))
	}
	rows = append(rows, "", m.render.Palette.Header().Render("FILTERS"))
	rows = append(rows,
		m.render.Palette.Subtle().Render("provider"),
		m.filterProviderView(),
		m.render.Palette.Subtle().Render("range"),
		m.filterRangeView(),
	)

	innerWidth := max(1, layout.railWidth-1)
	content := fitBlock(strings.Join(rows, "\n"), innerWidth, layout.bodyHeight)
	lines := strings.Split(content, "\n")
	divider := m.render.Palette.Border().Render("│")
	for index := range lines {
		lines[index] += divider
	}
	return strings.Join(lines, "\n")
}

func (m Model) filterProviderView() string {
	value := m.request.Provider.String()
	if m.request.Provider == AllProviders {
		return m.render.Palette.Subtle().Render("  " + value)
	}
	return m.render.Palette.Provider(value, 0).Bold(true).Render("  " + value)
}

func (m Model) filterRangeView() string {
	value := m.request.Range.String()
	style := m.render.Palette.Subtle()
	if m.request.Range != Range30Days {
		style = m.render.Palette.Emphasis().Bold(true)
	}
	return style.Render("  " + value)
}

func (m Model) contentView(layout cockpitLayout) string {
	body := ""
	if int(m.tab) < len(m.snapshot.Views) {
		body = m.snapshot.Views[m.tab]
	}
	body = fitBlock(body, layout.paneWidth, layout.bodyHeight)
	return lipgloss.NewStyle().Width(layout.paneWidth).Height(layout.bodyHeight).Render(body)
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

var footerHints = [...][2]string{
	{"tab", "views"}, {"p", "provider"}, {"r", "range"},
	{"R", "refresh"}, {"?", "help"}, {"q", "quit"},
}

func (m Model) footerHint(hint [2]string) string {
	key := m.render.Palette.Header().Bold(false).Render(hint[0])
	if hint[1] == "" {
		return key
	}
	return key + " " + m.render.Palette.Subtle().Render(hint[1])
}

func (m Model) footerHintsView(width int) string {
	subtle := m.render.Palette.Subtle()
	parts := make([]string, 0, len(footerHints))
	for _, hint := range footerHints {
		parts = append(parts, m.footerHint(hint))
	}
	line := strings.Join(parts, subtle.Render(" · "))
	if lipgloss.Width(line) <= width {
		return line
	}

	// Keep every command visible at the minimum cockpit width while shortening
	// only the descriptions that cannot fit beside them.
	compactHints := [...][2]string{
		{"tab", "views"}, {"p", "prov"}, {"r", "range"},
		{"R", "refresh"}, {"?", "help"}, {"q", "quit"},
	}
	parts = parts[:0]
	for _, hint := range compactHints {
		parts = append(parts, m.footerHint(hint))
	}
	return strings.Join(parts, subtle.Render(" · "))
}

func (m Model) footerStatusView(status string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(status) <= width {
		return status
	}
	if width == 1 {
		return m.render.Palette.Subtle().Render("…")
	}
	return fitLine(status, width-1) + m.render.Palette.Subtle().Render("…")
}

func (m Model) footerView(layout cockpitLayout) string {
	subtle := m.render.Palette.Subtle()
	hints := m.footerHintsView(layout.innerWidth)
	line := hints
	disclaimer := subtle.Render("API list-price equivalents, not actual bills")
	status := m.statusView()
	const separatorWidth = 2
	remaining := layout.innerWidth - lipgloss.Width(hints) - separatorWidth
	if status != "" && remaining >= lipgloss.Width(status) {
		line += subtle.Render("  ") + m.footerStatusView(status, remaining)
	} else if status != "" {
		// Keep the command row intact; a status that cannot fit beside it takes
		// the disclaimer row instead of being hidden behind the line width.
		disclaimer = m.footerStatusView(status, layout.innerWidth)
	}
	return strings.Join([]string{
		m.render.Palette.Border().Render(strings.Repeat("─", layout.innerWidth)),
		fitLine(line, layout.innerWidth),
		fitLine(disclaimer, layout.innerWidth),
	}, "\n")
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
