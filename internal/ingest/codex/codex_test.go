package codex_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
	"github.com/janiorvalle/tokenomnom/internal/ingest/codex"
)

func TestAdapterName(t *testing.T) {
	t.Parallel()

	if got := (codex.Adapter{}).Name(); got != "codex" {
		t.Fatalf("Name() = %q, want %q", got, "codex")
	}
}

func TestAdapterParseFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fixture    string
		wantEvents []ingest.UsageEvent
		wantStats  ingest.Stats
	}{
		{
			name:    "normal flow malformed input and model switch",
			fixture: "normal.jsonl",
			wantEvents: []ingest.UsageEvent{
				event("2026-07-18T08:03:00Z", "gpt-5.3-codex", 100, 40, 3, 0, 20, 5),
				event("2026-07-18T12:07:00Z", "gpt-5.3-codex-spark", 30, 10, 0, 0, 4, 2),
			},
			wantStats: ingest.Stats{
				Lines:               11,
				TokenEvents:         3,
				EmittedEvents:       2,
				MalformedLines:      2,
				LastUsageMismatches: 1,
			},
		},
		{
			name:    "usage buffered until first model",
			fixture: "buffer_flush.jsonl",
			wantEvents: []ingest.UsageEvent{
				event("2026-07-18T09:00:00Z", "first-model", 8, 3, 0, 0, 2, 1),
				event("2026-07-18T09:02:00Z", "first-model", 5, 0, 2, 0, 1, 0),
			},
			wantStats: ingest.Stats{
				Lines:               3,
				TokenEvents:         2,
				EmittedEvents:       2,
				BufferedBeforeModel: 1,
			},
		},
		{
			name:    "usage without model becomes unknown at EOF",
			fixture: "eof_unknown.jsonl",
			wantEvents: []ingest.UsageEvent{
				event("2026-07-18T10:00:00Z", "unknown", 4, 2, 0, 0, 1, 0),
			},
			wantStats: ingest.Stats{
				Lines:               1,
				TokenEvents:         1,
				EmittedEvents:       1,
				BufferedBeforeModel: 1,
				UnknownModelEvents:  1,
			},
		},
		{
			name:    "duplicate reset and nonpositive usage diagnostics",
			fixture: "diagnostics.jsonl",
			wantEvents: []ingest.UsageEvent{
				event("2026-07-18T11:01:00Z", "diagnostic-model", 10, 0, 0, 0, 2, 0),
				event("2026-07-18T11:02:00Z", "diagnostic-model", 3, 0, 0, 0, 1, 0),
			},
			wantStats: ingest.Stats{
				Lines:              5,
				TokenEvents:        4,
				EmittedEvents:      2,
				CounterResets:      1,
				DuplicateSnapshots: 1,
			},
		},
		{
			name:    "empty context model becomes unknown",
			fixture: "empty_model.jsonl",
			wantEvents: []ingest.UsageEvent{
				event("2026-07-18T13:00:00Z", "unknown", 2, 0, 0, 0, 1, 0),
				event("2026-07-18T13:02:00Z", "unknown", 3, 0, 0, 0, 1, 0),
			},
			wantStats: ingest.Stats{
				Lines:               3,
				TokenEvents:         2,
				EmittedEvents:       2,
				BufferedBeforeModel: 1,
			},
		},
		{
			name:    "turn context line larger than scanner default",
			fixture: "long_line.jsonl",
			wantEvents: []ingest.UsageEvent{
				event("2026-07-18T12:01:00Z", "long-line-model", 7, 2, 1, 0, 3, 2),
			},
			wantStats: ingest.Stats{
				Lines:         2,
				TokenEvents:   1,
				EmittedEvents: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join("testdata", tt.fixture)
			var gotEvents []ingest.UsageEvent
			gotStats, err := (codex.Adapter{}).ParseFile(discover.SourceFile{
				Provider: discover.ProviderCodex,
				Path:     path,
			}, func(got ingest.UsageEvent) {
				gotEvents = append(gotEvents, got)
			})
			if err != nil {
				t.Fatalf("ParseFile() error = %v", err)
			}
			if !reflect.DeepEqual(gotEvents, tt.wantEvents) {
				t.Errorf("ParseFile() events = %#v, want %#v", gotEvents, tt.wantEvents)
			}
			if gotStats != tt.wantStats {
				t.Errorf("ParseFile() stats = %+v, want %+v", gotStats, tt.wantStats)
			}
		})
	}
}

func TestLongLineFixtureExceedsScannerDefault(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile(filepath.Join("testdata", "long_line.jsonl"))
	if err != nil {
		t.Fatalf("read long-line fixture: %v", err)
	}
	firstLineLength := bytes.IndexByte(contents, '\n')
	if firstLineLength <= bufioScannerDefaultMaxTokenSize {
		t.Fatalf("long-line fixture line length = %d, want > %d", firstLineLength, bufioScannerDefaultMaxTokenSize)
	}
}

func TestAdapterParseFileOpenError(t *testing.T) {
	t.Parallel()

	_, err := (codex.Adapter{}).ParseFile(discover.SourceFile{
		Path: filepath.Join(t.TempDir(), "missing.jsonl"),
	}, func(ingest.UsageEvent) {})
	if err == nil {
		t.Fatal("ParseFile() error = nil, want an open error")
	}
}

func event(timestamp, model string, input, cacheRead, cacheWrite5m, cacheWrite1h, output, reasoning int64) ingest.UsageEvent {
	return ingest.UsageEvent{
		Timestamp:    mustTime(timestamp),
		Provider:     discover.ProviderCodex,
		Model:        model,
		Input:        input,
		CacheRead:    cacheRead,
		CacheWrite5m: cacheWrite5m,
		CacheWrite1h: cacheWrite1h,
		Output:       output,
		Reasoning:    reasoning,
	}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

const bufioScannerDefaultMaxTokenSize = 64 * 1024
