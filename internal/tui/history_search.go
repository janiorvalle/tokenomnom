package tui

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

// HistorySearchPageID is the stable sidebar identity for local prompt search.
const HistorySearchPageID PageID = "history-search"

type SearchHit = tuipages.SearchHit
type SearchResult = tuipages.SearchResult
type SessionPrompt = tuipages.SessionPrompt
type SessionDetail = tuipages.SessionDetail
type HistorySearchData = tuipages.HistorySearchData

// HistorySearchOptions supplies storage operations without coupling the page
// to the history database or to CLI command state.
type HistorySearchOptions struct {
	Load        func(Request) (HistorySearchData, error)
	Export      func(Request) (string, error)
	ReportError func(error)
}

// HistorySearchPage is the interactive local-history search destination.
type HistorySearchPage struct {
	load        func(Request) (HistorySearchData, error)
	export      func(Request) (string, error)
	reportError func(error)

	query           string
	sessionID       string
	hits            []SearchHit
	hasMore         bool
	warnings        []string
	detail          *SessionDetail
	notIndexed      bool
	searched        bool
	loading         bool
	inputMode       bool
	exporting       bool
	errorText       string
	exportErrorText string
	exportText      string
	exportID        string
	exportAttemptID string
	exportAttempt   uint64
	loadToken       string
}

// NewHistorySearchPage creates the page without opening the history index.
func NewHistorySearchPage(options HistorySearchOptions) *HistorySearchPage {
	return &HistorySearchPage{load: options.Load, export: options.Export, reportError: options.ReportError}
}

func (p *HistorySearchPage) ID() PageID           { return HistorySearchPageID }
func (p *HistorySearchPage) Section() PageSection { return HistorySection }
func (p *HistorySearchPage) Title() string        { return "Search" }

// View renders either the query results or the selected session detail.
func (p *HistorySearchPage) View(context PageContext) string {
	width := context.Width
	if width <= 0 {
		width = ContentWidth(context.Request.Width)
	}
	width = max(1, width)
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
func (p *HistorySearchPage) BeginLoad(request Request) {
	p.loadToken = request.PageLoadToken
	p.loading = true
	p.inputMode = false
	p.hits = nil
	p.hasMore = false
	p.warnings = nil
	p.detail = nil
	p.notIndexed = false
	p.errorText = ""
	p.clearExport()
}

// Update preserves the base Page contract for page-command dispatch.
func (p *HistorySearchPage) Update(context PageContext, key string) (Request, bool) {
	keyRunes := []rune(key)
	if len(keyRunes) != 1 {
		return context.Request, false
	}
	result := p.HandleKey(context.Request, tea.KeyMsg{Type: tea.KeyRunes, Runes: keyRunes})
	return result.Request, result.Handled && result.Changed
}

func (p *HistorySearchPage) NeedsReload(PageContext, Request) bool { return false }

// HandleKey owns text entry and result navigation while leaving global keys
// such as ? and ctrl+c to the dashboard.
func (p *HistorySearchPage) HandleKey(request Request, key tea.KeyMsg) PageKeyResult {
	if request.HistoryQuery != p.query {
		p.query = request.HistoryQuery
	}
	value := key.String()
	result := PageKeyResult{Request: request}
	if p.loading && value != "esc" {
		result.Handled = true
		return result
	}
	if p.inputMode && key.Type != tea.KeyRunes && key.Type != tea.KeySpace {
		switch value {
		case "enter", "esc", "backspace", "ctrl+u":
		default:
			result.Handled = true
			return result
		}
	}
	switch value {
	case "up":
		if p.sessionID != "" {
			result.Handled = true
			maxOffset := p.detailMaxOffset(request)
			currentOffset := min(max(result.Request.SessionDetailOffset, 0), maxOffset)
			nextOffset := max(0, currentOffset-1)
			if nextOffset != result.Request.SessionDetailOffset {
				result.Request.SessionDetailOffset = nextOffset
				result.Changed = true
			}
			return result
		}
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && len(p.hits) > 0 {
			currentSelection := min(max(request.HistorySelect, 0), len(p.hits)-1)
			nextSelection := max(0, currentSelection-1)
			if nextSelection != request.HistorySelect {
				result.Request.HistorySelect = nextSelection
				p.updateSelectionExport(&result.Request, nextSelection)
				result.Changed = true
			}
		}
		return result
	case "down":
		if p.sessionID != "" {
			result.Handled = true
			maxOffset := p.detailMaxOffset(request)
			currentOffset := min(max(result.Request.SessionDetailOffset, 0), maxOffset)
			nextOffset := min(maxOffset, currentOffset+1)
			if nextOffset != result.Request.SessionDetailOffset {
				result.Request.SessionDetailOffset = nextOffset
				result.Changed = true
			}
			return result
		}
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && len(p.hits) > 0 {
			currentSelection := min(max(request.HistorySelect, 0), len(p.hits)-1)
			nextSelection := min(len(p.hits)-1, currentSelection+1)
			if nextSelection != request.HistorySelect {
				result.Request.HistorySelect = nextSelection
				p.updateSelectionExport(&result.Request, nextSelection)
				result.Changed = true
			}
		}
		return result
	case "home":
		if p.sessionID != "" {
			result.Handled = true
			if result.Request.SessionDetailOffset != 0 {
				result.Request.SessionDetailOffset = 0
				result.Changed = true
			}
			return result
		}
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && request.HistorySelect != 0 {
			result.Request.HistorySelect = 0
			p.updateSelectionExport(&result.Request, 0)
			result.Changed = true
		}
		return result
	case "end":
		if p.sessionID != "" {
			result.Handled = true
			result.Request.SessionDetailOffset = p.detailMaxOffset(request)
			result.Changed = result.Request.SessionDetailOffset != request.SessionDetailOffset
			return result
		}
		if key.Type == tea.KeyRunes {
			break
		}
		result.Handled = true
		p.inputMode = false
		if p.sessionID == "" && len(p.hits) > 0 && request.HistorySelect != len(p.hits)-1 {
			result.Request.HistorySelect = len(p.hits) - 1
			p.updateSelectionExport(&result.Request, len(p.hits)-1)
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
			p.searched = true
			p.loading = true
			result.Changed, result.Action = true, PageActionLoad
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
		result.Request.SessionDetailOffset = 0
		result.Request.HistoryExportID = ""
		result.Request.HistoryExportToken = ""
		result.Changed, result.Action = true, PageActionLoad
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
			result.Request.SessionDetailOffset = 0
			result.Request.HistoryExportID = ""
			result.Request.HistoryExportToken = ""
			result.Changed, result.Action = true, PageActionLoad
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
		p.exportAttemptID = strconv.FormatUint(p.exportAttempt, 10)
		attemptID := p.exportAttemptID
		result.Request.HistoryExportID = target
		result.Request.HistoryExportToken = attemptID
		result.Changed, result.Action = true, PageActionExport
		return result
	}

	if p.inputMode {
		result.Handled = true
	}
	if !isPrintableKey(key) || p.sessionID != "" || !p.inputMode {
		return result
	}
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
func (p *HistorySearchPage) Load(request Request) (any, error) {
	if p.load == nil {
		return nil, errors.New("history search loader is unavailable")
	}
	data, err := p.load(request)
	if err != nil && p.reportError != nil {
		p.reportError(err)
	}
	return data, err
}

// Apply installs only the response for the current query/session. This keeps
// a slower older search from replacing a newer result after rapid typing.
func (p *HistorySearchPage) Apply(request Request, value any, err error) {
	if request.PageLoadToken != p.loadToken || request.HistoryQuery != p.query || request.HistorySessionID != p.sessionID {
		return
	}
	p.loading = false
	if err != nil {
		p.notIndexed = false
		p.errorText = "History search is unavailable. Try again."
		p.hits, p.warnings, p.detail = nil, nil, nil
		return
	}
	data, ok := value.(HistorySearchData)
	if !ok {
		p.notIndexed = false
		p.errorText = "History search returned an unreadable result."
		p.hits, p.warnings, p.detail = nil, nil, nil
		return
	}
	p.errorText, p.exportErrorText, p.exportText = "", "", ""
	p.notIndexed = data.NotIndexed
	if request.HistorySessionID == "" && strings.TrimSpace(request.HistoryQuery) != "" {
		p.searched = true
	}
	p.warnings = append([]string(nil), data.Search.Warnings...)
	p.hasMore = data.Search.HasMore
	p.hits = append([]SearchHit(nil), data.Search.Hits...)
	p.detail = data.Session
}

// Export writes the selected session through the CLI-owned callback.
func (p *HistorySearchPage) Export(request Request) (string, error) {
	if p.export == nil {
		return "", errors.New("history export is unavailable")
	}
	if request.HistoryExportID == "" {
		return "", errors.New("select a history result before exporting")
	}
	path, err := p.export(request)
	if err != nil && p.reportError != nil {
		p.reportError(err)
	}
	return path, err
}

// ApplyExport records a user-facing receipt for the completed export.
func (p *HistorySearchPage) ApplyExport(request Request, path string, err error) {
	if request.HistoryExportID != p.exportID || request.HistoryExportToken != p.exportAttemptID {
		return
	}
	p.exporting = false
	p.exportID = ""
	p.exportAttemptID = ""
	if err != nil {
		p.exportErrorText = "Export failed. Check the history index and try again."
		return
	}
	p.errorText, p.exportErrorText, p.exportText = "", "", "Exported to "+path
}

func (p *HistorySearchPage) exportTarget(request Request) string {
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
	p.exportErrorText = ""
	p.exportText = ""
	p.exportID = ""
	p.exportAttemptID = ""
}

func (p *HistorySearchPage) updateSelectionExport(request *Request, selectedIndex int) {
	if p.exporting && p.exportID != "" && selectedIndex >= 0 && selectedIndex < len(p.hits) && p.hits[selectedIndex].SessionID == p.exportID {
		request.HistoryExportID = p.exportID
		request.HistoryExportToken = p.exportAttemptID
		return
	}
	request.HistoryExportID = ""
	request.HistoryExportToken = ""
	p.clearExport()
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
	p.searched = false
	p.loading = false
	p.errorText = ""
	p.exportErrorText = ""
	p.exportText = ""
	p.exporting = false
	p.exportID = ""
}

func (p *HistorySearchPage) searchView(width int, context PageContext) string {
	lines := []string{
		context.Render.Palette.Header().Render("FIND IN HISTORY"),
		context.Render.Palette.Subtle().Render(truncateText("Search your indexed prompts by exact phrase.", width)),
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
	case !p.searched:
		lines = append(lines, context.Render.Palette.Subtle().Render(truncateText("Press enter to search.", width)))
	case len(p.hits) == 0:
		lines = append(lines, context.Render.Palette.Subtle().Render("No matching prompts."))
	default:
		selectedIndex := min(max(context.Request.HistorySelect, 0), len(p.hits)-1)
		visibleCount := max(1, (ContentHeight(context.Request.Height)-10)/2)
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
			snippet := truncateMarkedSnippet(oneLine(hit.Snippet), max(1, width-3), hit.SnippetMatchStart, hit.SnippetMatchEnd)
			plainStyle := style
			matchStyle := context.Render.Palette.Emphasis()
			if selected {
				matchStyle = style.Underline(true)
			}
			lines = append(lines, prefix+highlightSnippet(snippet, hit.SnippetMatchStart, hit.SnippetMatchEnd, plainStyle, matchStyle))
			project := oneLine(hit.Project)
			if project == "" {
				project = "unknown project"
			}
			metadata := oneLine(hit.Date) + " · " + oneLine(hit.Provider) + " · " + project
			metadata = truncateText(metadata, max(1, width-4))
			lines = append(lines, "    "+context.Render.Palette.Subtle().Render(metadata))
		}
		if end < len(p.hits) || p.hasMore {
			lines = append(lines, context.Render.Palette.Subtle().Render(truncateText("More results are available in `tokenomnom history search`.", width)))
		}
	}
	lines = appendPageStatus(lines, context.Render, p.warnings, p.exporting, p.exportText, p.exportErrorText)
	footer := "/ edit"
	if p.inputMode {
		footer = "type query  enter search  esc cancel"
	} else if p.query != "" || len(p.hits) > 0 {
		footer = "↑/↓  enter open  e export  / edit  esc"
	}
	lines = append(lines, "", context.Render.Palette.Subtle().Render(footer))
	return strings.Join(lines, "\n")
}

func (p *HistorySearchPage) detailView(width int, context PageContext) string {
	if p.loading {
		return strings.Join([]string{
			context.Render.Palette.Header().Render("SESSION DETAIL"),
			context.Render.Palette.Subtle().Render(oneLine(p.sessionID)),
			"",
			context.Render.Palette.Subtle().Render("Loading session…"),
			"",
			context.Render.Palette.Subtle().Render("esc back to search  e export"),
		}, "\n")
	}
	if p.errorText != "" || p.detail == nil {
		message := p.errorText
		if message == "" {
			message = "No session detail is available."
		}
		return strings.Join([]string{
			context.Render.Palette.Header().Render("SESSION DETAIL"),
			context.Render.Palette.Subtle().Render(oneLine(p.sessionID)),
			"",
			context.Render.Palette.Warning().Render(truncateText(oneLine(message), width)),
			"",
			context.Render.Palette.Subtle().Render("esc back to search  e export"),
		}, "\n")
	}
	notices := p.detailNotices()
	height := context.Height
	if height <= 0 {
		height = ContentHeight(context.Request.Height)
	}
	return tuipages.RenderHistorySearchSessionDetail(context.Render, *p.detail, width, height, context.Request.SessionDetailOffset, notices)
}

func (p *HistorySearchPage) detailNotices() []string {
	notices := make([]string, 0, 4)
	notices = append(notices, "esc back to search  e export")
	if p.exportText != "" {
		notices = append(notices, p.exportText)
	}
	if p.exporting {
		notices = append(notices, "Exporting session…")
	}
	if p.exportErrorText != "" {
		notices = append(notices, p.exportErrorText)
	}
	if len(p.warnings) > 0 {
		notices = append(notices, "Index note: "+p.warnings[0])
	}
	return notices
}

func (p *HistorySearchPage) detailMaxOffset(request Request) int {
	if p.detail == nil {
		return 0
	}
	width, height := ContentWidth(request.Width), ContentHeight(request.Height)
	render := theme.Context{Mode: theme.Plain, Width: width, Palette: theme.NewPalette(nil)}
	return tuipages.HistorySearchSessionDetailMaxOffset(render, *p.detail, width, height, p.detailNotices())
}

func appendPageStatus(lines []string, render theme.Context, warnings []string, exporting bool, exported, exportError string) []string {
	if exported != "" {
		lines = append(lines, "", render.Palette.Success().Render(oneLine(exported)))
	}
	if exporting {
		lines = append(lines, "", render.Palette.Subtle().Render("Exporting session…"))
	}
	if exportError != "" {
		lines = append(lines, "", render.Palette.Warning().Render(oneLine(exportError)))
	}
	if len(warnings) > 0 {
		lines = append(lines, "", render.Palette.Warning().Render("Index note: "+oneLine(warnings[0])))
	}
	return lines
}

func highlightSnippet(value, matchStart, matchEnd string, plainStyle, matchStyle lipgloss.Style) string {
	if matchStart == "" || matchEnd == "" {
		matchStart, matchEnd = string(history.SearchSnippetMatchStart), string(history.SearchSnippetMatchEnd)
	}
	var result strings.Builder
	for len(value) > 0 {
		start := strings.Index(value, matchStart)
		if start < 0 {
			result.WriteString(plainStyle.Render(value))
			break
		}
		if start > 0 {
			result.WriteString(plainStyle.Render(value[:start]))
		}
		value = value[start+len(matchStart):]
		end := strings.Index(value, matchEnd)
		if end < 0 {
			result.WriteString(matchStyle.Render(value))
			break
		}
		result.WriteString(matchStyle.Render(value[:end]))
		value = value[end+len(matchEnd):]
	}
	return result.String()
}

func truncateMarkedSnippet(value string, width int, matchStart, matchEnd string) string {
	if matchStart == "" || matchEnd == "" {
		matchStart, matchEnd = string(history.SearchSnippetMatchStart), string(history.SearchSnippetMatchEnd)
	}
	visible := strings.NewReplacer(matchStart, "", matchEnd, "").Replace(value)
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(visible) <= width {
		return value
	}
	remaining := max(0, width-lipgloss.Width("…"))
	var result strings.Builder
	position := 0
	for position < len(value) {
		start := strings.Index(value[position:], matchStart)
		if start < 0 {
			prefix, truncated := takeTextWidth(value[position:], remaining)
			result.WriteString(prefix)
			if truncated {
				result.WriteString("…")
			}
			return result.String()
		}
		start += position
		prefix, truncated := takeTextWidth(value[position:start], remaining)
		result.WriteString(prefix)
		remaining -= lipgloss.Width(prefix)
		if truncated {
			result.WriteString("…")
			return result.String()
		}
		result.WriteString(matchStart)
		contentStart := start + len(matchStart)
		end := strings.Index(value[contentStart:], matchEnd)
		if end < 0 {
			prefix, _ = takeTextWidth(value[contentStart:], remaining)
			result.WriteString(prefix)
			result.WriteString(matchEnd)
			result.WriteString("…")
			return result.String()
		}
		end += contentStart
		prefix, truncated = takeTextWidth(value[contentStart:end], remaining)
		result.WriteString(prefix)
		result.WriteString(matchEnd)
		if truncated {
			result.WriteString("…")
			return result.String()
		}
		remaining -= lipgloss.Width(prefix)
		position = end + len(matchEnd)
	}
	return result.String()
}

func takeTextWidth(value string, width int) (string, bool) {
	if width <= 0 {
		return "", value != ""
	}
	runes := []rune(value)
	currentWidth := 0
	for index, current := range runes {
		runeWidth := lipgloss.Width(string(current))
		if currentWidth+runeWidth > width {
			return string(runes[:index]), true
		}
		currentWidth += runeWidth
	}
	return value, false
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

var _ InteractivePage = (*HistorySearchPage)(nil)
var _ PageLoader = (*HistorySearchPage)(nil)
var _ PageLoadTracker = (*HistorySearchPage)(nil)
var _ PageExporter = (*HistorySearchPage)(nil)
