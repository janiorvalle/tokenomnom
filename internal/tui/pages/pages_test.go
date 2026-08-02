package pages

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

func TestVaultPageRendersHealth(t *testing.T) {
	render := testRender()
	data := VaultPageData{
		Directory: "/tmp/vault", Format: "v1, none", Files: 4, RawSize: "12.0 KiB", StoredSize: "4.0 KiB",
		Ratio: "3.00x", Verified: "yes · 2026-08-01T04:00:00Z", LastArchive: "2026-08-01T03:00:00Z",
		VerificationState: FindingOK, LastVerification: "2026-08-01T04:00:00Z", Reclaimable: "8.0 KiB",
	}
	view := RenderVault(render, data, 100, 30, 0)
	for _, fragment := range []string{"Archive health", "Raw size", "12.0 KiB", "Compression ratio", "3.00x", "Verified", "[v] verify vault"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("vault view missing %q:\n%s", fragment, view)
		}
	}
}

func TestVaultPageMarksBrokenBundlesAsWarning(t *testing.T) {
	render := testRender()
	if vaultBrokenBundlesState(0) != FindingOK {
		t.Fatal("zero broken-bundle count was not styled as healthy")
	}
	if vaultBrokenBundlesState(3) != FindingWarning || !vaultStatusStyle(render, vaultBrokenBundlesState(3)).GetBold() {
		t.Fatal("nonzero broken-bundle count was not styled as a warning")
	}
}

func TestVaultPagePaginatesWrappedValues(t *testing.T) {
	render := testRender()
	data := VaultPageData{
		Directory: "/Users/janiorvalle/.local/share/tokenomnom/vault/provider/codex/archive/2026/08/transcripts/with/a/path/that/needs/wrapping",
		Format:    "v1, none", Files: 4, RawSize: "12.0 KiB", StoredSize: "4.0 KiB", Ratio: "3.00x",
		Verified: "yes", LastArchive: "2026-08-01T03:00:00Z", LastVerification: "2026-08-01T04:00:00Z", Reclaimable: "8.0 KiB",
	}
	width, height := 70, 12
	for _, line := range strings.Split(RenderVault(render, data, width, height, 0), "\n") {
		if lineWidth := lipgloss.Width(line); lineWidth > width {
			t.Fatalf("initial Vault line width = %d, want <= %d: %q", lineWidth, width, line)
		}
	}

	offset := 0
	for index := 0; index < 100; index++ {
		next, changed := UpdateVaultOffset(render, data, width, height, offset, "down")
		if !changed {
			break
		}
		offset = next
	}
	view := RenderVault(render, data, width, height, offset)
	if !strings.Contains(view, "[v] verify vault (deep)") {
		t.Fatalf("scrolled Vault page did not expose verification action:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lineWidth := lipgloss.Width(line); lineWidth > width {
			t.Fatalf("scrolled Vault line width = %d, want <= %d: %q", lineWidth, width, line)
		}
	}
}

func TestSystemPageRendersDoctorAndPricing(t *testing.T) {
	render := testRender()
	data := SystemPageData{
		Findings:          []SystemFinding{{Name: "Codex", Value: "ready · 2 files", State: FindingOK}, {Name: "History", Value: "stale", State: FindingWarning}},
		Warnings:          []string{"run history index"},
		PricingDisclaimer: "Dollar figures are API list-price equivalents, not actual bills.",
		Pricing:           []PricingRow{{Model: "gpt-test", BaseInput: "$1", CacheRead: "$0.10", Output: "$2", Status: "active", Effective: "always", Source: "embedded"}},
	}
	view := RenderSystem(render, data, 100, 30, 0)
	for _, fragment := range []string{"Doctor", "Codex", "History", "Warnings", "run history index", "Effective pricing", "gpt-test", "$1", "embedded"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("system view missing %q:\n%s", fragment, view)
		}
	}
	if _, changed := UpdateSystemOffset(render, data, 100, 30, 0, "v"); changed {
		t.Fatal("system page claimed the vault action")
	}
}

func TestSystemPageScrollsLongContent(t *testing.T) {
	rows := make([]PricingRow, 20)
	for index := range rows {
		rows[index] = PricingRow{Model: fmt.Sprintf("model-%02d", index), BaseInput: "$1", Output: "$2"}
	}
	render := testRender()
	data := SystemPageData{
		Findings: []SystemFinding{{Name: "Store", Value: "ready", State: FindingOK}},
		Pricing:  rows,
	}
	width, height := 70, 18
	first := RenderSystem(render, data, width, height, 0)
	if strings.Contains(first, "model-19") {
		t.Fatalf("unscrolled system page exposed the final row:\n%s", first)
	}
	offset := 0
	for index := 0; index < 100; index++ {
		var changed bool
		offset, changed = UpdateSystemOffset(render, data, width, height, offset, "down")
		if !changed {
			break
		}
	}
	if _, changed := UpdateSystemOffset(render, data, width, height, offset, "down"); changed {
		t.Fatal("system page scrolled past its final row")
	}
	scrolled := RenderSystem(render, data, width, height, offset)
	if !strings.Contains(scrolled, "model-19") || strings.Contains(scrolled, "model-00") {
		t.Fatalf("scrolled system page did not move through pricing rows:\n%s", scrolled)
	}
	previous := offset
	offset, changed := UpdateSystemOffset(render, data, width, height, offset, "up")
	if !changed || offset >= previous {
		t.Fatalf("system page did not scroll back: before=%d after=%d", previous, offset)
	}
}

func TestSystemPagePaginatesWrappedPricingRows(t *testing.T) {
	render := testRender()
	data := SystemPageData{
		PricingDisclaimer: "Dollar figures are API list-price equivalents, not actual bills.",
		Pricing: []PricingRow{
			{Model: "first-model", BaseInput: "$1.00", CacheRead: "$0.10", Write5m: "$1.25", Write1h: "$2.00", Output: "$5.00", Status: "published", Effective: "always", Source: "embedded"},
			{Model: "last-model", BaseInput: "$10.00", CacheRead: "$1.00", Write5m: "$12.50", Write1h: "$20.00", Output: "$50.00", Status: "published", Effective: "always", Source: "embedded"},
		},
	}
	width, height := 70, 12
	offset := 0
	for {
		next, changed := UpdateSystemOffset(render, data, width, height, offset, "down")
		if !changed {
			break
		}
		offset = next
	}
	view := RenderSystem(render, data, width, height, offset)
	if !strings.Contains(view, "last-model") {
		t.Fatalf("wrapped pricing rows did not expose final entry:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if lineWidth := lipgloss.Width(line); lineWidth > width {
			t.Fatalf("system line width = %d, want <= %d: %q", lineWidth, width, line)
		}
	}
}

func TestQuest151WidePagesExactFillAndAvoidVoids(t *testing.T) {
	dataVault, dataSystem := quest151PageFixtures()
	render := theme.Context{Mode: theme.Plain, Width: 168, Palette: theme.NewPalette(nil)}
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 136, height: 59},
		{width: 168, height: 59},
	} {
		vault := RenderVault(render, dataVault, size.width, size.height, 0)
		assertDenseFrame(t, "Vault", vault, size.width, size.height)
		assertNoDenseVoids(t, "Vault", vault)
		system := RenderSystem(render, dataSystem, size.width, size.height, 0)
		assertDenseFrame(t, "System", system, size.width, size.height)
		assertNoDenseVoids(t, "System", system)
	}
}

func TestQuest151WideListsUseAvailableHeight(t *testing.T) {
	vault, system := quest151PageFixtures()
	render := theme.Context{Mode: theme.Plain, Width: 168, Palette: theme.NewPalette(nil)}
	smallVault := RenderVault(render, vault, 168, 20, 0)
	largeVault := RenderVault(render, vault, 168, 40, 0)
	if strings.Count(largeVault, "2026-08-") <= strings.Count(smallVault, "2026-08-") {
		t.Fatalf("Vault bundle rows did not grow with height:\nsmall:\n%s\nlarge:\n%s", smallVault, largeVault)
	}
	smallSystem := RenderSystem(render, system, 168, 20, 0)
	largeSystem := RenderSystem(render, system, 168, 40, 0)
	if strings.Count(largeSystem, "model-") <= strings.Count(smallSystem, "model-") {
		t.Fatalf("System pricing rows did not grow with height:\nsmall:\n%s\nlarge:\n%s", smallSystem, largeSystem)
	}
}

func TestQuest151WideVaultCanReachOldestBundle(t *testing.T) {
	vault, _ := quest151PageFixtures()
	render := theme.Context{Mode: theme.Plain, Width: 168, Palette: theme.NewPalette(nil)}
	width, height := 168, 20
	offset := 0
	for {
		next, changed := UpdateVaultOffset(render, vault, width, height, offset, "down")
		if !changed {
			break
		}
		offset = next
	}
	view := RenderVault(render, vault, width, height, offset)
	if !strings.Contains(view, "\n2026-06-29") || strings.Contains(view, "\n2026-08-12") || !strings.Contains(view, "[v] VERIFY VAULT (DEEP)") {
		t.Fatalf("wide Vault page did not reach the oldest bundle at offset %d:\n%s", offset, view)
	}
	assertDenseFrame(t, "Vault oldest bundle", view, width, height)
}

func TestQuest151WideVaultPreservesUnavailableState(t *testing.T) {
	render := theme.Context{Mode: theme.Plain, Width: 168, Palette: theme.NewPalette(nil)}
	view := RenderVault(render, VaultPageData{}, 168, 20, 0)
	if !strings.Contains(view, "Vault status is not loaded yet.") {
		t.Fatalf("wide Vault page hid its unavailable state:\n%s", view)
	}
	if strings.Contains(view, "VERIFY VAULT") {
		t.Fatalf("wide Vault page showed an enabled verification action without data:\n%s", view)
	}
	assertDenseFrame(t, "Vault unavailable", view, 168, 20)
}

func TestQuest151WidePanesKeepWrappedValuesVisible(t *testing.T) {
	vault, system := quest151PageFixtures()
	vault.Directory = "/Users/janiorvalle/.local/share/tokenomnom/vault/provider/codex/archive/2026/08/transcripts/with/a/path/that/needs/wrapping"
	vaultView := RenderVault(testRender(), vault, 136, 20, 0)
	for _, fragment := range []string{"12.0 MiB", "4.0 MiB", "wrapping"} {
		if !strings.Contains(vaultView, fragment) {
			t.Fatalf("wide Vault storage pane hid %q:\n%s", fragment, vaultView)
		}
	}
	system.Pricing[0].Override = "manual-override"
	system.Warnings = []string{"This warning is deliberately long so the final TAIL-MARKER remains visible after wrapping inside the warnings pane."}
	systemView := RenderSystem(testRender(), system, 136, 20, 0)
	if !strings.Contains(systemView, "TAIL-MARKER") || !strings.Contains(systemView, "manual-override") {
		t.Fatalf("wide System warnings pane truncated its wrapped text:\n%s", systemView)
	}
}

func TestQuest151WideSystemWarningsCanScroll(t *testing.T) {
	_, system := quest151PageFixtures()
	system.Pricing = nil
	system.Warnings = make([]string, 12)
	for index := range system.Warnings {
		system.Warnings[index] = fmt.Sprintf("warning-%02d", index)
	}
	render := testRender()
	offset := 0
	for {
		next, changed := UpdateSystemOffset(render, system, 136, 20, offset, "down")
		if !changed {
			break
		}
		offset = next
	}
	view := RenderSystem(render, system, 136, 20, offset)
	if !strings.Contains(view, "warning-11") || strings.Contains(view, "warning-00") {
		t.Fatalf("wide System warnings did not reach their oldest entry at offset %d:\n%s", offset, view)
	}
}

func TestQuest151WideSystemPreservesUnavailableScheduleState(t *testing.T) {
	view := RenderSystem(testRender(), SystemPageData{}, 136, 50, 0)
	if !strings.Contains(view, "Status:") || !strings.Contains(view, "unavailable") || !strings.Contains(view, "unknown") {
		t.Fatalf("wide System page did not label unavailable schedule data:\n%s", view)
	}
	if strings.Contains(view, "not installed") {
		t.Fatalf("wide System page presented unavailable schedule data as uninstalled:\n%s", view)
	}
}

func quest151PageFixtures() (VaultPageData, SystemPageData) {
	bundles := make([]VaultBundle, 0, 45)
	baseDate := time.Date(2026, time.August, 12, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 45; index++ {
		date := baseDate.AddDate(0, 0, -index).Format("2006-01-02")
		bundles = append(bundles, VaultBundle{Date: date, Files: index + 1, RawSize: fmt.Sprintf("%d.0 MiB", index+1), StoredSize: fmt.Sprintf("%d00 KiB", index+1), Status: "ready"})
	}
	pricing := make([]PricingRow, 0, 30)
	for index := 0; index < 30; index++ {
		pricing = append(pricing, PricingRow{Model: fmt.Sprintf("model-%02d", index), BaseInput: "$1.00", CacheRead: "$0.10", Write5m: "$1.25", Write1h: "$2.00", Output: "$5.00", Status: "published", Effective: "always", Source: "embedded"})
	}
	return VaultPageData{
			Directory: "/Users/janiorvalle/.local/share/tokenomnom/vault/provider/codex/archive/2026/08/transcripts/with/a/path/that/needs/wrapping", Initialized: true, Format: "v1, none", Files: 42,
			RawBytes: 12 * 1024 * 1024, StoredBytes: 4 * 1024 * 1024, ReclaimableBytes: 8 * 1024 * 1024,
			RawSize: "12.0 MiB", StoredSize: "4.0 MiB", Ratio: "3.00x", Verified: "yes · 2026-08-12T04:00:00Z",
			VerificationState: FindingOK, LastArchive: "2026-08-12T03:00:00Z", LastVerification: "2026-08-12T04:00:00Z",
			Reclaimable: "8.0 MiB", Bundles: bundles,
		}, SystemPageData{
			Findings: []SystemFinding{
				{Name: "Codex", Value: "ready · 42 files · 12.0 MiB", State: FindingOK},
				{Name: "Claude", Value: "ready · 18 files · 4.0 MiB", State: FindingOK},
				{Name: "Store", Value: "ready · 60 rows · 6 models · 2.0 MiB", State: FindingOK},
				{Name: "History", Value: "stale · 44 sessions · 180 prompts", State: FindingWarning},
				{Name: "Vault", Value: "verified · 45 bundles · 420 files", State: FindingOK},
				{Name: "Schedule", Value: "installed · launchd · every 24h", State: FindingWarning},
			},
			Warnings: []string{
				"One provider source is stale; run tokenomnom sync to refresh it.",
				"Schedule definition changed; reinstall the launchd job before the next sync.",
				"History index is stale; rerun the history index before reviewing sessions.",
				"Vault verification is older than the newest archive bundle.",
				"Claude transcript root contains unreadable files.",
				"One effective pricing override is active.",
				"Store cache has expired entries waiting for refresh.",
				"One archive bundle is pending compaction.",
				"Project attribution is missing for recent sessions.",
			},
			Pricing: pricing, PricingDisclaimer: "Dollar figures are API list-price equivalents, not actual bills.",
			Schedule: SystemSchedule{Installed: true, DefinitionExists: true, BinaryExists: true, Mechanism: "launchd", ConfiguredInterval: "24h", InstalledInterval: "24h"},
			Sources:  quest151Sources(),
		}
}

func quest151Sources() []SystemSource {
	names := []string{"Codex", "Claude", "Store", "History", "Vault", "Schedule"}
	sources := make([]SystemSource, 0, 18)
	for index := 0; index < 18; index++ {
		name := fmt.Sprintf("Archive-%02d", index-5)
		if index < len(names) {
			name = names[index]
		}
		sources = append(sources, SystemSource{Name: name, Files: 42 + index, Size: fmt.Sprintf("%d.0 MiB", index+1), Exists: true})
	}
	return sources
}

func assertDenseFrame(t *testing.T, name, view string, width, height int) {
	t.Helper()
	lines := strings.Split(view, "\n")
	if len(lines) != height {
		t.Fatalf("%s frame rows=%d, want %d:\n%s", name, len(lines), height, view)
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got != width {
			t.Fatalf("%s frame line %d width=%d, want %d: %q", name, index+1, got, width, line)
		}
	}
}

func assertNoDenseVoids(t *testing.T, name, view string) {
	t.Helper()
	run := 0
	for index, line := range strings.Split(view, "\n") {
		if isDenseVoidLine(line) {
			run++
			if run > 3 {
				t.Fatalf("%s has more than three blank rows ending at %d:\n%s", name, index+1, view)
			}
			continue
		}
		run = 0
	}
}

func isDenseVoidLine(line string) bool {
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
