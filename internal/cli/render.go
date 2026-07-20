package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/theme"
)

type tableStyle struct {
	hasProvider  bool
	providerCol  int
	hasModel     bool
	modelCol     int
	modelRanks   map[string]int
	moneyColumns map[int]bool
	badgeColumns map[int]bool
}

func writeReportTable(cmd *cobra.Command, headers []string, rows [][]string, rightAligned []bool, style tableStyle) {
	render := theme.FromContext(cmd.Context())
	if render.Mode == theme.Plain {
		writeTable(cmd.OutOrStdout(), headers, rows, rightAligned)
		return
	}
	fmt.Fprint(cmd.OutOrStdout(), renderStyledTable(render, headers, rows, rightAligned, style))
}

func renderStyledTable(render theme.Context, headers []string, rows [][]string, rightAligned []bool, spec tableStyle) string {
	widths := make([]int, len(headers))
	for index, header := range headers {
		widths[index] = lipgloss.Width(header)
	}
	for _, row := range rows {
		for index, value := range row {
			if width := lipgloss.Width(value); width > widths[index] {
				widths[index] = width
			}
		}
	}

	var output strings.Builder
	writeStyledTableRow(&output, render, headers, widths, rightAligned, nil, spec, true)
	for _, row := range rows {
		writeStyledTableRow(&output, render, row, widths, rightAligned, row, spec, false)
	}
	return output.String()
}

func writeStyledTableRow(output *strings.Builder, render theme.Context, values []string, widths []int, rightAligned []bool, row []string, spec tableStyle, header bool) {
	for index, value := range values {
		if index > 0 {
			output.WriteString("  ")
		}
		style := lipgloss.NewStyle()
		if header {
			style = render.Palette.Header()
		} else {
			style = tableCellStyle(render, row, index, spec)
		}
		padding := widths[index] - lipgloss.Width(value)
		if rightAligned[index] {
			output.WriteString(strings.Repeat(" ", padding))
			output.WriteString(style.Render(value))
		} else {
			output.WriteString(style.Render(value))
			if index != len(values)-1 {
				output.WriteString(strings.Repeat(" ", padding))
			}
		}
	}
	output.WriteByte('\n')
}

func tableCellStyle(render theme.Context, row []string, column int, spec tableStyle) lipgloss.Style {
	if spec.moneyColumns[column] {
		return render.Palette.Money()
	}
	if spec.badgeColumns[column] && row[column] != "—" {
		switch row[column] {
		case "proxy", "estimated":
			return render.Palette.Badge(row[column])
		case "yes":
			return render.Palette.Badge("override")
		default:
			return render.Palette.Subtle()
		}
	}
	if spec.hasProvider && column == spec.providerCol {
		return render.Palette.Provider(row[column], 0).Bold(true)
	}
	if spec.hasModel && column == spec.modelCol {
		provider := "codex"
		if spec.hasProvider {
			provider = row[spec.providerCol]
		} else if strings.HasPrefix(strings.ToLower(row[column]), "claude") {
			provider = "claude"
		}
		rank := spec.modelRanks[modelRankKey(provider, row[column])]
		return render.Palette.Provider(provider, rank)
	}
	return lipgloss.NewStyle()
}

func modelRanks(rows []store.ModelRow) map[string]int {
	ranks := make(map[string]int, len(rows))
	next := map[string]int{}
	for _, row := range rows {
		provider := providerName(row.Provider)
		ranks[modelRankKey(provider, row.Model)] = next[provider]
		next[provider]++
	}
	return ranks
}

func modelRankKey(provider, model string) string {
	return strings.ToLower(provider) + "\x00" + model
}

func writeHeading(cmd *cobra.Command, text string) {
	writeStyledLine(cmd, text, theme.FromContext(cmd.Context()).Palette.Header())
}

func writeProviderHeading(cmd *cobra.Command, provider, text string) {
	writeStyledLine(cmd, text, theme.FromContext(cmd.Context()).Palette.Provider(provider, 0).Bold(true))
}

func writeWarningLine(cmd *cobra.Command, text string) {
	writeStyledLine(cmd, text, theme.FromContext(cmd.Context()).Palette.Warning())
}

func writeSubtleLine(cmd *cobra.Command, text string) {
	writeStyledLine(cmd, text, theme.FromContext(cmd.Context()).Palette.Subtle())
}

func writeEmphasisLine(cmd *cobra.Command, text string) {
	writeStyledLine(cmd, text, theme.FromContext(cmd.Context()).Palette.Emphasis())
}

func writeStyledLine(cmd *cobra.Command, text string, style lipgloss.Style) {
	if theme.FromContext(cmd.Context()).Mode == theme.Styled {
		text = style.Render(text)
	}
	fmt.Fprintln(cmd.OutOrStdout(), text)
}
