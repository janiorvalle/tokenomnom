package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

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
	if snapshot.Cards[0].Value != "$0.18" || snapshot.Cards[1].Value != "206,910" || snapshot.Cards[2].Value != "3" || snapshot.Cards[3].Value != "gpt-5.2" {
		t.Fatalf("dashboard cards = %+v", snapshot.Cards)
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
	if codex.Cards[1].Value != "206,100" || strings.Contains(codex.Views[tui.ModelsTab], "Claude") {
		t.Fatalf("provider filter did not apply: cards=%+v\n%s", codex.Cards, codex.Views[tui.ModelsTab])
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

func syncSummaryForTest() syncer.Summary {
	return syncer.Summary{}
}
