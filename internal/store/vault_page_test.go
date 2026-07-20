package store_test

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
	_ "modernc.org/sqlite"
)

func TestOpenMigratesV2ManifestToV3(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	seedV2Database(t, path, `INSERT INTO vault_files VALUES ('/one', 'codex', 'one', 'bundle', 'abc', 12, 1, NULL, NULL, 1, 2, 1);`)
	migrated, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if got, err := migrated.Meta("schema_version"); err != nil || got != "3" {
		t.Fatalf("schema version = %q, %v", got, err)
	}
	files, err := migrated.VaultFiles()
	if err != nil || len(files) != 1 || files[0].SourcePath != "/one" {
		t.Fatalf("migrated files = %#v, %v", files, err)
	}
}

func TestOpenRefusesFutureSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta VALUES ('schema_version', '99')`)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	if _, err := store.Open(path); err == nil || !strings.Contains(err.Error(), "unsupported usage store schema 99") {
		t.Fatalf("future schema error = %v", err)
	}
}

func TestFailedV3MigrationRollsBackVersionAndDDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	seedV2Database(t, path, `CREATE TABLE vault_files_provider_first_ts (value TEXT);`)
	if _, err := store.Open(path); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var version string
	if err := check.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil || version != "2" {
		t.Fatalf("schema version after rollback = %q, %v", version, err)
	}
	var count int
	if err := check.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='vault_files_provider_last_ts'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("first index survived rollback: count=%d, err=%v", count, err)
	}
}

func TestConcurrentOpensMigrateV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	seedV2Database(t, path, "")
	const workers = 6
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			database, err := store.Open(path)
			if err == nil {
				err = database.Close()
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent migration: %v", err)
		}
	}
}

func TestVaultFilesPageTraversalFiltersSortsAndLatest(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	values := []store.VaultFile{
		vaultRow("/a", discover.ProviderCodex, 1, 20, "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"),
		vaultRow("/a", discover.ProviderCodex, 2, 30, "2026-02-01T00:00:00Z", "2026-02-02T00:00:00Z"),
		vaultRow("/b", discover.ProviderClaude, 1, 10, "", ""),
		vaultRow("/c", discover.ProviderCodex, 1, 30, "2026-03-01T00:00:00Z", "2026-03-02T00:00:00Z"),
		vaultRow("/d", discover.ProviderCodex, 1, 5, "invalid", "invalid"),
	}
	if err := putVaultRows(database, values); err != nil {
		t.Fatal(err)
	}

	for _, sortBy := range []store.VaultSort{store.VaultSortSource, store.VaultSortFirstTS, store.VaultSortLastTS, store.VaultSortSize} {
		t.Run(string(sortBy), func(t *testing.T) {
			query := store.VaultFileQuery{Sort: sortBy, Limit: 2}
			var got []string
			for {
				page, err := database.VaultFilesPage(query)
				if err != nil {
					t.Fatal(err)
				}
				if len(page.Files) > 2 {
					t.Fatalf("page has %d rows", len(page.Files))
				}
				for _, file := range page.Files {
					got = append(got, fmt.Sprintf("%s:%d", file.SourcePath, file.Version))
				}
				if !page.HasMore {
					break
				}
				query.Cursor = page.NextCursor
			}
			if len(got) != len(values) {
				t.Fatalf("traversal = %v", got)
			}
			seen := map[string]bool{}
			for _, key := range got {
				if seen[key] {
					t.Fatalf("duplicate %s in %v", key, got)
				}
				seen[key] = true
			}
		})
	}

	latest, err := database.VaultFilesPage(store.VaultFileQuery{LatestOnly: true, Sort: store.VaultSortSource, Limit: 10})
	if err != nil || len(latest.Files) != 4 || latest.Files[0].Version != 2 {
		t.Fatalf("latest page = %#v, %v", latest, err)
	}
	since := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	filtered, err := database.VaultFilesPage(store.VaultFileQuery{Provider: discover.ProviderCodex, Since: &since, Sort: store.VaultSortLastTS, Limit: 10})
	if err != nil || len(filtered.Files) != 3 {
		t.Fatalf("filtered page = %#v, %v", filtered, err)
	}
}

func TestVaultFilesPageRejectsBadAndMismatchedCursors(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.VaultFilesPage(store.VaultFileQuery{Cursor: "garbage"}); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	if err := putVaultRows(database, []store.VaultFile{vaultRow("/a", discover.ProviderCodex, 1, 1, "", ""), vaultRow("/b", discover.ProviderCodex, 1, 1, "", "")}); err != nil {
		t.Fatal(err)
	}
	first, err := database.VaultFilesPage(store.VaultFileQuery{Sort: store.VaultSortSource, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	_, err = database.VaultFilesPage(store.VaultFileQuery{Sort: store.VaultSortSource, Provider: discover.ProviderClaude, Cursor: first.NextCursor})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func BenchmarkVaultFilesPage10000(b *testing.B) {
	database, err := store.Open(filepath.Join(b.TempDir(), store.DatabaseName))
	if err != nil {
		b.Fatal(err)
	}
	defer database.Close()
	values := make([]store.VaultFile, 10_000)
	for i := range values {
		values[i] = vaultRow(fmt.Sprintf("/source/%05d", i), discover.ProviderCodex, 1, int64(i%1000), "2026-01-01T00:00:00Z", fmt.Sprintf("2026-01-%02dT00:00:00Z", i%28+1))
	}
	if err := putVaultRows(database, values); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		page, err := database.VaultFilesPage(store.VaultFileQuery{Sort: store.VaultSortLastTS, Limit: 100, LatestOnly: true})
		if err != nil || len(page.Files) != 100 {
			b.Fatalf("page rows=%d, err=%v", len(page.Files), err)
		}
	}
}

func seedV2Database(t *testing.T, path, extraSQL string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`
		CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
		INSERT INTO meta VALUES ('schema_version', '2');
		CREATE TABLE vault_files (
		  source_path TEXT NOT NULL, provider TEXT NOT NULL, rel_path TEXT NOT NULL,
		  archive TEXT NOT NULL, content_sha256 TEXT NOT NULL, size INTEGER NOT NULL,
		  mtime_unix INTEGER NOT NULL, first_ts TEXT, last_ts TEXT, line_count INTEGER,
		  vaulted_at INTEGER NOT NULL, version INTEGER NOT NULL,
		  PRIMARY KEY (source_path, version)
		);` + extraSQL)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func putVaultRows(database *store.Store, values []store.VaultFile) error {
	return database.Transaction(func(tx *store.Tx) error {
		for _, value := range values {
			if err := tx.PutVaultFile(value); err != nil {
				return err
			}
		}
		return nil
	})
}

func vaultRow(path string, provider discover.Provider, version int, size int64, first, last string) store.VaultFile {
	return store.VaultFile{SourcePath: path, Provider: provider, RelPath: strings.TrimPrefix(path, "/"), Archive: "bundle.tar.zst", ContentSHA256: fmt.Sprintf("hash-%s-%d", path, version), Size: size, FirstTS: first, LastTS: last, LineCount: 1, VaultedAt: 1, Version: version}
}
