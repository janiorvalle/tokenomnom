package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

const (
	SampleUnitPrompt  = "prompt"
	SampleUnitSession = "session"

	SampleStrategyRandom     = "random"
	SampleStrategyStratified = "stratified"
)

// SampleQuery selects a bounded deterministic sample of logical units.
type SampleQuery struct {
	PromptQuery
	Unit               string
	Strategy           string
	GroupBy            []string
	Count              int
	Seed               string
	MinLength          int
	OnePerSession      bool
	excludedSessionIDs []string
}

// SampleCoverage reports metadata and date coverage in the returned sample.
type SampleCoverage struct {
	FirstTimestamp *string            `json:"first_timestamp"`
	LastTimestamp  *string            `json:"last_timestamp"`
	Repository     FieldCoverage      `json:"repository"`
	Project        ProjectCoverage    `json:"project"`
	Branch         FieldCoverage      `json:"branch"`
	ThreadKind     ThreadKindCoverage `json:"thread_kind"`
}

// SampleItem contains exactly one logical prompt or session.
type SampleItem struct {
	Unit    string            `json:"unit"`
	Groups  map[string]string `json:"groups"`
	Prompt  *PromptResult     `json:"prompt,omitempty"`
	Session *CatalogSession   `json:"session,omitempty"`
	Text    *string           `json:"text,omitempty"`
}

// SampleResult is a deterministic, generation-bound sample.
type SampleResult struct {
	Items      []SampleItem   `json:"items"`
	Unit       string         `json:"unit"`
	Strategy   string         `json:"strategy"`
	GroupBy    []string       `json:"group_by"`
	Count      int            `json:"count"`
	Seed       string         `json:"seed"`
	Generation int64          `json:"index_generation"`
	Coverage   SampleCoverage `json:"coverage"`
	Warnings   []string       `json:"-"`
}

type sampleGroup struct {
	values  map[string]string
	key     string
	hash    []byte
	encoded string
}

type sampleState struct {
	group   sampleGroup
	lastKey []byte
	lastID  string
	wrapped bool
	done    bool
}

type sampledID struct {
	publicID  string
	key       []byte
	sessionID string
	group     sampleGroup
}

type seededSampleState struct {
	state sampleState
	first sampledID
}

type samplingMetadata struct {
	unitKind                                                         string
	unitID                                                           int64
	sampleKey                                                        []byte
	month, repository, project, projectSource, thread, provider, cwd string
	branch, firstDate, lastDate                                      string
	providerLive, providerArchive, vaultAvailable                    bool
}

type samplingExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
}

func populateSampleStrata(tx samplingExecer) error {
	if _, err := tx.Exec(`DELETE FROM sample_strata`); err != nil {
		return fmt.Errorf("clear history sample strata: %w", err)
	}
	rows, err := tx.Query(`SELECT 'session',s.id,s.sample_key,COALESCE(s.first_ts,''),COALESCE(s.repository_name,''),s.project,s.project_source,s.thread_kind,
		s.provider,COALESCE(s.cwd,''),COALESCE(s.branch,''),COALESCE(s.first_ts,''),COALESCE(s.last_ts,''),
		EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind IN ('codex_live','claude_project')),
		EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind='codex_archive'),
		EXISTS(SELECT 1 FROM preserved_snapshots ps JOIN locations l ON l.snapshot_id=ps.id WHERE ps.session_id=s.id AND l.available=1)
		FROM sessions s
		UNION ALL
		SELECT 'prompt',p.id,p.sample_key,COALESCE(p.timestamp,''),COALESCE(s.repository_name,''),s.project,s.project_source,s.thread_kind,
		s.provider,COALESCE(s.cwd,''),COALESCE(s.branch,''),COALESCE(p.timestamp,''),COALESCE(p.timestamp,''),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='provider_live'),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='provider_archive'),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='vault')
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE p.role='user' ORDER BY 1,2`)
	if err != nil {
		return fmt.Errorf("list history sampling metadata: %w", err)
	}
	values := []samplingMetadata{}
	for rows.Next() {
		var value samplingMetadata
		var timestamp string
		if err := rows.Scan(&value.unitKind, &value.unitID, &value.sampleKey, &timestamp, &value.repository, &value.project, &value.projectSource, &value.thread,
			&value.provider, &value.cwd, &value.branch, &value.firstDate, &value.lastDate,
			&value.providerLive, &value.providerArchive, &value.vaultAvailable); err != nil {
			rows.Close()
			return fmt.Errorf("scan history sampling metadata: %w", err)
		}
		value.month = sampleMonthText(timestamp)
		value.firstDate = sampleDateText(value.firstDate)
		value.lastDate = sampleDateText(value.lastDate)
		value.repository = normalizedSampleRepoText(value.repository)
		value.cwd = normalizedSampleCWD(value.cwd)
		if value.thread == "" {
			value.thread = "unknown"
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if err := insertSampleStrata(tx, value); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) refreshAllSampleStrata(sessionID int64) error {
	if _, err := tx.tx.Exec(`DELETE FROM sample_strata WHERE (unit_kind='session' AND unit_id=?) OR
		(unit_kind='prompt' AND unit_id IN (SELECT id FROM prompts WHERE session_id=?))`, sessionID, sessionID); err != nil {
		return fmt.Errorf("clear session sample strata: %w", err)
	}
	rows, err := tx.tx.Query(`SELECT 'session',s.id,s.sample_key,COALESCE(s.first_ts,''),COALESCE(s.repository_name,''),s.project,s.project_source,s.thread_kind,
		s.provider,COALESCE(s.cwd,''),COALESCE(s.branch,''),COALESCE(s.first_ts,''),COALESCE(s.last_ts,''),
		EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind IN ('codex_live','claude_project')),
		EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind='codex_archive'),
		EXISTS(SELECT 1 FROM preserved_snapshots ps JOIN locations l ON l.snapshot_id=ps.id WHERE ps.session_id=s.id AND l.available=1)
		FROM sessions s WHERE s.id=?
		UNION ALL
		SELECT 'prompt',p.id,p.sample_key,COALESCE(p.timestamp,''),COALESCE(s.repository_name,''),s.project,s.project_source,s.thread_kind,
		s.provider,COALESCE(s.cwd,''),COALESCE(s.branch,''),COALESCE(p.timestamp,''),COALESCE(p.timestamp,''),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='provider_live'),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='provider_archive'),
		EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='vault')
		FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE s.id=? AND p.role='user' ORDER BY 1,2`, sessionID, sessionID)
	if err != nil {
		return fmt.Errorf("read session sampling metadata: %w", err)
	}
	values := []samplingMetadata{}
	for rows.Next() {
		var value samplingMetadata
		var timestamp string
		if err := rows.Scan(&value.unitKind, &value.unitID, &value.sampleKey, &timestamp, &value.repository, &value.project, &value.projectSource, &value.thread,
			&value.provider, &value.cwd, &value.branch, &value.firstDate, &value.lastDate,
			&value.providerLive, &value.providerArchive, &value.vaultAvailable); err != nil {
			rows.Close()
			return fmt.Errorf("scan session sampling metadata: %w", err)
		}
		value.month = sampleMonthText(timestamp)
		value.firstDate = sampleDateText(value.firstDate)
		value.lastDate = sampleDateText(value.lastDate)
		value.repository = normalizedSampleRepoText(value.repository)
		value.cwd = normalizedSampleCWD(value.cwd)
		if value.thread == "" {
			value.thread = "unknown"
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, value := range values {
		if err := insertSampleStrata(tx.tx, value); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) refreshSessionSampleStratum(sessionID int64) error {
	if _, err := tx.tx.Exec(`DELETE FROM sample_strata WHERE unit_kind='session' AND unit_id=?`, sessionID); err != nil {
		return fmt.Errorf("clear session sample strata: %w", err)
	}
	row := tx.tx.QueryRow(`SELECT 'session',s.id,s.sample_key,COALESCE(s.first_ts,''),COALESCE(s.repository_name,''),s.project,s.project_source,s.thread_kind,
		s.provider,COALESCE(s.cwd,''),COALESCE(s.branch,''),COALESCE(s.first_ts,''),COALESCE(s.last_ts,''),
		EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind IN ('codex_live','claude_project')),
		EXISTS(SELECT 1 FROM source_heads sh WHERE sh.session_id=s.id AND sh.available=1 AND sh.source_kind='codex_archive'),
		EXISTS(SELECT 1 FROM preserved_snapshots ps JOIN locations l ON l.snapshot_id=ps.id WHERE ps.session_id=s.id AND l.available=1)
		FROM sessions s WHERE s.id=?`, sessionID)
	value, err := scanSamplingMetadata(row)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read session sampling metadata: %w", err)
	}
	return insertSampleStrata(tx.tx, value)
}

func (tx *Tx) refreshPromptSampleStrata(promptIDs []int64) error {
	for _, promptID := range promptIDs {
		if _, err := tx.tx.Exec(`DELETE FROM sample_strata WHERE unit_kind='prompt' AND unit_id=?`, promptID); err != nil {
			return fmt.Errorf("clear prompt sample strata: %w", err)
		}
		row := tx.tx.QueryRow(`SELECT 'prompt',p.id,p.sample_key,COALESCE(p.timestamp,''),COALESCE(s.repository_name,''),s.project,s.project_source,s.thread_kind,
			s.provider,COALESCE(s.cwd,''),COALESCE(s.branch,''),COALESCE(p.timestamp,''),COALESCE(p.timestamp,''),
			EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='provider_live'),
			EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='provider_archive'),
			EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1 AND l.kind='vault')
			FROM prompts p JOIN sessions s ON s.id=p.session_id WHERE p.id=? AND p.role='user'`, promptID)
		value, err := scanSamplingMetadata(row)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return fmt.Errorf("read prompt sampling metadata: %w", err)
		}
		if err := insertSampleStrata(tx.tx, value); err != nil {
			return err
		}
	}
	return nil
}

func scanSamplingMetadata(row interface{ Scan(...any) error }) (samplingMetadata, error) {
	var value samplingMetadata
	var timestamp string
	err := row.Scan(&value.unitKind, &value.unitID, &value.sampleKey, &timestamp, &value.repository, &value.project, &value.projectSource, &value.thread,
		&value.provider, &value.cwd, &value.branch, &value.firstDate, &value.lastDate,
		&value.providerLive, &value.providerArchive, &value.vaultAvailable)
	if err != nil {
		return samplingMetadata{}, err
	}
	value.month = sampleMonthText(timestamp)
	value.firstDate = sampleDateText(value.firstDate)
	value.lastDate = sampleDateText(value.lastDate)
	value.repository = normalizedSampleRepoText(value.repository)
	value.cwd = normalizedSampleCWD(value.cwd)
	if value.thread == "" {
		value.thread = "unknown"
	}
	return value, nil
}

func insertSampleStrata(tx interface {
	Exec(query string, args ...any) (sql.Result, error)
}, value samplingMetadata) error {
	dimensions := [][]string{
		{"month"}, {"repo"}, {"project"}, {"thread-kind"},
		{"cwd", "month"},
		{"month", "repo"}, {"month", "project"}, {"month", "thread-kind"}, {"project", "thread-kind"}, {"repo", "thread-kind"},
		{"month", "project", "thread-kind"}, {"month", "repo", "thread-kind"},
	}
	groupCWD := value.cwd
	if strings.TrimSpace(groupCWD) == "" {
		groupCWD = "unknown"
	}
	allValues := map[string]string{"month": value.month, "cwd": groupCWD, "repo": value.repository, "project": value.project, "project_source": value.projectSource, "thread-kind": value.thread}
	for _, current := range dimensions {
		expanded := expandedProjectDimensions(current)
		groups := make([]string, len(expanded))
		parts := make([]string, len(expanded))
		for i, dimension := range expanded {
			groups[i] = allValues[dimension]
			parts[i] = dimension + "=" + groups[i]
		}
		encoded, _ := json.Marshal(groups)
		groupKey := strings.Join(parts, "\x00")
		if err := insertSampleStratum(tx, value, strings.Join(current, ","), string(encoded), groupKey); err != nil {
			return fmt.Errorf("insert history sample stratum: %w", err)
		}
	}
	filters := map[string][]string{
		"provider": {value.provider}, "cwd": {value.cwd}, "repo-filter": {value.repository}, "project-filter": {value.project}, "branch": {value.branch},
	}
	if value.unitKind == SampleUnitPrompt {
		filters["date"] = []string{value.firstDate}
	} else {
		filters["first-date"] = []string{value.firstDate}
		filters["last-date"] = []string{value.lastDate}
	}
	sources := []string{}
	if value.providerLive || value.providerArchive {
		sources = append(sources, string(CatalogSourceProvider))
	}
	if value.providerLive {
		sources = append(sources, string(CatalogSourceProviderLive))
	}
	if value.providerArchive {
		sources = append(sources, string(CatalogSourceProviderArchive))
	}
	if value.vaultAvailable {
		sources = append(sources, string(CatalogSourceVault))
	}
	filters["source"] = sources
	for dimension, filterValues := range filters {
		for _, filterValue := range filterValues {
			encoded, _ := json.Marshal([]string{filterValue})
			if err := insertSampleStratum(tx, value, dimension, string(encoded), dimension+"="+filterValue); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertSampleStratum(tx interface {
	Exec(query string, args ...any) (sql.Result, error)
}, value samplingMetadata, dimension, encoded, groupKey string) error {
	_, err := tx.Exec(`INSERT INTO sample_strata(unit_kind,unit_id,dimensions,group_values,group_key,sample_key) VALUES(?,?,?,?,?,?)`,
		value.unitKind, value.unitID, dimension, encoded, sampleKey(groupKey), value.sampleKey)
	return err
}

func expandedProjectDimensions(dimensions []string) []string {
	result := make([]string, 0, len(dimensions)+1)
	for _, dimension := range dimensions {
		result = append(result, dimension)
		if dimension == "project" {
			result = append(result, "project_source")
		}
	}
	return result
}

// PrepareSampling performs the corpus-sized v7 population only during an
// explicit index operation, never while opening the database.
func (s *Store) PrepareSampling() error {
	ready, err := s.Meta("sampling_ready")
	if err != nil || ready == "1" {
		return err
	}
	return s.Transaction(func(tx *Tx) error {
		for _, table := range []string{"sessions", "prompts"} {
			rows, err := tx.tx.Query(`SELECT id,public_id FROM ` + table + ` WHERE length(sample_key)=0 ORDER BY id`)
			if err != nil {
				return fmt.Errorf("list %s for sampling preparation: %w", table, err)
			}
			var values []struct {
				id  int64
				key []byte
			}
			for rows.Next() {
				var id int64
				var publicID string
				if err := rows.Scan(&id, &publicID); err != nil {
					rows.Close()
					return err
				}
				values = append(values, struct {
					id  int64
					key []byte
				}{id: id, key: sampleKey(publicID)})
			}
			if err := rows.Close(); err != nil {
				return err
			}
			for _, value := range values {
				if _, err := tx.tx.Exec(`UPDATE `+table+` SET sample_key=? WHERE id=?`, value.key, value.id); err != nil {
					return err
				}
			}
		}
		if err := populateSampleStrata(tx.tx); err != nil {
			return err
		}
		if err := tx.SetMeta("sampling_ready", "1"); err != nil {
			return err
		}
		return tx.advanceGenerationIf(true)
	})
}

// Sample returns logical units by walking indexed SHA-256 keys around a seed
// pivot. It never uses a corpus-wide random sort.
func (s *Store) Sample(query SampleQuery) (SampleResult, error) {
	if query.Cursor != "" {
		return SampleResult{}, errors.New("history sample does not support cursors")
	}
	query.PromptQuery, _ = normalizePromptQuery(query.PromptQuery, 1)
	query.PromptQuery.Limit = 1
	if err := validatePromptQuery(query.PromptQuery); err != nil {
		return SampleResult{}, err
	}
	if query.Unit == "" {
		query.Unit = SampleUnitPrompt
	}
	if query.Unit != SampleUnitPrompt && query.Unit != SampleUnitSession {
		return SampleResult{}, fmt.Errorf("invalid history sample unit %q", query.Unit)
	}
	if query.Unit == SampleUnitSession && (query.MinLength != 0 || query.OnePerSession || len(query.PromptKinds) > 0 || query.ExcludeControl || query.AllOccurrences) {
		return SampleResult{}, errors.New("history session sampling does not support prompt-only filters or occurrence expansion")
	}
	if query.Count == 0 {
		query.Count = 25
	}
	if query.Count < 1 || query.Count > 100 {
		return SampleResult{}, errors.New("history sample count must be between 1 and 100")
	}
	if query.MinLength < 0 {
		return SampleResult{}, errors.New("history sample minimum length must be zero or greater")
	}
	if query.Seed == "" {
		query.Seed = "tokenomnom"
	}
	groups, err := normalizeSampleGroups(query.GroupBy)
	if err != nil {
		return SampleResult{}, err
	}
	query.GroupBy = groups
	if query.Strategy == "" {
		query.Strategy = SampleStrategyRandom
		if len(groups) > 0 {
			query.Strategy = SampleStrategyStratified
		}
	}
	if query.Strategy != SampleStrategyRandom && query.Strategy != SampleStrategyStratified {
		return SampleResult{}, fmt.Errorf("invalid history sample strategy %q", query.Strategy)
	}
	if query.Strategy == SampleStrategyStratified && len(groups) == 0 {
		return SampleResult{}, errors.New("stratified history sampling requires --group-by")
	}
	ready, err := s.Meta("sampling_ready")
	if err != nil {
		return SampleResult{}, err
	}
	if ready != "1" {
		return SampleResult{}, errors.New("history sampling index is not ready; run history index")
	}

	generation, err := s.indexGeneration()
	if err != nil {
		return SampleResult{}, err
	}
	pivot := sampleKey(query.Seed)
	selectedGroups := []sampleGroup{{values: map[string]string{}, key: ""}}
	if query.Strategy == SampleStrategyStratified {
		selectedGroups, err = s.sampleGroups(query)
		if err != nil {
			return SampleResult{}, err
		}
	}
	if query.Strategy == SampleStrategyRandom {
		selectedGroups = []sampleGroup{{values: map[string]string{}, key: ""}}
	}
	ids := make([]sampledID, 0, query.Count)
	seenSessions := map[string]bool{}
	accept := func(value sampledID) (bool, error) {
		if !query.OnePerSession || query.Unit == SampleUnitSession {
			ids = append(ids, value)
			return true, nil
		}
		sessionID := value.sessionID
		if seenSessions[sessionID] {
			return false, nil
		}
		seenSessions[sessionID] = true
		query.excludedSessionIDs = append(query.excludedSessionIDs, sessionID)
		ids = append(ids, value)
		return true, nil
	}
	states := make([]sampleState, 0, len(selectedGroups))
	if query.Strategy == SampleStrategyStratified {
		seeded := make([]seededSampleState, 0, len(selectedGroups))
		for _, group := range selectedGroups {
			state := sampleState{group: group}
			first, found, err := s.nextSampleID(query, pivot, &state)
			if err != nil {
				return SampleResult{}, err
			}
			if found {
				seeded = append(seeded, seededSampleState{state: state, first: first})
			}
		}
		if len(seeded) > query.Count {
			selectedCount := query.Count
			if query.OnePerSession {
				selectedCount = len(seeded)
			}
			seeded = seededStatesAroundPivot(seeded, pivot, selectedCount)
		}
		if !query.OnePerSession {
			sort.Slice(seeded, func(i, j int) bool { return seeded[i].state.group.key < seeded[j].state.group.key })
		}
		for _, value := range seeded {
			state := value.state
			accepted, err := accept(value.first)
			if err != nil {
				return SampleResult{}, err
			}
			for query.OnePerSession && !accepted {
				next, found, err := s.nextSampleID(query, pivot, &state)
				if err != nil {
					return SampleResult{}, err
				}
				if !found {
					break
				}
				accepted, err = accept(next)
				if err != nil {
					return SampleResult{}, err
				}
			}
			states = append(states, state)
			if len(ids) == query.Count {
				break
			}
		}
	} else {
		states = append(states, sampleState{group: selectedGroups[0]})
	}
	for len(ids) < query.Count {
		progress := false
		for i := range states {
			if len(ids) == query.Count {
				break
			}
			value, found, err := s.nextSampleID(query, pivot, &states[i])
			if err != nil {
				return SampleResult{}, err
			}
			if found {
				accepted, err := accept(value)
				if err != nil {
					return SampleResult{}, err
				}
				progress = progress || accepted || found
			}
		}
		if !progress {
			break
		}
	}
	if query.Strategy == SampleStrategyRandom && len(query.GroupBy) > 0 {
		for i := range ids {
			group, err := s.sampleGroupForID(query, ids[i].publicID)
			if err != nil {
				return SampleResult{}, err
			}
			ids[i].group = group
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i].group.key != ids[j].group.key {
			return ids[i].group.key < ids[j].group.key
		}
		if compared := bytes.Compare(ids[i].key, ids[j].key); compared != 0 {
			return compared < 0
		}
		return ids[i].publicID < ids[j].publicID
	})
	items := make([]SampleItem, 0, len(ids))
	for _, id := range ids {
		item := SampleItem{Unit: query.Unit, Groups: id.group.values}
		if query.Unit == SampleUnitPrompt {
			prompt, err := s.getPrompt(id.publicID, query.AllOccurrences)
			if err != nil {
				return SampleResult{}, err
			}
			if !query.IncludeText {
				prompt.Text = nil
			}
			if len(item.Groups) == 0 && len(query.GroupBy) > 0 {
				item.Groups = promptSampleGroups(prompt, query.GroupBy)
			}
			item.Prompt = &prompt
		} else {
			session, err := s.GetSession(id.publicID)
			if err != nil {
				return SampleResult{}, err
			}
			item.Session = &session
			if len(item.Groups) == 0 && len(query.GroupBy) > 0 {
				item.Groups = sessionSampleGroups(session, query.GroupBy)
			}
			if query.IncludeText {
				item.Text, err = s.sampleSessionText(id.publicID, pivot)
				if err != nil {
					return SampleResult{}, err
				}
			}
		}
		items = append(items, item)
	}
	coverage := sampleCoverage(items)
	return SampleResult{Items: items, Unit: query.Unit, Strategy: query.Strategy, GroupBy: query.GroupBy,
		Count: len(items), Seed: query.Seed, Generation: generation, Coverage: coverage, Warnings: sampleWarnings(query)}, nil
}

func (s *Store) sampleGroupForID(query SampleQuery, publicID string) (sampleGroup, error) {
	table, idColumn := "prompts", "prompt"
	if query.Unit == SampleUnitSession {
		table, idColumn = "sessions", "session"
	}
	var encoded string
	var hash []byte
	err := s.runner.QueryRow(`SELECT ss.group_values,ss.group_key FROM sample_strata ss JOIN `+table+` unit ON unit.id=ss.unit_id
		WHERE ss.unit_kind=? AND ss.dimensions=? AND unit.public_id=?`, idColumn, strings.Join(query.GroupBy, ","), publicID).Scan(&encoded, &hash)
	if err != nil {
		return sampleGroup{}, fmt.Errorf("read history sample group: %w", err)
	}
	var values []string
	expanded := expandedProjectDimensions(query.GroupBy)
	if err := json.Unmarshal([]byte(encoded), &values); err != nil || len(values) != len(expanded) {
		return sampleGroup{}, errors.New("invalid indexed history sample group")
	}
	mapping := make(map[string]string, len(values))
	parts := make([]string, len(values))
	for i, value := range values {
		mapping[expanded[i]] = value
		parts[i] = expanded[i] + "=" + value
	}
	return sampleGroup{values: mapping, key: strings.Join(parts, "\x00"), hash: hash, encoded: encoded}, nil
}

func promptSampleGroups(prompt PromptResult, groups []string) map[string]string {
	month := sampleMonth(prompt.Timestamp)
	values := make(map[string]string, len(groups))
	for _, group := range groups {
		switch group {
		case "month":
			values[group] = month
		case "repo":
			values[group] = normalizedSampleRepo(prompt.RepositoryName)
		case "project":
			values[group] = prompt.Project
			values["project_source"] = string(prompt.ProjectSource)
		case "cwd":
			values[group] = normalizedSampleCWD(prompt.CWD)
		case "thread-kind":
			values[group] = string(prompt.ThreadKind)
		}
	}
	return values
}

func sessionSampleGroups(session CatalogSession, groups []string) map[string]string {
	month := sampleMonth(session.FirstTimestamp)
	values := make(map[string]string, len(groups))
	for _, group := range groups {
		switch group {
		case "month":
			values[group] = month
		case "repo":
			values[group] = normalizedSampleRepo(session.RepositoryName)
		case "project":
			values[group] = session.Project
			values["project_source"] = string(session.ProjectSource)
		case "cwd":
			values[group] = normalizedSampleCWD(session.CWD)
		case "thread-kind":
			values[group] = string(session.ThreadKind)
		}
	}
	return values
}

func sampleMonth(value *string) string {
	if value == nil {
		return "unknown"
	}
	return sampleMonthText(*value)
}

func sampleMonthText(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "unknown"
	}
	return parsed.UTC().Format("2006-01")
}

func sampleDateText(value string) string {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "unknown"
	}
	return parsed.UTC().Format("2006-01-02")
}

func normalizedSampleRepo(value *string) string {
	if value == nil {
		return "unknown"
	}
	return normalizedSampleRepoText(*value)
}

func normalizedSampleRepoText(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizedSampleCWD(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func normalizeSampleGroups(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value != "month" && value != "cwd" && value != "repo" && value != "project" && value != "thread-kind" {
			return nil, fmt.Errorf("invalid history sample group %q", value)
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	combination := strings.Join(result, ",")
	if combination != "" && !supportedSampleGroupCombination(combination) {
		return nil, fmt.Errorf("unsupported history sample group combination %q", combination)
	}
	return result, nil
}

func supportedSampleGroupCombination(value string) bool {
	switch value {
	case "month", "cwd", "repo", "project", "thread-kind",
		"cwd,month", "month,repo", "month,project", "month,thread-kind", "project,thread-kind", "repo,thread-kind",
		"month,project,thread-kind", "month,repo,thread-kind":
		return true
	default:
		return false
	}
}

func (s *Store) sampleGroups(query SampleQuery) ([]sampleGroup, error) {
	rows, err := s.runner.Query(`SELECT group_values,group_key FROM sample_groups WHERE unit_kind=? AND dimensions=? ORDER BY group_key`,
		query.Unit, strings.Join(query.GroupBy, ","))
	if err != nil {
		return nil, fmt.Errorf("list history sample groups: %w", err)
	}
	defer rows.Close()
	result := []sampleGroup{}
	for rows.Next() {
		var encoded string
		var hash []byte
		if err := rows.Scan(&encoded, &hash); err != nil {
			return nil, fmt.Errorf("scan history sample group: %w", err)
		}
		var values []string
		expanded := expandedProjectDimensions(query.GroupBy)
		if err := json.Unmarshal([]byte(encoded), &values); err != nil || len(values) != len(expanded) {
			return nil, errors.New("invalid indexed history sample group")
		}
		mapping := make(map[string]string, len(values))
		parts := make([]string, len(values))
		for i, value := range values {
			mapping[expanded[i]] = value
			parts[i] = expanded[i] + "=" + value
		}
		key := strings.Join(parts, "\x00")
		result = append(result, sampleGroup{values: mapping, key: key, hash: append([]byte(nil), hash...), encoded: encoded})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key < result[j].key })
	return result, nil
}

func seededStatesAroundPivot(values []seededSampleState, pivot []byte, count int) []seededSampleState {
	sort.Slice(values, func(i, j int) bool {
		if compared := bytes.Compare(values[i].state.group.hash, values[j].state.group.hash); compared != 0 {
			return compared < 0
		}
		return values[i].state.group.key < values[j].state.group.key
	})
	start := sort.Search(len(values), func(i int) bool { return bytes.Compare(values[i].state.group.hash, pivot) >= 0 })
	selected := make([]seededSampleState, 0, count)
	for offset := 0; offset < count; offset++ {
		selected = append(selected, values[(start+offset)%len(values)])
	}
	return selected
}

func (s *Store) nextSampleID(query SampleQuery, pivot []byte, state *sampleState) (sampledID, bool, error) {
	if state.done {
		return sampledID{}, false, nil
	}
	where, whereArgs, from := sampleBaseQuery(query)
	joinArgs := []any{}
	keyColumn, idColumn, sessionColumn := "p.sample_key", "p.public_id", "s.public_id"
	if query.Unit == SampleUnitSession {
		keyColumn, idColumn, sessionColumn = "s.sample_key", "s.public_id", "s.public_id"
	}
	if state.group.encoded != "" {
		unitID := "p.id"
		if query.Unit == SampleUnitSession {
			unitID = "s.id"
		}
		from += ` JOIN sample_strata ssg ON ssg.unit_kind=? AND ssg.unit_id=` + unitID
		joinArgs = append(joinArgs, query.Unit)
		where = append(where, `ssg.dimensions=?`, `ssg.group_values=?`)
		whereArgs = append(whereArgs, strings.Join(query.GroupBy, ","), state.group.encoded)
		keyColumn = "ssg.sample_key"
	}
	for index, filter := range indexedSampleFilters(query) {
		unitID := "p.id"
		if query.Unit == SampleUnitSession {
			unitID = "s.id"
		}
		alias := fmt.Sprintf("sf%d", index)
		from += ` JOIN sample_strata ` + alias + ` ON ` + alias + `.unit_kind=? AND ` + alias + `.unit_id=` + unitID
		joinArgs = append(joinArgs, query.Unit)
		where = append(where, alias+`.dimensions=?`)
		whereArgs = append(whereArgs, filter.dimension)
		for _, condition := range filter.conditions {
			where = append(where, alias+`.group_values`+condition.operator+`?`)
			whereArgs = append(whereArgs, condition.value)
		}
		if state.group.encoded == "" && index == 0 {
			keyColumn = alias + ".sample_key"
		}
	}
	args := append(joinArgs, whereArgs...)
	if len(state.lastKey) == 0 {
		if state.wrapped {
			where = append(where, keyColumn+"<?")
			args = append(args, pivot)
		} else {
			where = append(where, keyColumn+">=?")
			args = append(args, pivot)
		}
	} else {
		where = append(where, "("+keyColumn+">? OR ("+keyColumn+"=? AND "+idColumn+">?))")
		args = append(args, state.lastKey, state.lastKey, state.lastID)
		if state.wrapped {
			where = append(where, keyColumn+"<?")
			args = append(args, pivot)
		}
	}
	var id, sessionID string
	var key []byte
	err := s.runner.QueryRow(`SELECT `+idColumn+`,`+keyColumn+`,`+sessionColumn+` FROM `+from+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+keyColumn+`,`+idColumn+` LIMIT 1`, args...).Scan(&id, &key, &sessionID)
	if err == sql.ErrNoRows && !state.wrapped {
		state.wrapped, state.lastKey, state.lastID = true, nil, ""
		return s.nextSampleID(query, pivot, state)
	}
	if err == sql.ErrNoRows {
		state.done = true
		return sampledID{}, false, nil
	}
	if err != nil {
		return sampledID{}, false, fmt.Errorf("select history sample unit: %w", err)
	}
	state.lastKey, state.lastID = append(state.lastKey[:0], key...), id
	return sampledID{publicID: id, key: append([]byte(nil), key...), sessionID: sessionID, group: state.group}, true, nil
}

type sampleFilterCondition struct {
	operator string
	value    string
}

type indexedSampleFilter struct {
	dimension  string
	conditions []sampleFilterCondition
}

func indexedSampleFilters(query SampleQuery) []indexedSampleFilter {
	filters := []indexedSampleFilter{}
	exact := func(dimension, value string) {
		encoded, _ := json.Marshal([]string{value})
		filters = append(filters, indexedSampleFilter{dimension: dimension, conditions: []sampleFilterCondition{{operator: "=", value: string(encoded)}}})
	}
	if query.Repo != "" {
		exact("repo-filter", normalizedSampleRepoText(query.Repo))
	}
	if query.Project != "" {
		exact("project-filter", query.Project)
	}
	if query.CWD != "" {
		exact("cwd", query.CWD)
	}
	if query.Branch != "" {
		exact("branch", query.Branch)
	}
	if query.Provider != "" {
		exact("provider", string(query.Provider))
	}
	if query.ThreadKind != "" && query.ThreadKind != "all" {
		exact("thread-kind", query.ThreadKind)
	}
	if query.Source != "" && query.Source != CatalogSourceAny {
		exact("source", string(query.Source))
	}
	dateDimension := "date"
	if query.Unit == SampleUnitSession {
		dateDimension = "last-date"
	}
	if query.Since != nil {
		encoded, _ := json.Marshal([]string{query.Since.UTC().Format("2006-01-02")})
		filters = append(filters, indexedSampleFilter{dimension: dateDimension, conditions: []sampleFilterCondition{
			{operator: ">=", value: string(encoded)},
			{operator: "<>", value: `["unknown"]`},
		}})
	}
	if query.Until != nil {
		untilDimension := dateDimension
		if query.Unit == SampleUnitSession {
			untilDimension = "first-date"
		}
		encoded, _ := json.Marshal([]string{query.Until.UTC().Format("2006-01-02")})
		condition := indexedSampleFilter{dimension: untilDimension, conditions: []sampleFilterCondition{
			{operator: "<=", value: string(encoded)},
			{operator: "<>", value: `["unknown"]`},
		}}
		if len(filters) > 0 && filters[len(filters)-1].dimension == untilDimension {
			filters[len(filters)-1].conditions = append(filters[len(filters)-1].conditions, condition.conditions...)
		} else {
			filters = append(filters, condition)
		}
	}
	return filters
}

func sampleBaseQuery(query SampleQuery) ([]string, []any, string) {
	if query.Unit == SampleUnitSession {
		catalog := CatalogQuery{Provider: query.Provider, Since: query.Since, Until: query.Until, CWD: query.CWD,
			Repo: query.Repo, Project: query.Project, Branch: query.Branch, Source: query.Source, ThreadKind: query.ThreadKind}
		where, args := catalogWhere(catalog, true)
		where = append(where, `EXISTS(SELECT 1 FROM prompts p JOIN occurrences o ON o.prompt_id=p.id JOIN locations l ON l.id=o.location_id
			WHERE p.session_id=s.id AND p.searchable=1 AND p.role='user' AND p.prompt_kind='human' AND l.available=1)`)
		return where, args, "sessions s"
	}
	where, args := promptWhere(query.PromptQuery, true, "p", "s")
	if query.MinLength > 0 {
		where = append(where, "length(p.clean_text)>=?")
		args = append(args, query.MinLength)
	}
	if query.OnePerSession && len(query.excludedSessionIDs) > 0 {
		placeholders := make([]string, len(query.excludedSessionIDs))
		for index, sessionID := range query.excludedSessionIDs {
			placeholders[index] = "?"
			args = append(args, sessionID)
		}
		where = append(where, "s.public_id NOT IN ("+strings.Join(placeholders, ",")+")")
	}
	return where, args, "prompts p JOIN sessions s ON s.id=p.session_id"
}

func sampleCoverage(items []SampleItem) SampleCoverage {
	var coverage SampleCoverage
	var firstInstant, lastInstant time.Time
	sessions := make(map[string]bool, len(items))
	for _, item := range items {
		var sessionID string
		var firstTimestamp, lastTimestamp, repository, branch *string
		var projectSource history.ProjectSource
		var threadKind history.ThreadKind
		if item.Prompt != nil {
			sessionID = item.Prompt.SessionID
			firstTimestamp = item.Prompt.Timestamp
			lastTimestamp = item.Prompt.Timestamp
			repository = item.Prompt.RepositoryName
			branch = item.Prompt.Branch
			projectSource = item.Prompt.ProjectSource
			threadKind = item.Prompt.ThreadKind
		} else if item.Session != nil {
			sessionID = item.Session.SessionID
			firstTimestamp = item.Session.FirstTimestamp
			lastTimestamp = item.Session.LastTimestamp
			repository = item.Session.RepositoryName
			branch = item.Session.Branch
			projectSource = item.Session.ProjectSource
			threadKind = item.Session.ThreadKind
		}
		if firstTimestamp != nil {
			instant, err := time.Parse(time.RFC3339Nano, *firstTimestamp)
			if err == nil && (coverage.FirstTimestamp == nil || instant.Before(firstInstant)) {
				value := *firstTimestamp
				coverage.FirstTimestamp = &value
				firstInstant = instant
			}
		}
		if lastTimestamp != nil {
			instant, err := time.Parse(time.RFC3339Nano, *lastTimestamp)
			if err == nil && (coverage.LastTimestamp == nil || instant.After(lastInstant)) {
				value := *lastTimestamp
				coverage.LastTimestamp = &value
				lastInstant = instant
			}
		}
		if sessions[sessionID] {
			continue
		}
		sessions[sessionID] = true
		if repository == nil || *repository == "" {
			coverage.Repository.Unknown++
		} else {
			coverage.Repository.Known++
		}
		if branch == nil || *branch == "" {
			coverage.Branch.Unknown++
		} else {
			coverage.Branch.Known++
		}
		switch projectSource {
		case history.ProjectSourceGit:
			coverage.Project.Git++
		case history.ProjectSourceCWD:
			coverage.Project.CWD++
		default:
			coverage.Project.Unknown++
		}
		switch threadKind {
		case history.ThreadRoot:
			coverage.ThreadKind.Root++
		case history.ThreadSubagent:
			coverage.ThreadKind.Subagent++
		default:
			coverage.ThreadKind.Unknown++
		}
	}
	return coverage
}

func sampleWarnings(query SampleQuery) []string {
	warnings := []string{}
	if query.Repo != "" {
		warnings = append(warnings, "--repo excludes sessions with unknown repository metadata; repository coverage is Codex-complete and Claude-partial")
	}
	if query.Branch != "" {
		warnings = append(warnings, "--branch excludes sessions with unknown branch metadata; branch coverage is Codex-complete and Claude-partial")
	}
	return warnings
}

func (s *Store) sampleSessionText(sessionID string, pivot []byte) (*string, error) {
	var value string
	err := s.runner.QueryRow(`SELECT p.clean_text FROM prompts p JOIN sessions s ON s.id=p.session_id
		WHERE s.public_id=? AND p.searchable=1 AND p.role='user' AND p.prompt_kind='human' AND p.occurrence_count>0 AND p.sample_key>=?
		ORDER BY p.sample_key,p.public_id LIMIT 1`, sessionID, pivot).Scan(&value)
	if err == sql.ErrNoRows {
		err = s.runner.QueryRow(`SELECT p.clean_text FROM prompts p JOIN sessions s ON s.id=p.session_id
			WHERE s.public_id=? AND p.searchable=1 AND p.role='user' AND p.prompt_kind='human' AND p.occurrence_count>0 AND p.sample_key<?
			ORDER BY p.sample_key,p.public_id LIMIT 1`, sessionID, pivot).Scan(&value)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read sampled session text: %w", err)
	}
	return &value, nil
}
