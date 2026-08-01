package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestListSessionCostSourcesBoundsPageAndKeepsLocationsOutOfJSON(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/cost.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(
		extraction("native:cost", "cost", source, prompt("native:p", "p", "price this", 1)),
		head(source, "cost-hash", 10, 1), ApplyReplace,
	); err != nil {
		t.Fatal(err)
	}
	secondSource := sourceRef("/provider/cost-second.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(
		extraction("native:cost-second", "cost-second", secondSource, prompt("native:p2", "p2", "price this too", 1)),
		head(secondSource, "cost-hash-2", 11, 1), ApplyReplace,
	); err != nil {
		t.Fatal(err)
	}

	page, err := database.ListSessionCostSources(SessionCostQuery{Catalog: CatalogQuery{Limit: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Page.Limit != 1 || page.Sessions[0].SessionID == "" || len(page.Sessions[0].Candidates) != 1 {
		t.Fatalf("session cost page = %+v", page)
	}
	if page.Sessions[0].Candidates[0].ContentSHA256 != "cost-hash" && page.Sessions[0].Candidates[0].ContentSHA256 != "cost-hash-2" {
		t.Fatalf("candidate = %+v", page.Sessions[0].Candidates[0])
	}
	if !page.Page.HasMore || page.Page.NextCursor == "" {
		t.Fatalf("session cost page did not return a continuation: %+v", page.Page)
	}
	continued, err := database.ListSessionCostSources(SessionCostQuery{Catalog: CatalogQuery{Cursor: page.Page.NextCursor}})
	if err != nil || continued.Page.Limit != 1 || len(continued.Sessions) != 1 {
		t.Fatalf("continued session cost page = %+v, err=%v", continued, err)
	}
	encoded, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "source_path") || strings.Contains(string(encoded), "cost.jsonl") {
		t.Fatalf("session cost JSON leaked raw location: %s", encoded)
	}

	single, err := database.ListSessionCostSources(SessionCostQuery{SessionID: page.Sessions[0].SessionID})
	if err != nil || len(single.Sessions) != 1 || single.Generation == 0 {
		t.Fatalf("single session cost page = %+v, err=%v", single, err)
	}
	if _, err := database.ListSessionCostSources(SessionCostQuery{Catalog: CatalogQuery{Limit: MaxSessionCostPageSize + 1}}); err == nil || !strings.Contains(err.Error(), "between 1 and 100") {
		t.Fatalf("oversized session cost page error = %v", err)
	}
}
