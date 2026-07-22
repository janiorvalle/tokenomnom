package indexer

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	historystore "github.com/janiorvalle/tokenomnom/internal/history/store"
	usagestore "github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/vault"
)

func TestVaultBundleRollbackRetryAndIdempotence(t *testing.T) {
	env := newVaultEnvironment(t)
	first := []byte(codexMeta("bundle-one") + "not-json\n" + codexPrompt("p1", "first bundle prompt"))
	second := []byte(codexMeta("bundle-two") + codexPrompt("p2", "second bundle prompt"))
	manifests := []usagestore.VaultFile{
		env.manifest("one.jsonl", "/gone/one.jsonl", first, 1),
		env.manifest("two.jsonl", "/gone/two.jsonl", second, 1),
	}
	env.record(t, manifests...)
	env.writeBundle(t, []vaultTestMember{{"one.jsonl", first}, {"two.jsonl", []byte("corrupt\n")}})

	failed, err := IndexVault(env.options(false))
	var partial PartialError
	if !errors.As(err, &partial) || failed.ErrorCount != 1 || len(failed.ExclusionCounts) != 0 {
		t.Fatalf("failed backfill err=%v summary=%+v", err, failed)
	}
	stats, _ := env.history.Stats()
	if stats.Snapshots != 0 || stats.Prompts != 0 || env.health(t).IndexGeneration != 0 {
		t.Fatalf("failed bundle leaked rows: stats=%+v health=%+v", stats, env.health(t))
	}

	env.writeBundle(t, []vaultTestMember{{"one.jsonl", first}, {"two.jsonl", second}})
	retried, err := IndexVault(env.options(false))
	if err != nil || retried.IndexedBundles != 1 || retried.IndexedVersions != 2 || len(retried.ExclusionCounts) != 1 || retried.ExclusionCounts[0].Count != 1 {
		t.Fatalf("retry err=%v summary=%+v", err, retried)
	}
	stats, _ = env.history.Stats()
	if stats.Snapshots != 2 || stats.Prompts != 2 || env.health(t).IndexGeneration != 1 {
		t.Fatalf("retry stats=%+v health=%+v", stats, env.health(t))
	}
	idempotent, err := IndexVault(env.options(false))
	if err != nil || idempotent.SkippedBundles != 1 || idempotent.TraversedBundles != 0 || idempotent.IndexedVersions != 0 || env.health(t).IndexGeneration != 1 {
		t.Fatalf("idempotent err=%v summary=%+v health=%+v", err, idempotent, env.health(t))
	}
	full, err := IndexVault(env.options(true))
	if err != nil || full.SkippedBundles != 1 || len(full.ExclusionCounts) != 1 || full.ExclusionCounts[0].Count != 1 || env.health(t).IndexGeneration != 1 {
		t.Fatalf("unchanged full rebuild invalidated catalog generation: err=%v summary=%+v health=%+v", err, full, env.health(t))
	}

	env.writeBundle(t, []vaultTestMember{{"one.jsonl", first}, {"two.jsonl", []byte("corrupt\n")}})
	if _, err := IndexVault(env.options(true)); err == nil {
		t.Fatal("corrupt previously indexed bundle succeeded")
	}
	unavailable, err := env.history.ListCatalog(historystore.CatalogQuery{Source: historystore.CatalogSourceVault})
	failedStats, _ := env.history.Stats()
	if err != nil || len(unavailable.Sessions) != 0 || failedStats.Prompts != 0 || failedStats.Occurrences != 0 || env.health(t).IndexGeneration != 2 {
		t.Fatalf("failed bundle remained available: err=%v page=%+v health=%+v", err, unavailable, env.health(t))
	}
	env.writeBundle(t, []vaultTestMember{{"one.jsonl", first}, {"two.jsonl", second}})
	if _, err := IndexVault(env.options(false)); err != nil {
		t.Fatal(err)
	}
	restored, err := env.history.ListCatalog(historystore.CatalogQuery{Source: historystore.CatalogSourceVault})
	if err != nil || len(restored.Sessions) != 2 || env.health(t).IndexGeneration != 3 {
		t.Fatalf("repaired bundle availability err=%v page=%+v health=%+v", err, restored, env.health(t))
	}
}

func TestVaultConsumerFailureKeepsPreviouslyIndexedBundleAvailable(t *testing.T) {
	env := newVaultEnvironment(t)
	content := []byte(codexMeta("consumer-failure") + codexPrompt("p", "keep me"))
	manifest := env.manifest("consumer.jsonl", "/gone/consumer.jsonl", content, 1)
	env.record(t, manifest)
	env.writeBundle(t, []vaultTestMember{{"consumer.jsonl", content}})
	if _, err := IndexVault(env.options(false)); err != nil {
		t.Fatal(err)
	}
	path := env.history.Path()
	if err := env.history.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := IndexVault(env.options(true)); err == nil {
		t.Fatal("closed history store did not fail consumer indexing")
	}
	reopened, err := historystore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	page, err := reopened.ListCatalog(historystore.CatalogQuery{Source: historystore.CatalogSourceVault})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].LogicalPromptCount != 1 {
		t.Fatalf("consumer failure revoked valid vault history: err=%v page=%+v", err, page)
	}
}

func TestVaultPartialRunCommitsIndependentBundleAndKeepsCompleteSuccess(t *testing.T) {
	env := newVaultEnvironment(t)
	goodContent := []byte(codexMeta("good") + codexPrompt("good", "committed"))
	badContent := []byte(codexMeta("bad") + codexPrompt("bad", "rolled back"))
	good := env.manifest("good.jsonl", "/gone/good.jsonl", goodContent, 1)
	bad := env.manifest("bad.jsonl", "/gone/bad.jsonl", badContent, 1)
	bad.Archive = "codex/2026-08.tar.zst"
	env.record(t, good, bad)
	env.writeBundleAt(t, good.Archive, []vaultTestMember{{"good.jsonl", goodContent}})
	env.writeBundleAt(t, bad.Archive, []vaultTestMember{{"bad.jsonl", []byte("corrupt\n")}})

	summary, err := IndexVault(env.options(false))
	var partial PartialError
	if !errors.As(err, &partial) || summary.IndexedBundles != 1 || summary.ErrorCount != 1 {
		t.Fatalf("partial backfill err=%v summary=%+v", err, summary)
	}
	stats, _ := env.history.Stats()
	health := env.health(t)
	if stats.Snapshots != 1 || stats.Prompts != 1 || health.IndexGeneration != 1 || health.LastIndexUnix != env.now().Unix() || health.LastCompleteSuccessUnix != 0 || health.LastAttemptUnix != env.now().Unix() || health.BrokenSkippedBundles != 1 {
		t.Fatalf("partial backfill stats=%+v health=%+v", stats, health)
	}
}

func TestVaultExtractionSurfacesMemberDiagnosticsAndIndexesValidEOFPrompt(t *testing.T) {
	env := newVaultEnvironment(t)
	content := []byte(codexMeta("diagnostic") + "not-json\n" + strings.TrimSuffix(codexPrompt("p", "valid final prompt"), "\n"))
	manifest := env.manifest("diagnostic.jsonl", "/gone/diagnostic.jsonl", content, 1)
	env.record(t, manifest)
	env.writeBundle(t, []vaultTestMember{{"diagnostic.jsonl", content}})
	summary, err := IndexVault(env.options(false))
	if err != nil || summary.IndexedPrompts != 1 || len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0].Path, "#diagnostic.jsonl") || !strings.Contains(summary.Warnings[0].Error, "malformed JSON") || len(summary.ExclusionCounts) != 1 || summary.ExclusionCounts[0].Count != 1 {
		t.Fatalf("vault diagnostics err=%v summary=%+v", err, summary)
	}
}

func TestVaultExtractionDiagnosesMalformedEOFWithoutFailingBundle(t *testing.T) {
	env := newVaultEnvironment(t)
	content := []byte(codexMeta("malformed-eof") + codexPrompt("p", "valid prompt") + "{\"truncated\":")
	manifest := env.manifest("malformed-eof.jsonl", "/gone/malformed-eof.jsonl", content, 1)
	env.record(t, manifest)
	env.writeBundle(t, []vaultTestMember{{"malformed-eof.jsonl", content}})
	summary, err := IndexVault(env.options(false))
	if err != nil || summary.IndexedBundles != 1 || summary.IndexedPrompts != 1 || len(summary.Warnings) != 1 || !strings.Contains(summary.Warnings[0].Error, "malformed JSON") || len(summary.ExclusionCounts) != 1 || summary.ExclusionCounts[0].Count != 1 {
		t.Fatalf("malformed EOF diagnostics err=%v summary=%+v", err, summary)
	}
}

func TestArchiveAndBackfillLockOrderCannotDeadlock(t *testing.T) {
	env := newVaultEnvironment(t)
	content := []byte(codexMeta("locks") + codexPrompt("p", "lock order"))
	manifest := env.manifest("locks.jsonl", "/gone/locks.jsonl", content, 1)
	env.record(t, manifest)
	env.writeBundle(t, []vaultTestMember{{"locks.jsonl", content}})

	entered := make(chan struct{})
	releaseVisit := make(chan struct{})
	walkDone := make(chan error, 1)
	go func() {
		_, err := env.instance.WalkVerifiedBundlesComplete(vault.BundleQuery{}, func() (func(), error) {
			return historystore.Lock(env.history.Path())
		}, func(reader *vault.BundleReader) error {
			close(entered)
			<-releaseVisit
			for {
				_, err := reader.Next()
				if errors.Is(err, io.EOF) {
					return nil
				}
				if err != nil {
					return err
				}
			}
		}, func(vault.BundleWalkResult) error {
			if release, err := historystore.Lock(env.history.Path()); err == nil {
				release()
				return errors.New("history lock was released before combined completion")
			}
			return nil
		})
		walkDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("backfill did not acquire vault then history locks")
	}
	archiveDone := make(chan error, 1)
	go func() {
		_, err := env.instance.Archive(true)
		archiveDone <- err
	}()
	select {
	case err := <-archiveDone:
		t.Fatalf("archive passed the held vault lock early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseVisit)
	for name, done := range map[string]<-chan error{"backfill": walkDone, "archive": archiveDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s failed: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s deadlocked", name)
		}
	}
}

func TestExactLiveVaultCoalescingAndOlderSnapshotRetention(t *testing.T) {
	env := newVaultEnvironment(t)
	livePath := filepath.Join(env.codexRoot, "sessions", "2026", "07", "session.jsonl")
	old := []byte(codexMeta("shared-session") + codexPrompt("old", "historical only"))
	current := append(append([]byte(nil), old...), []byte(codexPrompt("new", "current prompt"))...)
	writeFile(t, livePath, string(current))
	providerSummary, err := Index(Options{Store: env.history, Roots: env.roots, Now: env.now})
	if err != nil || providerSummary.NewSources != 1 {
		t.Fatalf("provider index err=%v summary=%+v", err, providerSummary)
	}
	manifest := env.manifest("old.jsonl", livePath, old, 1)
	env.record(t, manifest)
	env.writeBundle(t, []vaultTestMember{{"old.jsonl", old}})
	backfill, err := IndexVault(env.options(false))
	if err != nil || backfill.IndexedVersions != 1 {
		t.Fatalf("backfill err=%v summary=%+v", err, backfill)
	}
	stats, _ := env.history.Stats()
	health := env.health(t)
	if stats.Sessions != 1 || stats.Snapshots != 1 || stats.Prompts != 2 || stats.Occurrences != 3 || health.ExactLiveAndVaulted != 0 {
		t.Fatalf("older snapshot did not coalesce: stats=%+v health=%+v", stats, health)
	}

	exactManifest := env.manifest("current.jsonl", livePath, current, 2)
	env.record(t, exactManifest)
	env.writeBundle(t, []vaultTestMember{{"old.jsonl", old}, {"current.jsonl", current}})
	backfill, err = IndexVault(env.options(false))
	if err != nil || backfill.IndexedVersions != 2 {
		t.Fatalf("expanded bundle err=%v summary=%+v", err, backfill)
	}
	stats, _ = env.history.Stats()
	health = env.health(t)
	if stats.Sessions != 1 || stats.Snapshots != 2 || stats.Prompts != 2 || stats.Occurrences != 5 || health.ExactLiveAndVaulted != 1 {
		t.Fatalf("exact snapshot identity stats=%+v health=%+v", stats, health)
	}
}

type vaultEnvironment struct {
	root, codexRoot, vaultDir string
	history                   *historystore.Store
	usage                     *usagestore.Store
	instance                  *vault.Vault
	roots                     []discover.Root
	now                       func() time.Time
}

func newVaultEnvironment(t *testing.T) *vaultEnvironment {
	t.Helper()
	root := t.TempDir()
	historyStore, err := historystore.Open(filepath.Join(root, "state", historystore.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	usageStore, err := usagestore.Open(filepath.Join(root, "state", usagestore.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = historyStore.Close(); _ = usageStore.Close() })
	codexRoot := filepath.Join(root, "codex")
	roots := []discover.Root{{Provider: discover.ProviderCodex, Path: codexRoot}, {Provider: discover.ProviderClaude, Path: filepath.Join(root, "claude")}}
	now := func() time.Time { return time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC) }
	vaultDir := filepath.Join(root, "vault")
	instance, err := vault.New(vault.Options{Dir: vaultDir, Store: usageStore, Roots: roots, Providers: []discover.Provider{discover.ProviderCodex}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := instance.EnsureFormat(); err != nil {
		t.Fatal(err)
	}
	return &vaultEnvironment{root: root, codexRoot: codexRoot, vaultDir: vaultDir, history: historyStore, usage: usageStore, instance: instance, roots: roots, now: now}
}

func (e *vaultEnvironment) options(full bool) VaultOptions {
	return VaultOptions{Store: e.history, Vault: e.instance, Full: full, Now: e.now}
}

func (e *vaultEnvironment) manifest(rel, source string, content []byte, version int) usagestore.VaultFile {
	digest := sha256.Sum256(content)
	return usagestore.VaultFile{
		SourcePath: source, Provider: discover.ProviderCodex, RelPath: rel, Archive: "codex/2026-07.tar.zst",
		ContentSHA256: hex.EncodeToString(digest[:]), Size: int64(len(content)), Version: version,
		FirstTS: "2026-07-20T12:00:00Z", LastTS: "2026-07-20T12:00:01Z",
	}
}

func (e *vaultEnvironment) record(t *testing.T, values ...usagestore.VaultFile) {
	t.Helper()
	if err := e.usage.Transaction(func(tx *usagestore.Tx) error {
		for _, value := range values {
			if err := tx.PutVaultFile(value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type vaultTestMember struct {
	name    string
	content []byte
}

func (e *vaultEnvironment) writeBundle(t *testing.T, members []vaultTestMember) {
	e.writeBundleAt(t, "codex/2026-07.tar.zst", members)
}

func (e *vaultEnvironment) writeBundleAt(t *testing.T, archive string, members []vaultTestMember) {
	t.Helper()
	path := filepath.Join(e.vaultDir, filepath.FromSlash(archive))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	for _, member := range members {
		if err := writer.WriteHeader(&tar.Header{Name: member.name, Size: int64(len(member.content)), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func (e *vaultEnvironment) health(t *testing.T) historystore.Health {
	t.Helper()
	value, err := e.history.Health()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
