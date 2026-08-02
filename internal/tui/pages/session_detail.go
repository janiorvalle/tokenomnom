package pages

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

type sessionDetailRenderOptions struct {
	showPrompts     bool
	prompts         []SessionPrompt
	promptsHaveMore bool
	promptWarning   string
	footer          string
	notices         []string
	cost            SessionCost
	wide            bool
}

// RenderSessionDetail is shared by the sessions page, history search, and the
// future ledger drill-down. It intentionally renders metadata, not raw paths.
func RenderSessionDetail(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location, offset int) string {
	return renderSessionDetail(render, session, width, height, location, offset, sessionDetailRenderOptions{
		footer: "esc back to sessions",
	})
}

// RenderSessionDetailForViewport selects the full-page two-column detail only
// when the terminal, rather than the shell's content pane, is wide and tall.
func RenderSessionDetailForViewport(render theme.Context, session historystore.CatalogSession, cost SessionCost, width, height int, location *time.Location, offset, viewportWidth, viewportHeight int) string {
	return RenderSessionDetailForViewportWithPrompts(render, session, cost, nil, false, width, height, location, offset, viewportWidth, viewportHeight)
}

// RenderSessionDetailForViewportWithPrompts adds the indexed prompt list to a
// session detail without making the renderer read from the history store.
func RenderSessionDetailForViewportWithPrompts(render theme.Context, session historystore.CatalogSession, cost SessionCost, prompts []SessionPrompt, promptsHaveMore bool, width, height int, location *time.Location, offset, viewportWidth, viewportHeight int) string {
	return renderSessionDetailForViewportWithPromptPage(render, session, cost, SessionPromptPage{Prompts: prompts, HasMore: promptsHaveMore}, width, height, location, offset, viewportWidth, viewportHeight)
}

func renderSessionDetailForViewportWithPromptPage(render theme.Context, session historystore.CatalogSession, cost SessionCost, promptPage SessionPromptPage, width, height int, location *time.Location, offset, viewportWidth, viewportHeight int) string {
	return renderSessionDetail(render, session, width, height, location, offset, sessionDetailRenderOptions{
		footer: "esc back to sessions", cost: cost, showPrompts: true, prompts: promptPage.Prompts, promptsHaveMore: promptPage.HasMore, promptWarning: promptPage.Warning,
		wide: viewportWidth >= 160 && (viewportHeight >= 50 || viewportHeight == 0 && height >= 50),
	})
}

// RenderLedgerSessionDetail reuses the shared detail view while naming the
// ledger as the destination for the back action.
func RenderLedgerSessionDetail(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location, offset int) string {
	return renderSessionDetail(render, session, width, height, location, offset, sessionDetailRenderOptions{
		footer: "esc back to ledger",
	})
}

// RenderHistorySearchSessionDetail reuses the shared session-detail viewport
// and metadata layout while adding the prompt list returned by a search.
func RenderHistorySearchSessionDetail(render theme.Context, detail SessionDetail, width, height, offset int, notices []string) string {
	return renderSessionDetail(render, historySearchCatalogSession(detail), width, height, nil, offset, historySearchDetailOptions(detail, notices))
}

// RenderHistorySearchSessionDetailForViewport is the search page variant that
// can promote the detail to the dense two-column layout at wide+tall sizes.
func RenderHistorySearchSessionDetailForViewport(render theme.Context, detail SessionDetail, width, height, offset, viewportWidth, viewportHeight int, notices []string) string {
	options := historySearchDetailOptions(detail, notices)
	options.wide = viewportWidth >= 160 && (viewportHeight >= 50 || viewportHeight == 0 && height >= 50)
	return renderSessionDetail(render, historySearchCatalogSession(detail), width, height, nil, offset, options)
}

// HistorySearchSessionDetailMaxOffset reports the final scroll position for a
// history-search detail view using the same wrapped content as its renderer.
func HistorySearchSessionDetailMaxOffset(render theme.Context, detail SessionDetail, width, height int, notices []string) int {
	return max(0, len(sessionDetailLines(render, historySearchCatalogSession(detail), width, nil, historySearchDetailOptions(detail, notices)))-max(1, height))
}

// HistorySearchSessionDetailMaxOffsetForViewport mirrors the viewport chosen
// by RenderHistorySearchSessionDetailForViewport for keyboard scrolling.
func HistorySearchSessionDetailMaxOffsetForViewport(render theme.Context, detail SessionDetail, width, height, viewportWidth, viewportHeight int, notices []string) int {
	options := historySearchDetailOptions(detail, notices)
	options.wide = viewportWidth >= 160 && (viewportHeight >= 50 || viewportHeight == 0 && height >= 50)
	if options.wide {
		return max(0, wideSessionDetailLineCount(render, historySearchCatalogSession(detail), width, nil, options)-height)
	}
	return max(0, len(sessionDetailLines(render, historySearchCatalogSession(detail), width, nil, options))-max(1, height))
}

// SessionDetailMaxOffset reports the final scroll position for a detail view.
// Keeping this calculation in the renderer lets the page clamp keyboard state
// to the same wrapped content that the user sees.
func SessionDetailMaxOffset(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location) int {
	return max(0, len(sessionDetailLines(render, session, width, location, sessionDetailRenderOptions{footer: "esc back to sessions"}))-max(1, height))
}

// SessionDetailMaxOffsetForViewport keeps session-detail keys aligned with
// the wide renderer chosen for the current terminal.
func SessionDetailMaxOffsetForViewport(render theme.Context, session historystore.CatalogSession, cost SessionCost, width, height int, location *time.Location, viewportWidth, viewportHeight int) int {
	return SessionDetailMaxOffsetForViewportWithPrompts(render, session, cost, nil, false, width, height, location, viewportWidth, viewportHeight)
}

// SessionDetailMaxOffsetForViewportWithPrompts mirrors the prompt-aware
// renderer used when Sessions opens a full detail page.
func SessionDetailMaxOffsetForViewportWithPrompts(render theme.Context, session historystore.CatalogSession, cost SessionCost, prompts []SessionPrompt, promptsHaveMore bool, width, height int, location *time.Location, viewportWidth, viewportHeight int) int {
	return SessionDetailMaxOffsetForViewportWithPromptPage(render, session, cost, SessionPromptPage{Prompts: prompts, HasMore: promptsHaveMore}, width, height, location, viewportWidth, viewportHeight)
}

// SessionDetailMaxOffsetForViewportWithPromptPage mirrors the prompt-aware
// renderer while preserving prompt-loading failures in the scroll model.
func SessionDetailMaxOffsetForViewportWithPromptPage(render theme.Context, session historystore.CatalogSession, cost SessionCost, promptPage SessionPromptPage, width, height int, location *time.Location, viewportWidth, viewportHeight int) int {
	options := sessionDetailRenderOptions{footer: "esc back to sessions", cost: cost,
		showPrompts: true, prompts: promptPage.Prompts, promptsHaveMore: promptPage.HasMore, promptWarning: promptPage.Warning,
		wide: viewportWidth >= 160 && (viewportHeight >= 50 || viewportHeight == 0 && height >= 50)}
	if options.wide {
		return max(0, wideSessionDetailLineCount(render, session, width, location, options)-height)
	}
	return max(0, len(sessionDetailLines(render, session, width, location, options))-max(1, height))
}

// LedgerSessionDetailMaxOffset reports the final scroll position for the
// ledger-owned detail view.
func LedgerSessionDetailMaxOffset(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location) int {
	return max(0, len(sessionDetailLines(render, session, width, location, sessionDetailRenderOptions{footer: "esc back to ledger"}))-max(1, height))
}

func renderSessionDetail(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location, offset int, options sessionDetailRenderOptions) string {
	if options.wide {
		return renderWideSessionDetail(render, session, width, height, location, offset, options)
	}
	lines := sessionDetailLines(render, session, width, location, options)
	return strings.Join(detailViewport(render, lines, width, height, offset), "\n")
}

func historySearchCatalogSession(detail SessionDetail) historystore.CatalogSession {
	if detail.CatalogSession.SessionID != "" {
		session := detail.CatalogSession
		session.Preview = detail.Preview
		return session
	}
	first, last := detail.FirstDate, detail.LastDate
	return historystore.CatalogSession{
		SessionID: detail.SessionID, Provider: historyProvider(detail.Provider), Project: detail.Project,
		ProjectSource: projectSource(detail.ProjectSource), FirstTimestamp: &first, LastTimestamp: &last,
		Preview: detail.Preview, LogicalPromptCount: detail.PromptCount, OccurrenceCount: detail.OccurrenceCount,
	}
}

func historySearchDetailOptions(detail SessionDetail, notices []string) sessionDetailRenderOptions {
	return sessionDetailRenderOptions{
		showPrompts: true, prompts: detail.Prompts, promptsHaveMore: detail.HasMore,
		footer: "esc back to search", notices: notices, cost: detail.Cost,
	}
}

func sessionDetailLines(render theme.Context, session historystore.CatalogSession, width int, location *time.Location, options sessionDetailRenderOptions) []string {
	width = max(1, width)
	lines := []string{
		render.Palette.Header().Render("SESSION DETAIL"),
		render.Palette.Emphasis().Render(session.SessionID),
		"",
		render.Palette.Header().Render("FIRST PROMPT"),
	}
	if len(options.notices) > 0 {
		lines = append(lines[:3], renderNotices(render, options.notices, width)...)
		lines = append(lines, "", render.Palette.Header().Render("FIRST PROMPT"))
	}
	preview := cleanText(session.Preview)
	if strings.TrimSpace(preview) == "" {
		preview = "No human prompt text is available for this session."
	}
	lines = append(lines, strings.Split(indentWrapped(preview, max(1, width-2)), "\n")...)
	lines = append(lines, "")

	lines = append(lines,
		render.Palette.Header().Render("OVERVIEW"),
		metadataLine(render, "provider", string(session.Provider), width),
		metadataLine(render, "project", fmt.Sprintf("%s (%s)", session.Project, session.ProjectSource), width),
		metadataLine(render, "first", formatTimestamp(session.FirstTimestamp, location), width),
		metadataLine(render, "last", formatTimestamp(session.LastTimestamp, location), width),
		metadataLine(render, "prompts", fmt.Sprintf("%d logical", session.LogicalPromptCount), width),
		metadataLine(render, "occurrences", fmt.Sprintf("%d indexed", session.OccurrenceCount), width),
		metadataLine(render, "thread", fmt.Sprintf("%s (%s confidence)", session.ThreadKind, session.ThreadConfidence), width),
		"",
		render.Palette.Header().Render("PROVENANCE"),
		metadataLine(render, "preferred", session.PreferredRetrievalSource, width),
		metadataLine(render, "provider files", fmt.Sprintf("%d live · %d archive", session.Availability.ProviderLive, session.Availability.ProviderArchive), width),
		metadataLine(render, "vault snapshots", fmt.Sprintf("%d available", session.Availability.Vault), width),
		metadataLine(render, "relationships", relationshipSummary(session), width),
	)
	if options.cost.Status != "" || sessionCostAvailable(options.cost) {
		lines = append(lines, "")
		lines = append(lines, sessionCostLines(render, options.cost, width)...)
	}
	if options.showPrompts {
		lines = append(lines, "", render.Palette.Header().Render("PROMPTS"))
		if options.promptWarning != "" {
			lines = append(lines, render.Palette.Warning().Render(truncate(options.promptWarning, width)))
		} else if len(options.prompts) == 0 {
			lines = append(lines, render.Palette.Subtle().Render("No indexed prompts in this session."))
		}
		for _, prompt := range options.prompts {
			line := cleanInline(prompt.Date) + "  " + cleanInline(prompt.Snippet)
			lines = append(lines, truncate(line, width))
		}
		if options.promptsHaveMore {
			lines = append(lines, render.Palette.Subtle().Render(truncate("More prompts are available in the history index.", width)))
		}
	}
	footer := options.footer
	if footer == "" {
		footer = "esc back to sessions"
	}
	lines = append(lines, "", render.Palette.Subtle().Render(truncate(footer, width)))
	return lines
}

func renderWideSessionDetail(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location, offset int, options sessionDetailRenderOptions) string {
	content := wideSessionDetailContent(render, session, width, location, options)
	return strings.Join(fitPageLines(detailViewport(render, strings.Split(content, "\n"), width, height, offset), width, height), "\n")
}

func wideSessionDetailLineCount(render theme.Context, session historystore.CatalogSession, width int, location *time.Location, options sessionDetailRenderOptions) int {
	content := wideSessionDetailContent(render, session, width, location, options)
	return len(strings.Split(content, "\n"))
}

func wideSessionDetailContent(render theme.Context, session historystore.CatalogSession, width int, location *time.Location, options sessionDetailRenderOptions) string {
	leftWidth, rightWidth := detailColumnWidths(width)
	left := wideFirstPromptLines(render, session, leftWidth, options)
	right := wideSessionMetadataLines(render, session, rightWidth, location, options.cost)
	naturalHeight := max(len(left), len(right)) + 1
	return renderPageColumns(render, width, naturalHeight,
		"FIRST PROMPT", strings.Join(left, "\n"), leftWidth,
		"SESSION "+session.SessionID, strings.Join(right, "\n"), rightWidth)
}

func detailColumnWidths(width int) (int, int) {
	right := min(57, max(1, width-4))
	left := max(1, width-right-2)
	return left, right
}

func wideFirstPromptLines(render theme.Context, session historystore.CatalogSession, width int, options sessionDetailRenderOptions) []string {
	lines := []string{}
	for _, notice := range options.notices {
		lines = append(lines, render.Palette.Subtle().Render(truncate(cleanInline(notice), width)))
	}
	preview := cleanText(session.Preview)
	if strings.TrimSpace(preview) == "" {
		preview = "No human prompt text is available for this session."
	}
	lines = append(lines, strings.Split(indentWrapped(preview, max(1, width-2)), "\n")...)
	lines = append(lines, "", pageSectionTitle(render, "PROMPTS", width))
	if options.promptWarning != "" {
		lines = append(lines, render.Palette.Warning().Render(truncate(options.promptWarning, width)))
	} else if len(options.prompts) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No indexed prompts in this session."))
	}
	for _, prompt := range options.prompts {
		lines = append(lines, truncate(cleanInline(prompt.Date)+"  "+cleanInline(prompt.Snippet), width))
	}
	if options.promptsHaveMore {
		lines = append(lines, render.Palette.Subtle().Render(truncate("More prompts are available in the history index.", width)))
	}
	lines = append(lines, "", render.Palette.Subtle().Render(truncate(options.footer, width)))
	return lines
}

func wideSessionMetadataLines(render theme.Context, session historystore.CatalogSession, width int, location *time.Location, cost SessionCost) []string {
	lines := []string{render.Palette.Emphasis().Render(session.SessionID), pageSectionTitle(render, "OVERVIEW", width)}
	lines = append(lines,
		metadataLine(render, "provider", string(session.Provider), width),
		metadataLine(render, "project", fmt.Sprintf("%s (%s)", session.Project, session.ProjectSource), width),
		metadataLine(render, "first", formatTimestamp(session.FirstTimestamp, location), width),
		metadataLine(render, "last", formatTimestamp(session.LastTimestamp, location), width),
		metadataLine(render, "span", sessionSpan(session, location), width),
		metadataLine(render, "prompts", fmt.Sprintf("%d logical", session.LogicalPromptCount), width),
		metadataLine(render, "thread", fmt.Sprintf("%s (%s confidence)", session.ThreadKind, session.ThreadConfidence), width),
		"", pageSectionTitle(render, "PROVENANCE", width),
		metadataLine(render, "preferred", session.PreferredRetrievalSource, width),
		metadataLine(render, "provider files", fmt.Sprintf("%d live · %d archive", session.Availability.ProviderLive, session.Availability.ProviderArchive), width),
		metadataLine(render, "vault snapshots", fmt.Sprintf("%d available", session.Availability.Vault), width),
		metadataLine(render, "relationships", relationshipSummary(session), width),
		"",
	)
	lines = append(lines, sessionCostLines(render, cost, width)...)
	return lines
}

func sessionCostLines(render theme.Context, cost SessionCost, width int) []string {
	lines := []string{pageSectionTitle(render, "COST & TOKENS", width)}
	lines = append(lines, compactSessionCostLines(render, cost, width)...)
	lines = append(lines, pageSectionTitle(render, "MODELS", width))
	if !sessionCostAvailable(cost) || len(cost.Models) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No per-model attribution available."))
		return lines
	}
	lines = append(lines, render.Palette.Subtle().Render(truncate("DATE       PROVID MODEL              TOKENS     COST", width)))
	for _, model := range cost.Models {
		date := truncate(cleanInline(model.Date), 10)
		provider := truncate(cleanInline(model.Provider), 7)
		name := truncate(cleanInline(model.Model), 16)
		line := fmt.Sprintf("%-10s %-7s %-16s %9s %8s", date, provider, name, commaSessionInteger(model.TotalTokens), formatSessionMoney(model.CostUSD))
		lines = append(lines, truncate(line, width))
	}
	return lines
}

func compactSessionCostLines(render theme.Context, cost SessionCost, width int) []string {
	if cost.Status == "deferred" {
		return []string{
			metadataLine(render, "tokens", "open detail to attribute", width),
			metadataLine(render, "cost", "open detail to attribute", width),
		}
	}
	if !sessionCostAvailable(cost) {
		return []string{metadataLine(render, "tokens", "not attributed", width), metadataLine(render, "cost", "—", width)}
	}
	status := cost.Status
	if status == "" {
		status = "available"
	}
	return []string{
		metadataLine(render, "tokens", commaSessionInteger(cost.TotalTokens), width),
		metadataLine(render, "priced", commaSessionInteger(cost.PricedTokens), width),
		metadataLine(render, "unpriced", commaSessionInteger(cost.UnpricedTokens), width),
		metadataLine(render, "cost", formatSessionMoney(cost.CostUSD), width),
		metadataLine(render, "status", status, width),
	}
}

func sessionCostAvailable(cost SessionCost) bool {
	return (cost.Status != "" && cost.Status != "unavailable" && cost.Status != "deferred") ||
		cost.TotalTokens > 0 || cost.PricedTokens > 0 || cost.UnpricedTokens > 0 || len(cost.Models) > 0
}

func commaSessionInteger(value int64) string {
	if value == 0 {
		return "—"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := fmt.Sprintf("%d", value)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	if negative {
		return "-" + digits
	}
	return digits
}

func formatSessionMoney(value float64) string {
	return fmt.Sprintf("$%.2f", value)
}

func sessionSpan(session historystore.CatalogSession, location *time.Location) string {
	if session.FirstTimestamp == nil || session.LastTimestamp == nil {
		return "unknown"
	}
	first, firstErr := time.Parse(time.RFC3339Nano, *session.FirstTimestamp)
	last, lastErr := time.Parse(time.RFC3339Nano, *session.LastTimestamp)
	if firstErr != nil || lastErr != nil || last.Before(first) {
		return "unknown"
	}
	if location != nil {
		first, last = first.In(location), last.In(location)
	}
	duration := last.Sub(first).Round(time.Minute)
	if duration < time.Minute {
		return "under 1m"
	}
	return duration.String()
}

func pageSectionTitle(render theme.Context, title string, width int) string {
	return pageBandTitle(render, title, width)
}

func pageBandTitle(render theme.Context, title string, width int) string {
	label := render.Palette.Header().Render(strings.ToUpper(strings.TrimSpace(title)))
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitPageLine(label, width)
	}
	return fitPageLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

// PageBandTitle, MetadataLine, and IndentWrapped are small view primitives
// shared by the search page without exposing the detail renderer itself.
func PageBandTitle(render theme.Context, title string, width int) string {
	return pageBandTitle(render, title, width)
}

func MetadataLine(render theme.Context, label, value string, width int) string {
	return metadataLine(render, label, value, width)
}

func IndentWrapped(value string, width int) string {
	return indentWrapped(value, width)
}

func CleanText(value string) string {
	return cleanText(value)
}

func CleanInline(value string) string {
	return cleanInline(value)
}

func renderPageColumns(render theme.Context, width, height int, leftTitle, left string, leftWidth int, rightTitle, right string, rightWidth int) string {
	width, height = max(1, width), max(1, height)
	gap := 2
	if leftWidth+rightWidth+gap != width {
		rightWidth = max(1, min(rightWidth, width-gap-1))
		leftWidth = max(1, width-gap-rightWidth)
	}
	leftLines := renderPagePane(render, leftTitle, left, leftWidth, height)
	rightLines := renderPagePane(render, rightTitle, right, rightWidth, height)
	rows := make([]string, height)
	for index := range rows {
		rows[index] = fitPageLine(leftLines[index]+strings.Repeat(" ", gap)+rightLines[index], width)
	}
	return strings.Join(rows, "\n")
}

// RenderSearchColumns exposes the shared exact-fill two-pane primitive to the
// parent TUI package without creating an import cycle.
func RenderSearchColumns(render theme.Context, width, height int, left string, leftWidth int, right string, rightWidth int) string {
	return renderPageColumns(render, width, height, "SEARCH RESULTS", left, leftWidth, "MATCH PREVIEW", right, rightWidth)
}

func renderPagePane(render theme.Context, title, content string, width, height int) []string {
	lines := []string{pageBandTitle(render, title, width)}
	lines = append(lines, strings.Split(strings.TrimSuffix(content, "\n"), "\n")...)
	return fitPageLines(lines, width, height)
}

func fitPageLines(lines []string, width, height int) []string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for index := range lines {
		lines[index] = fitPageLine(lines[index], width)
	}
	return lines
}

func fitPageBlock(value string, width, height int) string {
	return strings.Join(fitPageLines(strings.Split(strings.TrimSuffix(value, "\n"), "\n"), max(1, width), max(1, height)), "\n")
}

func fitPageLine(value string, width int) string {
	width = max(1, width)
	value = ansi.Truncate(value, width, "")
	return value + strings.Repeat(" ", max(0, width-lipgloss.Width(value)))
}

func renderNotices(render theme.Context, notices []string, width int) []string {
	lines := make([]string, 0, len(notices))
	for _, notice := range notices {
		lines = append(lines, render.Palette.Subtle().Render(truncate(cleanInline(notice), width)))
	}
	return lines
}

func historyProvider(value string) history.Provider {
	if value == "" {
		return history.Provider("")
	}
	return history.Provider(value)
}

func projectSource(value string) history.ProjectSource {
	if value == "" {
		return history.ProjectSourceUnknown
	}
	return history.ProjectSource(value)
}

func detailViewport(render theme.Context, lines []string, width, height, offset int) []string {
	height = max(1, height)
	maxOffset := max(0, len(lines)-height)
	offset = min(max(0, offset), maxOffset)
	end := min(len(lines), offset+height)
	viewport := append([]string(nil), lines[offset:end]...)
	if offset > 0 && len(viewport) > 0 {
		viewport[0] = render.Palette.Subtle().Render(truncate("↑ more above", width))
	}
	if end < len(lines) && len(viewport) > 0 {
		viewport[len(viewport)-1] = render.Palette.Subtle().Render(truncate("↓ more below", width))
	}
	return viewport
}

func metadataLine(render theme.Context, label, value string, width int) string {
	label = strings.ToUpper(label)
	value = cleanInline(value)
	if value == "" {
		value = "unknown"
	}
	line := render.Palette.Subtle().Render(fmt.Sprintf("%-16s", label)) + "  " + value
	return truncate(line, width)
}

func relationshipSummary(session historystore.CatalogSession) string {
	if len(session.Relationships) == 0 {
		return "none recorded"
	}
	if session.RelationshipsTruncated {
		return fmt.Sprintf("%d recorded · more omitted", len(session.Relationships))
	}
	return fmt.Sprintf("%d recorded", len(session.Relationships))
}

func formatTimestamp(value *string, location *time.Location) string {
	if value == nil || *value == "" {
		return "unknown"
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return *value
	}
	if location != nil {
		parsed = parsed.In(location)
	}
	return parsed.Format("2006-01-02 15:04 MST")
}

func indentWrapped(value string, width int) string {
	paragraphs := strings.Split(value, "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		wrapped := wrapText(strings.TrimSpace(paragraph), max(1, width-2))
		if wrapped == "" {
			lines = append(lines, "  ")
			continue
		}
		for _, line := range strings.Split(wrapped, "\n") {
			lines = append(lines, "  "+line)
		}
	}
	return strings.Join(lines, "\n")
}

func wrapText(value string, width int) string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return ""
	}
	lines := []string{}
	current := ""
	for _, word := range words {
		if lipgloss.Width(word) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			for lipgloss.Width(word) > width {
				chunk, rest := splitTextWidth(word, width)
				lines = append(lines, chunk)
				word = rest
			}
		}
		if current == "" {
			current = word
			continue
		}
		if lipgloss.Width(current)+1+lipgloss.Width(word) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
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
	if end == 0 {
		end = 1
	}
	return string(runes[:end]), string(runes[end:])
}
