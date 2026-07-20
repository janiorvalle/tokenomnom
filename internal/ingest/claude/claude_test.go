package claude_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
	"github.com/janiorvalle/tokenomnom/internal/ingest/claude"
)

func TestAdapterName(t *testing.T) {
	t.Parallel()

	if got := (claude.Adapter{}).Name(); got != "claude" {
		t.Fatalf("Name() = %q, want %q", got, "claude")
	}
}

func TestAdapterParseFile(t *testing.T) {
	t.Parallel()

	gotCandidates, gotStats := parseFixture(t, "stage1.jsonl")
	wantCandidates := []claude.Candidate{
		{
			MessageID: "msg-normal",
			Timestamp: mustTime("2026-07-18T14:00:00Z"),
			Iterations: []claude.Iteration{{
				Model: "claude-main", RawInput: 2, CacheRead: 3, CacheCreation: 5,
				CacheWrite5m: 1, CacheWrite1h: 2, Output: 7,
			}},
			Score: 17,
		},
		{
			MessageID: "msg-empty",
			Timestamp: mustTime("2026-07-18T15:00:00Z"),
			Iterations: []claude.Iteration{{
				Model: "unknown", RawInput: 1, CacheCreation: 4, Output: 2,
			}},
			Score: 7,
		},
		{
			MessageID: "msg-multi",
			Timestamp: mustTime("2026-07-18T16:00:00Z"),
			Iterations: []claude.Iteration{
				{
					Model: "iteration-model", RawInput: 10, CacheRead: 20,
					CacheCreation: 30, CacheWrite5m: 5, CacheWrite1h: 10, Output: 40,
				},
				{Model: "fallback-model", RawInput: 1, CacheRead: 2, CacheCreation: 3, Output: 4},
				{Model: "zero-model"},
			},
			Score: 110,
		},
	}
	wantStats := claude.FileStats{
		Lines:             10,
		MatchedLines:      8,
		Candidates:        3,
		MalformedLines:    2,
		MissingMessageIDs: 1,
	}

	if !reflect.DeepEqual(gotCandidates, wantCandidates) {
		t.Errorf("ParseFile() candidates = %#v, want %#v", gotCandidates, wantCandidates)
	}
	if gotStats != wantStats {
		t.Errorf("ParseFile() stats = %+v, want %+v", gotStats, wantStats)
	}
}

func TestDeduperAcrossFiles(t *testing.T) {
	t.Parallel()

	deduper := claude.NewDeduper()
	for _, fixture := range []string{"duplicates_a.jsonl", "duplicates_b.jsonl"} {
		candidates, stats := parseFixture(t, fixture)
		if stats.Candidates != 3 || stats.MalformedLines != 0 {
			t.Fatalf("ParseFile(%q) stats = %+v, want 3 candidates and no malformed lines", fixture, stats)
		}
		for _, candidate := range candidates {
			deduper.Add(candidate)
		}
	}

	wantStats := claude.DedupeStats{
		RetainedMessages:             3,
		DuplicateRecords:             3,
		DifferingDuplicateRecords:    2,
		CacheWriteUnclassifiedTokens: 3,
	}
	if got := deduper.Stats(); got != wantStats {
		t.Errorf("Stats() = %+v, want %+v", got, wantStats)
	}

	var gotEvents []ingest.UsageEvent
	deduper.Emit(func(event ingest.UsageEvent) {
		gotEvents = append(gotEvents, event)
	})
	wantEvents := []ingest.UsageEvent{
		event("2026-07-18T10:00:00Z", "equal-first", 2, 0, 0, 0, 0, 1),
		event("2026-07-18T11:00:00Z", "replacement", 8, 0, 1, 1, 1, 2),
		event("2026-07-18T12:00:00Z", "winner", 6, 0, 0, 0, 2, 1),
	}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Errorf("Emit() events = %#v, want %#v", gotEvents, wantEvents)
	}
}

func TestDeduperEmitIterationsAndSkipZeroTotal(t *testing.T) {
	t.Parallel()

	candidates, _ := parseFixture(t, "stage1.jsonl")
	deduper := claude.NewDeduper()
	for _, candidate := range candidates {
		deduper.Add(candidate)
	}

	var gotEvents []ingest.UsageEvent
	deduper.Emit(func(event ingest.UsageEvent) {
		gotEvents = append(gotEvents, event)
	})
	wantEvents := []ingest.UsageEvent{
		event("2026-07-18T14:00:00Z", "claude-main", 10, 3, 1, 2, 2, 7),
		event("2026-07-18T15:00:00Z", "unknown", 5, 0, 0, 0, 4, 2),
		event("2026-07-18T16:00:00Z", "iteration-model", 60, 20, 5, 10, 15, 40),
		event("2026-07-18T16:00:00Z", "fallback-model", 6, 2, 0, 0, 3, 4),
	}
	if !reflect.DeepEqual(gotEvents, wantEvents) {
		t.Errorf("Emit() events = %#v, want %#v", gotEvents, wantEvents)
	}

	wantStats := claude.DedupeStats{RetainedMessages: 3, CacheWriteUnclassifiedTokens: 24}
	if got := deduper.Stats(); got != wantStats {
		t.Errorf("Stats() = %+v, want %+v", got, wantStats)
	}
}

func TestDeduperRetainedReturnsCopies(t *testing.T) {
	t.Parallel()

	deduper := claude.NewDeduper()
	deduper.Add(claude.Candidate{
		MessageID:  "message",
		Timestamp:  mustTime("2026-07-18T10:00:00Z"),
		Iterations: []claude.Iteration{{Model: "original", RawInput: 2}},
	})

	deduper.Retained(func(candidate claude.Candidate) {
		candidate.Iterations[0].Model = "mutated"
	})
	var gotModel string
	deduper.Emit(func(event ingest.UsageEvent) {
		gotModel = event.Model
	})
	if gotModel != "original" {
		t.Fatalf("Emit() model = %q after retained copy mutation, want %q", gotModel, "original")
	}
}

func TestUnclassifiedCacheWriteNeverNegative(t *testing.T) {
	t.Parallel()

	deduper := claude.NewDeduper()
	deduper.Add(claude.Candidate{
		MessageID: "overclassified",
		Timestamp: mustTime("2026-07-18T10:00:00Z"),
		Iterations: []claude.Iteration{{
			Model: "model", CacheCreation: 2, CacheWrite5m: 3, CacheWrite1h: 4, Output: 1,
		}},
	})

	if got := deduper.Stats().CacheWriteUnclassifiedTokens; got != 0 {
		t.Errorf("Stats().CacheWriteUnclassifiedTokens = %d, want 0", got)
	}
	deduper.Emit(func(event ingest.UsageEvent) {
		if event.CacheWriteUnclassified != 0 {
			t.Errorf("Emit() CacheWriteUnclassified = %d, want 0", event.CacheWriteUnclassified)
		}
	})
}

func TestAdapterParseFileLineLargerThanScannerLimit(t *testing.T) {
	t.Parallel()

	line := fmt.Sprintf(
		`{"padding":%q,"timestamp":"2026-07-18T10:00:00Z","type":"assistant","message":{"id":"long-line","model":"long-model","usage":{"input_tokens":7,"output_tokens":3}}}`,
		strings.Repeat("x", scannerDefaultMaxTokenSize),
	)
	if len(line) <= scannerDefaultMaxTokenSize {
		t.Fatalf("test line length = %d, want > %d", len(line), scannerDefaultMaxTokenSize)
	}
	path := filepath.Join(t.TempDir(), "long.jsonl")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("write long-line fixture: %v", err)
	}

	var candidates []claude.Candidate
	stats, err := (claude.Adapter{}).ParseFile(discover.SourceFile{
		Provider: discover.ProviderClaude,
		Path:     path,
	}, func(candidate claude.Candidate) {
		candidates = append(candidates, candidate)
	})
	if err != nil {
		t.Fatalf("ParseFile() error = %v", err)
	}
	if stats.Lines != 1 || stats.Candidates != 1 || len(candidates) != 1 {
		t.Fatalf("ParseFile() stats = %+v, candidates = %d; want one parsed candidate", stats, len(candidates))
	}
}

func TestAdapterParseFileOpenError(t *testing.T) {
	t.Parallel()

	_, err := (claude.Adapter{}).ParseFile(discover.SourceFile{
		Path: filepath.Join(t.TempDir(), "missing.jsonl"),
	}, func(claude.Candidate) {})
	if err == nil {
		t.Fatal("ParseFile() error = nil, want an open error")
	}
}

func parseFixture(t *testing.T, name string) ([]claude.Candidate, claude.FileStats) {
	t.Helper()

	var candidates []claude.Candidate
	stats, err := (claude.Adapter{}).ParseFile(discover.SourceFile{
		Provider: discover.ProviderClaude,
		Path:     filepath.Join("testdata", name),
	}, func(candidate claude.Candidate) {
		candidates = append(candidates, candidate)
	})
	if err != nil {
		t.Fatalf("ParseFile(%q) error = %v", name, err)
	}
	return candidates, stats
}

func event(timestamp, model string, input, cacheRead, cacheWrite5m, cacheWrite1h, unclassified, output int64) ingest.UsageEvent {
	return ingest.UsageEvent{
		Timestamp:              mustTime(timestamp),
		Provider:               discover.ProviderClaude,
		Model:                  model,
		Input:                  input,
		CacheRead:              cacheRead,
		CacheWrite5m:           cacheWrite5m,
		CacheWrite1h:           cacheWrite1h,
		CacheWriteUnclassified: unclassified,
		Output:                 output,
		Reasoning:              0,
	}
}

func mustTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

const scannerDefaultMaxTokenSize = 64 * 1024
