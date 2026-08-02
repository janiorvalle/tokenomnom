package pages

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// The shell's wide content pane starts at 136 cells: 160 terminal cells less
// frame padding, rail, and the rail-to-page gap. Keeping this boundary here
// lets page renderers stay independent from the shell package without changing
// the page loader contract.
const widePageContentWidth = 136

// RenderVault renders the compact legacy page below the wide tier and the
// two-band archive desk at wide widths.
func RenderVault(render theme.Context, data VaultPageData, width, height, offset int) string {
	if isWidePage(width) {
		return renderVaultWide(render, data, width, height, offset)
	}
	logicalLines := wrapVaultLines(vaultLines(render, data), max(1, width))
	viewportHeight := max(1, height)
	start := min(max(0, offset), vaultPageMaxOffset(len(logicalLines), viewportHeight))
	end := min(len(logicalLines), start+viewportHeight)
	return strings.Join(logicalLines[start:end], "\n")
}

// UpdateVaultOffset applies keyboard scrolling to the Vault page.
func UpdateVaultOffset(render theme.Context, data VaultPageData, width, height, offset int, key string) (int, bool) {
	if isWidePage(width) {
		maxOffset := vaultWideMaxOffset(render, data, width, height)
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

func renderVaultWide(render theme.Context, data VaultPageData, width, height, offset int) string {
	firstHeight, secondHeight := vaultWideBandHeights(height)
	if vaultDataUnavailable(data) {
		return renderDenseStack(render, width, height,
			denseBand{
				Title:  "Archive",
				Height: firstHeight,
				Panes: []densePane{
					{Title: "Archive health", Lines: []string{render.Palette.Subtle().Render("Vault status is not loaded yet.")}},
					{Title: "Storage", Lines: []string{render.Palette.Subtle().Render("Storage data is not loaded yet.")}},
				},
			},
			denseBand{
				Title:  "Bundles",
				Height: secondHeight,
				Panes:  []densePane{{Lines: []string{render.Palette.Subtle().Render("No archived bundles are available.")}}},
			},
		)
	}
	paneWidths := densePaneWidths(width, 2)
	archiveCapacity := max(1, firstHeight-2)
	archiveLines := denseScrollableLines(vaultArchiveHealthLines(render, data, paneWidths[0]), offset, archiveCapacity, "archive health")
	bundleStart := min(max(0, offset), vaultBundleMaxOffset(data, height))
	return renderDenseStack(render, width, height,
		denseBand{
			Title:  "Archive",
			Height: firstHeight,
			Panes: []densePane{
				{Title: "Archive health", Lines: archiveLines},
				{Title: "Storage", Lines: vaultStorageLines(render, data, paneWidths[1])},
			},
		},
		denseBand{
			Title:  "Bundles",
			Height: secondHeight,
			Panes:  []densePane{{Lines: vaultBundleLines(render, data, bundleStart, vaultBundleCapacity(height), width)}},
		},
	)
}

func isWidePage(width int) bool {
	return width >= widePageContentWidth
}

func vaultWideBandHeights(height int) (int, int) {
	height = max(2, height)
	available := max(2, height-1)
	first := min(15, max(10, height/3))
	if first >= available {
		first = max(1, available/2)
	}
	return first, max(1, available-first)
}

func vaultBundleCapacity(height int) int {
	_, second := vaultWideBandHeights(height)
	return max(1, second-1)
}

func vaultWideMaxOffset(render theme.Context, data VaultPageData, width, height int) int {
	if vaultDataUnavailable(data) {
		return 0
	}
	firstHeight, _ := vaultWideBandHeights(height)
	paneWidths := densePaneWidths(width, 2)
	archiveMax := denseScrollableMaxOffset(len(vaultArchiveHealthLines(render, data, paneWidths[0])), max(1, firstHeight-2))
	return max(archiveMax, vaultBundleMaxOffset(data, height))
}

func vaultBundleMaxOffset(data VaultPageData, height int) int {
	return max(0, len(data.Bundles)-max(1, vaultBundleCapacity(height)-2))
}

func vaultDataUnavailable(data VaultPageData) bool {
	return data.Directory == "" && data.Format == "" && data.Ratio == ""
}

func vaultArchiveHealthLines(render theme.Context, data VaultPageData, width int) []string {
	format := valueOrDash(data.Format)
	verified := valueOrDash(data.Verified)
	lines := denseFactLines(render, "Directory", valueOrDash(data.Directory), render.Palette.Subtle(), width)
	lines = append(lines,
		denseFact(render, "Format", format, render.Palette.Subtle(), width),
		denseFact(render, "Files", fmt.Sprintf("%d", data.Files), render.Palette.Emphasis(), width),
		denseFact(render, "Verified", verified, vaultStatusStyle(render, data.VerificationState), width),
		denseFact(render, "Broken bundles", fmt.Sprintf("%d", data.KnownBrokenBundles), vaultStatusStyle(render, vaultBrokenBundlesState(data.KnownBrokenBundles)), width),
		denseFact(render, "Reclaimable", valueOrDash(data.Reclaimable), render.Palette.Subtle(), width),
		render.Palette.Emphasis().Render("[v] VERIFY VAULT (DEEP)"),
		render.Palette.Subtle().Render("Checks every archived transcript."),
	)
	return lines
}

func vaultStorageLines(render theme.Context, data VaultPageData, width int) []string {
	raw, stored := data.RawBytes, data.StoredBytes
	total := raw
	if total <= 0 {
		total = stored
	}
	rawLabel, storedLabel := valueOrDash(data.RawSize), valueOrDash(data.StoredSize)
	return []string{
		storageBar(render, "RAW", raw, total, rawLabel, width),
		storageBar(render, "STORED", stored, total, storedLabel, width),
		denseFact(render, "Ratio", valueOrDash(data.Ratio), render.Palette.Emphasis(), width),
		denseFact(render, "Reclaimable", valueOrDash(data.Reclaimable), render.Palette.Subtle(), width),
		denseFact(render, "Archive", valueOrDash(data.LastArchive), render.Palette.Subtle(), width),
		denseFact(render, "Verify", valueOrDash(data.LastVerification), vaultStatusStyle(render, data.VerificationState), width),
	}
}

func storageBar(render theme.Context, label string, value, total int64, display string, width int) string {
	label = padRight(label, 6)
	valueWidth := lipgloss.Width(display)
	barWidth := max(1, width-lipgloss.Width(label)-valueWidth-2)
	fill := 0
	if value > 0 && total > 0 {
		fill = int(float64(barWidth)*float64(value)/float64(total) + 0.5)
	}
	fill = min(max(0, fill), barWidth)
	bar := strings.Repeat("█", fill) + strings.Repeat("·", barWidth-fill)
	return fitLine(render.Palette.Subtle().Render(label)+" "+render.Palette.Emphasis().Render(bar)+" "+render.Palette.Subtle().Render(display), width)
}

func vaultBundleLines(render theme.Context, data VaultPageData, start, capacity, width int) []string {
	dateWidth, filesWidth, sizeWidth, statusWidth := vaultBundleColumns(width)
	header := strings.Join([]string{
		padRight(render.Palette.Header().Render("DATE"), dateWidth),
		padLeft(render.Palette.Header().Render("FILES"), filesWidth),
		padRight(render.Palette.Header().Render("RAW -> STORED"), sizeWidth),
		padRight(render.Palette.Header().Render("STATUS"), statusWidth),
	}, "  ")
	lines := []string{fitLine(header, width)}
	if len(data.Bundles) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No archived bundles are recorded yet."))
		return lines
	}
	start = min(max(0, start), max(0, len(data.Bundles)-1))
	dataRows := max(1, capacity-1)
	if start > 0 {
		dataRows--
	}
	end := min(len(data.Bundles), start+max(1, dataRows))
	if end < len(data.Bundles) {
		dataRows = max(1, dataRows-1)
		end = min(len(data.Bundles), start+dataRows)
	}
	for _, bundle := range data.Bundles[start:end] {
		size := bundle.RawSize + " -> " + bundle.StoredSize
		status := valueOrDash(bundle.Status)
		statusStyle := render.Palette.Subtle()
		if strings.Contains(status, "missing") || strings.Contains(status, "broken") {
			statusStyle = render.Palette.Warning()
		} else if status == "ready" {
			statusStyle = render.Palette.Success()
		}
		line := strings.Join([]string{
			padRight(truncate(bundle.Date, dateWidth), dateWidth),
			padLeft(truncate(fmt.Sprintf("%d", bundle.Files), filesWidth), filesWidth),
			padRight(truncate(size, sizeWidth), sizeWidth),
			padRight(statusStyle.Render(truncate(status, statusWidth)), statusWidth),
		}, "  ")
		lines = append(lines, fitLine(line, width))
	}
	if start > 0 {
		lines = append([]string{render.Palette.Subtle().Render("↑ more recent bundles")}, lines...)
	}
	if end < len(data.Bundles) {
		lines = append(lines, render.Palette.Subtle().Render(fmt.Sprintf("↓ %d more bundles", len(data.Bundles)-end)))
	}
	return lines
}

func vaultBundleColumns(width int) (int, int, int, int) {
	dateWidth, filesWidth := 10, 5
	sizeWidth := min(32, max(18, width/4))
	statusWidth := max(8, width-dateWidth-filesWidth-sizeWidth-6)
	return dateWidth, filesWidth, sizeWidth, statusWidth
}

func vaultLines(render theme.Context, data VaultPageData) []string {
	if vaultDataUnavailable(data) {
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

type densePane struct {
	Title string
	Lines []string
}

type denseBand struct {
	Title  string
	Height int
	Panes  []densePane
}

// renderDenseStack is the page-local equivalent of the shell's band primitive.
// It keeps the page package independent from tui while sharing the same exact
// width, height, and two-cell pane-gap rules.
func renderDenseStack(render theme.Context, width, height int, bands ...denseBand) string {
	width, height = max(1, width), max(1, height)
	if len(bands) == 0 {
		return denseFillBlock(render, width, height)
	}
	requested := make([]int, len(bands))
	for index, band := range bands {
		requested[index] = max(1, band.Height)
	}
	heights := denseStackHeights(height, requested)
	lines := make([]string, 0, height)
	for index, band := range bands {
		if index > 0 {
			lines = append(lines, denseRule(render, width))
		}
		lines = append(lines, renderDenseBand(render, band, heights[index], width)...)
	}
	return denseFitLines(render, lines, width, height)
}

func denseStackHeights(height int, requested []int) []int {
	if len(requested) == 0 {
		return nil
	}
	available := max(len(requested), height-(len(requested)-1))
	heights := make([]int, len(requested))
	remaining := available
	for index, value := range requested {
		bandsLeft := len(requested) - index - 1
		allocation := min(max(1, value), max(1, remaining-bandsLeft))
		heights[index] = allocation
		remaining -= allocation
	}
	if remaining > 0 {
		heights[len(heights)-1] += remaining
	}
	return heights
}

func renderDenseBand(render theme.Context, band denseBand, height, width int) []string {
	height, width = max(1, height), max(1, width)
	rows := make([]string, 0, height)
	if strings.TrimSpace(band.Title) != "" {
		rows = append(rows, denseRuleTitle(render, band.Title, width))
	}
	contentHeight := max(0, height-len(rows))
	if len(band.Panes) == 0 {
		rows = append(rows, denseFitRows(render, nil, width, contentHeight)...)
		return denseFitRows(render, rows, width, height)
	}
	gap := densePaneGap(width, len(band.Panes))
	widths := densePaneWidths(width, len(band.Panes))
	paneRows := make([][]string, len(band.Panes))
	for index, pane := range band.Panes {
		paneRows[index] = renderDensePane(render, pane, widths[index], contentHeight)
	}
	for row := 0; row < contentHeight; row++ {
		line := ""
		for index, pane := range paneRows {
			if index > 0 {
				line += strings.Repeat(" ", gap)
			}
			if row < len(pane) {
				line += pane[row]
			} else {
				line += strings.Repeat(" ", widths[index])
			}
		}
		rows = append(rows, fitLine(line, width))
	}
	return denseFitRows(render, rows, width, height)
}

func densePaneGap(width, paneCount int) int {
	if paneCount <= 1 {
		return 0
	}
	return min(2, max(0, (width-paneCount)/(paneCount-1)))
}

func densePaneWidths(width, paneCount int) []int {
	if paneCount <= 0 {
		return nil
	}
	gap := densePaneGap(width, paneCount)
	available := max(paneCount, width-gap*(paneCount-1))
	widths := make([]int, paneCount)
	remaining := available
	for index := range widths {
		allocation := max(1, remaining/(paneCount-index))
		widths[index] = allocation
		remaining -= allocation
	}
	return widths
}

func renderDensePane(render theme.Context, pane densePane, width, height int) []string {
	width, height = max(1, width), max(0, height)
	rows := make([]string, 0, height)
	if strings.TrimSpace(pane.Title) != "" && len(rows) < height {
		rows = append(rows, denseRuleTitle(render, pane.Title, width))
	}
	for _, line := range pane.Lines {
		if len(rows) >= height {
			break
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		rows = append(rows, fitLine(line, width))
	}
	for len(rows) < height {
		rows = append(rows, denseFillLine(render, width))
	}
	return rows
}

func denseRuleTitle(render theme.Context, title string, width int) string {
	title = strings.ToUpper(strings.TrimSpace(title))
	label := render.Palette.Header().Render(title)
	remaining := width - lipgloss.Width(label) - 1
	if remaining <= 0 {
		return fitLine(label, width)
	}
	return fitLine(label+" "+render.Palette.Border().Render(strings.Repeat("─", remaining)), width)
}

func denseRule(render theme.Context, width int) string {
	return fitLine(render.Palette.Border().Render(strings.Repeat("─", width)), width)
}

func denseFact(render theme.Context, label, value string, valueStyle lipgloss.Style, width int) string {
	labelWidth := min(22, max(8, width/3))
	labelText := padRight(label+":", labelWidth)
	valueWidth := max(1, width-labelWidth-3)
	return fitLine(render.Palette.Subtle().Render("  "+labelText)+" "+valueStyle.Render(truncate(valueOrDash(value), valueWidth)), width)
}

func denseFactLines(render theme.Context, label, value string, valueStyle lipgloss.Style, width int) []string {
	labelWidth := min(22, max(8, width/3))
	labelText := padRight(label+":", labelWidth)
	valueWidth := max(1, width-labelWidth-3)
	wrapped := strings.Split(wrapText(valueOrDash(value), valueWidth), "\n")
	if len(wrapped) == 0 || wrapped[0] == "" {
		wrapped = []string{"-"}
	}
	lines := make([]string, 0, len(wrapped))
	lines = append(lines, fitLine(render.Palette.Subtle().Render("  "+labelText)+" "+valueStyle.Render(wrapped[0]), width))
	continuation := strings.Repeat(" ", labelWidth+3)
	for _, line := range wrapped[1:] {
		lines = append(lines, fitLine(continuation+valueStyle.Render(line), width))
	}
	return lines
}

func denseFillLine(render theme.Context, width int) string {
	return fitLine(render.Palette.Border().Render("·"), width)
}

func denseFitRows(render theme.Context, rows []string, width, height int) []string {
	rows = append([]string(nil), rows...)
	if len(rows) > height {
		rows = rows[:height]
	}
	for len(rows) < height {
		rows = append(rows, denseFillLine(render, width))
	}
	for index := range rows {
		rows[index] = fitLine(rows[index], width)
	}
	return rows
}

func denseFitLines(render theme.Context, lines []string, width, height int) string {
	return strings.Join(denseFitRows(render, lines, width, height), "\n")
}

func denseFillBlock(render theme.Context, width, height int) string {
	return denseFitLines(render, nil, width, height)
}
