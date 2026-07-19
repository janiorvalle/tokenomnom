package parity

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
)

const snapshotDate = "2026-07-18"

type usageKey struct {
	Date  string
	Model string
}

type usageValues struct {
	Input      int64
	Cached     int64
	CacheWrite int64
	Output     int64
	Total      int64
}

type fixture struct {
	provider discover.Provider
	filename string
}

func TestParity(t *testing.T) {
	if os.Getenv("TOKENOMNOM_PARITY") != "1" {
		t.Skip("set TOKENOMNOM_PARITY=1 to compare real logs with the frozen snapshot")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	roots, err := discover.Resolve(discover.ResolveOptions{Home: home, Getenv: os.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := syncer.Sync(syncer.Options{
		Store: database, Roots: roots, Location: location, Timezone: "America/New_York",
		TimezoneFingerprint: timezoneFingerprint(location),
	}); err != nil {
		t.Fatalf("sync real logs: %v", err)
	}
	gotRows, err := database.UsageRows()
	if err != nil {
		t.Fatal(err)
	}

	fixtures := []fixture{
		{provider: discover.ProviderCodex, filename: "codex_daily_token_usage_by_model_2026-02-03_to_2026-07-18.csv"},
		{provider: discover.ProviderClaude, filename: "claude_daily_token_usage_by_model_2026-06-11_to_2026-07-18.csv"},
	}
	tally := map[string]int{"exact": 0, "today-growth": 0, "eroded": 0, "failure": 0}
	var failures []string
	for _, item := range fixtures {
		path := filepath.Join(snapshotDirectory(t), item.filename)
		want, since, until := readFixture(t, path)
		got := make(map[usageKey]usageValues)
		for _, row := range gotRows {
			if row.Provider != item.provider || row.Date < since || row.Date > until {
				continue
			}
			got[usageKey{Date: row.Date, Model: row.Model}] = usageValues{
				Input: row.Input, Cached: row.CacheRead,
				CacheWrite: row.CacheWrite5m + row.CacheWrite1h + row.CacheWriteUnclassified,
				Output:     row.Output, Total: row.Input + row.Output,
			}
		}
		keys := unionKeys(want, got)
		for _, key := range keys {
			wantValues := want[key]
			gotValues := got[key]
			classification := classify(key, wantValues, gotValues)
			tally[classification]++
			if classification == "failure" {
				failures = append(failures, fmt.Sprintf("%s %s/%s: got=%+v want=%+v", item.provider, key.Date, key.Model, gotValues, wantValues))
			}
		}
	}
	t.Logf("parity classifications: exact=%d today-growth=%d eroded=%d failure=%d", tally["exact"], tally["today-growth"], tally["eroded"], tally["failure"])
	for _, failure := range failures {
		t.Error(failure)
	}
}

func timezoneFingerprint(location *time.Location) string {
	hash := sha256.New()
	end := time.Date(2101, time.January, 1, 12, 0, 0, 0, time.UTC)
	for instant := time.Date(1970, time.January, 1, 12, 0, 0, 0, time.UTC); instant.Before(end); instant = instant.AddDate(0, 0, 1) {
		name, offset := instant.In(location).Zone()
		fmt.Fprintf(hash, "%s:%s:%d\n", instant.Format("2006-01-02"), name, offset)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func snapshotDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate parity test source")
	}
	return filepath.Join(filepath.Dir(filename), "..", "..", "archive", "2026-07-18-snapshot")
}

func readFixture(t *testing.T, path string) (map[usageKey]usageValues, string, string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(records) < 2 {
		t.Fatalf("fixture %s has no usage rows", path)
	}
	columns := make(map[string]int, len(records[0]))
	for index, name := range records[0] {
		columns[name] = index
	}
	required := []string{"date", "model", "input_tokens", "cached_input_tokens", "cache_write_input_tokens", "output_tokens", "total_tokens"}
	for _, name := range required {
		if _, found := columns[name]; !found {
			t.Fatalf("fixture %s is missing column %q", path, name)
		}
	}
	result := make(map[usageKey]usageValues, len(records)-1)
	var since, until string
	for line, record := range records[1:] {
		date := record[columns["date"]]
		key := usageKey{Date: date, Model: record[columns["model"]]}
		values := usageValues{
			Input:      parseFixtureInt(t, path, line+2, "input_tokens", record[columns["input_tokens"]]),
			Cached:     parseFixtureInt(t, path, line+2, "cached_input_tokens", record[columns["cached_input_tokens"]]),
			CacheWrite: parseFixtureInt(t, path, line+2, "cache_write_input_tokens", record[columns["cache_write_input_tokens"]]),
			Output:     parseFixtureInt(t, path, line+2, "output_tokens", record[columns["output_tokens"]]),
			Total:      parseFixtureInt(t, path, line+2, "total_tokens", record[columns["total_tokens"]]),
		}
		if _, duplicate := result[key]; duplicate {
			t.Fatalf("fixture %s contains duplicate row %+v", path, key)
		}
		result[key] = values
		if since == "" || date < since {
			since = date
		}
		if date > until {
			until = date
		}
	}
	return result, since, until
}

func parseFixtureInt(t *testing.T, path string, line int, column, value string) int64 {
	t.Helper()
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		t.Fatalf("parse %s line %d column %s: %v", path, line, column, err)
	}
	return parsed
}

func unionKeys(first, second map[usageKey]usageValues) []usageKey {
	set := make(map[usageKey]bool, len(first)+len(second))
	for key := range first {
		set[key] = true
	}
	for key := range second {
		set[key] = true
	}
	keys := make([]usageKey, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Date == keys[j].Date {
			return keys[i].Model < keys[j].Model
		}
		return keys[i].Date < keys[j].Date
	})
	return keys
}

func classify(key usageKey, want, got usageValues) string {
	if got == want {
		return "exact"
	}
	if key.Date == snapshotDate && got.Input >= want.Input && got.Cached >= want.Cached && got.CacheWrite >= want.CacheWrite && got.Output >= want.Output && got.Total >= want.Total {
		return "today-growth"
	}
	if got == (usageValues{}) {
		return "eroded"
	}
	return "failure"
}
