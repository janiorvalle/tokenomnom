package exporter

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestRenderMarkdownCollapsesVolumeAndMarksUnknownRecords(t *testing.T) {
	raw := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"root"}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"response_item","payload":{"type":"message","id":"u1","role":"user","content":[{"type":"input_text","text":"hello\n` + "```go" + `\nfmt.Println(1)\n` + "```" + `"}]}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello\n` + "```go" + `\nfmt.Println(1)\n` + "```" + `"}}`,
		`{"timestamp":"2026-07-20T12:00:02Z","type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
		`{"timestamp":"2026-07-20T12:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{\"cmd\":\"pwd\"}"}}`,
		`{"timestamp":"2026-07-20T12:00:04Z","type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"workspace"}}`,
		`{"timestamp":"2026-07-20T12:00:05Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"private thought"}]}}`,
		`{"timestamp":"2026-07-20T12:00:06Z","type":"event_msg","payload":{"type":"user_message","message":"<environment_context>secret context</environment_context>"}}`,
		`not-json`,
	}, "\n") + "\n"
	first, last := "2026-07-20T12:00:00Z", "2026-07-20T12:00:05Z"
	session := Session{
		SessionID: "ses_root", Provider: history.ProviderCodex, NativeSessionID: "root",
		FirstTimestamp: &first, LastTimestamp: &last, Project: "demo", CWD: "/workspace/demo",
		ThreadKind: history.ThreadRoot, SourceSHA256: strings.Repeat("a", 64), Origin: "provider_live", Raw: []byte(raw),
	}
	var output bytes.Buffer
	counts, err := RenderMarkdown(&output, []Session{session}, Options{
		ExportedAt: time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC), Version: "v0.4.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, fragment := range []string{
		`exported_at: "2026-07-21T01:02:03Z"`,
		"### User - 2026-07-20T12:00:01Z",
		"### Assistant - 2026-07-20T12:00:02Z",
		"[tool call: shell, 13 bytes]",
		"[tool result: shell, 9 bytes]",
		"[thinking omitted: 15 bytes]",
		"[system record: system_injected]",
		"[unrecognized record]",
		"```go\nfmt.Println(1)\n```",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("markdown missing %q:\n%s", fragment, text)
		}
	}
	if strings.Count(text, "### User") != 1 {
		t.Fatalf("paired Codex user records were duplicated:\n%s", text)
	}
	if strings.Contains(text, "\nworkspace\n") || strings.Contains(text, "private thought") || strings.Contains(text, "secret context") {
		t.Fatalf("collapsed content leaked:\n%s", text)
	}
	if counts != (Counts{CollapsedToolRecords: 2, ExcludedThinking: 1, UnrecognizedRecords: 1}) {
		t.Fatalf("counts = %+v", counts)
	}

	output.Reset()
	counts, err = RenderMarkdown(&output, []Session{session}, Options{
		IncludeToolOutput: true, IncludeThinking: true,
		ExportedAt: time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC), Version: "v0.4.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "workspace") || !strings.Contains(output.String(), "private thought") || strings.Count(output.String(), "```") < 4 {
		t.Fatalf("restored Markdown blocks missing:\n%s", output.String())
	}
	if counts != (Counts{UnrecognizedRecords: 1}) {
		t.Fatalf("restored counts = %+v", counts)
	}
}

func TestRenderNormalizedRestoresClaudeToolAndThinkingContent(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"assistant","uuid":"a1","timestamp":"2026-07-20T13:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"working"},{"type":"thinking","thinking":"reason"},{"type":"tool_use","id":"tool-1","name":"Read","input":{"file_path":"demo.go"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-07-20T13:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"file bytes"}]}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	counts, err := RenderNormalized(&output, []Session{{
		SessionID: "ses_child", Provider: history.ProviderClaude, Raw: []byte(raw),
	}}, Options{IncludeToolOutput: true, IncludeThinking: true})
	if err != nil {
		t.Fatal(err)
	}
	if counts != (Counts{}) {
		t.Fatalf("counts = %+v", counts)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("normalized lines = %d:\n%s", len(lines), output.String())
	}
	kinds := []Kind{}
	for _, line := range lines {
		var value Record
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, value.Kind)
		if value.Text == nil {
			t.Fatalf("restored record omitted text: %+v", value)
		}
	}
	if strings.Join([]string{string(kinds[0]), string(kinds[1]), string(kinds[2]), string(kinds[3])}, ",") != "message,thinking,tool_call,tool_result" {
		t.Fatalf("kinds = %v", kinds)
	}
}

func TestRenderMarkdownPlacesIdentifiedDelegationMarker(t *testing.T) {
	rootRaw := `{"timestamp":"2026-07-20T12:00:00Z","type":"response_item","payload":{"type":"message","id":"spawn-message","role":"assistant","content":[{"type":"output_text","text":"delegating"}]}}` + "\n"
	childRaw := `{"type":"user","uuid":"u1","timestamp":"2026-07-20T12:01:00Z","message":{"role":"user","content":"child work"}}` + "\n"
	var output bytes.Buffer
	_, err := RenderMarkdown(&output, []Session{
		{SessionID: "ses_root", Provider: history.ProviderCodex, Raw: []byte(rootRaw)},
		{SessionID: "ses_child", Provider: history.ProviderClaude, Raw: []byte(childRaw), ParentSessionID: "ses_root", ParentNativeMessageID: "spawn-message"},
	}, Options{ExportedAt: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	marker := strings.Index(text, "[delegated to subagent session `ses_child`]")
	assistant := strings.Index(text, "### Assistant")
	child := strings.Index(text, "## Subagent session `ses_child`")
	if assistant < 0 || marker < assistant || child < marker {
		t.Fatalf("delegation marker placement is wrong:\n%s", text)
	}
}
