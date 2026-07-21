// Package store persists rebuildable transcript history separately from usage.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	SchemaVersion = 7
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
	if _, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES('extractor_version',?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, history.ExtractorVersion); err != nil {
		return fmt.Errorf("record current history extractor version: %w", err)
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
CREATE TABLE source_errors (
	provider TEXT NOT NULL,
	source_path TEXT NOT NULL,
	last_attempt_unix INTEGER NOT NULL,
	last_error TEXT NOT NULL,
	PRIMARY KEY(provider, source_path)
);
`,
			3: `
CREATE TABLE vault_bundle_state (
	archive TEXT PRIMARY KEY,
	manifest_fingerprint TEXT NOT NULL DEFAULT '',
	member_count INTEGER NOT NULL DEFAULT 0,
	extractor_version INTEGER NOT NULL DEFAULT 0,
	last_attempt_unix INTEGER NOT NULL DEFAULT 0,
	last_success_unix INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT ''
);
`,
			4: `
CREATE TABLE vault_prompt_tombstones (
	archive TEXT NOT NULL,
	provider TEXT NOT NULL,
	session_public_id TEXT NOT NULL,
	logical_key TEXT NOT NULL,
	prompt_public_id TEXT NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY(archive, provider, session_public_id, logical_key)
);
`,
			5: `
ALTER TABLE vault_bundle_state ADD COLUMN last_error_invalidates INTEGER NOT NULL DEFAULT 0 CHECK (last_error_invalidates IN (0, 1));
`,
			6: `
ALTER TABLE sessions ADD COLUMN thread_evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN thread_confidence TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE sessions ADD COLUMN thread_rule_version INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN forked_from_message_id TEXT;
CREATE TABLE session_relations (
	id INTEGER PRIMARY KEY,
	provider TEXT NOT NULL,
	parent_session_id INTEGER REFERENCES sessions(id) ON DELETE SET NULL,
	child_session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	relation_kind TEXT NOT NULL CHECK (relation_kind IN ('subagent', 'fork')),
	parent_native_session_id TEXT NOT NULL DEFAULT '',
	parent_native_message_id TEXT NOT NULL DEFAULT '',
	provider_native_value TEXT NOT NULL DEFAULT '',
	evidence TEXT NOT NULL,
	confidence TEXT NOT NULL CHECK (confidence IN ('exact', 'derived', 'unknown')),
	rule_version INTEGER NOT NULL,
	resolution_state TEXT NOT NULL CHECK (resolution_state IN ('resolved', 'unresolved')),
	CHECK ((parent_session_id IS NOT NULL AND resolution_state='resolved') OR
	       (parent_session_id IS NULL AND resolution_state='unresolved'))
);
CREATE UNIQUE INDEX session_relations_resolved_unique
	ON session_relations(parent_session_id, child_session_id, relation_kind)
	WHERE parent_session_id IS NOT NULL;
CREATE UNIQUE INDEX session_relations_unresolved_unique
	ON session_relations(provider, child_session_id, relation_kind, parent_native_session_id)
	WHERE parent_session_id IS NULL;
CREATE INDEX session_relations_parent_native_idx
	ON session_relations(provider, parent_native_session_id)
	WHERE parent_session_id IS NULL AND parent_native_session_id<>'';
CREATE INDEX session_relations_child_idx ON session_relations(child_session_id);
CREATE TRIGGER session_relations_parent_delete BEFORE DELETE ON sessions BEGIN
	UPDATE session_relations SET parent_session_id=NULL,resolution_state='unresolved'
		WHERE parent_session_id=old.id;
END;
CREATE TABLE session_relation_supports (
	id INTEGER PRIMARY KEY,
	relation_id INTEGER NOT NULL REFERENCES session_relations(id) ON DELETE CASCADE,
	source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	snapshot_id INTEGER REFERENCES preserved_snapshots(id) ON DELETE CASCADE,
	parent_native_message_id TEXT NOT NULL DEFAULT '',
	provider_native_value TEXT NOT NULL DEFAULT '',
	evidence TEXT NOT NULL,
	confidence TEXT NOT NULL CHECK (confidence IN ('exact','derived','unknown')),
	rule_version INTEGER NOT NULL,
	CHECK ((source_head_id IS NOT NULL AND snapshot_id IS NULL) OR
	       (source_head_id IS NULL AND snapshot_id IS NOT NULL))
);
CREATE UNIQUE INDEX session_relation_supports_source_unique
	ON session_relation_supports(relation_id,source_head_id) WHERE source_head_id IS NOT NULL;
CREATE UNIQUE INDEX session_relation_supports_snapshot_unique
	ON session_relation_supports(relation_id,snapshot_id) WHERE snapshot_id IS NOT NULL;
CREATE INDEX session_relation_supports_source_idx ON session_relation_supports(source_head_id);
CREATE INDEX session_relation_supports_snapshot_idx ON session_relation_supports(snapshot_id);
CREATE TABLE session_thread_supports (
	id INTEGER PRIMARY KEY,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	snapshot_id INTEGER REFERENCES preserved_snapshots(id) ON DELETE CASCADE,
	thread_kind TEXT NOT NULL CHECK (thread_kind IN ('root','subagent','unknown')),
	evidence TEXT NOT NULL DEFAULT '',
	confidence TEXT NOT NULL CHECK (confidence IN ('exact','derived','unknown')),
	rule_version INTEGER NOT NULL DEFAULT 0,
	parent_native_session_id TEXT NOT NULL DEFAULT '',
	forked_from_session_id TEXT NOT NULL DEFAULT '',
	forked_from_message_id TEXT NOT NULL DEFAULT '',
	originator TEXT NOT NULL DEFAULT '',
	CHECK ((source_head_id IS NOT NULL AND snapshot_id IS NULL) OR
	       (source_head_id IS NULL AND snapshot_id IS NOT NULL))
);
CREATE UNIQUE INDEX session_thread_supports_source_unique
	ON session_thread_supports(source_head_id) WHERE source_head_id IS NOT NULL;
CREATE UNIQUE INDEX session_thread_supports_snapshot_unique
	ON session_thread_supports(snapshot_id) WHERE snapshot_id IS NOT NULL;
CREATE INDEX session_thread_supports_session_idx ON session_thread_supports(session_id);
`,
			7: `
ALTER TABLE sessions ADD COLUMN sample_key BLOB NOT NULL DEFAULT X'';
ALTER TABLE prompts ADD COLUMN sample_key BLOB NOT NULL DEFAULT X'';
CREATE INDEX sessions_sample_key_idx ON sessions(sample_key, public_id);
CREATE INDEX sessions_sample_month_idx ON sessions(COALESCE(strftime('%Y-%m', first_ts), 'unknown'), sample_key, public_id);
CREATE INDEX sessions_sample_repo_idx ON sessions(COALESCE(NULLIF(lower(repository_name), ''), 'unknown'), sample_key, public_id);
CREATE INDEX sessions_sample_thread_idx ON sessions(thread_kind, sample_key, public_id);
CREATE INDEX prompts_sample_key_idx ON prompts(sample_key, public_id);
CREATE INDEX prompts_session_sample_key_idx ON prompts(session_id, sample_key, public_id);
CREATE TABLE sample_groups (
	unit_kind TEXT NOT NULL CHECK (unit_kind IN ('prompt','session')),
	dimensions TEXT NOT NULL,
	group_values TEXT NOT NULL,
	group_key BLOB NOT NULL,
	member_count INTEGER NOT NULL CHECK (member_count > 0),
	PRIMARY KEY(unit_kind,dimensions,group_values)
);
CREATE INDEX sample_groups_key_idx ON sample_groups(unit_kind,dimensions,group_key,group_values);
CREATE TABLE sample_strata (
	unit_kind TEXT NOT NULL CHECK (unit_kind IN ('prompt','session')),
	unit_id INTEGER NOT NULL,
	dimensions TEXT NOT NULL,
	group_values TEXT NOT NULL,
	group_key BLOB NOT NULL,
	sample_key BLOB NOT NULL,
	PRIMARY KEY(unit_kind,unit_id,dimensions,group_values)
);
CREATE INDEX sample_strata_group_key_idx ON sample_strata(unit_kind,dimensions,group_key,sample_key,unit_id);
CREATE INDEX sample_strata_member_idx ON sample_strata(unit_kind,dimensions,group_values,sample_key,unit_id);
CREATE TRIGGER sample_strata_group_insert AFTER INSERT ON sample_strata
	WHEN new.dimensions IN ('month','repo','thread-kind','month,repo','month,thread-kind','repo,thread-kind','month,repo,thread-kind') BEGIN
	INSERT INTO sample_groups(unit_kind,dimensions,group_values,group_key,member_count)
		VALUES(new.unit_kind,new.dimensions,new.group_values,new.group_key,1)
		ON CONFLICT(unit_kind,dimensions,group_values) DO UPDATE SET member_count=member_count+1;
END;
CREATE TRIGGER sample_strata_group_delete AFTER DELETE ON sample_strata
	WHEN old.dimensions IN ('month','repo','thread-kind','month,repo','month,thread-kind','repo,thread-kind','month,repo,thread-kind') BEGIN
	DELETE FROM sample_groups WHERE unit_kind=old.unit_kind AND dimensions=old.dimensions AND group_values=old.group_values AND member_count=1;
	UPDATE sample_groups SET member_count=member_count-1
		WHERE unit_kind=old.unit_kind AND dimensions=old.dimensions AND group_values=old.group_values AND member_count>1;
END;
CREATE TRIGGER sample_strata_session_delete AFTER DELETE ON sessions BEGIN
	DELETE FROM sample_strata WHERE unit_kind='session' AND unit_id=old.id;
END;
CREATE TRIGGER sample_strata_prompt_delete AFTER DELETE ON prompts BEGIN
	DELETE FROM sample_strata WHERE unit_kind='prompt' AND unit_id=old.id;
END;
`,
		},
		AfterStep: func(tx sqliteutil.MigrationExecer, version int) error {
			if _, err := tx.Exec(`INSERT INTO meta(key, value) VALUES
				('extractor_version', ?), ('index_generation', '0'),
				('last_attempt_unix', '0'), ('last_complete_success_unix', '0'), ('last_run_error_count', '0')
				ON CONFLICT(key) DO NOTHING`, history.ExtractorVersion); err != nil {
				return fmt.Errorf("record history metadata: %w", err)
			}
			if version == 7 {
				if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES('sampling_ready',
					CASE WHEN EXISTS(SELECT 1 FROM sessions) OR EXISTS(SELECT 1 FROM prompts) THEN '0' ELSE '1' END)
					ON CONFLICT(key) DO NOTHING`); err != nil {
					return fmt.Errorf("record history sampling readiness: %w", err)
				}
			}
			return nil
		},
	}
}

func sampleKey(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return append([]byte(nil), digest[:8]...)
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
