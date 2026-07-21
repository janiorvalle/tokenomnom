package indexer

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
	_ "modernc.org/sqlite"
)

func TestInitialUnchangedAppendGeneration(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("session.jsonl")
	writeFile(t, path, codexMeta("session-1")+codexPrompt("p1", "first alpha"))

	first := env.index(t, false)
	if first.NewSources != 1 || first.IndexedPrompts != 1 {
		t.Fatalf("initial summary = %+v", first)
	}
	firstHealth := env.health(t)
	firstSourceID := env.checkpoint(t, history.ProviderCodex, path).SourceID

	second := env.index(t, false)
	if second.SkippedSources != 1 || second.IndexedSources != 0 {
		t.Fatalf("unchanged summary = %+v", second)
	}
	if got := env.health(t).IndexGeneration; got != firstHealth.IndexGeneration {
		t.Fatalf("unchanged generation = %d, want %d", got, firstHealth.IndexGeneration)
	}

	appendFile(t, path, codexPrompt("p2", "second beta"))
	third := env.index(t, false)
	if third.AppendedSources != 1 || third.IndexedPrompts != 1 {
		t.Fatalf("append summary = %+v", third)
	}
	health := env.health(t)
	if health.Prompts != 2 || health.Occurrences != 2 || health.IndexGeneration != firstHealth.IndexGeneration+1 {
		t.Fatalf("append health = %+v", health)
	}
	if got := env.checkpoint(t, history.ProviderCodex, path).SourceID; got != firstSourceID {
		t.Fatalf("append source ID = %q, want %q", got, firstSourceID)
	}
}

func TestInitialClaudeAppend(t *testing.T) {
	env := newEnvironment(t)
	path := filepath.Join(env.claudeRoot, "projects", "repo", "session.jsonl")
	writeFile(t, path, claudePrompt("claude-session", "m1", "first claude"))
	first := env.index(t, false)
	if first.NewSources != 1 || first.IndexedPrompts != 1 {
		t.Fatalf("initial Claude summary = %+v", first)
	}
	checkpoint := env.checkpoint(t, history.ProviderClaude, path)
	if checkpoint.SourceKind != "claude_project" {
		t.Fatalf("Claude source kind = %q", checkpoint.SourceKind)
	}
	appendFile(t, path, claudePrompt("claude-session", "m2", "second claude"))
	second := env.index(t, false)
	if second.AppendedSources != 1 || second.IndexedPrompts != 1 || env.health(t).Prompts != 2 {
		t.Fatalf("Claude append summary=%+v health=%+v", second, env.health(t))
	}
}

func TestStreamingExtractionStopsAfterSessionConflict(t *testing.T) {
	tests := []struct {
		name     string
		path     func(*environment) string
		contents string
	}{
		{
			name: "codex",
			path: func(env *environment) string { return env.codexPath("conflict.jsonl") },
			contents: codexMeta("session-one") + codexPrompt("p1", "accepted") +
				codexMeta("session-two") + codexPrompt("p2", "excluded"),
		},
		{
			name: "claude",
			path: func(env *environment) string {
				return filepath.Join(env.claudeRoot, "projects", "repo", "conflict.jsonl")
			},
			contents: claudePrompt("session-one", "p1", "accepted") +
				claudePrompt("session-two", "p2", "excluded") + claudePrompt("session-one", "p3", "also excluded"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := newEnvironment(t)
			writeFile(t, test.path(env), test.contents)
			summary := env.index(t, false)
			if summary.IndexedPrompts != 1 || env.health(t).Prompts != 1 {
				t.Fatalf("conflicting stream summary=%+v health=%+v", summary, env.health(t))
			}
		})
	}
}

func TestPartialTrailingLineCompletion(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("partial.jsonl")
	partial := strings.TrimSuffix(codexPrompt("p1", "wait for newline"), "\n")
	writeFile(t, path, codexMeta("partial-session")+partial)

	first := env.index(t, false)
	if first.IndexedPrompts != 0 || env.health(t).Prompts != 0 {
		t.Fatalf("partial line indexed: summary=%+v health=%+v", first, env.health(t))
	}
	generation := env.health(t).IndexGeneration
	appendFile(t, path, "more-partial")
	second := env.index(t, false)
	if second.AppendedSources != 1 || second.IndexedPrompts != 0 || env.health(t).IndexGeneration != generation {
		t.Fatalf("growing partial line changed content: %+v health=%+v", second, env.health(t))
	}
	// Replace the still-unindexed partial suffix with one complete valid record.
	writeFile(t, path, codexMeta("partial-session")+codexPrompt("p1", "wait for newline"))
	third := env.index(t, false)
	if third.AppendedSources+third.RewrittenSources != 1 || third.IndexedPrompts != 1 || env.health(t).Prompts != 1 {
		t.Fatalf("completed partial summary = %+v health=%+v", third, env.health(t))
	}
}

func TestAppendDuringIndexingRemainsPendingForNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.jsonl")
	initial := codexMeta("concurrent-session") + codexPrompt("p1", "first")
	appended := codexPrompt("p2", "second")
	writeFile(t, path, initial)
	parsed, err := readRecordsWithHook(path, jsonl.Position{}, historystore.Checkpoint{}, fileNew, nil, func() {
		appendFile(t, path, appended)
	})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.size != int64(len(initial)) || parsed.position.ByteOffset != int64(len(initial)) || parsed.recordCount != 2 || parsed.modTimeUnixNano == 0 {
		t.Fatalf("concurrent parsed source = %+v", parsed)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := historystore.Checkpoint{
		Provider: history.ProviderCodex, Path: path, Kind: history.LocationProviderLive,
		Size: parsed.size, CompleteOffset: parsed.position.ByteOffset, LineCount: parsed.position.LineNumber,
		ContentSHA256: parsed.contentHash, ContentHashState: parsed.hashState,
		PrefixFingerprint: parsed.prefixFingerprint, TailFingerprint: parsed.tailFingerprint,
		ExtractorVersion: history.ExtractorVersion,
	}
	kind, err := classify(discover.SourceFile{Provider: discover.ProviderCodex, Kind: discover.SourceCodexLive, Path: path, Size: stat.Size(), ModTime: stat.ModTime()}, checkpoint, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if kind != fileAppend {
		t.Fatalf("concurrent append classified as %v", kind)
	}
	suffix, err := readRecords(path, jsonl.Position{ByteOffset: checkpoint.CompleteOffset, LineNumber: checkpoint.LineCount}, checkpoint, fileAppend, nil)
	if err != nil || suffix.recordCount != 1 || suffix.position.ByteOffset != int64(len(initial)+len(appended)) {
		t.Fatalf("pending append was not resumed: parsed=%+v err=%v", suffix, err)
	}
}

func TestAppendReadRejectsPrefixChangedAfterClassification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append-race.jsonl")
	initial := codexMeta("append-race") + codexPrompt("p1", strings.Repeat("x", 10_000))
	writeFile(t, path, initial)
	parsed, err := readRecords(path, jsonl.Position{}, historystore.Checkpoint{}, fileNew, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := historystore.Checkpoint{
		ContentSHA256: parsed.contentHash, ContentHashState: parsed.hashState,
		CompleteOffset: parsed.position.ByteOffset, LineCount: parsed.position.LineNumber,
	}
	rewritten := []byte(initial)
	rewritten[5_000] = 'y'
	writeFile(t, path, string(rewritten)+codexPrompt("p2", "appended"))
	_, err = readRecords(path, jsonl.Position{ByteOffset: checkpoint.CompleteOffset, LineNumber: checkpoint.LineCount}, checkpoint, fileAppend, nil)
	if !errors.Is(err, errSourceChanged) {
		t.Fatalf("append read error = %v, want errSourceChanged", err)
	}
}

func TestGrowingRewriteRejectsMixedSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "growing-rewrite.jsonl")
	initial := strings.Repeat("x", 100_000) + "\n"
	writeFile(t, path, initial)
	_, err := readRecordsWithHook(path, jsonl.Position{}, historystore.Checkpoint{}, fileNew, nil, func() {
		file, openErr := os.OpenFile(path, os.O_WRONLY, 0o644)
		if openErr != nil {
			t.Fatal(openErr)
		}
		if _, writeErr := file.WriteAt([]byte("y"), 5_000); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if _, seekErr := file.Seek(0, io.SeekEnd); seekErr != nil {
			_ = file.Close()
			t.Fatal(seekErr)
		}
		if _, writeErr := file.WriteString("appended\n"); writeErr != nil {
			_ = file.Close()
			t.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	})
	if !errors.Is(err, errSourceChanged) {
		t.Fatalf("growing rewrite error = %v, want errSourceChanged", err)
	}
}

func TestRewriteThenAppendRebuildsSource(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("rewrite-append.jsonl")
	initial := codexMeta("rewrite-append") + codexPrompt("p1", strings.Repeat("x", 100_000))
	writeFile(t, path, initial)
	env.index(t, false)

	rewritten := []byte(initial)
	rewritten[5_000] = 'y'
	writeFile(t, path, string(rewritten)+codexPrompt("p2", "appended"))
	summary := env.index(t, false)
	if summary.RewrittenSources != 1 || summary.AppendedSources != 0 || env.health(t).Prompts != 2 {
		t.Fatalf("rewrite then append summary=%+v health=%+v", summary, env.health(t))
	}
}

func TestSameSizeRewriteWithPreservedMtimeRebuildsSource(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("preserved-mtime.jsonl")
	initial := codexMeta("preserved-mtime") + codexPrompt("p1", strings.Repeat("x", 100_000))
	writeFile(t, path, initial)
	env.index(t, false)
	checkpoint := env.checkpoint(t, history.ProviderCodex, path)

	rewritten := []byte(initial)
	rewritten[5_000] = 'y'
	writeFile(t, path, string(rewritten))
	checkpointTime := time.Unix(0, checkpoint.ModTimeUnixNano)
	if err := os.Chtimes(path, checkpointTime, checkpointTime); err != nil {
		t.Fatal(err)
	}
	summary := env.index(t, false)
	if summary.RewrittenSources != 1 || summary.SkippedSources != 0 {
		t.Fatalf("preserved-mtime rewrite summary=%+v", summary)
	}
}

func TestExtractionDiagnosticsBecomeBoundedWarnings(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("diagnostic.jsonl")
	writeFile(t, path, codexMeta("diagnostic")+"not-json\n"+codexPrompt("p1", "accepted"))
	summary := env.index(t, false)
	if summary.IndexedPrompts != 1 || len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0].Error, "malformed JSON") {
		t.Fatalf("diagnostic summary=%+v", summary)
	}
}

func TestRewriteSameSizeAndShrink(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("rewrite.jsonl")
	firstContents := codexMeta("rewrite-session") + codexPrompt("p1", "alpha-one") + codexPrompt("p2", "keep-two")
	writeFile(t, path, firstContents)
	env.index(t, false)

	sameSize := codexMeta("rewrite-session") + codexPrompt("p3", "gamma-one") + codexPrompt("p2", "keep-two")
	if len(firstContents) != len(sameSize) {
		t.Fatal("same-size rewrite fixture is not the same size")
	}
	writeFile(t, path, sameSize)
	rewritten := env.index(t, false)
	if rewritten.RewrittenSources != 1 || env.health(t).Prompts != 2 {
		t.Fatalf("same-size rewrite = %+v health=%+v", rewritten, env.health(t))
	}

	writeFile(t, path, codexMeta("rewrite-session")+codexPrompt("p4", "short"))
	shrunk := env.index(t, false)
	if shrunk.RewrittenSources != 1 || env.health(t).Prompts != 1 || env.health(t).Occurrences != 1 {
		t.Fatalf("shrink = %+v health=%+v", shrunk, env.health(t))
	}
}

func TestRewriteSameSizeOutsideTailFingerprint(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("middle-rewrite.jsonl")
	firstText := strings.Repeat("a", 5000) + strings.Repeat("z", 5000)
	secondText := strings.Repeat("b", 5000) + strings.Repeat("z", 5000)
	first := codexMeta("middle-session") + codexPrompt("p1", firstText)
	second := codexMeta("middle-session") + codexPrompt("p2", secondText)
	if len(first) != len(second) {
		t.Fatal("middle rewrite fixture sizes differ")
	}
	writeFile(t, path, first)
	env.index(t, false)
	writeFile(t, path, second)
	summary := env.index(t, false)
	if summary.RewrittenSources != 1 || env.health(t).Prompts != 1 {
		t.Fatalf("middle rewrite summary=%+v health=%+v", summary, env.health(t))
	}
}

func TestMissingReappearingSource(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("missing.jsonl")
	contents := codexMeta("missing-session") + codexPrompt("p1", "temporary prompt")
	writeFile(t, path, contents)
	env.index(t, false)
	sourceID := env.checkpoint(t, history.ProviderCodex, path).SourceID

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing := env.index(t, false)
	if missing.MissingSources != 1 {
		t.Fatalf("missing summary = %+v", missing)
	}
	health := env.health(t)
	if health.MissingSources != 1 || health.Prompts != 0 || health.Occurrences != 0 || health.SourceHeads != 1 {
		t.Fatalf("missing health = %+v", health)
	}
	missingGeneration := health.IndexGeneration

	writeFile(t, path, contents)
	reappeared := env.index(t, false)
	if reappeared.RewrittenSources != 1 || env.health(t).Prompts != 1 || env.health(t).MissingSources != 0 {
		t.Fatalf("reappearing summary = %+v health=%+v", reappeared, env.health(t))
	}
	if env.health(t).IndexGeneration != missingGeneration+1 {
		t.Fatalf("reappearance did not advance generation: %+v", env.health(t))
	}
	if got := env.checkpoint(t, history.ProviderCodex, path).SourceID; got != sourceID {
		t.Fatalf("reappearing source ID = %q, want %q", got, sourceID)
	}
}

func TestProviderSourceKindAndExactDuplicatePaths(t *testing.T) {
	env := newEnvironment(t)
	contents := codexMeta("duplicate-session") + codexPrompt("p1", "shared prompt")
	writeFile(t, env.codexPath("live.jsonl"), contents)
	writeFile(t, filepath.Join(env.codexRoot, "archived_sessions", "archive.jsonl"), contents)

	summary := env.index(t, false)
	health := env.health(t)
	if summary.NewSources != 2 || health.SourceHeads != 2 || health.LiveSources != 1 || health.ProviderArchiveSources != 1 {
		t.Fatalf("provider kinds summary=%+v health=%+v", summary, health)
	}
	if health.Sessions != 1 || health.Prompts != 1 || health.Occurrences != 2 {
		t.Fatalf("duplicate paths were not occurrence-deduplicated: %+v", health)
	}
}

func TestProviderArchiveMovePreservesSourceIdentity(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("moved.jsonl")
	archivePath := filepath.Join(env.codexRoot, "archived_sessions", "moved.jsonl")
	writeFile(t, livePath, codexMeta("moved-session")+codexPrompt("p1", "moved prompt"))
	env.index(t, false)
	before := env.checkpoint(t, history.ProviderCodex, livePath)
	generation := env.health(t).IndexGeneration
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(livePath, archivePath); err != nil {
		t.Fatal(err)
	}
	env.index(t, false)
	after := env.checkpoint(t, history.ProviderCodex, archivePath)
	if after.SourceID != before.SourceID || after.SourceKind != "codex_archive" || after.Missing {
		t.Fatalf("archive move changed identity: before=%+v after=%+v", before, after)
	}
	health := env.health(t)
	if health.LiveSources != 0 || health.ProviderArchiveSources != 1 || health.IndexGeneration != generation+1 {
		t.Fatalf("archive move health = %+v", health)
	}
}

func TestFallbackIdentitySurvivesAppendAndFullReindex(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("fallback.jsonl")
	writeFile(t, path, codexPrompt("p1", "fallback one"))
	env.index(t, false)
	first := env.checkpoint(t, history.ProviderCodex, path)
	appendFile(t, path, codexPrompt("p2", "fallback two"))
	env.index(t, false)
	env.index(t, true)
	after := env.checkpoint(t, history.ProviderCodex, path)
	if first.SourceID != after.SourceID || after.Session.IdentityKey != first.Session.IdentityKey {
		t.Fatalf("fallback identity changed: before=%+v after=%+v", first, after)
	}
}

func TestExtractorVersionRebuild(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("stale.jsonl")
	writeFile(t, path, codexMeta("stale-session")+codexPrompt("p1", "stale prompt"))
	env.index(t, false)
	beforeSession, err := env.database.ListCatalog(historystore.CatalogQuery{Source: historystore.CatalogSourceAny})
	if err != nil {
		t.Fatal(err)
	}
	beforePrompts, err := env.database.ListPrompts(historystore.PromptQuery{Source: historystore.CatalogSourceAny})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", env.database.Path())
	if err != nil {
		t.Fatal(err)
	}
	result, err := raw.Exec(`UPDATE source_heads SET extractor_version=0`)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		raw.Close()
		t.Fatalf("updated %d stale sources", changed)
	}
	raw.Close()
	if env.health(t).StaleSources != 1 {
		t.Fatal("fixture source was not marked stale")
	}
	summary := env.index(t, false)
	if summary.RewrittenSources != 1 || env.health(t).StaleSources != 0 || env.health(t).Prompts != 1 {
		t.Fatalf("extractor rebuild summary=%+v health=%+v", summary, env.health(t))
	}
	afterSession, err := env.database.ListCatalog(historystore.CatalogQuery{Source: historystore.CatalogSourceAny})
	if err != nil {
		t.Fatal(err)
	}
	afterPrompts, err := env.database.ListPrompts(historystore.PromptQuery{Source: historystore.CatalogSourceAny})
	if err != nil || beforeSession.Sessions[0].SessionID != afterSession.Sessions[0].SessionID || beforePrompts.Prompts[0].PromptID != afterPrompts.Prompts[0].PromptID {
		t.Fatalf("extractor rebuild changed stable IDs: before=%+v/%+v after=%+v/%+v err=%v", beforeSession, beforePrompts, afterSession, afterPrompts, err)
	}
}

func TestAssistantConsentRebuildsAndDisablePrunes(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("assistant-consent.jsonl")
	writeFile(t, path, codexMeta("assistant-consent")+codexPrompt("p1", "user baseline")+codexAssistant("a1", "assistant proposal"))
	env.index(t, false)
	before, err := env.database.ListPrompts(historystore.PromptQuery{Source: historystore.CatalogSourceAny})
	if err != nil || len(before.Prompts) != 1 || env.health(t).AssistantPrompts != 0 {
		t.Fatalf("default corpus page=%+v health=%+v err=%v", before, env.health(t), err)
	}
	userID := before.Prompts[0].PromptID

	enabled, err := Index(Options{Store: env.database, Roots: env.roots, IndexAssistant: true})
	if err != nil || enabled.RewrittenSources != 1 {
		t.Fatalf("assistant rebuild summary=%+v err=%v", enabled, err)
	}
	health := env.health(t)
	if health.AssistantIndexed || health.UserPrompts != 1 || health.AssistantPrompts != 1 || health.StaleSources != 0 {
		t.Fatalf("partial assistant health = %+v", health)
	}
	partial, err := env.database.Search(historystore.SearchQuery{PromptQuery: historystore.PromptQuery{Role: "assistant", AssistantConsent: true}, Query: "assistant proposal"})
	if err != nil || len(partial.Hits) != 0 || len(partial.Warnings) == 0 {
		t.Fatalf("partial assistant search=%+v err=%v", partial, err)
	}
	completed, err := Index(Options{Store: env.database, Roots: env.roots, IndexAssistant: true, CompleteAssistantScope: true})
	if err != nil || completed.SkippedSources != 1 {
		t.Fatalf("assistant completion summary=%+v err=%v", completed, err)
	}
	health = env.health(t)
	if !health.AssistantIndexed || health.UserPrompts != 1 || health.AssistantPrompts != 1 || health.StaleSources != 0 {
		t.Fatalf("assistant health = %+v", health)
	}
	catalog, err := env.database.ListCatalog(historystore.CatalogQuery{Source: historystore.CatalogSourceAny})
	if err != nil || len(catalog.Sessions) != 1 || catalog.Sessions[0].LogicalPromptCount != 1 || catalog.Sessions[0].OccurrenceCount != 1 {
		t.Fatalf("user-default catalog=%+v err=%v", catalog, err)
	}
	assistant, err := env.database.Search(historystore.SearchQuery{PromptQuery: historystore.PromptQuery{Role: "assistant", AssistantConsent: true}, Query: "assistant proposal"})
	if err != nil || len(assistant.Hits) != 1 || assistant.Hits[0].Role != history.RoleAssistant {
		t.Fatalf("assistant search=%+v err=%v", assistant, err)
	}
	assistantID := assistant.Hits[0].PromptID
	defaults, err := env.database.Search(historystore.SearchQuery{Query: "assistant proposal"})
	if err != nil || len(defaults.Hits) != 0 {
		t.Fatalf("default role changed: page=%+v err=%v", defaults, err)
	}
	afterEnable, err := env.database.ListPrompts(historystore.PromptQuery{Source: historystore.CatalogSourceAny})
	if err != nil || len(afterEnable.Prompts) != 1 || afterEnable.Prompts[0].PromptID != userID {
		t.Fatalf("user ID changed after role rebuild: before=%+v after=%+v err=%v", before, afterEnable, err)
	}

	disabled, err := Index(Options{Store: env.database, Roots: env.roots})
	if err != nil || disabled.SkippedSources != 1 {
		t.Fatalf("assistant prune summary=%+v err=%v", disabled, err)
	}
	health = env.health(t)
	if health.AssistantIndexed || health.AssistantPrompts != 0 || health.UserPrompts != 1 {
		t.Fatalf("assistant prune health = %+v", health)
	}
	notIndexed, err := env.database.Search(historystore.SearchQuery{PromptQuery: historystore.PromptQuery{Role: "assistant", AssistantConsent: false}, Query: "assistant proposal"})
	if err != nil || len(notIndexed.Hits) != 0 || len(notIndexed.Warnings) == 0 || !strings.Contains(notIndexed.Warnings[0], "assistant content is not indexed") {
		t.Fatalf("disabled assistant search=%+v err=%v", notIndexed, err)
	}
	if _, err := Index(Options{Store: env.database, Roots: env.roots, IndexAssistant: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := Index(Options{Store: env.database, Roots: env.roots, IndexAssistant: true, CompleteAssistantScope: true}); err != nil {
		t.Fatal(err)
	}
	restored, err := env.database.Search(historystore.SearchQuery{PromptQuery: historystore.PromptQuery{Role: "assistant", AssistantConsent: true}, Query: "assistant proposal"})
	if err != nil || len(restored.Hits) != 1 || restored.Hits[0].PromptID != assistantID {
		t.Fatalf("restored assistant ID=%+v want=%s err=%v", restored, assistantID, err)
	}
}

func TestConcurrentIndexAttempts(t *testing.T) {
	env := newEnvironment(t)
	release, err := historystore.Lock(env.database.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Index(Options{Store: env.database, Roots: env.roots})
	if !errors.Is(err, historystore.ErrStoreInUse) {
		t.Fatalf("concurrent index error = %v", err)
	}
}

func TestPartialRunCommitsSuccessAndDoesNotAdvanceCompleteSuccess(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("partial-run.jsonl")
	writeFile(t, path, codexMeta("partial-run")+codexPrompt("p1", "first"))
	firstTime := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	if _, err := Index(Options{Store: env.database, Roots: env.roots, Now: func() time.Time { return firstTime }}); err != nil {
		t.Fatal(err)
	}
	appendFile(t, path, codexPrompt("p2", "second"))
	secondTime := firstTime.Add(time.Hour)
	roots := []discover.Root{
		{Provider: discover.ProviderCodex, Path: env.codexRoot},
		{Provider: discover.ProviderClaude, Path: filepath.Join(env.root, "bad\x00root")},
	}
	summary, err := Index(Options{Store: env.database, Roots: roots, Now: func() time.Time { return secondTime }})
	var partial PartialError
	if !errors.As(err, &partial) || summary.AppendedSources != 1 || summary.ErrorCount != 1 {
		t.Fatalf("partial run err=%v summary=%+v", err, summary)
	}
	health := env.health(t)
	if health.Prompts != 2 || health.LastAttemptUnix != secondTime.Unix() || health.LastCompleteSuccessUnix != firstTime.Unix() || health.ErrorSources != 1 {
		t.Fatalf("partial run health = %+v", health)
	}
	thirdTime := secondTime.Add(time.Hour)
	filtered, err := Index(Options{
		Store: env.database, Roots: roots, Providers: []history.Provider{history.ProviderCodex},
		Now: func() time.Time { return thirdTime },
	})
	if err != nil || filtered.ErrorCount != 0 {
		t.Fatalf("filtered run err=%v summary=%+v", err, filtered)
	}
	health = env.health(t)
	if health.LastAttemptUnix != thirdTime.Unix() || health.LastCompleteSuccessUnix != firstTime.Unix() || health.LastRunErrorCount != 1 || health.ErrorSources != 1 {
		t.Fatalf("filtered run erased unresolved provider failure: %+v", health)
	}
}

type environment struct {
	t          *testing.T
	root       string
	codexRoot  string
	claudeRoot string
	database   *historystore.Store
	roots      []discover.Root
}

func newEnvironment(t *testing.T) *environment {
	t.Helper()
	root := t.TempDir()
	codexRoot := filepath.Join(root, "codex")
	claudeRoot := filepath.Join(root, "claude")
	database, err := historystore.Open(filepath.Join(root, "state", historystore.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return &environment{
		t: t, root: root, codexRoot: codexRoot, claudeRoot: claudeRoot, database: database,
		roots: []discover.Root{{Provider: discover.ProviderCodex, Path: codexRoot}, {Provider: discover.ProviderClaude, Path: claudeRoot}},
	}
}

func (e *environment) codexPath(name string) string {
	return filepath.Join(e.codexRoot, "sessions", "2026", "07", name)
}

func (e *environment) index(t *testing.T, full bool) Summary {
	t.Helper()
	summary, err := Index(Options{Store: e.database, Roots: e.roots, Full: full, Now: func() time.Time {
		return time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatalf("index: %v summary=%+v", err, summary)
	}
	return summary
}

func (e *environment) health(t *testing.T) historystore.Health {
	t.Helper()
	value, err := e.database.Health()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (e *environment) checkpoints(t *testing.T) map[string]historystore.Checkpoint {
	t.Helper()
	value, err := e.database.Checkpoints()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (e *environment) checkpoint(t *testing.T, provider history.Provider, path string) historystore.Checkpoint {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		canonical = path
	}
	value, ok := e.checkpoints(t)[historystore.CheckpointKey(provider, canonical)]
	if !ok {
		t.Fatalf("checkpoint not found for %s %q", provider, canonical)
	}
	return value
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, contents string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func codexMeta(sessionID string) string {
	return fmt.Sprintf("{\"timestamp\":\"2026-07-20T12:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":%q,\"cwd\":\"/repo\"}}\n", sessionID)
}

func codexPrompt(id, text string) string {
	return fmt.Sprintf("{\"timestamp\":\"2026-07-20T12:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"client_id\":%q,\"message\":%q}}\n", id, text)
}

func codexAssistant(id, text string) string {
	return fmt.Sprintf("{\"timestamp\":\"2026-07-20T12:00:02Z\",\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":%q,\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n", id, text)
}

func claudePrompt(sessionID, messageID, text string) string {
	return fmt.Sprintf("{\"type\":\"user\",\"uuid\":%q,\"sessionId\":%q,\"cwd\":\"/repo\",\"timestamp\":\"2026-07-20T12:00:01Z\",\"message\":{\"role\":\"user\",\"content\":%q}}\n", messageID, sessionID, text)
}
