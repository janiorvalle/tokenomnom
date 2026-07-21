package store

import (
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestSearchLiteralPhraseEscapesPunctuationOperatorsAndRequiresAdjacency(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	source := sourceRef("/provider/literal.jsonl", history.LocationProviderLive)
	prompts := []history.Prompt{
		prompt("native:adjacent", "adjacent", `foo-bar <system-reminder> foo OR bar say "hello" fmt.Println(foo)`, 1),
		prompt("native:separated", "separated", "foo something bar and system later reminder", 2),
	}
	for index := range prompts {
		prompts[index].Timestamp = &when
	}
	extract := extraction("native:literal", "literal", source, prompts...)
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
	if _, err := database.ApplySource(extract, head(source, "literal", 100, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"foo-bar", "<system-reminder>", "foo OR bar", `say "hello"`, "fmt.Println(foo)"} {
		page, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, Query: query})
		if err != nil || len(page.Hits) != 1 || page.Hits[0].Text != nil || page.Hits[0].Rank == nil || page.Hits[0].RankDirection != "lower_is_better" {
			t.Fatalf("literal %q err=%v page=%+v", query, err, page)
		}
	}
	page, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, Query: "foo bar"})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("nonadjacent literal matched: err=%v page=%+v", err, page)
	}
	raw, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, Query: "foo OR separated", FTSQuery: true})
	if err != nil || len(raw.Hits) != 2 {
		t.Fatalf("raw FTS boolean err=%v page=%+v", err, raw)
	}
	punctuation, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, Query: `---<<<>>>`})
	if err != nil || len(punctuation.Hits) != 0 {
		t.Fatalf("punctuation-only literal became syntax: err=%v page=%+v", err, punctuation)
	}
}

func TestSearchDeduplicatesAndBoundsOccurrenceMetadata(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index := range maxOccurrenceMetadata + 5 {
		source := sourceRef("/provider/occurrence-"+string(rune('a'+index))+".jsonl", history.LocationProviderLive)
		extract := extraction("native:many-occurrences", "many-occurrences", source, prompt("native:shared", "shared", "one searchable logical prompt", 1))
		if _, err := database.ApplySource(extract, head(source, "hash", 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	page, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, Query: "searchable logical", FTSQuery: false})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("deduplicated search err=%v page=%+v", err, page)
	}
	hit := page.Hits[0]
	if hit.OccurrenceCount != maxOccurrenceMetadata+5 || len(hit.Occurrences) != maxOccurrenceMetadata || !hit.OccurrenceMetadataTruncated || len(hit.SourceHeadIDs) != maxOccurrenceMetadata+5 || hit.ProvenanceIDsTruncated {
		t.Fatalf("bounded occurrence metadata = %+v", hit)
	}
}

func TestSearchExactLiveAndVaultedRequiresCompleteLiveBytes(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/incomplete.jsonl", history.LocationProviderLive)
	extract := extraction("native:incomplete", "incomplete", source, prompt("native:p", "p", "incomplete exact check", 1))
	incomplete := head(source, "complete-prefix", 11, 1)
	incomplete.CompleteOffset = 10
	if _, err := database.ApplySource(extract, incomplete, ApplyReplace); err != nil {
		t.Fatal(err)
	}
	vaultSource := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: source.Path, Archive: "codex/incomplete.tar.zst", RelativePath: "incomplete.jsonl", VaultVersion: 1}
	extract.Source = vaultSource
	if _, err := database.PreserveSnapshot(extract, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "complete-prefix", Size: 10}); err != nil {
		t.Fatal(err)
	}
	page, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, Query: "incomplete exact"})
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Availability.ExactLiveAndVaulted || page.Hits[0].PreferredRetrievalSource != "vault" {
		t.Fatalf("incomplete exact availability err=%v page=%+v", err, page)
	}
}

func TestFilteredStatsDiscloseUnscopedErrors(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/stat-errors.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:stat-errors", "stat-errors", source, prompt("native:p", "p", "stats", 1)), head(source, "hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO source_errors(provider,source_path,last_attempt_unix,last_error) VALUES('claude','/unknown',1,'failed')`); err != nil {
		t.Fatal(err)
	}
	filtered, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Provider: history.ProviderCodex, Source: CatalogSourceAny}})
	if err != nil || filtered.ErrorCount != 0 || filtered.UnscopedErrorsExcluded != 1 || len(filtered.Warnings) != 1 {
		t.Fatalf("filtered errors err=%v stats=%+v", err, filtered)
	}
	unfiltered, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}})
	if err != nil || unfiltered.ErrorCount != 1 || unfiltered.UnscopedErrorsExcluded != 0 {
		t.Fatalf("unfiltered errors err=%v stats=%+v", err, unfiltered)
	}
}

func TestFilteredStatsCountAssociatedVaultErrors(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: "/gone/vault-error.jsonl", Archive: "codex/error.tar.zst", RelativePath: "vault-error.jsonl", VaultVersion: 1}
	extract := extraction("native:vault-error", "vault-error", source, prompt("native:p", "p", "vault error", 1))
	if _, err := database.PreserveSnapshot(extract, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "vault-error-hash", Size: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO vault_bundle_state(archive,last_error) VALUES(?,?)`, source.Archive, "failed"); err != nil {
		t.Fatal(err)
	}
	filtered, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Provider: history.ProviderCodex, Source: CatalogSourceAny}})
	if err != nil || filtered.ErrorCount != 1 || filtered.UnscopedErrorsExcluded != 0 {
		t.Fatalf("associated vault error err=%v stats=%+v", err, filtered)
	}
}

func TestFilteredStatsApplyDateRangeToOversizedPrompts(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	oldTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	source := sourceRef("/provider/oversized-stats.jsonl", history.LocationProviderLive)
	oldPrompt := prompt("native:old", "old", "old oversized", 1)
	oldPrompt.Timestamp, oldPrompt.Oversized, oldPrompt.Searchable = &oldTime, true, false
	newPrompt := prompt("native:new", "new", "new normal", 2)
	newPrompt.Timestamp = &newTime
	extract := extraction("native:oversized-stats", "oversized-stats", source, oldPrompt, newPrompt)
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &oldTime, &newTime
	if _, err := database.ApplySource(extract, head(source, "hash", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	since := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	filtered, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Since: &since, Source: CatalogSourceAny}})
	if err != nil || filtered.LogicalPrompts != 1 || filtered.OversizedCount != 0 {
		t.Fatalf("filtered oversized err=%v stats=%+v", err, filtered)
	}
	unfiltered, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}})
	if err != nil || unfiltered.OversizedCount != 1 {
		t.Fatalf("unfiltered oversized err=%v stats=%+v", err, unfiltered)
	}
	after := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	empty, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Since: &after, Source: CatalogSourceAny}, GroupBy: []string{"provider"}})
	if err != nil || empty.LogicalSessions != 0 || empty.MutableSourceHeads != 0 || empty.PreservedSnapshots != 0 ||
		empty.ProviderLiveAvailable != 0 || empty.ProviderArchiveAvailable != 0 || empty.VaultAvailable != 0 ||
		empty.StaleCount != 0 || empty.ErrorCount != 0 || len(empty.Groups) != 0 {
		t.Fatalf("empty prompt-scoped sessions err=%v stats=%+v", err, empty)
	}
}

func TestSearchCursorBindsQueryModeFiltersAndGeneration(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for index := range 3 {
		source := sourceRef("/provider/search-"+string(rune('a'+index))+".jsonl", history.LocationProviderLive)
		when := base.Add(-time.Duration(index) * time.Hour)
		value := prompt("native:p", "p", "alpha beta", 1)
		value.Timestamp = &when
		extract := extraction("native:search-"+string(rune('a'+index)), "search", source, value)
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
		if _, err := database.ApplySource(extract, head(source, "hash", 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	first, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Limit: 1}, Query: "alpha beta"})
	if err != nil || len(first.Hits) != 1 || !first.Page.HasMore || first.Page.NextCursor == "" {
		t.Fatalf("first search page err=%v page=%+v", err, first)
	}
	second, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Cursor: first.Page.NextCursor}, Query: "alpha beta"})
	if err != nil || len(second.Hits) != 1 || second.Hits[0].PromptID == first.Hits[0].PromptID {
		t.Fatalf("second search page err=%v page=%+v", err, second)
	}
	if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Cursor: first.Page.NextCursor}, Query: "alpha", FTSQuery: true}); err == nil || !strings.Contains(err.Error(), "query mode") {
		t.Fatalf("cursor query reuse error=%v", err)
	}
	extraSource := sourceRef("/provider/new-generation.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:new", "new", extraSource, prompt("native:p", "p", "alpha beta", 1)), head(extraSource, "new", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Cursor: first.Page.NextCursor}, Query: "alpha beta"}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale search cursor error=%v", err)
	}
}

func TestCoverageWarnsForRangesEntirelyOutsideIndex(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	source := sourceRef("/provider/coverage-gap.jsonl", history.LocationProviderLive)
	value := prompt("native:p", "p", "coverage gap", 1)
	value.Timestamp = &when
	extract := extraction("native:coverage-gap", "coverage-gap", source, value)
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
	if _, err := database.ApplySource(extract, head(source, "hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	after := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	afterPage, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Since: &after}, Query: "coverage"})
	if err != nil || len(afterPage.Hits) != 0 || len(afterPage.Warnings) != 1 || !strings.Contains(afterPage.Warnings[0], "begins after") {
		t.Fatalf("after coverage err=%v page=%+v", err, afterPage)
	}
	before := time.Date(2020, 1, 1, 23, 59, 59, 0, time.UTC)
	beforePage, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Until: &before}, Query: "coverage"})
	if err != nil || len(beforePage.Hits) != 0 || len(beforePage.Warnings) != 1 || !strings.Contains(beforePage.Warnings[0], "ends before") {
		t.Fatalf("before coverage err=%v page=%+v", err, beforePage)
	}
}

func TestPromptsShowRawCandidatesStatsAndCoverage(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 20, 12, 30, 0, 0, time.UTC)
	source := sourceRef("/provider/show.jsonl", history.LocationProviderLive)
	value := prompt("native:show", "show", "complete clean prompt", 1)
	value.Timestamp = &when
	extract := extraction("native:show", "show", source, value)
	extract.Session.CWD = "/repo"
	extract.Session.RepositoryName = "tokenomnom"
	extract.Session.Branch = "main"
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
	result, err := database.ApplySource(extract, head(source, "exact-hash", 42, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}

	prompts, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny})
	if err != nil || len(prompts.Prompts) != 1 || prompts.Prompts[0].Text != nil || len(prompts.Prompts[0].Occurrences) != 0 {
		t.Fatalf("default prompts err=%v page=%+v", err, prompts)
	}
	expanded, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny, IncludeText: true, AllOccurrences: true})
	if err != nil || expanded.Prompts[0].Text == nil || *expanded.Prompts[0].Text != value.CleanText || len(expanded.Prompts[0].Occurrences) != 1 {
		t.Fatalf("expanded prompts err=%v page=%+v", err, expanded)
	}
	shown, err := database.GetPrompt(result.PromptIDs[value.LogicalKey])
	if err != nil || shown.Text == nil || *shown.Text != value.CleanText {
		t.Fatalf("show prompt err=%v value=%+v", err, shown)
	}
	session, err := database.GetSession(result.SessionID)
	if err != nil || session.SessionID != result.SessionID || session.LogicalPromptCount != 1 {
		t.Fatalf("show session err=%v value=%+v", err, session)
	}
	sessionPrompts, err := database.SessionPrompts(result.SessionID, PromptQuery{Limit: 1})
	if err != nil || len(sessionPrompts.Prompts) != 1 || sessionPrompts.Prompts[0].Text == nil {
		t.Fatalf("session prompts err=%v value=%+v", err, sessionPrompts)
	}
	candidates, err := database.RawCandidates(result.SessionID, "")
	if err != nil || len(candidates) != 1 || candidates[0].SourceHeadID == nil || *candidates[0].SourceHeadID != result.SourceID || candidates[0].ContentSHA256 != "exact-hash" {
		t.Fatalf("raw candidates err=%v values=%+v", err, candidates)
	}

	since := when.Add(-24 * time.Hour)
	until := when.Add(24 * time.Hour)
	statistics, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Since: &since, Until: &until}, GroupBy: []string{"repo"}})
	if err != nil || statistics.LogicalSessions != 1 || statistics.LogicalPrompts != 1 || statistics.PromptOccurrences != 1 || statistics.PromptLengthTotalBytes != int64(len(value.CleanText)) || statistics.PromptLengthMedianBytes != float64(len(value.CleanText)) || len(statistics.Warnings) != 2 {
		t.Fatalf("statistics err=%v value=%+v", err, statistics)
	}
	foundUnknown := false
	for _, group := range statistics.Groups {
		foundUnknown = foundUnknown || group.Values["repo"] == "unknown"
	}
	if !foundUnknown || statistics.Coverage.Repository.Known != 1 {
		t.Fatalf("statistics coverage/groups=%+v", statistics)
	}
	multi, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: []string{"repo", "cwd"}})
	if err != nil {
		t.Fatal(err)
	}
	foundUnknownRepo, foundUnknownCWD := false, false
	for _, group := range multi.Groups {
		foundUnknownRepo = foundUnknownRepo || group.Values["repo"] == "unknown"
		foundUnknownCWD = foundUnknownCWD || group.Values["cwd"] == "unknown"
	}
	if !foundUnknownRepo || !foundUnknownCWD {
		t.Fatalf("multi-dimensional unknown groups=%+v", multi.Groups)
	}
}
