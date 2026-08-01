package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNoAvailableRawLocation means the index knows the session but none of
// its exact transcript locations can currently be read.
var ErrNoAvailableRawLocation = errors.New("no available exact raw location")

// RawCandidate is an indexed exact-byte location in preferred retrieval order.
type RawCandidate struct {
	Kind          string  `json:"kind"`
	SourceHeadID  *string `json:"source_head_id"`
	SnapshotID    *string `json:"snapshot_id"`
	SourcePath    string  `json:"source_path,omitempty"`
	Archive       string  `json:"archive,omitempty"`
	RelativePath  string  `json:"relative_path,omitempty"`
	VaultVersion  int     `json:"vault_version,omitempty"`
	ContentSHA256 string  `json:"content_sha256"`
	Size          int64   `json:"size"`
}

const maxExportTreeDepth = 16

// ExportSession is one session in a deterministic root-first subagent tree.
type ExportSession struct {
	CatalogSession
	ParentSessionID       *string `json:"parent_session_id,omitempty"`
	ParentNativeMessageID string  `json:"parent_native_message_id,omitempty"`
	Depth                 int     `json:"depth"`
}

// ResolveExportSessionID accepts a session ID or a prompt ID and returns the
// owning stable session ID.
func (s *Store) ResolveExportSessionID(publicID string) (string, error) {
	switch {
	case strings.HasPrefix(publicID, "ses_"):
		value, err := s.GetSession(publicID)
		if err != nil {
			return "", err
		}
		return value.SessionID, nil
	case strings.HasPrefix(publicID, "prm_"):
		value, err := s.GetPrompt(publicID)
		if err != nil {
			return "", err
		}
		return value.SessionID, nil
	default:
		return "", fmt.Errorf("%q is not a history prompt or session ID", publicID)
	}
}

// ExportTree returns a bounded, deterministic root-first subagent tree. Fork
// relationships are deliberately not included in full-session exports.
func (s *Store) ExportTree(publicID string, includeSubagents bool) ([]ExportSession, []string, error) {
	rootID, err := s.ResolveExportSessionID(publicID)
	if err != nil {
		return nil, nil, err
	}
	root, err := s.GetSession(rootID)
	if err != nil {
		return nil, nil, err
	}
	result := []ExportSession{{CatalogSession: root}}
	warnings := []string{}
	if !includeSubagents {
		return result, warnings, nil
	}
	seen := map[string]bool{root.SessionID: true}
	var appendChildren func(ExportSession) error
	appendChildren = func(parent ExportSession) error {
		if parent.Depth >= maxExportTreeDepth {
			var count int
			if err := s.runner.QueryRow(`SELECT COUNT(*) FROM session_relations r
				WHERE r.parent_session_id=? AND r.relation_kind='subagent'
				AND EXISTS(SELECT 1 FROM session_relation_supports support WHERE support.relation_id=r.id)`,
				parent.databaseID).Scan(&count); err != nil {
				return fmt.Errorf("count bounded history export children: %w", err)
			}
			if count > 0 {
				warnings = append(warnings, fmt.Sprintf("subagent recursion stopped at depth %d under session %s", maxExportTreeDepth, parent.SessionID))
			}
			return nil
		}
		rows, err := s.runner.Query(`SELECT child.public_id,r.parent_native_message_id
			FROM session_relations r JOIN sessions child ON child.id=r.child_session_id
			WHERE r.parent_session_id=? AND r.relation_kind='subagent'
			AND EXISTS(SELECT 1 FROM session_relation_supports support WHERE support.relation_id=r.id)
			ORDER BY (`+sqliteTimestampKey("COALESCE(NULLIF(child.first_ts,''),NULLIF(child.last_ts,''),'')")+`='') ASC,
				`+sqliteTimestampKey("COALESCE(NULLIF(child.first_ts,''),NULLIF(child.last_ts,''),'')")+`,child.public_id`,
			parent.databaseID)
		if err != nil {
			return fmt.Errorf("list history export children: %w", err)
		}
		type childReference struct {
			id              string
			parentMessageID string
		}
		children := []childReference{}
		for rows.Next() {
			var child childReference
			if err := rows.Scan(&child.id, &child.parentMessageID); err != nil {
				rows.Close()
				return fmt.Errorf("scan history export child: %w", err)
			}
			children = append(children, child)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, reference := range children {
			if seen[reference.id] {
				warnings = append(warnings, fmt.Sprintf("cyclic or duplicate subagent relationship skipped for session %s", reference.id))
				continue
			}
			value, err := s.GetSession(reference.id)
			if err != nil {
				return err
			}
			parentID := parent.SessionID
			child := ExportSession{
				CatalogSession:        value,
				ParentSessionID:       &parentID,
				ParentNativeMessageID: reference.parentMessageID,
				Depth:                 parent.Depth + 1,
			}
			seen[reference.id] = true
			result = append(result, child)
			if err := appendChildren(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := appendChildren(result[0]); err != nil {
		return nil, nil, err
	}
	return result, warnings, nil
}

// GetPrompt resolves a stable prompt ID and returns its full clean text.
func (s *Store) GetPrompt(publicID string) (PromptResult, error) {
	return s.getPrompt(publicID, true)
}

func (s *Store) getPrompt(publicID string, allOccurrences bool) (PromptResult, error) {
	resolved, err := s.ResolvePublicID(publicID)
	if err != nil {
		return PromptResult{}, err
	}
	row := s.runner.QueryRow(`SELECT p.id,p.public_id,s.public_id,s.provider,p.role,p.prompt_kind,p.timestamp,s.repository_name,s.project,s.project_source,s.cwd,s.branch,
		NULL,`+sqliteTimestampKey("p.timestamp")+`,substr(p.clean_text,1,2048),p.clean_text
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE p.public_id=? AND p.searchable=1 AND p.role IN ('user','assistant')`, resolved)
	values, err := s.scanPromptRows(&singlePromptRow{row: row}, true, false, allOccurrences)
	if err != nil {
		return PromptResult{}, err
	}
	if len(values) == 0 {
		return PromptResult{}, fmt.Errorf("history prompt ID %q not found", publicID)
	}
	return values[0], nil
}

// GetSession resolves one stable logical-session ID.
func (s *Store) GetSession(publicID string) (CatalogSession, error) {
	resolved, err := s.ResolvePublicID(publicID)
	if err != nil {
		return CatalogSession{}, err
	}
	value, err := scanCatalogSession(s.runner.QueryRow(catalogSelect+` WHERE s.public_id=?`, resolved))
	if errors.Is(err, sql.ErrNoRows) {
		return CatalogSession{}, fmt.Errorf("history session ID %q not found", publicID)
	}
	if err != nil {
		return CatalogSession{}, fmt.Errorf("get history session: %w", err)
	}
	value.Relationships, value.RelationshipsTruncated, err = s.sessionRelationships(value.databaseID)
	if err != nil {
		return CatalogSession{}, err
	}
	return value, nil
}

// SessionPrompts returns prompts from exactly one logical session.
func (s *Store) SessionPrompts(publicID string, query PromptQuery) (PromptsPage, error) {
	resolved, err := s.ResolvePublicID(publicID)
	if err != nil {
		return PromptsPage{}, err
	}
	if !strings.HasPrefix(resolved, "ses_") {
		return PromptsPage{}, fmt.Errorf("%q is not a history session ID", publicID)
	}
	if _, err := s.GetSession(resolved); err != nil {
		return PromptsPage{}, err
	}
	// Session ID is folded into CWD's cursor binding with an internal sentinel;
	// it is replaced by the exact session predicate below.
	query, _ = normalizePromptQuery(query, 100)
	if err := validatePromptQuery(query); err != nil {
		return PromptsPage{}, err
	}
	generation, err := s.indexGeneration()
	if err != nil {
		return PromptsPage{}, err
	}
	bound := query
	bound.CWD = "\x00session:" + resolved
	cursor, err := preparePromptCursor(query.Cursor, "session-prompts", bound, generation, "", false)
	if err != nil {
		return PromptsPage{}, err
	}
	if query.Cursor != "" && query.Limit == 0 {
		query.Limit = cursor.Limit
		bound.Limit = cursor.Limit
	}
	where, args := promptWhere(query, true, "p", "s")
	where = append(where, "s.public_id=?")
	args = append(args, resolved)
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
	queryArgs := []any{true}
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, query.Limit+1)
	rows, err := s.runner.Query(`SELECT p.id,p.public_id,s.public_id,s.provider,p.role,p.prompt_kind,p.timestamp,s.repository_name,s.project,s.project_source,s.cwd,s.branch,
		NULL,`+sortExpr+`,substr(p.clean_text,1,2048),CASE WHEN ? THEN p.clean_text ELSE NULL END
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE `+strings.Join(where, " AND ")+`
		ORDER BY (`+sortExpr+`='') ASC,`+sortExpr+` DESC,p.public_id ASC LIMIT ?`, queryArgs...)
	if err != nil {
		return PromptsPage{}, fmt.Errorf("list session prompts: %w", err)
	}
	defer rows.Close()
	prompts, err := s.scanPromptRows(rows, true, false, true)
	if err != nil {
		return PromptsPage{}, err
	}
	page := PromptsPage{Prompts: prompts, Generation: generation, Page: PageMetadata{Limit: query.Limit}, Warnings: []string{}}
	if len(page.Prompts) > query.Limit {
		page.Prompts = page.Prompts[:query.Limit]
		page.Page.HasMore = true
		last := page.Prompts[len(page.Prompts)-1]
		page.Page.NextCursor, err = encodePromptCursor(newPromptCursor("session-prompts", bound, generation, "", false, last))
		if err != nil {
			return PromptsPage{}, err
		}
	}
	if page.Prompts == nil {
		page.Prompts = []PromptResult{}
	}
	return page, nil
}

// RawCandidates returns only stored IDs and locations, never an arbitrary user path.
func (s *Store) RawCandidates(sessionID, snapshotID string) ([]RawCandidate, error) {
	resolved, err := s.ResolvePublicID(sessionID)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(resolved, "ses_") {
		return nil, fmt.Errorf("%q is not a history session ID", sessionID)
	}
	if _, err := s.GetSession(resolved); err != nil {
		return nil, err
	}
	whereSnapshot := ""
	if snapshotID != "" {
		if !strings.HasPrefix(snapshotID, "snap_") {
			return nil, fmt.Errorf("%q is not a preserved snapshot ID", snapshotID)
		}
		whereSnapshot = " AND ps.public_id=?"
	}
	statement := `SELECT kind,source_id,snapshot_id,source_path,archive,relative_path,vault_version,content_sha256,size FROM (
		SELECT l.kind,sh.public_id AS source_id,NULL AS snapshot_id,l.source_path,l.archive,l.relative_path,l.vault_version,
			sh.current_sha256 AS content_sha256,sh.size,CASE l.kind WHEN 'provider_live' THEN 0 ELSE 1 END AS preference,
			sh.indexed_at AS recency
		FROM source_heads sh JOIN sessions s ON s.id=sh.session_id JOIN locations l ON l.source_head_id=sh.id
		WHERE s.public_id=? AND l.available=1 AND sh.available=1 AND sh.current_sha256<>'' AND sh.complete_offset=sh.size`
	if snapshotID != "" {
		statement += ` AND 0`
	}
	statement += ` UNION ALL
		SELECT l.kind,NULL,ps.public_id,l.source_path,l.archive,l.relative_path,l.vault_version,
			ps.content_sha256,ps.size,2 AS preference,ps.created_at AS recency
		FROM preserved_snapshots ps JOIN sessions s ON s.id=ps.session_id JOIN locations l ON l.snapshot_id=ps.id
		WHERE s.public_id=? AND l.available=1` + whereSnapshot + `)
		ORDER BY preference,recency DESC,vault_version DESC,source_path,archive,relative_path`
	queryArgs := []any{resolved, resolved}
	if snapshotID != "" {
		queryArgs = append(queryArgs, snapshotID)
	}
	rows, err := s.runner.Query(statement, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("list raw history locations: %w", err)
	}
	defer rows.Close()
	result := []RawCandidate{}
	for rows.Next() {
		var value RawCandidate
		var source, snapshot sql.NullString
		if err := rows.Scan(&value.Kind, &source, &snapshot, &value.SourcePath, &value.Archive, &value.RelativePath, &value.VaultVersion, &value.ContentSHA256, &value.Size); err != nil {
			return nil, fmt.Errorf("scan raw history location: %w", err)
		}
		value.SourceHeadID, value.SnapshotID = optionalCatalogString(source), optionalCatalogString(snapshot)
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		if snapshotID != "" {
			return nil, fmt.Errorf("preserved snapshot %q is unavailable, unknown, or does not belong to session %q", snapshotID, sessionID)
		}
		return nil, fmt.Errorf("%w: session %q has no available exact raw location", ErrNoAvailableRawLocation, sessionID)
	}
	return result, nil
}

type singlePromptRow struct {
	row  *sql.Row
	done bool
	err  error
}

func (r *singlePromptRow) Next() bool {
	if r.done {
		return false
	}
	r.done = true
	return true
}

func (r *singlePromptRow) Scan(values ...any) error {
	err := r.row.Scan(values...)
	if errors.Is(err, sql.ErrNoRows) {
		r.err = nil
		return sql.ErrNoRows
	}
	r.err = err
	return err
}

func (r *singlePromptRow) Err() error { return r.err }

func (r *singlePromptRow) Close() error { return nil }
