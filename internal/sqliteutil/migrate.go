// Package sqliteutil shares SQLite setup and ordered schema migration support.
package sqliteutil

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MigrationPlan describes direct fresh creation plus ordered upgrades.
type MigrationPlan struct {
	Label     string
	Current   int
	FreshSQL  string
	Steps     map[int]string
	AfterStep func(MigrationExecer, int) error
}

// MigrationExecer is the transaction-scoped SQL surface exposed to hooks.
type MigrationExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

type connectionExecer struct {
	context context.Context
	conn    *sql.Conn
}

func (e connectionExecer) Exec(query string, args ...any) (sql.Result, error) {
	return e.conn.ExecContext(e.context, query, args...)
}

var errMigrationChanged = errors.New("schema version changed during migration")

// EnableWAL enables WAL with bounded retry for concurrent initial opens.
func EnableWAL(database *sql.DB, label string) error {
	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		var mode string
		lastErr = database.QueryRow(`PRAGMA journal_mode = WAL;`).Scan(&mode)
		if lastErr == nil {
			if strings.EqualFold(mode, "wal") {
				return nil
			}
			return fmt.Errorf("enable %s SQLite WAL mode: SQLite reported %q", label, mode)
		}
		message := strings.ToLower(lastErr.Error())
		if !strings.Contains(message, "database is locked") && !strings.Contains(message, "sqlite_busy") {
			return fmt.Errorf("enable %s SQLite WAL mode: %w", label, lastErr)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("enable %s SQLite WAL mode: %w", label, lastErr)
}

// Migrate creates a fresh current schema directly or applies every ordered
// step after the persisted version. Each step and its version update commit in
// one transaction.
func Migrate(db *sql.DB, plan MigrationPlan) error {
	for {
		version, _, err := SchemaVersion(db, plan.Label)
		if err != nil {
			return err
		}
		if version > plan.Current {
			return fmt.Errorf("unsupported %s schema %d (expected %d)", plan.Label, version, plan.Current)
		}
		if version == plan.Current {
			return nil
		}
		if version == 0 {
			err := ApplyStep(db, plan, plan.Current, plan.FreshSQL)
			if errors.Is(err, errMigrationChanged) {
				continue
			}
			return err
		}
		next := version + 1
		ddl, ok := plan.Steps[next]
		if !ok {
			return fmt.Errorf("no %s migration from schema %d", plan.Label, version)
		}
		if err := ApplyStep(db, plan, next, ddl); err != nil {
			if errors.Is(err, errMigrationChanged) {
				continue
			}
			return fmt.Errorf("migrate %s schema %d to %d: %w", plan.Label, version, next, err)
		}
	}
}

// SchemaVersion reads meta.schema_version when present.
func SchemaVersion(db *sql.DB, label string) (int, bool, error) {
	var lastErr error
	for attempt := 0; attempt < 50; attempt++ {
		version, exists, err := schemaVersionOnce(db, label)
		if err == nil {
			return version, exists, nil
		}
		lastErr = err
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "database is locked") && !strings.Contains(message, "sqlite_busy") {
			return 0, exists, err
		}
		time.Sleep(20 * time.Millisecond)
	}
	return 0, false, lastErr
}

func schemaVersionOnce(db *sql.DB, label string) (int, bool, error) {
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='meta')`).Scan(&exists); err != nil {
		return 0, false, fmt.Errorf("inspect %s schema: %w", label, err)
	}
	if !exists {
		return 0, false, nil
	}
	var version int
	err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, true, nil
	}
	if err != nil {
		return 0, true, fmt.Errorf("read %s schema version: %w", label, err)
	}
	return version, true, nil
}

// ApplyStep applies one migration and records its version atomically.
func ApplyStep(db *sql.DB, plan MigrationPlan, version int, ddl string) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("reserve %s schema connection: %w", plan.Label, err)
	}
	defer conn.Close()
	var began bool
	for attempt := 0; attempt < 50; attempt++ {
		_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
		if err == nil {
			began = true
			break
		}
		message := strings.ToLower(err.Error())
		if !strings.Contains(message, "database is locked") && !strings.Contains(message, "sqlite_busy") {
			return fmt.Errorf("begin %s schema transaction: %w", plan.Label, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !began {
		return fmt.Errorf("begin %s schema transaction: %w", plan.Label, err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	current, err := schemaVersionOnConnection(ctx, conn, plan.Label)
	if err != nil {
		return err
	}
	if current > version {
		return fmt.Errorf("%w: %s is now schema %d", errMigrationChanged, plan.Label, current)
	}
	if current == version {
		return nil
	}
	if current != 0 && current != version-1 {
		return fmt.Errorf("%w: %s is now schema %d", errMigrationChanged, plan.Label, current)
	}
	execer := connectionExecer{context: ctx, conn: conn}
	if _, err := execer.Exec(ddl); err != nil {
		return fmt.Errorf("apply %s schema DDL: %w", plan.Label, err)
	}
	if _, err := execer.Exec(`INSERT INTO meta(key, value) VALUES ('schema_version', ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version); err != nil {
		return fmt.Errorf("record %s schema version: %w", plan.Label, err)
	}
	if plan.AfterStep != nil {
		if err := plan.AfterStep(execer, version); err != nil {
			return err
		}
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit %s schema transaction: %w", plan.Label, err)
	}
	return nil
}

func schemaVersionOnConnection(ctx context.Context, conn *sql.Conn, label string) (int, error) {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='meta')`).Scan(&exists); err != nil {
		return 0, fmt.Errorf("inspect %s schema: %w", label, err)
	}
	if !exists {
		return 0, nil
	}
	var version int
	err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s schema version: %w", label, err)
	}
	return version, nil
}
