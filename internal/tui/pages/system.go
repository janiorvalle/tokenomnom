package pages

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// RenderSystem keeps the existing scrolling page below wide widths and uses
// the doctor, pricing, and scheduler desk at wide widths.
func RenderSystem(render theme.Context, data SystemPageData, width, height, offset int) string {
	if isWidePage(width) {
		return renderSystemWide(render, data, width, height, offset)
	}
	lines := systemLines(render, data, max(1, width))
	viewportHeight := max(1, height)
	start := min(max(0, offset), systemPageMaxOffset(len(lines), viewportHeight))
	end := min(len(lines), start+viewportHeight)
	return strings.Join(lines[start:end], "\n")
}

// UpdateSystemOffset applies keyboard scrolling to the System page.
func UpdateSystemOffset(render theme.Context, data SystemPageData, width, height, offset int, key string) (int, bool) {
	if isWidePage(width) {
		maxOffset := systemWideMaxOffset(render, data, width, height)
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
	lines := systemLines(render, data, max(1, width))
	maxOffset := systemPageMaxOffset(len(lines), max(1, height))
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

func renderSystemWide(render theme.Context, data SystemPageData, width, height, offset int) string {
	heights := systemWideBandHeights(height)
	healthPaneWidths := densePaneWidths(width, 2)
	healthCapacity := systemHealthPaneCapacity(heights[0])
	doctorLines := denseScrollableLines(systemDoctorLines(render, data, healthPaneWidths[0]), offset, healthCapacity, "doctor")
	warningLines := denseScrollableLines(systemWarningLines(render, data, healthPaneWidths[1]), offset, healthCapacity, "warnings")
	bands := []denseBand{
		{
			Title:  "System health",
			Height: heights[0],
			Panes: []densePane{
				{Title: "Doctor", Lines: doctorLines},
				{Title: "Warnings", Lines: warningLines},
			},
		},
		{
			Title:  "Effective pricing",
			Height: heights[1],
			Panes:  []densePane{{Lines: systemPricingLines(render, data, width, offset, systemPricingCapacity(heights[1]))}},
		},
	}
	if len(heights) >= 3 {
		scheduleCapacity := max(1, heights[2]-1)
		bands = append(bands, denseBand{
			Title:  "",
			Height: heights[2],
			Panes:  []densePane{{Title: "Schedule & sources", Lines: denseScrollableLines(systemScheduleLines(render, data, width), offset, scheduleCapacity, "sources")}},
		})
	}
	return renderDenseStack(render, width, height, bands...)
}

func systemWideBandHeights(height int) []int {
	height = max(2, height)
	first := min(14, max(10, height/4))
	// The shell's body is seven rows shorter than a wide/tall terminal.
	tall := height >= 43
	if !tall {
		available := max(2, height-1)
		if first >= available {
			first = max(1, available/2)
		}
		return []int{first, max(1, available-first)}
	}
	second := min(18, max(12, height/3))
	available := max(3, height-2)
	if first+second > available {
		second = max(1, available-first)
	}
	third := max(1, available-first-second)
	return []int{first, second, third}
}

func systemPricingCapacity(bandHeight int) int {
	return max(1, bandHeight-1)
}

func systemHealthPaneCapacity(bandHeight int) int {
	return max(1, bandHeight-2)
}

func systemWideMaxOffset(render theme.Context, data SystemPageData, width, height int) int {
	heights := systemWideBandHeights(height)
	visible := systemPricingVisibleRows(data, systemPricingCapacity(heights[1]))
	maxOffset := max(0, len(data.Pricing)-visible)
	healthWidths := densePaneWidths(width, 2)
	healthCapacity := systemHealthPaneCapacity(heights[0])
	maxOffset = max(maxOffset, denseScrollableMaxOffset(len(systemDoctorLines(render, data, healthWidths[0])), healthCapacity))
	maxOffset = max(maxOffset, denseScrollableMaxOffset(len(systemWarningLines(render, data, healthWidths[1])), healthCapacity))
	if len(heights) >= 3 {
		maxOffset = max(maxOffset, denseScrollableMaxOffset(len(systemScheduleLines(render, data, width)), max(1, heights[2]-1)))
	}
	return maxOffset
}

func systemPricingVisibleRows(data SystemPageData, capacity int) int {
	reserved := 1 // the aligned table header
	if data.PricingDisclaimer != "" {
		reserved++
	}
	if len(data.Pricing) > max(1, capacity-reserved) {
		reserved += 2 // keep both scroll markers visible when the table overflows
	}
	return max(1, capacity-reserved)
}

func denseScrollableMaxOffset(lineCount, capacity int) int {
	if lineCount <= max(1, capacity) {
		return 0
	}
	return max(0, lineCount-max(1, capacity-1))
}

func denseScrollableLines(lines []string, offset, capacity int, label string) []string {
	if len(lines) <= max(1, capacity) {
		return lines
	}
	start := min(max(0, offset), denseScrollableMaxOffset(len(lines), capacity))
	dataRows := max(1, capacity)
	if start > 0 {
		dataRows--
	}
	end := min(len(lines), start+max(1, dataRows))
	if end < len(lines) {
		dataRows = max(1, dataRows-1)
		end = min(len(lines), start+dataRows)
	}
	visible := append([]string(nil), lines[start:end]...)
	if start > 0 {
		visible = append([]string{"↑ more " + label}, visible...)
	}
	if end < len(lines) {
		visible = append(visible, fmt.Sprintf("↓ %d more %s", len(lines)-end, label))
	}
	return visible
}

func systemDoctorLines(render theme.Context, data SystemPageData, width int) []string {
	if len(data.Findings) == 0 {
		return []string{render.Palette.Subtle().Render("Doctor findings are not loaded yet.")}
	}
	lines := make([]string, 0, len(data.Findings))
	for _, finding := range data.Findings {
		style := render.Palette.Subtle()
		switch finding.State {
		case FindingOK:
			style = render.Palette.Success()
		case FindingWarning:
			style = render.Palette.Warning()
		}
		lines = append(lines, denseFact(render, finding.Name, finding.Value, style, width))
	}
	return lines
}

func systemWarningLines(render theme.Context, data SystemPageData, width int) []string {
	if len(data.Warnings) == 0 {
		return []string{render.Palette.Success().Render("No warnings.")}
	}
	lines := make([]string, 0, len(data.Warnings))
	for _, warning := range data.Warnings {
		wrapped := wrapText(cleanInline(warning), max(1, width-2))
		for _, line := range strings.Split(wrapped, "\n") {
			lines = append(lines, render.Palette.Warning().Render("! "+truncate(line, max(1, width-2))))
		}
	}
	return lines
}

func systemPricingLines(render theme.Context, data SystemPageData, width, offset, capacity int) []string {
	modelWidth, rateWidth, statusWidth, effectiveWidth, sourceWidth := pricingColumns(width)
	lines := make([]string, 0, capacity)
	if data.PricingDisclaimer != "" {
		lines = append(lines, fitLine(render.Palette.Subtle().Render(data.PricingDisclaimer), width))
	}
	lines = append(lines, pricingHeader(render, modelWidth, rateWidth, statusWidth, effectiveWidth, sourceWidth, width))
	if len(data.Pricing) == 0 {
		lines = append(lines, render.Palette.Subtle().Render("No pricing entries are available."))
		return lines
	}
	visible := systemPricingVisibleRows(data, capacity)
	start := min(max(0, offset), max(0, len(data.Pricing)-visible))
	end := min(len(data.Pricing), start+visible)
	if start > 0 {
		lines = append(lines, render.Palette.Subtle().Render("↑ more pricing"))
	}
	for _, row := range data.Pricing[start:end] {
		lines = append(lines, pricingTableRow(render, row, modelWidth, rateWidth, statusWidth, effectiveWidth, sourceWidth, width))
	}
	if end < len(data.Pricing) {
		lines = append(lines, render.Palette.Subtle().Render(fmt.Sprintf("↓ %d more pricing rows", len(data.Pricing)-end)))
	}
	return lines
}

func pricingColumns(width int) (int, int, int, int, int) {
	modelWidth := min(27, max(16, width/4))
	rateWidth, statusWidth, effectiveWidth, sourceWidth := 10, 18, 15, 10
	return modelWidth, rateWidth, statusWidth, effectiveWidth, sourceWidth
}

func pricingHeader(render theme.Context, modelWidth, rateWidth, statusWidth, effectiveWidth, sourceWidth, width int) string {
	labels := []struct {
		value string
		width int
	}{
		{"MODEL", modelWidth}, {"BASE INPUT", rateWidth}, {"CACHE READ", rateWidth}, {"WRITE 5M", rateWidth},
		{"WRITE 1H", rateWidth}, {"OUTPUT", rateWidth}, {"STATUS", statusWidth}, {"EFFECTIVE", effectiveWidth}, {"SOURCE", sourceWidth},
	}
	parts := make([]string, 0, len(labels))
	for _, label := range labels {
		parts = append(parts, padRight(render.Palette.Header().Render(label.value), label.width))
	}
	return fitLine(strings.Join(parts, "  "), width)
}

func pricingTableRow(render theme.Context, row PricingRow, modelWidth, rateWidth, statusWidth, effectiveWidth, sourceWidth, width int) string {
	status := row.Status
	if row.Override != "" {
		if status == "user rate" {
			status = status + " · " + row.Override
		} else {
			status = row.Override
		}
	}
	statusStyle := render.Palette.Subtle()
	if strings.Contains(strings.ToLower(status), "warn") || strings.Contains(strings.ToLower(status), "unpriced") {
		statusStyle = render.Palette.Warning()
	}
	parts := []string{
		padRight(render.Palette.Emphasis().Render(truncate(valueOrDash(row.Model), modelWidth)), modelWidth),
		padLeft(truncate(valueOrDash(row.BaseInput), rateWidth), rateWidth),
		padLeft(truncate(valueOrDash(row.CacheRead), rateWidth), rateWidth),
		padLeft(truncate(valueOrDash(row.Write5m), rateWidth), rateWidth),
		padLeft(truncate(valueOrDash(row.Write1h), rateWidth), rateWidth),
		padLeft(truncate(valueOrDash(row.Output), rateWidth), rateWidth),
		padRight(statusStyle.Render(truncate(valueOrDash(status), statusWidth)), statusWidth),
		padRight(truncate(valueOrDash(row.Effective), effectiveWidth), effectiveWidth),
		padRight(truncate(valueOrDash(row.Source), sourceWidth), sourceWidth),
	}
	return fitLine(strings.Join(parts, "  "), width)
}

func systemScheduleLines(render theme.Context, data SystemPageData, width int) []string {
	schedule := data.Schedule
	available := scheduleDataAvailable(schedule)
	status := "not installed"
	statusStyle := render.Palette.Subtle()
	if !available {
		status = "unavailable"
		statusStyle = render.Palette.Warning()
	} else if schedule.Installed {
		status = "installed"
		statusStyle = render.Palette.Success()
		if schedule.IntervalDrift || !schedule.DefinitionExists || !schedule.BinaryExists {
			status = "needs refresh"
			statusStyle = render.Palette.Warning()
		}
	}
	interval := valueOrDash(schedule.ConfiguredInterval)
	if schedule.InstalledInterval != "" && schedule.InstalledInterval != schedule.ConfiguredInterval {
		interval += " (installed " + schedule.InstalledInterval + ")"
	}
	definition, binary := yesNoUnknown(schedule.DefinitionExists), yesNoUnknown(schedule.BinaryExists)
	if !available {
		interval, definition, binary = "-", "unknown", "unknown"
	}
	lines := []string{
		denseFact(render, "Status", status, statusStyle, width),
		denseFact(render, "Mechanism", valueOrDash(schedule.Mechanism), render.Palette.Subtle(), width),
		denseFact(render, "Interval", interval, render.Palette.Subtle(), width),
		denseFact(render, "Definition", definition, render.Palette.Subtle(), width),
		denseFact(render, "Binary", binary, render.Palette.Subtle(), width),
		render.Palette.Header().Render("SOURCE  FILES  SIZE"),
	}
	if len(data.Sources) == 0 {
		return append(lines, render.Palette.Subtle().Render("No provider sources discovered."))
	}
	for _, source := range data.Sources {
		style := render.Palette.Subtle()
		if source.Warning {
			style = render.Palette.Warning()
		} else if source.Exists {
			style = render.Palette.Success()
		}
		line := fmt.Sprintf("%-8s %5d  %s", truncate(source.Name, 8), source.Files, valueOrDash(source.Size))
		lines = append(lines, style.Render(truncate(line, width)))
	}
	return lines
}

func scheduleDataAvailable(schedule SystemSchedule) bool {
	return schedule.Available || schedule.Installed || schedule.DefinitionExists || schedule.BinaryExists || schedule.Mechanism != "" || schedule.ConfiguredInterval != "" || schedule.InstalledInterval != ""
}

func yesNoUnknown(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func systemLines(render theme.Context, data SystemPageData, width int) []string {
	logicalLines := []string{render.Palette.Header().Render("Doctor"), ""}
	if len(data.Findings) == 0 {
		logicalLines = append(logicalLines, render.Palette.Subtle().Render("Doctor findings are not loaded yet."))
	} else {
		for _, finding := range data.Findings {
			logicalLines = append(logicalLines, systemFact(render, finding))
		}
	}
	if len(data.Warnings) > 0 {
		logicalLines = append(logicalLines, "", render.Palette.Header().Render("Warnings"))
		for _, warning := range data.Warnings {
			logicalLines = append(logicalLines, "  "+render.Palette.Warning().Render(warning))
		}
	}

	logicalLines = append(logicalLines, "", render.Palette.Header().Render("Effective pricing"))
	if data.PricingDisclaimer != "" {
		logicalLines = append(logicalLines, render.Palette.Subtle().Render(data.PricingDisclaimer))
	}
	if len(data.Pricing) == 0 {
		logicalLines = append(logicalLines, render.Palette.Subtle().Render("No pricing entries are available."))
	} else {
		for _, row := range data.Pricing {
			logicalLines = append(logicalLines, pricingFact(render, row))
		}
	}
	return wrapSystemLines(logicalLines, width)
}

func wrapSystemLines(logicalLines []string, width int) []string {
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

func systemPageMaxOffset(lineCount, viewportHeight int) int {
	return max(0, lineCount-viewportHeight)
}

func systemFact(render theme.Context, finding SystemFinding) string {
	style := render.Palette.Subtle()
	switch finding.State {
	case FindingOK:
		style = render.Palette.Success()
	case FindingWarning:
		style = render.Palette.Warning()
	}
	return "  " + render.Palette.Subtle().Render(fmt.Sprintf("%-12s", finding.Name+":")) + " " + style.Render(finding.Value)
}

func pricingFact(render theme.Context, row PricingRow) string {
	model := render.Palette.Emphasis().Render(row.Model)
	values := fmt.Sprintf("input %s · cache %s · write %s/%s · output %s", row.BaseInput, row.CacheRead, row.Write5m, row.Write1h, row.Output)
	status := row.Status
	if row.Override != "" {
		status += " · " + row.Override
	}
	details := strings.Trim(strings.Join([]string{status, row.Effective, row.Source}, " · "), " ·")
	return "  " + model + "  " + render.Palette.Subtle().Render(values) + "  " + render.Palette.Emphasis().Render(details)
}
