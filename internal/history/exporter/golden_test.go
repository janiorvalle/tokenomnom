package exporter

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestRenderMarkdownSessionTreeGolden(t *testing.T) {
	rootRaw := strings.Join([]string{
		`{"timestamp":"2026-07-20T12:00:00Z","type":"session_meta","payload":{"id":"native-root"}}`,
		`{"timestamp":"2026-07-20T12:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"timestamp":"2026-07-20T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"shell","call_id":"c1","arguments":"{\"cmd\":\"pwd\"}"}}`,
	}, "\n") + "\n"
	childRaw := `{"type":"assistant","uuid":"a1","timestamp":"2026-07-20T12:01:00Z","message":{"role":"assistant","content":"child done"}}` + "\n"
	firstRoot, lastRoot, childTime := "2026-07-20T12:00:00Z", "2026-07-20T12:00:02Z", "2026-07-20T12:01:00Z"
	sessions := []Session{
		{
			SessionID: "ses_root", NativeSessionID: "native-root", Provider: history.ProviderCodex,
			FirstTimestamp: &firstRoot, LastTimestamp: &lastRoot, Project: "demo", CWD: "/workspace/demo",
			ThreadKind: history.ThreadRoot, SourceSHA256: strings.Repeat("a", 64), Origin: "provider_live", Raw: []byte(rootRaw),
		},
		{
			SessionID: "ses_child", NativeSessionID: "native-child", Provider: history.ProviderClaude,
			FirstTimestamp: &childTime, LastTimestamp: &childTime, Project: "demo", CWD: "/workspace/demo",
			ThreadKind: history.ThreadSubagent, SourceSHA256: strings.Repeat("b", 64), Origin: "vault", Raw: []byte(childRaw),
			ParentSessionID: "ses_root",
		},
	}
	var output bytes.Buffer
	if _, err := RenderMarkdown(&output, sessions, Options{
		ExportedAt: time.Date(2026, 7, 21, 1, 2, 3, 0, time.UTC), Version: "v0.4.0",
	}); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "session.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatalf("markdown golden mismatch\n--- got ---\n%s\n--- want ---\n%s", output.Bytes(), want)
	}
}
