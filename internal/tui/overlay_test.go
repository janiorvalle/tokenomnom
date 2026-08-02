package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tuipages "github.com/janiorvalle/tokenomnom/internal/tui/pages"
)

func TestQuest152HelpOverlayUsesGroupedWideColumnsAndKeepsTheDeskVisible(t *testing.T) {
	model := quest152RichPageModel(DailyPageID, 192, 66)
	model.help = true
	view := model.View()
	assertFullWindowFrame(t, view, 192, 66, "wide help")
	for _, fragment := range []string{"HELP", "NAVIGATE", "PAGES", "ACTIONS", "SYSTEM", "tokenomnom", "DAILY"} {
		if !strings.Contains(view, fragment) {
			t.Fatalf("wide help missing %q:\n%s", fragment, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "NAVIGATE") && strings.Contains(line, "ACTIONS") {
			return
		}
	}
	t.Fatalf("wide help did not render two grouped columns:\n%s", view)
}

func TestQuest152ShortHelpFitsAndKeepsEveryGroupVisible(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {60, 18}} {
		model := quest152RichPageModel(DailyPageID, size.width, size.height)
		model.help = true
		view := model.View()
		assertFullWindowFrame(t, view, size.width, size.height, fmt.Sprintf("short help %dx%d", size.width, size.height))
		for _, fragment := range []string{"NAVIGATE", "PAGES", "ACTIONS", "SYSTEM", "enter · esc", "h / l · j / k", "p · r"} {
			if !strings.Contains(view, fragment) {
				t.Fatalf("short help %dx%d missing %q:\n%s", size.width, size.height, fragment, view)
			}
		}
		lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
		if len(lines) > size.height {
			t.Fatalf("short help %dx%d rendered %d lines, want at most %d:\n%s", size.width, size.height, len(lines), size.height, view)
		}
	}
}

func TestQuest152PaletteUsesViewportWidthAndBodyHeight(t *testing.T) {
	for _, test := range []struct {
		width, height, items, wantRows, wantWidth int
	}{
		{width: 192, height: 66, items: 20, wantRows: 20, wantWidth: 90},
		{width: 80, height: 24, items: 20, wantRows: 10, wantWidth: 72},
		{width: 80, height: 24, items: 0, wantRows: 1, wantWidth: 72},
	} {
		layout := newCockpitLayout(test.width, test.height)
		if got := paletteWidth(test.width); got != test.wantWidth {
			t.Fatalf("palette width %dx%d=%d, want %d", test.width, test.height, got, test.wantWidth)
		}
		if got := paletteRows(layout, test.items); got != test.wantRows {
			t.Fatalf("palette rows %dx%d items=%d=%d, want %d", test.width, test.height, test.items, got, test.wantRows)
		}
	}
}

func TestQuest152ReferenceFramesAndNoVoidSweep(t *testing.T) {
	pageIDs := []PageID{
		DailyPageID, LedgerPageID, ModelsPageID, HeatmapPageID,
		SessionsPageID, HistorySearchPageID, VaultPageID, SystemPageID,
	}
	for _, pageID := range pageIDs {
		for _, size := range []struct{ width, height int }{{192, 66}, {120, 40}, {80, 24}} {
			model := quest152RichPageModel(pageID, size.width, size.height)
			view := model.View()
			assertFullWindowFrame(t, view, size.width, size.height, string(pageID))
			writeQuest152EvidenceFrame(t, pageID, size.width, size.height, view)
			if size.width == 192 && size.height == 66 {
				body := model.contentView(newCockpitLayout(size.width, size.height))
				if got := maxBlankContentRun(body); got > 3 {
					t.Errorf("%s has %d consecutive blank content rows", pageID, got)
				}
				t.Logf("FRAME: %s %dx%d\nSource: internal/tui/overlay_test.go::TestQuest152ReferenceFramesAndNoVoidSweep\nCommand: go test ./internal/tui -run TestQuest152ReferenceFramesAndNoVoidSweep -count=1 -v\n\n%s", pageID, size.width, size.height, view)
			}
		}
	}
}

func TestQuest152KeyboardOnlyOverlayWalk(t *testing.T) {
	model := quest152RichPageModel(DailyPageID, 192, 66)
	for step := 0; step < len(model.router.Pages()); step++ {
		model = updateKeyForTest(t, model, "?")
		if !model.help {
			t.Fatalf("help did not open on step %d", step)
		}
		model = updateKeyForTest(t, model, "esc")
		if model.help {
			t.Fatalf("esc did not close help on step %d", step)
		}
		if step+1 < len(model.router.Pages()) {
			model = updateKeyForTest(t, model, "tab")
		}
	}
	if model.activePageID() != SessionsPageID {
		t.Fatalf("keyboard walk stopped on %s, want %s", model.activePageID(), SessionsPageID)
	}
	writeQuest152EvidenceFrameForTest(t, "keyboard-walk-final", model.request.Width, model.request.Height, model.View(), "TestQuest152KeyboardOnlyOverlayWalk")
	t.Logf("Source: internal/tui/overlay_test.go::TestQuest152KeyboardOnlyOverlayWalk\nCommand: go test ./internal/tui -run TestQuest152KeyboardOnlyOverlayWalk -count=1 -v\n\nwalked %d pages with ? / esc / tab", len(model.router.Pages()))
}

func TestQuest152READMEFrameEvidence(t *testing.T) {
	dashboard := quest152RichPageModel(DailyPageID, 192, 66)
	writeQuest152EvidenceFrameForTest(t, "readme-dashboard", 192, 66, dashboard.View(), "TestQuest152READMEFrameEvidence")

	detail := quest152HistoryDetailModel(192, 66)
	writeQuest152EvidenceFrameForTest(t, "readme-history-detail", 192, 66, detail.View(), "TestQuest152READMEFrameEvidence")
}

func TestQuest152OverlayEvidence(t *testing.T) {
	help := quest152RichPageModel(DailyPageID, 192, 66)
	help.help = true
	writeQuest152EvidenceFrameForTest(t, "help-wide", 192, 66, help.View(), "TestQuest152OverlayEvidence")

	palette := quest152RichPageModel(DailyPageID, 192, 66)
	palette = openPaletteForTest(t, palette)
	writeQuest152EvidenceFrameForTest(t, "palette-wide", 192, 66, palette.View(), "TestQuest152OverlayEvidence")
}

func TestQuest152NoVoidSweepTreatsStyledGlyphRowsAsVoid(t *testing.T) {
	for _, line := range []string{"\x1b[2m·\x1b[0m", "\x1b[2m—\x1b[0m", " ···· "} {
		if !isDenseVoidContentLine(line) {
			t.Errorf("glyph-only line %q was not treated as void", line)
		}
	}
	if isDenseVoidContentLine("  recent activity") {
		t.Fatal("content line was treated as void")
	}
}

func paletteRows(layout cockpitLayout, itemCount int) int {
	if itemCount == 0 {
		return 1
	}
	return min(itemCount, max(1, layout.bodyHeight-8))
}

func maxBlankContentRun(value string) int {
	maximum, current := 0, 0
	for _, line := range strings.Split(value, "\n") {
		if isDenseVoidContentLine(line) {
			current++
			maximum = max(maximum, current)
			continue
		}
		current = 0
	}
	return maximum
}

func isDenseVoidContentLine(line string) bool {
	trimmed := strings.TrimSpace(ansi.Strip(line))
	if trimmed == "" {
		return true
	}
	runes := []rune(trimmed)
	if len(runes) == 0 {
		return true
	}
	for _, r := range runes[1:] {
		if r != runes[0] {
			return false
		}
	}
	switch runes[0] {
	case '·', '—':
		return true
	default:
		return false
	}
}

func quest152RichPageModel(pageID PageID, width, height int) Model {
	model := realisticEvidenceModel()
	model.request.Width, model.request.Height = width, height
	model.render.Width = width

	switch pageID {
	case DailyPageID:
		layout := newCockpitLayout(width, height)
		render := model.render
		render.Width = layout.paneWidth
		model.snapshot.Views[DailyTab] = tuipages.RenderDaily(render, quest146DailyFrameData(), width, height, layout.bodyHeight, 0)
		model.router.Select(DailyPageID)
	case LedgerPageID:
		model.router.Select(LedgerPageID)
		model.request.Ledger = tuipages.State{Zoom: tuipages.ZoomMonth, Year: 2026, Cursor: -1}
		model.snapshot.Ledger = quest147LedgerPeriodsFrameData()
	case ModelsPageID:
		layout := newCockpitLayout(width, height)
		render := model.render
		render.Width = layout.paneWidth
		model.snapshot.Views[ModelsTab] = tuipages.RenderModels(render, quest148ModelsFrameData(), tuipages.ModelsViewport{
			Width: layout.paneWidth, Height: layout.bodyHeight,
			Wide: layout.tiers.Width == WidthWide, Tall: layout.tiers.Height == HeightTall,
			Standard: layout.tiers.Width == WidthStandard,
		})
		model.router.Select(ModelsPageID)
	case HeatmapPageID:
		layout := newCockpitLayout(width, height)
		render := model.render
		render.Width = width
		model.snapshot.Views[HeatmapTab] = tuipages.RenderHeatmap(render, heatmapEvidenceData(), layout.paneWidth, layout.bodyHeight)
		model.router.Select(HeatmapPageID)
	case SessionsPageID:
		model.snapshot.Sessions = quest150Sessions()
		model.router.Select(SessionsPageID)
	case HistorySearchPageID:
		page := NewHistorySearchPage(HistorySearchOptions{})
		page.query, page.searched, page.hits = "prompt", true, quest150SearchHits()
		page.preview = &tuipages.SearchPreview{PromptID: "prm_04", Detail: &tuipages.SessionDetail{
			SessionID: "ses_preview", Provider: "codex", Project: "tokenomnom", Preview: "first prompt", Prompts: quest150Prompts(),
		}}
		model.router = newRouter(page)
		model.router.Select(HistorySearchPageID)
		model.request.HistoryQuery = page.query
	case VaultPageID, SystemPageID:
		model.router = newRouter(NewVaultPage(), NewSystemPage())
		model.snapshot.Vault = quest151RichVaultFrameData()
		pricingRows := make([]tuipages.PricingRow, 0, 18)
		for index := 0; index < 18; index++ {
			pricingRows = append(pricingRows, tuipages.PricingRow{
				Model: fmt.Sprintf("model-%02d", index), BaseInput: "$1.00", CacheRead: "$0.10", Write5m: "$1.25", Write1h: "$2.00", Output: "$5.00",
				Status: "published", Effective: "always", Source: "embedded",
			})
		}
		model.snapshot.System = quest151RichSystemFrameData(pricingRows)
		model.snapshot.StatusBar = StatusBar{Sources: 18, Models: 30}
		model.router.Select(pageID)
	}
	return model
}

func quest152HistoryDetailModel(width, height int) Model {
	model := quest152RichPageModel(SessionsPageID, width, height)
	data := quest150BaseSessionData()
	prompts := quest150Prompts()
	data.PromptPages = map[string]tuipages.SessionPromptPage{
		"ses_00": {Prompts: prompts, HasMore: true},
	}
	data.Costs = map[string]tuipages.SessionCost{
		"ses_00": {
			Status: "complete", TotalTokens: 123456, PricedTokens: 120000, UnpricedTokens: 3456, CostUSD: 1.23,
			Models: []tuipages.SessionModel{{Date: "2026-07-21", Provider: "codex", Model: "gpt-5.2", TotalTokens: 123456, CostUSD: 1.23}},
		},
	}
	model.snapshot.Sessions = data
	model.request.SessionDetailID = "ses_00"
	return model
}

func writeQuest152EvidenceFrame(t *testing.T, pageID PageID, width, height int, view string) {
	writeQuest152EvidenceFrameForTest(t, pageID, width, height, view, "TestQuest152ReferenceFramesAndNoVoidSweep")
}

func writeQuest152EvidenceFrameForTest(t *testing.T, pageID PageID, width, height int, view, testName string) {
	t.Helper()
	directory := os.Getenv("QUEST_EVIDENCE_DIR")
	if directory == "" {
		return
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("create quest 152 evidence directory: %v", err)
	}
	name := fmt.Sprintf("frame-a-%s-%dx%d.txt", strings.ReplaceAll(string(pageID), "/", "-"), width, height)
	source := fmt.Sprintf("internal/tui/overlay_test.go::%s", testName)
	command := fmt.Sprintf("QUEST_EVIDENCE_DIR=quest-152-evidence go test ./internal/tui -run %s -count=1 -v", testName)
	content := fmt.Sprintf("Source: %s\nCommand: %s\n\n%s\n", source, command, view)
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s evidence: %v", name, err)
	}
}
