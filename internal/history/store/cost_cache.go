package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SessionCostCacheKey identifies one exact indexed transcript and the pricing
// context used to produce its cached attribution.
type SessionCostCacheKey struct {
	SessionID          string
	LocationID         string
	ContentSHA256      string
	ContentSize        int64
	PricingFingerprint string
	CandidateContext   string
	WindowSince        string
	WindowUntil        string
}

func (key SessionCostCacheKey) validate() error {
	if key.SessionID == "" {
		return errors.New("history session cost cache session ID is required")
	}
	if key.ContentSHA256 == "" {
		return errors.New("history session cost cache content digest is required")
	}
	if key.ContentSize < 0 {
		return fmt.Errorf("history session cost cache content size %d is invalid", key.ContentSize)
	}
	if key.PricingFingerprint == "" {
		return errors.New("history session cost cache pricing fingerprint is required")
	}
	if key.CandidateContext == "" {
		return errors.New("history session cost cache candidate context is required")
	}
	return nil
}

// GetSessionCostCache returns a previously verified attribution payload.
func (s *Store) GetSessionCostCache(key SessionCostCacheKey) ([]byte, bool, error) {
	if err := key.validate(); err != nil {
		return nil, false, err
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	var payload []byte
	err := s.runner.QueryRow(`SELECT payload FROM session_cost_cache
		WHERE session_id=? AND location_id=? AND content_sha256=? AND content_size=?
			AND pricing_fingerprint=? AND candidate_context=? AND window_since=? AND window_until=?`,
		key.SessionID, key.LocationID, key.ContentSHA256, key.ContentSize,
		key.PricingFingerprint, key.CandidateContext, key.WindowSince, key.WindowUntil).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read history session cost cache: %w", err)
	}
	return append([]byte(nil), payload...), true, nil
}

// PutSessionCostCache persists a verified attribution payload. Read-only
// history consumers use a short-lived write connection so cache persistence
// does not change their consistent catalog snapshot.
func (s *Store) PutSessionCostCache(key SessionCostCacheKey, payload []byte) error {
	if err := key.validate(); err != nil {
		return err
	}
	if len(payload) == 0 {
		return errors.New("history session cost cache payload is required")
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	runner, err := s.cacheWriterLocked()
	if err != nil {
		return err
	}
	if _, err := runner.Exec(`INSERT INTO session_cost_cache(
		session_id,location_id,content_sha256,content_size,pricing_fingerprint,candidate_context,window_since,window_until,payload,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(session_id,location_id,content_sha256,content_size,pricing_fingerprint,candidate_context,window_since,window_until)
		DO UPDATE SET payload=excluded.payload,created_at=excluded.created_at`,
		key.SessionID, key.LocationID, key.ContentSHA256, key.ContentSize,
		key.PricingFingerprint, key.CandidateContext, key.WindowSince, key.WindowUntil, payload, time.Now().UnixNano()); err != nil {
		return fmt.Errorf("write history session cost cache: %w", err)
	}
	if err := pruneSessionCostCache(runner); err != nil {
		return err
	}
	return nil
}

const (
	maxSessionCostCacheRowsPerSession = 8
	maxSessionCostCacheRows           = 50_000
)

func pruneSessionCostCache(runner sqlRunner) error {
	if _, err := runner.Exec(`DELETE FROM session_cost_cache AS cache
		WHERE NOT EXISTS (SELECT 1 FROM sessions s WHERE s.public_id=cache.session_id)
		   OR NOT EXISTS (
				SELECT 1 FROM source_heads sh WHERE sh.public_id=cache.location_id
				UNION ALL
				SELECT 1 FROM preserved_snapshots ps
					WHERE cache.location_id=ps.public_id OR cache.location_id LIKE 'vault:' || ps.public_id || ':%'
			)`); err != nil {
		return fmt.Errorf("prune orphaned history session cost cache rows: %w", err)
	}
	if _, err := runner.Exec(`DELETE FROM session_cost_cache
		WHERE rowid IN (
			SELECT rowid FROM (
				SELECT rowid, ROW_NUMBER() OVER (
					PARTITION BY session_id ORDER BY created_at DESC, rowid DESC
				) AS cache_rank
				FROM session_cost_cache
			) WHERE cache_rank > ?
		)`, maxSessionCostCacheRowsPerSession); err != nil {
		return fmt.Errorf("prune superseded history session cost cache rows: %w", err)
	}
	if _, err := runner.Exec(`DELETE FROM session_cost_cache
		WHERE rowid IN (
			SELECT rowid FROM session_cost_cache
			ORDER BY created_at DESC, rowid DESC
			LIMIT -1 OFFSET ?
		)`, maxSessionCostCacheRows); err != nil {
		return fmt.Errorf("bound history session cost cache rows: %w", err)
	}
	return nil
}

func (s *Store) cacheWriterLocked() (sqlRunner, error) {
	if s.readTx == nil {
		return s.db, nil
	}
	if s.cacheDB != nil {
		return s.cacheDB, nil
	}
	dsn, err := fileDSN(s.path, false)
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open history session cost cache writer: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	// Cache writes are optional; do not make an interactive load wait behind a
	// long history-index transaction.
	if _, err := database.Exec(`PRAGMA busy_timeout = 100;`); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("set history session cost cache busy timeout: %w", err)
	}
	s.cacheDB = database
	return database, nil
}
