package pages

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// RenderVault renders archive health into the supplied content viewport.
func RenderVault(render theme.Context, data VaultPageData, width, height, offset int) string {
	logicalLines := wrapVaultLines(vaultLines(render, data), max(1, width))
	viewportHeight := max(1, height)
	start := min(max(0, offset), vaultPageMaxOffset(len(logicalLines), viewportHeight))
	end := min(len(logicalLines), start+viewportHeight)
	return strings.Join(logicalLines[start:end], "\n")
}

// UpdateVaultOffset applies keyboard scrolling to the Vault page.
func UpdateVaultOffset(render theme.Context, data VaultPageData, width, height, offset int, key string) (int, bool) {
	logicalLines := wrapVaultLines(vaultLines(render, data), max(1, width))
	maxOffset := vaultPageMaxOffset(len(logicalLines), max(1, height))
	previous := offset
	switch key {
	case "up":
		offset = max(0, min(maxOffset, offset)-1)
	case "down":
		offset = min(maxOffset, offset+1)
	default:
		return offset, false
	}
	return offset, previous != offset
}

func vaultLines(render theme.Context, data VaultPageData) []string {
	if data.Directory == "" && data.Format == "" && data.Ratio == "" {
		return []string{
			render.Palette.Header().Render("Archive health"),
			"",
			render.Palette.Subtle().Render("Vault status is not loaded yet."),
			"",
			render.Palette.Emphasis().Render("[v] verify vault"),
		}
	}
	format := data.Format
	if format == "" {
		format = "not initialized"
	}
	verified := data.Verified
	if verified == "" {
		verified = "not checked"
	}
	return []string{
		render.Palette.Header().Render("Archive health"),
		"",
		vaultFact(render, "Directory", valueOrDash(data.Directory), render.Palette.Subtle()),
		vaultFact(render, "Format", format, render.Palette.Subtle()),
		vaultFact(render, "Files", fmt.Sprintf("%d", data.Files), render.Palette.Emphasis()),
		vaultFact(render, "Raw size", valueOrDash(data.RawSize), render.Palette.Subtle()),
		vaultFact(render, "Stored size", valueOrDash(data.StoredSize), render.Palette.Subtle()),
		vaultFact(render, "Compression ratio", valueOrDash(data.Ratio), render.Palette.Emphasis()),
		vaultFact(render, "Verified", verified, vaultStatusStyle(render, data.VerificationState)),
		vaultFact(render, "Last archive", valueOrDash(data.LastArchive), render.Palette.Subtle()),
		vaultFact(render, "Last verification", valueOrDash(data.LastVerification), render.Palette.Subtle()),
		vaultFact(render, "Broken bundles", fmt.Sprintf("%d", data.KnownBrokenBundles), vaultStatusStyle(render, vaultBrokenBundlesState(data.KnownBrokenBundles))),
		vaultFact(render, "Reclaimable", valueOrDash(data.Reclaimable), render.Palette.Subtle()),
		"",
		render.Palette.Emphasis().Render("[v] verify vault (deep)"),
		render.Palette.Subtle().Render("Verification checks every archived transcript."),
	}
}

func wrapVaultLines(logicalLines []string, width int) []string {
	lines := make([]string, 0, len(logicalLines))
	for _, line := range logicalLines {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		wrapped := lipgloss.NewStyle().Width(width).Render(line)
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}
	return lines
}

func vaultPageMaxOffset(lineCount, viewportHeight int) int {
	return max(0, lineCount-viewportHeight)
}

func vaultFact(render theme.Context, label, value string, valueStyle lipgloss.Style) string {
	return "  " + render.Palette.Subtle().Render(fmt.Sprintf("%-22s", label+":")) + " " + valueStyle.Render(value)
}

func vaultStatusStyle(render theme.Context, state FindingState) lipgloss.Style {
	switch state {
	case FindingOK:
		return render.Palette.Success()
	case FindingWarning:
		return render.Palette.Warning()
	default:
		return render.Palette.Subtle()
	}
}

func vaultBrokenBundlesState(count int) FindingState {
	if count > 0 {
		return FindingWarning
	}
	return FindingOK
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
