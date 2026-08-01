package cli

import (
	"testing"

	historymodel "github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/tui"
)

func TestHistorySearchPageReportsMissingIndexWithoutCreatingIt(t *testing.T) {
	t.Setenv("TOKENOMNOM_STATE_DIR", t.TempDir())
	command := NewRootCommand()
	data, err := loadHistorySearchPage(command, tui.Request{Width: 100, Height: 30, HistoryQuery: "prompt"}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !data.NotIndexed {
		t.Fatalf("missing index data = %+v", data)
	}
}

func TestHistorySearchPageReportsMissingIndexForEmptyQuery(t *testing.T) {
	t.Setenv("TOKENOMNOM_STATE_DIR", t.TempDir())
	command := NewRootCommand()
	data, err := loadHistorySearchPage(command, tui.Request{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !data.NotIndexed {
		t.Fatalf("empty-query missing index data = %+v", data)
	}
}

func TestHistorySearchJSONPageKeepsBracketSnippetContract(t *testing.T) {
	page := historySearchJSONPage(historystore.SearchPage{Hits: []historystore.PromptResult{{
		Snippet: string(historymodel.SearchSnippetMatchStart) + "match" + string(historymodel.SearchSnippetMatchEnd),
	}}})
	if page.Hits[0].Snippet != "[match]" {
		t.Fatalf("JSON snippet = %q", page.Hits[0].Snippet)
	}
}

func TestHistoryExportDirectoryNameIsDeterministic(t *testing.T) {
	first := historyExportDirectoryName("ses_example")
	second := historyExportDirectoryName("ses_example")
	if first == "" || first != second || first == historyExportDirectoryName("ses_other") {
		t.Fatalf("export directory names are not stable: %q %q", first, second)
	}
}

func TestHistoryExportCoordinatorRejectsConcurrentSession(t *testing.T) {
	coordinator := historyExportCoordinator{inFlight: make(map[string]struct{})}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := coordinator.run("ses_example", func() (string, error) {
			close(started)
			<-release
			return "exported", nil
		})
		finished <- err
	}()
	<-started
	if _, err := coordinator.run("ses_example", func() (string, error) { return "unexpected", nil }); err == nil {
		t.Fatal("concurrent export was not rejected")
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("first export failed: %v", err)
	}
	if _, err := coordinator.run("ses_example", func() (string, error) { return "retry", nil }); err != nil {
		t.Fatalf("export was not released after completion: %v", err)
	}
}
