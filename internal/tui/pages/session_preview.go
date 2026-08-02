package pages

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// SessionPreview is the small, read-only session summary used beside dense
// lists. Keeping it separate from the catalog and cost query lets other pages
// reuse the same pane without loading transcripts again.
type SessionPreview struct {
	SessionID       string
	Provider        string
	Project         string
	ProjectSource   string
	FirstTimestamp  *string
	LastTimestamp   *string
	Preview         string
	PromptCount     int
	OccurrenceCount int
	Tokens          int64
	Cost            pricing.Money
	PricedTokens    int64
	UnpricedTokens  int64
	Attribution     string
	Warning         string
	Location        *time.Location
}

// SessionPreviewFromCatalog adapts a history catalog row when a page has no
// session-level cost attribution available.
func SessionPreviewFromCatalog(session historystore.CatalogSession) SessionPreview {
	return SessionPreview{
		SessionID: session.SessionID, Provider: string(session.Provider), Project: session.Project,
		ProjectSource: string(session.ProjectSource), FirstTimestamp: session.FirstTimestamp,
		LastTimestamp: session.LastTimestamp, Preview: session.Preview,
		PromptCount: session.LogicalPromptCount, OccurrenceCount: session.OccurrenceCount,
	}
}

// SessionPreviewFromLedger adapts the priced session row used by the ledger.
func SessionPreviewFromLedger(session LedgerSession) SessionPreview {
	preview := SessionPreviewFromCatalog(session.CatalogSession)
	preview.Tokens = session.Tokens
	preview.Cost = session.Cost
	preview.PricedTokens = session.PricedTokens
	preview.UnpricedTokens = session.UnpricedTokens
	preview.Attribution = session.AttributionStatus
	preview.Warning = session.Warning
	return preview
}

// RenderSessionPreview renders a bounded preview pane. It performs no I/O and
// is therefore safe to repaint on every cursor move.
func RenderSessionPreview(render theme.Context, preview SessionPreview, width, height int) string {
	width, height = max(1, width), max(1, height)
	sessionID := cleanInline(preview.SessionID)
	lines := []string{
		previewTitle(render, "SESSION", width),
		fitPreviewLine(render.Palette.Emphasis().Render(truncate(sessionID, width)), width),
		previewRule(render, "FIRST PROMPT", width),
	}

	prompt := cleanText(preview.Preview)
	if strings.TrimSpace(prompt) == "" {
		prompt = "No human prompt text is available for this session."
	}
	promptLines := wrapPreview(prompt, max(1, width-2))
	summary := []string{
		previewRule(render, "OVERVIEW", width),
		previewKeyValue(render, "provider", preview.Provider, width),
		previewKeyValue(render, "project", fmt.Sprintf("%s (%s)", displayPreviewValue(preview.Project), displayPreviewValue(preview.ProjectSource)), width),
		previewKeyValue(render, "first", previewTimestamp(preview.FirstTimestamp, preview.Location), width),
		previewKeyValue(render, "last", previewTimestamp(preview.LastTimestamp, preview.Location), width),
		previewKeyValue(render, "prompts", fmt.Sprintf("%d logical · %d indexed", preview.PromptCount, preview.OccurrenceCount), width),
		previewRule(render, "COST & TOKENS", width),
		previewKeyValue(render, "tokens", commaInteger(preview.Tokens), width),
		previewKeyValue(render, "cost", formatMoney(preview.Cost, preview.PricedTokens, false, preview.UnpricedTokens > 0), width),
	}
	if preview.UnpricedTokens > 0 {
		summary = append(summary, previewKeyValue(render, "unpriced", commaInteger(preview.UnpricedTokens), width))
	}
	if preview.Attribution != "" {
		summary = append(summary, previewKeyValue(render, "attribution", preview.Attribution, width))
	}
	if preview.Warning != "" {
		summary = append(summary, render.Palette.Warning().Render(truncate("! "+cleanInline(preview.Warning), width)))
	}
	promptBudget := max(0, height-len(lines)-len(summary))
	if len(promptLines) > promptBudget {
		promptLines = promptLines[:promptBudget]
		if promptBudget > 0 {
			promptLines[promptBudget-1] = truncate(promptLines[promptBudget-1], max(1, width-1))
		}
	}
	lines = append(lines, promptLines...)
	lines = append(lines, summary...)
	if sessionID == "" {
		lines = []string{previewTitle(render, "SESSION", width)}
		if preview.Warning != "" {
			lines = append(lines, render.Palette.Warning().Render(truncate("! "+preview.Warning, width)))
		} else {
			lines = append(lines, render.Palette.Subtle().Render("Move to a session to preview it."))
		}
	}
	return strings.Join(fitPreviewLines(lines, width, height), "\n")
}

func previewTitle(render theme.Context, value string, width int) string {
	return previewRule(render, value, width)
}

func previewRule(render theme.Context, value string, width int) string {
	label := render.Palette.Header().Render(strings.ToUpper(value))
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitPreviewLine(label, width)
	}
	return fitPreviewLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func previewKeyValue(render theme.Context, key, value string, width int) string {
	return fitPreviewLine(render.Palette.Subtle().Render(padRight(key, 12))+" "+truncate(cleanInline(value), max(1, width-13)), width)
}

func wrapPreview(value string, width int) []string {
	value = cleanText(value)
	paragraphs := strings.Split(value, "\n")
	lines := []string{}
	for _, paragraph := range paragraphs {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				line = word
				continue
			}
			candidate := line + " " + word
			if lipgloss.Width(candidate) <= width {
				line = candidate
				continue
			}
			lines = append(lines, "  "+truncate(line, max(1, width-2)))
			line = word
		}
		if line != "" {
			lines = append(lines, "  "+truncate(line, max(1, width-2)))
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func fitPreviewLines(lines []string, width, height int) []string {
	if len(lines) > height {
		lines = lines[:height]
	}
	for index := range lines {
		lines[index] = fitPreviewLine(lines[index], width)
	}
	for len(lines) < height {
		lines = append(lines, strings.Repeat(" ", width))
	}
	return lines
}

func fitPreviewLine(value string, width int) string {
	return aligned(value, width, false)
}

func displayPreviewValue(value string) string {
	value = cleanInline(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func previewTimestamp(value *string, location *time.Location) string {
	if value == nil || *value == "" {
		return "unknown"
	}
	parsed, err := time.Parse(time.RFC3339Nano, *value)
	if err != nil {
		return displayPreviewValue(*value)
	}
	if location != nil {
		parsed = parsed.In(location)
	}
	return parsed.Format("2006-01-02 15:04")
}
