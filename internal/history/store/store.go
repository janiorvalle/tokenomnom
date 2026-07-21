// Package store persists rebuildable transcript history separately from usage.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/sqliteutil"
	usagestore "github.com/janiorvalle/tokenomnom/internal/store"
	_ "modernc.org/sqlite"
)

const (
	SchemaVersion = 2
	DatabaseName  = "history.db"
)

var ErrStoreInUse = errors.New("history store is busy")

// Store owns one history database connection.
type Store struct {
	db   *sql.DB
	path string
}

// Info is non-content history database metadata.
type Info struct {
	Exists           bool
	Path             string
	Size             int64
	SchemaVersion    int
	ExtractorVersion int
}

// Open creates or migrates a history database.
func Open(path string) (*Store, error) {
	stateDir := filepath.Dir(path)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create history state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure history state directory: %w", err)
	}
	dsn, err := fileDSN(path, false)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open history store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	value := &Store{db: db, path: path}
	if err := value.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	if err := secureFiles(path); err != nil {
		db.Close()
		return nil, err
	}
	return value, nil
}

// Inspect reads versions without creating a database or parent directory.
func Inspect(path string) (Info, error) {
	info := Info{Path: path}
	stat, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return info, nil
	}
	if err != nil {
		return info, fmt.Errorf("inspect history store: %w", err)
	}
	info.Exists, info.Size = true, stat.Size()
	dsn, err := fileDSN(path, true)
	if err != nil {
		return info, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return info, fmt.Errorf("inspect history store: %w", err)
	}
	defer db.Close()
	if err := db.QueryRow(`SELECT
		COALESCE((SELECT value FROM meta WHERE key='schema_version'), '0'),
		COALESCE((SELECT value FROM meta WHERE key='extractor_version'), '0')`).Scan(&info.SchemaVersion, &info.ExtractorVersion); err != nil {
		return info, fmt.Errorf("inspect history versions: %w", err)
	}
	return info, nil
}

func fileDSN(path string, readOnly bool) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve history store path: %w", err)
	}
	uriPath := filepath.ToSlash(absolute)
	if runtime.GOOS == "windows" && len(filepath.VolumeName(absolute)) == 2 {
		uriPath = "/" + uriPath
	}
	uri := &url.URL{Scheme: "file", Path: uriPath}
	query := url.Values{}
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		query.Set("mode", "ro")
	}
	uri.RawQuery = query.Encode()
	return uri.String(), nil
}

func (s *Store) initialize() error {
	if _, err := s.db.Exec(`PRAGMA busy_timeout = 5000;`); err != nil {
		return fmt.Errorf("set history SQLite busy timeout: %w", err)
	}
	if err := sqliteutil.EnableWAL(s.db, "history"); err != nil {
		return err
	}
	if _, err := s.db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		return fmt.Errorf("enable history foreign keys: %w", err)
	}
	var enabled bool
	if err := s.db.QueryRow(`PRAGMA foreign_keys;`).Scan(&enabled); err != nil || !enabled {
		return fmt.Errorf("history foreign keys are not enabled")
	}

	if err := sqliteutil.Migrate(s.db, historyMigrationPlan()); err != nil {
		return err
	}
	return validateSchema(s.db)
}

func historyMigrationPlan() sqliteutil.MigrationPlan {
	return sqliteutil.MigrationPlan{
		Label:    "history store",
		Current:  SchemaVersion,
		FreshSQL: schemaSQL,
		Steps: map[int]string{
			2: `
ALTER TABLE source_heads ADD COLUMN source_kind TEXT NOT NULL DEFAULT 'codex_live' CHECK (source_kind IN ('codex_live', 'codex_archive', 'claude_project'));
ALTER TABLE source_heads ADD COLUMN content_hash_state TEXT NOT NULL DEFAULT '';
ALTER TABLE source_heads ADD COLUMN prefix_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE source_heads ADD COLUMN tail_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE source_heads ADD COLUMN extractor_state TEXT NOT NULL DEFAULT '';
ALTER TABLE source_heads ADD COLUMN last_attempt_unix INTEGER NOT NULL DEFAULT 0;
ALTER TABLE source_heads ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
UPDATE source_heads SET source_kind='claude_project' WHERE provider='claude';
UPDATE source_heads SET source_kind='codex_archive' WHERE provider='codex' AND EXISTS (
	SELECT 1 FROM locations WHERE locations.source_head_id=source_heads.id AND locations.kind='provider_archive'
);
CREATE TABLE prompt_tombstones (
	id INTEGER PRIMARY KEY,
	source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	provider TEXT NOT NULL,
	source_path TEXT NOT NULL,
	prompt_public_id TEXT NOT NULL,
	logical_key TEXT NOT NULL,
	reason TEXT NOT NULL,
	deleted_at INTEGER NOT NULL
);
CREATE INDEX prompt_tombstones_source_idx ON prompt_tombstones(source_head_id, deleted_at DESC);
`,
		},
		AfterStep: func(tx sqliteutil.MigrationExecer, _ int) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES
				('extractor_version', ?), ('index_generation', '0'),
				('last_attempt_unix', '0'), ('last_complete_success_unix', '0'), ('last_run_error_count', '0')
				ON CONFLICT(key) DO NOTHING`, history.ExtractorVersion); err != nil {
				return fmt.Errorf("record history metadata: %w", err)
			}
			return nil
		},
	}
}

func applySchemaStep(db *sql.DB, version int, ddl string) error {
	plan := historyMigrationPlan()
	return sqliteutil.ApplyStep(db, plan, version, ddl)
}

func validateSchema(db *sql.DB) error {
	for _, name := range []string{"prompts_ai", "prompts_ad", "prompts_au"} {
		var exists bool
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name=?)`, name).Scan(&exists); err != nil || !exists {
			return fmt.Errorf("history schema missing FTS trigger %s", name)
		}
	}
	return nil
}

func secureFiles(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(candidate, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("secure history SQLite file %q: %w", candidate, err)
		}
	}
	return nil
}

// Path returns the database path.
func (s *Store) Path() string { return s.path }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Meta returns one history metadata value or an empty string when absent.
func (s *Store) Meta(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read history metadata %q: %w", key, err)
	}
	return value, nil
}

// ResolvePublicID returns the current opaque ID for an active or retired ID.
func (s *Store) ResolvePublicID(publicID string) (string, error) {
	var canonical string
	err := s.db.QueryRow(`SELECT canonical_public_id FROM public_id_aliases WHERE alias_public_id=?`, publicID).Scan(&canonical)
	if err == sql.ErrNoRows {
		return publicID, nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve history public ID %q: %w", publicID, err)
	}
	return canonical, nil
}

// Lock acquires the dedicated history database lock.
func Lock(databasePath string) (func(), error) {
	stateDir := filepath.Dir(databasePath)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create history state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure history state directory: %w", err)
	}
	release, err := usagestore.Lock(databasePath)
	if err != nil {
		if errors.Is(err, usagestore.ErrStoreInUse) {
			return nil, fmt.Errorf("%w: another history operation may be running (lock %s)", ErrStoreInUse, databasePath+".lock")
		}
		return nil, fmt.Errorf("acquire history store lock: %w", err)
	}
	return release, nil
}

// Transaction executes fn atomically.
func (s *Store) Transaction(fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("begin history transaction: %w", err)
	}
	wrapper := &Tx{tx: tx}
	if err := fn(wrapper); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history transaction: %w", err)
	}
	return nil
}

// Tx exposes a scoped SQL transaction to store-owned operations.
type Tx struct{ tx *sql.Tx }

// SetMeta records one history metadata value in the current transaction.
func (tx *Tx) SetMeta(key, value string) error {
	if _, err := tx.tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
		return fmt.Errorf("set history metadata %q: %w", key, err)
	}
	return nil
}

func newPublicID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", prefix, err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
