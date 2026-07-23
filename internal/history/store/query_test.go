package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestPromptKindFiltersAndCompactOccurrenceOutput(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	humanSource := sourceRef("/provider/kinds-human.jsonl", history.LocationProviderLive)
	controlSource := sourceRef("/provider/kinds-control.jsonl", history.LocationProviderLive)
	human := prompt("native:human", "human", "ordinary planning request", 1)
	control := prompt("native:control", "control", "<heartbeat>background worker status</heartbeat>", 2)
	control.PromptKind = history.PromptKindControl
	if _, err := database.ApplySource(extraction("native:kinds-human", "kinds-human", humanSource, human), head(humanSource, "kinds-human", 100, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApplySource(extraction("native:kinds-control", "kinds-control", controlSource, control), head(controlSource, "kinds-control", 100, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}

	defaultPage, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny})
	if err != nil || len(defaultPage.Prompts) != 1 || defaultPage.Prompts[0].PromptKind != history.PromptKindHuman {
		t.Fatalf("default prompt kinds err=%v page=%+v", err, defaultPage)
	}
	compact := defaultPage.Prompts[0]
	if compact.OccurrenceCount != 1 || compact.PreferredLocation == nil || len(compact.Occurrences) != 0 || len(compact.SourceHeadIDs) != 0 {
		t.Fatalf("compact provenance=%+v", compact)
	}
	compactJSON, err := json.Marshal(compact)
	if err != nil || strings.Contains(string(compactJSON), `"occurrences"`) || strings.Contains(string(compactJSON), `"source_head_ids"`) {
		t.Fatalf("compact provenance JSON=%s err=%v", compactJSON, err)
	}
	controlPage, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny, PromptKinds: []history.PromptKind{history.PromptKindControl}, AllOccurrences: true})
	if err != nil || len(controlPage.Prompts) != 1 || controlPage.Prompts[0].PromptKind != history.PromptKindControl || len(controlPage.Prompts[0].Occurrences) != 1 {
		t.Fatalf("control prompt kinds err=%v page=%+v", err, controlPage)
	}
	expandedJSON, err := json.Marshal(controlPage.Prompts[0])
	if err != nil || !strings.Contains(string(expandedJSON), `"preserved_snapshot_ids":[]`) || !strings.Contains(string(expandedJSON), `"source_head_ids":[`) || !strings.Contains(string(expandedJSON), `"occurrence_metadata_truncated":false`) || !strings.Contains(string(expandedJSON), `"provenance_ids_truncated":false`) {
		t.Fatalf("expanded provenance JSON=%s err=%v", expandedJSON, err)
	}
	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, PromptKinds: []history.PromptKind{history.PromptKindControl}}})
	if err != nil || stats.LogicalSessions != 1 || stats.LogicalPrompts != 1 {
		t.Fatalf("control statistics scope err=%v stats=%+v", err, stats)
	}
}

func TestCompactOutputBudgetForTwentyFiveItems(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index := 0; index < 25; index++ {
		source := sourceRef(fmt.Sprintf("/provider/compact-%02d.jsonl", index), history.LocationProviderLive)
		value := prompt(fmt.Sprintf("native:p-%02d", index), fmt.Sprintf("p-%02d", index), fmt.Sprintf("compact prompt %02d %s", index, strings.Repeat("x", 512)), 1)
		if _, err := database.ApplySource(extraction(fmt.Sprintf("native:s-%02d", index), fmt.Sprintf("s-%02d", index), source, value), head(source, fmt.Sprintf("hash-%02d", index), 100, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	result, err := database.Sample(SampleQuery{Count: 25})
	encoded, marshalErr := json.Marshal(result)
	if err != nil || marshalErr != nil || len(result.Items) != 25 || len(encoded) > 24<<10 {
		t.Fatalf("compact sample items=%d bytes=%d query_err=%v marshal_err=%v", len(result.Items), len(encoded), err, marshalErr)
	}
	for _, item := range result.Items {
		if item.Prompt == nil || len(item.Prompt.Snippet) != defaultSampleSnippetBytes {
			t.Fatalf("default snippet=%+v", item.Prompt)
		}
	}
}

func TestSampleCompactAndExpandedProvenance(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/sample-provenance.jsonl", history.LocationProviderLive)
	value := prompt("native:sample-provenance", "sample-provenance", "sample provenance prompt", 1)
	if _, err := database.ApplySource(extraction("native:sample-provenance", "sample-provenance", source, value), head(source, "sample-provenance", 100, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}

	compact, err := database.Sample(SampleQuery{Count: 1})
	compactJSON, marshalErr := json.Marshal(compact)
	if err != nil || marshalErr != nil || len(compact.Items) != 1 {
		t.Fatalf("compact sample err=%v marshal=%v result=%+v", err, marshalErr, compact)
	}
	for _, omitted := range []string{`"occurrences"`, `"source_head_ids"`, `"relationships"`, `"thread_evidence"`, `"source_path"`, `"line_number"`, `"provider_archive":0`, `"unavailable":false`} {
		if strings.Contains(string(compactJSON), omitted) {
			t.Fatalf("compact sample retained %s: %s", omitted, compactJSON)
		}
	}
	for _, required := range []string{`"occurrence_count":1`, `"preferred_location":`, `"availability":`} {
		if !strings.Contains(string(compactJSON), required) {
			t.Fatalf("compact sample missing %s: %s", required, compactJSON)
		}
	}

	expanded, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{AllOccurrences: true}, Count: 1})
	expandedJSON, marshalErr := json.Marshal(expanded)
	if err != nil || marshalErr != nil || !strings.Contains(string(expandedJSON), `"occurrences":[`) || !strings.Contains(string(expandedJSON), `"source_head_ids":[`) || !strings.Contains(string(expandedJSON), `"relationships":`) || !strings.Contains(string(expandedJSON), `"source_path":`) || !strings.Contains(string(expandedJSON), `"line_number":`) {
		t.Fatalf("expanded sample err=%v marshal=%v json=%s", err, marshalErr, expandedJSON)
	}
}

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
	page, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, AllOccurrences: true}, Query: "searchable logical", FTSQuery: false})
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
	if err != nil || len(page.Hits) != 1 || page.Hits[0].Availability.ExactLiveAndVaulted || page.Hits[0].PreferredRetrievalSource != "vault" || page.Hits[0].PreferredLocation == nil || page.Hits[0].PreferredLocation.Kind != "vault" {
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

func TestStatisticsTopGroupsDiscloseRemainder(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index := 0; index < 25; index++ {
		source := sourceRef(fmt.Sprintf("/provider/top-%02d.jsonl", index), history.LocationProviderLive)
		extract := extraction(fmt.Sprintf("native:top-%02d", index), fmt.Sprintf("top-%02d", index), source,
			prompt(fmt.Sprintf("native:top-p-%02d", index), fmt.Sprintf("top-p-%02d", index), "top group prompt", 1))
		extract.Session.RepositoryName = fmt.Sprintf("repo-%02d", index)
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("top-%02d", index), 100, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: []string{"repo"}, Top: 5})
	if err != nil || len(stats.Groups) != 5 || !stats.GroupsTruncated || stats.Other == nil || stats.Other.LogicalPrompts != 20 {
		t.Fatalf("top statistics err=%v stats=%+v", err, stats)
	}
}

func TestStatisticsTopRemainderCountsDistinctSessions(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	sharedSource := sourceRef("/provider/top-shared.jsonl", history.LocationProviderLive)
	monday := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	tuesday := monday.Add(24 * time.Hour)
	first := prompt("native:monday", "monday", "monday remainder", 1)
	second := prompt("native:tuesday", "tuesday", "tuesday remainder", 2)
	first.Timestamp, second.Timestamp = &monday, &tuesday
	if _, err := database.ApplySource(extraction("native:top-shared", "top-shared", sharedSource, first, second), head(sharedSource, "top-shared", 100, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 3; index++ {
		source := sourceRef(fmt.Sprintf("/provider/top-wednesday-%d.jsonl", index), history.LocationProviderLive)
		wednesday := tuesday.Add(24 * time.Hour)
		value := prompt(fmt.Sprintf("native:wednesday-%d", index), fmt.Sprintf("wednesday-%d", index), "wednesday top", 1)
		value.Timestamp = &wednesday
		if _, err := database.ApplySource(extraction(fmt.Sprintf("native:top-wednesday-%d", index), fmt.Sprintf("top-wednesday-%d", index), source, value), head(source, fmt.Sprintf("top-wednesday-%d", index), 100, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: []string{"weekday"}, Top: 1})
	if err != nil || stats.Other == nil || stats.Other.LogicalPrompts != 2 || stats.Other.LogicalSessions != 1 {
		t.Fatalf("distinct remainder sessions err=%v stats=%+v", err, stats)
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
	if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, ThreadKind: "root", Cursor: first.Page.NextCursor}, Query: "alpha beta"}); err == nil || !strings.Contains(err.Error(), "filters") {
		t.Fatalf("cursor thread-kind reuse error=%v", err)
	}
	extraSource := sourceRef("/provider/new-generation.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:new", "new", extraSource, prompt("native:p", "p", "alpha beta", 1)), head(extraSource, "new", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Cursor: first.Page.NextCursor}, Query: "alpha beta"}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale search cursor error=%v", err)
	}
}

func TestPromptCursorsUseSQLiteSortKeyForOffsetTimestamp(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index := range 3 {
		source := sourceRef(fmt.Sprintf("/provider/prompt-offset-%d.jsonl", index), history.LocationProviderLive)
		when := time.Date(2026, 7, 21, 12-index, 0, 0, 0, time.UTC)
		value := prompt("native:p", "p", "offset cursor", 1)
		value.Timestamp = &when
		extract := extraction(fmt.Sprintf("native:prompt-offset-%d", index), fmt.Sprintf("prompt-offset-%d", index), source, value)
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("hash-%d", index), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.db.Exec(`UPDATE prompts SET timestamp='2026-07-21T12:00:00.123-04:00' WHERE session_id=(SELECT id FROM sessions WHERE native_session_id='prompt-offset-0')`); err != nil {
		t.Fatal(err)
	}
	firstPrompts, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil || firstPrompts.Page.NextCursor == "" {
		t.Fatalf("first prompts page err=%v page=%+v", err, firstPrompts)
	}
	secondPrompts, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny, Cursor: firstPrompts.Page.NextCursor})
	if err != nil || len(secondPrompts.Prompts) != 1 || secondPrompts.Prompts[0].PromptID == firstPrompts.Prompts[0].PromptID {
		t.Fatalf("prompt offset continuation err=%v page=%+v", err, secondPrompts)
	}
	firstSearch, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Limit: 1}, Query: "offset cursor"})
	if err != nil || firstSearch.Page.NextCursor == "" {
		t.Fatalf("first search page err=%v page=%+v", err, firstSearch)
	}
	secondSearch, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Cursor: firstSearch.Page.NextCursor}, Query: "offset cursor"})
	if err != nil || len(secondSearch.Hits) != 1 || secondSearch.Hits[0].PromptID == firstSearch.Hits[0].PromptID {
		t.Fatalf("search offset continuation err=%v page=%+v", err, secondSearch)
	}
	since := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	filtered, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny, Since: &since})
	if err != nil || len(filtered.Prompts) != 1 || filtered.Prompts[0].PromptID != firstPrompts.Prompts[0].PromptID {
		t.Fatalf("prompt offset instant filter err=%v page=%+v", err, filtered)
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

func TestProjectDerivationFilteringGroupingCoverageAndCursorBinding(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()

	add := func(provider history.Provider, identity, path, repository, cwd string) {
		t.Helper()
		source := history.SourceReference{Provider: provider, Kind: history.LocationProviderLive, Path: path}
		extract := extraction("native:"+identity, identity, source, prompt("native:"+identity, identity, "project phrase "+identity, 1))
		extract.Provider = provider
		extract.Session.RepositoryName = repository
		extract.Session.CWD = cwd
		if _, err := database.ApplySource(extract, head(source, identity, 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	add(history.ProviderCodex, "git", "/provider/git.jsonl", "demo", "/workspace/fallback")
	add(history.ProviderClaude, "cwd", "/provider/cwd.jsonl", "", "/workspace/demo")
	add(history.ProviderClaude, "unknown", "/provider/unknown.jsonl", "", "")

	list, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Project: "demo", Limit: 1})
	if err != nil || len(list.Sessions) != 1 || !list.HasMore || list.Coverage.Project.Git != 1 || list.Coverage.Project.CWD != 1 || list.Coverage.Project.Unknown != 1 {
		t.Fatalf("project list = %+v err=%v", list, err)
	}
	if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Project: "other", Cursor: list.NextCursor}); err == nil || !strings.Contains(err.Error(), "filters") {
		t.Fatalf("project list cursor mismatch = %v", err)
	}
	unknown, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Project: "unknown"})
	if err != nil || len(unknown.Sessions) != 1 || unknown.Sessions[0].ProjectSource != history.ProjectSourceUnknown || unknown.Sessions[0].RepositoryName != nil {
		t.Fatalf("unknown project list = %+v err=%v", unknown, err)
	}

	search, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Project: "demo", Limit: 1}, Query: "project phrase"})
	if err != nil || len(search.Hits) != 1 || !search.Page.HasMore || search.Hits[0].Project != "demo" || search.Hits[0].ProjectSource == history.ProjectSourceUnknown {
		t.Fatalf("project search = %+v err=%v", search, err)
	}
	if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Project: "other", Cursor: search.Page.NextCursor}, Query: "project phrase"}); err == nil || !strings.Contains(err.Error(), "filters") {
		t.Fatalf("project search cursor mismatch = %v", err)
	}

	prompts, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny, Project: "demo"})
	if err != nil || len(prompts.Prompts) != 2 {
		t.Fatalf("project prompts = %+v err=%v", prompts, err)
	}

	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Project: "demo"}, GroupBy: []string{"project"}})
	if err != nil || stats.LogicalPrompts != 2 {
		t.Fatalf("project stats = %+v err=%v", stats, err)
	}
	sources := map[string]bool{}
	for _, group := range stats.Groups {
		if group.Values["project"] == "demo" {
			sources[group.Values["project_source"]] = true
		}
	}
	if !sources["git"] || !sources["cwd"] {
		t.Fatalf("project statistics groups = %+v", stats.Groups)
	}

	sample, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny, Project: "demo"}, Count: 2, GroupBy: []string{"project"}})
	if err != nil || len(sample.Items) != 2 || sample.Coverage.Project.Git != 1 || sample.Coverage.Project.CWD != 1 {
		t.Fatalf("project sample = %+v err=%v", sample, err)
	}
	for _, item := range sample.Items {
		if item.Groups["project"] != "demo" || (item.Groups["project_source"] != "git" && item.Groups["project_source"] != "cwd") {
			t.Fatalf("project sample item = %+v", item)
		}
	}
	if _, err := database.Sample(SampleQuery{Count: 2, GroupBy: []string{"project", "repo"}}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported project strata error = %v", err)
	}
}

func TestRareProjectsFoldIntoVisibleOtherGroups(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index, project := range []string{"one-off-a", "one-off-b"} {
		source := sourceRef(fmt.Sprintf("/provider/rare-%d.jsonl", index), history.LocationProviderLive)
		extract := extraction(fmt.Sprintf("native:rare-%d", index), fmt.Sprintf("rare-%d", index), source,
			prompt(fmt.Sprintf("native:rare-p-%d", index), fmt.Sprintf("rare-p-%d", index), "rare project prompt", 1))
		extract.Session.CWD = "/workspace/" + project
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("rare-%d", index), 100, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: []string{"project"}})
	if err != nil {
		t.Fatal(err)
	}
	var other *StatisticsGroup
	for index := range stats.Groups {
		if stats.Groups[index].Values["project"] == "other" {
			other = &stats.Groups[index]
		}
	}
	if other == nil || other.Values["project_source"] != "unknown" || other.LogicalSessions != 2 || other.LogicalPrompts != 2 || stats.LogicalPrompts != 2 {
		t.Fatalf("rare project statistics=%+v", stats)
	}

	sample, err := database.Sample(SampleQuery{Count: 2, GroupBy: []string{"project"}})
	if err != nil || len(sample.Items) != 2 {
		t.Fatalf("rare project sample=%+v err=%v", sample, err)
	}
	for _, item := range sample.Items {
		if item.Groups["project"] != "other" || item.Groups["project_source"] != "unknown" {
			t.Fatalf("rare project sample item=%+v", item)
		}
	}
}

func TestRoleQueriesCursorCoverageAndStatistics(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	if err := database.ConfigureAssistantIndexing(true); err != nil {
		t.Fatal(err)
	}
	source := sourceRef("/provider/roles.jsonl", history.LocationProviderLive)
	user := prompt("native:user", "user", "shared role phrase", 1)
	assistant := prompt("native:assistant", "assistant", "shared role phrase", 2)
	assistant.Role = history.RoleAssistant
	assistant.Classification = history.ClassificationAssistant
	extract := extraction("native:roles", "roles", source, user, assistant)
	if _, err := database.ApplySource(extract, head(source, "roles", 100, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	emptySource := sourceRef("/provider/promptless.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:promptless", "promptless", emptySource), head(emptySource, "promptless", 10, 0), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkAssistantIndexingComplete(); err != nil {
		t.Fatal(err)
	}

	defaultPage, err := database.Search(SearchQuery{Query: "shared role phrase"})
	if err != nil || len(defaultPage.Hits) != 1 || defaultPage.Hits[0].Role != history.RoleUser {
		t.Fatalf("default role page=%+v err=%v", defaultPage, err)
	}
	assistantPage, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Role: "assistant", AssistantConsent: true}, Query: "shared role phrase"})
	if err != nil || len(assistantPage.Hits) != 1 || assistantPage.Hits[0].Role != history.RoleAssistant {
		t.Fatalf("assistant role page=%+v err=%v", assistantPage, err)
	}
	anyPage, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Role: "any", AssistantConsent: true, Limit: 1}, Query: "shared role phrase"})
	if err != nil || len(anyPage.Hits) != 1 || !anyPage.Page.HasMore || anyPage.Page.NextCursor == "" || anyPage.Coverage.Roles.User.LogicalPrompts != 1 || anyPage.Coverage.Roles.Assistant.LogicalPrompts != 1 {
		t.Fatalf("any role page=%+v err=%v", anyPage, err)
	}
	if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Role: "user", AssistantConsent: true, Cursor: anyPage.Page.NextCursor}, Query: "shared role phrase"}); err == nil || !strings.Contains(err.Error(), "filters") {
		t.Fatalf("role-mismatched cursor error=%v", err)
	}
	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{AssistantConsent: true}, GroupBy: []string{"role"}})
	if err != nil || stats.LogicalPrompts != 1 || stats.RoleCounts.User.LogicalPrompts != 1 || stats.RoleCounts.Assistant.LogicalPrompts != 1 || len(stats.Groups) != 2 {
		t.Fatalf("role stats=%+v err=%v", stats, err)
	}
	claudeSource := sourceRef("/provider/claude-user-only.jsonl", history.LocationProviderLive)
	claudeSource.Provider = history.ProviderClaude
	if _, err := database.ApplySource(extraction("native:claude-user-only", "claude-user-only", claudeSource, prompt("native:claude-user", "claude-user", "provider-only phrase", 1)), head(claudeSource, "claude-user", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	missingRole, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Provider: history.ProviderClaude, Role: "assistant", AssistantConsent: true}, Query: "provider-only phrase"})
	if err != nil || len(missingRole.Hits) != 0 || len(missingRole.Warnings) == 0 {
		t.Fatalf("missing provider role coverage=%+v err=%v", missingRole, err)
	}
}

func TestRoleCoverageIgnoresUndatedPromptsForBounds(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/role-bounds.jsonl", history.LocationProviderLive)
	undated := prompt("native:undated", "undated", "coverage phrase", 1)
	undated.Timestamp = nil
	dated := prompt("native:dated", "dated", "coverage phrase", 2)
	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	dated.Timestamp = &when
	if _, err := database.ApplySource(extraction("native:role-bounds", "role-bounds", source, undated, dated), head(source, "bounds", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListPrompts(PromptQuery{})
	if err != nil || page.Coverage.Roles.User.FirstTimestamp == nil || page.Coverage.Roles.User.LastTimestamp == nil || page.Coverage.Roles.AssistantProviders == nil {
		t.Fatalf("role bounds page=%+v err=%v", page, err)
	}
}

func TestMaterialRoleCoverageDifferenceIncludesFirstBound(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	if err := database.ConfigureAssistantIndexing(true); err != nil {
		t.Fatal(err)
	}
	source := sourceRef("/provider/material-role-bounds.jsonl", history.LocationProviderLive)
	user := prompt("native:old-user", "old-user", "coverage difference phrase", 1)
	assistant := prompt("native:recent-assistant", "recent-assistant", "coverage difference phrase", 2)
	assistant.Role = history.RoleAssistant
	assistant.Classification = history.ClassificationAssistant
	oldTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	recentTime := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	user.Timestamp = &oldTime
	assistant.Timestamp = &recentTime
	if _, err := database.ApplySource(extraction("native:material-role-bounds", "material-role-bounds", source, user, assistant), head(source, "material-bounds", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if err := database.MarkAssistantIndexingComplete(); err != nil {
		t.Fatal(err)
	}
	page, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Role: "any", AssistantConsent: true}, Query: "coverage difference phrase"})
	if err != nil || len(page.Hits) != 2 || len(page.Warnings) == 0 || !strings.Contains(page.Warnings[0], "coverage differs materially") {
		t.Fatalf("material role coverage page=%+v err=%v", page, err)
	}
}

func TestAssistantQueriesRespectCompletedProviderScope(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	if err := database.ConfigureAssistantIndexing(true); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []history.Provider{history.ProviderCodex, history.ProviderClaude} {
		source := sourceRef("/provider/"+string(provider)+"-assistant.jsonl", history.LocationProviderLive)
		source.Provider = provider
		assistant := prompt("native:"+string(provider), string(provider), "scoped assistant phrase", 1)
		assistant.Role = history.RoleAssistant
		assistant.Classification = history.ClassificationAssistant
		if _, err := database.ApplySource(extraction("native:"+string(provider)+"-scope", string(provider)+"-scope", source, assistant), head(source, string(provider), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	if err := database.MarkAssistantIndexingComplete(history.ProviderCodex); err != nil {
		t.Fatal(err)
	}
	partial, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Role: "assistant", AssistantConsent: true}, Query: "scoped assistant phrase"})
	if err != nil || len(partial.Hits) != 1 || partial.Hits[0].Provider != history.ProviderCodex || len(partial.Warnings) == 0 || len(partial.Coverage.Roles.AssistantProviders) != 1 {
		t.Fatalf("partial provider scope=%+v err=%v", partial, err)
	}
	stats, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{AssistantConsent: true}, GroupBy: []string{"role"}})
	if err != nil || len(stats.Warnings) == 0 || !strings.Contains(strings.Join(stats.Warnings, " "), "indexed only for providers") {
		t.Fatalf("partial provider stats=%+v err=%v", stats, err)
	}
	var assistantStrata int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sample_strata ss JOIN prompts p ON p.id=ss.unit_id WHERE ss.unit_kind='prompt' AND p.role='assistant'`).Scan(&assistantStrata); err != nil || assistantStrata != 0 {
		t.Fatalf("assistant sample strata=%d err=%v", assistantStrata, err)
	}
	claude, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Provider: history.ProviderClaude, Role: "assistant", AssistantConsent: true}, Query: "scoped assistant phrase"})
	if err != nil || len(claude.Hits) != 0 || len(claude.Warnings) == 0 || !strings.Contains(claude.Warnings[0], "not indexed") {
		t.Fatalf("uncompleted provider scope=%+v err=%v", claude, err)
	}
}

func TestUserCursorIgnoresAssistantConsentConfig(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/user-cursor-consent.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:user-cursor", "user-cursor", source,
		prompt("native:first", "first", "cursor consent phrase", 1),
		prompt("native:second", "second", "cursor consent phrase", 2),
	), head(source, "cursor-consent", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	first, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Limit: 1}, Query: "cursor consent phrase"})
	if err != nil || first.Page.NextCursor == "" {
		t.Fatalf("first user cursor=%+v err=%v", first, err)
	}
	second, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Limit: 1, Cursor: first.Page.NextCursor, AssistantConsent: true}, Query: "cursor consent phrase"})
	if err != nil || len(second.Hits) != 1 {
		t.Fatalf("user cursor after consent change=%+v err=%v", second, err)
	}
}
