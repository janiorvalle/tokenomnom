package store

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestSampleDeterministicSeedAndLogicalPromptUnits(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 8; index++ {
		source := sourceRef(fmt.Sprintf("/provider/sample-%d.jsonl", index), history.LocationProviderLive)
		value := prompt(fmt.Sprintf("native:p-%d", index), fmt.Sprintf("p-%d", index), fmt.Sprintf("prompt text %d", index), 1)
		value.Timestamp = &when
		extract := extraction(fmt.Sprintf("native:sample-%d", index), fmt.Sprintf("sample-%d", index), source, value)
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("hash-%d", index), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	// A second exact logical occurrence must not become a second sampling unit.
	copySource := sourceRef("/provider/sample-copy.jsonl", history.LocationProviderArchive)
	copyPrompt := prompt("native:p-0", "p-0", "prompt text 0", 1)
	copyPrompt.Timestamp = &when
	copyExtract := extraction("native:sample-0", "sample-0", copySource, copyPrompt)
	copyExtract.Session.FirstTimestamp, copyExtract.Session.LastTimestamp = &when, &when
	if _, err := database.ApplySource(copyExtract, head(copySource, "copy-hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}

	implicit, err := database.Sample(SampleQuery{Count: 8})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := database.Sample(SampleQuery{Count: 8, Seed: "tokenomnom"})
	if err != nil {
		t.Fatal(err)
	}
	if len(implicit.Items) != 8 || len(explicit.Items) != 8 {
		t.Fatalf("sample counts implicit=%d explicit=%d", len(implicit.Items), len(explicit.Items))
	}
	seen := map[string]bool{}
	for index := range implicit.Items {
		left, right := implicit.Items[index].Prompt, explicit.Items[index].Prompt
		if left == nil || right == nil || left.PromptID != right.PromptID {
			t.Fatalf("default seed diverged at %d: left=%+v right=%+v", index, left, right)
		}
		if left.Text != nil || left.Snippet == "" || seen[left.PromptID] {
			t.Fatalf("unbounded, empty, or duplicate prompt: %+v", left)
		}
		seen[left.PromptID] = true
	}
	withText, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{IncludeText: true}, Count: 1})
	if err != nil || len(withText.Items) != 1 || withText.Items[0].Prompt.Text == nil {
		t.Fatalf("include text sample=%+v err=%v", withText, err)
	}
	if got := withText.Coverage.Repository.Known + withText.Coverage.Repository.Unknown; got != 1 {
		t.Fatalf("sample coverage counted the corpus instead of returned sessions: %+v", withText.Coverage)
	}
}

func TestSampleStratifiedAllocationUnknownsAndSessionMonth(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	times := []*time.Time{
		timePointer(time.Date(2026, 1, 5, 1, 0, 0, 0, time.UTC)),
		timePointer(time.Date(2026, 1, 20, 1, 0, 0, 0, time.UTC)),
		timePointer(time.Date(2026, 2, 5, 1, 0, 0, 0, time.UTC)),
		nil,
	}
	repos := []string{"Alpha", "Alpha", "Beta", ""}
	threads := []history.ThreadKind{history.ThreadRoot, history.ThreadRoot, history.ThreadSubagent, history.ThreadUnknown}
	for index := range times {
		source := sourceRef(fmt.Sprintf("/provider/stratum-%d.jsonl", index), history.LocationProviderLive)
		value := prompt(fmt.Sprintf("native:p-%d", index), fmt.Sprintf("p-%d", index), fmt.Sprintf("stratum %d", index), 1)
		value.Timestamp = timePointer(time.Date(2026, time.December, index+1, 1, 0, 0, 0, time.UTC))
		extract := extraction(fmt.Sprintf("native:stratum-%d", index), fmt.Sprintf("stratum-%d", index), source, value)
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = times[index], times[index]
		extract.Session.RepositoryName = repos[index]
		extract.Session.ThreadKind = threads[index]
		if threads[index] == history.ThreadUnknown {
			extract.Session.ThreadConfidence = history.ConfidenceUnknown
		}
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("stratum-hash-%d", index), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}

	repoSample, err := database.Sample(SampleQuery{Count: 3, GroupBy: []string{"repo"}})
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string]int{}
	for _, item := range repoSample.Items {
		groups[item.Groups["repo"]]++
	}
	if len(repoSample.Items) != 3 || groups["alpha"] != 1 || groups["beta"] != 1 || groups["unknown"] != 1 || repoSample.Strategy != SampleStrategyStratified {
		t.Fatalf("repository strata items=%+v groups=%+v", repoSample.Items, groups)
	}
	roundRobin, err := database.Sample(SampleQuery{Count: 4, GroupBy: []string{"repo"}})
	if err != nil {
		t.Fatal(err)
	}
	groups = map[string]int{}
	for _, item := range roundRobin.Items {
		groups[item.Groups["repo"]]++
	}
	if groups["alpha"] != 2 || groups["beta"] != 1 || groups["unknown"] != 1 {
		t.Fatalf("round-robin strata=%+v", groups)
	}
	fewerGroups, err := database.Sample(SampleQuery{Count: 2, GroupBy: []string{"repo"}, Seed: "group-pivot"})
	if err != nil {
		t.Fatal(err)
	}
	fewerGroupsAgain, err := database.Sample(SampleQuery{Count: 2, GroupBy: []string{"repo"}, Seed: "group-pivot"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fewerGroups.Items) != 2 || fewerGroups.Items[0].Groups["repo"] == fewerGroups.Items[1].Groups["repo"] {
		t.Fatalf("group pivot did not select distinct groups: %+v", fewerGroups.Items)
	}
	for index := range fewerGroups.Items {
		if fewerGroups.Items[index].Prompt.PromptID != fewerGroupsAgain.Items[index].Prompt.PromptID {
			t.Fatalf("group pivot changed at %d: first=%+v second=%+v", index, fewerGroups, fewerGroupsAgain)
		}
	}

	sessionSample, err := database.Sample(SampleQuery{Unit: SampleUnitSession, Count: 4, GroupBy: []string{"month", "thread-kind"}})
	if err != nil {
		t.Fatal(err)
	}
	monthGroups := map[string]bool{}
	for _, item := range sessionSample.Items {
		monthGroups[item.Groups["month"]] = true
		if item.Session == nil || item.Text != nil {
			t.Fatalf("unexpected session sample item: %+v", item)
		}
	}
	if !monthGroups["2026-01"] || !monthGroups["2026-02"] || !monthGroups["unknown"] || monthGroups["2026-12"] {
		t.Fatalf("session month used prompt timestamp instead of first session timestamp: %+v", monthGroups)
	}
	withText, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{IncludeText: true}, Unit: SampleUnitSession, Count: 1})
	if err != nil || withText.Items[0].Text == nil {
		t.Fatalf("session text sample=%+v err=%v", withText, err)
	}
}

func TestSampleCountValidationAndIndexedKeys(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for _, count := range []int{-1, 101} {
		if _, err := database.Sample(SampleQuery{Count: count}); err == nil {
			t.Fatalf("count %d succeeded", count)
		}
	}
	for _, table := range []string{"sessions", "prompts"} {
		var publicID string
		var key []byte
		err := database.db.QueryRow(`SELECT public_id,sample_key FROM `+table+` LIMIT 1`).Scan(&publicID, &key)
		if err == nil && !bytes.Equal(key, sampleKey(publicID)) {
			t.Fatalf("%s sample key %x does not match public ID %q", table, key, publicID)
		}
	}
	for _, index := range []string{"sessions_sample_key_idx", "prompts_sample_key_idx", "sample_strata_group_key_idx", "sample_strata_member_idx"} {
		var exists bool
		if err := database.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND name=?)`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("sample index %s exists=%v err=%v", index, exists, err)
		}
	}
}

func TestSampleCoverageComparesTimestampInstants(t *testing.T) {
	earlier := "2026-07-21T15:00:00Z"
	later := "2026-07-21T12:00:00-04:00"
	coverage := sampleCoverage([]SampleItem{
		{Prompt: &PromptResult{SessionID: "first", Timestamp: &later, ThreadKind: history.ThreadRoot}},
		{Prompt: &PromptResult{SessionID: "second", Timestamp: &earlier, ThreadKind: history.ThreadRoot}},
	})
	if coverage.FirstTimestamp == nil || *coverage.FirstTimestamp != earlier || coverage.LastTimestamp == nil || *coverage.LastTimestamp != later {
		t.Fatalf("offset timestamp coverage = %+v", coverage)
	}
}

func TestSampleAppendRefreshesOnlyTouchedPromptStrata(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/append-sample.jsonl", history.LocationProviderLive)
	initial := extraction("native:append-sample", "append-sample", source, prompt("native:old", "old", "old prompt", 1))
	if _, err := database.ApplySource(initial, head(source, "old", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := database.db.QueryRow(`SELECT group_concat(rowid, ',') FROM (SELECT ss.rowid FROM sample_strata ss JOIN prompts p ON p.id=ss.unit_id WHERE ss.unit_kind='prompt' AND p.logical_key='native:old' ORDER BY ss.rowid)`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	appended := extraction("native:append-sample", "append-sample", source, prompt("native:new", "new", "new prompt", 2))
	appended.Occurrences[0].LineNumber, appended.Occurrences[0].StartOffset, appended.Occurrences[0].EndOffset = 2, 10, 19
	if _, err := database.ApplySource(appended, head(source, "new", 20, 2), ApplyAppend); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := database.db.QueryRow(`SELECT group_concat(rowid, ',') FROM (SELECT ss.rowid FROM sample_strata ss JOIN prompts p ON p.id=ss.unit_id WHERE ss.unit_kind='prompt' AND p.logical_key='native:old' ORDER BY ss.rowid)`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("append rebuilt untouched prompt strata: before=%q after=%q", before, after)
	}
}

func BenchmarkSampleSelectiveFilters(b *testing.B) {
	database, err := Open(b.TempDir() + "/" + DatabaseName)
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	for index := 0; index < 1000; index++ {
		source := sourceRef(fmt.Sprintf("/provider/benchmark-%d.jsonl", index), history.LocationProviderLive)
		if index%25 == 7 {
			source.Provider = history.ProviderClaude
		}
		when := time.Date(2026, time.Month(index%12+1), index%27+1, 12, 0, 0, 0, time.UTC)
		currentPrompt := prompt(fmt.Sprintf("native:p-%d", index), fmt.Sprintf("p-%d", index), "benchmark prompt", 1)
		currentPrompt.Timestamp = &when
		value := extraction(fmt.Sprintf("native:benchmark-%d", index), fmt.Sprintf("benchmark-%d", index), source,
			currentPrompt)
		value.Session.RepositoryName = fmt.Sprintf("repo-%02d", index%25)
		value.Session.CWD = fmt.Sprintf("/workspace/%02d", index%25)
		value.Session.Branch = fmt.Sprintf("branch-%02d", index%25)
		value.Session.FirstTimestamp, value.Session.LastTimestamp = &when, &when
		if index%25 == 7 {
			value.Session.ThreadKind = history.ThreadSubagent
		}
		if _, err := database.ApplySource(value, head(source, fmt.Sprintf("hash-%d", index), 10, 1), ApplyReplace); err != nil {
			b.Fatal(err)
		}
	}
	queries := map[string]PromptQuery{
		"repo":        {Repo: "repo-07"},
		"cwd":         {CWD: "/workspace/07"},
		"branch":      {Branch: "branch-07"},
		"provider":    {Provider: history.ProviderClaude},
		"thread-kind": {ThreadKind: "subagent"},
		"date":        {Since: timePointer(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))},
		"source":      {Source: CatalogSourceVault},
	}
	for name, query := range queries {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				if _, err := database.Sample(SampleQuery{PromptQuery: query, Count: 25, Seed: "benchmark"}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	b.Run("stratified", func(b *testing.B) {
		for range b.N {
			if _, err := database.Sample(SampleQuery{GroupBy: []string{"month", "repo"}, Count: 25, Seed: "benchmark"}); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func timePointer(value time.Time) *time.Time { return &value }
