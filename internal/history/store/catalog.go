package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

const (
	maxPreviewBytes = 512
	maxPreviewLines = 4
)

// CatalogSource selects location evidence for history list.
type CatalogSource string

const (
	CatalogSourceAny             CatalogSource = "any"
	CatalogSourceProvider        CatalogSource = "provider"
	CatalogSourceProviderLive    CatalogSource = "provider-live"
	CatalogSourceProviderArchive CatalogSource = "provider-archive"
	CatalogSourceVault           CatalogSource = "vault"
)

// CatalogQuery describes one bounded logical-session page.
type CatalogQuery struct {
	Provider   history.Provider
	Since      *time.Time
	Until      *time.Time
	CWD        string
	Repo       string
	Branch     string
	Source     CatalogSource
	ThreadKind string
	Limit      int
	Cursor     string
}

// FieldCoverage discloses known versus unknown metadata in the selected
// provider/date/source/cwd population before repo or branch filtering.
type FieldCoverage struct {
	Known   int `json:"known"`
	Unknown int `json:"unknown"`
}

// CatalogCoverage reports provider-uneven repository metadata coverage.
type CatalogCoverage struct {
	Repository FieldCoverage      `json:"repository"`
	Branch     FieldCoverage      `json:"branch"`
	ThreadKind ThreadKindCoverage `json:"thread_kind"`
}

// ThreadKindCoverage discloses explicit root, subagent, and unknown coverage.
type ThreadKindCoverage struct {
	Root     int `json:"root"`
	Subagent int `json:"subagent"`
	Unknown  int `json:"unknown"`
}

// Availability summarizes exact indexed location evidence.
type Availability struct {
	ProviderLive        int  `json:"provider_live"`
	ProviderArchive     int  `json:"provider_archive"`
	Vault               int  `json:"vault"`
	Unavailable         bool `json:"unavailable"`
	ExactLiveAndVaulted bool `json:"exact_live_and_vaulted"`
}

// CatalogSession is one logical-session result, never one row per snapshot or
// prompt occurrence.
type CatalogSession struct {
	databaseID               int64
	sortTimestamp            string
	SessionID                string                `json:"session_id"`
	Provider                 history.Provider      `json:"provider"`
	NativeSessionID          string                `json:"native_session_id,omitempty"`
	FirstTimestamp           *string               `json:"first_timestamp"`
	LastTimestamp            *string               `json:"last_timestamp"`
	CWD                      string                `json:"cwd,omitempty"`
	RepositoryName           *string               `json:"repository_name"`
	Branch                   *string               `json:"branch"`
	ThreadKind               history.ThreadKind    `json:"thread_kind"`
	ThreadEvidence           string                `json:"thread_evidence"`
	ThreadConfidence         history.Confidence    `json:"thread_confidence"`
	ThreadRuleVersion        int                   `json:"thread_rule_version"`
	Originator               string                `json:"originator,omitempty"`
	Relationships            []SessionRelationship `json:"relationships"`
	RelationshipsTruncated   bool                  `json:"relationships_truncated"`
	SourceHeadIDs            []string              `json:"source_head_ids"`
	SourceHeadCount          int                   `json:"source_head_count"`
	PreservedSnapshotIDs     []string              `json:"preserved_snapshot_ids"`
	PreservedSnapshotCount   int                   `json:"preserved_snapshot_count"`
	LogicalPromptCount       int                   `json:"logical_prompt_count"`
	OccurrenceCount          int                   `json:"occurrence_count"`
	Availability             Availability          `json:"availability"`
	PreferredRetrievalSource string                `json:"preferred_retrieval_source"`
	Preview                  string                `json:"preview"`
}

// CatalogPage is one generation-bound keyset page.
type CatalogPage struct {
	Sessions   []CatalogSession `json:"sessions"`
	Coverage   CatalogCoverage  `json:"coverage"`
	Warnings   []string         `json:"-"`
	Limit      int              `json:"limit"`
	HasMore    bool             `json:"has_more"`
	NextCursor string           `json:"next_cursor"`
	Generation int64            `json:"index_generation"`
}

type catalogCursor struct {
	Version    int           `json:"v"`
	Generation int64         `json:"generation"`
	Provider   string        `json:"provider"`
	Since      string        `json:"since"`
	Until      string        `json:"until"`
	CWD        string        `json:"cwd"`
	Repo       string        `json:"repo"`
	Branch     string        `json:"branch"`
	Source     CatalogSource `json:"source"`
	ThreadKind string        `json:"thread_kind"`
	Limit      int           `json:"limit"`
	Unknown    bool          `json:"unknown"`
	Timestamp  string        `json:"timestamp"`
	SessionID  string        `json:"session_id"`
}

// ListCatalog returns current logical sessions with stable IDs and aggregated
// availability. Older preserved snapshots remain represented in counts and
// prompt occurrences without duplicating session rows.
func (s *Store) ListCatalog(query CatalogQuery) (CatalogPage, error) {
	if query.Source == "" {
		query.Source = CatalogSourceAny
	}
	if !validCatalogSource(query.Source) {
		return CatalogPage{}, fmt.Errorf("invalid history source %q", query.Source)
	}
	query.ThreadKind = normalizedThreadKindFilter(query.ThreadKind)
	if !validThreadKindFilter(query.ThreadKind) {
		return CatalogPage{}, fmt.Errorf("invalid history thread kind %q", query.ThreadKind)
	}
	generation, err := s.indexGeneration()
	if err != nil {
		return CatalogPage{}, err
	}
	var cursor catalogCursor
	if query.Cursor != "" {
		cursor, err = decodeCatalogCursor(query.Cursor)
		if err != nil {
			return CatalogPage{}, err
		}
		if err := cursor.matches(query, generation); err != nil {
			return CatalogPage{}, err
		}
		if query.Limit == 0 {
			query.Limit = cursor.Limit
		}
	}
	if query.Limit == 0 {
		query.Limit = 100
	}
	if query.Limit < 1 || query.Limit > 500 {
		return CatalogPage{}, errors.New("history list limit must be between 1 and 500")
	}

	coverage, err := s.catalogCoverage(query)
	if err != nil {
		return CatalogPage{}, err
	}
	warnings := []string{}
	if query.Repo != "" && coverage.Repository.Unknown > 0 {
		warnings = append(warnings, fmt.Sprintf("--repo excluded %d session(s) with unknown repository metadata; repository coverage is Codex-complete and Claude-partial", coverage.Repository.Unknown))
	}
	if query.Branch != "" && coverage.Branch.Unknown > 0 {
		warnings = append(warnings, fmt.Sprintf("--branch excluded %d session(s) with unknown branch metadata; branch coverage is Codex-complete and Claude-partial", coverage.Branch.Unknown))
	}

	where, args := catalogWhere(query, true)
	rawSortExpr := "COALESCE(NULLIF(s.last_ts,''),NULLIF(s.first_ts,''),'')"
	sortExpr := sqliteTimestampKey(rawSortExpr)
	if query.Cursor != "" {
		if cursor.Unknown {
			where = append(where, sortExpr+"='' AND s.public_id>?")
			args = append(args, cursor.SessionID)
		} else {
			where = append(where, "("+sortExpr+"='' OR "+sortExpr+"<? OR ("+sortExpr+"=? AND s.public_id>?))")
			args = append(args, cursor.Timestamp, cursor.Timestamp, cursor.SessionID)
		}
	}
	args = append(args, query.Limit+1)
	statement := catalogSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY (" + sortExpr + "='') ASC," + sortExpr + " DESC,s.public_id ASC LIMIT ?"
	rows, err := s.db.Query(statement, args...)
	if err != nil {
		return CatalogPage{}, fmt.Errorf("list history catalog: %w", err)
	}
	defer rows.Close()
	values := make([]CatalogSession, 0, query.Limit+1)
	for rows.Next() {
		value, err := scanCatalogSession(rows)
		if err != nil {
			return CatalogPage{}, fmt.Errorf("scan history catalog: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return CatalogPage{}, err
	}
	if err := rows.Close(); err != nil {
		return CatalogPage{}, err
	}
	for index := range values {
		values[index].Relationships, values[index].RelationshipsTruncated, err = s.sessionRelationships(values[index].databaseID)
		if err != nil {
			return CatalogPage{}, err
		}
	}
	page := CatalogPage{Sessions: values, Coverage: coverage, Warnings: warnings, Limit: query.Limit, Generation: generation}
	if len(page.Sessions) > query.Limit {
		page.Sessions = page.Sessions[:query.Limit]
		page.HasMore = true
		lastValue := page.Sessions[len(page.Sessions)-1]
		page.NextCursor, err = encodeCatalogCursor(newCatalogCursor(query, generation, lastValue.sortTimestamp, lastValue.SessionID))
		if err != nil {
			return CatalogPage{}, err
		}
	}
	if page.Sessions == nil {
		page.Sessions = []CatalogSession{}
	}
	return page, nil
}

func scanCatalogSession(row rowScanner) (CatalogSession, error) {
	var value CatalogSession
	var native, first, last, cwd, repo, branch, threadEvidence, originator, sourceIDs, snapshotIDs, preview sql.NullString
	if err := row.Scan(
		&value.databaseID, &value.SessionID, &value.Provider, &native, &first, &last, &cwd, &repo, &branch,
		&value.ThreadKind, &threadEvidence, &value.ThreadConfidence, &value.ThreadRuleVersion, &originator,
		&sourceIDs, &value.SourceHeadCount, &snapshotIDs, &value.PreservedSnapshotCount,
		&value.LogicalPromptCount, &value.OccurrenceCount, &value.Availability.ProviderLive,
		&value.Availability.ProviderArchive, &value.Availability.Vault, &value.Availability.ExactLiveAndVaulted,
		&value.PreferredRetrievalSource, &preview, &value.sortTimestamp,
	); err != nil {
		return CatalogSession{}, err
	}
	value.NativeSessionID, value.CWD = native.String, cwd.String
	value.RepositoryName, value.Branch = optionalCatalogString(repo), optionalCatalogString(branch)
	value.ThreadEvidence, value.Originator = threadEvidence.String, originator.String
	value.Relationships = []SessionRelationship{}
	value.FirstTimestamp, value.LastTimestamp = optionalCatalogString(first), optionalCatalogString(last)
	value.SourceHeadIDs, value.PreservedSnapshotIDs = splitCatalogIDs(sourceIDs.String), splitCatalogIDs(snapshotIDs.String)
	value.Preview = boundPreview(preview.String)
	value.Availability.Unavailable = value.Availability.ProviderLive+value.Availability.ProviderArchive+value.Availability.Vault == 0
	return value, nil
}

var catalogSelect = `SELECT s.id,s.public_id,s.provider,s.native_session_id,s.first_ts,s.last_ts,s.cwd,s.repository_name,s.branch,
	s.thread_kind,s.thread_evidence,s.thread_confidence,s.thread_rule_version,s.originator,
	(SELECT group_concat(public_id) FROM (SELECT public_id FROM source_heads WHERE session_id=s.id ORDER BY public_id LIMIT 100)),
	(SELECT COUNT(*) FROM source_heads WHERE session_id=s.id),
	(SELECT group_concat(public_id) FROM (SELECT public_id FROM preserved_snapshots WHERE session_id=s.id ORDER BY public_id LIMIT 100)),
	(SELECT COUNT(*) FROM preserved_snapshots WHERE session_id=s.id),
	(SELECT COUNT(*) FROM prompts p WHERE p.session_id=s.id AND p.role='user' AND EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1)),
	(SELECT COUNT(*) FROM occurrences o JOIN prompts p ON p.id=o.prompt_id JOIN locations l ON l.id=o.location_id WHERE p.session_id=s.id AND p.role='user' AND l.available=1),
	(SELECT COUNT(*) FROM source_heads WHERE session_id=s.id AND available=1 AND source_kind IN ('codex_live','claude_project')),
	(SELECT COUNT(*) FROM source_heads WHERE session_id=s.id AND available=1 AND source_kind='codex_archive'),
	(SELECT COUNT(*) FROM locations l JOIN preserved_snapshots ps ON ps.id=l.snapshot_id WHERE ps.session_id=s.id AND l.available=1),
	EXISTS(SELECT 1 FROM source_heads sh JOIN preserved_snapshots ps ON ps.session_id=sh.session_id JOIN locations l ON l.snapshot_id=ps.id AND l.available=1 WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind IN ('codex_live','claude_project') AND sh.current_sha256<>'' AND sh.complete_offset=sh.size AND sh.current_sha256=ps.content_sha256),
	CASE
		WHEN EXISTS(SELECT 1 FROM source_heads WHERE session_id=s.id AND available=1 AND source_kind IN ('codex_live','claude_project') AND current_sha256<>'' AND complete_offset=size) THEN 'provider-live'
		WHEN EXISTS(SELECT 1 FROM source_heads WHERE session_id=s.id AND available=1 AND source_kind='codex_archive' AND current_sha256<>'' AND complete_offset=size) THEN 'provider-archive'
		WHEN EXISTS(SELECT 1 FROM locations l JOIN preserved_snapshots ps ON ps.id=l.snapshot_id WHERE ps.session_id=s.id AND l.available=1) THEN 'vault'
		ELSE 'unavailable'
	END,
	(SELECT substr(p.clean_text,1,2048) FROM prompts p WHERE p.session_id=s.id AND p.searchable=1 AND p.role='user' AND EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1) ORDER BY (p.timestamp IS NULL),` + sqliteTimestampKey("p.timestamp") + `,p.id LIMIT 1),
	` + sqliteTimestampKey("COALESCE(NULLIF(s.last_ts,''),NULLIF(s.first_ts,''),'')") + `
	FROM sessions s`

func catalogWhere(query CatalogQuery, includeMetadataFilters bool) ([]string, []any) {
	where := []string{"1=1"}
	args := []any{}
	if query.Provider != "" {
		where = append(where, "s.provider=?")
		args = append(args, query.Provider)
	}
	if query.Since != nil {
		where = append(where, "s.last_ts IS NOT NULL AND s.last_ts<>'' AND "+sqliteTimestampKey("s.last_ts")+">=?")
		args = append(args, fixedCatalogTimestamp(*query.Since))
	}
	if query.Until != nil {
		where = append(where, "s.first_ts IS NOT NULL AND s.first_ts<>'' AND "+sqliteTimestampKey("s.first_ts")+"<=?")
		args = append(args, fixedCatalogTimestamp(*query.Until))
	}
	if query.CWD != "" {
		where = append(where, "s.cwd=?")
		args = append(args, query.CWD)
	}
	if includeMetadataFilters && query.Repo != "" {
		where = append(where, "s.repository_name=?")
		args = append(args, query.Repo)
	}
	if includeMetadataFilters && query.Branch != "" {
		where = append(where, "s.branch=?")
		args = append(args, query.Branch)
	}
	if query.ThreadKind != "" && query.ThreadKind != "all" {
		where = append(where, "s.thread_kind=?")
		args = append(args, query.ThreadKind)
	}
	switch query.Source {
	case CatalogSourceProvider:
		where = append(where, "EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1)")
	case CatalogSourceProviderLive:
		where = append(where, "EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind IN ('codex_live','claude_project'))")
	case CatalogSourceProviderArchive:
		where = append(where, "EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind='codex_archive')")
	case CatalogSourceVault:
		where = append(where, "EXISTS(SELECT 1 FROM preserved_snapshots ps JOIN locations l ON l.snapshot_id=ps.id WHERE ps.session_id=s.id AND l.available=1)")
	}
	return where, args
}

func (s *Store) catalogCoverage(query CatalogQuery) (CatalogCoverage, error) {
	where, args := catalogWhere(query, false)
	var value CatalogCoverage
	if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN repository_name IS NOT NULL AND repository_name<>'' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN repository_name IS NULL OR repository_name='' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN branch IS NOT NULL AND branch<>'' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN branch IS NULL OR branch='' THEN 1 ELSE 0 END),0)
			FROM sessions s WHERE `+strings.Join(where, " AND "), args...).Scan(
		&value.Repository.Known, &value.Repository.Unknown, &value.Branch.Known, &value.Branch.Unknown); err != nil {
		return CatalogCoverage{}, fmt.Errorf("read history metadata coverage: %w", err)
	}
	query.ThreadKind = "all"
	where, args = catalogWhere(query, false)
	if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN thread_kind='root' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN thread_kind='subagent' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN thread_kind='unknown' THEN 1 ELSE 0 END),0)
			FROM sessions s WHERE `+strings.Join(where, " AND "), args...).Scan(
		&value.ThreadKind.Root, &value.ThreadKind.Subagent, &value.ThreadKind.Unknown); err != nil {
		return CatalogCoverage{}, fmt.Errorf("read history thread coverage: %w", err)
	}
	return value, nil
}

func (s *Store) indexGeneration() (int64, error) {
	var value int64
	if err := s.db.QueryRow(`SELECT COALESCE((SELECT value FROM meta WHERE key='index_generation'),'0')`).Scan(&value); err != nil {
		return 0, fmt.Errorf("read history index generation: %w", err)
	}
	return value, nil
}

func validCatalogSource(value CatalogSource) bool {
	return value == CatalogSourceAny || value == CatalogSourceProvider || value == CatalogSourceProviderLive || value == CatalogSourceProviderArchive || value == CatalogSourceVault
}

func optionalCatalogString(value sql.NullString) *string {
	if !value.Valid || value.String == "" {
		return nil
	}
	result := value.String
	return &result
}

func splitCatalogIDs(value string) []string {
	if value == "" {
		return []string{}
	}
	return strings.Split(value, ",")
}

func boundPreview(value string) string {
	lines := strings.Split(value, "\n")
	if len(lines) > maxPreviewLines {
		lines = lines[:maxPreviewLines]
	}
	value = strings.Join(lines, "\n")
	if len(value) > maxPreviewBytes {
		value = value[:maxPreviewBytes]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func newCatalogCursor(query CatalogQuery, generation int64, timestamp, sessionID string) catalogCursor {
	return catalogCursor{
		Version: 1, Generation: generation, Provider: string(query.Provider), Since: cursorCatalogTime(query.Since), Until: cursorCatalogTime(query.Until),
		CWD: query.CWD, Repo: query.Repo, Branch: query.Branch, Source: query.Source, Limit: query.Limit,
		ThreadKind: normalizedThreadKindFilter(query.ThreadKind),
		Unknown:    timestamp == "", Timestamp: timestamp, SessionID: sessionID,
	}
}

func (cursor catalogCursor) matches(query CatalogQuery, generation int64) error {
	if cursor.Version != 1 {
		return fmt.Errorf("unsupported history cursor version %d", cursor.Version)
	}
	if cursor.Generation != generation {
		return errors.New("history cursor is stale because the index generation changed")
	}
	if cursor.Provider != string(query.Provider) || cursor.Since != cursorCatalogTime(query.Since) || cursor.Until != cursorCatalogTime(query.Until) ||
		cursor.CWD != query.CWD || cursor.Repo != query.Repo || cursor.Branch != query.Branch || cursor.Source != query.Source ||
		cursor.ThreadKind != normalizedThreadKindFilter(query.ThreadKind) {
		return errors.New("history cursor does not match the requested filters")
	}
	if cursor.Limit < 1 || cursor.Limit > 500 || cursor.SessionID == "" || (cursor.Unknown && cursor.Timestamp != "") || (!cursor.Unknown && !validCatalogTimestamp(cursor.Timestamp)) {
		return errors.New("invalid history cursor")
	}
	return nil
}

func validCatalogTimestamp(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func encodeCatalogCursor(cursor catalogCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode history cursor: %w", err)
	}
	return "v1:" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeCatalogCursor(value string) (catalogCursor, error) {
	if !strings.HasPrefix(value, "v1:") {
		return catalogCursor{}, errors.New("malformed or unsupported history cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	if err != nil {
		return catalogCursor{}, errors.New("malformed history cursor")
	}
	var cursor catalogCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return catalogCursor{}, errors.New("malformed history cursor")
	}
	return cursor, nil
}

func cursorCatalogTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func sqliteTimestampKey(column string) string {
	tail := "substr(" + column + ",21)"
	fractionEnd := "(CASE " +
		"WHEN instr(" + tail + ",'Z')>0 THEN instr(" + tail + ",'Z') " +
		"WHEN instr(" + tail + ",'+')>0 THEN instr(" + tail + ",'+') " +
		"WHEN instr(" + tail + ",'-')>0 THEN instr(" + tail + ",'-') " +
		"ELSE length(" + tail + ")+1 END)"
	fraction := "(CASE WHEN instr(" + column + ",'.')=0 THEN '000000000' ELSE " +
		"substr(substr(" + tail + ",1," + fractionEnd + "-1)||'000000000',1,9) END)"
	return "(CASE WHEN " + column + " IS NULL OR " + column + "='' THEN '' " +
		"ELSE strftime('%Y-%m-%dT%H:%M:%S'," + column + ")||'.'||" + fraction + "||'Z' END)"
}

func fixedCatalogTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func normalizedThreadKindFilter(value string) string {
	if value == "" {
		return "all"
	}
	return value
}

func validThreadKindFilter(value string) bool {
	return value == "all" || value == string(history.ThreadRoot) || value == string(history.ThreadSubagent) || value == string(history.ThreadUnknown)
}
