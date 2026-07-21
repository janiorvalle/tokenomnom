package store

import (
	"database/sql"
	"fmt"
	"strconv"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

// Checkpoint is the independent resumable state for one provider source.
type Checkpoint struct {
	SourceID          string
	Provider          history.Provider
	Path              string
	Kind              history.LocationKind
	SourceKind        string
	Size              int64
	ModTimeUnixNano   int64
	CompleteOffset    int64
	LineCount         int64
	ContentSHA256     string
	ContentHashState  string
	PrefixFingerprint string
	TailFingerprint   string
	ExtractorState    string
	ExtractorVersion  int
	IndexedAtUnix     int64
	LastAttemptUnix   int64
	LastError         string
	Missing           bool
	Session           history.Session
}

// SourceError retains bounded diagnostics for a source that failed before a
// source head could be published.
type SourceError struct {
	Provider history.Provider
	Path     string
}

// Health is bounded non-content index status used by status and doctor.
type Health struct {
	Exists                  bool
	Path                    string
	SizeBytes               int64
	SchemaVersion           int
	ExtractorVersion        int
	Sessions                int
	SourceHeads             int
	Prompts                 int
	Occurrences             int
	LiveSources             int
	ProviderArchiveSources  int
	StaleSources            int
	ErrorSources            int
	MissingSources          int
	LastIndexUnix           int64
	LastAttemptUnix         int64
	LastCompleteSuccessUnix int64
	LastRunErrorCount       int
	IndexGeneration         int64
	InspectionError         string
}

// Checkpoints returns provider source checkpoints keyed by provider and path.
func (s *Store) Checkpoints() (map[string]Checkpoint, error) {
	rows, err := s.db.Query(`SELECT sh.public_id,sh.provider,sh.source_path,sh.source_kind,sh.size,sh.mtime_unix,
		sh.complete_offset,sh.line_count,sh.current_sha256,sh.content_hash_state,sh.prefix_fingerprint,
		sh.tail_fingerprint,sh.extractor_state,sh.extractor_version,sh.indexed_at,sh.last_attempt_unix,
		sh.last_error,sh.available,s.identity_key,COALESCE(s.native_session_id,''),s.fallback_key,
		COALESCE(s.cwd,''),COALESCE(s.repository_root,''),COALESCE(s.repository_name,''),
		COALESCE(s.repository_identity,''),COALESCE(s.branch,''),s.thread_kind,
		COALESCE(s.parent_native_session_id,''),COALESCE(s.forked_from_session_id,''),
		COALESCE(s.originator,''),COALESCE(s.evidence,''),s.confidence,s.first_ts,s.last_ts
		FROM source_heads sh JOIN sessions s ON s.id=sh.session_id ORDER BY sh.provider,sh.source_path`)
	if err != nil {
		return nil, fmt.Errorf("list history checkpoints: %w", err)
	}
	defer rows.Close()
	result := make(map[string]Checkpoint)
	for rows.Next() {
		var value Checkpoint
		var available bool
		var firstTS, lastTS sql.NullString
		if err := rows.Scan(&value.SourceID, &value.Provider, &value.Path, &value.SourceKind, &value.Size,
			&value.ModTimeUnixNano, &value.CompleteOffset, &value.LineCount, &value.ContentSHA256,
			&value.ContentHashState, &value.PrefixFingerprint, &value.TailFingerprint, &value.ExtractorState,
			&value.ExtractorVersion, &value.IndexedAtUnix, &value.LastAttemptUnix, &value.LastError, &available,
			&value.Session.IdentityKey, &value.Session.NativeSessionID, &value.Session.FallbackKey, &value.Session.CWD,
			&value.Session.RepositoryRoot, &value.Session.RepositoryName, &value.Session.RepositoryIdentity,
			&value.Session.Branch, &value.Session.ThreadKind, &value.Session.ParentNativeSessionID,
			&value.Session.ForkedFromSessionID, &value.Session.Originator, &value.Session.Evidence,
			&value.Session.Confidence, &firstTS, &lastTS); err != nil {
			return nil, fmt.Errorf("scan history checkpoint: %w", err)
		}
		value.Missing = !available
		if value.SourceKind == "codex_archive" {
			value.Kind = history.LocationProviderArchive
		} else {
			value.Kind = history.LocationProviderLive
		}
		value.Session.FirstTimestamp = parseOptionalTime(firstTS.String)
		value.Session.LastTimestamp = parseOptionalTime(lastTS.String)
		result[CheckpointKey(value.Provider, value.Path)] = value
	}
	return result, rows.Err()
}

// CheckpointKey avoids path collisions between providers.
func CheckpointKey(provider history.Provider, path string) string {
	return string(provider) + "\x00" + path
}

// UpdateCheckpointOnly publishes non-content metadata without changing the
// index generation. It is used when only an incomplete trailing line changed.
func (s *Store) UpdateCheckpointOnly(head history.SourceHead) error {
	return s.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`UPDATE source_heads SET source_kind=?,size=?,mtime_unix=?,complete_offset=?,line_count=?,
			current_sha256=?,content_hash_state=?,prefix_fingerprint=?,tail_fingerprint=?,extractor_state=?,
			last_attempt_unix=?,last_error='' WHERE provider=? AND source_path=?`,
			providerSourceKind(head.Source.Provider, head.Source.Kind), head.Size, head.ModTimeUnix, head.CompleteOffset, head.LineCount,
			head.ContentSHA256, head.ContentHashState, head.PrefixFingerprint, head.TailFingerprint, head.ExtractorState,
			time.Now().Unix(), head.Source.Provider, head.Source.Path); err != nil {
			return fmt.Errorf("update history checkpoint metadata: %w", err)
		}
		return tx.clearSourceError(head.Source.Provider, head.Source.Path)
	})
}

// MarkSourceMissing removes only mutable source occurrences and content that
// no longer has an exact reconstructible occurrence.
func (s *Store) MarkSourceMissing(provider history.Provider, path string) (bool, error) {
	changed := false
	err := s.Transaction(func(tx *Tx) error {
		var sourceID, sessionID int64
		var available bool
		if err := tx.tx.QueryRow(`SELECT id,session_id,available FROM source_heads WHERE provider=? AND source_path=?`, provider, path).Scan(&sourceID, &sessionID, &available); err != nil {
			if err == sql.ErrNoRows {
				return nil
			}
			return fmt.Errorf("find missing history source: %w", err)
		}
		if !available {
			return nil
		}
		if err := tx.tombstoneSourceOrphans(sourceID, "missing"); err != nil {
			return err
		}
		if err := tx.deleteSourceOccurrences(sourceID); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`DELETE FROM prompts WHERE occurrence_count=0`); err != nil {
			return fmt.Errorf("remove prompts from missing history source: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE source_heads SET available=0,last_attempt_unix=?,last_error='' WHERE id=?`, time.Now().Unix(), sourceID); err != nil {
			return fmt.Errorf("mark history source missing: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE locations SET available=0 WHERE source_head_id=?`, sourceID); err != nil {
			return fmt.Errorf("mark history source location missing: %w", err)
		}
		if err := tx.clearSourceError(provider, path); err != nil {
			return err
		}
		if err := tx.recomputeSessionBounds(sessionID); err != nil {
			return err
		}
		changed = true
		return tx.advanceGenerationIf(true)
	})
	return changed, err
}

// RecordSourceError retains bounded diagnostics without storing transcript text.
func (s *Store) RecordSourceError(provider history.Provider, path string, indexErr error) error {
	message := indexErr.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	return s.Transaction(func(tx *Tx) error {
		now := time.Now().Unix()
		result, err := tx.tx.Exec(`UPDATE source_heads SET last_attempt_unix=?,last_error=? WHERE provider=? AND source_path=?`, now, message, provider, path)
		if err != nil {
			return fmt.Errorf("record history source error: %w", err)
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count updated history source errors: %w", err)
		}
		if updated > 0 {
			return tx.clearSourceError(provider, path)
		}
		if _, err := tx.tx.Exec(`INSERT INTO source_errors(provider,source_path,last_attempt_unix,last_error) VALUES(?,?,?,?)
			ON CONFLICT(provider,source_path) DO UPDATE SET last_attempt_unix=excluded.last_attempt_unix,last_error=excluded.last_error`, provider, path, now, message); err != nil {
			return fmt.Errorf("retain unpublished history source error: %w", err)
		}
		return nil
	})
}

// RecordSourceChecked clears a prior source error after a successful no-op check.
func (s *Store) RecordSourceChecked(provider history.Provider, path string) error {
	return s.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`UPDATE source_heads SET last_attempt_unix=?,last_error='' WHERE provider=? AND source_path=?`, time.Now().Unix(), provider, path); err != nil {
			return fmt.Errorf("record successful history source check: %w", err)
		}
		return tx.clearSourceError(provider, path)
	})
}

// SourceErrors lists non-content diagnostics for unpublished source heads.
func (s *Store) SourceErrors() ([]SourceError, error) {
	rows, err := s.db.Query(`SELECT provider,source_path FROM source_errors ORDER BY provider,source_path`)
	if err != nil {
		return nil, fmt.Errorf("list unpublished history source errors: %w", err)
	}
	defer rows.Close()
	var values []SourceError
	for rows.Next() {
		var value SourceError
		if err := rows.Scan(&value.Provider, &value.Path); err != nil {
			return nil, fmt.Errorf("scan unpublished history source error: %w", err)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// ClearSourceError removes a diagnostic after the source succeeds or vanishes.
func (s *Store) ClearSourceError(provider history.Provider, path string) error {
	_, err := s.db.Exec(`DELETE FROM source_errors WHERE provider=? AND source_path=?`, provider, path)
	if err != nil {
		return fmt.Errorf("clear unpublished history source error: %w", err)
	}
	return nil
}

// RecordRun records an indexing attempt and only advances complete success
// when every selected source succeeded.
func (s *Store) RecordRun(attempt time.Time, errorCount int) error {
	return s.Transaction(func(tx *Tx) error {
		if err := tx.SetMeta("last_attempt_unix", strconv.FormatInt(attempt.Unix(), 10)); err != nil {
			return err
		}
		if errorCount == 0 {
			if err := tx.SetMeta("last_run_error_count", "0"); err != nil {
				return err
			}
			return tx.SetMeta("last_complete_success_unix", strconv.FormatInt(attempt.Unix(), 10))
		}
		return tx.SetMeta("last_run_error_count", strconv.Itoa(errorCount))
	})
}

// Health returns aggregate non-content index status.
func (s *Store) Health() (Health, error) {
	info, err := Inspect(s.path)
	if err != nil {
		return Health{}, err
	}
	value := Health{Exists: info.Exists, Path: info.Path, SizeBytes: info.Size, SchemaVersion: info.SchemaVersion, ExtractorVersion: info.ExtractorVersion}
	err = scanHealth(s.db.QueryRow(healthQuery, history.ExtractorVersion), &value)
	if err != nil {
		return Health{}, fmt.Errorf("read history health: %w", err)
	}
	return value, nil
}

// InspectHealth reads aggregate status without creating or migrating storage.
func InspectHealth(path string) (Health, error) {
	info, err := Inspect(path)
	if err != nil {
		return Health{}, err
	}
	value := Health{Exists: info.Exists, Path: info.Path, SizeBytes: info.Size, SchemaVersion: info.SchemaVersion, ExtractorVersion: info.ExtractorVersion}
	if !info.Exists {
		return value, nil
	}
	if info.SchemaVersion != SchemaVersion {
		value.StaleSources = 1
		return value, nil
	}
	dsn, err := fileDSN(path, true)
	if err != nil {
		return Health{}, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return Health{}, fmt.Errorf("inspect history health: %w", err)
	}
	defer db.Close()
	if err := scanHealth(db.QueryRow(healthQuery, history.ExtractorVersion), &value); err != nil {
		return Health{}, fmt.Errorf("inspect history health: %w", err)
	}
	return value, nil
}

const healthQuery = `SELECT
		(SELECT COUNT(*) FROM sessions),(SELECT COUNT(*) FROM source_heads),(SELECT COUNT(*) FROM prompts),
		(SELECT COUNT(*) FROM occurrences),(SELECT COUNT(*) FROM source_heads WHERE source_kind IN ('codex_live','claude_project')),
		(SELECT COUNT(*) FROM source_heads WHERE source_kind='codex_archive'),
		(SELECT COUNT(*) FROM source_heads WHERE extractor_version<>?),
		((SELECT COUNT(*) FROM source_heads WHERE last_error<>'')+(SELECT COUNT(*) FROM source_errors)),(SELECT COUNT(*) FROM source_heads WHERE available=0),
		COALESCE((SELECT MAX(indexed_at) FROM source_heads),0),
		COALESCE((SELECT value FROM meta WHERE key='last_attempt_unix'),'0'),
		COALESCE((SELECT value FROM meta WHERE key='last_complete_success_unix'),'0'),
		COALESCE((SELECT value FROM meta WHERE key='last_run_error_count'),'0'),
		COALESCE((SELECT value FROM meta WHERE key='index_generation'),'0')`

type rowScanner interface {
	Scan(...any) error
}

func scanHealth(row rowScanner, value *Health) error {
	return row.Scan(
		&value.Sessions, &value.SourceHeads, &value.Prompts, &value.Occurrences, &value.LiveSources,
		&value.ProviderArchiveSources, &value.StaleSources, &value.ErrorSources, &value.MissingSources,
		&value.LastIndexUnix, &value.LastAttemptUnix, &value.LastCompleteSuccessUnix, &value.LastRunErrorCount, &value.IndexGeneration)
}

func parseOptionalTime(value string) *time.Time {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &parsed
}
