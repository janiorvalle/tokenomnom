package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Bars in a rail block must all start at the same cell regardless of how wide
// the label text is — alignment is the block's contract, not a side effect.
func TestQuest181RailBarsShareOneColumn(t *testing.T) {
	model := realisticEvidenceModel()
	blocks := map[string][]string{
		"mix":      model.railMixRows(19),
		"projects": model.railProjectRows(19),
	}
	for name, rows := range blocks {
		if len(rows) < 2 {
			t.Fatalf("%s: need at least two rows to compare", name)
		}
		barStart := -1
		for _, row := range rows {
			plain := ansi.Strip(row)
			index := strings.IndexAny(plain, "█·")
			if index < 0 {
				t.Fatalf("%s: row has no bar: %q", name, plain)
			}
			if barStart == -1 {
				barStart = index
				continue
			}
			if index != barStart {
				t.Fatalf("%s: bar starts at %d, want %d: %q", name, index, barStart, plain)
			}
		}
	}
}

// The active page marker must not shift page numbers out of column.
func TestQuest181RailNavNumbersStayInOneColumn(t *testing.T) {
	model := realisticEvidenceModel()
	rows := model.railNavigationRows()
	column := -1
	for _, row := range rows {
		plain := ansi.Strip(row)
		index := strings.IndexAny(plain, "123456789")
		if index < 0 {
			continue
		}
		if column == -1 {
			column = index
			continue
		}
		if index != column {
			t.Fatalf("page number column drifts: %d vs %d in %q", index, column, plain)
		}
	}
}
