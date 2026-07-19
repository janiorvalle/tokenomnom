package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestReportCommandsRenderSeededStore(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	tests := []struct {
		name      string
		args      []string
		fragments []string
	}{
		{
			name: "summary",
			args: []string{"summary", "--no-sync"},
			fragments: []string{
				"Summary\n", "Date range:  2026-01-31 to 2026-02-03", "Active days: 3", "Tokens\n",
				"201,700", "206,910", "Providers\n", "Top models\n", "gpt-large",
				"Note: 350 tokens are attributed to the unknown model.", "Note: 5 cache-write tokens are unclassified.",
			},
		},
		{
			name:      "daily last",
			args:      []string{"daily", "--last", "2", "--no-sync"},
			fragments: []string{"DATE", "CACHE READ", "2026-02-01", "200,300", "205,350", "2026-02-03", "460"},
		},
		{
			name:      "monthly",
			args:      []string{"monthly", "--no-sync"},
			fragments: []string{"MONTH", "2026-01", "1,100", "2026-02", "205,810"},
		},
		{
			name:      "models",
			args:      []string{"models", "--no-sync"},
			fragments: []string{"PROVIDER", "MODEL", "SHARE", "DATE RANGE", "gpt-large", "99.1%", "2026-02-01 to 2026-02-01", "claude-model"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output, err := executeReport(test.args, codexDir, claudeDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, fragment := range test.fragments {
				if !strings.Contains(output, fragment) {
					t.Errorf("output missing %q:\n%s", fragment, output)
				}
			}
			if test.name == "daily last" && strings.Contains(output, "2026-01-31") {
				t.Fatalf("--last did not limit active days:\n%s", output)
			}
		})
	}
}

func TestReportFiltersAndValidation(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	output, err := executeReport([]string{"daily", "--provider", "codex", "--since", "2026-02-01", "--until", "2026-02-01", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "2026-02-01") || strings.Contains(output, "2026-01-31") || strings.Contains(output, "2026-02-03") {
		t.Fatalf("filtered output:\n%s", output)
	}

	tests := []struct {
		args    []string
		message string
	}{
		{[]string{"daily", "--last", "0", "--no-sync"}, "--last must be greater than zero"},
		{[]string{"daily", "--last", "2", "--since", "2026-02-01", "--no-sync"}, "--last cannot be combined"},
		{[]string{"summary", "--since", "not-a-date", "--no-sync"}, "invalid --since"},
		{[]string{"summary", "--since", "2026-02-02", "--until", "2026-02-01", "--no-sync"}, "--until must be on or after --since"},
		{[]string{"models", "--provider", "other", "--no-sync"}, "invalid --provider"},
	}
	for _, test := range tests {
		_, err := executeReport(test.args, codexDir, claudeDir)
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("execute %v error = %v, want %q", test.args, err, test.message)
		}
	}
}

func TestReportsHandleEmptyState(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "missing-state")
	codexDir := filepath.Join(root, "missing-codex")
	claudeDir := filepath.Join(root, "missing-claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)

	output, err := executeReport([]string{"summary"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, noProviderHint) {
		t.Fatalf("missing provider hint:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(stateDir, store.DatabaseName)); !os.IsNotExist(err) {
		t.Fatalf("empty report created a database: %v", err)
	}

	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	output, err = executeReport([]string{"monthly", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != noUsageMessage {
		t.Fatalf("empty report output = %q", output)
	}
}

func TestReportsSyncByDefaultAndRespectNoSync(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "missing-claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "fresh.jsonl"),
		"{\"timestamp\":\"2026-03-04T09:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":\"fresh-model\"}}\n"+
			"{\"timestamp\":\"2026-03-04T10:00:00Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":12,\"output_tokens\":3},\"last_token_usage\":{\"input_tokens\":12,\"output_tokens\":3}}}}\n")

	output, err := executeReport([]string{"daily", "--no-sync"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != noUsageMessage {
		t.Fatalf("--no-sync imported usage:\n%s", output)
	}
	output, err = executeReport([]string{"daily", "--tz", "UTC"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "2026-03-04") || !strings.Contains(output, "15") || strings.Contains(output, "Sync complete") {
		t.Fatalf("quiet freshness sync output:\n%s", output)
	}
}

func TestReportWarnsOnSyncErrorAndUsesStoredData(t *testing.T) {
	stateDir, codexDir, claudeDir := seedReportStore(t)
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(func(tx *store.Tx) error {
		return tx.SetMeta("pending_timezone", "Pacific/Honolulu")
	}); err != nil {
		t.Fatal(err)
	}
	database.Close()

	output, err := executeReport([]string{"summary", "--tz", "UTC"}, codexDir, claudeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "WARNING: sync usage: incomplete timezone migration") || !strings.Contains(output, "Summary\n") {
		t.Fatalf("sync warning report:\n%s", output)
	}
}

func seedReportStore(t *testing.T) (stateDir, codexDir, claudeDir string) {
	t.Helper()
	root := t.TempDir()
	stateDir = filepath.Join(root, "state")
	codexDir = filepath.Join(root, "missing-codex")
	claudeDir = filepath.Join(root, "missing-claude")
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	seed := []store.Usage{
		{Date: "2026-01-31", Provider: discover.ProviderCodex, Model: "gpt-small", Input: 1_000, CacheRead: 500, Output: 100},
		{Date: "2026-02-01", Provider: discover.ProviderCodex, Model: "gpt-large", Input: 200_000, CacheRead: 150_000, Output: 5_000},
		{Date: "2026-02-01", Provider: discover.ProviderClaude, Model: "unknown", Input: 300, CacheRead: 100, CacheWrite5m: 10, CacheWriteUnclassified: 5, Output: 50},
		{Date: "2026-02-03", Provider: discover.ProviderClaude, Model: "claude-model", Input: 400, CacheRead: 200, CacheWrite1h: 20, Output: 60},
	}
	if err := database.Transaction(func(tx *store.Tx) error {
		for _, usage := range seed {
			if err := tx.ApplyUsage(usage, ""); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	return stateDir, codexDir, claudeDir
}

func executeReport(args []string, codexDir, claudeDir string) (string, error) {
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	args = append(args, "--codex-dir", codexDir, "--claude-dir", claudeDir)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}
