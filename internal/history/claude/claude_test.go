package claude

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

func TestExtractSyntheticTranscriptFork(t *testing.T) {
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
	if len(result.Relationships) != 1 || result.Relationships[0].Kind != history.RelationFork ||
		result.Relationships[0].ParentNativeSessionID != "11111111-1111-4111-8111-111111111111" ||
		result.Relationships[0].ParentNativeMessageID != "11111111-1111-4111-8111-111111111101" {
		t.Fatalf("fork relationships = %#v", result.Relationships)
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

func TestExtractAssistantConsentIndexesOnlyTextBlocks(t *testing.T) {
	var records []jsonl.Record
	_, err := jsonl.ReadPositioned(filepath.Join("testdata", "history.jsonl"), jsonl.Position{}, func(record jsonl.Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		records = append(records, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	result := ExtractWithOptions(history.SourceReference{Provider: history.ProviderClaude, Kind: history.LocationProviderLive, Path: "session.jsonl"}, records, history.ExtractionOptions{IndexAssistant: true})
	var assistants []history.Prompt
	for _, prompt := range result.Prompts {
		if prompt.Role == history.RoleAssistant {
			assistants = append(assistants, prompt)
		}
	}
	if len(assistants) != 1 || assistants[0].CleanText != "Working." || assistants[0].Model != "claude-test" || assistants[0].Classification != history.ClassificationAssistant || !assistants[0].Searchable {
		t.Fatalf("assistant prompts = %#v", assistants)
	}
}

func TestExtractAssistantRejectsScalarThinkingAndToolOnlyContent(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte(`{"type":"assistant","uuid":"scalar","sessionId":"session-1","message":{"role":"assistant","content":"not a text block"}}`), LineNumber: 1, EndOffset: 140},
		{Raw: []byte(`{"type":"assistant","uuid":"blocks","sessionId":"session-1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"secret"},{"type":"tool_use","name":"Read","input":{"path":"private"}}]}}`), LineNumber: 2, StartOffset: 140, EndOffset: 340},
		{Raw: []byte(`{"type":"assistant","uuid":"meta","sessionId":"session-1","isMeta":true,"message":{"role":"assistant","content":[{"type":"text","text":"injected metadata"}]}}`), LineNumber: 3, StartOffset: 340, EndOffset: 500},
		{Raw: []byte(`{"type":"assistant","uuid":"compact","sessionId":"session-1","isCompactSummary":true,"message":{"role":"assistant","content":[{"type":"text","text":"compact summary"}]}}`), LineNumber: 4, StartOffset: 500, EndOffset: 680},
	}
	result := ExtractWithOptions(history.SourceReference{Provider: history.ProviderClaude, Path: "session.jsonl"}, records, history.ExtractionOptions{IndexAssistant: true})
	if len(result.Prompts) != 0 || len(result.Diagnostics) != 4 {
		t.Fatalf("non-text assistant content = %#v", result)
	}
}

func TestExtractRootAndSubagentFromProviderPath(t *testing.T) {
	rootRecord := jsonl.Record{Raw: []byte("{\"type\":\"user\",\"uuid\":\"root-prompt\",\"sessionId\":\"parent\",\"message\":{\"role\":\"user\",\"content\":\"root work\"}}\n"), LineNumber: 1, EndOffset: 130}
	root := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "/claude/projects/project/parent.jsonl"}, []jsonl.Record{rootRecord})
	if root.Session.ThreadKind != history.ThreadRoot || root.Session.ThreadConfidence != history.ConfidenceDerived || root.Session.ThreadEvidence != "source_path.main_transcript" {
		t.Fatalf("root path classification = %#v", root.Session)
	}

	var subagentRecords []jsonl.Record
	_, err := jsonl.ReadPositioned(filepath.Join("testdata", "subagent.jsonl"), jsonl.Position{}, func(record jsonl.Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		subagentRecords = append(subagentRecords, record)
	})
	if err != nil {
		t.Fatal(err)
	}
	subagent := Extract(history.SourceReference{Provider: history.ProviderClaude, Path: "/claude/projects/project/33333333-3333-4333-8333-333333333333/subagents/agent-child.jsonl"}, subagentRecords)
	if subagent.Session.ThreadKind != history.ThreadSubagent || subagent.Session.NativeSessionID != "33333333-3333-4333-8333-333333333333/subagents/agent-child" ||
		subagent.Session.ParentNativeSessionID != "33333333-3333-4333-8333-333333333333" || subagent.Session.Originator != "agent-child" || len(subagent.Prompts) != 1 {
		t.Fatalf("subagent path classification = %#v prompts=%#v", subagent.Session, subagent.Prompts)
	}
	if len(subagent.Relationships) != 1 || subagent.Relationships[0].Kind != history.RelationSubagent ||
		subagent.Relationships[0].ProviderNativeValue != "subagents/agent-child" {
		t.Fatalf("subagent relationship = %#v", subagent.Relationships)
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
