package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	appconfig "github.com/janiorvalle/tokenomnom/internal/config"
	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
	"github.com/janiorvalle/tokenomnom/internal/theme"
	"github.com/janiorvalle/tokenomnom/internal/tui"
)

func TestDashboardSnapshotRendersAllViewsAndFilteredCards(t *testing.T) {
	stateDir, _, _ := seedReportStore(t)
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	render := styledRenderContext(120)
	snapshot, err := dashboardSnapshot(database, tui.Request{Range: tui.RangeAll, Width: 120, Height: 35}, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	metrics := snapshot.Summary.Metrics
	if metrics[0].Value != "$0.18" || metrics[1].Value != "206,910" || metrics[2].Value != "3" || metrics[3].Value != "$0.06" || metrics[4].Value != "$0.18" {
		t.Fatalf("dashboard summary = %+v", metrics)
	}
	for index, metric := range metrics {
		if metric.Label != "" {
			t.Errorf("dashboard summary metric %d supplies label %q; TUI owns summary labels", index, metric.Label)
		}
	}
	for index, fragments := range [][]string{{"cost/day", "DATE"}, {"cost/month", "MONTH"}, {"PROVIDER", "MODEL"}, {"Less", "active days"}} {
		for _, fragment := range fragments {
			if !strings.Contains(snapshot.Views[index], fragment) {
				t.Errorf("view %d missing %q:\n%s", index, fragment, snapshot.Views[index])
			}
		}
	}

	codex, err := dashboardSnapshot(database, tui.Request{Provider: tui.CodexProvider, Range: tui.RangeAll, Width: 120, Height: 35}, render, time.UTC, syncSummaryForTest())
	if err != nil {
		t.Fatal(err)
	}
	if codex.Summary.Metrics[1].Value != "206,100" || strings.Contains(codex.Views[tui.ModelsTab], "Claude") {
		t.Fatalf("provider filter did not apply: summary=%+v\n%s", codex.Summary.Metrics, codex.Views[tui.ModelsTab])
	}
}

func TestDashboardStatusBarHintsMissingOptionalStores(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	cmd := &cobra.Command{}
	cmd.SetContext(appconfig.WithContext(context.Background(), appconfig.Loaded{Config: appconfig.Defaults()}))
	status := dashboardStatusBar(cmd, database, stateDir, root, []discover.Root{
		{Provider: discover.ProviderCodex, Path: filepath.Join(root, "codex")},
		{Provider: discover.ProviderClaude, Path: filepath.Join(root, "claude")},
	})
	if status.History.Exists || status.History.Hint != "not indexed" || status.Sessions != 0 {
		t.Fatalf("missing history status = %+v", status.History)
	}
	if status.Vault.Exists || status.Vault.Hint != "not initialized" {
		t.Fatalf("missing vault status = %+v", status.Vault)
	}
}

func TestDashboardStatusBarReportsFreshIndexedHistory(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	codexDir := filepath.Join(root, "codex")
	claudeDir := filepath.Join(root, "claude")
	t.Setenv("TOKENOMNOM_STATE_DIR", stateDir)
	t.Setenv("TOKENOMNOM_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("TOKENOMNOM_CONFIG_DIR", filepath.Join(root, "config"))
	writeTextFixture(t, filepath.Join(codexDir, "sessions", "status.jsonl"), historyCodexFixture("status", "status prompt"))
	if _, err := executeReport([]string{"history", "index", "--source", "provider"}, codexDir, claudeDir); err != nil {
		t.Fatal(err)
	}

	database, err := store.Open(filepath.Join(stateDir, store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	cmd := &cobra.Command{}
	cmd.SetContext(appconfig.WithContext(context.Background(), appconfig.Loaded{Config: appconfig.Defaults()}))
	status := dashboardStatusBar(cmd, database, stateDir, root, []discover.Root{
		{Provider: discover.ProviderCodex, Path: codexDir, Exists: true},
		{Provider: discover.ProviderClaude, Path: claudeDir},
	})
	if !status.History.Exists || !status.History.Fresh || status.History.Hint != "" || status.Sessions != 1 {
		t.Fatalf("fresh history status = %+v sessions=%d", status.History, status.Sessions)
	}
}

func TestBareStyledInvocationLaunchesDashboard(t *testing.T) {
	original := runDashboardProgram
	defer func() { runDashboardProgram = original }()
	called := false
	runDashboardProgram = func(_ *cobra.Command, _ tui.Model) error {
		called = true
		return nil
	}
	var output bytes.Buffer
	terminal := true
	dark := true
	cmd := newRootCommand(theme.ResolveOptions{
		ForceTerminal: &terminal, Width: 100, ForceColor: true, Dark: &dark,
		LookupEnv: func(string) (string, bool) { return "", false },
	})
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !called || strings.Contains(output.String(), "Your agents nom tokens") {
		t.Fatalf("styled bare launch = called %v, output %q", called, output.String())
	}
}

func TestBarePlainInvocationRemainsHelp(t *testing.T) {
	plain, err := executeCLI()
	if err != nil {
		t.Fatal(err)
	}
	noColor, err := executeCLI("--no-color")
	if err != nil {
		t.Fatal(err)
	}
	if plain != noColor || !strings.Contains(plain, "Your agents nom tokens") {
		t.Fatalf("plain bare output changed:\nplain:\n%s\nno-color:\n%s", plain, noColor)
	}
}

func TestForcedColorDoesNotLaunchDashboardWithoutTerminal(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("TOKENOMNOM_CONFIG_DIR", configDir)
	withoutEnv(t, "NO_COLOR")
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[reports]\ncolor = \"always\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := runDashboardProgram
	defer func() { runDashboardProgram = original }()
	called := false
	runDashboardProgram = func(_ *cobra.Command, _ tui.Model) error {
		called = true
		return nil
	}
	var output bytes.Buffer
	cmd := NewRootCommand()
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if called || !strings.Contains(output.String(), "Your agents nom tokens") {
		t.Fatalf("redirected forced-color launch = called %t, output %q", called, output.String())
	}
}

func syncSummaryForTest() syncer.Summary {
	return syncer.Summary{}
}
