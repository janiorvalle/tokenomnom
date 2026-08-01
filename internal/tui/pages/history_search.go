package pages

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	"github.com/janiorvalle/tokenomnom/internal/tui"
)

// HistorySearchPageID is the stable sidebar identity for local prompt search.
const HistorySearchPageID tui.PageID = "history-search"

// SearchHit is the small, presentation-ready result shape used by the page.
// The CLI adapter deliberately keeps storage types out of the TUI package.
type SearchHit struct {
	PromptID  string
	SessionID string
	Provider  string
	Date      string
	Project   string
	Snippet   string
}

// SearchResult contains one bounded search response.
type SearchResult struct {
	Hits     []SearchHit
	HasMore  bool
	Warnings []string
}

// SessionPrompt is one prompt preview in the selected session.
type SessionPrompt struct {
	PromptID string
	Date     string
	Snippet  string
}

// SessionDetail is the bounded session view opened from a search result.
type SessionDetail struct {
	SessionID string
	Provider  string
	Project   string
	FirstDate string
	LastDate  string
	Preview   string
	Prompts   []SessionPrompt
}

// HistorySearchData is returned by the CLI-owned history loader.
type HistorySearchData struct {
	NotIndexed bool
	Search     SearchResult
	Session    *SessionDetail
}

// HistorySearchOptions supplies storage operations without coupling the page
// to the history database or to CLI command state.
type HistorySearchOptions struct {
	Load   func(tui.Request) (HistorySearchData, error)
	Export func(tui.Request) (string, error)
}

// HistorySearchPage is the interactive local-history search destination.
type HistorySearchPage struct {
	load   func(tui.Request) (HistorySearchData, error)
	export func(tui.Request) (string, error)

	query         string
	sessionID     string
	hits          []SearchHit
	hasMore       bool
	warnings      []string
	detail        *SessionDetail
	notIndexed    bool
	loading       bool
	inputMode     bool
	exporting     bool
	errorText     string
	exportText    string
	exportID      string
	exportToken   string
	exportAttempt uint64
	loadToken     string
}

// NewHistorySearchPage creates the page without opening the history index.
func NewHistorySearchPage(options HistorySearchOptions) *HistorySearchPage {
	return &HistorySearchPage{load: options.Load, export: options.Export}
}

func (p *HistorySearchPage) ID() tui.PageID           { return HistorySearchPageID }
func (p *HistorySearchPage) Section() tui.PageSection { return tui.HistorySection }
func (p *HistorySearchPage) Title() string            { return "Search" }

// View renders either the query results or the selected session detail.
func (p *HistorySearchPage) View(context tui.PageContext) string {
	width := max(1, tui.ContentWidth(context.Request.Width))
	if p.sessionID != "" {
		return p.detailView(width, context)
	}
	return p.searchView(width, context)
}

// Editing reports whether printable keys should stay inside the search input
// instead of activating dashboard-wide shortcuts.
func (p *HistorySearchPage) Editing() bool {
	return p.inputMode && p.sessionID == ""
}

// BeginLoad records the request generation on the update loop before a
// background history query starts.
func (p *HistorySearchPage) BeginLoad(request tui.Request) {
	p.loadToken = request.HistoryLoadToken
}

// Update preserves the base Page contract for page-command dispatch.
func (p *HistorySearchPage) Update(request tui.Request, key string) (tui.Request, bool) {
	result := p.HandleKey(request, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return result.Request, result.Handled && result.Changed
}

// HandleKey owns text entry and result navigation while leaving global keys
// such as ? and ctrl+c to the dashboard.
func (p *HistorySearchPage) HandleKey(request tui.Request, key tea.KeyMsg) tui.PageKeyResult {
	if request.HistoryQuery != p.query {
		p.query = request.HistoryQuery
	}
	value := key.String()
	result := tui.PageKeyResult{Request: request}
	switch value {
	case "up":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && len(p.hits) > 0 && request.HistorySelect > 0 {
			result.Request.HistorySelect--
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			p.clearExport()
			result.Changed = true
		}
		return result
	case "down":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && len(p.hits) > 0 && request.HistorySelect < len(p.hits)-1 {
			result.Request.HistorySelect++
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			p.clearExport()
			result.Changed = true
		}
		return result
	case "home":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && request.HistorySelect != 0 {
			result.Request.HistorySelect = 0
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			p.clearExport()
			result.Changed = true
		}
		return result
	case "end":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && len(p.hits) > 0 && request.HistorySelect != len(p.hits)-1 {
			result.Request.HistorySelect = len(p.hits) - 1
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			p.clearExport()
			result.Changed = true
		}
		return result
	case "enter":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		if p.inputMode {
			if strings.TrimSpace(request.HistoryQuery) == "" {
				return result
			}
			p.inputMode = false
			p.loading = true
			result.Changed, result.Action = true, tui.PageActionLoad
			return result
		}
		if p.sessionID != "" || len(p.hits) == 0 {
			return result
		}
		selected := min(max(request.HistorySelect, 0), len(p.hits)-1)
		p.sessionID = p.hits[selected].SessionID
		p.detail = nil
		p.loading = true
		p.inputMode = false
		p.errorText = ""
		p.clearExport()
		result.Request.HistorySessionID = p.sessionID
		result.Request.HistoryExportID = ""
		result.Request.HistoryExportToken = ""
		result.Changed, result.Action = true, tui.PageActionLoad
		return result
	case "esc":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		if p.sessionID != "" {
			p.sessionID = ""
			p.detail = nil
			p.loading = true
			p.inputMode = false
			p.errorText = ""
			p.clearExport()
			result.Request.HistorySessionID = ""
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			result.Changed, result.Action = true, tui.PageActionLoad
			return result
		}
		if request.HistoryQuery != "" {
			p.resetSearch()
			p.query = ""
			p.inputMode = false
			result.Request.HistoryQuery = ""
			result.Request.HistorySelect = 0
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			result.Changed = true
		}
		if request.HistoryQuery == "" {
			p.inputMode = false
		}
		return result
	case "/":
		if key.Type != tea.KeyRunes || len(key.Runes) != 1 {
			break
		}
		result.Handled = true
		if p.sessionID != "" {
			return result
		}
		if p.inputMode {
			result.Request.HistoryQuery += printableKeyText(key)
			result.Request.HistorySelect = 0
			result.Request.HistorySessionID = ""
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			p.beginSearch(result.Request.HistoryQuery)
			result.Changed = true
			return result
		}
		result.Changed = !p.inputMode
		p.inputMode = true
		return result
	case "backspace":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		if p.sessionID != "" || !p.inputMode {
			return result
		}
		query := []rune(request.HistoryQuery)
		if len(query) == 0 {
			return result
		}
		result.Request.HistoryQuery = string(query[:len(query)-1])
		result.Request.HistorySelect = 0
		result.Request.HistoryExportID = ""
		result.Request.HistoryExportToken = ""
		p.beginSearch(result.Request.HistoryQuery)
		result.Changed = true
		return result
	case "ctrl+u":
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		if p.sessionID != "" || !p.inputMode || request.HistoryQuery == "" {
			return result
		}
		result.Request.HistoryQuery = ""
		result.Request.HistorySelect = 0
		result.Request.HistoryExportID = ""
		result.Request.HistoryExportToken = ""
		p.resetSearch()
		p.query = ""
		p.inputMode = true
		result.Changed = true
		return result
	case "e":
		if key.Type != tea.KeyRunes || len(key.Runes) != 1 {
			break
		}
		result.Handled = true
		if p.inputMode {
			result.Request.HistoryQuery += string(key.Runes)
			result.Request.HistorySelect = 0
			result.Request.HistorySessionID = ""
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			p.beginSearch(result.Request.HistoryQuery)
			result.Changed = true
			return result
		}
		if p.exporting {
			return result
		}
		target := p.exportTarget(request)
		if target == "" {
			p.errorText = "Select a search result before exporting."
			return result
		}
		p.exporting, p.exportText, p.errorText = true, "", ""
		p.exportID = target
		p.exportAttempt++
		p.exportToken = strconv.FormatUint(p.exportAttempt, 10)
		result.Request.HistoryExportID = target
		result.Request.HistoryExportToken = p.exportToken
		result.Changed, result.Action = true, tui.PageActionExport
		return result
	}

	if !isPrintableKey(key) || p.sessionID != "" || !p.inputMode {
		return result
	}
	result.Handled = true
	result.Request.HistoryQuery += printableKeyText(key)
	result.Request.HistorySelect = 0
	result.Request.HistorySessionID = ""
	result.Request.HistoryExportID = ""
	result.Request.HistoryExportToken = ""
	p.beginSearch(result.Request.HistoryQuery)
	result.Changed = true
	return result
}

// Load executes the storage callback outside the Bubble Tea update loop.
func (p *HistorySearchPage) Load(request tui.Request) (any, error) {
	if p.load == nil {
		return nil, errors.New("history search loader is unavailable")
	}
	return p.load(request)
}

// Apply installs only the response for the current query/session. This keeps
// a slower older search from replacing a newer result after rapid typing.
func (p *HistorySearchPage) Apply(request tui.Request, value any, err error) {
	if request.HistoryLoadToken != p.loadToken || request.HistoryQuery != p.query || request.HistorySessionID != p.sessionID {
		return
	}
	p.loading = false
	if err != nil {
		p.errorText = "History search is unavailable. Try again."
		p.hits, p.warnings, p.detail = nil, nil, nil
		return
	}
	data, ok := value.(HistorySearchData)
	if !ok {
		p.errorText = "History search returned an unreadable result."
		p.hits, p.warnings, p.detail = nil, nil, nil
		return
	}
	p.errorText, p.exportText = "", ""
	p.notIndexed = data.NotIndexed
	p.warnings = append([]string(nil), data.Search.Warnings...)
	p.hasMore = data.Search.HasMore
	p.hits = append([]SearchHit(nil), data.Search.Hits...)
	p.detail = data.Session
}

// Export writes the selected session through the CLI-owned callback.
func (p *HistorySearchPage) Export(request tui.Request) (string, error) {
	if p.export == nil {
		return "", errors.New("history export is unavailable")
	}
	if request.HistoryExportID == "" {
		return "", errors.New("select a history result before exporting")
	}
	return p.export(request)
}

// ApplyExport records a user-facing receipt for the completed export.
func (p *HistorySearchPage) ApplyExport(request tui.Request, path string, err error) {
	if request.HistoryExportID != p.exportID || request.HistoryExportToken != p.exportToken {
		return
	}
	p.exporting = false
	p.exportID = ""
	p.exportToken = ""
	if err != nil {
		p.errorText = "Export failed. Check the history index and try again."
		return
	}
	p.errorText, p.exportText = "", "Exported to "+path
}

func (p *HistorySearchPage) exportTarget(request tui.Request) string {
	if request.HistorySessionID != "" {
		return request.HistorySessionID
	}
	if len(p.hits) == 0 {
		return ""
	}
	selected := min(max(request.HistorySelect, 0), len(p.hits)-1)
	return p.hits[selected].SessionID
}

func (p *HistorySearchPage) clearExport() {
	p.exporting = false
	p.exportText = ""
	p.exportID = ""
	p.exportToken = ""
}

func (p *HistorySearchPage) beginSearch(query string) {
	p.query = query
	p.resetSearch()
	p.inputMode = true
}

func (p *HistorySearchPage) resetSearch() {
	p.hits = nil
	p.hasMore = false
	p.warnings = nil
	p.detail = nil
	p.notIndexed = false
	p.loading = false
	p.errorText = ""
	p.exportText = ""
	p.exporting = false
	p.exportID = ""
}

func (p *HistorySearchPage) searchView(width int, context tui.PageContext) string {
	lines := []string{
		context.Render.Palette.Header().Render("FIND IN HISTORY"),
		context.Render.Palette.Subtle().Render("Search your indexed prompts by exact phrase."),
		"",
		context.Render.Palette.Subtle().Render("SEARCH ") + context.Render.Palette.Emphasis().Render("/"+oneLine(p.query)+"█"),
		context.Render.Palette.Border().Render(strings.Repeat("─", min(width, 40))),
	}
	switch {
	case p.loading:
		lines = append(lines, context.Render.Palette.Subtle().Render("Searching local history…"))
	case p.notIndexed:
		lines = append(lines,
			context.Render.Palette.Warning().Render("History index is not available."),
			context.Render.Palette.Subtle().Render("run `tokenomnom history index`, then search here."),
		)
	case p.errorText != "":
		lines = append(lines, context.Render.Palette.Warning().Render(p.errorText))
	case p.query == "":
		lines = append(lines, context.Render.Palette.Subtle().Render("Type a phrase to search local session history."))
	case len(p.hits) == 0:
		lines = append(lines, context.Render.Palette.Subtle().Render("No matching prompts."))
	default:
		selectedIndex := min(max(context.Request.HistorySelect, 0), len(p.hits)-1)
		visibleCount := max(1, (tui.ContentHeight(context.Request.Height)-10)/2)
		start := max(0, selectedIndex-visibleCount+1)
		end := min(len(p.hits), start+visibleCount)
		if start > 0 {
			lines = append(lines, context.Render.Palette.Subtle().Render("↑ earlier results"))
		}
		for index, hit := range p.hits[start:end] {
			absoluteIndex := start + index
			selected := absoluteIndex == selectedIndex
			prefix := "  "
			style := context.Render.Palette.Subtle()
			if selected {
				prefix = "› "
				style = context.Render.Palette.Emphasis().Bold(true)
			}
			snippet := truncateText(oneLine(hit.Snippet), max(1, width-3))
			lines = append(lines, prefix+style.Render(highlightSnippet(snippet, context.Render.Palette)))
			project := oneLine(hit.Project)
			if project == "" {
				project = "unknown project"
			}
			metadata := oneLine(hit.Date) + " · " + oneLine(hit.Provider) + " · " + project
			lines = append(lines, "    "+context.Render.Palette.Subtle().Render(metadata))
		}
		if end < len(p.hits) || p.hasMore {
			lines = append(lines, context.Render.Palette.Subtle().Render("More results are available in `tokenomnom history search`."))
		}
	}
	lines = appendPageStatus(lines, context.Render, p.warnings, p.exporting, p.exportText)
	footer := "/ edit"
	if p.inputMode {
		footer = "type query  enter search  esc cancel"
	} else if p.query != "" || len(p.hits) > 0 {
		footer = "↑/↓  enter open  e export  / edit  esc"
	}
	lines = append(lines, "", context.Render.Palette.Subtle().Render(footer))
	return strings.Join(lines, "\n")
}

func (p *HistorySearchPage) detailView(width int, context tui.PageContext) string {
	lines := []string{
		context.Render.Palette.Header().Render("SESSION DETAIL"),
		context.Render.Palette.Subtle().Render(oneLine(p.sessionID)),
		"",
	}
	if p.loading {
		lines = append(lines, context.Render.Palette.Subtle().Render("Loading session…"))
	} else if p.errorText != "" {
		lines = append(lines, context.Render.Palette.Warning().Render(p.errorText))
	} else if p.detail == nil {
		lines = append(lines, context.Render.Palette.Subtle().Render("No session detail is available."))
	} else {
		project := oneLine(p.detail.Project)
		if project == "" {
			project = "unknown project"
		}
		lines = append(lines,
			context.Render.Palette.Subtle().Render("PROVENANCE ")+oneLine(p.detail.Provider)+" · "+project,
			context.Render.Palette.Subtle().Render("DATE ")+oneLine(p.detail.FirstDate)+" → "+oneLine(p.detail.LastDate),
		)
		if p.detail.Preview != "" {
			lines = append(lines, "", truncateText(oneLine(p.detail.Preview), max(1, width)))
		}
		lines = append(lines, "", context.Render.Palette.Header().Render("PROMPTS"))
		if len(p.detail.Prompts) == 0 {
			lines = append(lines, context.Render.Palette.Subtle().Render("No indexed prompts in this session."))
		}
		for _, prompt := range p.detail.Prompts {
			line := oneLine(prompt.Date) + "  " + oneLine(prompt.Snippet)
			lines = append(lines, truncateText(line, max(1, width)))
		}
	}
	lines = appendPageStatus(lines, context.Render, p.warnings, p.exporting, p.exportText)
	lines = append(lines, "", context.Render.Palette.Subtle().Render("esc back  e export"))
	return strings.Join(lines, "\n")
}

func appendPageStatus(lines []string, render theme.Context, warnings []string, exporting bool, exported string) []string {
	if exported != "" {
		lines = append(lines, "", render.Palette.Success().Render(oneLine(exported)))
	}
	if exporting {
		lines = append(lines, "", render.Palette.Subtle().Render("Exporting session…"))
	}
	if len(warnings) > 0 {
		lines = append(lines, "", render.Palette.Warning().Render("Index note: "+oneLine(warnings[0])))
	}
	return lines
}

func highlightSnippet(value string, palette interface {
	Emphasis() lipgloss.Style
}) string {
	var result strings.Builder
	var plain, match strings.Builder
	inMatch := false
	flushPlain := func() {
		if plain.Len() > 0 {
			result.WriteString(plain.String())
			plain.Reset()
		}
	}
	flushMatch := func() {
		if match.Len() > 0 {
			result.WriteString(palette.Emphasis().Render(match.String()))
			match.Reset()
		}
	}
	for _, current := range value {
		switch current {
		case '[':
			if inMatch {
				match.WriteRune(current)
			} else {
				plain.WriteRune(current)
			}
		case history.SearchSnippetMatchStart:
			if inMatch {
				match.WriteRune(current)
				continue
			}
			flushPlain()
			inMatch = true
		case history.SearchSnippetMatchEnd:
			if !inMatch {
				plain.WriteRune(current)
				continue
			}
			flushMatch()
			inMatch = false
		default:
			if inMatch {
				match.WriteRune(current)
			} else {
				plain.WriteRune(current)
			}
		}
	}
	if inMatch {
		flushMatch()
	} else {
		flushPlain()
	}
	return result.String()
}

func isPrintableKey(key tea.KeyMsg) bool {
	if key.Alt {
		return false
	}
	if key.Type == tea.KeySpace {
		return true
	}
	if key.Type != tea.KeyRunes || len(key.Runes) == 0 {
		return false
	}
	for _, current := range key.Runes {
		if !unicode.IsPrint(current) {
			return false
		}
	}
	return true
}

func printableKeyText(key tea.KeyMsg) string {
	if key.Type == tea.KeySpace {
		return " "
	}
	return string(key.Runes)
}

func oneLine(value string) string {
	return strings.Map(func(current rune) rune {
		if current == '\n' || current == '\r' || unicode.IsControl(current) {
			return ' '
		}
		return current
	}, value)
}

func truncateText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	runes := []rune(value)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

var _ tui.InteractivePage = (*HistorySearchPage)(nil)
var _ tui.PageLoader = (*HistorySearchPage)(nil)
var _ tui.PageLoadTracker = (*HistorySearchPage)(nil)
var _ tui.PageExporter = (*HistorySearchPage)(nil)
