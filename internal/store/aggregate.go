package store

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/janiorvalle/tokenomnom/internal/discover"
)

// Filter limits aggregate queries to an inclusive date range and optional
// provider and model.
type Filter struct {
	Since    string
	Until    string
	Provider discover.Provider
	Model    string
}

// TokenTotals contains both the stored token buckets and the combined values
// used by reports.
type TokenTotals struct {
	Input                  int64
	CacheRead              int64
	CacheWrite5m           int64
	CacheWrite1h           int64
	CacheWriteUnclassified int64
	CacheWrite             int64
	Output                 int64
	Reasoning              int64
	Total                  int64
}

// DailyRow is usage aggregated across one date.
type DailyRow struct {
	Date string
	TokenTotals
}

// DailyModelRow is one provider/model contribution for a single date.
type DailyModelRow struct {
	Provider discover.Provider
	Model    string
	TokenTotals
}

// DailyBreakdown contains the provider and model contributions for one date.
type DailyBreakdown struct {
	Date string
	TokenTotals
	Providers []ProviderTotals
	Models    []DailyModelRow
}

// MonthlyRow is usage aggregated across one calendar month.
type MonthlyRow struct {
	Month string
	TokenTotals
}

// ModelRow is usage aggregated for one provider and model.
type ModelRow struct {
	Provider discover.Provider
	Model    string
	TokenTotals
	ActiveDays int
	FirstDate  string
	LastDate   string
}

// ProviderTotals is one provider's contribution to a TotalsResult.
type ProviderTotals struct {
	Provider discover.Provider
	TokenTotals
}

// TotalsResult contains grand and per-provider totals plus coverage metadata.
type TotalsResult struct {
	TokenTotals
	Providers  []ProviderTotals
	FirstDate  string
	LastDate   string
	ActiveDays int
}

const aggregateColumns = `
	COALESCE(SUM(input), 0),
	COALESCE(SUM(cache_read), 0),
	COALESCE(SUM(cache_write_5m), 0),
	COALESCE(SUM(cache_write_1h), 0),
	COALESCE(SUM(cache_write_unclassified), 0),
	COALESCE(SUM(cache_write_5m + cache_write_1h + cache_write_unclassified), 0),
	COALESCE(SUM(output), 0),
	COALESCE(SUM(reasoning), 0),
	COALESCE(SUM(input + output), 0)`

// Daily returns one aggregate per active date, oldest first.
func (s *Store) Daily(filter Filter) ([]DailyRow, error) {
	where, args := filterSQL(filter)
	rows, err := s.db.Query(`SELECT date, `+aggregateColumns+` FROM usage_daily `+where+` GROUP BY date ORDER BY date`, args...)
	if err != nil {
		return nil, fmt.Errorf("query daily usage: %w", err)
	}
	defer rows.Close()
	var result []DailyRow
	for rows.Next() {
		var row DailyRow
		if err := scanTokens(rows, []any{&row.Date}, &row.TokenTotals); err != nil {
			return nil, fmt.Errorf("scan daily usage: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// DailyBreakdown returns one day's usage grouped by provider and model.
func (s *Store) DailyBreakdown(date string, provider discover.Provider) (DailyBreakdown, error) {
	if strings.TrimSpace(date) == "" {
		return DailyBreakdown{}, fmt.Errorf("query daily breakdown: date must not be empty")
	}
	filter := Filter{Since: date, Until: date, Provider: provider}
	where, args := filterSQL(filter)
	rows, err := s.db.Query(`SELECT provider, model, `+aggregateColumns+` FROM usage_daily `+where+` GROUP BY provider, model ORDER BY SUM(input + output) DESC, provider, model`, args...)
	if err != nil {
		return DailyBreakdown{}, fmt.Errorf("query daily breakdown: %w", err)
	}
	defer rows.Close()

	result := DailyBreakdown{Date: date}
	providerIndexes := make(map[discover.Provider]int)
	for rows.Next() {
		var row DailyModelRow
		if err := scanTokens(rows, []any{&row.Provider, &row.Model}, &row.TokenTotals); err != nil {
			return DailyBreakdown{}, fmt.Errorf("scan daily breakdown: %w", err)
		}
		result.Models = append(result.Models, row)
		result.TokenTotals = addTokenTotals(result.TokenTotals, row.TokenTotals)
		index, found := providerIndexes[row.Provider]
		if !found {
			index = len(result.Providers)
			providerIndexes[row.Provider] = index
			result.Providers = append(result.Providers, ProviderTotals{Provider: row.Provider})
		}
		result.Providers[index].TokenTotals = addTokenTotals(result.Providers[index].TokenTotals, row.TokenTotals)
	}
	if err := rows.Err(); err != nil {
		return DailyBreakdown{}, fmt.Errorf("iterate daily breakdown: %w", err)
	}
	sort.SliceStable(result.Providers, func(i, j int) bool {
		if result.Providers[i].Total != result.Providers[j].Total {
			return result.Providers[i].Total > result.Providers[j].Total
		}
		return result.Providers[i].Provider < result.Providers[j].Provider
	})
	return result, nil
}

// Monthly returns one aggregate per calendar month, oldest first.
func (s *Store) Monthly(filter Filter) ([]MonthlyRow, error) {
	where, args := filterSQL(filter)
	rows, err := s.db.Query(`SELECT substr(date, 1, 7), `+aggregateColumns+` FROM usage_daily `+where+` GROUP BY substr(date, 1, 7) ORDER BY substr(date, 1, 7)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query monthly usage: %w", err)
	}
	defer rows.Close()
	var result []MonthlyRow
	for rows.Next() {
		var row MonthlyRow
		if err := scanTokens(rows, []any{&row.Month}, &row.TokenTotals); err != nil {
			return nil, fmt.Errorf("scan monthly usage: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// ByModel returns provider/model aggregates ordered by total tokens.
func (s *Store) ByModel(filter Filter) ([]ModelRow, error) {
	where, args := filterSQL(filter)
	rows, err := s.db.Query(`SELECT provider, model, `+aggregateColumns+`, COUNT(DISTINCT date), MIN(date), MAX(date)
		FROM usage_daily `+where+` GROUP BY provider, model ORDER BY SUM(input + output) DESC, provider, model`, args...)
	if err != nil {
		return nil, fmt.Errorf("query usage by model: %w", err)
	}
	defer rows.Close()
	var result []ModelRow
	for rows.Next() {
		var row ModelRow
		if err := scanTokens(rows, []any{&row.Provider, &row.Model}, &row.TokenTotals, &row.ActiveDays, &row.FirstDate, &row.LastDate); err != nil {
			return nil, fmt.Errorf("scan usage by model: %w", err)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// Totals returns grand totals, coverage, and one subtotal per provider.
func (s *Store) Totals(filter Filter) (TotalsResult, error) {
	where, args := filterSQL(filter)
	var result TotalsResult
	row := s.db.QueryRow(`SELECT `+aggregateColumns+`, COUNT(DISTINCT date), COALESCE(MIN(date), ''), COALESCE(MAX(date), '') FROM usage_daily `+where, args...)
	if err := scanTokens(row, nil, &result.TokenTotals, &result.ActiveDays, &result.FirstDate, &result.LastDate); err != nil {
		return TotalsResult{}, fmt.Errorf("query usage totals: %w", err)
	}

	rows, err := s.db.Query(`SELECT provider, `+aggregateColumns+` FROM usage_daily `+where+` GROUP BY provider ORDER BY provider`, args...)
	if err != nil {
		return TotalsResult{}, fmt.Errorf("query provider totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var provider ProviderTotals
		if err := scanTokens(rows, []any{&provider.Provider}, &provider.TokenTotals); err != nil {
			return TotalsResult{}, fmt.Errorf("scan provider totals: %w", err)
		}
		result.Providers = append(result.Providers, provider)
	}
	if err := rows.Err(); err != nil {
		return TotalsResult{}, err
	}
	return result, nil
}

// FilteredUsageRows returns daily provider/model rows for pricing and other
// calculations that cannot be performed after aggregation.
func (s *Store) FilteredUsageRows(filter Filter) ([]Usage, error) {
	where, args := filterSQL(filter)
	rows, err := s.db.Query(`SELECT date, provider, model, input, cache_read, cache_write_5m, cache_write_1h, cache_write_unclassified, output, reasoning FROM usage_daily `+where+` ORDER BY date, provider, model`, args...)
	if err != nil {
		return nil, fmt.Errorf("query filtered usage: %w", err)
	}
	defer rows.Close()
	var result []Usage
	for rows.Next() {
		var usage Usage
		if err := rows.Scan(&usage.Date, &usage.Provider, &usage.Model, &usage.Input, &usage.CacheRead, &usage.CacheWrite5m, &usage.CacheWrite1h, &usage.CacheWriteUnclassified, &usage.Output, &usage.Reasoning); err != nil {
			return nil, fmt.Errorf("scan filtered usage: %w", err)
		}
		result = append(result, usage)
	}
	return result, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanTokens(source scanner, prefix []any, totals *TokenTotals, suffix ...any) error {
	dest := append(prefix,
		&totals.Input,
		&totals.CacheRead,
		&totals.CacheWrite5m,
		&totals.CacheWrite1h,
		&totals.CacheWriteUnclassified,
		&totals.CacheWrite,
		&totals.Output,
		&totals.Reasoning,
		&totals.Total,
	)
	dest = append(dest, suffix...)
	return source.Scan(dest...)
}

func addTokenTotals(left, right TokenTotals) TokenTotals {
	return TokenTotals{
		Input:                  left.Input + right.Input,
		CacheRead:              left.CacheRead + right.CacheRead,
		CacheWrite5m:           left.CacheWrite5m + right.CacheWrite5m,
		CacheWrite1h:           left.CacheWrite1h + right.CacheWrite1h,
		CacheWriteUnclassified: left.CacheWriteUnclassified + right.CacheWriteUnclassified,
		CacheWrite:             left.CacheWrite + right.CacheWrite,
		Output:                 left.Output + right.Output,
		Reasoning:              left.Reasoning + right.Reasoning,
		Total:                  left.Total + right.Total,
	}
}

func filterSQL(filter Filter) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if filter.Since != "" {
		clauses = append(clauses, "date >= ?")
		args = append(args, filter.Since)
	}
	if filter.Until != "" {
		clauses = append(clauses, "date <= ?")
		args = append(args, filter.Until)
	}
	if filter.Provider != "" {
		clauses = append(clauses, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.Model != "" {
		clauses = append(clauses, "model = ?")
		args = append(args, filter.Model)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

var _ scanner = (*sql.Rows)(nil)
var _ scanner = (*sql.Row)(nil)
