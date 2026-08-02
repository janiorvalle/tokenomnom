package pages

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// SessionPageData is the bounded history catalog snapshot used by the TUI.
// Raw transcript locations deliberately stay in the history store layer; the
// page only needs stable metadata and the first-prompt preview.
type SessionPageData struct {
	Sessions []historystore.CatalogSession
	// Costs is populated only for a selected session. Keeping attribution out
	// of the catalog list preserves the bounded, cheap dashboard load.
	Costs        map[string]SessionCost
	PromptPages  map[string]SessionPromptPage
	Projects     []ProjectOption
	ProjectStats []ProjectStat
	HasMore      bool
	NextCursor   string
	// Pending distinguishes an in-flight catalog load from an absent index.
	Pending        bool
	IndexAvailable bool
	Warning        string
	Location       *time.Location
}

// ProjectStat is the bounded 30-day project population used by the ambient
// rail. Shares are normalized by the dashboard loader before rendering.
type ProjectStat struct {
	Label    string
	Sessions int
	Share    float64
}

// ProjectOption keeps the exact catalog key separate from the label shown in
// the filter UI. A key can contain whitespace that should not be normalized
// before it reaches the history query.
type ProjectOption struct {
	Key   string
	Label string
}

// SessionViewState contains navigation state that belongs to the dashboard,
// rather than to the history database snapshot.
type SessionViewState struct {
	SelectedIndex int
	DetailID      string
	Costs         map[string]SessionCost
	PromptPages   map[string]SessionPromptPage
	Provider      string
	Project       string
	ProjectActive bool
	DateRange     string
	DetailOffset  int
	SelectLast    bool
	// Viewport dimensions classify the terminal, while width/height passed to
	// the renderer describe the content pane inside the shell.
	ViewportWidth  int
	ViewportHeight int
}

// RenderSessions renders either the bounded session list or the shared detail
// view for the selected session.
func RenderSessions(render theme.Context, data SessionPageData, state SessionViewState, width, height int) string {
	width = max(1, width)
	height = max(1, height)
	if state.DetailID != "" {
		for _, session := range data.Sessions {
			if session.SessionID == state.DetailID {
				costs := data.Costs
				promptPages := data.PromptPages
				if len(state.Costs) > 0 {
					costs = state.Costs
				}
				if len(state.PromptPages) > 0 {
					promptPages = state.PromptPages
				}
				prompts := promptPages[session.SessionID]
				return RenderSessionDetailForViewportWithPrompts(render, session, costs[session.SessionID], prompts.Prompts, prompts.HasMore, width, height, data.Location, state.DetailOffset, state.ViewportWidth, state.ViewportHeight)
			}
		}
	}

	viewportWidth, viewportHeight := state.ViewportWidth, state.ViewportHeight
	if viewportWidth <= 0 {
		viewportWidth = width
	}
	if viewportHeight <= 0 {
		viewportHeight = height
	}
	wide := viewportWidth >= 160 && viewportHeight >= 50
	if wide {
		return renderWideSessions(render, data, state, width, height)
	}
	return renderSessionsTable(render, data, state, width, height)
}

func renderWideSessions(render theme.Context, data SessionPageData, state SessionViewState, width, height int) string {
	const previewWidth = 57
	if width < previewWidth+4 {
		return renderSessionsTable(render, data, state, width, height)
	}
	selected := selectedSession(data.Sessions, state)
	preview := "No session is selected."
	costs := data.Costs
	if len(state.Costs) > 0 {
		costs = state.Costs
	}
	if selected != nil {
		cost, ok := costs[selected.SessionID]
		if !ok {
			cost.Status = "deferred"
		}
		preview = RenderSessionPreview(render, *selected, cost, previewWidth, max(1, height-1), data.Location)
	}
	leftWidth := max(1, width-previewWidth-2)
	return renderPageColumns(render, width, height,
		"SESSIONS", renderSessionsTableContent(render, data, state, leftWidth, max(1, height-1)), leftWidth,
		"SESSION PREVIEW", preview, previewWidth)
}

func renderSessionsTable(render theme.Context, data SessionPageData, state SessionViewState, width, height int) string {
	lines := []string{render.Palette.Header().Render("SESSIONS")}
	lines = append(lines, renderSessionsTableLines(render, data, state, width, max(1, height-1))...)
	return strings.Join(lines, "\n")
}

func renderSessionsTableContent(render theme.Context, data SessionPageData, state SessionViewState, width, height int) string {
	return strings.Join(renderSessionsTableLines(render, data, state, width, height), "\n")
}

func renderSessionsTableLines(render theme.Context, data SessionPageData, state SessionViewState, width, height int) []string {
	width = max(1, width)
	height = max(1, height)
	lines := []string{}
	project := ""
	if state.ProjectActive {
		project = ProjectLabel(state.Project, data.Projects)
	}
	filterLine := fmt.Sprintf("provider %s  ·  project %s  ·  range %s", displayFilter(state.Provider), displayFilter(project), displayFilter(state.DateRange))
	lines = append(lines, render.Palette.Subtle().Render(truncate(filterLine, width)), "")
	if data.Warning != "" {
		lines = append(lines, render.Palette.Warning().Render(truncate(data.Warning, width)), "")
	}
	if data.Pending {
		lines = append(lines, render.Palette.Subtle().Render(truncate("Loading sessions…", width)))
		return lines
	}
	if !data.IndexAvailable {
		lines = append(lines,
			render.Palette.Warning().Render(truncate("No history index is available.", width)),
			render.Palette.Subtle().Render(truncate("Run tokenomnom history index to browse sessions.", width)),
		)
		return lines
	}
	if len(data.Sessions) == 0 {
		lines = append(lines,
			render.Palette.Warning().Render(truncate("No indexed sessions match these filters.", width)),
			render.Palette.Subtle().Render(truncate("Run tokenomnom history index to refresh the history index.", width)),
		)
		return lines
	}

	count := fmt.Sprintf("%d sessions on this page", len(data.Sessions))
	if data.HasMore {
		count += "  ·  more available"
	}
	lines = append(lines, render.Palette.Subtle().Render(count), "")
	dateWidth, providerWidth, projectWidth, promptWidth := sessionColumnWidths(width)
	header := "  " + padRight("DATE", dateWidth) + "  " + padRight("PROVIDER", providerWidth) + "  " + padRight("PROJECT", projectWidth) + "  " + padLeft("PROMPTS", promptWidth) + "  FIRST PROMPT"
	lines = append(lines, render.Palette.Header().Render(truncate(header, width)))
	selected := clampIndex(state.SelectedIndex, len(data.Sessions))
	if state.SelectLast {
		selected = len(data.Sessions) - 1
	}
	trailingLines := 2 // blank line and the keyboard footer
	if data.HasMore {
		trailingLines += 2 // blank line and the overflow marker
	}
	rowCapacity := max(1, height-len(lines)-trailingLines)
	start, end := sessionVisibleWindow(len(data.Sessions), selected, rowCapacity)
	for index, session := range data.Sessions[start:end] {
		actualIndex := start + index
		lines = append(lines, renderSessionRow(render, session, actualIndex == selected, width, dateWidth, providerWidth, projectWidth, promptWidth, data.Location))
	}
	if data.HasMore {
		lines = append(lines, "", render.Palette.Subtle().Render(truncate("↓ more sessions", width)))
	}
	lines = append(lines, "", render.Palette.Subtle().Render(truncate("↑/↓ select  ·  enter open  ·  f project filter", width)))
	return lines
}

func selectedSession(sessions []historystore.CatalogSession, state SessionViewState) *historystore.CatalogSession {
	if len(sessions) == 0 {
		return nil
	}
	index := clampIndex(state.SelectedIndex, len(sessions))
	if state.SelectLast {
		index = len(sessions) - 1
	}
	selected := sessions[index]
	return &selected
}

func sessionVisibleWindow(length, selected, capacity int) (int, int) {
	capacity = max(1, capacity)
	if length <= capacity {
		return 0, length
	}
	selected = clampIndex(selected, length)
	start := max(0, min(selected-capacity+1, length-capacity))
	return start, start + capacity
}

func renderSessionRow(render theme.Context, session historystore.CatalogSession, selected bool, width, dateWidth, providerWidth, projectWidth, promptWidth int, location *time.Location) string {
	marker := "  "
	if selected {
		marker = render.Palette.Emphasis().Render("› ")
	}
	date := padRight(sessionDate(session.LastTimestamp, session.FirstTimestamp, location), dateWidth)
	provider := padRight(string(session.Provider), providerWidth)
	project := padRight(cleanInline(session.Project), projectWidth)
	prompts := padLeft(fmt.Sprintf("%d", session.LogicalPromptCount), promptWidth)
	previewWidth := max(1, width-2-dateWidth-providerWidth-projectWidth-promptWidth-10)
	preview := truncate(cleanInline(session.Preview), previewWidth)
	line := marker + date + "  " + render.Palette.Provider(string(session.Provider), 0).Render(provider) + "  " + truncate(project, projectWidth) + "  " + prompts + "  " + preview
	return truncate(line, width)
}

func sessionColumnWidths(width int) (int, int, int, int) {
	dateWidth, providerWidth, promptWidth := 10, 8, 7
	projectWidth := min(22, max(12, width/4))
	minimumPreviewWidth := 12
	fixed := 2 + dateWidth + providerWidth + promptWidth + 10
	if available := width - fixed - minimumPreviewWidth; available < projectWidth {
		projectWidth = max(8, available)
	}
	return dateWidth, providerWidth, projectWidth, promptWidth
}

func displayFilter(value string) string {
	if strings.TrimSpace(value) == "" {
		return "all"
	}
	return value
}

func sessionDate(primary, fallback *string, location *time.Location) string {
	value := primary
	if value == nil || *value == "" {
		value = fallback
	}
	if value == nil || *value == "" {
		return "unknown"
	}
	if parsed, err := time.Parse(time.RFC3339Nano, *value); err == nil {
		if location != nil {
			parsed = parsed.In(location)
		}
		return parsed.Format("2006-01-02")
	}
	if len(*value) >= 10 {
		return (*value)[:10]
	}
	return *value
}

func cleanInline(value string) string {
	value = cleanText(value)
	return strings.Join(strings.Fields(value), " ")
}

func cleanText(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != '\u007f' && (r < '\u0080' || r > '\u009f') {
			return r
		}
		return -1
	}, value)
}

func padRight(value string, width int) string {
	value = truncate(value, width)
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func padLeft(value string, width int) string {
	value = truncate(value, width)
	return strings.Repeat(" ", max(0, width-lipgloss.Width(value))) + value
}

func clampIndex(index, length int) int {
	if length == 0 {
		return 0
	}
	return min(max(index, 0), length-1)
}

// ProjectOptions returns stable project keys and display labels for a catalog.
// The history query already bounds the input page; this keeps keyboard
// filtering deterministic without doing unbounded work in the TUI.
func ProjectOptions(sessions []historystore.CatalogSession) []ProjectOption {
	values := make([]string, 0, len(sessions))
	for _, session := range sessions {
		values = append(values, session.Project)
	}
	return projectOptions(values)
}

// ProjectOptionsFromKeys turns the exact keys returned by the history store
// into stable filter options without normalizing the query values.
func ProjectOptionsFromKeys(values []string) []ProjectOption {
	return projectOptions(values)
}

func projectOptions(values []string) []ProjectOption {
	seen := make(map[string]bool, len(values))
	projects := make([]ProjectOption, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		projects = append(projects, ProjectOption{Key: value, Label: projectLabel(value)})
	}
	sort.SliceStable(projects, func(i, j int) bool {
		left, right := strings.ToLower(projects[i].Label), strings.ToLower(projects[j].Label)
		if left == right {
			return projects[i].Key < projects[j].Key
		}
		return left < right
	})
	return projects
}

// ProjectLabel returns the display label for a selected exact project key.
func ProjectLabel(value string, options []ProjectOption) string {
	for _, option := range options {
		if option.Key == value {
			return option.Label
		}
	}
	return projectLabel(value)
}

func projectLabel(value string) string {
	value = cleanInline(value)
	if value == "" {
		return "unknown"
	}
	return value
}
