package vault

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestArchiveRoundTripVerifyStatusAndDeletedPresence(t *testing.T) {
	instance, database, source, original := testVault(t, nil)
	defer database.Close()
	result, err := instance.Archive(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Providers) != 1 || result.Providers[0].Archived != 1 {
		t.Fatalf("archive result = %#v", result)
	}
	var output bytes.Buffer
	manifest, err := instance.Cat(source, 0, &output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), original) {
		t.Fatalf("cat changed bytes: %q", output.Bytes())
	}
	if manifest.FirstTS != "2026-06-10T12:00:00Z" || manifest.LastTS != "2026-06-10T13:00:00Z" || manifest.LineCount != 2 {
		t.Fatalf("manifest scan stats = %#v", manifest)
	}
	output.Reset()
	if _, err := instance.Cat(manifest.RelPath, 0, &output); err != nil || !bytes.Equal(output.Bytes(), original) {
		t.Fatalf("cat by relative path = %q, %v", output.Bytes(), err)
	}
	for _, deep := range []bool{false, true} {
		verified, err := instance.Verify(deep)
		if err != nil || verified.Verified != 1 {
			t.Fatalf("verify(deep=%t) = %#v, %v", deep, verified, err)
		}
	}
	status, err := instance.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Files != 1 || status.RawBytes != int64(len(original)) || status.ReclaimableBytes != int64(len(original)) || len(status.ReclaimablePaths) != 1 {
		t.Fatalf("status = %#v", status)
	}
	replacement := bytes.Replace(original, []byte("first"), []byte("other"), 1)
	if err := os.WriteFile(source, replacement, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(0, manifest.ModTimeUnix)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	status, err = instance.Status()
	if err != nil || status.ReclaimableBytes != 0 {
		t.Fatalf("same-metadata changed source status = %#v, %v", status, err)
	}
	if err := os.WriteFile(source, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	entries, err := instance.List(ListFilter{})
	if err != nil || len(entries) != 1 || entries[0].OriginalExists {
		t.Fatalf("list after source removal = %#v, %v", entries, err)
	}
	status, err = instance.Status()
	if err != nil || status.ReclaimableBytes != 0 {
		t.Fatalf("status after source removal = %#v, %v", status, err)
	}
}

func TestArchiveRepairsMissingBundleFromCurrentSource(t *testing.T) {
	instance, database, source, original := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	touched := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, touched, touched); err != nil {
		t.Fatal(err)
	}
	if result, err := instance.Archive(true); err != nil || result.Providers[0].Deduplicated != 1 {
		t.Fatalf("content-identical touch = %#v, %v", result, err)
	}
	manifest, found, err := database.LatestVaultFile(resolveTestPath(source))
	if err != nil || !found {
		t.Fatalf("manifest found=%t, err=%v", found, err)
	}
	if manifest.Archive != "codex/2026-06.tar.zst" {
		t.Fatalf("touched source moved manifest archive to %q", manifest.Archive)
	}
	if err := os.Remove(filepath.Join(instance.dir, filepath.FromSlash(manifest.Archive))); err != nil {
		t.Fatal(err)
	}
	result, err := instance.Archive(true)
	if err != nil || result.Providers[0].Archived != 1 {
		t.Fatalf("repair archive = %#v, %v", result, err)
	}
	var output bytes.Buffer
	if _, err := instance.Cat(source, 0, &output); err != nil || !bytes.Equal(output.Bytes(), original) {
		t.Fatalf("cat repaired bundle = %q, %v", output.Bytes(), err)
	}
	if verified, err := instance.Verify(true); err != nil || verified.Verified != 1 {
		t.Fatalf("verify repaired bundle = %#v, %v", verified, err)
	}
}

func TestArchiveReportsIncrementalStoredBytes(t *testing.T) {
	instance, database, source, _ := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := database.LatestVaultFile(resolveTestPath(source))
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(instance.dir, filepath.FromSlash(manifest.Archive))
	before, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(filepath.Dir(source), "second.jsonl")
	if err := os.WriteFile(second, []byte("{\"timestamp\":\"2026-06-10T14:00:00Z\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(second, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	result, err := instance.Archive(true)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Providers[0].StoredBytes != after.Size()-before.Size() {
		t.Fatalf("stored bytes = %d, want delta %d", result.Providers[0].StoredBytes, after.Size()-before.Size())
	}
}

func TestArchiveAllRehashesSameMetadataSource(t *testing.T) {
	instance, database, source, original := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := database.LatestVaultFile(resolveTestPath(source))
	if err != nil {
		t.Fatal(err)
	}
	updated := bytes.Replace(original, []byte("first"), []byte("other"), 1)
	if err := os.WriteFile(source, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(0, manifest.ModTimeUnix)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	latest, _, err := database.LatestVaultFile(resolveTestPath(source))
	if err != nil || latest.Version != 2 {
		t.Fatalf("latest same-metadata version = %#v, %v", latest, err)
	}
}

func TestLineStatsOrdersFractionalTimestampsChronologically(t *testing.T) {
	stats := &lineStats{}
	_, _ = stats.Write([]byte("{\"timestamp\":\"2026-01-01T12:00:00.1Z\"}\n{\"timestamp\":\"2026-01-01T12:00:00Z\"}\n"))
	stats.finish()
	if stats.firstString() != "2026-01-01T12:00:00Z" || stats.lastString() != "2026-01-01T12:00:00.1Z" {
		t.Fatalf("timestamp range = %s to %s", stats.firstString(), stats.lastString())
	}
}

func TestLineStatsBoundsOversizedRecords(t *testing.T) {
	stats := &lineStats{}
	oversized := append(bytes.Repeat([]byte("x"), (1<<20)+1), '\n')
	_, _ = stats.Write(oversized)
	_, _ = stats.Write([]byte("{\"timestamp\":\"2026-01-01T12:00:00Z\"}\n"))
	stats.finish()
	if stats.lines != 2 || stats.firstString() != "2026-01-01T12:00:00Z" || len(stats.buffer) != 0 {
		t.Fatalf("oversized line stats = %#v", stats)
	}
}

func TestChangedContentCreatesVersions(t *testing.T) {
	instance, database, source, original := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	updated := append(append([]byte{}, original...), []byte(`{"timestamp":"2026-06-11T01:00:00Z","type":"more"}`+"\n")...)
	if err := os.WriteFile(source, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	latest, err := instance.Cat(source, 0, &output)
	if err != nil || latest.Version != 2 || !bytes.Equal(output.Bytes(), updated) {
		t.Fatalf("latest cat = v%d %q, %v", latest.Version, output.Bytes(), err)
	}
	output.Reset()
	first, err := instance.Cat(source, 1, &output)
	if err != nil || first.Version != 1 || !bytes.Equal(output.Bytes(), original) {
		t.Fatalf("version 1 cat = v%d %q, %v", first.Version, output.Bytes(), err)
	}
	verified, err := instance.Verify(true)
	if err != nil || verified.Verified != 2 {
		t.Fatalf("deep version verify = %#v, %v", verified, err)
	}
	status, err := instance.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Files != 2 || status.RawBytes != int64(len(original)+len(updated)) {
		t.Fatalf("versioned status totals = %#v", status)
	}
	var stored int64
	for _, archive := range []string{"codex/2026-06.tar.zst", "codex/2026-07.tar.zst"} {
		info, err := os.Stat(filepath.Join(instance.dir, filepath.FromSlash(archive)))
		if err != nil {
			t.Fatal(err)
		}
		stored += info.Size()
	}
	if status.StoredBytes != stored {
		t.Fatalf("stored bytes = %d, want %d", status.StoredBytes, stored)
	}
}

func TestShallowVerifyDoesNotSubstituteOlderSameSizeVersion(t *testing.T) {
	instance, database, source, original := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	updated := bytes.Replace(original, []byte("first"), []byte("other"), 1)
	if len(updated) != len(original) {
		t.Fatal("test update must preserve size")
	}
	if err := os.WriteFile(source, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	files, err := database.VaultFiles()
	if err != nil || len(files) != 2 {
		t.Fatalf("manifest = %#v, %v", files, err)
	}
	archivePath := filepath.Join(instance.dir, filepath.FromSlash(files[0].Archive))
	writeTestBundle(t, archivePath, files[0], original)
	verified, err := instance.Verify(false)
	if err == nil || verified.Verified != 1 || len(verified.Failures) != 1 || verified.Failures[0].Version != 2 {
		t.Fatalf("truncated shallow verify = %#v, %v", verified, err)
	}
	status, err := instance.Status()
	if err != nil || status.ReclaimableBytes != 0 {
		t.Fatalf("truncated status = %#v, %v", status, err)
	}
}

func TestSettledFilterAllAndMidReadChange(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	instance, database, source, _ := testVault(t, func(options *Options) {
		options.Now = func() time.Time { return now }
		options.MinAge = 7 * 24 * time.Hour
	})
	defer database.Close()
	recent := now.Add(-time.Hour)
	if err := os.Chtimes(source, recent, recent); err != nil {
		t.Fatal(err)
	}
	result, err := instance.Archive(false)
	if err != nil || result.Providers[0].Archived != 0 || result.Providers[0].Skipped != 1 {
		t.Fatalf("settled archive = %#v, %v", result, err)
	}
	result, err = instance.Archive(true)
	if err != nil || result.Providers[0].Archived != 1 {
		t.Fatalf("all archive = %#v, %v", result, err)
	}

	other := filepath.Join(filepath.Dir(source), "changing.jsonl")
	if err := os.WriteFile(other, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(other, old, old); err != nil {
		t.Fatal(err)
	}
	changed := false
	instance.beforeRead = func(path string) {
		if filepath.Base(path) == filepath.Base(other) && !changed {
			changed = true
			file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			_, _ = file.WriteString("{}\n")
			_ = file.Close()
		}
	}
	result, err = instance.Archive(true)
	if err != nil || result.Providers[0].Changed != 1 {
		t.Fatalf("mid-read archive = %#v, %v", result, err)
	}
	resolvedOther, _ := filepath.EvalSymlinks(other)
	if _, found, err := database.LatestVaultFile(resolvedOther); err != nil || found {
		t.Fatalf("changed file manifest found=%t, err=%v", found, err)
	}
}

func TestArchiveCleansAbandonedStagingAndPreservesOtherFiles(t *testing.T) {
	instance, database, _, _ := testVault(t, nil)
	defer database.Close()
	if _, err := instance.EnsureFormat(); err != nil {
		t.Fatal(err)
	}
	remove := []string{".source-abandoned.zst", ".bundle-abandoned.tar.zst"}
	preserve := []string{"published.tar.zst", "published.tar.zst.rollback", "unknown.tmp"}
	for _, name := range append(append([]string{}, remove...), preserve...) {
		if err := os.WriteFile(filepath.Join(instance.dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	for _, name := range remove {
		if _, err := os.Stat(filepath.Join(instance.dir, name)); !os.IsNotExist(err) {
			t.Errorf("abandoned staging file %s remains: %v", name, err)
		}
	}
	for _, name := range append(preserve, markerName, ".tokenomnom-vault.lock") {
		if _, err := os.Stat(filepath.Join(instance.dir, name)); err != nil {
			t.Errorf("preserved vault file %s: %v", name, err)
		}
	}
}

func TestArchiveStopsWhenAbandonedStagingCleanupFails(t *testing.T) {
	instance, database, source, _ := testVault(t, nil)
	defer database.Close()
	if _, err := instance.EnsureFormat(); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(instance.dir, ".source-blocked.zst")
	if err := os.MkdirAll(filepath.Join(blocked, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Archive(true); err == nil || !strings.Contains(err.Error(), "remove abandoned vault staging file") {
		t.Fatalf("cleanup error = %v", err)
	}
	if _, found, err := database.LatestVaultFile(resolveTestPath(source)); err != nil || found {
		t.Fatalf("archive continued after cleanup failure: found=%t, err=%v", found, err)
	}
}

func TestReadinessSeparatesCoverageVerificationStatusAndBrokenBundles(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	instance, database, source, _ := testVault(t, func(options *Options) {
		options.Now = func() time.Time { return now }
		options.MinAge = 7 * 24 * time.Hour
	})
	defer database.Close()
	readiness, err := instance.Readiness()
	if err != nil {
		t.Fatal(err)
	}
	if readiness.Initialized || readiness.SettledUnvaulted != 1 || readiness.RecentUnsettled != 0 || readiness.VaultedSources != 0 {
		t.Fatalf("empty readiness = %#v", readiness)
	}
	recent := now.Add(-time.Hour)
	if err := os.Chtimes(source, recent, recent); err != nil {
		t.Fatal(err)
	}
	readiness, err = instance.Readiness()
	if err != nil || readiness.SettledUnvaulted != 0 || readiness.RecentUnsettled != 1 {
		t.Fatalf("recent readiness = %#v, %v", readiness, err)
	}
	settled := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(source, settled, settled); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Verify(false); err != nil {
		t.Fatal(err)
	}
	readiness, err = instance.Readiness()
	if err != nil || !readiness.Initialized || readiness.VaultedSources != 1 || readiness.LastArchiveUnix != now.Unix() || readiness.LastDeepVerificationUnix != 0 {
		t.Fatalf("shallow readiness = %#v, %v", readiness, err)
	}
	now = now.Add(time.Hour)
	if _, err := instance.Verify(true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := instance.Status(); err != nil {
		t.Fatal(err)
	}
	readiness, err = instance.Readiness()
	if err != nil || readiness.LastDeepVerificationUnix != now.Add(-time.Hour).Unix() || readiness.LastStatusScanUnix != now.Unix() {
		t.Fatalf("verification/status readiness = %#v, %v", readiness, err)
	}
	files, err := database.VaultFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("manifest = %#v, %v", files, err)
	}
	bundle := filepath.Join(instance.dir, filepath.FromSlash(files[0].Archive))
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(bundle, data, 0o600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := instance.Verify(true); err == nil {
		t.Fatal("corrupt bundle passed deep verification")
	}
	readiness, err = instance.Readiness()
	if err != nil || readiness.KnownBrokenBundles != 1 || readiness.LastDeepVerificationUnix != now.Add(-2*time.Hour).Unix() {
		t.Fatalf("broken readiness = %#v, %v", readiness, err)
	}
}

func TestCorruptBundleFailsDeepVerify(t *testing.T) {
	instance, database, _, _ := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	files, err := database.VaultFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("manifest = %#v, %v", files, err)
	}
	path := filepath.Join(instance.dir, filepath.FromSlash(files[0].Archive))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := instance.Verify(true)
	if err == nil || len(result.Failures) == 0 || !strings.Contains(result.Failures[0].SourcePath, "session.jsonl") {
		t.Fatalf("corrupt verify = %#v, %v", result, err)
	}
	if status, err := instance.Status(); err != nil || status.ReclaimableBytes != 0 {
		t.Fatalf("corrupt archive status = %#v, %v", status, err)
	}
	repairResult, err := instance.Archive(true)
	if err != nil {
		t.Fatalf("repair corrupt bundle: %v", err)
	}
	if repaired, err := instance.Verify(true); err != nil || repaired.Verified != 1 {
		t.Fatalf("verify repaired corrupt bundle after %#v = %#v, %v", repairResult, repaired, err)
	}
}

func TestShallowVerifyRejectsTruncatedMember(t *testing.T) {
	instance, database, _, _ := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	files, err := database.VaultFiles()
	if err != nil || len(files) != 1 {
		t.Fatalf("manifest = %#v, %v", files, err)
	}
	path := filepath.Join(instance.dir, filepath.FromSlash(files[0].Archive))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := instance.Verify(false); err == nil || result.Verified != 0 {
		t.Fatalf("truncated shallow verify = %#v, %v", result, err)
	}
}

func TestBrokenOldBundleDoesNotMisrouteChangedVersion(t *testing.T) {
	instance, database, source, original := testVault(t, nil)
	defer database.Close()
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	files, _ := database.VaultFiles()
	oldArchive := filepath.Join(instance.dir, filepath.FromSlash(files[0].Archive))
	data, err := os.ReadFile(oldArchive)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(oldArchive, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Verify(true); err == nil {
		t.Fatal("deep verify unexpectedly accepted corrupt old bundle")
	}
	updated := append(append([]byte{}, original...), []byte("{}\n")...)
	if err := os.WriteFile(source, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Archive(true); err != nil {
		t.Fatal(err)
	}
	latest, _, err := database.LatestVaultFile(resolveTestPath(source))
	if err != nil || latest.Version != 2 || latest.Archive != "codex/2026-07.tar.zst" {
		t.Fatalf("latest repaired version = %#v, %v", latest, err)
	}
	var output bytes.Buffer
	if _, err := instance.Cat(source, 0, &output); err != nil || !bytes.Equal(output.Bytes(), updated) {
		t.Fatalf("latest rerouted cat = %q, %v", output.Bytes(), err)
	}
}

func TestMarkerRefusesUnknownValuesAndNormalizesWindowsSeparators(t *testing.T) {
	if got := normalizeMemberPath(`sessions\2026\06\session.jsonl`); got != "sessions/2026/06/session.jsonl" {
		t.Fatalf("normalized member = %q", got)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, markerName), []byte(`{"vault_format":2,"encryption":"none"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectFormat(dir); err == nil || !strings.Contains(err.Error(), "unsupported vault format") {
		t.Fatalf("unknown format error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, markerName), []byte(`{"vault_format":1,"encryption":"age"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InspectFormat(dir); err == nil || !strings.Contains(err.Error(), "unsupported vault encryption") {
		t.Fatalf("unknown encryption error = %v", err)
	}
}

func testVault(t *testing.T, configure func(*Options)) (*Vault, *store.Store, string, []byte) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "sessions", "2026", "06", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"timestamp\":\"2026-06-10T12:00:00Z\",\"type\":\"first\"}\n{\"timestamp\":\"2026-06-10T13:00:00Z\",\"type\":\"second\"}")
	if err := os.WriteFile(source, content, 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 6, 11, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(source, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	options := Options{
		Dir: filepath.Join(t.TempDir(), "vault"), Store: database,
		Roots:     []discover.Root{{Provider: discover.ProviderCodex, Path: root, Exists: true}},
		Providers: []discover.Provider{discover.ProviderCodex}, MinAge: 7 * 24 * time.Hour,
		Now: func() time.Time { return time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC) },
	}
	if configure != nil {
		configure(&options)
	}
	instance, err := New(options)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return instance, database, source, content
}

func writeTestBundle(t *testing.T, path string, manifest store.VaultFile, content []byte) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	if err := writer.WriteHeader(&tar.Header{Name: manifest.RelPath, Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
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

func resolveTestPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return resolved
}
