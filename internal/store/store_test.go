package store_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/store"
)

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
