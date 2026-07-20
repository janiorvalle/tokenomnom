package cli

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

var exportCSVHeader = []string{
	"provider", "date", "month", "year", "model", "input_tokens", "cached_input_tokens",
	"cache_write_5m_tokens", "cache_write_1h_tokens", "cache_write_unclassified_tokens",
	"cache_write_input_tokens", "uncached_input_tokens", "output_tokens",
	"reasoning_output_tokens", "total_tokens", "cost_usd",
}

type jsonExportRow struct {
	Provider                     string   `json:"provider"`
	Date                         string   `json:"date"`
	Month                        string   `json:"month"`
	Year                         string   `json:"year"`
	Model                        string   `json:"model"`
	InputTokens                  int64    `json:"input_tokens"`
	CachedInputTokens            int64    `json:"cached_input_tokens"`
	CacheWrite5mTokens           int64    `json:"cache_write_5m_tokens"`
	CacheWrite1hTokens           int64    `json:"cache_write_1h_tokens"`
	CacheWriteUnclassifiedTokens int64    `json:"cache_write_unclassified_tokens"`
	CacheWriteInputTokens        int64    `json:"cache_write_input_tokens"`
	UncachedInputTokens          int64    `json:"uncached_input_tokens"`
	OutputTokens                 int64    `json:"output_tokens"`
	ReasoningOutputTokens        int64    `json:"reasoning_output_tokens"`
	TotalTokens                  int64    `json:"total_tokens"`
	CostUSD                      *float64 `json:"cost_usd"`
}

type jsonExportData struct {
	Rows                         []jsonExportRow `json:"rows"`
	UnpricedTokens               int64           `json:"unpriced_tokens"`
	UnclassifiedCacheWriteTokens int64           `json:"unclassified_cache_write_tokens"`
	UnknownModelTokens           int64           `json:"unknown_model_tokens"`
}

func newExportCommand(codexDir, claudeDir, timezone *string) *cobra.Command {
	var flags reportFlags
	var format string
	var out string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export full daily model usage",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			flags.applyConfig(cmd)
			filter, err := flags.filter()
			if err != nil {
				return err
			}
			return runReport(cmd, codexDir, claudeDir, timezone, flags.noSync, func(database *store.Store, context jsonReportContext) error {
				var rows []store.Usage
				if database != nil {
					rows, err = database.FilteredUsageRows(filter)
					if err != nil {
						return err
					}
				}
				table, err := loadPricingTable()
				if err != nil {
					return err
				}
				return writeExport(cmd, rows, table, flags, context, out)
			})
		},
	}
	addReportFlags(cmd, &flags)
	cmd.Flags().StringVar(&format, "format", "csv", "output format (csv or json)")
	cmd.Flags().StringVar(&out, "out", "", "write output atomically to a file")
	return cmd
}

func writeExport(cmd *cobra.Command, rows []store.Usage, table pricing.Table, flags reportFlags, context jsonReportContext, out string) error {
	write := func(writer io.Writer) error {
		if currentFormat(cmd) == "json" {
			return writeExportJSONTo(cmd, writer, rows, table, flags, context)
		}
		return writeExportCSV(writer, rows, table)
	}
	if out == "" {
		return write(cmd.OutOrStdout())
	}
	return atomicWrite(out, write)
}

func writeExportCSV(writer io.Writer, rows []store.Usage, table pricing.Table) error {
	csvWriter := csv.NewWriter(writer)
	if err := csvWriter.Write(exportCSVHeader); err != nil {
		return err
	}
	for _, row := range rows {
		value, err := exportRow(row, table)
		if err != nil {
			return err
		}
		cost := ""
		if value.CostUSD != nil {
			cost = fmt.Sprintf("%.2f", *value.CostUSD)
		}
		record := []string{
			value.Provider, value.Date, value.Month, value.Year, spreadsheetSafeCSV(value.Model),
			strconv.FormatInt(value.InputTokens, 10), strconv.FormatInt(value.CachedInputTokens, 10),
			strconv.FormatInt(value.CacheWrite5mTokens, 10), strconv.FormatInt(value.CacheWrite1hTokens, 10),
			strconv.FormatInt(value.CacheWriteUnclassifiedTokens, 10), strconv.FormatInt(value.CacheWriteInputTokens, 10),
			strconv.FormatInt(value.UncachedInputTokens, 10), strconv.FormatInt(value.OutputTokens, 10),
			strconv.FormatInt(value.ReasoningOutputTokens, 10), strconv.FormatInt(value.TotalTokens, 10), cost,
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

func spreadsheetSafeCSV(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r', '\n':
		return "'" + value
	default:
		return value
	}
}

func writeExportJSONTo(cmd *cobra.Command, writer io.Writer, rows []store.Usage, table pricing.Table, flags reportFlags, context jsonReportContext) error {
	result := make([]jsonExportRow, 0, len(rows))
	costs := calculateReportCosts(table, rows)
	for _, row := range rows {
		value, err := exportRow(row, table)
		if err != nil {
			return err
		}
		result = append(result, value)
	}
	data := jsonExportData{Rows: result, UnpricedTokens: costs.Grand.UnpricedTokens, UnclassifiedCacheWriteTokens: costs.UnclassifiedWrites, UnknownModelTokens: costs.UnknownModelTokens}
	original := cmd.OutOrStdout()
	cmd.SetOut(writer)
	defer cmd.SetOut(original)
	return writeJSONEnvelope(cmd, "export", context.Timezone, filtersJSON(flags), reportWarnings(context.Warnings, costs), data)
}

func exportRow(row store.Usage, table pricing.Table) (jsonExportRow, error) {
	date, err := time.Parse("2006-01-02", row.Date)
	if err != nil {
		return jsonExportRow{}, fmt.Errorf("parse stored date %q: %w", row.Date, err)
	}
	breakdown := table.Cost(row)
	var cost *float64
	if breakdown.PricedTokens > 0 {
		value := moneyUSD(breakdown.Total)
		cost = &value
	}
	writes := row.CacheWrite5m + row.CacheWrite1h + row.CacheWriteUnclassified
	return jsonExportRow{
		Provider: string(row.Provider), Date: row.Date, Month: date.Month().String(), Year: date.Format("2006"), Model: row.Model,
		InputTokens: row.Input, CachedInputTokens: row.CacheRead, CacheWrite5mTokens: row.CacheWrite5m,
		CacheWrite1hTokens: row.CacheWrite1h, CacheWriteUnclassifiedTokens: row.CacheWriteUnclassified,
		CacheWriteInputTokens: writes, UncachedInputTokens: row.Input - row.CacheRead,
		OutputTokens: row.Output, ReasoningOutputTokens: row.Reasoning, TotalTokens: row.Input + row.Output, CostUSD: cost,
	}, nil
}

func atomicWrite(path string, write func(io.Writer) error) (err error) {
	directory := filepath.Dir(path)
	temp, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary export: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if err := write(temp); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace export %q: %w", path, err)
	}
	return nil
}
