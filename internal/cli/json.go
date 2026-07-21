package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

const reportSchema = "tokenomnom.report/v1"

type jsonFilters struct {
	Provider   *string `json:"provider"`
	Model      *string `json:"model"`
	Since      *string `json:"since"`
	Until      *string `json:"until"`
	ThreadKind *string `json:"thread_kind,omitempty"`
}

type jsonEnvelope struct {
	Schema      string      `json:"schema"`
	Command     string      `json:"command"`
	GeneratedAt string      `json:"generated_at"`
	Timezone    string      `json:"timezone"`
	Filters     jsonFilters `json:"filters"`
	Disclaimer  string      `json:"disclaimer"`
	Warnings    []string    `json:"warnings"`
	Data        any         `json:"data"`
}

type jsonTokenTotals struct {
	InputTokens      int64   `json:"input_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type jsonReportContext struct {
	Timezone string
	Warnings []string
	NoStore  bool
}

type jsonDateRange struct {
	FirstDate *string `json:"first_date"`
	LastDate  *string `json:"last_date"`
}

type jsonProviderTotals struct {
	Provider string `json:"provider"`
	jsonTokenTotals
}

type jsonTopModel struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model"`
	TotalTokens int64   `json:"total_tokens"`
	CostUSD     float64 `json:"cost_usd"`
}

type jsonSummaryData struct {
	DateRange                    jsonDateRange        `json:"date_range"`
	ActiveDays                   int                  `json:"active_days"`
	Totals                       jsonTokenTotals      `json:"totals"`
	Providers                    []jsonProviderTotals `json:"providers"`
	TopModels                    []jsonTopModel       `json:"top_models"`
	UnpricedTokens               int64                `json:"unpriced_tokens"`
	UnclassifiedCacheWriteTokens int64                `json:"unclassified_cache_write_tokens"`
	UnknownModelTokens           int64                `json:"unknown_model_tokens"`
}

type jsonDailyRow struct {
	Date string `json:"date"`
	jsonTokenTotals
}

type jsonMonthlyRow struct {
	Month string `json:"month"`
	jsonTokenTotals
}

type jsonPeriodData struct {
	Rows                         any   `json:"rows"`
	UnpricedTokens               int64 `json:"unpriced_tokens"`
	UnclassifiedCacheWriteTokens int64 `json:"unclassified_cache_write_tokens"`
	UnknownModelTokens           int64 `json:"unknown_model_tokens"`
}

type jsonModelRow struct {
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	InputTokens      int64    `json:"input_tokens"`
	CacheReadTokens  int64    `json:"cache_read_tokens"`
	CacheWriteTokens int64    `json:"cache_write_tokens"`
	OutputTokens     int64    `json:"output_tokens"`
	TotalTokens      int64    `json:"total_tokens"`
	Share            float64  `json:"share"`
	ActiveDays       int      `json:"active_days"`
	FirstDate        string   `json:"first_date"`
	LastDate         string   `json:"last_date"`
	CostUSD          float64  `json:"cost_usd"`
	CostShare        *float64 `json:"cost_share"`
	Priced           bool     `json:"priced"`
}

type jsonModelsData struct {
	Rows                         []jsonModelRow `json:"rows"`
	UnpricedTokens               int64          `json:"unpriced_tokens"`
	UnclassifiedCacheWriteTokens int64          `json:"unclassified_cache_write_tokens"`
	UnknownModelTokens           int64          `json:"unknown_model_tokens"`
}

func currentFormat(cmd *cobra.Command) string {
	value, _ := cmd.Flags().GetString("format")
	return value
}

func writeJSONEnvelope(cmd *cobra.Command, command, timezone string, filters jsonFilters, warnings []string, data any) error {
	if timezone == "" {
		timezone = localTimezoneName()
	}
	if warnings == nil {
		warnings = []string{}
	}
	envelope := jsonEnvelope{
		Schema:      reportSchema,
		Command:     command,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Timezone:    timezone,
		Filters:     filters,
		Disclaimer:  pricingDisclaimer,
		Warnings:    warnings,
		Data:        data,
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(envelope)
}

func filtersJSON(flags reportFlags) jsonFilters {
	return jsonFilters{
		Provider: optionalString(flags.provider),
		Model:    optionalString(flags.model),
		Since:    optionalString(flags.since),
		Until:    optionalString(flags.until),
	}
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func reportTimezone(database *store.Store, requested string) (string, error) {
	if database != nil {
		info, err := database.Info()
		if err != nil {
			return "", err
		}
		if info.Timezone != "" {
			if info.Timezone == "Local" {
				return localTimezoneName(), nil
			}
			return info.Timezone, nil
		}
	}
	if requested != "" {
		return requested, nil
	}
	return localTimezoneName(), nil
}

func localTimezoneName() string {
	if value := strings.TrimPrefix(os.Getenv("TZ"), ":"); value != "" {
		if _, err := time.LoadLocation(value); err == nil {
			return value
		}
	}
	if value := time.Local.String(); value != "" && value != "Local" {
		return value
	}
	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		const marker = "/zoneinfo/"
		if index := strings.LastIndex(filepath.ToSlash(target), marker); index >= 0 {
			value := filepath.ToSlash(target)[index+len(marker):]
			if _, err := time.LoadLocation(value); err == nil {
				return value
			}
		}
	}
	if contents, err := os.ReadFile("/etc/timezone"); err == nil {
		value := strings.TrimSpace(string(contents))
		if _, err := time.LoadLocation(value); err == nil {
			return value
		}
	}
	return "Local"
}

func tokenTotalsJSON(totals store.TokenTotals, cost aggregateCost) jsonTokenTotals {
	return jsonTokenTotals{
		InputTokens: totals.Input, CacheReadTokens: totals.CacheRead,
		CacheWriteTokens: totals.CacheWrite, OutputTokens: totals.Output,
		TotalTokens: totals.Total, CostUSD: moneyUSD(cost.Total),
	}
}

func moneyUSD(value pricing.Money) float64 {
	return float64(value.RoundedCents()) / 100
}

func costWarnings(costs reportCosts) []string {
	warnings := make([]string, 0, 2)
	if len(costs.UnpricedByModel) > 0 {
		models := make([]string, 0, len(costs.UnpricedByModel))
		for model := range costs.UnpricedByModel {
			models = append(models, model)
		}
		sort.Strings(models)
		parts := make([]string, 0, len(models))
		for _, model := range models {
			parts = append(parts, fmt.Sprintf("%s: %d", model, costs.UnpricedByModel[model]))
		}
		warnings = append(warnings, "Unpriced tokens by model: "+strings.Join(parts, "; ")+".")
	}
	if costs.UnclassifiedWrites > 0 {
		warnings = append(warnings, fmt.Sprintf("%d unclassified cache-write tokens use the model's 1h cache-write pricing policy.", costs.UnclassifiedWrites))
	}
	return warnings
}

func unknownWarning(tokens int64) []string {
	if tokens == 0 {
		return nil
	}
	return []string{fmt.Sprintf("%d tokens are attributed to the unknown model.", tokens)}
}
