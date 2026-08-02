package pricing

import (
	"bytes"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestUserRatesRoundTripAndOverridePublishedModel(t *testing.T) {
	input := Rate(2750)
	cacheRead := Rate(275)
	cacheWrite := Rate(3500)
	output := Rate(18_500)
	want := map[string]UserRate{
		"gpt-5.2": {Input: &input, CacheRead: &cacheRead, CacheWrite: &cacheWrite, Output: &output},
	}
	var encoded bytes.Buffer
	if err := WriteUserRates(&encoded, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadUserRates(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	rate := got["gpt-5.2"]
	if rate.Input == nil || *rate.Input != input || rate.CacheRead == nil || *rate.CacheRead != cacheRead || rate.CacheWrite == nil || *rate.CacheWrite != cacheWrite || rate.Output == nil || *rate.Output != output {
		t.Fatalf("round-tripped user rate = %+v", rate)
	}

	table, err := Load(strings.NewReader(`{"gpt-5.2":[{"base_input":1,"cache_read":0.1,"output":5,"status":"published","source":"https://example.com/published"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	table = table.ApplyUserRates(got)
	entry, found := table.RateFor("gpt-5.2", "2026-08-02")
	if !found || entry.Status != "user" || entry.ProvenanceLabel() != "user rate" || !table.IsUserRated("gpt-5.2") {
		t.Fatalf("user entry = %+v, found %v", entry, found)
	}
	if entry.BaseInput == nil || *entry.BaseInput != input || entry.CacheRead == nil || *entry.CacheRead != cacheRead || entry.Write5m == nil || *entry.Write5m != cacheWrite || entry.Write1h == nil || *entry.Write1h != cacheWrite || entry.Output == nil || *entry.Output != output {
		t.Fatalf("effective user entry = %+v", entry)
	}
	cost := table.Cost(store.Usage{Date: "2026-08-02", Model: "gpt-5.2", Input: 4_000_000, CacheRead: 1_000_000, CacheWrite5m: 1_000_000, Output: 1_000_000})
	if cost.Total != 27_775_000_000 || cost.UnpricedTokens != 0 || cost.PricedTokens != 5_000_000 {
		t.Fatalf("user-rate cost = %+v", cost)
	}
}

func TestUserRatesCanAddAnUnpricedModel(t *testing.T) {
	input, output := Rate(1200), Rate(8000)
	table, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	table = table.ApplyUserRates(map[string]UserRate{"gpt-5.6-terra": {Input: &input, Output: &output}})
	entry, found := table.RateFor("gpt-5.6-terra", "2026-08-02")
	if !found || entry.Status != "user" || entry.Source != UserRateSource {
		t.Fatalf("new user model entry = %+v, found %v", entry, found)
	}
	cost := table.Cost(store.Usage{Date: "2026-08-02", Model: "gpt-5.6-terra", Input: 2_000_000, Output: 1_000_000})
	if cost.Total != 10_400_000_000 || cost.UnpricedTokens != 0 {
		t.Fatalf("new user model cost = %+v", cost)
	}
}

func TestUserRatesDoNotFallbackToPublishedCacheBuckets(t *testing.T) {
	input, output := Rate(1200), Rate(8000)
	table, err := Load(strings.NewReader(`{"gpt-5.2":[{"base_input":1,"cache_read":0.1,"write_5m":1,"write_1h":2,"output":5,"status":"published","source":"https://example.com/published"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	table = table.ApplyUserRates(map[string]UserRate{"gpt-5.2": {Input: &input, Output: &output}})
	cost := table.Cost(store.Usage{Date: "2026-08-02", Model: "gpt-5.2", Input: 2_000_000, CacheRead: 1_000_000, Output: 1_000_000})
	if cost.Total != 9_200_000_000 || cost.PricedTokens != 2_000_000 || cost.UnpricedTokens != 1_000_000 {
		t.Fatalf("user-rate cache fallback = %+v", cost)
	}
}

func TestUserRatesIgnorePublishedEffectiveWindows(t *testing.T) {
	input, output := Rate(1200), Rate(8000)
	table, err := Load(strings.NewReader(`{"gpt-5.2":[{"base_input":1,"output":5,"status":"published","effective_from":"2027-01-01","source":"https://example.com/published"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	table = table.ApplyUserRates(map[string]UserRate{"gpt-5.2": {Input: &input, Output: &output}})
	if _, found := table.RateFor("gpt-5.2", "2026-08-02"); !found {
		t.Fatal("user rate retained a future published effective window")
	}
}

func TestLoadUserRatesRejectsMissingRequiredRates(t *testing.T) {
	_, err := LoadUserRates(strings.NewReader(`{"gpt-5.6-terra":{"input":1}}`))
	if err == nil || !strings.Contains(err.Error(), "both input and output") {
		t.Fatalf("missing required user rate error = %v", err)
	}
}

func TestWriteUserRatesRejectsInvalidModelName(t *testing.T) {
	input, output := Rate(1_000), Rate(5_000)
	err := WriteUserRates(&bytes.Buffer{}, map[string]UserRate{" ": {Input: &input, Output: &output}})
	if err == nil || !strings.Contains(err.Error(), "model name must not be empty") {
		t.Fatalf("invalid user rate model error = %v", err)
	}
}

func TestParseRateRejectsOverflowProneUserRate(t *testing.T) {
	if _, err := ParseRate("1000000.001"); err == nil || !strings.Contains(err.Error(), "too large for cost calculations") {
		t.Fatalf("overflow-prone user rate error = %v", err)
	}
}
