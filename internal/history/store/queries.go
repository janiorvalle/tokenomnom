package store

import (
	"errors"
	"fmt"
)

const (
	// DefaultSessionCostPageSize keeps an unqualified cost request small and
	// resumable while still allowing callers to request larger pages.
	DefaultSessionCostPageSize = 20
	// MaxSessionCostPageSize keeps one cost request bounded. A 1,200-session
	// index is therefore at most 12 pages before transcript parsing begins.
	MaxSessionCostPageSize = 100
	// MaxSessionCostCandidates limits fallback transcript reads per session.
	MaxSessionCostCandidates = 3
)

// SessionCostQuery selects either one session or a bounded catalog page. The
// returned locations are exact indexed bytes; callers still decide how to
// parse and price those bytes.
type SessionCostQuery struct {
	Catalog   CatalogQuery
	SessionID string
}

// SessionCostSession joins one catalog session to its preferred exact raw
// locations. Candidates are deliberately excluded from JSON serialization so
// a caller cannot accidentally expose local paths in an agent response.
type SessionCostSession struct {
	CatalogSession
	Candidates           []RawCandidate `json:"-"`
	CandidateCount       int            `json:"-"`
	CandidatesTruncated  bool           `json:"-"`
	MissingSourceSettled bool           `json:"missing_source_settled,omitempty"`
}

// SessionCostPage is the bounded history-index half of the session-cost
// query. The CLI adds provider-neutral usage events and pricing fields.
type SessionCostPage struct {
	Sessions   []SessionCostSession `json:"sessions"`
	Page       PageMetadata         `json:"page"`
	Coverage   CatalogCoverage      `json:"coverage"`
	Warnings   []string             `json:"-"`
	Generation int64                `json:"index_generation"`
}

// ListSessionCostSources returns logical sessions and a bounded set of exact
// transcript locations for each one. The page is generation-bound just like
// history list, so a caller can safely continue it with the returned cursor.
func (s *Store) ListSessionCostSources(query SessionCostQuery) (SessionCostPage, error) {
	if query.SessionID != "" {
		return s.singleSessionCostSources(query.SessionID)
	}

	catalogQuery := query.Catalog
	if catalogQuery.Limit < 0 || catalogQuery.Limit > MaxSessionCostPageSize {
		return SessionCostPage{}, fmt.Errorf("history session cost limit must be between 1 and %d", MaxSessionCostPageSize)
	}
	if catalogQuery.Cursor != "" && catalogQuery.Limit == 0 {
		cursor, err := decodeCatalogCursor(catalogQuery.Cursor)
		if err != nil {
			return SessionCostPage{}, err
		}
		if cursor.Limit > MaxSessionCostPageSize {
			return SessionCostPage{}, fmt.Errorf("history session cost cursor was created with a limit above %d; start a new page", MaxSessionCostPageSize)
		}
		catalogQuery.Limit = cursor.Limit
	}
	if catalogQuery.Limit == 0 {
		catalogQuery.Limit = DefaultSessionCostPageSize
	}

	catalogPage, err := s.ListCatalog(catalogQuery)
	if err != nil {
		return SessionCostPage{}, err
	}
	page := SessionCostPage{
		Sessions:   make([]SessionCostSession, 0, len(catalogPage.Sessions)),
		Page:       PageMetadata{Limit: catalogPage.Limit, HasMore: catalogPage.HasMore, NextCursor: catalogPage.NextCursor},
		Coverage:   catalogPage.Coverage,
		Warnings:   append([]string{}, catalogPage.Warnings...),
		Generation: catalogPage.Generation,
	}
	for _, session := range catalogPage.Sessions {
		value, warning, err := s.sessionCostSource(session)
		if err != nil {
			return SessionCostPage{}, err
		}
		page.Sessions = append(page.Sessions, value)
		if warning != "" {
			page.Warnings = append(page.Warnings, warning)
		}
	}
	if err := s.requireSessionCostGeneration(catalogPage.Generation); err != nil {
		return SessionCostPage{}, err
	}
	return page, nil
}

func (s *Store) singleSessionCostSources(publicID string) (SessionCostPage, error) {
	generation, err := s.indexGeneration()
	if err != nil {
		return SessionCostPage{}, err
	}
	session, err := s.GetSession(publicID)
	if err != nil {
		return SessionCostPage{}, err
	}
	value, warning, err := s.sessionCostSource(session)
	if err != nil {
		return SessionCostPage{}, err
	}
	warnings := []string{}
	if warning != "" {
		warnings = append(warnings, warning)
	}
	if err := s.requireSessionCostGeneration(generation); err != nil {
		return SessionCostPage{}, err
	}
	return SessionCostPage{
		Sessions:   []SessionCostSession{value},
		Page:       PageMetadata{Limit: 1},
		Coverage:   CatalogCoverage{},
		Warnings:   warnings,
		Generation: generation,
	}, nil
}

func (s *Store) requireSessionCostGeneration(expected int64) error {
	current, err := s.indexGeneration()
	if err != nil {
		return err
	}
	if current != expected {
		return errors.New("history index changed while collecting session cost sources; retry the query")
	}
	return nil
}

func (s *Store) sessionCostSource(session CatalogSession) (SessionCostSession, string, error) {
	candidates, err := s.RawCandidates(session.SessionID, "")
	if err != nil {
		if errors.Is(err, ErrNoAvailableRawLocation) {
			settled, settlementErr := s.missingSourceSettlement(session.SessionID)
			if settlementErr != nil {
				return SessionCostSession{}, "", settlementErr
			}
			value := SessionCostSession{CatalogSession: session, Candidates: []RawCandidate{}, MissingSourceSettled: settled}
			if settled {
				return value, "", nil
			}
			return value, fmt.Sprintf("session %s has no available exact transcript location; cost attribution will be unavailable", session.SessionID), nil
		}
		return SessionCostSession{}, "", err
	}
	value := SessionCostSession{CatalogSession: session, CandidateCount: len(candidates)}
	if len(candidates) > MaxSessionCostCandidates {
		value.CandidatesTruncated = true
		candidates = candidates[:MaxSessionCostCandidates]
	}
	value.Candidates = append([]RawCandidate{}, candidates...)
	if value.CandidatesTruncated {
		return value, fmt.Sprintf("session %s has %d exact transcript locations; this bounded query will try the preferred first %d", session.SessionID, value.CandidateCount, MaxSessionCostCandidates), nil
	}
	return value, "", nil
}

func (s *Store) missingSourceSettlement(publicID string) (bool, error) {
	var settled, unsettled int
	err := s.runner.QueryRow(`SELECT
		COALESCE(SUM(CASE WHEN sh.available=0 AND sh.settled_missing=1 THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN sh.available=0 AND sh.settled_missing=0 THEN 1 ELSE 0 END),0)
		FROM sessions session LEFT JOIN source_heads sh ON sh.session_id=session.id
		WHERE session.public_id=?
		AND NOT EXISTS (
			SELECT 1 FROM source_heads unavailable_head
			WHERE unavailable_head.session_id=session.id AND unavailable_head.available=1
				AND (unavailable_head.current_sha256='' OR unavailable_head.complete_offset<>unavailable_head.size OR NOT EXISTS (
					SELECT 1 FROM locations available_location
					WHERE available_location.source_head_id=unavailable_head.id AND available_location.available=1
				))
		)
		AND NOT EXISTS (
			SELECT 1 FROM preserved_snapshots unavailable_snapshot
			WHERE unavailable_snapshot.session_id=session.id AND NOT EXISTS (
				SELECT 1 FROM locations available_snapshot_location
				WHERE available_snapshot_location.snapshot_id=unavailable_snapshot.id AND available_snapshot_location.available=1
			)
		)`, publicID).Scan(&settled, &unsettled)
	if err != nil {
		return false, fmt.Errorf("read missing history source settlement: %w", err)
	}
	return settled > 0 && unsettled == 0, nil
}
