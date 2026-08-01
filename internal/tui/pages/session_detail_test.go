package pages

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
)

func TestRenderSessionDetailIncludesPromptCountsAndProvenance(t *testing.T) {
	first, last := "2026-07-21T12:00:00Z", "2026-07-21T13:00:00Z"
	view := RenderSessionDetail(testRender(), historystore.CatalogSession{
		SessionID: "ses_detail", Provider: history.ProviderCodex, Project: "tokenomnom",
		ProjectSource: history.ProjectSourceGit, FirstTimestamp: &first, LastTimestamp: &last,
		Preview: "do not implement this until the index is ready", LogicalPromptCount: 7,
		OccurrenceCount: 11, PreferredRetrievalSource: "provider-live",
		Availability: historystore.Availability{ProviderLive: 1, ProviderArchive: 2, Vault: 3},
		ThreadKind:   history.ThreadRoot, ThreadConfidence: history.ConfidenceExact,
	}, 70, 40, time.UTC, 0)
	for _, fragment := range []string{"SESSION DETAIL", "ses_detail", "FIRST PROMPT", "do not implement", "7 logical", "11 indexed", "PROVENANCE", "provider-live", "1 live", "3 available", "esc back"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("detail missing %q:\n%s", fragment, view)
		}
	}
	for index, line := range strings.Split(view, "\n") {
		if lipgloss.Width(line) > 70 {
			t.Fatalf("line %d exceeds detail width: %d\n%s", index+1, lipgloss.Width(line), view)
		}
	}
}
