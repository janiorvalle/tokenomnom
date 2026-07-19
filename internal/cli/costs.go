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
	Total          pricing.Money
	PricedTokens   int64
	UnpricedTokens int64
}

type modelCostKey struct {
	Provider discover.Provider
	Model    string
}

type reportCosts struct {
	Grand              aggregateCost
	ByDate             map[string]aggregateCost
	ByMonth            map[string]aggregateCost
	ByProvider         map[discover.Provider]aggregateCost
	ByModel            map[modelCostKey]aggregateCost
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
		UnpricedByModel: make(map[string]int64),
	}
	for _, row := range rows {
		breakdown := table.Cost(row)
		value := aggregateCost{Total: breakdown.Total, PricedTokens: breakdown.PricedTokens, UnpricedTokens: breakdown.UnpricedTokens}
		costs.Grand = addAggregateCost(costs.Grand, value)
		costs.ByDate[row.Date] = addAggregateCost(costs.ByDate[row.Date], value)
		month := row.Date
		if len(month) > 7 {
			month = month[:7]
		}
		costs.ByMonth[month] = addAggregateCost(costs.ByMonth[month], value)
		costs.ByProvider[row.Provider] = addAggregateCost(costs.ByProvider[row.Provider], value)
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

func addAggregateCost(left, right aggregateCost) aggregateCost {
	return aggregateCost{
		Total:          left.Total.Add(right.Total),
		PricedTokens:   left.PricedTokens + right.PricedTokens,
		UnpricedTokens: left.UnpricedTokens + right.UnpricedTokens,
	}
}

func loadReportCosts(database *store.Store, filter store.Filter, keep func(store.Usage) bool) (reportCosts, error) {
	table, err := loadPricingTable()
	if err != nil {
		return reportCosts{}, err
	}
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
	writer := cmd.OutOrStdout()
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
		fmt.Fprintf(writer, "WARNING: Unpriced tokens by model: %s.\n", strings.Join(parts, "; "))
	}
	if costs.UnclassifiedWrites > 0 {
		fmt.Fprintf(writer, "WARNING: %s unclassified cache-write tokens use the model's 1h cache-write pricing policy.\n", formatNumber(costs.UnclassifiedWrites))
	}
	fmt.Fprintln(writer, pricingDisclaimer)
}
