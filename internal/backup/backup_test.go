package backup

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/store"
)

func TestRunCreatesOpenableIdenticalBackupWhenDue(t *testing.T) {
	database := seedStore(t)
	defer database.Close()
	dir := filepath.Join(t.TempDir(), "backups")
	now := time.Date(2026, 7, 19, 12, 34, 56, 0, time.UTC)
	result, err := Run(database, Options{Enabled: true, Dir: dir, Interval: time.Hour, Keep: 14, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || filepath.Base(result.Path) != "usage-20260719-123456.db" {
		t.Fatalf("result = %#v", result)
	}
	want, err := database.UsageRows()
	if err != nil {
		t.Fatal(err)
	}
	copy, err := store.Open(result.Path)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer copy.Close()
	got, err := copy.UsageRows()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backup rows = %#v, want %#v", got, want)
	}
}

func TestRunDueDisabledAndFailurePaths(t *testing.T) {
	database := seedStore(t)
	defer database.Close()
	dir := filepath.Join(t.TempDir(), "backups")
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if result, err := Run(database, Options{Dir: dir, Interval: time.Hour, Now: now}); err != nil || result.Created {
		t.Fatalf("disabled = %#v, %v", result, err)
	}
	first, err := Run(database, Options{Enabled: true, Dir: dir, Interval: time.Hour, Keep: 14, Now: now})
	if err != nil || !first.Created {
		t.Fatalf("first = %#v, %v", first, err)
	}
	second, err := Run(database, Options{Enabled: true, Dir: dir, Interval: time.Hour, Keep: 14, Now: now.Add(59 * time.Minute)})
	if err != nil || second.Created {
		t.Fatalf("not due = %#v, %v", second, err)
	}

	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(database, Options{Enabled: true, Dir: blocked, Interval: time.Hour, Now: now.Add(2 * time.Hour)}); err == nil {
		t.Fatal("backup into a file unexpectedly succeeded")
	}
}

func TestRunUsesSubsecondPrecisionForDueCheck(t *testing.T) {
	database := seedStore(t)
	defer database.Close()
	dir := t.TempDir()
	now := time.Date(2026, 7, 19, 12, 0, 0, 900_000_000, time.UTC)
	options := Options{Enabled: true, Dir: dir, Interval: 500 * time.Millisecond, Keep: 0, Now: now}
	if result, err := Run(database, options); err != nil || !result.Created {
		t.Fatalf("initial backup = %#v, %v", result, err)
	}
	options.Now = now.Add(100 * time.Millisecond)
	if result, err := Run(database, options); err != nil || result.Created {
		t.Fatalf("early backup = %#v, %v", result, err)
	}
	options.Now = now.Add(500 * time.Millisecond)
	if result, err := Run(database, options); err != nil || !result.Created {
		t.Fatalf("due backup = %#v, %v", result, err)
	}
}

func TestRetentionPrunesOnlyOldestMatchingFiles(t *testing.T) {
	dir := t.TempDir()
	matching := []string{
		"usage-20260716-000000.db", "usage-20260717-000000.db", "usage-20260718-000000.db",
	}
	for _, name := range append(matching, "notes.db", "usage-bad.db") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	count, err := prune(dir, 2)
	if err != nil || count != 1 {
		t.Fatalf("prune = %d, %v", count, err)
	}
	if _, err := os.Stat(filepath.Join(dir, matching[0])); !os.IsNotExist(err) {
		t.Fatalf("oldest matching backup still exists: %v", err)
	}
	for _, name := range []string{matching[1], matching[2], "notes.db", "usage-bad.db"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("retained %q: %v", name, err)
		}
	}
	stats, err := Inspect(dir)
	if err != nil || stats.Count != 2 || stats.NewestFile != matching[2] {
		t.Fatalf("stats = %#v, %v", stats, err)
	}
}

func TestRunPreservesExistingDirectoryModeAndAvoidsCollision(t *testing.T) {
	database := seedStore(t)
	defer database.Close()
	dir := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if _, err := Run(database, Options{Enabled: true, Dir: dir, Interval: 100 * time.Millisecond, Keep: 0, Now: now}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("existing directory mode = %o, want 755", info.Mode().Perm())
		}
	}
	if _, err := Run(database, Options{Enabled: true, Dir: dir, Interval: 100 * time.Millisecond, Keep: 0, Now: now.Add(500 * time.Millisecond)}); err != nil {
		t.Fatalf("same-second backup: %v", err)
	}
	stats, err := Inspect(dir)
	if err != nil || stats.Count != 2 {
		t.Fatalf("collision stats = %#v, %v", stats, err)
	}
}

func TestConcurrentStoresSerializeSharedBackupDirectory(t *testing.T) {
	first := seedStore(t)
	defer first.Close()
	second := seedStore(t)
	defer second.Close()
	dir := t.TempDir()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	errors := make(chan error, 2)
	for _, database := range []*store.Store{first, second} {
		go func(database *store.Store) {
			_, err := Run(database, Options{Enabled: true, Dir: dir, Interval: time.Hour, Keep: 0, Now: now})
			errors <- err
		}(database)
	}
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	stats, err := Inspect(dir)
	if err != nil || stats.Count != 2 {
		t.Fatalf("shared-directory stats = %#v, %v", stats, err)
	}
}

func seedStore(t *testing.T) *store.Store {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(func(tx *store.Tx) error {
		return tx.ApplyUsage(store.Usage{
			Date: "2026-07-19", Provider: discover.ProviderCodex, Model: "gpt-test",
			Input: 100, CacheRead: 20, Output: 10,
		}, "")
	})
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	return database
}
