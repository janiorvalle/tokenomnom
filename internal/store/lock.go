package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	usageLockWaitTimeout  = 2 * time.Second
	usageLockPollInterval = 25 * time.Millisecond
	maxLockOwnerBytes     = 4096
)

// ErrStoreInUse reports that another tokenomnom process owns the sync lock.
var ErrStoreInUse = errors.New("usage store is busy")

// LockStatus describes the persistent usage lock and the process recorded as
// its owner. InspectLock never removes or rewrites the lock file.
type LockStatus struct {
	Path       string
	Exists     bool
	Held       bool
	Stale      bool
	OwnerKnown bool
	Released   bool
	PID        int
	Started    string
	PIDAlive   bool
}

type lockOwner struct {
	PID      int
	Started  string
	Released bool
}

// LockSync prevents two tokenomnom processes from racing checkpoints and
// double-applying the same source records.
func (s *Store) LockSync() (func(), error) {
	return Lock(s.path)
}

// Lock acquires the process-wide sync lock before SQLite is opened. A dead
// process releases the OS lock automatically; its persistent owner record is
// replaced safely by the next successful acquisition.
func Lock(databasePath string) (func(), error) {
	stateDir := filepath.Dir(databasePath)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure state directory: %w", err)
	}
	return acquireUsageLock(databasePath + ".lock")
}

// LockPath acquires an advisory process lock without changing its parent
// directory. These auxiliary locks retain their existing blocking semantics;
// the usage lock above is the lock with a persistent owner record and bounded
// acquisition contract.
func LockPath(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create lock %s: %w", path, err)
	}
	if err := lockFileWait(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}

// InspectLock reports whether the usage lock is held and whether its recorded
// owner is still alive. It is deliberately read-only so doctor can run during
// a sync without disturbing ownership.
func InspectLock(databasePath string) (LockStatus, error) {
	status := LockStatus{Path: databasePath + ".lock"}
	file, err := os.OpenFile(status.Path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return status, nil
		}
		return status, fmt.Errorf("open usage store lock %s: %w", status.Path, err)
	}
	defer file.Close()
	status.Exists = true
	var owner lockOwner
	var ownerErr error
	if err := lockFile(file); err != nil {
		if !isLockBusy(err) {
			return status, fmt.Errorf("probe usage store lock %s: %w", status.Path, err)
		}
		status.Held = true
		owner, ownerErr = readLockOwnerFile(file)
	} else {
		// Once the probe owns the inode, the owner record cannot change
		// underneath us. This avoids reporting active metadata as stale when
		// a clean release races doctor.
		owner, ownerErr = readLockOwnerFile(file)
		if err := unlockFile(file); err != nil {
			return status, fmt.Errorf("release usage store lock probe %s: %w", status.Path, err)
		}
	}
	if ownerErr == nil {
		status.OwnerKnown = true
		status.Released = owner.Released
		status.PID = owner.PID
		status.Started = owner.Started
		status.PIDAlive = isLockProcessAlive(owner.PID)
	}
	// The lock inode is intentionally persistent. A live recorded PID with
	// no OS lock therefore means a cleanly released lock, not a stale one.
	status.Stale = !status.Held && (!status.OwnerKnown || !owner.Released)
	return status, nil
}

func acquireUsageLock(lockPath string) (func(), error) {
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create usage store lock %s: %w", lockPath, err)
	}
	deadline := time.Now().Add(usageLockWaitTimeout)
	var owner lockOwner
	var ownerErr error
	for {
		if err := lockFile(file); err == nil {
			owner = lockOwner{PID: os.Getpid(), Started: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := writeLockOwner(file, owner); err != nil {
				_ = unlockFile(file)
				_ = file.Close()
				return nil, fmt.Errorf("record usage store lock owner: %w", err)
			}
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = writeReleasedOwner(file, owner)
					_ = unlockFile(file)
					_ = file.Close()
				})
			}, nil
		} else if !isLockBusy(err) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire usage store lock %s: %w", lockPath, err)
		}

		// A dead owner should have released the OS lock. Keep retrying the
		// same inode instead of unlinking it, which could split ownership if
		// the recorded PID was reused or the original process is still alive.
		owner, ownerErr = readLockOwner(lockPath)
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, usageLockBusyError(lockPath, owner, ownerErr)
		}
		time.Sleep(usageLockPollInterval)
	}
}

func writeLockOwner(file *os.File, owner lockOwner) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("clear previous owner record: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind owner record: %w", err)
	}
	if _, err := fmt.Fprintf(file, "pid=%d started=%s state=active\n", owner.PID, owner.Started); err != nil {
		return err
	}
	return file.Sync()
}

func writeReleasedOwner(file *os.File, owner lockOwner) error {
	owner.Released = true
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(file, "pid=%d started=%s state=released released=%s\n", owner.PID, owner.Started, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return file.Sync()
}

func readLockOwner(path string) (lockOwner, error) {
	file, err := os.Open(path)
	if err != nil {
		return lockOwner{}, err
	}
	defer file.Close()
	return readLockOwnerFile(file)
}

func readLockOwnerFile(file *os.File) (lockOwner, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return lockOwner{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxLockOwnerBytes))
	if err != nil {
		return lockOwner{}, err
	}
	var owner lockOwner
	for _, field := range strings.Fields(string(data)) {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "pid":
			if _, err := fmt.Sscanf(value, "%d", &owner.PID); err != nil {
				return lockOwner{}, fmt.Errorf("invalid usage lock owner record")
			}
		case "started":
			owner.Started = value
		case "state":
			if value == "released" {
				owner.Released = true
			}
		}
	}
	if owner.PID <= 0 || owner.Started == "" {
		return lockOwner{}, fmt.Errorf("invalid usage lock owner record")
	}
	return owner, nil
}

func usageLockBusyError(lockPath string, owner lockOwner, ownerErr error) error {
	if ownerErr == nil {
		if owner.Released {
			return fmt.Errorf("%w: usage store lock %s is still held while its last recorded sync is released; wait for the current holder to finish, then retry", ErrStoreInUse, lockPath)
		}
		if isLockProcessAlive(owner.PID) {
			return fmt.Errorf("%w: pid=%d started=%s still holds %s; wait for that process to finish, then retry tokenomnom. Do not delete the lock while pid=%d is running", ErrStoreInUse, owner.PID, owner.Started, lockPath, owner.PID)
		}
		return fmt.Errorf("%w: stale lock %s records pid=%d started=%s, but that process is not running; retry tokenomnom to take it over. If it remains blocked, run tokenomnom doctor to identify the actual holder and wait for the OS lock to release; never remove the lock file while it is held", ErrStoreInUse, lockPath, owner.PID, owner.Started)
	}
	return fmt.Errorf("%w: lock %s is held but its owner record is unreadable; run tokenomnom doctor, wait for any sync to finish, then retry", ErrStoreInUse, lockPath)
}
