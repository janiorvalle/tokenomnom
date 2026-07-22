package freshness_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/history/freshness"
	"github.com/janiorvalle/tokenomnom/internal/history/indexer"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
)

func TestProbeDetectsChangedAndNewSourcesWithoutContentReadsOrWrites(t *testing.T) {
	root := t.TempDir()
	providerRoot := filepath.Join(root, "codex")
	sessions := filepath.Join(providerRoot, "sessions")
	first := filepath.Join(sessions, "first.jsonl")
	writeSource(t, first, codexFixture("first"))
	removed := filepath.Join(sessions, "removed.jsonl")
	writeSource(t, removed, codexFixture("removed"))
	indexedModTime := time.Unix(50, 0)
	for _, path := range []string{first, removed} {
		if err := os.Chtimes(path, indexedModTime, indexedModTime); err != nil {
			t.Fatal(err)
		}
	}
	databasePath := filepath.Join(root, "state", historystore.DatabaseName)
	database, err := historystore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	roots := []discover.Root{{Provider: discover.ProviderCodex, Path: providerRoot, Exists: true}}
	if _, err := indexer.Index(indexer.Options{Store: database, Roots: roots, LockHeld: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	unchanged := freshness.Probe(databasePath, roots, func() time.Time { return time.Unix(100, 0) })
	if unchanged.ChangedSourcesSinceIndex != 0 || unchanged.NewSourcesSinceIndex != 0 || unchanged.NewestSourceChange != nil || len(unchanged.Warnings) != 0 {
		t.Fatalf("unchanged probe = %+v", unchanged)
	}
	before, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(first, 0); err != nil {
		t.Fatal(err)
	}
	release, err := historystore.Lock(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	locked := freshness.Probe(databasePath, roots, nil)
	release()
	if locked.ChangedSourcesSinceIndex != 0 || len(locked.Warnings) != 0 {
		t.Fatalf("locked metadata-only probe = %+v", locked)
	}
	if err := os.Chmod(first, 0o600); err != nil {
		t.Fatal(err)
	}
	writeSource(t, first, codexFixture("first")+"\n")
	second := filepath.Join(sessions, "second.jsonl")
	writeSource(t, second, codexFixture("second"))
	changedModTime := time.Unix(100, 0)
	for _, path := range []string{first, second} {
		if err := os.Chtimes(path, changedModTime, changedModTime); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	changed := freshness.Probe(databasePath, roots, func() time.Time { return time.Unix(1_000, 0) })
	if changed.ChangedSourcesSinceIndex != 3 || changed.NewSourcesSinceIndex != 1 || changed.SettledChangedSources != 3 || changed.SettledNewSources != 1 ||
		changed.ActiveChangedSources != 0 || changed.ActiveNewSources != 0 || changed.NewestSourceChange == nil || !changed.AsOf.Equal(time.Unix(1_000, 0)) || len(changed.Warnings) != 0 {
		t.Fatalf("changed probe = %+v", changed)
	}
	after, err := os.Stat(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("probe changed history database: before=%+v after=%+v", before, after)
	}
}

func TestProbeSeparatesActiveAndSettledDrift(t *testing.T) {
	root := t.TempDir()
	providerRoot := filepath.Join(root, "codex")
	sessions := filepath.Join(providerRoot, "sessions")
	active := filepath.Join(sessions, "active.jsonl")
	recent := filepath.Join(sessions, "recent.jsonl")
	settled := filepath.Join(sessions, "settled.jsonl")
	for _, path := range []string{active, recent, settled} {
		writeSource(t, path, codexFixture(filepath.Base(path)))
	}
	databasePath := filepath.Join(root, "state", historystore.DatabaseName)
	database, err := historystore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	roots := []discover.Root{{Provider: discover.ProviderCodex, Path: providerRoot, Exists: true}}
	if _, err := indexer.Index(indexer.Options{Store: database, Roots: roots, LockHeld: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		path    string
		modTime time.Time
	}{
		{active, now.Add(-time.Minute)},
		{recent, now.Add(-freshness.SettleWindow + time.Minute)},
		{settled, now.Add(-freshness.SettleWindow - time.Minute)},
	} {
		writeSource(t, item.path, codexFixture(filepath.Base(item.path))+"\n")
		if err := os.Chtimes(item.path, item.modTime, item.modTime); err != nil {
			t.Fatal(err)
		}
	}

	result := freshness.Probe(databasePath, roots, func() time.Time { return now })
	if result.ChangedSourcesSinceIndex != 3 || result.NewSourcesSinceIndex != 0 || result.ActiveChangedSources != 2 ||
		result.ActiveNewSources != 0 || result.SettledChangedSources != 1 || result.SettledNewSources != 0 {
		t.Fatalf("settle-aware probe = %+v", result)
	}
}

func TestProbeSkipsAbsentProviderRootsAndDoesNotCreateAnIndex(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "missing", historystore.DatabaseName)
	result := freshness.Probe(databasePath, []discover.Root{{Provider: discover.ProviderCodex, Path: filepath.Join(root, "codex"), Exists: false}}, nil)
	if result.ChangedSourcesSinceIndex != 0 || result.NewSourcesSinceIndex != 0 || result.NewestSourceChange != nil || len(result.Warnings) != 0 {
		t.Fatalf("absent probe = %+v", result)
	}
	if _, err := os.Stat(filepath.Dir(databasePath)); !os.IsNotExist(err) {
		t.Fatalf("probe created history state: %v", err)
	}
}

func TestProbeDoesNotTreatAnAbsentProviderRootAsRemovedSources(t *testing.T) {
	root := t.TempDir()
	providerRoot := filepath.Join(root, "codex")
	writeSource(t, filepath.Join(providerRoot, "sessions", "first.jsonl"), codexFixture("first"))
	databasePath := filepath.Join(root, "state", historystore.DatabaseName)
	database, err := historystore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	roots := []discover.Root{{Provider: discover.ProviderCodex, Path: providerRoot, Exists: true}}
	if _, err := indexer.Index(indexer.Options{Store: database, Roots: roots, LockHeld: true}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(providerRoot); err != nil {
		t.Fatal(err)
	}
	roots[0].Exists = false
	result := freshness.Probe(databasePath, roots, nil)
	if result.ChangedSourcesSinceIndex != 0 || result.NewSourcesSinceIndex != 0 || result.NewestSourceChange != nil || len(result.Warnings) != 0 {
		t.Fatalf("absent provider root probe = %+v", result)
	}
}

func TestProbeReportsNoDriftForVaultOnlyHistory(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "state", historystore.DatabaseName)
	database, err := historystore.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	source := history.SourceReference{
		Provider:     history.ProviderCodex,
		Kind:         history.LocationVault,
		Path:         "/unavailable/provider/session.jsonl",
		Archive:      "codex/archive.tar.zst",
		RelativePath: "session.jsonl",
		VaultVersion: 1,
	}
	extraction := history.Extraction{
		Provider: history.ProviderCodex,
		Source:   source,
		Session: history.Session{
			IdentityKey:      "native:vault-only",
			NativeSessionID:  "vault-only",
			ThreadKind:       history.ThreadRoot,
			ThreadConfidence: history.ConfidenceExact,
		},
	}
	if _, err := database.PreserveSnapshot(extraction, history.PreservedSnapshot{
		Provider: history.ProviderCodex, ContentSHA256: "vault-only-hash", Size: 42,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	result := freshness.Probe(databasePath, []discover.Root{{
		Provider: discover.ProviderCodex, Path: filepath.Join(root, "missing-codex"), Exists: false,
	}}, nil)
	if result.ChangedSourcesSinceIndex != 0 || result.NewSourcesSinceIndex != 0 || result.NewestSourceChange != nil || len(result.Warnings) != 0 {
		t.Fatalf("vault-only probe = %+v", result)
	}
}

func BenchmarkProbe5000Sources(b *testing.B) {
	root := b.TempDir()
	providerRoot := filepath.Join(root, "codex")
	sessions := filepath.Join(providerRoot, "sessions")
	for index := 0; index < 5_500; index++ {
		writeBenchmarkSource(b, filepath.Join(sessions, fmt.Sprintf("%04d.jsonl", index)))
	}
	databasePath := filepath.Join(root, "state", historystore.DatabaseName)
	database, err := historystore.Open(databasePath)
	if err != nil {
		b.Fatal(err)
	}
	if err := database.Close(); err != nil {
		b.Fatal(err)
	}
	roots := []discover.Root{{Provider: discover.ProviderCodex, Path: providerRoot, Exists: true}}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result := freshness.Probe(databasePath, roots, nil)
		if result.ChangedSourcesSinceIndex != 5_500 || result.NewSourcesSinceIndex != 5_500 || len(result.Warnings) != 0 {
			b.Fatalf("probe = %+v", result)
		}
	}
}

func codexFixture(id string) string {
	return fmt.Sprintf("{\"timestamp\":\"2026-07-22T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":%q,\"thread_source\":\"user\",\"source\":\"cli\"}}\n", id)
}

func writeSource(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeBenchmarkSource(b *testing.B, path string) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		b.Fatal(err)
	}
}
