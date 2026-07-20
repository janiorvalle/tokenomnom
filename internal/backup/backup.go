// Package backup creates and inspects automatic SQLite usage-store backups.
package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/store"
)

const (
	MetaKey     = "last_backup_unix"
	metaNanoKey = "last_backup_unix_nano"
)

var backupName = regexp.MustCompile(`^usage-[0-9]{8}-[0-9]{6}\.db$`)

type Options struct {
	Enabled  bool
	Dir      string
	Interval time.Duration
	Keep     int
	Now      time.Time
}

type Result struct {
	Created bool
	Path    string
	Pruned  int
}

type Stats struct {
	Count      int
	TotalBytes int64
	NewestFile string
}

// Run creates a due backup and applies retention. Callers own sync locking.
func Run(database *store.Store, options Options) (Result, error) {
	if !options.Enabled {
		return Result{}, nil
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	lastText, err := database.Meta(metaNanoKey)
	if err != nil {
		return Result{}, err
	}
	if lastText != "" {
		last, parseErr := strconv.ParseInt(lastText, 10, 64)
		if parseErr != nil {
			return Result{}, fmt.Errorf("read last backup time: %w", parseErr)
		}
		if options.Now.Sub(time.Unix(0, last)) < options.Interval {
			return Result{}, nil
		}
	} else {
		lastText, err = database.Meta(MetaKey)
		if err != nil {
			return Result{}, err
		}
		if lastText != "" {
			last, parseErr := strconv.ParseInt(lastText, 10, 64)
			if parseErr != nil {
				return Result{}, fmt.Errorf("read last backup time: %w", parseErr)
			}
			if options.Now.Sub(time.Unix(last, 0)) < options.Interval {
				return Result{}, nil
			}
		}
	}
	_, statErr := os.Stat(options.Dir)
	dirCreated := os.IsNotExist(statErr)
	if statErr != nil && !dirCreated {
		return Result{}, fmt.Errorf("inspect backup directory: %w", statErr)
	}
	if dirCreated {
		if err := os.MkdirAll(options.Dir, 0o700); err != nil {
			return Result{}, fmt.Errorf("create backup directory: %w", err)
		}
		if err := os.Chmod(options.Dir, 0o700); err != nil {
			return Result{}, fmt.Errorf("secure backup directory: %w", err)
		}
	}
	release, err := store.LockPath(filepath.Join(options.Dir, ".tokenomnom-backup.lock"))
	if err != nil {
		return Result{}, fmt.Errorf("lock backup directory: %w", err)
	}
	defer release()
	stagingDir, err := os.MkdirTemp(options.Dir, ".tokenomnom-backup-")
	if err != nil {
		return Result{}, fmt.Errorf("create private backup staging directory: %w", err)
	}
	if err := os.Chmod(stagingDir, 0o700); err != nil {
		os.RemoveAll(stagingDir)
		return Result{}, fmt.Errorf("secure backup staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	tempPath := filepath.Join(stagingDir, "usage.db")
	if err := database.VacuumInto(tempPath); err != nil {
		return Result{}, err
	}
	if err := os.Chmod(tempPath, 0o600); err != nil {
		return Result{}, fmt.Errorf("secure backup file: %w", err)
	}
	backupTime := options.Now.UTC()
	var destination string
	for {
		destination = filepath.Join(options.Dir, "usage-"+backupTime.Format("20060102-150405")+".db")
		if _, err := os.Lstat(destination); os.IsNotExist(err) {
			break
		} else if err != nil {
			return Result{}, fmt.Errorf("inspect backup destination: %w", err)
		}
		backupTime = backupTime.Add(time.Second)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return Result{}, fmt.Errorf("publish backup: %w", err)
	}
	result := Result{Created: true, Path: destination}
	if err := database.Transaction(func(tx *store.Tx) error {
		if err := tx.SetMeta(MetaKey, strconv.FormatInt(options.Now.Unix(), 10)); err != nil {
			return err
		}
		return tx.SetMeta(metaNanoKey, strconv.FormatInt(options.Now.UnixNano(), 10))
	}); err != nil {
		return result, fmt.Errorf("record backup time: %w", err)
	}
	result.Pruned, err = prune(options.Dir, options.Keep)
	if err != nil {
		return result, err
	}
	return result, nil
}

func prune(dir string, keep int) (int, error) {
	if keep == 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read backup directory: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && backupName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	pruned := 0
	for _, name := range names[:max(0, len(names)-keep)] {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			return pruned, fmt.Errorf("prune backup %q: %w", name, err)
		}
		pruned++
	}
	return pruned, nil
}

func Inspect(dir string) (Stats, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return Stats{}, nil
	}
	if err != nil {
		return Stats{}, fmt.Errorf("read backup directory: %w", err)
	}
	var result Stats
	for _, entry := range entries {
		if entry.IsDir() || !backupName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return Stats{}, fmt.Errorf("inspect backup %q: %w", entry.Name(), err)
		}
		result.Count++
		result.TotalBytes += info.Size()
		if entry.Name() > result.NewestFile {
			result.NewestFile = entry.Name()
		}
	}
	return result, nil
}
