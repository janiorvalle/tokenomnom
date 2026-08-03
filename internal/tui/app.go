// Package tui owns the interactive dashboard state machine.
package tui

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

const (
	minimumWidth  = 60
	minimumHeight = 18
)

// Tab identifies a dashboard view.
type Tab uint8

const (
	DailyTab Tab = iota
	LedgerTab
	ModelsTab
	HeatmapTab
	tabCount
)

// MonthlyTab is retained as an index alias for callers that still refer to
// the pre-ledger dashboard layout.
const MonthlyTab = LedgerTab

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
	Provider Provider
	Range    Range
	Width    int
	Height   int
	Ledger   tuipages.State
	// DailyCursor is the number of active daily bars to move back from the
	// newest active day. Zero always means the newest active day.
	DailyCursor          int
	DailyWindowStart     int
	DailyDetailOffset    int
	MonthlyOffset        int
	ModelOffset          int
	ModelSort            int
	HeatmapOffset        int
	HeatmapYear          bool
	SessionProject       string
	SessionProjectActive bool
	SessionCursor        string
	SessionCursorStack   string
	SessionOffset        int
	SessionReturnToEnd   bool
	SessionDetailID      string
	SessionDetailOffset  int
	VaultOffset          int
	SystemOffset         int
	RefreshPages         bool
	Sync                 bool
	Action               string
	LoadID               uint64
	PageLoadToken        string
	// Initial marks the store-only load used to make the first dashboard frame.
	Initial            bool
	HistoryQuery       string
	HistorySelect      int
	HistorySessionID   string
	HistoryExportID    string
	HistoryExportToken string
	FullSync           bool
	Progress           *LoadProgressReporter
}

// LoadProgress is the user-facing progress reported by a dashboard loader.
// The callback stays on Request so existing loaders can opt into streaming
// status without changing the Loader function signature.
type LoadProgress struct {
	Phase          string
	FilesFound     int
	FilesProcessed int
}

// LoadProgressReporter receives progress updates from a dashboard loader.
type LoadProgressReporter func(LoadProgress)

// ProgressSink delivers background loader progress to the running TUI.
type ProgressSink func(Request, uint64, LoadProgress)

// ProgressMsg carries a loader update into the Bubble Tea event loop.
type ProgressMsg struct {
	Request    Request
	Generation uint64
	Progress   LoadProgress
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

// RailData is the bounded ambient context rendered beside every page. The
// loader supplies formatted snapshot values and normalized shares so the rail
// remains a pure view and never performs its own I/O.
type RailData struct {
	Snapshot RailSnapshot
	Mix      RailMix
	Projects []RailProject
}

type RailSnapshot struct {
	Today      string
	SevenDays  string
	ThirtyDays string
	Peak       string
	PeakDate   string
}

type RailMix struct {
	Codex  float64
	Claude float64
}

type RailProject struct {
	Label string
	Share float64
}

// Snapshot is a fully rendered, immutable dashboard data result.
type Snapshot struct {
	Summary   Summary
	Rail      RailData
	Views     [4]string
	Sessions  tuipages.SessionPageData
	StatusBar StatusBar
	Ledger    tuipages.Data
	Vault     tuipages.VaultPageData
	System    tuipages.SystemPageData
	// DailyCursor is the normalized distance from the newest active daily bar.
	DailyCursor          int
	DailyCursorMax       int
	DailyWindowStart     int
	DailyDetailOffset    int
	DailyDetailMaxOffset int
	Empty                bool
	FilesScanned         int
	SyncDuration         time.Duration
	Warning              string
	ActionStatus         string
	ActionWarning        string
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
	request    Request
	generation uint64
	snapshot   Snapshot
	err        error
}

type pageLoadedMsg struct {
	id      PageID
	request Request
	data    any
	err     error
}

type pageExportedMsg struct {
	id      PageID
	request Request
	path    string
	err     error
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

type commandFinishedMsg struct {
	command paletteCommand
	result  CommandResult
	err     error
}

type skillOfferState uint8

const (
	skillOfferHidden skillOfferState = iota
	skillOfferPrompt
	skillOfferInstalling
	skillOfferResult
)

// Model is the pure dashboard state machine.
type Model struct {
	render                   theme.Context
	loader                   Loader
	offer                    SkillOffer
	spinner                  spinner.Model
	router                   PageRouter
	request                  Request
	snapshot                 Snapshot
	help                     bool
	loading                  bool
	syncing                  bool
	syncFresh                bool
	loaded                   bool
	started                  time.Time
	status                   string
	warning                  string
	actionInFlight           string
	loadID                   uint64
	pendingAction            bool
	pendingActionStatus      string
	pendingActionWarning     string
	offerState               skillOfferState
	offerChecked             bool
	offerResults             []string
	pendingSync              bool
	syncCompletionPending    bool
	loadGeneration           uint64
	syncGeneration           uint64
	syncInFlight             bool
	syncCompletionGeneration uint64
	pageLoadAttempt          uint64
	pageLoadTokens           map[PageID]string
	commandRegistry          CommandRegistry
	palette                  paletteState
	commandBusy              bool
	commandOutput            string
	commandOutputOffset      int
	pendingResize            bool
	quitAfterCommand         bool
	commandOutputFailure     bool
	commandOutputHint        string
	dashboardLoadBusy        bool
	initialSnapshotPending   bool
	progress                 LoadProgress
	progressSink             ProgressSink
}

// New creates a dashboard model. The first snapshot loads in Init.
func New(render theme.Context, loader Loader, offer SkillOffer, registries ...CommandRegistry) Model {
	return NewWithProvider(render, loader, offer, AllProviders, registries...)
}

// NewWithProvider creates a dashboard model with an initial provider filter.
func NewWithProvider(render theme.Context, loader Loader, offer SkillOffer, provider Provider, registries ...CommandRegistry) Model {
	model := newModel(render, loader, offer, provider)
	model.commandRegistry = mergeCommandRegistries(registries...)
	return model
}

// NewWithPages creates a dashboard with additional registered pages.
func NewWithPages(render theme.Context, loader Loader, offer SkillOffer, pages ...Page) Model {
	return NewWithProviderAndPages(render, loader, offer, AllProviders, pages...)
}

// NewWithProviderAndPages creates a dashboard with additional registered
// pages. The default spend pages remain first so existing numeric navigation
// keeps its meaning while later sections can be supplied by their owners.
func NewWithProviderAndPages(render theme.Context, loader Loader, offer SkillOffer, provider Provider, pages ...Page) Model {
	return newModel(render, loader, offer, provider, pages...)
}

// NewWithProviderAndPagesAndCommands combines page and command registration
// for dashboard adapters that own both extension points.
func NewWithProviderAndPagesAndCommands(render theme.Context, loader Loader, offer SkillOffer, provider Provider, registry CommandRegistry, pages ...Page) Model {
	model := newModel(render, loader, offer, provider, pages...)
	model.commandRegistry = registry
	return model
}

func newModel(render theme.Context, loader Loader, offer SkillOffer, provider Provider, pages ...Page) Model {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = render.Palette.Emphasis()
	return Model{
		render: render, loader: loader, offer: offer, spinner: spin,
		router:  newRouter(pages...),
		palette: newPalette(render.Palette.Emphasis()),
		request: Request{Provider: provider, Range: Range30Days, Width: render.Width, RefreshPages: true, Initial: true, Ledger: tuipages.State{Cursor: -1}},
		loading: true, dashboardLoadBusy: true, started: time.Now(), progress: LoadProgress{Phase: "starting sync"},
		pageLoadTokens: make(map[PageID]string),
	}
}

func mergeCommandRegistries(registries ...CommandRegistry) CommandRegistry {
	merged := CommandRegistry{}
	for _, registry := range registries {
		merged.Actions = append(merged.Actions, registry.Actions...)
	}
	return merged
}

// SetProgressSink connects loader progress to the running TUI program.
func (m *Model) SetProgressSink(sink ProgressSink) {
	m.progressSink = sink
}

// Init starts the initial store load.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.loadCmdAt(m.request, m.loadGeneration))
}

func (m *Model) loadCmd(request Request) tea.Cmd {
	m.loadGeneration++
	if m.syncCompletionPending && m.loadGeneration != m.syncCompletionGeneration {
		m.syncCompletionPending = false
	}
	if request.Sync {
		m.syncGeneration = m.loadGeneration
		m.syncInFlight = true
	}
	return m.loadCmdAt(request, m.loadGeneration)
}

func (m *Model) startDashboardLoad(request Request) tea.Cmd {
	if request.Action != "" {
		m.clearPendingActionOutcome()
	} else if request.Sync {
		m.clearPendingActionOutcome()
	}
	if request.Sync {
		m.progress = LoadProgress{Phase: "discovering files"}
	}
	request.Initial = false
	m.loadID++
	request.LoadID = m.loadID
	m.request.LoadID = request.LoadID
	m.request.Initial = false
	m.dashboardLoadBusy = true
	return m.loadCmd(request)
}

func (m *Model) startInitialStoreLoad(request Request) tea.Cmd {
	request.Initial = true
	request.Sync = false
	request.LoadID = 0
	m.request.Initial = true
	m.request.LoadID = 0
	m.dashboardLoadBusy = true
	return m.loadCmd(request)
}

func (m *Model) finishAction(request Request, snapshot Snapshot) {
	m.actionInFlight = ""
	m.request.Action = ""
	if request.Sync && request.LoadID == m.loadID {
		m.syncInFlight = false
	}
	if !m.syncInFlight {
		m.syncing = false
	}
	m.status = snapshot.ActionStatus
	if snapshot.ActionWarning != "" {
		m.status = ""
		m.warning = snapshot.ActionWarning
	}
}

func (m *Model) queueActionOutcome(snapshot Snapshot) {
	m.pendingAction = true
	m.pendingActionStatus = snapshot.ActionStatus
	m.pendingActionWarning = snapshot.ActionWarning
}

func (m *Model) clearPendingActionOutcome() {
	m.pendingAction = false
	m.pendingActionStatus = ""
	m.pendingActionWarning = ""
}

func (m *Model) applyPendingActionOutcome() {
	if !m.pendingAction {
		return
	}
	m.status = m.pendingActionStatus
	if m.pendingActionWarning != "" {
		m.status = ""
		m.warning = m.pendingActionWarning
	}
	m.clearPendingActionOutcome()
}

func (m *Model) refreshCurrentSnapshotWithActionOutcome(snapshot Snapshot) tea.Cmd {
	next := m.request
	next.Action = ""
	next.Sync = false
	next.RefreshPages = true
	if next.Width < minimumWidth || next.Height < minimumHeight {
		m.clearPendingActionOutcome()
		return nil
	}
	m.queueActionOutcome(snapshot)
	return m.startDashboardLoad(next)
}

func (m Model) loadCmdAt(request Request, generation uint64) tea.Cmd {
	if m.loader == nil {
		return func() tea.Msg {
			return loadedMsg{request: request, generation: generation, err: fmt.Errorf("dashboard loader is unavailable")}
		}
	}
	if !request.Sync {
		return func() tea.Msg {
			snapshot, err := m.loader(request)
			return loadedMsg{request: request, generation: generation, snapshot: snapshot, err: err}
		}
	}
	if request.Sync && m.progressSink != nil {
		reportProgress := LoadProgressReporter(func(update LoadProgress) {
			m.progressSink(request, generation, update)
		})
		request.Progress = &reportProgress
	}
	return func() tea.Msg {
		snapshot, err := m.loader(request)
		return loadedMsg{request: request, generation: generation, snapshot: snapshot, err: err}
	}
}

func (m Model) loadPageCmd(page PageLoader, request Request) tea.Cmd {
	return func() tea.Msg {
		data, err := page.Load(request)
		return pageLoadedMsg{id: page.ID(), request: request, data: data, err: err}
	}
}

func (m Model) startPageLoad(page PageLoader, request Request) (Model, tea.Cmd) {
	m.pageLoadAttempt++
	commandRequest := request
	commandRequest.PageLoadToken = strconv.FormatUint(m.pageLoadAttempt, 10)
	request.Sync = false
	if m.pageLoadTokens == nil {
		m.pageLoadTokens = make(map[PageID]string)
	}
	m.pageLoadTokens[page.ID()] = commandRequest.PageLoadToken
	if tracker, ok := page.(PageLoadTracker); ok {
		tracker.BeginLoad(commandRequest)
	}
	m.request = request
	m.request.PageLoadToken = ""
	return m, m.loadPageCmd(page, commandRequest)
}

func (m Model) exportPageCmd(page PageExporter, request Request) tea.Cmd {
	return func() tea.Msg {
		path, err := page.Export(request)
		return pageExportedMsg{id: page.ID(), request: request, path: path, err: err}
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
	if !m.pendingSync || m.commandBusy || m.dashboardLoadBusy {
		return nil
	}
	m.pendingSync = false
	m.pendingResize = false
	m.syncing = true
	next := m.request
	next.Sync = true
	next.RefreshPages = true
	return m.startDashboardLoad(next)
}

func (m *Model) resumePendingResize() tea.Cmd {
	if !m.pendingResize || m.commandBusy || m.syncing || m.dashboardLoadBusy {
		return nil
	}
	if m.request.Width < minimumWidth || m.request.Height < minimumHeight {
		m.pendingResize = false
		return nil
	}
	m.pendingResize = false
	return m.startDashboardLoad(m.request)
}

func (m *Model) resumePendingWork() tea.Cmd {
	if command := m.resumeInitialSync(); command != nil {
		return command
	}
	return m.resumePendingResize()
}

func combineCommands(commands ...tea.Cmd) tea.Cmd {
	available := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			available = append(available, command)
		}
	}
	switch len(available) {
	case 0:
		return nil
	case 1:
		return available[0]
	default:
		return tea.Batch(available...)
	}
}

// Update handles navigation and background snapshot results.
func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.request.Width, m.request.Height = msg.Width, msg.Height
		m.render.Width = msg.Width
		m.palette.resize(msg.Width)
		if msg.Width >= minimumWidth && msg.Height >= minimumHeight && m.initialSnapshotPending && !m.commandBusy && !m.syncing && !m.pendingSync && !m.dashboardLoadBusy {
			m.pendingResize = false
			return m, m.startInitialStoreLoad(m.request)
		}
		if msg.Width >= minimumWidth && msg.Height >= minimumHeight && !m.commandBusy && !m.syncing && !m.pendingSync && !m.dashboardLoadBusy {
			m.pendingResize = false
			return m, m.startDashboardLoad(m.request)
		}
		m.pendingResize = msg.Width >= minimumWidth && msg.Height >= minimumHeight
		return m, nil
	case spinner.TickMsg:
		var command tea.Cmd
		m.spinner, command = m.spinner.Update(msg)
		return m, command
	case ProgressMsg:
		if msg.Generation == m.loadGeneration && sameRequestIgnoringSync(msg.Request, m.request) {
			m.progress = msg.Progress
		}
		return m, nil
	case pageLoadedMsg:
		if m.pageLoadTokens[msg.id] != msg.request.PageLoadToken {
			return m, nil
		}
		if page := m.page(msg.id); page != nil {
			if loader, ok := page.(PageLoader); ok {
				loader.Apply(msg.request, msg.data, msg.err)
			}
		}
		return m, nil
	case pageExportedMsg:
		if page := m.page(msg.id); page != nil {
			if exporter, ok := page.(PageExporter); ok {
				exporter.ApplyExport(msg.request, msg.path, msg.err)
			}
		}
		return m, nil
	case loadedMsg:
		if msg.generation == m.loadGeneration {
			m.dashboardLoadBusy = false
		}
		current := msg.request.LoadID == m.loadID
		if msg.request.LoadID == 0 && msg.generation == m.loadGeneration {
			current = true
		}
		if msg.request.Action != "" {
			if msg.err != nil {
				m.actionInFlight = ""
				m.request.Action = ""
				m.loading = false
				if current && msg.request.Sync {
					m.syncInFlight = false
				}
				if !m.syncInFlight {
					m.syncing = false
				}
				m.status = ""
				m.warning = msg.err.Error()
				m.clearPendingActionOutcome()
				if !current {
					return m, m.refreshCurrentSnapshotWithActionOutcome(Snapshot{ActionWarning: msg.err.Error()})
				}
				return m, m.resumePendingWork()
			}
			if !current {
				m.finishAction(msg.request, msg.snapshot)
				return m, m.refreshCurrentSnapshotWithActionOutcome(msg.snapshot)
			}
			m.snapshot = msg.snapshot
			m.request.DailyCursor = msg.snapshot.DailyCursor
			m.request.DailyWindowStart = msg.snapshot.DailyWindowStart
			m.request.DailyDetailOffset = msg.snapshot.DailyDetailOffset
			m.request.RefreshPages = false
			m.loading = false
			m.loaded = true
			m.warning = msg.snapshot.Warning
			m.finishAction(msg.request, msg.snapshot)
			return m, m.resumePendingWork()
		}
		if msg.request.Sync && m.syncInFlight && msg.generation == m.syncGeneration && (msg.generation != m.loadGeneration || !sameRequestIgnoringSync(msg.request, m.request)) {
			if msg.err != nil {
				m.syncInFlight = false
				m.syncing = false
				m.commandBusy = false
				if m.quitAfterCommand {
					m.quitAfterCommand = false
					return m, tea.Quit
				}
				m.warning = msg.err.Error()
				return m, nil
			}
			m.syncInFlight = false
			m.syncing = false
			m.loading = true
			m.syncCompletionPending = true
			m.syncCompletionGeneration = m.loadGeneration + 1
			if msg.request.FullSync {
				m.commandBusy = false
				if m.quitAfterCommand {
					m.quitAfterCommand = false
					return m, tea.Quit
				}
				m.status = "full sync complete"
			}
			command := m.startDashboardLoad(m.request)
			return m, command
		}
		if msg.generation != m.loadGeneration {
			return m, nil
		}
		// The first store-only load contains pre-rendered page strings and can finish
		// against startup dimensions. Keep its result long enough to repaint at the
		// live dimensions before the ambient sync begins.
		initialLoad := !m.loaded && msg.request.Initial && msg.request.LoadID == 0
		requestMatches := initialLoad || sameRequestIgnoringSync(msg.request, m.request)
		if !requestMatches {
			return m, m.resumePendingWork()
		}
		if msg.err != nil {
			if msg.request.Initial {
				m.initialSnapshotPending = false
				m.request.Initial = false
			}
			m.loading, m.syncFresh = false, false
			m.commandBusy = false
			if m.syncCompletionPending && msg.generation == m.syncCompletionGeneration {
				m.syncCompletionPending = false
			}
			if msg.request.Sync {
				m.syncInFlight = false
			}
			if !m.syncInFlight {
				m.syncing = false
			}
			if m.quitAfterCommand {
				m.quitAfterCommand = false
				return m, tea.Quit
			}
			m.warning = msg.err.Error()
			return m, m.resumePendingWork()
		}
		initial := !m.loaded
		initialStoreLoad := msg.request.Initial && !msg.request.Sync
		m.snapshot = msg.snapshot
		if requestMatches {
			m.request.DailyCursor = msg.snapshot.DailyCursor
			m.request.DailyWindowStart = msg.snapshot.DailyWindowStart
			m.request.DailyDetailOffset = msg.snapshot.DailyDetailOffset
		}
		m.loading = false
		m.loaded = true
		m.request.RefreshPages = false
		m.warning = msg.snapshot.Warning
		if initialStoreLoad {
			currentSizeReady := m.request.Width >= minimumWidth && m.request.Height >= minimumHeight
			currentSizeMatches := msg.request.Width == m.request.Width && msg.request.Height == m.request.Height
			if !msg.snapshot.Empty && (!currentSizeReady || !currentSizeMatches) {
				m.initialSnapshotPending = true
				m.request.Initial = true
				m.syncing = false
				m.loading = msg.snapshot.Empty
				if !currentSizeReady {
					m.pendingResize = true
					return m, nil
				}
				m.pendingResize = false
				return m, m.startInitialStoreLoad(m.request)
			}
			m.initialSnapshotPending = false
			m.request.Initial = false
		}
		if msg.request.Sync {
			m.syncCompletionPending = false
			m.syncInFlight = false
			m.syncing = false
			m.syncFresh = true
			if msg.request.FullSync {
				m.commandBusy = false
				if m.quitAfterCommand {
					m.quitAfterCommand = false
					return m, tea.Quit
				}
				m.status = "full sync complete"
			} else {
				m.status = fmt.Sprintf("synced · %s ago", shortAge(0))
			}
			return m, combineCommands(m.maybeCheckSkillOffer(), m.resumePendingWork())
		}
		if m.syncCompletionPending && msg.generation == m.syncCompletionGeneration {
			m.syncCompletionPending = false
			m.syncing = false
			m.syncFresh = true
			if msg.request.FullSync {
				m.commandBusy = false
				if m.quitAfterCommand {
					m.quitAfterCommand = false
					return m, tea.Quit
				}
				m.status = "full sync complete"
			} else {
				m.status = fmt.Sprintf("synced · %s ago", shortAge(0))
			}
			return m, combineCommands(m.maybeCheckSkillOffer(), m.resumePendingWork())
		}
		m.applyPendingActionOutcome()
		if !initial && !initialStoreLoad {
			return m, m.resumePendingWork()
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
			return m, m.startDashboardLoad(next)
		}
		checkCommand := m.maybeCheckSkillOffer()
		if checkCommand == nil {
			return m, m.startDashboardLoad(next)
		}
		m.pendingSync = true
		return m, checkCommand
	case skillOfferCheckedMsg:
		if msg.err != nil || msg.check.Answered || !msg.check.HasRoots {
			return m, m.resumePendingWork()
		}
		if msg.check.Installed {
			return m, m.recordSkillOfferCmd(SkillOfferPreinstalled)
		}
		m.palette.close()
		m.offerState = skillOfferPrompt
		return m, nil
	case skillOfferInstalledMsg:
		m.palette.close()
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
		return m, m.resumePendingWork()
	case commandFinishedMsg:
		m.commandBusy = false
		m.commandOutputOffset = 0
		quitAfterCommand := m.quitAfterCommand
		m.quitAfterCommand = false
		if msg.err != nil {
			m.status = ""
			m.commandOutput = strings.TrimSpace(msg.result.Output)
			if m.commandOutput == "" {
				m.commandOutput = msg.err.Error()
			}
			m.commandOutputFailure = true
			m.commandOutputHint = paletteActionFailure(msg.command, msg.err)
			m.warning = m.commandOutputHint
			if quitAfterCommand {
				return m, tea.Quit
			}
			return m, m.resumePendingWork()
		}
		m.warning = ""
		m.commandOutput = msg.result.Output
		m.commandOutputFailure = false
		m.commandOutputHint = ""
		m.status = strings.ToLower(msg.command.title) + " complete"
		if quitAfterCommand {
			return m, tea.Quit
		}
		return m, m.resumePendingWork()
	case tea.KeyMsg:
		if m.offerState != skillOfferHidden {
			m.palette.close()
			return m.updateSkillOfferKey(msg.String())
		}
		if m.palette.active {
			return m.updatePaletteKey(msg)
		}
		if m.commandOutput != "" {
			return m.updateCommandOutputKey(msg)
		}
		return m.updateKey(msg)
	}
	return m, nil
}

func sameRequestIgnoringSync(left, right Request) bool {
	// Dashboard loads do not own the history-search page's private state.
	left.Sync = false
	right.Sync = false
	left.LoadID = 0
	right.LoadID = 0
	left.PageLoadToken = ""
	right.PageLoadToken = ""
	left.Initial = false
	right.Initial = false
	left.HistoryQuery = ""
	right.HistoryQuery = ""
	left.HistorySelect = 0
	right.HistorySelect = 0
	left.HistorySessionID = ""
	right.HistorySessionID = ""
	left.SessionDetailOffset = 0
	right.SessionDetailOffset = 0
	left.HistoryExportID = ""
	right.HistoryExportID = ""
	left.HistoryExportToken = ""
	right.HistoryExportToken = ""
	left.Progress = nil
	right.Progress = nil
	return reflect.DeepEqual(left, right)
}

func actionProgress(action string) string {
	switch action {
	case VerifyVaultAction:
		return "verifying vault…"
	default:
		return "working…"
	}
}

func (m Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	value := key.String()
	if m.offerState != skillOfferHidden {
		return m.updateSkillOfferKey(value)
	}
	if value == "?" {
		if m.help {
			m.help = false
			return m, nil
		}
		page := m.activePage()
		interactive, ok := page.(InteractivePage)
		if !ok || !interactive.Editing() || key.Type != tea.KeyRunes {
			m.help = true
			return m, nil
		}
	}
	if m.help {
		if value == "esc" {
			m.help = false
			return m, nil
		}
		if binding, ok := keyBindingFor(value, len(m.router.Pages())); ok && binding.Action == keyActionQuit {
			return m.updateBinding(binding, value)
		}
		return m, nil
	}
	binding, bound := keyBindingFor(value, len(m.router.Pages()))
	page := m.activePage()
	interactive, interactivePage := page.(InteractivePage)
	globalFirst := bound && binding.Action != keyActionPageCommand &&
		(!interactivePage || !interactive.Editing() || key.Type != tea.KeyRunes)
	if globalFirst {
		if (m.commandBusy || m.dashboardLoadBusy || m.pendingSync) && binding.Action != keyActionNavigatePages && binding.Action != keyActionToggleHelp && binding.Action != keyActionQuit {
			return m, nil
		}
		return m.updateBinding(binding, value)
	}
	if bound && interactivePage && binding.Action == keyActionPageCommand && (m.commandBusy || m.dashboardLoadBusy || m.syncing || m.pendingSync) {
		return m, nil
	}
	if interactivePage {
		result := interactive.HandleKey(m.request, key)
		if result.Handled {
			m.request = result.Request
			switch result.Action {
			case PageActionLoad:
				if loader, ok := page.(PageLoader); ok {
					return m.startPageLoad(loader, m.request)
				}
			case PageActionExport:
				if exporter, ok := page.(PageExporter); ok {
					return m, m.exportPageCmd(exporter, m.request)
				}
			}
			return m, nil
		}
	}
	if !bound {
		return m, nil
	}
	if m.commandBusy || m.pendingSync {
		return m, nil
	}
	return m.updateBinding(binding, value)
}

func (m Model) updateBinding(binding KeyBinding, value string) (tea.Model, tea.Cmd) {
	if binding.Action == keyActionQuit {
		if m.commandBusy {
			m.quitAfterCommand = true
			m.status = "finishing current operation before exit"
			return m, nil
		}
		return m, tea.Quit
	}
	if binding.Action == keyActionToggleHelp {
		m.help = !m.help
		return m, nil
	}
	if binding.Action == keyActionOpenPalette {
		if m.commandBusy || m.loading || m.syncing || m.pendingSync || m.dashboardLoadBusy {
			return m, nil
		}
		m.help = false
		m.palette.resize(m.request.Width)
		return m, m.palette.open(m.router, m.commandRegistry, m.request.Width)
	}
	if m.help {
		return m, nil
	}
	switch binding.Action {
	case keyActionNavigatePages:
		if m.navigatePages(value) {
			if page := m.activePage(); page != nil {
				if loader, ok := page.(PageLoader); ok {
					return m.startPageLoad(loader, m.request)
				}
			}
		}
		return m, nil
	case keyActionProvider:
		m.request.Provider = (m.request.Provider + 1) % 3
		m.request.DailyCursor = 0
		m.request.DailyWindowStart = 0
		m.request.DailyDetailOffset = 0
		m.resetSessionNavigation()
		return m.loadDashboardAndActivePage(m.request)
	case keyActionRange:
		m.request.Range = (m.request.Range + 1) % 4
		m.request.DailyCursor = 0
		m.request.DailyWindowStart = 0
		m.request.DailyDetailOffset = 0
		m.resetSessionNavigation()
		return m.loadDashboardAndActivePage(m.request)
	case keyActionRefresh:
		m.syncing = true
		m.resetSessionNavigation()
		request := m.request
		request.Sync = true
		request.RefreshPages = true
		return m.loadDashboardAndActivePage(request)
	case keyActionPageCommand:
		page := m.activePage()
		if page == nil {
			return m, nil
		}
		context := PageContext{
			Render: m.render, Snapshot: m.snapshot, Request: m.request,
			Width: ContentWidth(m.request.Width), Height: ContentHeightFor(m.request.Width, m.request.Height),
		}
		request, changed := page.Update(context, value)
		if !changed {
			return m, nil
		}
		if request.Action != "" {
			if m.actionInFlight != "" || m.syncInFlight || m.dashboardLoadBusy {
				return m, nil
			}
			request.RefreshPages = true
			loadRequest := request
			request.Action = ""
			m.request = request
			m.warning = ""
			m.status = actionProgress(loadRequest.Action)
			m.syncing = true
			m.actionInFlight = loadRequest.Action
			return m, m.startDashboardLoad(loadRequest)
		}
		if !page.NeedsReload(context, request) {
			m.request = request
			return m, nil
		}
		m.request = request
		command := m.startDashboardLoad(m.request)
		return m, command
	}
	return m, nil
}

func (m Model) loadDashboardAndActivePage(request Request) (Model, tea.Cmd) {
	if page := m.activePage(); page != nil {
		if loader, ok := page.(PageLoader); ok {
			var pageCommand tea.Cmd
			m, pageCommand = m.startPageLoad(loader, request)
			dashboardCommand := m.startDashboardLoad(request)
			return m, tea.Batch(dashboardCommand, pageCommand)
		}
	}
	command := m.startDashboardLoad(request)
	return m, command
}

func (m Model) updatePaletteKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "ctrl+c" {
		m.palette.close()
		return m, tea.Quit
	}
	selection, command := m.palette.update(key)
	if selection.event != paletteSelected {
		return m, command
	}
	return m, m.runPaletteCommand(selection.command)
}

func (m *Model) runPaletteCommand(command paletteCommand) tea.Cmd {
	if command.page != "" {
		if !m.router.Select(command.page) {
			return nil
		}
		if page := m.activePage(); page != nil {
			if loader, ok := page.(PageLoader); ok {
				updated, pageCommand := m.startPageLoad(loader, m.request)
				*m = updated
				return pageCommand
			}
		}
		return nil
	}
	if command.id == CommandQuitID {
		if m.commandBusy {
			m.quitAfterCommand = true
			m.status = "finishing current operation before exit"
			return nil
		}
		return tea.Quit
	}
	if m.commandBusy || m.loading || m.syncing || m.pendingSync || m.dashboardLoadBusy {
		m.warning = "dashboard is busy; wait for the current operation to finish"
		m.status = ""
		return nil
	}
	if command.id == CommandSyncFullID {
		m.commandBusy, m.syncing = true, true
		m.dashboardLoadBusy = true
		m.warning = ""
		m.status = "running sync --full"
		m.resetSessionNavigation()
		request := m.request
		request.Sync, request.FullSync = true, true
		return m.startDashboardLoad(request)
	}
	if command.action.Run == nil {
		m.warning = command.title + " is unavailable in this dashboard build"
		m.status = ""
		return nil
	}
	m.commandBusy = true
	m.warning = ""
	m.status = "running " + strings.ToLower(command.title)
	return func() tea.Msg {
		result, err := command.action.Run()
		return commandFinishedMsg{command: command, result: result, err: err}
	}
}

func (m Model) updateCommandOutputKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.String() == "ctrl+c" {
		return m, tea.Quit
	}
	layout := newCockpitLayout(m.request.Width, m.request.Height)
	width := min(84, max(46, layout.width-8))
	contentWidth := max(1, width-6)
	_, maxLines, maxOffset := commandOutputViewport(m.commandOutput, contentWidth, layout.height, m.commandOutputFailure, m.commandOutputHint)
	switch key.String() {
	case "up":
		m.commandOutputOffset = max(0, m.commandOutputOffset-1)
		return m, nil
	case "down":
		m.commandOutputOffset = min(maxOffset, m.commandOutputOffset+1)
		return m, nil
	case "pgup":
		m.commandOutputOffset = max(0, m.commandOutputOffset-maxLines)
		return m, nil
	case "pgdown":
		m.commandOutputOffset = min(maxOffset, m.commandOutputOffset+maxLines)
		return m, nil
	case "home":
		m.commandOutputOffset = 0
		return m, nil
	case "end":
		m.commandOutputOffset = maxOffset
		return m, nil
	}
	commandHint := m.commandOutputHint
	m.commandOutput = ""
	m.commandOutputOffset = 0
	if commandHint != "" && m.warning == commandHint {
		m.warning = ""
	}
	m.commandOutputFailure, m.commandOutputHint = false, ""
	return m, nil
}

func (m *Model) navigatePages(key string) bool {
	var changed bool
	switch key {
	case "tab":
		changed = m.router.Move(1)
	case "shift+tab":
		changed = m.router.Move(-1)
	default:
		if len(key) != 1 || key[0] < '1' || key[0] > '9' {
			return false
		}
		changed = m.router.SelectIndex(int(key[0] - '1'))
	}
	return changed
}

func (m Model) activePage() Page {
	return m.router.ActivePage()
}

func (m *Model) resetSessionNavigation() {
	m.request.SessionProject = ""
	m.request.SessionProjectActive = false
	m.request.SessionCursor = ""
	m.request.SessionCursorStack = ""
	m.request.SessionOffset = 0
	m.request.SessionReturnToEnd = false
	m.request.SessionDetailID = ""
	m.request.SessionDetailOffset = 0
	m.request.Ledger.SessionCursor = 0
	m.request.Ledger.SessionPageCursor = ""
	m.request.Ledger.SessionCursorStack = ""
	m.request.Ledger.SessionSelectLast = false
	m.request.Ledger.DetailID = ""
	m.request.Ledger.DetailOffset = 0
}

func (m Model) page(id PageID) Page {
	return m.router.PageAt(m.router.IndexOf(id))
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

// View renders the current immutable model state.
func (m Model) View() string {
	if m.request.Width > 0 && m.request.Height > 0 && (m.request.Width < minimumWidth || m.request.Height < minimumHeight) {
		return m.place(m.render.Palette.Subtle().Render("terminal too small") + "\n")
	}
	if m.offerState != skillOfferHidden {
		return m.skillOfferView()
	}
	if m.palette.active {
		return m.paletteView()
	}
	if m.commandOutput != "" {
		return m.commandOutputView()
	}
	if m.help {
		return m.helpView()
	}
	return m.baseView()
}

func (m Model) baseView() string {
	if m.loading {
		elapsed := time.Since(m.started).Round(time.Second)
		line := fmt.Sprintf("%s Syncing Codex + Claude · %s · %s\n", m.spinner.View(), m.syncProgressText(), elapsed)
		return m.place(line)
	}
	return m.cockpitView()
}

func (m Model) syncProgressText() string {
	switch m.progress.Phase {
	case "starting sync":
		return "starting sync"
	case "discovering files":
		return "discovering files"
	case "preparing sync":
		if m.progress.FilesFound > 0 {
			return fmt.Sprintf("preparing sync · %s files found", formatStatusNumber(m.progress.FilesFound))
		}
		return "preparing sync"
	case "ingesting files":
		if m.progress.FilesFound > 0 {
			return fmt.Sprintf("ingesting %s/%s files", formatStatusNumber(m.progress.FilesProcessed), formatStatusNumber(m.progress.FilesFound))
		}
		return "ingesting files"
	default:
		return fmt.Sprintf("%d files scanned", m.snapshot.FilesScanned)
	}
}

func (m Model) cockpitView() string {
	layout := newCockpitLayout(m.request.Width, m.request.Height)
	content := m.contentView(layout)
	if layout.showRail {
		content = lipgloss.JoinHorizontal(lipgloss.Top,
			m.railView(layout),
			fitBlock("", gridGap, layout.bodyHeight),
			content,
		)
	}
	content = fitBlock(content, layout.bodyWidth, layout.bodyHeight)
	rows := []string{
		m.topBarView(layout),
		m.summaryView(layout),
		m.chromeDividerView(layout, '┬'),
		content,
		m.chromeDividerView(layout, '┴'),
		m.statusBarView(layout),
	}
	rows = append(rows, m.footerView(layout))
	view := strings.Join(rows, "\n")
	return frameBlock(view, layout)
}

func (m Model) chromeDividerView(layout cockpitLayout, junction rune) string {
	if !layout.showRail {
		return fitLine(strings.Repeat("─", layout.innerWidth), layout.innerWidth)
	}
	leftWidth := max(0, layout.railWidth-1)
	rightWidth := max(0, layout.innerWidth-layout.railWidth)
	line := strings.Repeat("─", leftWidth) + string(junction) + strings.Repeat("─", rightWidth)
	return fitLine(line, layout.innerWidth)
}

func (m Model) topBarView(layout cockpitLayout) string {
	active := ""
	if page := m.activePage(); page != nil {
		active = page.Title()
	}
	left := m.render.Palette.Header().Render("tokenomnom") +
		m.render.Palette.Subtle().Render("  /  ") +
		m.render.Palette.Emphasis().Render(strings.ToUpper(active))
	rightText := "LOCAL  ·  " + strings.ToUpper(m.request.Provider.String()) + "  ·  " + strings.ToUpper(m.request.Range.String())
	if !layout.showRail {
		rightText = fmt.Sprintf("%s %dx%d  ·  1-8 pages", strings.ToUpper(layout.tiers.Width.String()), layout.width, layout.height)
	}
	right := m.render.Palette.Subtle().Render(rightText)
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

func (m Model) contentView(layout cockpitLayout) string {
	body := ""
	render := m.render
	render.Width = layout.paneWidth
	if page := m.activePage(); page != nil {
		body = page.View(PageContext{
			Render: render, Snapshot: m.snapshot, Request: m.request,
			Width: layout.paneWidth, Height: layout.pageHeight,
		})
	}
	// The global top bar already names the page. Keeping the band title empty
	// here preserves the existing page body while giving later pages the same
	// exact-fill primitive for their own titled bands.
	return RenderBand(render, NewBand("", layout.paneWidth, layout.bodyHeight, NewPane("", body)))
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
