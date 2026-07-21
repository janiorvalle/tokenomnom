package claude

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

func TestExtractSyntheticTranscript(t *testing.T) {
	var records []jsonl.Record
	_, err := jsonl.ReadPositioned(filepath.Join("testdata", "history.jsonl"), jsonl.Position{}, func(record jsonl.Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		records = append(records, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Kind: history.LocationProviderLive, Path: "session.jsonl"}, records)
	if result.Session.NativeSessionID != "22222222-2222-4222-8222-222222222222" || result.Session.ParentNativeSessionID != "" || result.Session.ForkedFromSessionID == "" || result.Session.ThreadKind != history.ThreadUnknown {
		t.Fatalf("session = %#v", result.Session)
	}
	if len(result.Prompts) != 4 || len(result.Occurrences) != 5 {
		t.Fatalf("prompts=%d occurrences=%d", len(result.Prompts), len(result.Occurrences))
	}
	if got := result.Prompts[0]; got.Classification != history.ClassificationToolResult || got.Searchable {
		t.Fatalf("tool result = %#v", got)
	}
	if got := result.Prompts[1]; got.Classification != history.ClassificationAgentInstruction || got.Searchable {
		t.Fatalf("meta = %#v", got)
	}
	if got := result.Prompts[2]; got.CleanText != "Keep <example>XML</example>.\nSecond block." || !got.Searchable {
		t.Fatalf("mixed = %#v", got)
	}
	if got := result.Prompts[3]; got.Classification != history.ClassificationLocalCommand || got.Searchable || got.Timestamp != nil {
		t.Fatalf("command = %#v", got)
	}
}

func TestExtractWithSessionPreservesSuffixIdentity(t *testing.T) {
	source := history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}
	initialRecord := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"timestamp\":\"2026-07-20T12:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"first\"}}\n"), LineNumber: 1, EndOffset: 150}
	initial := Extract(source, []jsonl.Record{initialRecord})
	suffixRecord := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p2\",\"timestamp\":\"2026-07-20T12:01:00Z\",\"message\":{\"role\":\"user\",\"content\":\"suffix\"}}\n"), LineNumber: 2, StartOffset: 150, EndOffset: 280}
	suffix := ExtractWithSession(source, []jsonl.Record{suffixRecord}, initial.Session)
	if suffix.Session.IdentityKey != initial.Session.IdentityKey || suffix.Session.NativeSessionID != "session-1" {
		t.Fatalf("suffix = %#v initial = %#v", suffix.Session, initial.Session)
	}
	if suffix.Session.ThreadKind != history.ThreadUnknown {
		t.Fatalf("unproven relationship = %q", suffix.Session.ThreadKind)
	}
}

func TestExtractWithSessionPromotesEmptyPathFallback(t *testing.T) {
	source := history.SourceReference{Provider: history.ProviderClaude, Path: "/old/session.jsonl"}
	empty := Extract(source, nil)
	record := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"message\":{\"role\":\"user\",\"content\":\"first\"}}\n"), LineNumber: 1, EndOffset: 100}
	appended := ExtractWithSession(source, []jsonl.Record{record}, empty.Session)
	if !strings.HasPrefix(empty.Session.FallbackKey, "source-path:") || !strings.HasPrefix(appended.Session.FallbackKey, "first-record:") || appended.Session.IdentityKey == empty.Session.IdentityKey {
		t.Fatalf("fallback transition = %#v -> %#v", empty.Session, appended.Session)
	}
}

func TestExtractRejectsImageOnlyUserRecord(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"image\",\"source\":{}}]}}\n"), LineNumber: 1, EndOffset: 140}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, []jsonl.Record{record})
	if len(result.Prompts) != 0 || len(result.Occurrences) != 0 || len(result.Diagnostics) == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExtractRejectsMismatchedNestedRole(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"message\":{\"role\":\"assistant\",\"content\":\"not human\"}}\n"), LineNumber: 1, EndOffset: 120}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, []jsonl.Record{record})
	if len(result.Prompts) != 0 || len(result.Diagnostics) == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExtractReportsMalformedNestedMessage(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"message\":42}\n"), LineNumber: 1, EndOffset: 80}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, []jsonl.Record{record})
	if len(result.Prompts) != 0 || len(result.Diagnostics) == 0 {
		t.Fatalf("malformed nested message = %#v", result)
	}
}

func TestExtractRejectsConflictingSessionID(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"timestamp\":\"2026-07-20T12:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"first\"}}\n"), LineNumber: 1, EndOffset: 150},
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p2\",\"sessionId\":\"session-2\",\"timestamp\":\"2026-07-20T13:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"foreign\"}}\n"), LineNumber: 2, StartOffset: 150, EndOffset: 300},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, records)
	if result.Session.NativeSessionID != "session-1" || result.Session.IdentityKey != "native:session-1" || len(result.Prompts) != 1 || result.Prompts[0].CleanText != "first" {
		t.Fatalf("mixed session result = %#v", result)
	}
	if len(result.Diagnostics) == 0 || result.Session.LastTimestamp == nil || result.Session.LastTimestamp.Hour() != 12 {
		t.Fatalf("mixed session diagnostics/range = %#v", result)
	}
}

func TestExtractStopsAfterConflictingSessionID(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"message\":{\"role\":\"user\",\"content\":\"first\"}}\n"), LineNumber: 1, EndOffset: 120},
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p2\",\"sessionId\":\"session-2\",\"message\":{\"role\":\"user\",\"content\":\"foreign\"}}\n"), LineNumber: 2, StartOffset: 120, EndOffset: 240},
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p3\",\"message\":{\"role\":\"user\",\"content\":\"idless suffix\"}}\n"), LineNumber: 3, StartOffset: 240, EndOffset: 340},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, records)
	if result.Session.NativeSessionID != "session-1" || len(result.Prompts) != 1 || result.Prompts[0].CleanText != "first" || len(result.Diagnostics) == 0 {
		t.Fatalf("conflicting suffix result = %#v", result)
	}
}

func TestExtractRepeatedNativeMessageCanonicalIsOrderIndependent(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"timestamp\":\"2026-07-20T12:01:00Z\",\"message\":{\"role\":\"user\",\"content\":\"final text\"}}\n"), LineNumber: 1, EndOffset: 160},
		{Raw: []byte("{\"type\":\"user\",\"uuid\":\"p1\",\"sessionId\":\"session-1\",\"timestamp\":\"2026-07-20T12:00:00Z\",\"message\":{\"role\":\"user\",\"content\":\"stale draft\"}}\n"), LineNumber: 2, StartOffset: 160, EndOffset: 320},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, records)
	if len(result.Prompts) != 1 || result.Prompts[0].CleanText != "final text" {
		t.Fatalf("canonical repeated message = %#v", result.Prompts)
	}
}
