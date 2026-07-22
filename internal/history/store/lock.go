package store

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type lockOwner struct {
	PID       int    `json:"pid"`
	StartHint string `json:"start_hint"`
	Token     string `json:"token"`
	Acquired  string `json:"acquired"`
	Legacy    bool   `json:"-"`
}

var processStartedAt = time.Now().UTC()

// Lock acquires the writer-only advisory file lock. The persistent inode is
// reused so the OS owns exclusion and old metadata self-heals after acquisition.
func Lock(databasePath string) (func(), error) {
	stateDir := filepath.Dir(databasePath)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create history state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure history state directory: %w", err)
	}
	acquired := time.Now().UTC().Format(time.RFC3339Nano)
	var tokenBytes [16]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, fmt.Errorf("generate history lock owner token: %w", err)
	}
	owner := lockOwner{
		PID: os.Getpid(), StartHint: processStartedAt.Format(time.RFC3339Nano), Token: hex.EncodeToString(tokenBytes[:]),
		Acquired: acquired,
	}
	release, err := acquireHistoryLock(databasePath+".lock", owner)
	if err != nil {
		return nil, err
	}
	return release, nil
}

func acquireHistoryLock(path string, owner lockOwner) (func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create history store lock: %w", err)
	}
	if err := tryLockHistoryOwnerFile(file); err != nil {
		_ = file.Close()
		if isHistoryOwnerFileLockBusy(err) {
			return nil, historyLockBusyError(path)
		}
		return nil, fmt.Errorf("lock history store ownership file: %w", err)
	}
	if err := file.Truncate(0); err != nil {
		_ = unlockHistoryOwnerFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("clear stale history store lock: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = unlockHistoryOwnerFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("rewind history store lock: %w", err)
	}
	encoded, err := json.Marshal(owner)
	if err == nil {
		encoded = append(encoded, '\n')
		_, err = file.Write(encoded)
	}
	if err != nil {
		_ = unlockHistoryOwnerFile(file)
		_ = file.Close()
		return nil, fmt.Errorf("record history lock owner: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlockHistoryOwnerFile(file)
			_ = file.Close()
		})
	}, nil
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
	data, err := io.ReadAll(file)
	if err != nil {
		return lockOwner{}, err
	}
	var owner lockOwner
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&owner); err != nil {
		var pid int
		var acquired string
		if _, legacyErr := fmt.Sscanf(string(data), "pid=%d started=%s", &pid, &acquired); legacyErr != nil || pid <= 0 || acquired == "" {
			return lockOwner{}, err
		}
		digest := sha256.Sum256(data)
		return lockOwner{PID: pid, Token: "legacy-" + hex.EncodeToString(digest[:]), Acquired: acquired, Legacy: true}, nil
	}
	if owner.PID <= 0 || owner.Token == "" {
		return lockOwner{}, errors.New("incomplete history lock owner")
	}
	return owner, nil
}

func historyLockBusyError(path string) error {
	return fmt.Errorf("%w: another history operation may be running (lock %s)", ErrStoreInUse, path)
}
