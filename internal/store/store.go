// Package store persists usage aggregates and incremental ingest checkpoints.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	_ "modernc.org/sqlite"
)

const (
	// SchemaVersion is the current usage database schema.
	SchemaVersion = 2
	// DatabaseName is the filename within tokenomnom's state directory.
	DatabaseName = "usage.db"
)

// ErrStoreInUse reports that another tokenomnom process owns the sync lock.
var ErrStoreInUse = errors.New("usage store is busy")

// Store owns a SQLite usage database.
type Store struct {
	db   *sql.DB
	path string
}

// Checkpoint records the last complete JSONL position processed for a file.
type Checkpoint struct {
	Path           string
	Provider       discover.Provider
	Size           int64
	ModTimeUnix    int64 // Unix nanoseconds; column name is retained by schema v1.
	ByteOffset     int64
	TailHash       string
	ParserState    string
	Missing        bool
	LastSyncedUnix int64
}

// Usage is one day, provider, and model aggregate.
type Usage struct {
	Date                   string
	Provider               discover.Provider
	Model                  string
	Input                  int64
	CacheRead              int64
	CacheWrite5m           int64
	CacheWrite1h           int64
	CacheWriteUnclassified int64
	Output                 int64
	Reasoning              int64
}

// Message is the retained normalized representation of a Claude message.
type Message struct {
	MessageID      string
	Score          int64
	TimestampMS    int64
	IterationsJSON string
}

// VaultFile records one archived version of a source transcript.
type VaultFile struct {
	SourcePath    string            `json:"source_path"`
	Provider      discover.Provider `json:"provider"`
	RelPath       string            `json:"rel_path"`
	Archive       string            `json:"archive"`
	ContentSHA256 string            `json:"content_sha256"`
	Size          int64             `json:"size"`
	ModTimeUnix   int64             `json:"mtime_unix"`
	FirstTS       string            `json:"first_ts,omitempty"`
	LastTS        string            `json:"last_ts,omitempty"`
	LineCount     int64             `json:"line_count"`
	VaultedAt     int64             `json:"vaulted_at"`
	Version       int               `json:"version"`
}

// Info summarizes persisted state for doctor and sync output.
type Info struct {
	SchemaVersion       int
	Timezone            string
	TimezoneFingerprint string
	PendingTimezone     string
	PendingFingerprint  string
	LastSyncUnix        int64
	UsageRows           int
	DistinctModels      int
	OldestDate          string
	NewestDate          string
	MissingFiles        int
	SkillOffer          string
}

// Open creates or opens a usage database and initializes the current schema.
func Open(path string) (*Store, error) {
	stateDir := filepath.Dir(path)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure state directory: %w", err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve usage store path: %w", err)
	}
	uriPath := filepath.ToSlash(absolutePath)
	if runtime.GOOS == "windows" && len(filepath.VolumeName(absolutePath)) == 2 {
		uriPath = "/" + uriPath
	}
	dsn := (&url.URL{Scheme: "file", Path: uriPath}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open usage store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: path}
	if err := store.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure usage store: %w", err)
	}
	for _, companion := range []string{path + "-wal", path + "-shm"} {
		if err := os.Chmod(companion, 0o600); err != nil && !os.IsNotExist(err) {
			db.Close()
			return nil, fmt.Errorf("secure SQLite companion file: %w", err)
		}
	}
	return store, nil
}

func (s *Store) initialize() error {
	if _, err := s.db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		return fmt.Errorf("set SQLite busy timeout: %w", err)
	}
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode = WAL;`).Scan(&mode); err != nil {
		return fmt.Errorf("enable SQLite WAL mode: %w", err)
	}
	var existingVersion int
	var metaTableExists bool
	if err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'meta')`).Scan(&metaTableExists); err != nil {
		return fmt.Errorf("inspect usage schema: %w", err)
	}
	if metaTableExists {
		err := s.db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&existingVersion)
		if err == nil {
			if existingVersion == SchemaVersion {
				return nil
			}
			if existingVersion != 1 {
				return fmt.Errorf("unsupported usage store schema %d (expected %d)", existingVersion, SchemaVersion)
			}
		} else if err != sql.ErrNoRows {
			return fmt.Errorf("read schema version: %w", err)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin schema transaction: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaSQL); err != nil {
		return fmt.Errorf("initialize usage schema: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, SchemaVersion); err != nil {
		return fmt.Errorf("record schema version: %w", err)
	}
	var version int
	if err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version != SchemaVersion {
		return fmt.Errorf("unsupported usage store schema %d (expected %d)", version, SchemaVersion)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema transaction: %w", err)
	}
	return nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database path.
func (s *Store) Path() string { return s.path }

// Meta returns one metadata value, or an empty string when the key is absent.
func (s *Store) Meta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read metadata %q: %w", key, err)
	}
	return value, nil
}

// LatestVaultFile returns the latest archived version for sourcePath.
func (s *Store) LatestVaultFile(sourcePath string) (VaultFile, bool, error) {
	row := s.db.QueryRow(vaultSelect+` WHERE source_path = ? ORDER BY version DESC LIMIT 1`, sourcePath)
	value, err := scanVaultFile(row)
	if err == sql.ErrNoRows {
		return VaultFile{}, false, nil
	}
	if err != nil {
		return VaultFile{}, false, fmt.Errorf("read vault manifest for %q: %w", sourcePath, err)
	}
	return value, true, nil
}

// VaultFileVersion returns one archived version for sourcePath.
func (s *Store) VaultFileVersion(sourcePath string, version int) (VaultFile, bool, error) {
	row := s.db.QueryRow(vaultSelect+` WHERE source_path = ? AND version = ?`, sourcePath, version)
	value, err := scanVaultFile(row)
	if err == sql.ErrNoRows {
		return VaultFile{}, false, nil
	}
	if err != nil {
		return VaultFile{}, false, fmt.Errorf("read vault manifest for %q version %d: %w", sourcePath, version, err)
	}
	return value, true, nil
}

// VaultFiles returns manifest rows in stable path and version order.
func (s *Store) VaultFiles() ([]VaultFile, error) {
	rows, err := s.db.Query(vaultSelect + ` ORDER BY source_path, version`)
	if err != nil {
		return nil, fmt.Errorf("list vault manifest: %w", err)
	}
	defer rows.Close()
	var values []VaultFile
	for rows.Next() {
		value, err := scanVaultFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan vault manifest: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// LatestVaultFiles returns only the newest manifest row for each source.
func (s *Store) LatestVaultFiles() ([]VaultFile, error) {
	rows, err := s.db.Query(vaultSelect + ` WHERE version = (SELECT MAX(v2.version) FROM vault_files v2 WHERE v2.source_path = vault_files.source_path) ORDER BY source_path`)
	if err != nil {
		return nil, fmt.Errorf("list latest vault manifest: %w", err)
	}
	defer rows.Close()
	var values []VaultFile
	for rows.Next() {
		value, err := scanVaultFile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan latest vault manifest: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

const vaultSelect = `SELECT source_path, provider, rel_path, archive, content_sha256, size, mtime_unix, first_ts, last_ts, line_count, vaulted_at, version FROM vault_files`

func scanVaultFile(row rowScanner) (VaultFile, error) {
	var value VaultFile
	var firstTS, lastTS sql.NullString
	var lineCount sql.NullInt64
	err := row.Scan(&value.SourcePath, &value.Provider, &value.RelPath, &value.Archive, &value.ContentSHA256,
		&value.Size, &value.ModTimeUnix, &firstTS, &lastTS, &lineCount, &value.VaultedAt, &value.Version)
	value.FirstTS, value.LastTS = firstTS.String, lastTS.String
	if lineCount.Valid {
		value.LineCount = lineCount.Int64
	}
	return value, err
}

// VacuumInto writes a consistent online copy of the database to path.
func (s *Store) VacuumInto(path string) error {
	if _, err := s.db.Exec(`VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("vacuum usage store into %q: %w", path, err)
	}
	return nil
}

// LockSync prevents two tokenomnom processes from racing checkpoints and
// double-applying the same source records.
func (s *Store) LockSync() (func(), error) {
	return Lock(s.path)
}

// Lock acquires the process-wide sync lock before SQLite is opened.
func Lock(databasePath string) (func(), error) {
	stateDir := filepath.Dir(databasePath)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure state directory: %w", err)
	}
	lockPath := databasePath + ".lock"
	return lockPathFile(lockPath, fmt.Errorf("%w; another sync may be running (lock %s)", ErrStoreInUse, lockPath))
}

// LockPath acquires an advisory process lock without changing its parent directory.
func LockPath(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create lock %s: %w", path, err)
	}
	if err := lockFileWait(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("acquire lock %s: %w", path, err)
	}
	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}

func lockPathFile(lockPath string, busyError error) (func(), error) {
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create sync lock: %w", err)
	}
	if err := lockFile(file); err != nil {
		file.Close()
		if isLockBusy(err) {
			return nil, busyError
		}
		return nil, fmt.Errorf("acquire sync lock: %w", err)
	}
	_ = file.Truncate(0)
	_, _ = file.WriteAt([]byte(fmt.Sprintf("pid=%d started=%s\n", os.Getpid(), time.Now().Format(time.RFC3339))), 0)
	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}

// PromoteAlias transfers all contribution ownership to a changed duplicate
// while retaining the old path as a zero-contribution alias checkpoint.
func (s *Store) PromoteAlias(aliasPath, ownerPath string, newOwner, oldOwnerAlias Checkpoint) error {
	return s.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`DELETE FROM files WHERE path = ?`, aliasPath); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`UPDATE files SET path=?, provider=?, size=?, mtime_unix=?, byte_offset=?, tail_hash=?, parser_state=?, missing=?, last_synced_unix=? WHERE path=?`,
			newOwner.Path, newOwner.Provider, newOwner.Size, newOwner.ModTimeUnix, newOwner.ByteOffset,
			newOwner.TailHash, newOwner.ParserState, newOwner.Missing, newOwner.LastSyncedUnix, ownerPath); err != nil {
			return err
		}
		if err := mergeFileContributions(tx, ownerPath, aliasPath); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`UPDATE files SET parser_state = json_set(parser_state, '$.alias_of', ?) WHERE provider = ? AND json_extract(parser_state, '$.alias_of') = ?`,
			aliasPath, discover.ProviderCodex, ownerPath); err != nil {
			return fmt.Errorf("repoint promoted aliases: %w", err)
		}
		return tx.PutCheckpoint(oldOwnerAlias)
	})
}

// ResetFileContribution reverses and removes one file's reversible rows.
func (s *Store) ResetFileContribution(path string) error {
	return s.Transaction(func(tx *Tx) error { return tx.ReverseFile(path) })
}

// Checkpoints loads all file checkpoints keyed by absolute path.
func (s *Store) Checkpoints() (map[string]Checkpoint, error) {
	rows, err := s.db.Query(`SELECT path, provider, size, mtime_unix, byte_offset, tail_hash, COALESCE(parser_state, ''), missing, last_synced_unix FROM files`)
	if err != nil {
		return nil, fmt.Errorf("query file checkpoints: %w", err)
	}
	defer rows.Close()
	result := make(map[string]Checkpoint)
	for rows.Next() {
		var checkpoint Checkpoint
		if err := rows.Scan(&checkpoint.Path, &checkpoint.Provider, &checkpoint.Size, &checkpoint.ModTimeUnix, &checkpoint.ByteOffset, &checkpoint.TailHash, &checkpoint.ParserState, &checkpoint.Missing, &checkpoint.LastSyncedUnix); err != nil {
			return nil, fmt.Errorf("scan file checkpoint: %w", err)
		}
		result[checkpoint.Path] = checkpoint
	}
	return result, rows.Err()
}

// MoveFile transfers checkpoint and reversible contribution ownership when a
// Codex session is moved from the live sessions tree into the archive.
func (s *Store) MoveFile(oldPath, newPath string) error {
	return s.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`UPDATE files SET path = ?, missing = 0 WHERE path = ?`, newPath, oldPath); err != nil {
			return fmt.Errorf("move file checkpoint: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE file_daily SET path = ? WHERE path = ?`, newPath, oldPath); err != nil {
			return fmt.Errorf("move file contributions: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE files SET parser_state = json_set(parser_state, '$.alias_of', ?) WHERE provider = ? AND json_extract(parser_state, '$.alias_of') = ?`,
			newPath, discover.ProviderCodex, oldPath); err != nil {
			return fmt.Errorf("repoint duplicate aliases: %w", err)
		}
		return nil
	})
}

// CollapseFile removes a duplicate contribution, turns its checkpoint into
// an alias, and repoints aliases that previously depended on it.
func (s *Store) CollapseFile(duplicatePath, ownerPath string, alias Checkpoint) error {
	return s.Transaction(func(tx *Tx) error {
		if err := tx.ReverseFile(duplicatePath); err != nil {
			return fmt.Errorf("reverse duplicate file %q: %w", duplicatePath, err)
		}
		if _, err := tx.tx.Exec(`UPDATE files SET parser_state = json_set(parser_state, '$.alias_of', ?) WHERE provider = ? AND json_extract(parser_state, '$.alias_of') = ?`,
			ownerPath, discover.ProviderCodex, duplicatePath); err != nil {
			return fmt.Errorf("repoint duplicate aliases: %w", err)
		}
		return tx.PutCheckpoint(alias)
	})
}

// PreserveContribution moves a contribution to a vanished duplicate before
// its present owner is rewritten, retaining the old aggregate history.
func (s *Store) PreserveContribution(ownerPath, retainedPath string, retained Checkpoint) error {
	return s.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`UPDATE file_daily SET path = ? WHERE path = ?`, retainedPath, ownerPath); err != nil {
			return fmt.Errorf("preserve file contributions: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE files SET parser_state = json_set(parser_state, '$.alias_of', ?) WHERE provider = ? AND json_extract(parser_state, '$.alias_of') = ?`,
			retainedPath, discover.ProviderCodex, ownerPath); err != nil {
			return fmt.Errorf("repoint retained aliases: %w", err)
		}
		return tx.PutCheckpoint(retained)
	})
}

// MarkMissingTimezoneStale records that missing Codex contributions could not
// be re-bucketed and must be rewritten if their raw file returns.
func (s *Store) MarkMissingTimezoneStale() error {
	return s.Transaction(func(tx *Tx) error {
		_, err := tx.tx.Exec(`UPDATE files SET last_synced_unix = -ABS(last_synced_unix) WHERE provider = ? AND missing = 1`, discover.ProviderCodex)
		return err
	})
}

// Messages loads the Claude dedupe authority keyed by message ID.
func (s *Store) Messages() (map[string]Message, error) {
	rows, err := s.db.Query(`SELECT message_id, score, ts_unix_ms, iterations FROM messages`)
	if err != nil {
		return nil, fmt.Errorf("query retained messages: %w", err)
	}
	defer rows.Close()
	result := make(map[string]Message)
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.MessageID, &message.Score, &message.TimestampMS, &message.IterationsJSON); err != nil {
			return nil, fmt.Errorf("scan retained message: %w", err)
		}
		result[message.MessageID] = message
	}
	return result, rows.Err()
}

// Transaction runs fn atomically.
func (s *Store) Transaction(fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin store transaction: %w", err)
	}
	wrapper := &Tx{tx: tx}
	if err := fn(wrapper); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit store transaction: %w", err)
	}
	return nil
}

// MarkMissing marks undiscovered checkpoint rows without changing aggregates.
func (s *Store) MarkMissing(seen map[string]bool) (int, error) {
	checkpoints, err := s.Checkpoints()
	if err != nil {
		return 0, err
	}
	count := 0
	err = s.Transaction(func(tx *Tx) error {
		for path, checkpoint := range checkpoints {
			missing := !seen[path]
			if !missing {
				continue
			}
			count++
			if checkpoint.Missing {
				continue
			}
			if _, err := tx.tx.Exec(`UPDATE files SET missing = 1 WHERE path = ?`, path); err != nil {
				return fmt.Errorf("mark checkpoint missing: %w", err)
			}
		}
		return nil
	})
	return count, err
}

// Info returns database diagnostics.
func (s *Store) Info() (Info, error) {
	var info Info
	err := s.db.QueryRow(`SELECT
		COALESCE((SELECT value FROM meta WHERE key='schema_version'), '0'),
		COALESCE((SELECT value FROM meta WHERE key='timezone'), ''),
		COALESCE((SELECT value FROM meta WHERE key='timezone_fingerprint'), ''),
		COALESCE((SELECT value FROM meta WHERE key='pending_timezone'), ''),
		COALESCE((SELECT value FROM meta WHERE key='pending_timezone_fingerprint'), ''),
		COALESCE((SELECT value FROM meta WHERE key='last_sync_unix'), '0'),
		(SELECT COUNT(*) FROM usage_daily),
		(SELECT COUNT(DISTINCT model) FROM usage_daily),
		COALESCE((SELECT MIN(date) FROM usage_daily), ''),
		COALESCE((SELECT MAX(date) FROM usage_daily), ''),
		(SELECT COUNT(*) FROM files WHERE missing=1),
		COALESCE((SELECT value FROM meta WHERE key='skill_offer'), '')`).Scan(
		&info.SchemaVersion, &info.Timezone, &info.TimezoneFingerprint,
		&info.PendingTimezone, &info.PendingFingerprint, &info.LastSyncUnix, &info.UsageRows,
		&info.DistinctModels, &info.OldestDate, &info.NewestDate, &info.MissingFiles, &info.SkillOffer)
	if err != nil {
		return Info{}, fmt.Errorf("query store diagnostics: %w", err)
	}
	return info, nil
}

// UsageRows returns all aggregates in stable key order.
func (s *Store) UsageRows() ([]Usage, error) {
	rows, err := s.db.Query(`SELECT date, provider, model, input, cache_read, cache_write_5m, cache_write_1h, cache_write_unclassified, output, reasoning FROM usage_daily ORDER BY date, provider, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Usage
	for rows.Next() {
		var usage Usage
		if err := rows.Scan(&usage.Date, &usage.Provider, &usage.Model, &usage.Input, &usage.CacheRead, &usage.CacheWrite5m, &usage.CacheWrite1h, &usage.CacheWriteUnclassified, &usage.Output, &usage.Reasoning); err != nil {
			return nil, err
		}
		result = append(result, usage)
	}
	return result, rows.Err()
}

// Tx exposes scoped write operations within one transaction.
type Tx struct{ tx *sql.Tx }

// DeleteProviderUsage removes one provider's global aggregates. Provider-owned
// authority tables remain intact so callers can rebuild the rows atomically.
func (tx *Tx) DeleteProviderUsage(provider discover.Provider) error {
	_, err := tx.tx.Exec(`DELETE FROM usage_daily WHERE provider = ?`, provider)
	return err
}

// ApplyUsage adds delta to the global aggregate and optionally to a Codex
// file's reversible contribution.
func (tx *Tx) ApplyUsage(delta Usage, path string) error {
	if err := upsertUsage(tx.tx, "usage_daily", delta, ""); err != nil {
		return err
	}
	if path != "" {
		return upsertUsage(tx.tx, "file_daily", delta, path)
	}
	return nil
}

func upsertUsage(tx *sql.Tx, table string, value Usage, path string) error {
	columns := `date, provider, model, input, cache_read, cache_write_5m, cache_write_1h, cache_write_unclassified, output, reasoning`
	args := []any{value.Date, value.Provider, value.Model, value.Input, value.CacheRead, value.CacheWrite5m, value.CacheWrite1h, value.CacheWriteUnclassified, value.Output, value.Reasoning}
	conflict := `date, provider, model`
	if table == "file_daily" {
		columns = "path, " + columns
		args = append([]any{path}, args...)
		conflict = `path, date, provider, model`
	}
	placeholders := `?, ?, ?, ?, ?, ?, ?, ?, ?, ?`
	if table == "file_daily" {
		placeholders = "?, " + placeholders
	}
	query := fmt.Sprintf(`INSERT INTO %s(%s) VALUES (%s)
		ON CONFLICT(%s) DO UPDATE SET
		input=input+excluded.input, cache_read=cache_read+excluded.cache_read,
		cache_write_5m=cache_write_5m+excluded.cache_write_5m,
		cache_write_1h=cache_write_1h+excluded.cache_write_1h,
		cache_write_unclassified=cache_write_unclassified+excluded.cache_write_unclassified,
		output=output+excluded.output, reasoning=reasoning+excluded.reasoning`, table, columns, placeholders, conflict)
	if _, err := tx.Exec(query, args...); err != nil {
		return fmt.Errorf("upsert %s: %w", table, err)
	}
	deleteWhere := `date=? AND provider=? AND model=?`
	deleteArgs := []any{value.Date, value.Provider, value.Model}
	if table == "file_daily" {
		deleteWhere = `path=? AND ` + deleteWhere
		deleteArgs = append([]any{path}, deleteArgs...)
	}
	_, err := tx.Exec(fmt.Sprintf(`DELETE FROM %s WHERE %s AND input=0 AND cache_read=0 AND cache_write_5m=0 AND cache_write_1h=0 AND cache_write_unclassified=0 AND output=0 AND reasoning=0`, table, deleteWhere), deleteArgs...)
	return err
}

// ReverseFile removes a present Codex file's prior contribution.
func (tx *Tx) ReverseFile(path string) error {
	rows, err := tx.tx.Query(`SELECT date, provider, model, input, cache_read, cache_write_5m, cache_write_1h, cache_write_unclassified, output, reasoning FROM file_daily WHERE path = ?`, path)
	if err != nil {
		return err
	}
	var values []Usage
	for rows.Next() {
		var value Usage
		if err := rows.Scan(&value.Date, &value.Provider, &value.Model, &value.Input, &value.CacheRead, &value.CacheWrite5m, &value.CacheWrite1h, &value.CacheWriteUnclassified, &value.Output, &value.Reasoning); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read file contributions for reversal: %w", err)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		value.Input *= -1
		value.CacheRead *= -1
		value.CacheWrite5m *= -1
		value.CacheWrite1h *= -1
		value.CacheWriteUnclassified *= -1
		value.Output *= -1
		value.Reasoning *= -1
		if err := upsertUsage(tx.tx, "usage_daily", value, ""); err != nil {
			return err
		}
	}
	_, err = tx.tx.Exec(`DELETE FROM file_daily WHERE path = ?`, path)
	return err
}

func mergeFileContributions(tx *Tx, fromPath, toPath string) error {
	rows, err := tx.tx.Query(`SELECT date, provider, model, input, cache_read, cache_write_5m, cache_write_1h, cache_write_unclassified, output, reasoning FROM file_daily WHERE path = ?`, fromPath)
	if err != nil {
		return err
	}
	var values []Usage
	for rows.Next() {
		var value Usage
		if err := rows.Scan(&value.Date, &value.Provider, &value.Model, &value.Input, &value.CacheRead, &value.CacheWrite5m, &value.CacheWrite1h, &value.CacheWriteUnclassified, &value.Output, &value.Reasoning); err != nil {
			rows.Close()
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if err := upsertUsage(tx.tx, "file_daily", value, toPath); err != nil {
			return err
		}
	}
	_, err = tx.tx.Exec(`DELETE FROM file_daily WHERE path = ?`, fromPath)
	return err
}

// PutCheckpoint creates or replaces a file checkpoint.
func (tx *Tx) PutCheckpoint(value Checkpoint) error {
	var parserState any
	if value.ParserState != "" {
		parserState = value.ParserState
	}
	_, err := tx.tx.Exec(`INSERT INTO files(path, provider, size, mtime_unix, byte_offset, tail_hash, parser_state, missing, last_synced_unix)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET provider=excluded.provider, size=excluded.size,
		mtime_unix=excluded.mtime_unix, byte_offset=excluded.byte_offset,
		tail_hash=excluded.tail_hash, parser_state=excluded.parser_state,
		missing=excluded.missing, last_synced_unix=excluded.last_synced_unix`,
		value.Path, value.Provider, value.Size, value.ModTimeUnix, value.ByteOffset,
		value.TailHash, parserState, value.Missing, value.LastSyncedUnix)
	return err
}

// PutMessage creates or replaces a retained Claude message.
func (tx *Tx) PutMessage(value Message) error {
	_, err := tx.tx.Exec(`INSERT INTO messages(message_id, score, ts_unix_ms, iterations) VALUES (?, ?, ?, ?)
		ON CONFLICT(message_id) DO UPDATE SET score=excluded.score, ts_unix_ms=excluded.ts_unix_ms, iterations=excluded.iterations`,
		value.MessageID, value.Score, value.TimestampMS, value.IterationsJSON)
	return err
}

// PutVaultFile records one archived source version.
func (tx *Tx) PutVaultFile(value VaultFile) error {
	var firstTS, lastTS any
	if value.FirstTS != "" {
		firstTS = value.FirstTS
	}
	if value.LastTS != "" {
		lastTS = value.LastTS
	}
	_, err := tx.tx.Exec(`INSERT INTO vault_files(source_path, provider, rel_path, archive, content_sha256, size, mtime_unix, first_ts, last_ts, line_count, vaulted_at, version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.SourcePath, value.Provider, value.RelPath, value.Archive,
		value.ContentSHA256, value.Size, value.ModTimeUnix, firstTS, lastTS, value.LineCount, value.VaultedAt, value.Version)
	return err
}

// UpdateVaultFileSourceState records current source metadata after a content-identical recheck.
func (tx *Tx) UpdateVaultFileSourceState(sourcePath string, version int, size, modTimeUnix int64) error {
	_, err := tx.tx.Exec(`UPDATE vault_files SET size = ?, mtime_unix = ? WHERE source_path = ? AND version = ?`,
		size, modTimeUnix, sourcePath, version)
	return err
}

// SetMeta records one metadata value.
func (tx *Tx) SetMeta(key, value string) error {
	_, err := tx.tx.Exec(`INSERT INTO meta(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// DeleteMeta removes one metadata value.
func (tx *Tx) DeleteMeta(key string) error {
	_, err := tx.tx.Exec(`DELETE FROM meta WHERE key = ?`, key)
	return err
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS files (
  path TEXT PRIMARY KEY, provider TEXT NOT NULL,
  size INTEGER NOT NULL, mtime_unix INTEGER NOT NULL,
  byte_offset INTEGER NOT NULL, tail_hash TEXT NOT NULL,
  parser_state TEXT, missing INTEGER NOT NULL DEFAULT 0,
  last_synced_unix INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  message_id TEXT PRIMARY KEY, score INTEGER NOT NULL,
  ts_unix_ms INTEGER NOT NULL, iterations TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_daily (
  date TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
  input INTEGER NOT NULL DEFAULT 0, cache_read INTEGER NOT NULL DEFAULT 0,
  cache_write_5m INTEGER NOT NULL DEFAULT 0, cache_write_1h INTEGER NOT NULL DEFAULT 0,
  cache_write_unclassified INTEGER NOT NULL DEFAULT 0,
  output INTEGER NOT NULL DEFAULT 0, reasoning INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (date, provider, model)
);
CREATE TABLE IF NOT EXISTS file_daily (
  path TEXT NOT NULL, date TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
  input INTEGER NOT NULL DEFAULT 0, cache_read INTEGER NOT NULL DEFAULT 0,
  cache_write_5m INTEGER NOT NULL DEFAULT 0, cache_write_1h INTEGER NOT NULL DEFAULT 0,
  cache_write_unclassified INTEGER NOT NULL DEFAULT 0,
  output INTEGER NOT NULL DEFAULT 0, reasoning INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (path, date, provider, model)
);
CREATE TABLE IF NOT EXISTS vault_files (
  source_path TEXT NOT NULL, provider TEXT NOT NULL, rel_path TEXT NOT NULL,
  archive TEXT NOT NULL, content_sha256 TEXT NOT NULL, size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL, first_ts TEXT, last_ts TEXT, line_count INTEGER,
  vaulted_at INTEGER NOT NULL, version INTEGER NOT NULL,
  PRIMARY KEY (source_path, version)
);`
