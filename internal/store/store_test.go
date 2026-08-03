package store_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/store"
	"github.com/janiorvalle/tokenomnom/internal/version"
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
	if got, err := migrated.Meta("schema_version"); err != nil || got != "4" {
		t.Fatalf("schema version = %q, %v", got, err)
	}
	if files, err := migrated.VaultFiles(); err != nil || len(files) != 0 {
		t.Fatalf("vault table after migration = %#v, %v", files, err)
	}
}

func TestDevBuildCannotMigrateDefaultStoreWithoutOptIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TOKENOMNOM_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	previousVersion := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = previousVersion })

	path := filepath.Join(home, ".local", "state", "tokenomnom", store.DatabaseName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta(key, value) VALUES ('schema_version', '1');`); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Open(path); !errors.Is(err, store.ErrDevMigrationBlocked) {
		t.Fatalf("unapproved default-store open error = %v", err)
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var filesTable bool
	if err := check.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='files')`).Scan(&filesTable); err != nil {
		check.Close()
		t.Fatal(err)
	}
	if err := check.Close(); err != nil {
		t.Fatal(err)
	}
	if filesTable {
		t.Fatal("blocked development open migrated the default store")
	}

	database, err := store.OpenWithOptions(path, store.OpenOptions{AllowDevMigration: true})
	if err != nil {
		t.Fatalf("approved development open: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = store.Open(path)
	if err != nil {
		t.Fatalf("development open of current default store: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExplicitStateOverrideAllowsDevelopmentMigration(t *testing.T) {
	t.Setenv("TOKENOMNOM_STATE_DIR", filepath.Join(t.TempDir(), "isolated-state"))
	previousVersion := version.Version
	version.Version = "dev"
	t.Cleanup(func() { version.Version = previousVersion })

	database, err := store.Open(filepath.Join(os.Getenv("TOKENOMNOM_STATE_DIR"), store.DatabaseName))
	if err != nil {
		t.Fatalf("development open with explicit state override: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSyncLockWaitsBoundedlyAndReleases(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	release, err := database.LockSync()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	secondRelease, err := database.LockSync()
	if secondRelease != nil {
		secondRelease()
	}
	if err == nil || !errors.Is(err, store.ErrStoreInUse) {
		t.Fatalf("second lock error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("lock wait was not bounded: %s", elapsed)
	}
	if !strings.Contains(err.Error(), "pid=") || !strings.Contains(err.Error(), database.Path()+".lock") || !strings.Contains(err.Error(), "retry tokenomnom") {
		t.Fatalf("second lock error is not actionable: %v", err)
	}
	release()
	secondRelease, err = database.LockSync()
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	secondRelease()
}

func TestInspectLockReportsDeadOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	lockPath := path + ".lock"
	if err := os.WriteFile(lockPath, []byte("pid=2147483647 started=2026-08-01T23:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := store.InspectLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Exists || status.Held || !status.Stale || !status.OwnerKnown || status.PID != 2147483647 || status.PIDAlive {
		t.Fatalf("dead lock status = %+v", status)
	}
}

func TestInspectLockDoesNotCallHeldLockStaleWithIncompleteOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), store.DatabaseName)
	release, err := store.Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o600); err != nil {
		release()
		t.Fatal(err)
	}
	status, err := store.InspectLock(path)
	release()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Held || status.Stale || status.OwnerKnown {
		t.Fatalf("active incomplete lock status = %+v", status)
	}
}

func TestOpenReadOnlyDoesNotCreateOrMigrate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), store.DatabaseName)
	if _, err := store.OpenReadOnly(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only store error = %v", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only open created missing store: %v", err)
	}

	emptyPath := filepath.Join(t.TempDir(), store.DatabaseName)
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenReadOnly(emptyPath); !errors.Is(err, store.ErrStoreNeedsInitialization) {
		t.Fatalf("empty read-only store error = %v", err)
	}

	path := filepath.Join(t.TempDir(), store.DatabaseName)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT); INSERT INTO meta(key, value) VALUES ('schema_version', '1');`); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenReadOnly(path); err == nil || !strings.Contains(err.Error(), "requires migration") {
		t.Fatalf("old read-only store error = %v", err)
	}
	check, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var filesTable bool
	if err := check.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='files')`).Scan(&filesTable); err != nil {
		t.Fatal(err)
	}
	if filesTable {
		t.Fatal("read-only open migrated the old store")
	}

	currentPath := filepath.Join(t.TempDir(), store.DatabaseName)
	current, err := store.Open(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := store.OpenReadOnly(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := readOnly.Info()
	if err != nil {
		readOnly.Close()
		t.Fatal(err)
	}
	if err := readOnly.Close(); err != nil {
		t.Fatal(err)
	}
	if info.SchemaVersion != 4 {
		t.Fatalf("read-only schema version = %d", info.SchemaVersion)
	}
}

func TestStaleLockFileWithoutOSLockIsReclaimed(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), store.DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	lockPath := database.Path() + ".lock"
	if err := os.WriteFile(lockPath, []byte("pid=2147483647 started=2026-08-01T23:00:00Z\n"), 0o600); err != nil {
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
	status, err := store.InspectLock(database.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !status.OwnerKnown || status.PID != os.Getpid() || status.Stale {
		t.Fatalf("reclaimed lock status = %+v", status)
	}
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
