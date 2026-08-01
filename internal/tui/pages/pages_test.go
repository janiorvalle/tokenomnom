package pages

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
