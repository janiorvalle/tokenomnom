package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/pricing"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

type aggregateCost struct {
	Total                 pricing.Money
	PricedTokens          int64
	UnpricedTokens        int64
	Provenance            string
	ProvenanceTokens      int64
	ProvenanceTokenCounts map[string]int64
}

type modelCostKey struct {
	Provider discover.Provider
	Model    string
}

type providerChartValue struct {
	Cost   aggregateCost
	Tokens int64
}

type reportCosts struct {
	Grand              aggregateCost
	ByDate             map[string]aggregateCost
	ByMonth            map[string]aggregateCost
	ByProvider         map[discover.Provider]aggregateCost
	ByModel            map[modelCostKey]aggregateCost
	ByDateProvider     map[string]map[discover.Provider]providerChartValue
	ByMonthProvider    map[string]map[discover.Provider]providerChartValue
	UnpricedByModel    map[string]int64
	UnclassifiedWrites int64
	UnknownModelTokens int64
}

func calculateReportCosts(table pricing.Table, rows []store.Usage) reportCosts {
	costs := reportCosts{
		ByDate:          make(map[string]aggregateCost),
		ByMonth:         make(map[string]aggregateCost),
		ByProvider:      make(map[discover.Provider]aggregateCost),
		ByModel:         make(map[modelCostKey]aggregateCost),
		ByDateProvider:  make(map[string]map[discover.Provider]providerChartValue),
		ByMonthProvider: make(map[string]map[discover.Provider]providerChartValue),
		UnpricedByModel: make(map[string]int64),
	}
	for _, row := range rows {
		breakdown := table.Cost(row)
		value := aggregateCost{Total: breakdown.Total, PricedTokens: breakdown.PricedTokens, UnpricedTokens: breakdown.UnpricedTokens}
		if breakdown.PricedTokens > 0 {
			if entry, found := table.RateFor(row.Model, row.Date); found {
				value.Provenance = entry.Status
				value.ProvenanceTokens = breakdown.PricedTokens
				value.ProvenanceTokenCounts = map[string]int64{entry.Status: breakdown.PricedTokens}
			}
		}
		costs.Grand = addAggregateCost(costs.Grand, value)
		costs.ByDate[row.Date] = addAggregateCost(costs.ByDate[row.Date], value)
		month := row.Date
		if len(month) > 7 {
			month = month[:7]
		}
		costs.ByMonth[month] = addAggregateCost(costs.ByMonth[month], value)
		costs.ByProvider[row.Provider] = addAggregateCost(costs.ByProvider[row.Provider], value)
		addProviderChartValue(costs.ByDateProvider, row.Date, row.Provider, value, row.Input+row.Output)
		addProviderChartValue(costs.ByMonthProvider, month, row.Provider, value, row.Input+row.Output)
		key := modelCostKey{Provider: row.Provider, Model: row.Model}
		costs.ByModel[key] = addAggregateCost(costs.ByModel[key], value)
		if breakdown.UnpricedTokens > 0 {
			costs.UnpricedByModel[row.Model] += breakdown.UnpricedTokens
		}
		costs.UnclassifiedWrites += breakdown.UnclassifiedCacheWriteTokens
		if row.Model == "unknown" {
			costs.UnknownModelTokens += row.Input + row.Output
		}
	}
	return costs
}

func addProviderChartValue(target map[string]map[discover.Provider]providerChartValue, period string, provider discover.Provider, cost aggregateCost, tokens int64) {
	if target[period] == nil {
		target[period] = make(map[discover.Provider]providerChartValue)
	}
	current := target[period][provider]
	current.Cost = addAggregateCost(current.Cost, cost)
	current.Tokens += tokens
	target[period][provider] = current
}

func addAggregateCost(left, right aggregateCost) aggregateCost {
	total, totalFits := addAggregateMoney(left.Total, right.Total)
	if !totalFits {
		total = left.Total
	}
	result := aggregateCost{
		Total:          total,
		PricedTokens:   left.PricedTokens,
		UnpricedTokens: left.UnpricedTokens + right.UnpricedTokens,
	}
	if totalFits {
		result.PricedTokens += right.PricedTokens
	} else {
		result.UnpricedTokens += right.PricedTokens
	}
	if totalFits {
		result.ProvenanceTokenCounts = mergeProvenanceTokenCounts(left, right)
	} else {
		result.ProvenanceTokenCounts = provenanceTokenCounts(left)
	}
	counts := result.ProvenanceTokenCounts
	if userTokens := counts["user"]; userTokens > 0 {
		result.Provenance, result.ProvenanceTokens = "user", userTokens
		return result
	}
	for status, tokens := range counts {
		if tokens > result.ProvenanceTokens || (tokens == result.ProvenanceTokens && pricing.ProvenancePriority(status) < pricing.ProvenancePriority(result.Provenance)) {
			result.Provenance, result.ProvenanceTokens = status, tokens
		}
	}
	return result
}

func addAggregateMoney(left, right pricing.Money) (pricing.Money, bool) {
	if right > 0 && left > pricing.Money(^uint64(0)>>1)-right {
		return 0, false
	}
	if right < 0 && left < -pricing.Money(^uint64(0)>>1)-1-right {
		return 0, false
	}
	return left + right, true
}

func mergeProvenanceTokenCounts(left, right aggregateCost) map[string]int64 {
	counts := make(map[string]int64)
	for status, tokens := range provenanceTokenCounts(left) {
		counts[status] += tokens
	}
	for status, tokens := range provenanceTokenCounts(right) {
		counts[status] += tokens
	}
	return counts
}

func provenanceTokenCounts(value aggregateCost) map[string]int64 {
	if len(value.ProvenanceTokenCounts) > 0 {
		return value.ProvenanceTokenCounts
	}
	if value.Provenance == "" || value.ProvenanceTokens <= 0 {
		return nil
	}
	return map[string]int64{value.Provenance: value.ProvenanceTokens}
}

func loadReportCosts(database *store.Store, filter store.Filter, keep func(store.Usage) bool) (reportCosts, error) {
	table, err := loadPricingTable()
	if err != nil {
		return reportCosts{}, err
	}
	return loadReportCostsWithTable(database, filter, keep, table)
}

func loadReportCostsWithTable(database *store.Store, filter store.Filter, keep func(store.Usage) bool, table pricing.Table) (reportCosts, error) {
	rows, err := database.FilteredUsageRows(filter)
	if err != nil {
		return reportCosts{}, err
	}
	if keep != nil {
		filtered := rows[:0]
		for _, row := range rows {
			if keep(row) {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	return calculateReportCosts(table, rows), nil
}

func formatCost(cost aggregateCost) string {
	if cost.PricedTokens == 0 {
		return "—"
	}
	return formatUSD(cost.Total)
}

func formatReportCost(cost aggregateCost) string {
	value := formatCost(cost)
	if cost.Provenance == "user" || cost.Provenance == "user rate" {
		return value + " (user rate)"
	}
	return value
}

func formatUSD(value pricing.Money) string {
	cents := value.RoundedCents()
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s$%s.%02d", sign, formatNumber(cents/100), cents%100)
}

func writeCostNotes(cmd *cobra.Command, costs reportCosts) {
	if len(costs.UnpricedByModel) > 0 {
		models := make([]string, 0, len(costs.UnpricedByModel))
		for model := range costs.UnpricedByModel {
			models = append(models, model)
		}
		sort.Strings(models)
		parts := make([]string, 0, len(models))
		for _, model := range models {
			parts = append(parts, fmt.Sprintf("%s: %s", model, formatNumber(costs.UnpricedByModel[model])))
		}
		writeWarningLine(cmd, fmt.Sprintf("WARNING: Unpriced tokens by model: %s.", strings.Join(parts, "; ")))
	}
	if costs.UnclassifiedWrites > 0 {
		writeWarningLine(cmd, fmt.Sprintf("WARNING: %s unclassified cache-write tokens use the model's 1h cache-write pricing policy.", formatNumber(costs.UnclassifiedWrites)))
	}
	writeSubtleLine(cmd, pricingDisclaimer)
}
