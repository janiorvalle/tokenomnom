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
