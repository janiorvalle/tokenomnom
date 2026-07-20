package sqliteutil

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateFreshAndOrdered(t *testing.T) {
	plan := MigrationPlan{Label: "test store", Current: 3, FreshSQL: `CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); CREATE TABLE current(value TEXT);`, Steps: map[int]string{2: `ALTER TABLE old ADD COLUMN two TEXT`, 3: `ALTER TABLE old ADD COLUMN three TEXT`}}
	fresh := openDB(t, filepath.Join(t.TempDir(), "fresh.db"))
	if err := Migrate(fresh, plan); err != nil {
		t.Fatal(err)
	}
	version, _, _ := SchemaVersion(fresh, plan.Label)
	if version != 3 {
		t.Fatalf("fresh version = %d", version)
	}
	fresh.Close()

	ordered := openDB(t, filepath.Join(t.TempDir(), "ordered.db"))
	if _, err := ordered.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); INSERT INTO meta VALUES('schema_version','1'); CREATE TABLE old(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	var applied []int
	plan.AfterStep = func(_ MigrationExecer, version int) error { applied = append(applied, version); return nil }
	if err := Migrate(ordered, plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(applied, []int{2, 3}) {
		t.Fatalf("steps = %v", applied)
	}
	ordered.Close()
}

func TestMigrateConcurrentUpgradeAppliesEachStepOnce(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "concurrent.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); INSERT INTO meta VALUES('schema_version','1'); CREATE TABLE old(value TEXT)`); err != nil {
		t.Fatal(err)
	}
	plan := MigrationPlan{Label: "test store", Current: 3, Steps: map[int]string{2: `ALTER TABLE old ADD COLUMN two TEXT`, 3: `ALTER TABLE old ADD COLUMN three TEXT`}}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsFound <- Migrate(db, plan)
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	version, _, err := SchemaVersion(db, plan.Label)
	if err != nil || version != plan.Current {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}

func TestApplyStepReportsConcurrentlyInstalledFutureSchema(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "future.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT); INSERT INTO meta VALUES('schema_version','4')`); err != nil {
		t.Fatal(err)
	}
	plan := MigrationPlan{Label: "test store", Current: 3}
	if err := ApplyStep(db, plan, 3, `CREATE TABLE stale(value TEXT)`); !errors.Is(err, errMigrationChanged) {
		t.Fatalf("future schema step error = %v", err)
	}
}

func TestEnableWALVerifiesReportedMode(t *testing.T) {
	file := openDB(t, filepath.Join(t.TempDir(), "wal.db"))
	defer file.Close()
	if err := EnableWAL(file, "test store"); err != nil {
		t.Fatal(err)
	}
	memory := openDB(t, ":memory:")
	defer memory.Close()
	if err := EnableWAL(memory, "memory store"); err == nil {
		t.Fatal("in-memory SQLite falsely reported WAL success")
	}
}

func openDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
