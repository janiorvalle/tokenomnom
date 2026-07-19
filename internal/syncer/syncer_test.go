package syncer_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/syncer"
)

func TestInitialSyncNoChangeAndCodexAppendRestoresModel(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("session.jsonl")
	write(t, path, codexModel("gpt-right")+codexUsage("2026-07-18T10:00:00Z", 10, 2), env.tick())

	first := env.sync(t, false, time.UTC, "UTC")
	if first.FilesScanned != 1 || first.EventsApplied != 1 {
		t.Fatalf("initial summary = %+v", first)
	}
	second := env.sync(t, false, time.UTC, "UTC")
	if second.FilesSkipped != 1 || second.EventsApplied != 0 {
		t.Fatalf("no-change summary = %+v", second)
	}

	appendFile(t, path, codexUsage("2026-07-18T11:00:00Z", 7, 1), env.tick())
	third := env.sync(t, false, time.UTC, "UTC")
	if third.FilesAppended != 1 || third.EventsApplied != 1 {
		t.Fatalf("append summary = %+v", third)
	}
	rows := env.rows(t)
	if len(rows) != 1 || rows[0].Model != "gpt-right" || rows[0].Input != 17 || rows[0].Output != 3 {
		t.Fatalf("rows after append = %+v", rows)
	}
}

func TestUnknownIngestionWarningSurvivesSeparateReattribution(t *testing.T) {
	env := newEnvironment(t)
	reattributedPath := env.codexPath("a-reattributed.jsonl")
	unknownPath := env.codexPath("b-unknown.jsonl")
	write(t, reattributedPath, codexUsage("2026-07-18T10:00:00Z", 100, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, reattributedPath, codexModel("now-known"), env.tick())
	write(t, unknownPath, codexUsage("2026-07-18T11:00:00Z", 10, 0), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	if summary.UnknownModelTokens != 10 {
		t.Fatalf("unknown ingestion warning tokens = %d, want 10; summary=%+v", summary.UnknownModelTokens, summary)
	}
}

func TestUnknownWarningExcludesPreviouslyBufferedTokens(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("still-unknown.jsonl")
	write(t, path, codexUsage("2026-07-18T10:00:00Z", 5, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, path, codexUsage("2026-07-18T11:00:00Z", 3, 0), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	if summary.UnknownModelTokens != 3 {
		t.Fatalf("unknown warning recounted buffered tokens: summary=%+v", summary)
	}
}

func TestTrailingPartialLineWaitsForCompletion(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("live.jsonl")
	model := codexModel("live-model")
	usage := codexUsage("2026-07-18T10:00:00Z", 4, 1)
	write(t, path, model+usage[:len(usage)-1], env.tick())

	env.sync(t, false, time.UTC, "UTC")
	if rows := env.rows(t); len(rows) != 0 {
		t.Fatalf("partial record was counted: %+v", rows)
	}
	checkpoints, err := env.database.Checkpoints()
	if err != nil {
		t.Fatal(err)
	}
	checkpointPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := checkpoints[checkpointPath].ByteOffset; got != int64(len(model)) {
		t.Fatalf("partial checkpoint offset = %d, want %d", got, len(model))
	}

	appendFile(t, path, "\n", env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	if summary.EventsApplied != 1 || len(env.rows(t)) != 1 {
		t.Fatalf("completed record not counted once: summary=%+v rows=%+v", summary, env.rows(t))
	}
}

func TestModelLessCodexUsageIsUnknownThenReattributed(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("late-model.jsonl")
	write(t, path, codexUsage("2026-07-18T10:00:00Z", 5, 2), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if len(rows) != 1 || rows[0].Model != "unknown" || rows[0].Input != 5 {
		t.Fatalf("model-less usage was not retained as unknown: %+v", rows)
	}

	appendFile(t, path, codexModel("late-model"), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	rows = env.rows(t)
	if len(rows) != 1 || rows[0].Model != "late-model" || rows[0].Input != 5 || rows[0].Output != 2 {
		t.Fatalf("unknown usage was not reattributed: %+v", rows)
	}
}

func TestClaudeProgressiveRewriteAndLowerDuplicate(t *testing.T) {
	env := newEnvironment(t)
	path := env.claudePath("transcript.jsonl")
	write(t, path, claudeUsage("msg-1", "first", "2026-07-18T10:00:00Z", 2, 1, 0, 0, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")

	write(t, path, claudeUsage("msg-1", "replacement", "2026-07-18T12:00:00Z", 8, 3, 0, 0, 0), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Model != "replacement" || rows[0].Input != 8 || rows[0].Output != 3 {
		t.Fatalf("progressive replacement failed: summary=%+v rows=%+v", summary, rows)
	}

	write(t, path, claudeUsage("msg-1", "lower", "2026-07-18T09:00:00Z", 1, 1, 0, 0, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if got := env.rows(t); !reflect.DeepEqual(got, rows) {
		t.Fatalf("lower duplicate changed aggregates: got %+v want %+v", got, rows)
	}
}

func TestClaudeProgressiveSnapshotsWithinOneFileCountHighestOnly(t *testing.T) {
	env := newEnvironment(t)
	path := env.claudePath("progressive.jsonl")
	contents := claudeUsage("msg-progress", "first", "2026-07-18T10:00:00Z", 2, 1, 0, 0, 0) +
		claudeUsage("msg-progress", "final", "2026-07-18T11:00:00Z", 8, 3, 0, 0, 0) +
		claudeUsage("msg-progress", "equal-later", "2026-07-18T12:00:00Z", 8, 3, 0, 0, 0)
	write(t, path, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if len(rows) != 1 || rows[0].Model != "final" || rows[0].Input != 8 || rows[0].Output != 3 {
		t.Fatalf("progressive in-file dedupe failed: %+v", rows)
	}
}

func TestCodexRewriteReversesOldContribution(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("rewritten.jsonl")
	write(t, path, codexModel("old-model")+codexUsage("2026-07-18T10:00:00Z", 10, 2), env.tick())
	env.sync(t, false, time.UTC, "UTC")

	write(t, path, codexModel("new-model")+codexUsage("2026-07-18T10:00:00Z", 3, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Model != "new-model" || rows[0].Input != 3 {
		t.Fatalf("rewrite reversal failed: summary=%+v rows=%+v", summary, rows)
	}
}

func TestDeletionRetainsUsageAndIdenticalReappearanceSkips(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("retained.jsonl")
	contents := codexModel("retained") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	modTime := env.tick()
	write(t, path, contents, modTime)
	env.sync(t, false, time.UTC, "UTC")
	want := env.rows(t)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	missing := env.sync(t, false, time.UTC, "UTC")
	if missing.FilesMissing != 1 || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("deletion eroded usage: summary=%+v rows=%+v", missing, env.rows(t))
	}
	fullMissing := env.sync(t, true, time.UTC, "UTC")
	if !fullMissing.FullReingest || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("full sync eroded vanished usage: summary=%+v rows=%+v", fullMissing, env.rows(t))
	}

	write(t, path, contents, modTime)
	reappeared := env.sync(t, false, time.UTC, "UTC")
	if reappeared.FilesSkipped != 1 || reappeared.EventsApplied != 0 || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("identical reappearance did not resume cleanly: summary=%+v rows=%+v", reappeared, env.rows(t))
	}
	info, err := env.database.Info()
	if err != nil || info.MissingFiles != 0 {
		t.Fatalf("missing checkpoints after reappearance: info=%+v err=%v", info, err)
	}
}

func TestCodexArchiveMoveTransfersContributionOwnership(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("archived.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "archived.jsonl")
	write(t, livePath, codexModel("archived-model")+codexUsage("2026-07-18T10:00:00Z", 6, 2), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	want := env.rows(t)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(livePath, archivePath); err != nil {
		t.Fatal(err)
	}
	summary := env.sync(t, false, time.UTC, "UTC")
	if summary.FilesSkipped != 1 || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("archive move duplicated usage: summary=%+v rows=%+v", summary, env.rows(t))
	}
	checkpoints, err := env.database.Checkpoints()
	if err != nil || len(checkpoints) != 1 {
		t.Fatalf("archive checkpoints = %+v err=%v", checkpoints, err)
	}
	resolvedArchive, err := filepath.EvalSymlinks(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := checkpoints[resolvedArchive]; !found {
		t.Fatalf("archive checkpoint ownership was not transferred: %+v", checkpoints)
	}
}

func TestCodexArchiveMoveWithUnprocessedAppendResumes(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("moved-append.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "moved-append.jsonl")
	write(t, livePath, codexModel("moved-append")+codexUsage("2026-07-18T10:00:00Z", 6, 2), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, livePath, codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(livePath, archivePath); err != nil {
		t.Fatal(err)
	}
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesAppended != 1 || summary.EventsApplied != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("moved append was not resumed: summary=%+v rows=%+v", summary, rows)
	}
}

func TestCodexArchiveMoveMayDropUnparsedTail(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("moved-with-partial.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "moved-with-partial.jsonl")
	complete := codexModel("moved-partial") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, complete+"{\"partial\":", env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	write(t, archivePath, complete, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Input != 6 || rows[0].Output != 2 {
		t.Fatalf("truncated partial tail made archive move duplicate usage: summary=%+v rows=%+v", summary, rows)
	}
}

func TestCodexArchiveCopyDoesNotDuplicateAndTakesOverAfterSourceDeletion(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("copied.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "copied.jsonl")
	contents := codexModel("copied-model") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	modTime := env.tick()
	write(t, livePath, contents, modTime)
	env.sync(t, false, time.UTC, "UTC")
	want := env.rows(t)
	write(t, archivePath, contents, env.tick())
	copySummary := env.sync(t, false, time.UTC, "UTC")
	if copySummary.FilesSkipped != 1 || copySummary.FilesRewritten != 1 || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("archive copy duplicated usage: summary=%+v rows=%+v", copySummary, env.rows(t))
	}
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	takeoverSummary := env.sync(t, false, time.UTC, "UTC")
	if takeoverSummary.FilesSkipped != 1 || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("archive takeover changed usage: summary=%+v rows=%+v", takeoverSummary, env.rows(t))
	}
}

func TestInitialSyncDeduplicatesExistingCodexArchiveCopy(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("initial-copy.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "initial-copy.jsonl")
	contents := codexModel("initial-copy") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesScanned != 2 || summary.FilesSkipped != 1 || summary.EventsApplied != 1 ||
		len(rows) != 1 || rows[0].Input != 6 || rows[0].Output != 2 {
		t.Fatalf("initial archive copy was not deduplicated: summary=%+v rows=%+v", summary, rows)
	}
}

func TestInitialSyncDeduplicatesCodexCopiesWithDifferentFilenames(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("live-name.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "archive-name.jsonl")
	contents := codexModel("different-names") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesSkipped != 1 || summary.EventsApplied != 1 || len(rows) != 1 || rows[0].Input != 6 || rows[0].Output != 2 {
		t.Fatalf("differently named initial copies were not deduplicated: summary=%+v rows=%+v", summary, rows)
	}
}

func TestInitialSyncSplitsLongerCodexArchiveCopy(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("initial-prefix.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "initial-prefix.jsonl")
	contents := codexModel("initial-prefix") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents+codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.EventsApplied != 2 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("initial strict-prefix copies duplicated shared usage: summary=%+v rows=%+v", summary, rows)
	}
	unchanged := env.sync(t, false, time.UTC, "UTC")
	if unchanged.EventsApplied != 0 || unchanged.FilesSkipped != 2 {
		t.Fatalf("strict-prefix checkpoints were not idempotent: %+v", unchanged)
	}
}

func TestNewLongerCodexArchiveCopyStartsAtExistingCheckpoint(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("later-prefix.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "later-prefix.jsonl")
	contents := codexModel("later-prefix") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, archivePath, contents+codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesAppended != 1 || summary.EventsApplied != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("new strict-prefix copy replayed existing usage: summary=%+v rows=%+v", summary, rows)
	}
}

func TestRenamedRetainedCodexDuplicateMovesContributionOwner(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("retained-live.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "retained-archive.jsonl")
	returnedPath := env.codexPath("returned-name.jsonl")
	contents := codexModel("retained-rename") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	want := env.rows(t)
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, time.UTC, "UTC")
	write(t, returnedPath, contents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	if summary.FilesRewritten != 1 || summary.EventsApplied != 1 || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("renamed retained duplicate was ingested twice: summary=%+v rows=%+v", summary, env.rows(t))
	}
}

func TestCodexCopyGrowthResumesAfterSharedPrefix(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("growing-copy.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "growing-copy.jsonl")
	contents := codexModel("growing-copy") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	checkpoints, err := env.database.Checkpoints()
	if err != nil || len(checkpoints) != 2 {
		t.Fatalf("duplicate checkpoints were not persisted: %+v err=%v", checkpoints, err)
	}
	appendFile(t, livePath, codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesAppended != 1 || summary.EventsApplied != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("growing copy replayed its prefix: summary=%+v rows=%+v", summary, rows)
	}
	noChange := env.sync(t, false, time.UTC, "UTC")
	if noChange.FilesSkipped != 2 || noChange.EventsApplied != 0 {
		t.Fatalf("persistent copy checkpoints did not skip: %+v", noChange)
	}
}

func TestCodexOwnerAndAliasSameAppendIsCountedOnce(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("shared-append.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "shared-append.jsonl")
	contents := codexModel("shared-append") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendLine := codexUsage("2026-07-18T11:00:00Z", 4, 1)
	appendFile(t, livePath, appendLine, env.tick())
	appendFile(t, archivePath, appendLine, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesAppended != 1 || summary.FilesSkipped != 1 || summary.EventsApplied != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("shared copy append was counted more than once: summary=%+v rows=%+v", summary, rows)
	}
}

func TestCodexOwnerAndAliasDivergentAppendsStayIdempotent(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("divergent-append.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "divergent-append.jsonl")
	contents := codexModel("divergent-append") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, livePath, codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	appendFile(t, archivePath, codexUsage("2026-07-18T12:00:00Z", 5, 1), env.tick())
	first := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if first.FilesAppended != 2 || first.EventsApplied != 2 || len(rows) != 1 || rows[0].Input != 15 || rows[0].Output != 4 {
		t.Fatalf("divergent appends were not split over the shared prefix: summary=%+v rows=%+v", first, rows)
	}
	second := env.sync(t, false, time.UTC, "UTC")
	rows = env.rows(t)
	if second.EventsApplied != 0 || len(rows) != 1 || rows[0].Input != 15 || rows[0].Output != 4 {
		t.Fatalf("unchanged divergent appends were not idempotent: summary=%+v rows=%+v", second, rows)
	}
}

func TestCodexOwnerAndAliasSequentialDivergenceStaysIdempotent(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("sequential-divergence.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "sequential-divergence.jsonl")
	contents := codexModel("sequential-divergence") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, archivePath, codexUsage("2026-07-18T11:00:00Z", 5, 1), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, livePath, codexUsage("2026-07-18T12:00:00Z", 4, 1), env.tick())
	diverged := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if diverged.FilesAppended != 1 || len(rows) != 1 || rows[0].Input != 15 || rows[0].Output != 4 {
		t.Fatalf("sequential divergence did not retain one shared prefix: summary=%+v rows=%+v", diverged, rows)
	}
	unchanged := env.sync(t, false, time.UTC, "UTC")
	rows = env.rows(t)
	if unchanged.EventsApplied != 0 || len(rows) != 1 || rows[0].Input != 15 || rows[0].Output != 4 {
		t.Fatalf("sequential divergence was not idempotent: summary=%+v rows=%+v", unchanged, rows)
	}
}

func TestCodexMoveDetectionRequiresMatchingFullPrefix(t *testing.T) {
	env := newEnvironment(t)
	oldPath := env.codexPath("missing-prefix-owner.jsonl")
	newPath := env.codexPath("different-prefix.jsonl")
	padding := fmt.Sprintf("{\"padding\":%q}\n", strings.Repeat("x", 5000))
	oldContents := codexModel("prefix-old") + padding + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("prefix-new") + padding + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	if len(oldContents) != len(newContents) {
		t.Fatal("test transcripts must have equal size")
	}
	write(t, oldPath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, time.UTC, "UTC")
	write(t, newPath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesMissing != 1 || summary.EventsApplied != 1 || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("shared tail was mistaken for a move: summary=%+v rows=%+v", summary, rows)
	}
	info, err := env.database.Info()
	if err != nil || info.MissingFiles != 1 {
		t.Fatalf("retained contribution owner was not left missing: info=%+v err=%v", info, err)
	}
}

func TestCodexAliasRewritePromotesContributionOwnership(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("rewritten-copy.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "rewritten-copy.jsonl")
	oldContents := codexModel("copy-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("copy-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, oldContents, env.tick())
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, livePath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Model != "copy-new" || rows[0].Input != 6 {
		t.Fatalf("rewritten alias duplicated its prefix: summary=%+v rows=%+v", summary, rows)
	}
}

func TestCodexOwnerRewriteRetainsDivergedAliasContribution(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("owner-rewrite.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "owner-rewrite.jsonl")
	oldContents := codexModel("owner-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("owner-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, oldContents, env.tick())
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, archivePath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 2 || summary.EventsApplied != 2 || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("owner rewrite discarded diverged alias usage: summary=%+v rows=%+v", summary, rows)
	}
	full := env.sync(t, true, time.UTC, "UTC")
	rows = env.rows(t)
	if !full.FullReingest || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("full sync did not retain diverged copies: summary=%+v rows=%+v", full, rows)
	}
}

func TestCodexOwnerRewritePreservesVanishedAliasHistory(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("vanished-alias.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "vanished-alias.jsonl")
	oldContents := codexModel("vanished-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("vanished-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, oldContents, env.tick())
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	write(t, archivePath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("owner rewrite erased vanished alias history: summary=%+v rows=%+v", summary, rows)
	}
}

func TestByteIdenticalCodexAliasTakesOverMissingOwnerAfterTouch(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("identical-takeover.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "identical-takeover.jsonl")
	contents := codexModel("identical-takeover") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	write(t, livePath, contents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Input != 6 || rows[0].Output != 2 {
		t.Fatalf("identical alias takeover duplicated retained usage: summary=%+v rows=%+v", summary, rows)
	}
}

func TestPromotedCodexOwnerRewriteRetainsEarlierAlias(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("promoted-owner.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "promoted-owner.jsonl")
	oldContents := codexModel("promoted-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("promoted-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, oldContents, env.tick())
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	appendFile(t, livePath, codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, livePath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 2 || summary.EventsApplied != 2 || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("promoted owner rewrite discarded earlier alias: summary=%+v rows=%+v", summary, rows)
	}
}

func TestDivergedCodexOwnersCollapseWhenTheyConverge(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("converging.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "converging.jsonl")
	oldContents := codexModel("converge-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("converge-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, oldContents, env.tick())
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, archivePath, newContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, livePath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.EventsApplied != 0 || len(rows) != 1 || rows[0].Model != "converge-new" || rows[0].Input != 6 {
		t.Fatalf("converged owners remained double-counted: summary=%+v rows=%+v", summary, rows)
	}
}

func TestChangedCodexAliasKeepsMissingOwnerDiagnostic(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("missing-owner.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "missing-owner.jsonl")
	oldContents := codexModel("alias-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("alias-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, oldContents, env.tick())
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	write(t, livePath, newContents, env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("changed alias erased missing owner contribution: summary=%+v rows=%+v", summary, rows)
	}
	info, err := env.database.Info()
	if err != nil || info.MissingFiles != 1 {
		t.Fatalf("changed alias lost missing-owner diagnostic: info=%+v err=%v", info, err)
	}
}

func TestAppendedCodexAliasKeepsMissingOwnerContribution(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("missing-owner-append.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "missing-owner-append.jsonl")
	contents := codexModel("missing-owner-append") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	appendFile(t, livePath, codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesAppended != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("appended alias replayed or erased missing owner usage: summary=%+v rows=%+v", summary, rows)
	}
	info, err := env.database.Info()
	if err != nil || info.MissingFiles != 1 {
		t.Fatalf("missing owner diagnostic was lost: info=%+v err=%v", info, err)
	}
	write(t, livePath, contents+codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	touched := env.sync(t, false, time.UTC, "UTC")
	rows = env.rows(t)
	if touched.FilesSkipped != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("touching split alias changed retained totals: summary=%+v rows=%+v", touched, rows)
	}
	full := env.sync(t, true, time.UTC, "UTC")
	rows = env.rows(t)
	if !full.FullReingest || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("full sync replayed split contribution prefix: summary=%+v rows=%+v", full, rows)
	}
}

func TestSplitCodexAliasSurvivesOwnerFullReingestAndCollapse(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("split-owner-return.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "split-owner-return.jsonl")
	contents := codexModel("split-owner-return") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	suffix := codexUsage("2026-07-18T11:00:00Z", 4, 1)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	appendFile(t, livePath, suffix, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, livePath, contents+suffix, env.tick())
	touched := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if touched.FilesSkipped != 2 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("touching split alias with present owner changed totals: summary=%+v rows=%+v", touched, rows)
	}
	full := env.sync(t, true, time.UTC, "UTC")
	rows = env.rows(t)
	if !full.FullReingest || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("owner full reingest replayed split prefix: summary=%+v rows=%+v", full, rows)
	}
	appendFile(t, archivePath, suffix, env.tick())
	catchup := env.sync(t, false, time.UTC, "UTC")
	rows = env.rows(t)
	if catchup.FilesAppended != 1 || len(rows) != 1 || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("owner catch-up left split suffix duplicated: summary=%+v rows=%+v", catchup, rows)
	}
}

func TestSplitCodexAliasSuffixRewriteKeepsSharedPrefixSingle(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("split-suffix-rewrite.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "split-suffix-rewrite.jsonl")
	contents := codexModel("split-suffix-rewrite") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	appendFile(t, livePath, codexUsage("2026-07-18T11:00:00Z", 4, 1), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, livePath, contents+codexUsage("2026-07-18T12:00:00Z", 5, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesAppended != 1 || len(rows) != 1 || rows[0].Input != 11 || rows[0].Output != 3 {
		t.Fatalf("split suffix rewrite replayed shared prefix: summary=%+v rows=%+v", summary, rows)
	}
}

func TestCodexDivergenceRepairsAfterOwnerOnlyRewritePass(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("interrupted-alias.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "interrupted-alias.jsonl")
	oldContents := codexModel("interrupt-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("interrupt-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	oldModTime := env.tick()
	write(t, livePath, oldContents, oldModTime)
	write(t, archivePath, oldContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(livePath); err != nil {
		t.Fatal(err)
	}
	write(t, archivePath, newContents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, livePath, oldContents, oldModTime)
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesSkipped != 2 || summary.EventsApplied != 0 || len(rows) != 2 || rows[0].Input+rows[1].Input != 12 {
		t.Fatalf("post-rewrite divergence was not repaired: summary=%+v rows=%+v", summary, rows)
	}
}

func TestTimezoneChangePromotesOnlyPresentCodexAlias(t *testing.T) {
	env := newEnvironment(t)
	livePath := env.codexPath("surviving-alias.jsonl")
	archivePath := filepath.Join(env.root, "codex", "archived_sessions", "surviving-alias.jsonl")
	contents := codexModel("surviving-alias") + codexUsage("2026-07-18T00:30:00Z", 6, 2)
	write(t, livePath, contents, env.tick())
	write(t, archivePath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(archivePath); err != nil {
		t.Fatal(err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, newYork, "America/New_York")
	rows := env.rows(t)
	if len(rows) != 1 || rows[0].Date != "2026-07-17" || rows[0].Input != 6 {
		t.Fatalf("surviving alias was not re-bucketed: %+v", rows)
	}
	info, err := env.database.Info()
	if err != nil || info.MissingFiles != 1 {
		t.Fatalf("missing owner was not retained in diagnostics: info=%+v err=%v", info, err)
	}
}

func TestCodexReappearanceRepairsTimezoneStaleContribution(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("timezone-reappearance.jsonl")
	contents := codexModel("tz-reappearance") + codexUsage("2026-07-18T00:30:00Z", 6, 2)
	modTime := env.tick()
	write(t, path, contents, modTime)
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, time.UTC, "UTC")
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, newYork, "America/New_York")
	write(t, path, contents, modTime)
	summary := env.sync(t, false, newYork, "America/New_York")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Date != "2026-07-17" || rows[0].Input != 6 {
		t.Fatalf("reappearance did not repair timezone bucket: summary=%+v rows=%+v", summary, rows)
	}
}

func TestAppendedCodexReappearanceRepairsTimezoneStaleContribution(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("timezone-appended.jsonl")
	contents := codexModel("tz-appended") + codexUsage("2026-07-18T00:30:00Z", 6, 2)
	write(t, path, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, time.UTC, "UTC")
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, newYork, "America/New_York")
	write(t, path, contents+codexUsage("2026-07-18T01:30:00Z", 4, 1), env.tick())
	summary := env.sync(t, false, newYork, "America/New_York")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Date != "2026-07-17" || rows[0].Input != 10 || rows[0].Output != 3 {
		t.Fatalf("appended reappearance did not repair timezone bucket: summary=%+v rows=%+v", summary, rows)
	}
}

func TestMovedCodexReappearanceRepairsTimezoneStaleContribution(t *testing.T) {
	env := newEnvironment(t)
	oldPath := env.codexPath("timezone-old-name.jsonl")
	newPath := env.codexPath("timezone-new-name.jsonl")
	contents := codexModel("tz-moved") + codexUsage("2026-07-18T00:30:00Z", 6, 2)
	write(t, oldPath, contents, env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, time.UTC, "UTC")
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, newYork, "America/New_York")
	write(t, newPath, contents, env.tick())
	summary := env.sync(t, false, newYork, "America/New_York")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Date != "2026-07-17" || rows[0].Input != 6 {
		t.Fatalf("moved reappearance did not repair timezone bucket: summary=%+v rows=%+v", summary, rows)
	}
}

func TestFullSyncRetainsVanishedClaudeMessage(t *testing.T) {
	env := newEnvironment(t)
	path := env.claudePath("retained.jsonl")
	write(t, path, claudeUsage("msg-retained", "claude-retained", "2026-07-18T10:00:00Z", 6, 2, 0, 0, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	want := env.rows(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	env.sync(t, true, time.UTC, "UTC")
	if got := env.rows(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("full sync eroded vanished Claude usage: got=%+v want=%+v", got, want)
	}
}

func TestChangedReappearanceWithSameMetadataIsRewritten(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("same-metadata.jsonl")
	oldContents := codexModel("model-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("model-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	if len(oldContents) != len(newContents) {
		t.Fatal("test fixture contents must have the same size")
	}
	modTime := env.tick()
	write(t, path, oldContents, modTime)
	env.sync(t, false, time.UTC, "UTC")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, time.UTC, "UTC")
	write(t, path, newContents, modTime)
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Model != "model-new" {
		t.Fatalf("changed reappearance was not rewritten: summary=%+v rows=%+v", summary, rows)
	}
}

func TestSameSizeRewriteWithinOneSecondIsDetected(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("subsecond.jsonl")
	oldContents := codexModel("model-old") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	newContents := codexModel("model-new") + codexUsage("2026-07-18T10:00:00Z", 6, 2)
	second := time.Date(2026, 7, 18, 13, 0, 0, 0, time.UTC)
	write(t, path, oldContents, second.Add(100*time.Millisecond))
	env.sync(t, false, time.UTC, "UTC")
	write(t, path, newContents, second.Add(200*time.Millisecond))
	summary := env.sync(t, false, time.UTC, "UTC")
	rows := env.rows(t)
	if summary.FilesRewritten != 1 || len(rows) != 1 || rows[0].Model != "model-new" {
		t.Fatalf("same-second rewrite was missed: summary=%+v rows=%+v", summary, rows)
	}
}

func TestTimezoneChangeAutomaticallyReingests(t *testing.T) {
	env := newEnvironment(t)
	path := env.codexPath("timezone.jsonl")
	write(t, path, codexModel("tz-model")+codexUsage("2026-07-18T00:30:00Z", 2, 1), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if got := env.rows(t)[0].Date; got != "2026-07-18" {
		t.Fatalf("UTC date = %s", got)
	}

	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	summary := env.sync(t, false, newYork, "America/New_York")
	if !summary.FullReingest || env.rows(t)[0].Date != "2026-07-17" {
		t.Fatalf("timezone reingest failed: summary=%+v rows=%+v", summary, env.rows(t))
	}
}

func TestTimezoneChangeRebucketsRetainedClaudeMessages(t *testing.T) {
	env := newEnvironment(t)
	write(t, env.claudePath("timezone.jsonl"), claudeUsage("msg-tz", "claude-tz", "2026-07-18T00:30:00Z", 2, 1, 0, 0, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, newYork, "America/New_York")
	rows := env.rows(t)
	if len(rows) != 1 || rows[0].Date != "2026-07-17" || rows[0].Input != 2 {
		t.Fatalf("Claude timezone rebucket failed: %+v", rows)
	}
}

func TestSystemTimezoneFingerprintChangeTriggersReingest(t *testing.T) {
	env := newEnvironment(t)
	write(t, env.codexPath("local-timezone.jsonl"), codexModel("local-tz")+codexUsage("2026-07-18T00:30:00Z", 2, 1), env.tick())
	firstLocation := time.FixedZone("Local", 0)
	secondLocation := time.FixedZone("Local", -5*60*60)
	env.syncFingerprint(t, false, firstLocation, "Local", "offset-zero")
	summary := env.syncFingerprint(t, false, secondLocation, "Local", "offset-minus-five")
	rows := env.rows(t)
	if !summary.FullReingest || len(rows) != 1 || rows[0].Date != "2026-07-17" {
		t.Fatalf("local timezone rule change was missed: summary=%+v rows=%+v", summary, rows)
	}
}

func TestIncompleteTimezoneMigrationMustBeCompleted(t *testing.T) {
	env := newEnvironment(t)
	write(t, env.codexPath("pending-timezone.jsonl"), codexModel("pending-tz")+codexUsage("2026-07-18T00:30:00Z", 2, 1), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	if err := env.database.Transaction(func(tx *store.Tx) error {
		if err := tx.SetMeta("pending_timezone", "America/New_York"); err != nil {
			return err
		}
		return tx.SetMeta("pending_timezone_fingerprint", "America/New_York")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.Sync(syncer.Options{
		Store:    env.database,
		Roots:    []discover.Root{{Provider: discover.ProviderCodex, Path: filepath.Join(env.root, "codex")}},
		Location: time.UTC, Timezone: "UTC", TimezoneFingerprint: "UTC",
	}); err == nil || !strings.Contains(err.Error(), "incomplete timezone migration") {
		t.Fatalf("conflicting migration error = %v", err)
	}
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	env.sync(t, false, newYork, "America/New_York")
	info, err := env.database.Info()
	if err != nil || info.PendingTimezone != "" || info.Timezone != "America/New_York" {
		t.Fatalf("migration was not completed: info=%+v err=%v", info, err)
	}
}

func TestFullSyncIsIdempotent(t *testing.T) {
	env := newEnvironment(t)
	write(t, env.codexPath("one.jsonl"), codexModel("codex")+codexUsage("2026-07-18T10:00:00Z", 3, 1), env.tick())
	write(t, env.claudePath("two.jsonl"), claudeUsage("msg-full", "claude", "2026-07-18T11:00:00Z", 4, 2, 0, 0, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	want := env.rows(t)

	summary := env.sync(t, true, time.UTC, "UTC")
	if !summary.FullReingest || !reflect.DeepEqual(env.rows(t), want) {
		t.Fatalf("full sync differs: summary=%+v got=%+v want=%+v", summary, env.rows(t), want)
	}
}

func TestFullSyncPreservesHigherClaudeAuthority(t *testing.T) {
	env := newEnvironment(t)
	path := env.claudePath("full-lower.jsonl")
	write(t, path, claudeUsage("msg-lower", "high", "2026-07-18T10:00:00Z", 10, 2, 0, 0, 0), env.tick())
	env.sync(t, false, time.UTC, "UTC")
	write(t, path, claudeUsage("msg-lower", "low", "2026-07-18T10:00:00Z", 3, 1, 0, 0, 0), env.tick())
	env.sync(t, true, time.UTC, "UTC")
	rows := env.rows(t)
	if len(rows) != 1 || rows[0].Model != "high" || rows[0].Input != 10 || rows[0].Output != 2 {
		t.Fatalf("full sync downgraded retained message authority: %+v", rows)
	}
}

func TestResidualDiagnosticsAreLoudInSummary(t *testing.T) {
	env := newEnvironment(t)
	write(t, env.codexPath("unknown.jsonl"), codexModel("")+codexUsage("2026-07-18T10:00:00Z", 5, 2), env.tick())
	write(t, env.claudePath("unclassified.jsonl"), claudeUsage("msg-cache", "claude", "2026-07-18T11:00:00Z", 1, 1, 5, 1, 1), env.tick())
	summary := env.sync(t, false, time.UTC, "UTC")
	if summary.UnknownModelTokens != 7 || summary.UnclassifiedCacheWriteTokens != 3 {
		t.Fatalf("diagnostics = %+v", summary)
	}
}

type environment struct {
	t        *testing.T
	root     string
	database *store.Store
	nextTime time.Time
}

func newEnvironment(t *testing.T) *environment {
	t.Helper()
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "state", store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return &environment{t: t, root: root, database: database, nextTime: time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)}
}

func (e *environment) codexPath(name string) string {
	return filepath.Join(e.root, "codex", "sessions", name)
}

func (e *environment) claudePath(name string) string {
	return filepath.Join(e.root, "claude", "projects", "fixture", name)
}

func (e *environment) tick() time.Time {
	e.nextTime = e.nextTime.Add(time.Second)
	return e.nextTime
}

func (e *environment) sync(t *testing.T, full bool, location *time.Location, timezone string) syncer.Summary {
	return e.syncFingerprint(t, full, location, timezone, timezone)
}

func (e *environment) syncFingerprint(t *testing.T, full bool, location *time.Location, timezone, fingerprint string) syncer.Summary {
	t.Helper()
	summary, err := syncer.Sync(syncer.Options{
		Store: e.database,
		Roots: []discover.Root{
			{Provider: discover.ProviderCodex, Path: filepath.Join(e.root, "codex")},
			{Provider: discover.ProviderClaude, Path: filepath.Join(e.root, "claude")},
		},
		Location: location, Timezone: timezone, TimezoneFingerprint: fingerprint, Full: full,
	})
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func (e *environment) rows(t *testing.T) []store.Usage {
	t.Helper()
	rows, err := e.database.UsageRows()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func write(t *testing.T, path, contents string, modTime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, contents string, modTime time.Time) {
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
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

func codexModel(model string) string {
	return fmt.Sprintf("{\"timestamp\":\"2026-07-18T09:00:00Z\",\"type\":\"turn_context\",\"payload\":{\"model\":%q}}\n", model)
}

func codexUsage(timestamp string, input, output int64) string {
	return fmt.Sprintf("{\"timestamp\":%q,\"type\":\"event_msg\",\"payload\":{\"type\":\"token_count\",\"info\":{\"total_token_usage\":{\"input_tokens\":%d,\"output_tokens\":%d,\"total_tokens\":%d},\"last_token_usage\":{\"input_tokens\":%d,\"output_tokens\":%d,\"total_tokens\":%d}}}}\n", timestamp, input, output, input+output, input, output, input+output)
}

func claudeUsage(id, model, timestamp string, input, output, cacheCreation, cache5m, cache1h int64) string {
	return fmt.Sprintf("{\"timestamp\":%q,\"type\":\"assistant\",\"message\":{\"id\":%q,\"model\":%q,\"usage\":{\"input_tokens\":%d,\"cache_creation_input_tokens\":%d,\"output_tokens\":%d,\"cache_creation\":{\"ephemeral_5m_input_tokens\":%d,\"ephemeral_1h_input_tokens\":%d}}}}\n", timestamp, id, model, input, cacheCreation, output, cache5m, cache1h)
}
