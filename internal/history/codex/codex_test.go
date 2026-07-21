package codex

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

func TestExtractSyntheticRolloutSubagentOrigin(t *testing.T) {
	records := readRecords(t, filepath.Join("testdata", "history.jsonl"))
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationProviderLive, Path: "rollout.jsonl"}, records)
	if result.Session.NativeSessionID != "11111111-1111-4111-8111-111111111111" || result.Session.ParentNativeSessionID == "" || result.Session.ThreadKind != history.ThreadSubagent {
		t.Fatalf("session = %#v", result.Session)
	}
	if result.Session.Branch != "feature/history" || result.Session.CWD != "/workspace/demo" || result.Session.Originator != "codex_cli_rs" {
		t.Fatalf("metadata = %#v", result.Session)
	}
	if len(result.Relationships) != 1 || result.Relationships[0].Kind != history.RelationSubagent ||
		result.Relationships[0].ParentNativeSessionID != "00000000-0000-4000-8000-000000000001" ||
		result.Relationships[0].Evidence != "session_meta.source.subagent" {
		t.Fatalf("subagent relationships = %#v", result.Relationships)
	}
	if len(result.Prompts) != 4 || len(result.Occurrences) != 5 {
		t.Fatalf("prompts=%d occurrences=%d diagnostics=%#v", len(result.Prompts), len(result.Occurrences), result.Diagnostics)
	}
	if result.Occurrences[0].PromptKey != result.Occurrences[1].PromptKey {
		t.Fatal("paired Codex representations did not reconcile")
	}
	if got := result.Prompts[0]; got.CleanText != "Explain `héllo`.\n```go\nfmt.Println(\"hi\")\n```" || !got.Searchable {
		t.Fatalf("human prompt = %#v", got)
	}
	if got := result.Prompts[1]; got.Classification != history.ClassificationSystemInjected || got.Searchable {
		t.Fatalf("injected prompt = %#v", got)
	}
	if got := result.Prompts[2]; got.Classification != history.ClassificationLocalCommand || got.Searchable {
		t.Fatalf("command prompt = %#v", got)
	}
	if got := result.Prompts[3]; got.CleanText != "Keep <request>XML</request> and \nmixed text blocks." || got.Searchable || got.Classification != history.ClassificationProviderMetadata || got.Timestamp != nil {
		t.Fatalf("mixed prompt = %#v", got)
	}
	if len(result.Diagnostics) < 4 {
		t.Fatalf("diagnostics = %#v", result.Diagnostics)
	}
}

func TestExtractAssistantConsentIndexesOnlyOutputText(t *testing.T) {
	records := readRecords(t, filepath.Join("testdata", "history.jsonl"))
	result := ExtractWithOptions(history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationProviderLive, Path: "rollout.jsonl"}, records, history.ExtractionOptions{IndexAssistant: true})
	var assistants []history.Prompt
	for _, prompt := range result.Prompts {
		if prompt.Role == history.RoleAssistant {
			assistants = append(assistants, prompt)
		}
	}
	if len(assistants) != 1 || assistants[0].CleanText != "Sure." || assistants[0].Classification != history.ClassificationAssistant || !assistants[0].Searchable {
		t.Fatalf("assistant prompts = %#v", assistants)
	}
	for _, prompt := range result.Prompts {
		if strings.Contains(prompt.CleanText, "done") || strings.Contains(prompt.CleanText, "shell") {
			t.Fatalf("tool content became searchable: %#v", prompt)
		}
	}
}

func TestExtractOversizedAssistantIsVisibleAndNotSearchable(t *testing.T) {
	text := strings.Repeat("a", history.MaxPromptBytes+1)
	record := jsonl.Record{Raw: []byte(fmt.Sprintf(`{"timestamp":"2026-07-20T12:00:00Z","type":"response_item","payload":{"type":"message","id":"assistant-large","role":"assistant","content":[{"type":"output_text","text":%q}]}}`, text)), LineNumber: 1, EndOffset: int64(len(text) + 200)}
	result := ExtractWithOptions(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, []jsonl.Record{record}, history.ExtractionOptions{IndexAssistant: true})
	if len(result.Prompts) != 1 || !result.Prompts[0].Oversized || result.Prompts[0].Searchable || len(result.Prompts[0].CleanText) != len(text) {
		t.Fatalf("oversized assistant = %#v", result.Prompts)
	}
}

func TestExtractFallbackSessionIdentityIsStableAcrossMove(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"hello\"}}\n"), LineNumber: 1, EndOffset: 88}
	a := Extract(history.SourceReference{Path: "/old/file.jsonl"}, []jsonl.Record{record})
	b := Extract(history.SourceReference{Path: "/new/file.jsonl"}, []jsonl.Record{record})
	if a.Session.IdentityKey != b.Session.IdentityKey || a.Session.FallbackKey == "" {
		t.Fatalf("fallback identities differ: %q %q", a.Session.IdentityKey, b.Session.IdentityKey)
	}
}

func TestExtractWithSessionPreservesIdentityForSuffix(t *testing.T) {
	meta := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\",\"id\":\"session-1\",\"timestamp\":\"2026-07-20T12:00:00Z\",\"cwd\":\"/tmp\",\"originator\":\"test\"}}\n"), LineNumber: 1, EndOffset: 190}
	source := history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}
	initial := Extract(source, []jsonl.Record{meta})
	suffix := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"p1\",\"message\":\"suffix\"}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 330}
	appended := ExtractWithSession(source, []jsonl.Record{suffix}, initial.Session)
	if appended.Session.IdentityKey != initial.Session.IdentityKey || appended.Session.NativeSessionID != "session-1" {
		t.Fatalf("suffix session = %#v, initial = %#v", appended.Session, initial.Session)
	}
}

func TestExtractWithSessionPromotesEmptyPathFallback(t *testing.T) {
	source := history.SourceReference{Provider: history.ProviderCodex, Path: "/old/rollout.jsonl"}
	empty := Extract(source, nil)
	record := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"suffix\"}}\n"), LineNumber: 1, EndOffset: 130}
	appended := ExtractWithSession(source, []jsonl.Record{record}, empty.Session)
	if !strings.HasPrefix(empty.Session.FallbackKey, "source-path:") || !strings.HasPrefix(appended.Session.FallbackKey, "first-record:") || appended.Session.IdentityKey == empty.Session.IdentityKey {
		t.Fatalf("fallback transition = %#v -> %#v", empty.Session, appended.Session)
	}
}

func TestExtractLeavesUnprovenRootUnknownAndRejectsEmptyText(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\",\"id\":\"session-1\",\"timestamp\":\"2026-07-20T12:00:00Z\",\"cwd\":\"/tmp\",\"originator\":\"test\"}}\n"), LineNumber: 1, EndOffset: 190},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_image\",\"image_url\":\"x\"}]}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 350},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if result.Session.ThreadKind != history.ThreadUnknown || len(result.Prompts) != 0 || len(result.Diagnostics) == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExtractDoesNotTreatForkAsSubagent(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"fork\",\"id\":\"fork\",\"forked_from_id\":\"parent\",\"timestamp\":\"2026-07-20T12:00:00Z\",\"cwd\":\"/tmp\",\"originator\":\"test\",\"source\":\"cli\"}}\n"), LineNumber: 1, EndOffset: 230}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, []jsonl.Record{record})
	if result.Session.ThreadKind != history.ThreadUnknown || result.Session.ParentNativeSessionID != "" || result.Session.ForkedFromSessionID != "parent" {
		t.Fatalf("fork relationship = %#v", result.Session)
	}
	if len(result.Relationships) != 1 || result.Relationships[0].Kind != history.RelationFork || result.Relationships[0].ParentNativeSessionID != "parent" {
		t.Fatalf("fork evidence = %#v", result.Relationships)
	}
}

func TestExtractRootFromProviderThreadSource(t *testing.T) {
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, readRecords(t, filepath.Join("testdata", "root.jsonl")))
	if result.Session.ThreadKind != history.ThreadRoot || result.Session.ThreadEvidence != "session_meta.thread_source=user" ||
		result.Session.ThreadConfidence != history.ConfidenceExact || result.Session.ThreadRuleVersion != history.RelationshipRuleVersion {
		t.Fatalf("root classification = %#v", result.Session)
	}
}

func TestExtractPairKeepsNativeResponseIdentityAcrossSuffixes(t *testing.T) {
	source := history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}
	response := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"same text\"}]}}\n"), LineNumber: 1, EndOffset: 190}
	event := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-1\",\"message\":\"same text\"}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 350}

	first, state := ExtractWithState(source, []jsonl.Record{response}, State{})
	second, _ := ExtractWithState(source, []jsonl.Record{event}, state)
	if first.Prompts[0].LogicalKey != "native:response-1" || second.Prompts[0].LogicalKey != "native:response-1" || second.Prompts[0].NativeMessageID != "response-1" {
		t.Fatalf("paired identities = %#v %#v", first.Prompts, second.Prompts)
	}
}

func TestExtractIDLessPairIdentitySurvivesLineShifts(t *testing.T) {
	pair := func(firstLine int64) history.Extraction {
		response := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"same text\"}]}}\n"), LineNumber: firstLine, EndOffset: 180}
		event := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"same text\"}}\n"), LineNumber: firstLine + 1, StartOffset: 180, EndOffset: 330}
		return Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, []jsonl.Record{response, event})
	}
	first, shifted := pair(1), pair(10)
	if len(first.Prompts) != 1 || len(shifted.Prompts) != 1 || first.Prompts[0].LogicalKey != shifted.Prompts[0].LogicalKey {
		t.Fatalf("line-shifted pair identities = %#v %#v", first.Prompts, shifted.Prompts)
	}
}

func TestExtractNativeMessageIdentitySurvivesTextChanges(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"draft\"}]}}\n"), LineNumber: 1, EndOffset: 180},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:01Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"final text\"}]}}\n"), LineNumber: 2, StartOffset: 180, EndOffset: 370},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 1 || len(result.Occurrences) != 2 || result.Prompts[0].LogicalKey != "native:response-1" || result.Prompts[0].CleanText != "final text" {
		t.Fatalf("progressive response = %#v", result)
	}
}

func TestExtractRepeatedNativeMessageCanonicalIsOrderIndependent(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"final text\"}]}}\n"), LineNumber: 1, EndOffset: 190},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"stale draft\"}]}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 380},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 1 || result.Prompts[0].CleanText != "final text" {
		t.Fatalf("canonical repeated message = %#v", result.Prompts)
	}
}

func TestExtractDoesNotMergeDistinctNativeMessages(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-1\",\"message\":\"same text\"}}\n"), LineNumber: 1, EndOffset: 160},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-2\",\"message\":\"same text\"}}\n"), LineNumber: 2, StartOffset: 160, EndOffset: 320},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 2 || result.Prompts[0].LogicalKey == result.Prompts[1].LogicalKey {
		t.Fatalf("distinct native messages = %#v", result.Prompts)
	}
}

func TestExtractDoesNotTreatClientIDAsNativeIdentity(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"reused\",\"message\":\"first\"}}\n"), LineNumber: 1, EndOffset: 150},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"reused\",\"message\":\"second\"}}\n"), LineNumber: 2, StartOffset: 150, EndOffset: 300},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 2 || result.Prompts[0].LogicalKey == result.Prompts[1].LogicalKey || result.Prompts[0].NativeMessageID != "" || result.Prompts[1].NativeMessageID != "" {
		t.Fatalf("client ID identities = %#v", result.Prompts)
	}
}

func TestExtractDoesNotPairSameTextAtDifferentTimes(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"same text\"}]}}\n"), LineNumber: 1, EndOffset: 190},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-1\",\"message\":\"same text\"}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 350},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 2 || result.Prompts[0].LogicalKey == result.Prompts[1].LogicalKey {
		t.Fatalf("different-time messages = %#v", result.Prompts)
	}
}

func TestExtractDoesNotPairMillisecondSkew(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"same text\"}]}}\n"), LineNumber: 1, EndOffset: 190},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00.001Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"same text\"}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 340},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 2 {
		t.Fatalf("millisecond-skewed messages paired = %#v", result.Prompts)
	}
}

func TestExtractFallbackSessionConfidenceIsDerived(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"cwd\":\"/tmp\"}}\n"), LineNumber: 1, EndOffset: 100}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, []jsonl.Record{record})
	if result.Session.NativeSessionID != "" || result.Session.Confidence != history.ConfidenceDerived || !strings.HasPrefix(result.Session.IdentityKey, "fallback:") {
		t.Fatalf("fallback session confidence = %#v", result.Session)
	}
}

func TestExtractPairsAcrossInterveningMetadata(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"same text\"}]}}\n"), LineNumber: 1, EndOffset: 190},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"test\"}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 290},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-1\",\"message\":\"same text\"}}\n"), LineNumber: 3, StartOffset: 290, EndOffset: 450},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 1 || len(result.Occurrences) != 2 {
		t.Fatalf("intervening metadata pair = %#v", result)
	}
}

func TestExtractUnmatchedUserEventClearsPendingPair(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"response-1\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"same text\"}]}}\n"), LineNumber: 1, EndOffset: 190},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00.001Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-other\",\"message\":\"other text\"}}\n"), LineNumber: 2, StartOffset: 190, EndOffset: 350},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00.002Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":\"event-same\",\"message\":\"same text\"}}\n"), LineNumber: 3, StartOffset: 350, EndOffset: 510},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if len(result.Prompts) != 3 || result.Prompts[0].LogicalKey == result.Prompts[2].LogicalKey {
		t.Fatalf("unmatched event retained pending pair = %#v", result.Prompts)
	}
}

func TestExtractRejectsConflictingSessionMetadata(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\",\"cwd\":\"/one\"}}\n"), LineNumber: 1, EndOffset: 130},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T13:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-2\",\"cwd\":\"/two\"}}\n"), LineNumber: 2, StartOffset: 130, EndOffset: 260},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if result.Session.NativeSessionID != "session-1" || result.Session.IdentityKey != "native:session-1" || result.Session.CWD != "/one" {
		t.Fatalf("mixed session result = %#v", result)
	}
	if len(result.Diagnostics) == 0 || result.Session.LastTimestamp == nil || result.Session.LastTimestamp.Hour() != 12 {
		t.Fatalf("mixed session diagnostics/range = %#v", result)
	}
}

func TestExtractStopsAfterConflictingSessionMetadata(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\"}}\n"), LineNumber: 1, EndOffset: 110},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"session one\"}}\n"), LineNumber: 2, StartOffset: 110, EndOffset: 240},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T13:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-2\"}}\n"), LineNumber: 3, StartOffset: 240, EndOffset: 350},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T13:01:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"session two\"}}\n"), LineNumber: 4, StartOffset: 350, EndOffset: 480},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if result.Session.NativeSessionID != "session-1" || len(result.Prompts) != 1 || result.Prompts[0].CleanText != "session one" || len(result.Diagnostics) == 0 {
		t.Fatalf("conflicting suffix result = %#v", result)
	}
}

func TestExtractReportsMalformedSupportedPayload(t *testing.T) {
	record := jsonl.Record{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"event_msg\",\"payload\":42}\n"), LineNumber: 1, EndOffset: 80}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, []jsonl.Record{record})
	if len(result.Prompts) != 0 || len(result.Diagnostics) == 0 {
		t.Fatalf("malformed supported payload = %#v", result)
	}
}

func TestExtractSparseSessionMetadataDoesNotEraseValues(t *testing.T) {
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\",\"cwd\":\"/one\",\"originator\":\"codex\",\"forked_from_id\":\"fork\",\"git\":{\"branch\":\"main\",\"repository_url\":\"repo\"}}}\n"), LineNumber: 1, EndOffset: 240},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T13:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\"}}\n"), LineNumber: 2, StartOffset: 240, EndOffset: 350},
	}
	result := Extract(history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}, records)
	if result.Session.CWD != "/one" || result.Session.Originator != "codex" || result.Session.ForkedFromSessionID != "fork" || result.Session.Branch != "main" || result.Session.RepositoryIdentity != "repo" {
		t.Fatalf("sparse metadata erased values: %#v", result.Session)
	}
}

func TestExtractNativeDiscoverySurvivesLaterSparseMetadata(t *testing.T) {
	source := history.SourceReference{Provider: history.ProviderCodex, Path: "rollout.jsonl"}
	prior := Extract(source, nil).Session
	records := []jsonl.Record{
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"session_id\":\"session-1\"}}\n"), LineNumber: 1, EndOffset: 110},
		{Raw: []byte("{\"timestamp\":\"2026-07-20T12:01:00Z\",\"type\":\"session_meta\",\"payload\":{}}\n"), LineNumber: 2, StartOffset: 110, EndOffset: 200},
	}
	result, _ := ExtractWithState(source, records, State{Session: prior})
	if result.Session.NativeSessionID != "session-1" || result.Session.IdentityKey != "native:session-1" {
		t.Fatalf("native discovery = %#v", result.Session)
	}
}

func readRecords(t *testing.T, path string) []jsonl.Record {
	t.Helper()
	var records []jsonl.Record
	_, err := jsonl.ReadPositioned(path, jsonl.Position{}, func(record jsonl.Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		records = append(records, record)
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return records
}
