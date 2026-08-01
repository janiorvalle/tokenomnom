package pages

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

type sessionDetailRenderOptions struct {
	showPrompts     bool
	prompts         []SessionPrompt
	promptsHaveMore bool
	footer          string
	notices         []string
}

// RenderSessionDetail is shared by the sessions page, history search, and the
// future ledger drill-down. It intentionally renders metadata, not raw paths.
func RenderSessionDetail(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location, offset int) string {
	return renderSessionDetail(render, session, width, height, location, offset, sessionDetailRenderOptions{
		footer: "esc back to sessions",
	})
}

// RenderHistorySearchSessionDetail reuses the shared session-detail viewport
// and metadata layout while adding the prompt list returned by a search.
func RenderHistorySearchSessionDetail(render theme.Context, detail SessionDetail, width, height, offset int, notices []string) string {
	first, last := detail.FirstDate, detail.LastDate
	session := historystore.CatalogSession{
		SessionID: detail.SessionID, Provider: historyProvider(detail.Provider), Project: detail.Project,
		ProjectSource: projectSource(detail.ProjectSource), FirstTimestamp: &first, LastTimestamp: &last,
		Preview: detail.Preview, LogicalPromptCount: detail.PromptCount, OccurrenceCount: detail.OccurrenceCount,
	}
	return renderSessionDetail(render, session, width, height, nil, offset, sessionDetailRenderOptions{
		showPrompts:     true,
		prompts:         detail.Prompts,
		promptsHaveMore: detail.HasMore,
		footer:          "esc back to search",
		notices:         notices,
	})
}

// SessionDetailMaxOffset reports the final scroll position for a detail view.
// Keeping this calculation in the renderer lets the page clamp keyboard state
// to the same wrapped content that the user sees.
func SessionDetailMaxOffset(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location) int {
	return max(0, len(sessionDetailLines(render, session, width, location, sessionDetailRenderOptions{footer: "esc back to sessions"}))-max(1, height))
}

func renderSessionDetail(render theme.Context, session historystore.CatalogSession, width, height int, location *time.Location, offset int, options sessionDetailRenderOptions) string {
	lines := sessionDetailLines(render, session, width, location, options)
	return strings.Join(detailViewport(render, lines, width, height, offset), "\n")
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
		lines = append(lines[:3], append([]string{""}, renderNotices(render, options.notices, width)...)...)
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
	if options.showPrompts {
		lines = append(lines, "", render.Palette.Header().Render("PROMPTS"))
		if len(options.prompts) == 0 {
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
