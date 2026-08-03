package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/janiorvalle/tokenomnom/internal/theme"
)

// StatusBar contains the optional facts shown beneath the dashboard content.
// It is populated by the CLI loader so rendering stays free of I/O.
type StatusBar struct {
	History      HistoryStatus
	Vault        VaultStatus
	Sessions     int
	LastSyncUnix int64
	Sources      int
	Models       int
}

// HistoryStatus describes whether the rebuildable transcript index is usable
// and caught up with settled provider files.
type HistoryStatus struct {
	Exists bool
	Fresh  bool
	Hint   string
}

// VaultStatus contains lightweight archive size facts for the status bar.
type VaultStatus struct {
	Exists           bool
	Files            int
	RawBytes         int64
	StoredBytes      int64
	CompressionRatio float64
	Hint             string
}

type statusBarSegment struct {
	text     string
	styled   string
	optional bool
}

func (m Model) statusBarView(layout cockpitLayout) string {
	segments := []statusBarSegment{{
		text:   syncStatusText(m.syncing, m.syncFresh),
		styled: syncStatusStyle(m.render, m.syncing, m.syncFresh).Render(syncStatusText(m.syncing, m.syncFresh)),
	}}

	if m.warning != "" {
		return fitRight(m.statusBarWarning(segments[0], layout.innerWidth), layout.innerWidth)
	}
	if busy, ok := m.dashboardBusySegment(); ok {
		segments = append(segments, busy)
	}

	if history, ok := m.historyStatusSegment(); ok {
		segments = append(segments, history)
	}
	if vault, ok := m.vaultStatusSegment(); ok {
		segments = append(segments, vault)
	}
	if m.snapshot.StatusBar.Sessions > 0 || m.snapshot.StatusBar.History.Exists {
		value := fmt.Sprintf("%s sessions", formatStatusNumber(m.snapshot.StatusBar.Sessions))
		segments = append(segments, statusBarSegment{
			text:     value,
			styled:   m.render.Palette.Subtle().Render(value),
			optional: true,
		})
	}
	if metadata, ok := m.syncMetadataSegment(); ok {
		segments = append(segments, metadata)
	}
	if message := m.statusBarMessage(); message != "" {
		segments = append(segments, statusBarSegment{
			text:     message,
			styled:   m.render.Palette.Subtle().Render(message),
			optional: true,
		})
	}

	return fitRight(joinStatusBarSegments(segments, layout.innerWidth, m.render.Palette.Subtle()), layout.innerWidth)
}

func (m Model) dashboardBusySegment() (statusBarSegment, bool) {
	label := ""
	switch {
	case m.commandBusy && !m.syncing:
		label = "working"
	case m.dashboardLoadBusy && !m.syncing:
		label = "load-busy"
	case m.queuedKeySet:
		label = "working"
	}
	if m.queuedKeySet {
		queued := m.queuedKey.String()
		if queued == "" {
			queued = "key"
		}
		label += " · " + queued + " queued"
	}
	if label == "" {
		return statusBarSegment{}, false
	}
	text := m.spinner.View() + " " + label
	return statusBarSegment{
		text:   text,
		styled: m.spinner.View() + m.render.Palette.Subtle().Render(" "+label),
	}, true
}

func (m Model) syncMetadataSegment() (statusBarSegment, bool) {
	status := m.snapshot.StatusBar
	parts := make([]string, 0, 3)
	if status.LastSyncUnix > 0 {
		parts = append(parts, "last sync "+formatSyncAge(status.LastSyncUnix))
	}
	if status.Sources > 0 {
		parts = append(parts, fmt.Sprintf("%s sources", formatStatusNumber(status.Sources)))
	}
	if status.Models > 0 {
		parts = append(parts, fmt.Sprintf("%s models", formatStatusNumber(status.Models)))
	}
	if len(parts) == 0 {
		return statusBarSegment{}, false
	}
	value := strings.Join(parts, "  ·  ")
	return statusBarSegment{text: value, styled: m.render.Palette.Subtle().Render(value), optional: true}, true
}

func formatSyncAge(lastSyncUnix int64) string {
	age := time.Since(time.Unix(lastSyncUnix, 0))
	if age < 0 {
		age = 0
	}
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age/time.Minute))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(age/(24*time.Hour)))
	}
}

func (m Model) statusBarWarning(sync statusBarSegment, width int) string {
	const warningPrefix = "! "
	separator := "  ·  "
	available := width - lipgloss.Width(sync.text) - lipgloss.Width(separator)
	if available <= 0 {
		return sync.styled
	}
	warning := warningPrefix + m.warning
	message := m.statusBarMessage()
	if message != "" {
		warningWidth := available - lipgloss.Width(message) - lipgloss.Width(separator)
		minimumWarningWidth := lipgloss.Width(warningPrefix + "warn…")
		if lipgloss.Width(message) <= available && warningWidth >= minimumWarningWidth {
			if lipgloss.Width(warning) > warningWidth {
				warning = truncate(warning, warningWidth)
			}
			return sync.styled + m.render.Palette.Subtle().Render(separator) + m.render.Palette.Success().Render(message) + m.render.Palette.Subtle().Render(separator) + m.render.Palette.Warning().Render(warning)
		}
	}
	if lipgloss.Width(warning) > available {
		warning = truncate(warning, available)
	}
	return sync.styled + m.render.Palette.Subtle().Render(separator) + m.render.Palette.Warning().Render(warning)
}

func (m Model) statusBarMessage() string {
	if m.status == "" || strings.HasPrefix(m.status, "synced ·") {
		return ""
	}
	return m.status
}

func (m Model) historyStatusSegment() (statusBarSegment, bool) {
	status := m.snapshot.StatusBar.History
	if !status.Exists && status.Hint == "" {
		return statusBarSegment{}, false
	}

	label := "index "
	style := m.render.Palette.Subtle()
	switch {
	case status.Hint != "":
		label += status.Hint
		style = m.render.Palette.Warning()
	case status.Fresh:
		label += "fresh"
		style = m.render.Palette.Success()
	default:
		return statusBarSegment{}, false
	}
	return statusBarSegment{text: label, styled: style.Render(label), optional: true}, true
}

func (m Model) vaultStatusSegment() (statusBarSegment, bool) {
	status := m.snapshot.StatusBar.Vault
	if !status.Exists && status.Hint == "" {
		return statusBarSegment{}, false
	}

	label := "vault "
	style := m.render.Palette.Subtle()
	if status.Hint != "" {
		label += status.Hint
		style = m.render.Palette.Warning()
	} else if status.Files == 0 {
		label += "empty"
	} else {
		label += formatStatusBytes(status.StoredBytes)
		if status.CompressionRatio > 0 {
			label += fmt.Sprintf(" %.1fx", status.CompressionRatio)
		}
	}
	return statusBarSegment{text: label, styled: style.Render(label), optional: true}, true
}

func joinStatusBarSegments(segments []statusBarSegment, width int, subtle lipgloss.Style) string {
	const separatorText = "  ·  "
	separator := subtle.Render(separatorText)
	for len(segments) > 1 {
		if statusBarWidth(segments, separatorText) <= width {
			return renderStatusBarSegments(segments, separator)
		}
		removed := false
		for index := len(segments) - 1; index >= 1; index-- {
			if !segments[index].optional {
				continue
			}
			segments = append(segments[:index], segments[index+1:]...)
			removed = true
			break
		}
		if !removed {
			break
		}
	}
	if statusBarWidth(segments, separatorText) <= width {
		return renderStatusBarSegments(segments, separator)
	}
	return segments[0].styled
}

func statusBarWidth(segments []statusBarSegment, separator string) int {
	if len(segments) == 0 {
		return 0
	}
	width := 0
	for index, segment := range segments {
		if index > 0 {
			width += lipgloss.Width(separator)
		}
		width += lipgloss.Width(segment.text)
	}
	return width
}

func renderStatusBarSegments(segments []statusBarSegment, separator string) string {
	values := make([]string, 0, len(segments))
	for _, segment := range segments {
		values = append(values, segment.styled)
	}
	return strings.Join(values, separator)
}

func syncStatusText(syncing, fresh bool) string {
	if syncing {
		return "● syncing"
	}
	if !fresh {
		return "● idle"
	}
	return "● fresh"
}

func syncStatusStyle(render theme.Context, syncing, fresh bool) lipgloss.Style {
	if syncing {
		return render.Palette.Warning()
	}
	if !fresh {
		return render.Palette.Subtle()
	}
	return render.Palette.Success()
}

func formatStatusNumber(value int) string {
	digits := strconv.Itoa(value)
	for index := len(digits) - 3; index > 0; index -= 3 {
		digits = digits[:index] + "," + digits[index:]
	}
	return digits
}

func formatStatusBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value := float64(size)
	for _, unit := range []string{"KiB", "MiB", "GiB", "TiB", "PiB"} {
		value /= 1024
		if value < 1024 {
			return fmt.Sprintf("%.1f %s", value, unit)
		}
	}
	return fmt.Sprintf("%.1f PiB", value)
}
