package store

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

const maxOccurrenceMetadata = 20
const maxPromptProvenanceIDs = 100

// PromptQuery selects a bounded page of available human prompts.
type PromptQuery struct {
	Provider                 history.Provider
	Role                     string
	AssistantConsent         bool
	Since                    *time.Time
	Until                    *time.Time
	CWD                      string
	Repo                     string
	Branch                   string
	Source                   CatalogSource
	ThreadKind               string
	Limit                    int
	Cursor                   string
	IncludeText              bool
	AllOccurrences           bool
	PromptKinds              []history.PromptKind
	ExcludeControl           bool
	assistantIndexed         bool
	assistantProviders       []history.Provider
	assistantCoveragePartial bool
}

// SearchQuery adds an FTS expression to the shared prompt filters.
type SearchQuery struct {
	PromptQuery
	Query    string
	FTSQuery bool
}

// PageMetadata is the common keyset pagination contract.
type PageMetadata struct {
	Limit      int    `json:"limit"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// QueryCoverage discloses indexed date and provider-uneven metadata coverage.
type QueryCoverage struct {
	FirstTimestamp *string            `json:"first_timestamp"`
	LastTimestamp  *string            `json:"last_timestamp"`
	Repository     FieldCoverage      `json:"repository"`
	Branch         FieldCoverage      `json:"branch"`
	ThreadKind     ThreadKindCoverage `json:"thread_kind"`
	Roles          RoleQueryCoverage  `json:"roles"`
}

// RoleCoverage discloses searchable prompt counts and date bounds per role.
type RoleCoverage struct {
	LogicalPrompts int     `json:"logical_prompts"`
	FirstTimestamp *string `json:"first_timestamp"`
	LastTimestamp  *string `json:"last_timestamp"`
}

// RoleQueryCoverage reports independently bounded user and assistant corpora.
type RoleQueryCoverage struct {
	AssistantIndexed   bool               `json:"assistant_indexed"`
	AssistantProviders []history.Provider `json:"assistant_providers"`
	User               RoleCoverage       `json:"user"`
	Assistant          RoleCoverage       `json:"assistant"`
}

// PromptOccurrence is bounded provenance for one exact prompt occurrence.
type PromptOccurrence struct {
	Kind         string  `json:"kind"`
	SourceHeadID *string `json:"source_head_id"`
	SnapshotID   *string `json:"snapshot_id"`
	SourcePath   string  `json:"source_path,omitempty"`
	Archive      string  `json:"archive,omitempty"`
	RelativePath string  `json:"relative_path,omitempty"`
	VaultVersion int     `json:"vault_version,omitempty"`
	LineNumber   int64   `json:"line_number"`
	StartOffset  int64   `json:"start_offset"`
	EndOffset    int64   `json:"end_offset"`
}

// PromptResult is one logical human prompt, not one snapshot occurrence.
type PromptResult struct {
	sortTimestamp               string
	provenanceExpanded          bool
	PromptID                    string                `json:"prompt_id"`
	SessionID                   string                `json:"session_id"`
	Provider                    history.Provider      `json:"provider"`
	Role                        history.Role          `json:"role"`
	PromptKind                  history.PromptKind    `json:"prompt_kind"`
	Timestamp                   *string               `json:"timestamp"`
	RepositoryName              *string               `json:"repository_name"`
	CWD                         string                `json:"cwd,omitempty"`
	Branch                      *string               `json:"branch"`
	ThreadKind                  history.ThreadKind    `json:"thread_kind"`
	ThreadEvidence              string                `json:"thread_evidence"`
	ThreadConfidence            history.Confidence    `json:"thread_confidence"`
	ThreadRuleVersion           int                   `json:"thread_rule_version"`
	Relationships               []SessionRelationship `json:"relationships"`
	RelationshipsTruncated      bool                  `json:"relationships_truncated"`
	Rank                        *float64              `json:"rank,omitempty"`
	RankDirection               string                `json:"rank_direction,omitempty"`
	Snippet                     string                `json:"snippet"`
	Text                        *string               `json:"text,omitempty"`
	OccurrenceCount             int                   `json:"occurrence_count"`
	SourceHeadIDs               []string              `json:"source_head_ids,omitempty"`
	PreservedSnapshotIDs        []string              `json:"preserved_snapshot_ids,omitempty"`
	Occurrences                 []PromptOccurrence    `json:"occurrences,omitempty"`
	OccurrenceMetadataTruncated bool                  `json:"occurrence_metadata_truncated,omitempty"`
	ProvenanceIDsTruncated      bool                  `json:"provenance_ids_truncated,omitempty"`
	Availability                Availability          `json:"availability"`
	PreferredRetrievalSource    string                `json:"preferred_retrieval_source"`
	PreferredLocation           *PromptOccurrence     `json:"preferred_location"`
}

// MarshalJSON preserves the expanded provenance schema while omitting its
// arrays entirely from compact search and sample results.
func (value PromptResult) MarshalJSON() ([]byte, error) {
	type promptResultJSON PromptResult
	if !value.provenanceExpanded {
		return json.Marshal(promptResultJSON(value))
	}
	return json.Marshal(struct {
		promptResultJSON
		SourceHeadIDs               []string           `json:"source_head_ids"`
		PreservedSnapshotIDs        []string           `json:"preserved_snapshot_ids"`
		Occurrences                 []PromptOccurrence `json:"occurrences"`
		OccurrenceMetadataTruncated bool               `json:"occurrence_metadata_truncated"`
		ProvenanceIDsTruncated      bool               `json:"provenance_ids_truncated"`
	}{
		promptResultJSON:            promptResultJSON(value),
		SourceHeadIDs:               value.SourceHeadIDs,
		PreservedSnapshotIDs:        value.PreservedSnapshotIDs,
		Occurrences:                 value.Occurrences,
		OccurrenceMetadataTruncated: value.OccurrenceMetadataTruncated,
		ProvenanceIDsTruncated:      value.ProvenanceIDsTruncated,
	})
}

// SearchPage is one generation-bound FTS result page.
type SearchPage struct {
	Hits       []PromptResult `json:"hits"`
	Page       PageMetadata   `json:"page"`
	Coverage   QueryCoverage  `json:"coverage"`
	Warnings   []string       `json:"-"`
	Generation int64          `json:"index_generation"`
}

// PromptsPage is one generation-bound prompt enumeration page.
type PromptsPage struct {
	Prompts    []PromptResult `json:"prompts"`
	Page       PageMetadata   `json:"page"`
	Coverage   QueryCoverage  `json:"coverage"`
	Warnings   []string       `json:"-"`
	Generation int64          `json:"index_generation"`
}

type promptCursor struct {
	Version          int           `json:"v"`
	Kind             string        `json:"kind"`
	Generation       int64         `json:"generation"`
	Provider         string        `json:"provider"`
	Role             string        `json:"role"`
	AssistantConsent bool          `json:"assistant_consent"`
	Since            string        `json:"since"`
	Until            string        `json:"until"`
	CWD              string        `json:"cwd"`
	Repo             string        `json:"repo"`
	Branch           string        `json:"branch"`
	Source           CatalogSource `json:"source"`
	ThreadKind       string        `json:"thread_kind"`
	PromptKinds      string        `json:"prompt_kinds"`
	ExcludeControl   bool          `json:"exclude_control"`
	Query            string        `json:"query,omitempty"`
	FTSQuery         bool          `json:"fts_query,omitempty"`
	Limit            int           `json:"limit"`
	RankBits         string        `json:"rank_bits,omitempty"`
	Unknown          bool          `json:"unknown"`
	Timestamp        string        `json:"timestamp"`
	PromptID         string        `json:"prompt_id"`
}

// Search returns literal phrase search by default. Raw FTS5 syntax is accepted
// only when FTSQuery is true.
func (s *Store) Search(query SearchQuery) (SearchPage, error) {
	if strings.TrimSpace(query.Query) == "" {
		return SearchPage{}, errors.New("history search query must not be empty")
	}
	query.PromptQuery, _ = normalizePromptQuery(query.PromptQuery, 50)
	var err error
	query.PromptQuery, err = s.resolvePromptRole(query.PromptQuery)
	if err != nil {
		return SearchPage{}, err
	}
	if err := validatePromptQuery(query.PromptQuery); err != nil {
		return SearchPage{}, err
	}
	match := query.Query
	if !query.FTSQuery {
		match = literalFTSQuery(query.Query)
	}
	generation, err := s.indexGeneration()
	if err != nil {
		return SearchPage{}, err
	}
	cursor, err := preparePromptCursor(query.Cursor, "search", query.PromptQuery, generation, query.Query, query.FTSQuery)
	if err != nil {
		return SearchPage{}, err
	}
	if query.Cursor != "" && query.Limit == 0 {
		query.Limit = cursor.Limit
	}

	coverage, warnings, err := s.promptCoverage(query.PromptQuery)
	if err != nil {
		return SearchPage{}, err
	}
	where, args := promptWhere(query.PromptQuery, true, "p", "s")
	args = append([]any{match, query.IncludeText}, args...)
	statement := `WITH matched AS (
		SELECT p.id,p.public_id AS prompt_id,s.public_id AS session_id,s.provider,p.role,p.prompt_kind,p.timestamp,
			s.repository_name,s.cwd,s.branch,bm25(prompt_fts) AS rank,
			` + sqliteTimestampKey("p.timestamp") + ` AS sort_ts,
			snippet(prompt_fts,0,'[',']',' ... ',24) AS snippet,
			CASE WHEN ? THEN p.clean_text ELSE NULL END AS full_text
		FROM prompt_fts JOIN prompts p ON p.id=prompt_fts.rowid JOIN sessions s ON s.id=p.session_id
		WHERE prompt_fts MATCH ? AND ` + strings.Join(where, " AND ") + `)
		SELECT id,prompt_id,session_id,provider,role,prompt_kind,timestamp,repository_name,cwd,branch,rank,sort_ts,snippet,full_text
		FROM matched`
	// IncludeText precedes MATCH in SQL, so repair the argument order.
	args[0], args[1] = args[1], args[0]
	if query.Cursor != "" {
		rank, parseErr := cursor.rank()
		if parseErr != nil {
			return SearchPage{}, parseErr
		}
		if cursor.Unknown {
			statement += ` WHERE (rank>? OR (rank=? AND sort_ts='' AND prompt_id>?))`
			args = append(args, rank, rank, cursor.PromptID)
		} else {
			statement += ` WHERE (rank>? OR (rank=? AND (sort_ts='' OR sort_ts<? OR (sort_ts=? AND prompt_id>?))))`
			args = append(args, rank, rank, cursor.Timestamp, cursor.Timestamp, cursor.PromptID)
		}
	}
	statement += ` ORDER BY rank ASC,(sort_ts='') ASC,sort_ts DESC,prompt_id ASC LIMIT ?`
	args = append(args, query.Limit+1)
	rows, err := s.runner.Query(statement, args...)
	if err != nil {
		return SearchPage{}, fmt.Errorf("search history prompts: %w", err)
	}
	defer rows.Close()
	hits, err := s.scanPromptRows(rows, query.IncludeText, true, query.AllOccurrences)
	if err != nil {
		return SearchPage{}, err
	}
	page := SearchPage{Hits: hits, Coverage: coverage, Warnings: warnings, Generation: generation, Page: PageMetadata{Limit: query.Limit}}
	if len(page.Hits) > query.Limit {
		page.Hits = page.Hits[:query.Limit]
		page.Page.HasMore = true
		last := page.Hits[len(page.Hits)-1]
		page.Page.NextCursor, err = encodePromptCursor(newPromptCursor("search", query.PromptQuery, generation, query.Query, query.FTSQuery, last))
		if err != nil {
			return SearchPage{}, err
		}
	}
	if page.Hits == nil {
		page.Hits = []PromptResult{}
	}
	return page, nil
}

// ListPrompts returns clean logical human prompts without FTS ranking.
func (s *Store) ListPrompts(query PromptQuery) (PromptsPage, error) {
	query, _ = normalizePromptQuery(query, 100)
	var err error
	query, err = s.resolvePromptRole(query)
	if err != nil {
		return PromptsPage{}, err
	}
	if err := validatePromptQuery(query); err != nil {
		return PromptsPage{}, err
	}
	generation, err := s.indexGeneration()
	if err != nil {
		return PromptsPage{}, err
	}
	cursor, err := preparePromptCursor(query.Cursor, "prompts", query, generation, "", false)
	if err != nil {
		return PromptsPage{}, err
	}
	if query.Cursor != "" && query.Limit == 0 {
		query.Limit = cursor.Limit
	}
	coverage, warnings, err := s.promptCoverage(query)
	if err != nil {
		return PromptsPage{}, err
	}
	where, args := promptWhere(query, true, "p", "s")
	sortExpr := sqliteTimestampKey("p.timestamp")
	if query.Cursor != "" {
		if cursor.Unknown {
			where = append(where, sortExpr+"='' AND p.public_id>?")
			args = append(args, cursor.PromptID)
		} else {
			where = append(where, "("+sortExpr+"='' OR "+sortExpr+"<? OR ("+sortExpr+"=? AND p.public_id>?))")
			args = append(args, cursor.Timestamp, cursor.Timestamp, cursor.PromptID)
		}
	}
	queryArgs := []any{query.IncludeText}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, query.Limit+1)
	statement := `SELECT p.id,p.public_id,s.public_id,s.provider,p.role,p.prompt_kind,p.timestamp,s.repository_name,s.cwd,s.branch,
		NULL,` + sortExpr + `,substr(p.clean_text,1,2048),CASE WHEN ? THEN p.clean_text ELSE NULL END
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY (` + sortExpr + `='') ASC,` + sortExpr + ` DESC,p.public_id ASC LIMIT ?`
	rows, err := s.runner.Query(statement, queryArgs...)
	if err != nil {
		return PromptsPage{}, fmt.Errorf("list history prompts: %w", err)
	}
	defer rows.Close()
	prompts, err := s.scanPromptRows(rows, query.IncludeText, false, query.AllOccurrences)
	if err != nil {
		return PromptsPage{}, err
	}
	page := PromptsPage{Prompts: prompts, Coverage: coverage, Warnings: warnings, Generation: generation, Page: PageMetadata{Limit: query.Limit}}
	if len(page.Prompts) > query.Limit {
		page.Prompts = page.Prompts[:query.Limit]
		page.Page.HasMore = true
		last := page.Prompts[len(page.Prompts)-1]
		page.Page.NextCursor, err = encodePromptCursor(newPromptCursor("prompts", query, generation, "", false, last))
		if err != nil {
			return PromptsPage{}, err
		}
	}
	if page.Prompts == nil {
		page.Prompts = []PromptResult{}
	}
	return page, nil
}

func normalizePromptQuery(query PromptQuery, defaultLimit int) (PromptQuery, bool) {
	if query.Source == "" {
		query.Source = CatalogSourceAny
	}
	query.ThreadKind = normalizedThreadKindFilter(query.ThreadKind)
	if query.Role == "" {
		query.Role = string(history.RoleUser)
	}
	usedDefault := query.Limit == 0 && query.Cursor == ""
	if usedDefault {
		query.Limit = defaultLimit
	}
	return query, usedDefault
}

func (s *Store) resolvePromptRole(query PromptQuery) (PromptQuery, error) {
	providers, err := s.AssistantIndexingProviders()
	if err != nil {
		return query, err
	}
	if !query.AssistantConsent {
		return query, nil
	}
	query.assistantProviders = append([]history.Provider(nil), providers...)
	if query.Provider != "" {
		for _, provider := range providers {
			query.assistantIndexed = query.assistantIndexed || provider == query.Provider
		}
		query.assistantCoveragePartial = !query.assistantIndexed
	} else {
		query.assistantIndexed = len(providers) > 0
		query.assistantCoveragePartial = len(providers) < 2
	}
	return query, nil
}

func validatePromptQuery(query PromptQuery) error {
	if !validCatalogSource(query.Source) {
		return fmt.Errorf("invalid history source %q", query.Source)
	}
	if !validThreadKindFilter(query.ThreadKind) {
		return fmt.Errorf("invalid history thread kind %q", query.ThreadKind)
	}
	if query.Role != string(history.RoleUser) && query.Role != string(history.RoleAssistant) && query.Role != "any" {
		return fmt.Errorf("invalid history role %q", query.Role)
	}
	if query.Limit != 0 && (query.Limit < 1 || query.Limit > 500) {
		return errors.New("history prompt limit must be between 1 and 500")
	}
	for _, kind := range query.PromptKinds {
		if !validPromptKind(kind) {
			return fmt.Errorf("invalid history prompt kind %q", kind)
		}
	}
	return nil
}

func validPromptKind(kind history.PromptKind) bool {
	switch kind {
	case history.PromptKindHuman, history.PromptKindDelegation, history.PromptKindAgentMessage, history.PromptKindCommand, history.PromptKindControl, history.PromptKindUnknown:
		return true
	default:
		return false
	}
}

func literalFTSQuery(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func promptWhere(query PromptQuery, includeMetadataFilters bool, promptAlias, sessionAlias string) ([]string, []any) {
	p, s := promptAlias+".", sessionAlias+"."
	where := []string{p + "searchable=1", `EXISTS(SELECT 1 FROM occurrences qo JOIN locations ql ON ql.id=qo.location_id WHERE qo.prompt_id=` + p + `id AND ql.available=1)`}
	args := []any{}
	assistantProviderClause := "1=0"
	if len(query.assistantProviders) > 0 {
		placeholders := make([]string, 0, len(query.assistantProviders))
		for _, provider := range query.assistantProviders {
			placeholders = append(placeholders, "?")
			args = append(args, provider)
		}
		assistantProviderClause = s + "provider IN (" + strings.Join(placeholders, ",") + ")"
	}
	switch query.Role {
	case string(history.RoleAssistant):
		if query.assistantIndexed {
			where = append(where, p+"role='assistant' AND "+assistantProviderClause)
		} else {
			args = args[:0]
			where = append(where, "1=0")
		}
	case "any":
		if query.assistantIndexed {
			where = append(where, "("+p+"role='user' OR ("+p+"role='assistant' AND "+assistantProviderClause+"))")
		} else {
			args = args[:0]
			where = append(where, p+"role='user'")
		}
	default:
		args = args[:0]
		where = append(where, p+"role='user'")
	}
	if len(query.PromptKinds) > 0 {
		placeholders := make([]string, len(query.PromptKinds))
		for index, kind := range query.PromptKinds {
			placeholders[index] = "?"
			args = append(args, kind)
		}
		where = append(where, p+"prompt_kind IN ("+strings.Join(placeholders, ",")+")")
	} else {
		where = append(where, "("+p+"role<>'user' OR "+p+"prompt_kind='human')")
	}
	if query.ExcludeControl {
		where = append(where, p+"prompt_kind<>'control'")
	}
	if query.Provider != "" {
		where = append(where, s+"provider=?")
		args = append(args, query.Provider)
	}
	if query.Since != nil {
		where = append(where, p+"timestamp IS NOT NULL AND "+p+"timestamp<>'' AND "+sqliteTimestampKey(p+"timestamp")+">=?")
		args = append(args, fixedCatalogTimestamp(*query.Since))
	}
	if query.Until != nil {
		where = append(where, p+"timestamp IS NOT NULL AND "+p+"timestamp<>'' AND "+sqliteTimestampKey(p+"timestamp")+"<=?")
		args = append(args, fixedCatalogTimestamp(*query.Until))
	}
	if query.CWD != "" {
		where = append(where, s+"cwd=?")
		args = append(args, query.CWD)
	}
	if includeMetadataFilters && query.Repo != "" {
		where = append(where, s+"repository_name=?")
		args = append(args, query.Repo)
	}
	if includeMetadataFilters && query.Branch != "" {
		where = append(where, s+"branch=?")
		args = append(args, query.Branch)
	}
	if query.ThreadKind != "" && query.ThreadKind != "all" {
		where = append(where, s+"thread_kind=?")
		args = append(args, query.ThreadKind)
	}
	sourceClause := ""
	switch query.Source {
	case CatalogSourceProvider:
		sourceClause = "ql.kind IN ('provider_live','provider_archive')"
	case CatalogSourceProviderLive:
		sourceClause = "ql.kind='provider_live'"
	case CatalogSourceProviderArchive:
		sourceClause = "ql.kind='provider_archive'"
	case CatalogSourceVault:
		sourceClause = "ql.kind='vault'"
	}
	if sourceClause != "" {
		where = append(where, `EXISTS(SELECT 1 FROM occurrences qo JOIN locations ql ON ql.id=qo.location_id WHERE qo.prompt_id=`+p+`id AND ql.available=1 AND `+sourceClause+`)`)
	}
	return where, args
}

type promptRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

func (s *Store) scanPromptRows(rows promptRows, includeText, ranked, includeOccurrences bool) ([]PromptResult, error) {
	type scannedPrompt struct {
		dbID  int64
		value PromptResult
	}
	var scanned []scannedPrompt
	for rows.Next() {
		var value PromptResult
		var dbID int64
		var timestamp, repo, cwd, branch, snippet, text sql.NullString
		var rank sql.NullFloat64
		var sortTimestamp string
		if err := rows.Scan(&dbID, &value.PromptID, &value.SessionID, &value.Provider, &value.Role, &value.PromptKind, &timestamp, &repo, &cwd, &branch, &rank, &sortTimestamp, &snippet, &text); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				_ = rows.Close()
				return []PromptResult{}, nil
			}
			return nil, fmt.Errorf("scan history prompt: %w", err)
		}
		value.Timestamp = optionalCatalogString(timestamp)
		value.RepositoryName, value.Branch = optionalCatalogString(repo), optionalCatalogString(branch)
		value.CWD = cwd.String
		value.sortTimestamp = sortTimestamp
		value.Snippet = boundPreview(snippet.String)
		if includeText && text.Valid {
			value.Text = optionalCatalogString(text)
		}
		if ranked && rank.Valid {
			value.Rank = &rank.Float64
			value.RankDirection = "lower_is_better"
		}
		scanned = append(scanned, scannedPrompt{dbID: dbID, value: value})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	values := make([]PromptResult, 0, len(scanned))
	for _, item := range scanned {
		if err := s.populatePromptProvenance(item.dbID, &item.value, includeOccurrences); err != nil {
			return nil, err
		}
		values = append(values, item.value)
	}
	return values, nil
}

func (s *Store) populatePromptProvenance(promptID int64, value *PromptResult, includeOccurrences bool) error {
	value.provenanceExpanded = includeOccurrences
	var sessionID int64
	var threadEvidence sql.NullString
	if err := s.runner.QueryRow(`SELECT s.id,s.thread_kind,s.thread_evidence,s.thread_confidence,s.thread_rule_version
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE p.id=?`, promptID).Scan(
		&sessionID, &value.ThreadKind, &threadEvidence, &value.ThreadConfidence, &value.ThreadRuleVersion); err != nil {
		return fmt.Errorf("read prompt thread metadata: %w", err)
	}
	value.ThreadEvidence = threadEvidence.String
	var err error
	value.Relationships, value.RelationshipsTruncated, err = s.sessionRelationships(sessionID)
	if err != nil {
		return err
	}
	if err := s.runner.QueryRow(`SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN l.kind='provider_live' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN l.kind='provider_archive' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN l.kind='vault' THEN 1 ELSE 0 END),0)
		FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=? AND l.available=1`, promptID).Scan(
		&value.OccurrenceCount, &value.Availability.ProviderLive, &value.Availability.ProviderArchive, &value.Availability.Vault); err != nil {
		return fmt.Errorf("count prompt occurrences: %w", err)
	}
	if err := s.runner.QueryRow(`SELECT EXISTS(
		SELECT 1 FROM occurrences live_o JOIN locations live_l ON live_l.id=live_o.location_id
		JOIN source_heads sh ON sh.id=live_o.source_head_id
		JOIN occurrences vault_o ON vault_o.prompt_id=live_o.prompt_id
		JOIN locations vault_l ON vault_l.id=vault_o.location_id
		JOIN preserved_snapshots ps ON ps.id=vault_o.snapshot_id
		WHERE live_o.prompt_id=? AND live_l.available=1 AND live_l.kind='provider_live'
		AND vault_l.available=1 AND vault_l.kind='vault' AND sh.available=1 AND sh.complete_offset=sh.size
		AND sh.current_sha256<>'' AND sh.current_sha256=ps.content_sha256)`, promptID).Scan(&value.Availability.ExactLiveAndVaulted); err != nil {
		return fmt.Errorf("read prompt exact availability: %w", err)
	}
	var exactProviderLive, exactProviderArchive bool
	if err := s.runner.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id JOIN source_heads sh ON sh.id=o.source_head_id
			WHERE o.prompt_id=? AND l.available=1 AND l.kind='provider_live' AND sh.available=1 AND sh.complete_offset=sh.size AND sh.current_sha256<>''),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id JOIN source_heads sh ON sh.id=o.source_head_id
			WHERE o.prompt_id=? AND l.available=1 AND l.kind='provider_archive' AND sh.available=1 AND sh.complete_offset=sh.size AND sh.current_sha256<>'')`, promptID, promptID).Scan(&exactProviderLive, &exactProviderArchive); err != nil {
		return fmt.Errorf("read prompt retrieval availability: %w", err)
	}
	preferredKind := ""
	switch {
	case exactProviderLive:
		value.PreferredRetrievalSource, preferredKind = "provider-live", "provider_live"
	case exactProviderArchive:
		value.PreferredRetrievalSource, preferredKind = "provider-archive", "provider_archive"
	case value.Availability.Vault > 0:
		value.PreferredRetrievalSource, preferredKind = "vault", "vault"
	default:
		value.PreferredRetrievalSource = "unavailable"
	}
	occurrenceLimit := 1
	if includeOccurrences {
		occurrenceLimit = maxOccurrenceMetadata + 1
	}
	rows, err := s.runner.Query(`SELECT l.kind,sh.public_id,ps.public_id,l.source_path,l.archive,l.relative_path,l.vault_version,
		o.line_number,o.start_offset,o.end_offset
		FROM occurrences o JOIN locations l ON l.id=o.location_id
		LEFT JOIN source_heads sh ON sh.id=o.source_head_id LEFT JOIN preserved_snapshots ps ON ps.id=o.snapshot_id
		WHERE o.prompt_id=? AND l.available=1
		ORDER BY CASE WHEN l.kind=? AND (l.kind='vault' OR
			(sh.available=1 AND sh.complete_offset=sh.size AND sh.current_sha256<>'')) THEN 0
			WHEN l.kind='provider_live' THEN 1 WHEN l.kind='provider_archive' THEN 2 ELSE 3 END,
			l.vault_version DESC,o.line_number,o.start_offset,l.location_key,o.id LIMIT ?`, promptID, preferredKind, occurrenceLimit)
	if err != nil {
		return fmt.Errorf("list prompt occurrences: %w", err)
	}
	defer rows.Close()
	value.Occurrences = []PromptOccurrence{}
	for rows.Next() {
		var occurrence PromptOccurrence
		var sourceID, snapshotID sql.NullString
		if err := rows.Scan(&occurrence.Kind, &sourceID, &snapshotID, &occurrence.SourcePath, &occurrence.Archive, &occurrence.RelativePath, &occurrence.VaultVersion, &occurrence.LineNumber, &occurrence.StartOffset, &occurrence.EndOffset); err != nil {
			return fmt.Errorf("scan prompt occurrence: %w", err)
		}
		if sourceID.Valid {
			id := sourceID.String
			occurrence.SourceHeadID = &id
		}
		if snapshotID.Valid {
			id := snapshotID.String
			occurrence.SnapshotID = &id
		}
		if value.PreferredLocation == nil {
			preferred := occurrence
			value.PreferredLocation = &preferred
		}
		if includeOccurrences && len(value.Occurrences) < maxOccurrenceMetadata {
			value.Occurrences = append(value.Occurrences, occurrence)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if includeOccurrences && value.OccurrenceCount > maxOccurrenceMetadata {
		value.OccurrenceMetadataTruncated = true
	}
	value.SourceHeadIDs = []string{}
	value.PreservedSnapshotIDs = []string{}
	if includeOccurrences {
		value.SourceHeadIDs, err = s.promptProvenanceIDs(promptID, "source_heads", "source_head_id")
		if err != nil {
			return err
		}
		if len(value.SourceHeadIDs) > maxPromptProvenanceIDs {
			value.SourceHeadIDs = value.SourceHeadIDs[:maxPromptProvenanceIDs]
			value.ProvenanceIDsTruncated = true
		}
		value.PreservedSnapshotIDs, err = s.promptProvenanceIDs(promptID, "preserved_snapshots", "snapshot_id")
		if err != nil {
			return err
		}
		if len(value.PreservedSnapshotIDs) > maxPromptProvenanceIDs {
			value.PreservedSnapshotIDs = value.PreservedSnapshotIDs[:maxPromptProvenanceIDs]
			value.ProvenanceIDsTruncated = true
		}
	}
	value.Availability.Unavailable = value.OccurrenceCount == 0
	return nil
}

func (s *Store) promptProvenanceIDs(promptID int64, table, occurrenceColumn string) ([]string, error) {
	statement := `SELECT DISTINCT entity.public_id FROM occurrences o
		JOIN locations l ON l.id=o.location_id JOIN ` + table + ` entity ON entity.id=o.` + occurrenceColumn + `
		WHERE o.prompt_id=? AND l.available=1 ORDER BY entity.public_id LIMIT ?`
	rows, err := s.runner.Query(statement, promptID, maxPromptProvenanceIDs+1)
	if err != nil {
		return nil, fmt.Errorf("list prompt provenance IDs: %w", err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan prompt provenance ID: %w", err)
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) promptCoverage(query PromptQuery) (QueryCoverage, []string, error) {
	base := query
	base.Since, base.Until = nil, nil
	where, args := promptWhere(base, true, "p", "s")
	var first, last sql.NullString
	if err := s.runner.QueryRow(`SELECT MIN(`+sqliteTimestampKey("p.timestamp")+`),MAX(`+sqliteTimestampKey("p.timestamp")+`)
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE p.timestamp IS NOT NULL AND p.timestamp<>'' AND `+strings.Join(where, " AND "), args...).Scan(&first, &last); err != nil {
		return QueryCoverage{}, nil, fmt.Errorf("read history date coverage: %w", err)
	}
	coverageQuery := query
	coverageQuery.Repo, coverageQuery.Branch = "", ""
	coverageWhere, coverageArgs := promptWhere(coverageQuery, false, "p", "s")
	var coverage QueryCoverage
	coverage.FirstTimestamp, coverage.LastTimestamp = optionalCatalogString(first), optionalCatalogString(last)
	coverage.Roles.AssistantIndexed = query.assistantIndexed
	coverage.Roles.AssistantProviders = append([]history.Provider{}, query.assistantProviders...)
	roleQuery := base
	roleQuery.Role = "any"
	roleWhere, roleArgs := promptWhere(roleQuery, true, "p", "s")
	var userFirst, userLast, assistantFirst, assistantLast sql.NullString
	if err := s.runner.QueryRow(`SELECT
		COUNT(CASE WHEN p.role='user' THEN 1 END),
		MIN(CASE WHEN p.role='user' AND p.timestamp IS NOT NULL AND p.timestamp<>'' THEN `+sqliteTimestampKey("p.timestamp")+` END),
		MAX(CASE WHEN p.role='user' AND p.timestamp IS NOT NULL AND p.timestamp<>'' THEN `+sqliteTimestampKey("p.timestamp")+` END),
		COUNT(CASE WHEN p.role='assistant' THEN 1 END),
		MIN(CASE WHEN p.role='assistant' AND p.timestamp IS NOT NULL AND p.timestamp<>'' THEN `+sqliteTimestampKey("p.timestamp")+` END),
		MAX(CASE WHEN p.role='assistant' AND p.timestamp IS NOT NULL AND p.timestamp<>'' THEN `+sqliteTimestampKey("p.timestamp")+` END)
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE `+strings.Join(roleWhere, " AND "), roleArgs...).Scan(
		&coverage.Roles.User.LogicalPrompts, &userFirst, &userLast,
		&coverage.Roles.Assistant.LogicalPrompts, &assistantFirst, &assistantLast); err != nil {
		return QueryCoverage{}, nil, fmt.Errorf("read history role coverage: %w", err)
	}
	coverage.Roles.User.FirstTimestamp, coverage.Roles.User.LastTimestamp = optionalCatalogString(userFirst), optionalCatalogString(userLast)
	coverage.Roles.Assistant.FirstTimestamp, coverage.Roles.Assistant.LastTimestamp = optionalCatalogString(assistantFirst), optionalCatalogString(assistantLast)
	if err := s.runner.QueryRow(`SELECT
		COUNT(DISTINCT CASE WHEN s.repository_name IS NOT NULL AND s.repository_name<>'' THEN s.id END),
		COUNT(DISTINCT CASE WHEN s.repository_name IS NULL OR s.repository_name='' THEN s.id END),
		COUNT(DISTINCT CASE WHEN s.branch IS NOT NULL AND s.branch<>'' THEN s.id END),
		COUNT(DISTINCT CASE WHEN s.branch IS NULL OR s.branch='' THEN s.id END)
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE `+strings.Join(coverageWhere, " AND "), coverageArgs...).Scan(
		&coverage.Repository.Known, &coverage.Repository.Unknown, &coverage.Branch.Known, &coverage.Branch.Unknown); err != nil {
		return QueryCoverage{}, nil, fmt.Errorf("read history query metadata coverage: %w", err)
	}
	coverageQuery.ThreadKind = "all"
	coverageWhere, coverageArgs = promptWhere(coverageQuery, false, "p", "s")
	if err := s.runner.QueryRow(`SELECT
		COUNT(DISTINCT CASE WHEN s.thread_kind='root' THEN s.id END),
		COUNT(DISTINCT CASE WHEN s.thread_kind='subagent' THEN s.id END),
		COUNT(DISTINCT CASE WHEN s.thread_kind='unknown' THEN s.id END)
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE `+strings.Join(coverageWhere, " AND "), coverageArgs...).Scan(
		&coverage.ThreadKind.Root, &coverage.ThreadKind.Subagent, &coverage.ThreadKind.Unknown); err != nil {
		return QueryCoverage{}, nil, fmt.Errorf("read history query thread coverage: %w", err)
	}
	warnings := coverageWarnings(query, coverage)
	if (query.Role == "any" || query.Role == string(history.RoleAssistant)) && query.assistantIndexed && materiallyDifferentRoleCoverage(coverage.Roles) {
		warnings = append(warnings, "user and assistant role coverage differs materially; qualify conclusions using data.coverage.roles")
	}
	return coverage, warnings, nil
}

func materiallyDifferentRoleCoverage(coverage RoleQueryCoverage) bool {
	if coverage.User.LogicalPrompts == 0 || coverage.Assistant.LogicalPrompts == 0 {
		return coverage.User.LogicalPrompts != coverage.Assistant.LogicalPrompts
	}
	largerCount := max(coverage.User.LogicalPrompts, coverage.Assistant.LogicalPrompts)
	countDifference := coverage.User.LogicalPrompts - coverage.Assistant.LogicalPrompts
	if countDifference < 0 {
		countDifference = -countDifference
	}
	if countDifference*5 > largerCount {
		return true
	}
	return materiallyDifferentRoleBound(coverage.User.FirstTimestamp, coverage.Assistant.FirstTimestamp) ||
		materiallyDifferentRoleBound(coverage.User.LastTimestamp, coverage.Assistant.LastTimestamp)
}

func materiallyDifferentRoleBound(user, assistant *string) bool {
	if user == nil || assistant == nil {
		return user != nil || assistant != nil
	}
	userTime, userErr := time.Parse(time.RFC3339Nano, *user)
	assistantTime, assistantErr := time.Parse(time.RFC3339Nano, *assistant)
	if userErr != nil || assistantErr != nil {
		return *user != *assistant
	}
	difference := userTime.Sub(assistantTime)
	if difference < 0 {
		difference = -difference
	}
	return difference > 24*time.Hour
}

func coverageWarnings(query PromptQuery, coverage QueryCoverage) []string {
	warnings := assistantIndexWarnings(query)
	if query.Repo != "" && coverage.Repository.Unknown > 0 {
		warnings = append(warnings, fmt.Sprintf("--repo excluded %d session(s) with unknown repository metadata; repository coverage is Codex-complete and Claude-partial", coverage.Repository.Unknown))
	}
	if query.Branch != "" && coverage.Branch.Unknown > 0 {
		warnings = append(warnings, fmt.Sprintf("--branch excluded %d session(s) with unknown branch metadata; branch coverage is Codex-complete and Claude-partial", coverage.Branch.Unknown))
	}
	if (query.Since != nil || query.Until != nil) && coverage.FirstTimestamp == nil && coverage.LastTimestamp == nil {
		warnings = append(warnings, "requested date coverage cannot be established because no indexed timestamped prompts match the other filters")
		return warnings
	}
	if query.Since != nil && coverage.FirstTimestamp != nil {
		first, err := time.Parse(time.RFC3339Nano, *coverage.FirstTimestamp)
		last, lastErr := time.Parse(time.RFC3339Nano, *coverage.LastTimestamp)
		if err == nil && lastErr == nil {
			if query.Since.After(last) {
				warnings = append(warnings, fmt.Sprintf("requested --since %s begins after indexed coverage ending %s", query.Since.Format("2006-01-02"), last.Format(time.RFC3339Nano)))
			} else if query.Since.Before(first) {
				warnings = append(warnings, fmt.Sprintf("requested --since %s predates indexed coverage beginning %s", query.Since.Format("2006-01-02"), first.Format(time.RFC3339Nano)))
			}
		}
	}
	if query.Until != nil && coverage.LastTimestamp != nil {
		last, err := time.Parse(time.RFC3339Nano, *coverage.LastTimestamp)
		first, firstErr := time.Parse(time.RFC3339Nano, *coverage.FirstTimestamp)
		if err == nil && firstErr == nil {
			if query.Until.Before(first) {
				warnings = append(warnings, fmt.Sprintf("requested --until %s ends before indexed coverage beginning %s", query.Until.Format("2006-01-02"), first.Format(time.RFC3339Nano)))
			} else if query.Until.After(last) {
				warnings = append(warnings, fmt.Sprintf("requested --until %s extends beyond indexed coverage ending %s", query.Until.Format("2006-01-02"), last.Format(time.RFC3339Nano)))
			}
		}
	}
	return warnings
}

func assistantIndexWarnings(query PromptQuery) []string {
	warnings := []string{}
	if (query.Role == string(history.RoleAssistant) || query.Role == "any") && !query.assistantIndexed {
		warnings = append(warnings, "assistant content is not indexed; set history.index_assistant=true and run tokenomnom history index")
	}
	if (query.Role == string(history.RoleAssistant) || query.Role == "any") && query.assistantCoveragePartial && query.assistantIndexed {
		providers := make([]string, 0, len(query.assistantProviders))
		for _, provider := range query.assistantProviders {
			providers = append(providers, string(provider))
		}
		warnings = append(warnings, "assistant content is indexed only for providers: "+strings.Join(providers, ", ")+"; other provider coverage is unavailable")
	}
	return warnings
}

func newPromptCursor(kind string, query PromptQuery, generation int64, search string, fts bool, result PromptResult) promptCursor {
	cursor := promptCursor{Version: 1, Kind: kind, Generation: generation, Provider: string(query.Provider), Role: query.Role, AssistantConsent: cursorAssistantConsent(query), Since: cursorCatalogTime(query.Since), Until: cursorCatalogTime(query.Until), CWD: query.CWD, Repo: query.Repo, Branch: query.Branch, Source: query.Source, ThreadKind: normalizedThreadKindFilter(query.ThreadKind), PromptKinds: promptKindsCursorValue(query.PromptKinds), ExcludeControl: query.ExcludeControl, Query: search, FTSQuery: fts, Limit: query.Limit, PromptID: result.PromptID}
	if result.Timestamp == nil {
		cursor.Unknown = true
	} else {
		cursor.Timestamp = result.sortTimestamp
	}
	if result.Rank != nil {
		cursor.RankBits = strconv.FormatUint(math.Float64bits(*result.Rank), 16)
	}
	return cursor
}

func preparePromptCursor(value, kind string, query PromptQuery, generation int64, search string, fts bool) (promptCursor, error) {
	if value == "" {
		return promptCursor{}, nil
	}
	cursor, err := decodePromptCursor(value)
	if err != nil {
		return promptCursor{}, err
	}
	if cursor.Version != 1 || cursor.Kind != kind {
		return promptCursor{}, errors.New("unsupported history prompt cursor")
	}
	if cursor.Generation != generation {
		return promptCursor{}, errors.New("history cursor is stale because the index generation changed")
	}
	if cursor.Provider != string(query.Provider) || cursor.Role != query.Role || cursor.AssistantConsent != cursorAssistantConsent(query) || cursor.Since != cursorCatalogTime(query.Since) || cursor.Until != cursorCatalogTime(query.Until) || cursor.CWD != query.CWD || cursor.Repo != query.Repo || cursor.Branch != query.Branch || cursor.Source != query.Source || cursor.ThreadKind != normalizedThreadKindFilter(query.ThreadKind) || cursor.PromptKinds != promptKindsCursorValue(query.PromptKinds) || cursor.ExcludeControl != query.ExcludeControl || cursor.Query != search || cursor.FTSQuery != fts {
		return promptCursor{}, errors.New("history cursor does not match the requested filters or query mode")
	}
	if cursor.Limit < 1 || cursor.Limit > 500 || cursor.PromptID == "" || (cursor.Unknown && cursor.Timestamp != "") || (!cursor.Unknown && !validCatalogTimestamp(cursor.Timestamp)) {
		return promptCursor{}, errors.New("invalid history prompt cursor")
	}
	if kind == "search" {
		if _, err := cursor.rank(); err != nil {
			return promptCursor{}, err
		}
	}
	return cursor, nil
}

func promptKindsCursorValue(kinds []history.PromptKind) string {
	values := make([]string, len(kinds))
	for index, kind := range kinds {
		values[index] = string(kind)
	}
	sort.Strings(values)
	return strings.Join(values, ",")
}

func cursorAssistantConsent(query PromptQuery) bool {
	return query.Role != string(history.RoleUser) && query.AssistantConsent
}

func (cursor promptCursor) rank() (float64, error) {
	bits, err := strconv.ParseUint(cursor.RankBits, 16, 64)
	if err != nil || cursor.RankBits == "" {
		return 0, errors.New("invalid history search cursor rank")
	}
	value := math.Float64frombits(bits)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("invalid history search cursor rank")
	}
	return value, nil
}

func encodePromptCursor(cursor promptCursor) (string, error) {
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode history prompt cursor: %w", err)
	}
	return "v1:" + base64.RawURLEncoding.EncodeToString(data), nil
}

func decodePromptCursor(value string) (promptCursor, error) {
	if !strings.HasPrefix(value, "v1:") {
		return promptCursor{}, errors.New("malformed or unsupported history prompt cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	if err != nil {
		return promptCursor{}, errors.New("malformed history prompt cursor")
	}
	var cursor promptCursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return promptCursor{}, errors.New("malformed history prompt cursor")
	}
	return cursor, nil
}
