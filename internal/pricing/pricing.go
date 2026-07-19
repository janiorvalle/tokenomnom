// Package pricing loads model rates and computes API list-price equivalents.
package pricing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/store"
)

//go:embed pricing.json
var embeddedPricing []byte

// Money stores nanodollars. With rates represented to three decimal places,
// tokens * rate-per-million is exact in nanodollars and sums without drift.
type Money int64

// Add returns the sum of two monetary values.
func (m Money) Add(other Money) Money { return m + other }

// RoundedCents returns cents rounded half up.
func (m Money) RoundedCents() int64 {
	if m < 0 {
		return -((-int64(m) + 5_000_000) / 10_000_000)
	}
	return (int64(m) + 5_000_000) / 10_000_000
}

// Rate is a USD-per-million-token rate stored in thousandths of a dollar.
type Rate int64

// Entry is one model rate in force during an optional inclusive date window.
type Entry struct {
	Model          string
	BaseInput      *Rate
	CacheRead      *Rate
	Write5m        *Rate
	Write1h        *Rate
	Output         *Rate
	Status         string
	Source         string
	Notes          string
	EffectiveFrom  string
	EffectiveUntil string
}

// Table is the validated effective rate table.
type Table struct {
	models     map[string][]Entry
	overridden map[string]bool
}

// CostBreakdown carries exact per-bucket costs and pricing diagnostics.
type CostBreakdown struct {
	BaseInput                    Money
	CacheRead                    Money
	CacheWrite5m                 Money
	CacheWrite1h                 Money
	CacheWriteUnclassified       Money
	Output                       Money
	Total                        Money
	PricedTokens                 int64
	UnpricedTokens               int64
	UnclassifiedCacheWriteTokens int64
}

type rawEntry struct {
	BaseInput      *json.Number `json:"base_input"`
	CacheRead      *json.Number `json:"cache_read"`
	Write5m        *json.Number `json:"write_5m"`
	Write1h        *json.Number `json:"write_1h"`
	Output         *json.Number `json:"output"`
	Status         string       `json:"status"`
	Source         string       `json:"source"`
	Notes          string       `json:"notes,omitempty"`
	EffectiveFrom  string       `json:"effective_from,omitempty"`
	EffectiveUntil string       `json:"effective_until,omitempty"`
}

// Load reads the embedded table and applies each override in order. An
// override model replaces that model's complete entry list.
func Load(overrides ...io.Reader) (Table, error) {
	embedded, err := decodeTable(strings.NewReader(string(embeddedPricing)))
	if err != nil {
		return Table{}, fmt.Errorf("load embedded pricing: %w", err)
	}
	table := Table{models: embedded, overridden: make(map[string]bool)}
	for _, override := range overrides {
		if override == nil {
			continue
		}
		models, err := decodeTable(override)
		if err != nil {
			return Table{}, fmt.Errorf("load pricing override: %w", err)
		}
		for model, entries := range models {
			table.models[model] = entries
			table.overridden[model] = true
		}
	}
	return table, nil
}

func decodeTable(reader io.Reader) (map[string][]Entry, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var raw map[string][]rawEntry
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode pricing JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("pricing table must be a JSON object")
	}
	models := make(map[string][]Entry, len(raw))
	for model, rawEntries := range raw {
		if strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("model name must not be empty")
		}
		if len(rawEntries) == 0 {
			return nil, fmt.Errorf("model %q must have at least one rate entry", model)
		}
		entries := make([]Entry, 0, len(rawEntries))
		for index, value := range rawEntries {
			entry, err := convertEntry(model, value)
			if err != nil {
				return nil, fmt.Errorf("model %q entry %d: %w", model, index+1, err)
			}
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].EffectiveFrom < entries[j].EffectiveFrom })
		if err := validateNonOverlap(model, entries); err != nil {
			return nil, err
		}
		models[model] = entries
	}
	return models, nil
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("pricing JSON contains multiple values")
		}
		return fmt.Errorf("decode pricing JSON: %w", err)
	}
	return nil
}

func convertEntry(model string, raw rawEntry) (Entry, error) {
	entry := Entry{Model: model, Status: raw.Status, Source: raw.Source, Notes: raw.Notes, EffectiveFrom: raw.EffectiveFrom, EffectiveUntil: raw.EffectiveUntil}
	var err error
	for _, field := range []struct {
		name string
		raw  *json.Number
		dest **Rate
	}{
		{"base_input", raw.BaseInput, &entry.BaseInput},
		{"cache_read", raw.CacheRead, &entry.CacheRead},
		{"write_5m", raw.Write5m, &entry.Write5m},
		{"write_1h", raw.Write1h, &entry.Write1h},
		{"output", raw.Output, &entry.Output},
	} {
		*field.dest, err = parseRate(field.raw)
		if err != nil {
			return Entry{}, fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if entry.Status != "published" && entry.Status != "proxy" && entry.Status != "estimated" {
		return Entry{}, fmt.Errorf("invalid status %q", entry.Status)
	}
	parsedURL, err := url.ParseRequestURI(entry.Source)
	if err != nil || (parsedURL.Scheme != "https" && parsedURL.Scheme != "http") || parsedURL.Host == "" {
		return Entry{}, fmt.Errorf("source must be an HTTP(S) URL")
	}
	if err := validateDate("effective_from", entry.EffectiveFrom); err != nil {
		return Entry{}, err
	}
	if err := validateDate("effective_until", entry.EffectiveUntil); err != nil {
		return Entry{}, err
	}
	if entry.EffectiveFrom != "" && entry.EffectiveUntil != "" && entry.EffectiveUntil < entry.EffectiveFrom {
		return Entry{}, fmt.Errorf("effective_until must be on or after effective_from")
	}
	return entry, nil
}

func parseRate(number *json.Number) (*Rate, error) {
	if number == nil {
		return nil, nil
	}
	text := string(*number)
	if strings.ContainsAny(text, "eE") {
		return nil, fmt.Errorf("rate must use plain decimal notation")
	}
	parts := strings.Split(text, ".")
	if len(parts) > 2 || len(parts) == 0 {
		return nil, fmt.Errorf("invalid rate %q", text)
	}
	if strings.HasPrefix(parts[0], "-") {
		return nil, fmt.Errorf("rate must not be negative")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
	}
	if len(fraction) > 3 {
		return nil, fmt.Errorf("rate %q has more than three decimal places", text)
	}
	for len(fraction) < 3 {
		fraction += "0"
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid rate %q", text)
	}
	frac := int64(0)
	if fraction != "" {
		frac, err = strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid rate %q", text)
		}
	}
	rate := Rate(whole*1000 + frac)
	return &rate, nil
}

func validateDate(name, value string) error {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return fmt.Errorf("%s must use YYYY-MM-DD", name)
	}
	return nil
}

func validateNonOverlap(model string, entries []Entry) error {
	for i := range entries {
		for j := i + 1; j < len(entries); j++ {
			if rangesOverlap(entries[i], entries[j]) {
				return fmt.Errorf("model %q has overlapping effective ranges", model)
			}
		}
	}
	return nil
}

func rangesOverlap(a, b Entry) bool {
	aBeforeBEnds := b.EffectiveUntil == "" || a.EffectiveFrom == "" || a.EffectiveFrom <= b.EffectiveUntil
	bBeforeAEnds := a.EffectiveUntil == "" || b.EffectiveFrom == "" || b.EffectiveFrom <= a.EffectiveUntil
	return aBeforeBEnds && bBeforeAEnds
}

// Entries returns every effective entry ordered by model and start date.
func (t Table) Entries() []Entry {
	models := make([]string, 0, len(t.models))
	for model := range t.models {
		models = append(models, model)
	}
	sort.Strings(models)
	var entries []Entry
	for _, model := range models {
		entries = append(entries, t.models[model]...)
	}
	return entries
}

// IsOverridden reports whether a user's file replaced this model.
func (t Table) IsOverridden(model string) bool { return t.overridden[model] }

// RateFor returns the model entry in force on date.
func (t Table) RateFor(model, date string) (Entry, bool) {
	for _, entry := range t.models[model] {
		if (entry.EffectiveFrom == "" || date >= entry.EffectiveFrom) && (entry.EffectiveUntil == "" || date <= entry.EffectiveUntil) {
			return entry, true
		}
	}
	return Entry{}, false
}

// Cost prices one stored daily usage row.
func (t Table) Cost(row store.Usage) CostBreakdown {
	baseInput := row.Input - row.CacheRead - row.CacheWrite5m - row.CacheWrite1h - row.CacheWriteUnclassified
	buckets := []struct {
		tokens int64
		rate   func(Entry) *Rate
		dest   func(*CostBreakdown) *Money
	}{
		{baseInput, func(e Entry) *Rate { return e.BaseInput }, func(c *CostBreakdown) *Money { return &c.BaseInput }},
		{row.CacheRead, func(e Entry) *Rate { return e.CacheRead }, func(c *CostBreakdown) *Money { return &c.CacheRead }},
		{row.CacheWrite5m, func(e Entry) *Rate { return e.Write5m }, func(c *CostBreakdown) *Money { return &c.CacheWrite5m }},
		{row.CacheWrite1h, func(e Entry) *Rate { return e.Write1h }, func(c *CostBreakdown) *Money { return &c.CacheWrite1h }},
		{row.CacheWriteUnclassified, func(e Entry) *Rate { return e.Write1h }, func(c *CostBreakdown) *Money { return &c.CacheWriteUnclassified }},
		{row.Output, func(e Entry) *Rate { return e.Output }, func(c *CostBreakdown) *Money { return &c.Output }},
	}
	result := CostBreakdown{UnclassifiedCacheWriteTokens: row.CacheWriteUnclassified}
	entry, found := t.RateFor(row.Model, row.Date)
	for _, bucket := range buckets {
		if bucket.tokens == 0 {
			continue
		}
		if !found || bucket.rate(entry) == nil {
			result.UnpricedTokens += bucket.tokens
			continue
		}
		cost := Money(bucket.tokens * int64(*bucket.rate(entry)))
		*bucket.dest(&result) = cost
		result.Total += cost
		result.PricedTokens += bucket.tokens
	}
	return result
}

// FormatRate formats a rate for the effective pricing table.
func FormatRate(rate *Rate) string {
	if rate == nil {
		return "—"
	}
	whole := int64(*rate) / 1000
	fraction := int64(*rate) % 1000
	text := fmt.Sprintf("%03d", fraction)
	if strings.HasSuffix(text, "0") {
		text = text[:2]
	}
	return fmt.Sprintf("$%d.%s", whole, text)
}
