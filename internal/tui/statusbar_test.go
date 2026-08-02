package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestStatusBarRendersSyncIndexVaultAndSessions(t *testing.T) {
	model := loadedTestModel()
	model.syncFresh = true
	model.snapshot.StatusBar = StatusBar{
		History: HistoryStatus{Exists: true, Fresh: true},
		Vault: VaultStatus{
			Exists:           true,
			Files:            3,
			StoredBytes:      2 * 1024 * 1024,
			CompressionRatio: 2.5,
		},
		Sessions: 1_234,
	}

	view := model.View()
	for _, fragment := range []string{"● fresh", "index fresh", "vault 2.0 MiB 2.5x", "1,234 sessions"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("status bar missing %q:\n%s", fragment, view)
		}
	}
}

func TestStatusBarUsesAmberSyncAndHintsMissingSources(t *testing.T) {
	model := loadedTestModel()
	model.syncing = true
	if view := model.View(); !strings.Contains(view, "● syncing") || strings.Contains(view, "index") || strings.Contains(view, "vault") {
		t.Fatalf("empty status bar =\n%s", view)
	}

	model.snapshot.StatusBar.History.Hint = "not indexed"
	model.snapshot.StatusBar.Vault.Hint = "not initialized"
	view := model.View()
	for _, fragment := range []string{"● syncing", "index not indexed", "vault not initialized"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("status bar missing graceful hint %q:\n%s", fragment, view)
		}
	}
}

func TestStatusBarDoesNotClaimFreshBeforeASuccessfulSync(t *testing.T) {
	model := loadedTestModel()
	if view := model.View(); !strings.Contains(view, "● idle") || strings.Contains(view, "● fresh") {
		t.Fatalf("idle status bar claimed fresh data:\n%s", view)
	}
}

func TestStatusBarKeepsActionCompletionVisibleWithWarning(t *testing.T) {
	model := loadedTestModel()
	model.request.Width = 60
	model.status = "vault verified · 4 files checked"
	model.warning = "history index is stale"
	view := model.View()
	if !strings.Contains(view, "vault verified · 4 files checked") || !strings.Contains(view, "history") || !strings.Contains(view, "…") {
		t.Fatalf("action completion disappeared behind warning:\n%s", view)
	}
}

func TestStatusBarUsesCliOwnedStaleHint(t *testing.T) {
	model := loadedTestModel()
	model.syncFresh = true
	model.snapshot.StatusBar.History = HistoryStatus{Exists: true, Hint: "stale"}
	if view := model.View(); !strings.Contains(view, "index stale") {
		t.Fatalf("stale index status missing:\n%s", view)
	}
}

func TestStatusBarDoesNotInventHistoryState(t *testing.T) {
	model := loadedTestModel()
	model.snapshot.StatusBar.History = HistoryStatus{Exists: true}
	if view := model.View(); strings.Contains(view, "index pending") {
		t.Fatalf("status bar invented a pending state:\n%s", view)
	}
}

func TestStatusBarShowsSyncMetadataAsOneOptionalSegment(t *testing.T) {
	model := loadedTestModel()
	model.snapshot.StatusBar = StatusBar{LastSyncUnix: time.Now().Add(-2 * time.Minute).Unix(), Sources: 2, Models: 10}
	view := model.View()
	for _, fragment := range []string{"last sync", "2 sources", "10 models"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("sync metadata missing %q:\n%s", fragment, view)
		}
	}
}

func TestStatusBarWarningFitsTheCockpit(t *testing.T) {
	model := loadedTestModel()
	model.syncFresh = true
	model.request.Width, model.request.Height = 60, 18
	model.warning = "history index is stale: run 'tokenomnom history index' to refresh before searching"

	view := model.View()
	if !strings.Contains(view, "history index is stale") || !strings.Contains(view, "…") {
		t.Fatalf("warning was not preserved in the ambient bar:\n%s", view)
	}
	if strings.Contains(view, "……") {
		t.Fatalf("warning rendered a double ellipsis:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if len(lines) != 18 {
		t.Fatalf("warning view rendered %d lines, want 18", len(lines))
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != 60 {
			t.Fatalf("warning view line %d has width %d, want 60:\n%s", index+1, width, view)
		}
	}
}

func TestQuest110AfterSnapshot(t *testing.T) {
	model := loadedTestModel()
	model.syncFresh = true
	model.snapshot.StatusBar = StatusBar{
		History: HistoryStatus{Exists: true, Fresh: true},
		Vault: VaultStatus{
			Exists:           true,
			Files:            12,
			StoredBytes:      12 * 1024 * 1024,
			CompressionRatio: 2.8,
		},
		Sessions: 42,
	}
	t.Log("\n" + model.View())
}
