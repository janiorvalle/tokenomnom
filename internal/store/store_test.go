package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/store"
	_ "modernc.org/sqlite"
)

func TestConcurrentOpensOfInitializedStoreSucceed(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	database, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	start := make(chan struct{})
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			opened, err := store.Open(path)
			if err == nil {
				err = opened.Close()
			}
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent open: %v", err)
		}
	}
}

func TestOpenMigratesSchemaV1ToCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta(key, value) VALUES ('schema_version', '1');`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	if got, err := migrated.Meta("schema_version"); err != nil || got != "3" {
		t.Fatalf("schema version = %q, %v", got, err)
	}
	if files, err := migrated.VaultFiles(); err != nil || len(files) != 0 {
		t.Fatalf("vault table after migration = %#v, %v", files, err)
	}
}

func TestSyncLockFailsFastAndReleases(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	release, err := database.LockSync()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.LockSync(); err == nil || !strings.Contains(err.Error(), "another sync may be running") {
		t.Fatalf("second lock error = %v", err)
	}
	release()
	secondRelease, err := database.LockSync()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	secondRelease()
}

func TestStaleLockFileWithoutOSLockIsReclaimed(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	lockPath := database.Path() + ".lock"
	if err := os.WriteFile(lockPath, []byte("stale-looking"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := database.LockSync()
	if err != nil {
		t.Fatalf("stale sentinel should not block an OS lock: %v", err)
	}
	release()
}

func TestStorePermissionsArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(stateDir, store.DatabaseName)
	database, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	database.Close()
	for target, want := range map[string]os.FileMode{stateDir: 0o700, path: 0o600} {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s permissions = %o, want %o", target, got, want)
		}
	}
}

func TestOpenEscapesSQLiteDSNPathCharacters(t *testing.T) {
	names := []string{"state#copy"}
	if runtime.GOOS != "windows" {
		names = append(names, "state?old")
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, name, store.DatabaseName)
			database, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("database was not created at exact path: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
				t.Fatalf("SQLite created a truncated DSN path: %v", err)
			}
		})
	}
}
