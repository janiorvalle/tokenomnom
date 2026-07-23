package store

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestBoundSampleSnippetPreservesUTF8WithinBudget(t *testing.T) {
	value := strings.Repeat("é", defaultSampleSnippetBytes)
	bounded := boundSampleSnippet(value, defaultSampleSnippetBytes)
	if len(bounded) > defaultSampleSnippetBytes || !utf8.ValidString(bounded) {
		t.Fatalf("bounded snippet bytes=%d valid=%t", len(bounded), utf8.ValidString(bounded))
	}
	custom := boundSampleSnippet(strings.Repeat("é", 100), 33)
	if len(custom) != 32 || !utf8.ValidString(custom) {
		t.Fatalf("custom bounded snippet bytes=%d valid=%t", len(custom), utf8.ValidString(custom))
	}
}

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

func TestSampleMinLengthAndOnePerSessionComposeDeterministically(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	firstSource := sourceRef("/provider/sample-filter-first.jsonl", history.LocationProviderLive)
	first := extraction("native:sample-filter-first", "sample-filter-first", firstSource,
		prompt("native:short", "short", "no", 1),
		prompt("native:first", "first", "long enough first", 2),
		prompt("native:second", "second", "long enough second", 3))
	if _, err := database.ApplySource(first, head(firstSource, "sample-filter-first", 100, 3), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	secondSource := sourceRef("/provider/sample-filter-second.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:sample-filter-second", "sample-filter-second", secondSource,
		prompt("native:third", "third", "long enough third", 1)), head(secondSource, "sample-filter-second", 100, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	query := SampleQuery{Count: 10, Seed: "filters", MinLength: 10, OnePerSession: true}
	firstRun, err := database.Sample(query)
	secondRun, secondErr := database.Sample(query)
	if err != nil || secondErr != nil || len(firstRun.Items) != 2 || len(secondRun.Items) != 2 {
		t.Fatalf("filtered samples first=%+v second=%+v err=%v/%v", firstRun, secondRun, err, secondErr)
	}
	seenSessions := map[string]bool{}
	for index := range firstRun.Items {
		prompt := firstRun.Items[index].Prompt
		if prompt == nil || len([]rune(prompt.Snippet)) < 10 || seenSessions[prompt.SessionID] || prompt.PromptID != secondRun.Items[index].Prompt.PromptID {
			t.Fatalf("sample item %d first=%+v second=%+v", index, firstRun.Items[index], secondRun.Items[index])
		}
		seenSessions[prompt.SessionID] = true
	}
}

func TestSampleSnippetLengthDefaultsValidatesAndHonorsOverride(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/snippet-length.jsonl", history.LocationProviderLive)
	value := prompt("native:snippet-length", "snippet-length", strings.Repeat("é", 300), 1)
	if _, err := database.ApplySource(extraction("native:snippet-length", "snippet-length", source, value), head(source, "snippet-length", 1000, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	defaultResult, err := database.Sample(SampleQuery{Count: 1})
	if err != nil || defaultResult.SnippetLength != defaultSampleSnippetBytes || len(defaultResult.Items[0].Prompt.Snippet) != defaultSampleSnippetBytes {
		t.Fatalf("default snippet result=%+v err=%v", defaultResult, err)
	}
	custom, err := database.Sample(SampleQuery{Count: 1, SnippetLength: 33})
	if err != nil || custom.SnippetLength != 33 || len(custom.Items[0].Prompt.Snippet) != 32 || !utf8.ValidString(custom.Items[0].Prompt.Snippet) {
		t.Fatalf("custom snippet result=%+v err=%v", custom, err)
	}
	expanded, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{AllOccurrences: true}, Count: 1, SnippetLength: 33})
	if err != nil || len(expanded.Items[0].Prompt.Snippet) != 32 {
		t.Fatalf("expanded custom snippet result=%+v err=%v", expanded, err)
	}
	session, err := database.Sample(SampleQuery{Unit: SampleUnitSession, Count: 1, SnippetLength: 33})
	if err != nil || len(session.Items[0].Session.Preview) != 32 {
		t.Fatalf("session custom snippet result=%+v err=%v", session, err)
	}
	for _, length := range []int{31, 513} {
		if _, err := database.Sample(SampleQuery{Count: 1, SnippetLength: length}); err == nil {
			t.Fatalf("snippet length %d succeeded", length)
		}
	}
	if _, err := database.Sample(SampleQuery{Count: 1, MinStratumSize: 2}); err == nil {
		t.Fatal("ungrouped minimum stratum size succeeded")
	}
	if _, err := database.Sample(SampleQuery{Count: 1, GroupBy: []string{"repo"}, Strategy: SampleStrategyRandom, MinStratumSize: 2}); err == nil {
		t.Fatal("random minimum stratum size succeeded")
	}
}

func TestSampleProjectSourceAndMinimumStratumSizeComposeDeterministically(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	type fixture struct {
		repo string
		cwd  string
		text string
	}
	fixtures := []fixture{
		{repo: "alpha", text: "alpha eligible one"},
		{repo: "alpha", text: "alpha eligible two"},
		{repo: "alpha", text: "alpha eligible three"},
		{repo: "beta", text: "beta eligible"},
		{repo: "beta", text: "no"},
		{repo: "gamma", text: "gamma eligible"},
		{cwd: "/workspace/cwd-only", text: "cwd eligible"},
		{text: "unknown eligible"},
	}
	for index, item := range fixtures {
		source := sourceRef(fmt.Sprintf("/provider/noise-%d.jsonl", index), history.LocationProviderLive)
		extract := extraction(fmt.Sprintf("native:noise-%d", index), fmt.Sprintf("noise-%d", index), source,
			prompt(fmt.Sprintf("native:noise-p-%d", index), fmt.Sprintf("noise-p-%d", index), item.text, 1))
		extract.Session.RepositoryName = item.repo
		extract.Session.CWD = item.cwd
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("noise-%d", index), 100, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	query := SampleQuery{
		Count: 10, Seed: "noise-controls", GroupBy: []string{"repo"}, MinLength: 10,
		MinStratumSize: 2, ProjectSource: string(history.ProjectSourceGit),
	}
	first, err := database.Sample(query)
	second, secondErr := database.Sample(query)
	if err != nil || secondErr != nil || len(first.Items) != 5 || len(second.Items) != 5 {
		t.Fatalf("noise samples first=%+v second=%+v err=%v/%v", first, second, err, secondErr)
	}
	groups := map[string]int{}
	for index, item := range first.Items {
		if item.Prompt.ProjectSource != history.ProjectSourceGit || item.Prompt.PromptID != second.Items[index].Prompt.PromptID {
			t.Fatalf("sample item %d first=%+v second=%+v", index, item, second.Items[index])
		}
		groups[item.Groups["repo"]]++
	}
	if groups["alpha"] != 3 || groups["other"] != 2 || first.Coverage.Project.Git != 5 || first.Coverage.Project.CWD != 0 || first.Coverage.Project.Unknown != 0 {
		t.Fatalf("folded groups=%+v coverage=%+v", groups, first.Coverage)
	}
	cwd, err := database.Sample(SampleQuery{Count: 10, ProjectSource: string(history.ProjectSourceCWD)})
	if err != nil || len(cwd.Items) != 1 || cwd.Items[0].Prompt.ProjectSource != history.ProjectSourceCWD {
		t.Fatalf("cwd-only sample=%+v err=%v", cwd, err)
	}
	any, err := database.Sample(SampleQuery{Count: 20, ProjectSource: SampleProjectSourceAny})
	if err != nil || len(any.Items) != len(fixtures) {
		t.Fatalf("any-source sample=%+v err=%v", any, err)
	}
}

func TestStatisticsProjectSourceFiltersTotalsGroupsAndCoverage(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index, repository := range []string{"alpha", "beta"} {
		source := sourceRef(fmt.Sprintf("/provider/stats-source-%d.jsonl", index), history.LocationProviderLive)
		extract := extraction(fmt.Sprintf("native:stats-source-%d", index), fmt.Sprintf("stats-source-%d", index), source,
			prompt(fmt.Sprintf("native:stats-source-p-%d", index), fmt.Sprintf("stats-source-p-%d", index), "stats source prompt", 1))
		if repository == "alpha" {
			extract.Session.RepositoryName = repository
		} else {
			extract.Session.CWD = "/workspace/" + repository
		}
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("stats-source-%d", index), 100, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := database.Statistics(StatisticsQuery{
		PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: []string{"project"},
		ProjectSource: string(history.ProjectSourceGit),
	})
	if err != nil || stats.LogicalSessions != 1 || stats.LogicalPrompts != 1 || stats.ProjectSource != string(history.ProjectSourceGit) ||
		stats.Coverage.Project.Git != 1 || stats.Coverage.Project.CWD != 0 || stats.Coverage.Project.Unknown != 0 {
		t.Fatalf("git statistics=%+v err=%v", stats, err)
	}
	for _, group := range stats.Groups {
		if group.Values["project_source"] != string(history.ProjectSourceUnknown) && group.Values["project_source"] != string(history.ProjectSourceGit) {
			t.Fatalf("unexpected project source group=%+v", group)
		}
	}
}

func TestSampleOnePerSessionFillsAcrossExtraStrata(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	sharedSource := sourceRef("/provider/one-per-shared.jsonl", history.LocationProviderLive)
	january := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	february := time.Date(2026, 2, 5, 12, 0, 0, 0, time.UTC)
	first := prompt("native:one-per-jan", "one-per-jan", "january prompt", 1)
	second := prompt("native:one-per-feb", "one-per-feb", "february prompt", 2)
	first.Timestamp, second.Timestamp = &january, &february
	if _, err := database.ApplySource(extraction("native:one-per-shared", "one-per-shared", sharedSource, first, second), head(sharedSource, "one-per-shared", 100, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	marchSource := sourceRef("/provider/one-per-march.jsonl", history.LocationProviderLive)
	march := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	third := prompt("native:one-per-mar", "one-per-mar", "march prompt", 1)
	third.Timestamp = &march
	if _, err := database.ApplySource(extraction("native:one-per-march", "one-per-march", marchSource, third), head(marchSource, "one-per-march", 100, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 20; index++ {
		result, err := database.Sample(SampleQuery{Count: 2, Seed: fmt.Sprintf("strata-%d", index), GroupBy: []string{"month"}, OnePerSession: true})
		if err != nil || len(result.Items) != 2 || result.Items[0].Prompt.SessionID == result.Items[1].Prompt.SessionID {
			t.Fatalf("seed %d one-per-session err=%v result=%+v", index, err, result)
		}
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
	cwds := []string{"/workspace/one", "/workspace/one", "/workspace/two", ""}
	threads := []history.ThreadKind{history.ThreadRoot, history.ThreadRoot, history.ThreadSubagent, history.ThreadUnknown}
	for index := range times {
		source := sourceRef(fmt.Sprintf("/provider/stratum-%d.jsonl", index), history.LocationProviderLive)
		value := prompt(fmt.Sprintf("native:p-%d", index), fmt.Sprintf("p-%d", index), fmt.Sprintf("stratum %d", index), 1)
		value.Timestamp = timePointer(time.Date(2026, time.December, index+1, 1, 0, 0, 0, time.UTC))
		extract := extraction(fmt.Sprintf("native:stratum-%d", index), fmt.Sprintf("stratum-%d", index), source, value)
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = times[index], times[index]
		extract.Session.RepositoryName = repos[index]
		extract.Session.CWD = cwds[index]
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
	cwdSample, err := database.Sample(SampleQuery{Count: 3, GroupBy: []string{"month", "cwd"}})
	if err != nil {
		t.Fatal(err)
	}
	cwdGroups := map[string]int{}
	for _, item := range cwdSample.Items {
		cwdGroups[item.Groups["cwd"]]++
	}
	if len(cwdSample.Items) != 3 || cwdGroups["/workspace/one"] != 1 || cwdGroups["/workspace/two"] != 1 || cwdGroups["unknown"] != 1 {
		t.Fatalf("cwd strata items=%+v groups=%+v", cwdSample.Items, cwdGroups)
	}
	cwdOnly, err := database.Sample(SampleQuery{Count: 3, GroupBy: []string{"cwd"}})
	if err != nil {
		t.Fatal(err)
	}
	cwdGroups = map[string]int{}
	for _, item := range cwdOnly.Items {
		cwdGroups[item.Groups["cwd"]]++
	}
	if cwdGroups["/workspace/one"] != 1 || cwdGroups["/workspace/two"] != 1 || cwdGroups["unknown"] != 1 {
		t.Fatalf("standalone cwd strata=%+v", cwdGroups)
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

func BenchmarkStatsSingleAndComposite(b *testing.B) {
	database, err := Open(b.TempDir() + "/" + DatabaseName)
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	for index := 0; index < 1000; index++ {
		source := sourceRef(fmt.Sprintf("/provider/stats-benchmark-%d.jsonl", index), history.LocationProviderLive)
		when := time.Date(2026, time.Month(index%12+1), index%27+1, index%24, 0, 0, 0, time.UTC)
		currentPrompt := prompt(fmt.Sprintf("native:stats-p-%d", index), fmt.Sprintf("stats-p-%d", index), "benchmark statistics prompt", 1)
		currentPrompt.Timestamp = &when
		value := extraction(fmt.Sprintf("native:stats-%d", index), fmt.Sprintf("stats-%d", index), source, currentPrompt)
		value.Session.RepositoryName = fmt.Sprintf("repo-%02d", index%25)
		value.Session.FirstTimestamp, value.Session.LastTimestamp = &when, &when
		if _, err := database.ApplySource(value, head(source, fmt.Sprintf("stats-hash-%d", index), 10, 1), ApplyReplace); err != nil {
			b.Fatal(err)
		}
	}
	for name, groups := range map[string][]string{"single": {"repo"}, "composite": {"repo", "weekday"}} {
		b.Run(name, func(b *testing.B) {
			for range b.N {
				if _, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: groups}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }
