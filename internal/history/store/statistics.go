package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

// StatisticsQuery selects corpus aggregates. Group dimensions are validated
// against a fixed SQL expression map.
type StatisticsQuery struct {
	PromptQuery
	GroupBy       []string
	Top           int
	ProjectSource string
}

// StatisticsGroup contains text-free aggregates for one dimension tuple.
type StatisticsGroup struct {
	Values            map[string]string `json:"values"`
	LogicalSessions   int               `json:"logical_sessions"`
	LogicalPrompts    int               `json:"logical_prompts"`
	PromptOccurrences int               `json:"prompt_occurrences"`
	PromptLengthBytes int64             `json:"prompt_length_bytes"`
}

// Statistics is a SQL-aggregated, prompt-text-free corpus summary.
type Statistics struct {
	Scope                    string               `json:"scope"`
	LogicalSessions          int                  `json:"logical_sessions"`
	MutableSourceHeads       int                  `json:"mutable_source_heads"`
	PreservedSnapshots       int                  `json:"preserved_snapshots"`
	LogicalPrompts           int                  `json:"logical_prompts"`
	PromptOccurrences        int                  `json:"prompt_occurrences"`
	ActiveDays               int                  `json:"active_days"`
	PromptLengthTotalBytes   int64                `json:"prompt_length_total_bytes"`
	PromptLengthMedianBytes  float64              `json:"prompt_length_median_bytes"`
	ProviderLiveAvailable    int                  `json:"provider_live_available"`
	ProviderArchiveAvailable int                  `json:"provider_archive_available"`
	VaultAvailable           int                  `json:"vault_available"`
	IndexSizeBytes           int64                `json:"index_size_bytes"`
	StaleCount               int                  `json:"stale_count"`
	ErrorCount               int                  `json:"error_count"`
	UnscopedErrorsExcluded   int                  `json:"unscoped_errors_excluded"`
	OversizedCount           int                  `json:"oversized_count"`
	RoleCounts               StatisticsRoleCounts `json:"role_counts"`
	Groups                   []StatisticsGroup    `json:"groups"`
	GroupsTruncated          bool                 `json:"groups_truncated"`
	Other                    *StatisticsGroup     `json:"other"`
	Coverage                 QueryCoverage        `json:"coverage"`
	Warnings                 []string             `json:"-"`
	Generation               int64                `json:"index_generation"`
	ProjectSource            string               `json:"project_source"`
}

// StatisticsRoleCount is a text-free aggregate for one searchable role.
type StatisticsRoleCount struct {
	LogicalPrompts    int   `json:"logical_prompts"`
	PromptOccurrences int   `json:"prompt_occurrences"`
	PromptLengthBytes int64 `json:"prompt_length_bytes"`
}

// StatisticsRoleCounts keeps default user totals while disclosing role sizes.
type StatisticsRoleCounts struct {
	User      StatisticsRoleCount `json:"user"`
	Assistant StatisticsRoleCount `json:"assistant"`
}

// Statistics computes all body-dependent aggregates in SQLite.
func (s *Store) Statistics(query StatisticsQuery) (Statistics, error) {
	if query.Top == 0 {
		query.Top = 20
	}
	if query.Top < 1 || query.Top > 100 {
		return Statistics{}, errors.New("history stats top must be between 1 and 100")
	}
	if query.ProjectSource == "" {
		query.ProjectSource = SampleProjectSourceAny
	}
	if !validSampleProjectSource(query.ProjectSource) {
		return Statistics{}, fmt.Errorf("invalid history stats project source %q", query.ProjectSource)
	}
	if query.Source == "" {
		query.Source = CatalogSourceAny
	}
	if !validCatalogSource(query.Source) {
		return Statistics{}, fmt.Errorf("invalid history source %q", query.Source)
	}
	query.ThreadKind = normalizedThreadKindFilter(query.ThreadKind)
	if query.Role == "" {
		query.Role = string(history.RoleUser)
	}
	var err error
	query.PromptQuery, err = s.resolvePromptRole(query.PromptQuery)
	if err != nil {
		return Statistics{}, err
	}
	if err := validatePromptQuery(query.PromptQuery); err != nil {
		return Statistics{}, err
	}
	if !validThreadKindFilter(query.ThreadKind) {
		return Statistics{}, fmt.Errorf("invalid history thread kind %q", query.ThreadKind)
	}
	dimensions, expressions, err := statisticsDimensions(query.GroupBy)
	if err != nil {
		return Statistics{}, err
	}
	groupByRole := false
	groupByProject := false
	for _, dimension := range dimensions {
		groupByRole = groupByRole || dimension == "role"
		groupByProject = groupByProject || dimension == "project"
	}
	coverage, warnings, err := s.promptCoverageForProjectSource(query.PromptQuery, query.ProjectSource)
	if err != nil {
		return Statistics{}, err
	}
	if query.AssistantConsent {
		roleWarningQuery := query.PromptQuery
		roleWarningQuery.Role = "any"
		for _, warning := range assistantIndexWarnings(roleWarningQuery) {
			found := false
			for _, existing := range warnings {
				found = found || existing == warning
			}
			if !found {
				warnings = append(warnings, warning)
			}
		}
	}
	generation, err := s.indexGeneration()
	if err != nil {
		return Statistics{}, err
	}
	sessionWhere, sessionArgs := catalogWhere(CatalogQuery{
		Provider: query.Provider, Since: query.Since, Until: query.Until, CWD: query.CWD,
		Repo: query.Repo, Project: query.Project, Branch: query.Branch, Source: query.Source, ThreadKind: query.ThreadKind,
	}, true)
	sessionWhere, sessionArgs = appendProjectSourceFilter(sessionWhere, sessionArgs, "s", query.ProjectSource)
	promptFilters, promptArgs := promptWhere(query.PromptQuery, true, "p", "s")
	promptFilters, promptArgs = appendProjectSourceFilter(promptFilters, promptArgs, "s", query.ProjectSource)
	oversizedFilters := append([]string{"p.oversized=1"}, promptFilters[1:]...)
	cte := `WITH selected_sessions AS (SELECT s.* FROM sessions s WHERE ` + strings.Join(sessionWhere, " AND ") + `),
		available_prompts AS (
			SELECT p.*, (SELECT COUNT(*) FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1) AS available_occurrences
			FROM prompts p JOIN selected_sessions s ON s.id=p.session_id WHERE ` + strings.Join(promptFilters, " AND ") + `
		),
		matching_oversized AS (
			SELECT p.id FROM prompts p JOIN selected_sessions s ON s.id=p.session_id WHERE ` + strings.Join(oversizedFilters, " AND ") + `
		)`
	promptScopedSessions := true
	cte += `, statistics_sessions AS (SELECT s.id FROM selected_sessions s WHERE EXISTS(SELECT 1 FROM available_prompts p WHERE p.session_id=s.id))`
	statement := cte + ` SELECT
		(SELECT COUNT(*) FROM statistics_sessions),
		(SELECT COUNT(*) FROM source_heads sh WHERE sh.session_id IN (SELECT id FROM statistics_sessions)),
		(SELECT COUNT(*) FROM preserved_snapshots ps WHERE ps.session_id IN (SELECT id FROM statistics_sessions)),
		(SELECT COUNT(*) FROM available_prompts p WHERE p.session_id IN (SELECT id FROM statistics_sessions)),
		COALESCE((SELECT SUM(available_occurrences) FROM available_prompts p WHERE p.session_id IN (SELECT id FROM statistics_sessions)),0),
		(SELECT COUNT(DISTINCT substr(p.timestamp,1,10)) FROM available_prompts p WHERE p.session_id IN (SELECT id FROM statistics_sessions) AND p.timestamp IS NOT NULL AND p.timestamp<>''),
		COALESCE((SELECT SUM(length(CAST(p.clean_text AS BLOB))) FROM available_prompts p WHERE p.session_id IN (SELECT id FROM statistics_sessions)),0),
		COALESCE((WITH ordered AS (
			SELECT length(CAST(p.clean_text AS BLOB)) AS bytes,
			ROW_NUMBER() OVER (ORDER BY length(CAST(p.clean_text AS BLOB))) AS row_number,
			COUNT(*) OVER () AS row_count
			FROM available_prompts p WHERE p.session_id IN (SELECT id FROM statistics_sessions))
			SELECT AVG(bytes) FROM ordered WHERE row_number IN ((row_count+1)/2,(row_count+2)/2)),0),
		(SELECT COUNT(*) FROM source_heads sh WHERE sh.session_id IN (SELECT id FROM statistics_sessions) AND sh.available=1 AND sh.source_kind IN ('codex_live','claude_project')),
		(SELECT COUNT(*) FROM source_heads sh WHERE sh.session_id IN (SELECT id FROM statistics_sessions) AND sh.available=1 AND sh.source_kind='codex_archive'),
		(SELECT COUNT(*) FROM locations l JOIN preserved_snapshots ps ON ps.id=l.snapshot_id WHERE ps.session_id IN (SELECT id FROM statistics_sessions) AND l.available=1),
		((SELECT COUNT(*) FROM source_heads sh WHERE sh.session_id IN (SELECT id FROM statistics_sessions) AND sh.available=1 AND sh.extractor_version<>?)+
		 (SELECT COUNT(*) FROM preserved_snapshots ps WHERE ps.session_id IN (SELECT id FROM statistics_sessions) AND ps.extractor_version<>?)),
		((SELECT COUNT(*) FROM source_heads sh WHERE sh.session_id IN (SELECT id FROM statistics_sessions) AND sh.last_error<>'')+
		 (SELECT COUNT(*) FROM source_errors se WHERE EXISTS(SELECT 1 FROM source_heads sh WHERE sh.provider=se.provider AND sh.source_path=se.source_path AND sh.session_id IN (SELECT id FROM statistics_sessions)))+
		 (SELECT COUNT(*) FROM vault_bundle_state vb WHERE vb.last_error<>'' AND EXISTS(
			SELECT 1 FROM locations l JOIN preserved_snapshots ps ON ps.id=l.snapshot_id
			WHERE l.archive=vb.archive AND ps.session_id IN (SELECT id FROM statistics_sessions)))),
		(SELECT COUNT(*) FROM matching_oversized)`
	args := append([]any{}, sessionArgs...)
	args = append(args, promptArgs...)
	args = append(args, promptArgs...)
	args = append(args, history.ExtractorVersion, history.ExtractorVersion)
	value := Statistics{Scope: "searchable_prompt_corpus", Coverage: coverage, Warnings: warnings, Generation: generation, Groups: []StatisticsGroup{}, ProjectSource: query.ProjectSource}
	if err := s.runner.QueryRow(statement, args...).Scan(
		&value.LogicalSessions, &value.MutableSourceHeads, &value.PreservedSnapshots,
		&value.LogicalPrompts, &value.PromptOccurrences, &value.ActiveDays,
		&value.PromptLengthTotalBytes, &value.PromptLengthMedianBytes,
		&value.ProviderLiveAvailable, &value.ProviderArchiveAvailable, &value.VaultAvailable,
		&value.StaleCount, &value.ErrorCount, &value.OversizedCount,
	); err != nil {
		return Statistics{}, fmt.Errorf("read history statistics: %w", err)
	}
	roleQuery := query.PromptQuery
	roleQuery.Role = "any"
	roleFilters, roleArgs := promptWhere(roleQuery, true, "p", "s")
	roleFilters, roleArgs = appendProjectSourceFilter(roleFilters, roleArgs, "s", query.ProjectSource)
	roleStatement := `WITH selected_sessions AS (SELECT s.* FROM sessions s WHERE ` + strings.Join(sessionWhere, " AND ") + `),
		available_prompts AS (
			SELECT p.*,(SELECT COUNT(*) FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1) AS available_occurrences
			FROM prompts p JOIN selected_sessions s ON s.id=p.session_id WHERE ` + strings.Join(roleFilters, " AND ") + `)
		SELECT
		COUNT(CASE WHEN role='user' THEN 1 END),COALESCE(SUM(CASE WHEN role='user' THEN available_occurrences ELSE 0 END),0),COALESCE(SUM(CASE WHEN role='user' THEN length(CAST(clean_text AS BLOB)) ELSE 0 END),0),
		COUNT(CASE WHEN role='assistant' THEN 1 END),COALESCE(SUM(CASE WHEN role='assistant' THEN available_occurrences ELSE 0 END),0),COALESCE(SUM(CASE WHEN role='assistant' THEN length(CAST(clean_text AS BLOB)) ELSE 0 END),0)
		FROM available_prompts`
	roleQueryArgs := append([]any{}, sessionArgs...)
	roleQueryArgs = append(roleQueryArgs, roleArgs...)
	if err := s.runner.QueryRow(roleStatement, roleQueryArgs...).Scan(
		&value.RoleCounts.User.LogicalPrompts, &value.RoleCounts.User.PromptOccurrences, &value.RoleCounts.User.PromptLengthBytes,
		&value.RoleCounts.Assistant.LogicalPrompts, &value.RoleCounts.Assistant.PromptOccurrences, &value.RoleCounts.Assistant.PromptLengthBytes); err != nil {
		return Statistics{}, fmt.Errorf("read history role statistics: %w", err)
	}
	var unscopedErrors int
	if err := s.runner.QueryRow(`SELECT
		(SELECT COUNT(*) FROM source_errors se WHERE NOT EXISTS(SELECT 1 FROM source_heads sh WHERE sh.provider=se.provider AND sh.source_path=se.source_path))+
		(SELECT COUNT(*) FROM vault_bundle_state vb WHERE vb.last_error<>'' AND NOT EXISTS(
			SELECT 1 FROM locations l JOIN preserved_snapshots ps ON ps.id=l.snapshot_id WHERE l.archive=vb.archive))`).Scan(&unscopedErrors); err != nil {
		return Statistics{}, fmt.Errorf("read unscoped history errors: %w", err)
	}
	if statisticsQueryIsUnfiltered(query) {
		value.ErrorCount += unscopedErrors
	} else if unscopedErrors > 0 {
		value.UnscopedErrorsExcluded = unscopedErrors
		value.Warnings = append(value.Warnings, fmt.Sprintf("%d index error(s) without filterable session metadata were excluded from filtered statistics", unscopedErrors))
	}
	if stat, statErr := os.Stat(s.path); statErr == nil {
		value.IndexSizeBytes = stat.Size()
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Statistics{}, fmt.Errorf("stat history index: %w", statErr)
	}
	if len(dimensions) > 0 {
		groupQuery := query.PromptQuery
		if groupByRole {
			groupQuery.Role = "any"
		}
		groupFilters, groupPromptArgs := promptWhere(groupQuery, true, "p", "s")
		groupFilters, groupPromptArgs = appendProjectSourceFilter(groupFilters, groupPromptArgs, "s", query.ProjectSource)
		selectedSessionColumns := "s.*"
		if groupByProject {
			selectedSessionColumns += `,(SELECT COUNT(*) FROM sessions project_sessions WHERE project_sessions.project=s.project) AS project_session_count`
		}
		groupCTE := `WITH selected_sessions AS (SELECT ` + selectedSessionColumns + ` FROM sessions s WHERE ` + strings.Join(sessionWhere, " AND ") + `),
			available_prompts AS (
				SELECT p.*,(SELECT COUNT(*) FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1) AS available_occurrences
				FROM prompts p JOIN selected_sessions s ON s.id=p.session_id WHERE ` + strings.Join(groupFilters, " AND ") + `)`
		groupArgs := append([]any{}, sessionArgs...)
		groupArgs = append(groupArgs, groupPromptArgs...)
		groupSessionsFollowPrompts := promptScopedSessions
		for _, dimension := range dimensions {
			groupSessionsFollowPrompts = groupSessionsFollowPrompts || dimension == "weekday" || dimension == "hour" || dimension == "role"
		}
		value.Groups, err = s.statisticsGroups(groupCTE, groupArgs, dimensions, expressions, groupSessionsFollowPrompts)
		if err != nil {
			return Statistics{}, err
		}
		value.Groups = ensureUnknownStatisticsGroups(value.Groups, dimensions)
		sort.SliceStable(value.Groups, func(i, j int) bool {
			if value.Groups[i].LogicalPrompts != value.Groups[j].LogicalPrompts {
				return value.Groups[i].LogicalPrompts > value.Groups[j].LogicalPrompts
			}
			return statisticsGroupKey(value.Groups[i], dimensions) < statisticsGroupKey(value.Groups[j], dimensions)
		})
		if len(value.Groups) > query.Top {
			value.Groups, value.Other = limitStatisticsGroups(value.Groups, dimensions, query.Top)
			value.Other.LogicalSessions, err = s.statisticsRemainderSessions(groupCTE, groupArgs, dimensions, expressions, value.Groups, groupSessionsFollowPrompts)
			if err != nil {
				return Statistics{}, err
			}
			value.GroupsTruncated = true
		}
	}
	return value, nil
}

func limitStatisticsGroups(groups []StatisticsGroup, dimensions []string, top int) ([]StatisticsGroup, *StatisticsGroup) {
	selected := append([]StatisticsGroup(nil), groups[:top]...)
	selectedIndexes := map[int]bool{}
	selectedOriginal := make([]int, top)
	for index := range top {
		selectedIndexes[index] = true
		selectedOriginal[index] = index
	}
	protectedSlots := map[int]bool{}
	for _, dimension := range dimensions {
		if dimension != "repo" && dimension != "cwd" && dimension != "project" {
			continue
		}
		wanted := []string{"unknown"}
		if dimension == "project" {
			wanted = []string{"other", "unknown"}
		}
		for _, wantedValue := range wanted {
			if containsStatisticsDimensionValue(selected, dimension, wantedValue) {
				continue
			}
			candidate := -1
			for index := top; index < len(groups); index++ {
				if groups[index].Values[dimension] == wantedValue && groups[index].LogicalSessions > 0 {
					candidate = index
					break
				}
			}
			if candidate < 0 {
				continue
			}
			replace := -1
			for index := len(selected) - 1; index >= 0; index-- {
				if !protectedSlots[index] {
					replace = index
					break
				}
			}
			if replace < 0 {
				continue
			}
			selectedIndexes[selectedOriginal[replace]] = false
			selected[replace] = groups[candidate]
			selectedOriginal[replace] = candidate
			selectedIndexes[candidate] = true
			protectedSlots[replace] = true
		}
	}
	other := &StatisticsGroup{Values: map[string]string{}}
	for _, dimension := range dimensions {
		other.Values[dimension] = "other"
	}
	for index, group := range groups {
		if selectedIndexes[index] {
			continue
		}
		other.LogicalPrompts += group.LogicalPrompts
		other.PromptOccurrences += group.PromptOccurrences
		other.PromptLengthBytes += group.PromptLengthBytes
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].LogicalPrompts != selected[j].LogicalPrompts {
			return selected[i].LogicalPrompts > selected[j].LogicalPrompts
		}
		return statisticsGroupKey(selected[i], dimensions) < statisticsGroupKey(selected[j], dimensions)
	})
	return selected, other
}

func (s *Store) statisticsRemainderSessions(cte string, args []any, dimensions, expressions []string, selected []StatisticsGroup, sessionsFollowPrompts bool) (int, error) {
	tuples := make([]string, 0, len(selected))
	queryArgs := append([]any{}, args...)
	for _, group := range selected {
		parts := make([]string, len(expressions))
		for index, expression := range expressions {
			parts[index] = "COALESCE(CAST((" + expression + ") AS TEXT),'unknown')=?"
			queryArgs = append(queryArgs, group.Values[dimensions[index]])
		}
		tuples = append(tuples, "("+strings.Join(parts, " AND ")+")")
	}
	where := "NOT (" + strings.Join(tuples, " OR ") + ")"
	from := "FROM selected_sessions s LEFT JOIN available_prompts p ON p.session_id=s.id"
	if sessionsFollowPrompts {
		from = "FROM available_prompts p JOIN selected_sessions s ON s.id=p.session_id"
	}
	var count int
	statement := cte + ` SELECT COUNT(DISTINCT s.id) ` + from + ` WHERE ` + where
	if err := s.runner.QueryRow(statement, queryArgs...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count history statistics remainder sessions: %w", err)
	}
	return count, nil
}

func statisticsGroupKey(group StatisticsGroup, dimensions []string) string {
	values := make([]string, len(dimensions))
	for index, dimension := range dimensions {
		values[index] = group.Values[dimension]
	}
	return strings.Join(values, "\x00")
}

func containsStatisticsDimensionValue(groups []StatisticsGroup, dimension, value string) bool {
	for _, group := range groups {
		if group.Values[dimension] == value {
			return true
		}
	}
	return false
}

func statisticsQueryIsUnfiltered(query StatisticsQuery) bool {
	return query.Provider == "" && query.Since == nil && query.Until == nil && query.CWD == "" && query.Repo == "" && query.Project == "" && query.Branch == "" &&
		(query.Source == "" || query.Source == CatalogSourceAny) && (query.ThreadKind == "" || query.ThreadKind == "all") && len(query.PromptKinds) == 0 && !query.ExcludeControl &&
		(query.ProjectSource == "" || query.ProjectSource == SampleProjectSourceAny)
}

func statisticsDimensions(requested []string) ([]string, []string, error) {
	expressionByDimension := map[string]string{
		"provider":    "s.provider",
		"repo":        "COALESCE(NULLIF(s.repository_name,''),'unknown')",
		"project":     fmt.Sprintf("CASE WHEN s.project_session_count < %d THEN 'other' ELSE s.project END", history.ProjectGroupMinSessions),
		"cwd":         "COALESCE(NULLIF(s.cwd,''),'unknown')",
		"thread-kind": "s.thread_kind",
		"weekday":     "CASE strftime('%w',p.timestamp) WHEN '0' THEN 'Sunday' WHEN '1' THEN 'Monday' WHEN '2' THEN 'Tuesday' WHEN '3' THEN 'Wednesday' WHEN '4' THEN 'Thursday' WHEN '5' THEN 'Friday' WHEN '6' THEN 'Saturday' ELSE 'unknown' END",
		"hour":        "CASE WHEN p.timestamp IS NULL OR p.timestamp='' THEN 'unknown' ELSE strftime('%H',p.timestamp) END",
		"role":        "p.role",
	}
	seen := map[string]bool{}
	dimensions, expressions := []string{}, []string{}
	for _, value := range requested {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		expression, ok := expressionByDimension[value]
		if !ok {
			return nil, nil, fmt.Errorf("invalid history stats group %q (expected provider, project, repo, cwd, thread-kind, weekday, hour, or role)", value)
		}
		seen[value] = true
		dimensions = append(dimensions, value)
		expressions = append(expressions, expression)
		if value == "project" {
			seen["project_source"] = true
			dimensions = append(dimensions, "project_source")
			expressions = append(expressions, fmt.Sprintf("CASE WHEN s.project_session_count < %d THEN 'unknown' ELSE s.project_source END", history.ProjectGroupMinSessions))
		}
	}
	return dimensions, expressions, nil
}

func (s *Store) statisticsGroups(cte string, args []any, dimensions, expressions []string, sessionsFollowPrompts bool) ([]StatisticsGroup, error) {
	selectExpressions := strings.Join(expressions, ",")
	sessionCountExpression := "COUNT(DISTINCT s.id)"
	from := "FROM selected_sessions s LEFT JOIN available_prompts p ON p.session_id=s.id"
	if sessionsFollowPrompts {
		// Drive prompt-scoped groups from the materialized prompt set. Starting
		// from sessions makes SQLite rescan that CTE once per session because a
		// materialized CTE has no session_id index.
		from = "FROM available_prompts p JOIN selected_sessions s ON s.id=p.session_id"
	}
	statement := cte + ` SELECT ` + selectExpressions + `,
		` + sessionCountExpression + `,COUNT(DISTINCT p.id),COALESCE(SUM(p.available_occurrences),0),
		COALESCE(SUM(length(CAST(p.clean_text AS BLOB))),0)
		` + from + `
		GROUP BY ` + selectExpressions
	statement += ` ORDER BY ` + selectExpressions
	rows, err := s.runner.Query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("group history statistics: %w", err)
	}
	defer rows.Close()
	result := []StatisticsGroup{}
	for rows.Next() {
		values := make([]sql.NullString, len(dimensions))
		destinations := make([]any, 0, len(dimensions)+4)
		for index := range values {
			destinations = append(destinations, &values[index])
		}
		group := StatisticsGroup{Values: map[string]string{}}
		destinations = append(destinations, &group.LogicalSessions, &group.LogicalPrompts, &group.PromptOccurrences, &group.PromptLengthBytes)
		if err := rows.Scan(destinations...); err != nil {
			return nil, fmt.Errorf("scan history statistics group: %w", err)
		}
		for index, dimension := range dimensions {
			value := values[index].String
			if !values[index].Valid || value == "" {
				value = "unknown"
			}
			group.Values[dimension] = value
		}
		result = append(result, group)
	}
	return result, rows.Err()
}

func ensureUnknownStatisticsGroups(groups []StatisticsGroup, dimensions []string) []StatisticsGroup {
	missing := map[string]bool{}
	for _, dimension := range dimensions {
		if dimension == "repo" || dimension == "cwd" || dimension == "project" || dimension == "project_source" {
			missing[dimension] = true
		}
	}
	if len(missing) == 0 {
		return groups
	}
	for _, group := range groups {
		for dimension := range missing {
			if group.Values[dimension] == "unknown" {
				delete(missing, dimension)
			}
		}
	}
	if len(missing) == 0 {
		return groups
	}
	values := map[string]string{}
	for _, dimension := range dimensions {
		values[dimension] = "unknown"
	}
	return append(groups, StatisticsGroup{Values: values})
}
