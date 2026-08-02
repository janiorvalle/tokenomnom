package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// UserRate is the durable, user-supplied rate for one model. Input and output
// are required by the CLI; cache rates are optional because some providers do
// not expose cache buckets for a model.
type UserRate struct {
	Input      *Rate
	CacheRead  *Rate
	CacheWrite *Rate
	Output     *Rate
}

// UserRateSource identifies estimates created by the user-rate file.
const UserRateSource = "user rate"

// maxUserRate keeps persisted estimates inside a range that can be multiplied
// by realistic token counts without overflowing Money.
const maxUserRate Rate = 1_000_000_000

type rawUserRate struct {
	Input      *json.Number `json:"input"`
	CacheRead  *json.Number `json:"cache_read"`
	CacheWrite *json.Number `json:"cache_write"`
	Output     *json.Number `json:"output"`
}

type encodedUserRate struct {
	Input      *json.Number `json:"input"`
	CacheRead  *json.Number `json:"cache_read,omitempty"`
	CacheWrite *json.Number `json:"cache_write,omitempty"`
	Output     *json.Number `json:"output"`
}

// LoadUserRates decodes the additive user-rate file. A missing file should be
// handled by the caller as an empty file; nil readers are also empty.
func LoadUserRates(reader io.Reader) (map[string]UserRate, error) {
	if reader == nil {
		return map[string]UserRate{}, nil
	}
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var raw map[string]rawUserRate
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode user rates JSON: %w", err)
	}
	if err := ensureEOF(decoder); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("user rates must be a JSON object")
	}

	rates := make(map[string]UserRate, len(raw))
	for model, value := range raw {
		if err := validateUserRateModelName(model); err != nil {
			return nil, err
		}
		input, err := parseUserRateField("input", value.Input)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", model, err)
		}
		output, err := parseUserRateField("output", value.Output)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", model, err)
		}
		if input == nil || output == nil {
			return nil, fmt.Errorf("model %q must define both input and output rates", model)
		}
		cacheRead, err := parseUserRateField("cache_read", value.CacheRead)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", model, err)
		}
		cacheWrite, err := parseUserRateField("cache_write", value.CacheWrite)
		if err != nil {
			return nil, fmt.Errorf("model %q: %w", model, err)
		}
		rates[model] = UserRate{Input: input, CacheRead: cacheRead, CacheWrite: cacheWrite, Output: output}
	}
	return rates, nil
}

func parseUserRateField(name string, number *json.Number) (*Rate, error) {
	if number == nil {
		return nil, nil
	}
	rate, err := ParseRate(string(*number))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &rate, nil
}

// WriteUserRates writes the stable additive user-rate format.
func WriteUserRates(writer io.Writer, rates map[string]UserRate) error {
	if rates == nil {
		rates = map[string]UserRate{}
	}
	models := make([]string, 0, len(rates))
	for model := range rates {
		if err := validateUserRateModelName(model); err != nil {
			return err
		}
		if rates[model].Input == nil || rates[model].Output == nil {
			return fmt.Errorf("model %q must define both input and output rates", model)
		}
		models = append(models, model)
	}
	sort.Strings(models)
	ordered := make(map[string]encodedUserRate, len(rates))
	for _, model := range models {
		rate := rates[model]
		ordered[model] = encodedUserRate{
			Input:      rateJSONNumber(rate.Input),
			CacheRead:  rateJSONNumber(rate.CacheRead),
			CacheWrite: rateJSONNumber(rate.CacheWrite),
			Output:     rateJSONNumber(rate.Output),
		}
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(ordered)
}

func validateUserRateModelName(model string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("user rate model name must not be empty")
	}
	return nil
}

func rateJSONNumber(rate *Rate) *json.Number {
	if rate == nil {
		return nil
	}
	value := json.Number(formatRateNumber(*rate))
	return &value
}

func formatRateNumber(rate Rate) string {
	whole := int64(rate) / 1000
	fraction := int64(rate) % 1000
	if fraction == 0 {
		return strconv.FormatInt(whole, 10)
	}
	text := fmt.Sprintf("%03d", fraction)
	text = strings.TrimRight(text, "0")
	return strconv.FormatInt(whole, 10) + "." + text
}

// ParseRate parses a nonnegative USD-per-million-token rate with at most three
// decimal places.
func ParseRate(text string) (Rate, error) {
	value := strings.TrimSpace(text)
	if value == "" {
		return 0, fmt.Errorf("rate must be a plain decimal number")
	}
	number := json.Number(value)
	rate, err := parseRate(&number)
	if err != nil {
		return 0, err
	}
	if *rate > maxUserRate {
		return 0, fmt.Errorf("rate %q is too large for cost calculations (maximum is $1000000 per million tokens)", value)
	}
	return *rate, nil
}

// ApplyUserRates overlays user rates on top of the effective table. A user
// rate is authoritative for the model, so omitted cache buckets remain
// unpriced instead of silently falling back to a published list rate.
func (t Table) ApplyUserRates(rates map[string]UserRate) Table {
	if len(rates) == 0 {
		return t
	}
	if t.models == nil {
		t.models = make(map[string][]Entry)
	}
	if t.userRated == nil {
		t.userRated = make(map[string]bool)
	}
	for model, rate := range rates {
		t.models[model] = []Entry{{
			Model: model, BaseInput: cloneRate(rate.Input), CacheRead: cloneRate(rate.CacheRead),
			Write5m: cloneRate(rate.CacheWrite), Write1h: cloneRate(rate.CacheWrite), Output: cloneRate(rate.Output),
			Status: "user", Source: UserRateSource, Notes: "User-supplied rate estimate.",
		}}
		t.userRated[model] = true
	}
	return t
}

func cloneRate(rate *Rate) *Rate {
	if rate == nil {
		return nil
	}
	value := *rate
	return &value
}
