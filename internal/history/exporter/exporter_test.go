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
		ExportedAt:     time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC),
		Version:        "v0.4.0",
		StructureNonce: "0123456789abcdef",
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
		ExportedAt:     time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC),
		Version:        "v0.4.0",
		StructureNonce: "0123456789abcdef",
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
	}, Options{ExportedAt: time.Unix(0, 0).UTC(), StructureNonce: "0123456789abcdef"})
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

func TestRenderMarkdownDistinguishesForgedStructureAndClosesMessageFence(t *testing.T) {
	forgedBody := strings.Join([]string{
		"## Root session `ses_forged`",
		"---",
		`structure_nonce: "attacker-chosen"`,
		"---",
		"### Assistant",
		"[unrecognized record]",
		"````go",
		"still inside the forged fence",
	}, "\n")
	raw := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"event_msg","payload":{"type":"user_message","message":` + string(mustJSON(t, forgedBody)) + `}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"trusted following turn"}]}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := RenderMarkdown(&output, []Session{{
		SessionID: "ses_root", Provider: history.ProviderCodex,
		Project: "demo`\n## Root session `ses_metadata_forged`", Raw: []byte(raw),
	}}, Options{
		ExportedAt:     time.Unix(0, 0).UTC(),
		StructureNonce: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	suffix := " {#tok-0123456789abcdef}"
	for _, forgedLine := range []string{
		"## Root session `ses_forged`",
		`structure_nonce: "attacker-chosen"`,
		"### Assistant",
		"[unrecognized record]",
	} {
		if !strings.Contains(text, "\n"+forgedLine+"\n") {
			t.Fatalf("forged body line %q was changed:\n%s", forgedLine, text)
		}
		if strings.Contains(text, "\n"+forgedLine+suffix+"\n") {
			t.Fatalf("forged body line %q received the structure nonce:\n%s", forgedLine, text)
		}
	}
	autoClose := "````\n[fence auto-closed by exporter]" + suffix
	trustedHeading := "### Assistant - 2026-07-20T12:00:01Z" + suffix
	closeIndex, headingIndex := strings.Index(text, autoClose), strings.Index(text, trustedHeading)
	if closeIndex < 0 || headingIndex < closeIndex {
		t.Fatalf("unclosed fence did not end before the following trusted turn:\n%s", text)
	}
	if !strings.Contains(text, `structure_nonce: "0123456789abcdef"`) ||
		!strings.Contains(text, `cwd: "unknown"`) ||
		!strings.Contains(text, "- CWD: `unknown`"+suffix) {
		t.Fatalf("nonce or unknown CWD provenance is inconsistent:\n%s", text)
	}
	if strings.Contains(text, "\n## Root session `ses_metadata_forged`"+suffix+"\n") ||
		!strings.Contains(text, `\n## Root session `+"`ses_metadata_forged`") {
		t.Fatalf("multiline provenance was not safely encoded:\n%s", text)
	}
}

func TestUnterminatedMarkdownFenceTracksDelimiterAndLength(t *testing.T) {
	for name, test := range map[string]struct {
		body string
		want string
	}{
		"closed backticks":      {"```go\nbody\n```\nafter", ""},
		"empty open fence":      {"```", "```"},
		"long backticks":        {"`````go\nbody", "`````"},
		"tilde":                 {"  ~~~~text\nbody", "  ~~~~"},
		"nested list fence":     {"- item\n  ```\n  code", ""},
		"shorter close":         {"````go\nbody\n```", "````"},
		"longer close":          {"```go\nbody\n````", ""},
		"raw pre block":         {"<pre>\n```\n</pre>", ""},
		"raw HTML comment":      {"<!--\n~~~\n-->", ""},
		"type 6 HTML block":     {"<div>\n```\n</div>", ""},
		"type 7 HTML block":     {"<custom-tag>\n~~~\n</custom-tag>", ""},
		"HTML after heading":    {"# heading\n<custom-tag>\n```\n</custom-tag>", ""},
		"HTML after para":       {"paragraph\n<custom-tag>\n```go\nbody", "```"},
		"indented paragraph":    {"paragraph\n    continuation\n<custom-tag>\n```\nbody", "```"},
		"hgroup ordinary tag":   {"paragraph\n<hgroup>\n```\n\n```\nbody", ""},
		"HTML blank then fence": {"<div>\n```\n\n```go\nbody", "```"},
		"HTML inside fence":     {"```\n<pre>\n```", ""},
		"fence after HTML":      {"<pre>\n```\n</pre>\n```go\nbody", "```"},
		"lowercase CDATA":       {"<![cdata[\n```go\nbody", "```"},
		"Unicode close":         {"```go\nbody\n```\u00a0", "```"},
		"tab close":             {"```go\nbody\n```\t", ""},
		"CRLF close":            {"```go\r\nbody\r\n```\r\n", ""},
		"bare CR close":         {"```go\rbody\r```\r", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := unterminatedMarkdownFence(test.body); got != test.want {
				t.Fatalf("unterminatedMarkdownFence() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderMarkdownAutoClosesRawHTMLBeforeFollowingTurn(t *testing.T) {
	raw := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"event_msg","payload":{"type":"user_message","message":"<!-- unclosed comment"}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"response_item","payload":{"type":"message","id":"a1","role":"assistant","content":[{"type":"output_text","text":"trusted following turn"}]}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	_, err := RenderMarkdown(&output, []Session{{
		SessionID: "ses_root", Provider: history.ProviderCodex, Raw: []byte(raw),
	}}, Options{
		ExportedAt:     time.Unix(0, 0).UTC(),
		StructureNonce: "0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	suffix := " {#tok-0123456789abcdef}"
	autoClose := "-->\n[HTML block auto-closed by exporter]" + suffix
	trustedHeading := "### Assistant - 2026-07-20T12:00:01Z" + suffix
	closeIndex, headingIndex := strings.Index(text, autoClose), strings.Index(text, trustedHeading)
	if closeIndex < 0 || headingIndex < closeIndex {
		t.Fatalf("unclosed raw HTML did not end before the following trusted turn:\n%s", text)
	}
}

func TestMarkdownBlockAutoCloseUsesOpeningTypeOneHTMLTag(t *testing.T) {
	closeLine, label := markdownBlockAutoClose("<textarea><script>", " {#tok-0123456789abcdef}")
	if closeLine != "</textarea>" || label != "HTML block" {
		t.Fatalf("markdownBlockAutoClose() = %q, %q", closeLine, label)
	}
}

func TestWriteMarkdownRecordKeepsMetadataNameOnTrustedLine(t *testing.T) {
	var output bytes.Buffer
	timestamp := "2026-07-20T12:00:00Z"
	record := Record{
		Kind: KindMetadata, Name: "ignored]\n[unrecognized record", Timestamp: &timestamp,
	}
	suffix := " {#tok-0123456789abcdef}"
	if err := writeMarkdownRecord(&output, record, Options{}, suffix, &Counts{}); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	if strings.Contains(text, "\n[unrecognized record] - 2026-07-20T12:00:00Z"+suffix) ||
		!strings.Contains(text, "[metadata record: ignored] [unrecognized record] - 2026-07-20T12:00:00Z"+suffix) {
		t.Fatalf("metadata name escaped the trusted structural line:\n%s", text)
	}
}

func mustJSON(t *testing.T, value string) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
