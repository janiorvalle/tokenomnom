package store

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// LedgerMonthStat is the session count for a calendar month.
type LedgerMonthStat struct {
	Month    string
	Sessions int
}

// LedgerDayStat is the session count for a local calendar day.
type LedgerDayStat struct {
	Day      string
	Sessions int
}

// LedgerProfileStat is one weekday or hour bucket from session start times.
type LedgerProfileStat struct {
	Bucket   int
	Sessions int
}

// LedgerProjectMonthStat is the bounded project/month intensity matrix.
type LedgerProjectMonthStat struct {
	Project  string
	Month    string
	Sessions int
}

// LedgerAnalytics is derived from the existing history catalog tables. It is
// intentionally session-based because the usage store has no project or start
// time dimension to join against.
type LedgerAnalytics struct {
	Months        []LedgerMonthStat
	Days          []LedgerDayStat
	Weekdays      []LedgerProfileStat
	Hours         []LedgerProfileStat
	ProjectMonths []LedgerProjectMonthStat
}

// LedgerAnalytics returns bounded profile facts for the ledger side panes.
// Catalog filters are applied before grouping so provider, period, and source
// selections stay consistent with the sessions page. Buckets are calculated
// in Go so an explicit dashboard timezone, including DST, is respected.
func (s *Store) LedgerAnalytics(query CatalogQuery, location *time.Location) (LedgerAnalytics, error) {
	profile, _, err := s.LedgerAnalyticsWithCounts(query, query, location)
	return profile, err
}

// LedgerAnalyticsWithCounts derives a scoped profile and a second count view
// from one catalog pass. The dashboard needs all-period row counts alongside
// period-scoped profiles, and keeping both masks in one query avoids scanning
// a large history database twice on every navigation step.
func (s *Store) LedgerAnalyticsWithCounts(profileQuery, countsQuery CatalogQuery, location *time.Location) (LedgerAnalytics, LedgerAnalytics, error) {
	profileQuery, err := normalizeLedgerAnalyticsQuery(profileQuery)
	if err != nil {
		return LedgerAnalytics{}, LedgerAnalytics{}, err
	}
	countsQuery, err = normalizeLedgerAnalyticsQuery(countsQuery)
	if err != nil {
		return LedgerAnalytics{}, LedgerAnalytics{}, err
	}
	if location == nil {
		location = time.Local
	}
	profileWhere, profileArgs := catalogWhere(profileQuery, true)
	countsWhere, countsArgs := catalogWhere(countsQuery, true)
	profileCondition := strings.Join(profileWhere, " AND ")
	countsCondition := strings.Join(countsWhere, " AND ")
	args := make([]any, 0, len(profileArgs)*2+len(countsArgs)*2)
	args = append(args, profileArgs...)
	args = append(args, countsArgs...)
	args = append(args, profileArgs...)
	args = append(args, countsArgs...)
	rows, err := s.runner.Query(`SELECT
		COALESCE(NULLIF(s.first_ts,''), NULLIF(s.last_ts,''), ''), s.project,
		CASE WHEN `+profileCondition+` THEN 1 ELSE 0 END,
		CASE WHEN `+countsCondition+` THEN 1 ELSE 0 END
		FROM sessions s WHERE (`+profileCondition+") OR ("+countsCondition+")"+
		` ORDER BY s.project, s.first_ts, s.last_ts`, args...)
	if err != nil {
		return LedgerAnalytics{}, LedgerAnalytics{}, fmt.Errorf("read ledger history profile: %w", err)
	}
	defer rows.Close()

	profileAccumulator := newLedgerAnalyticsAccumulator()
	countsAccumulator := newLedgerAnalyticsAccumulator()
	for rows.Next() {
		var rawTimestamp, project string
		var profileMatch, countsMatch int
		if err := rows.Scan(&rawTimestamp, &project, &profileMatch, &countsMatch); err != nil {
			return LedgerAnalytics{}, LedgerAnalytics{}, fmt.Errorf("scan ledger history profile: %w", err)
		}
		if profileMatch != 0 {
			profileAccumulator.add(rawTimestamp, project, location)
		}
		if countsMatch != 0 {
			countsAccumulator.add(rawTimestamp, project, location)
		}
	}
	if err := rows.Err(); err != nil {
		return LedgerAnalytics{}, LedgerAnalytics{}, fmt.Errorf("iterate ledger history profile: %w", err)
	}
	if err := rows.Close(); err != nil {
		return LedgerAnalytics{}, LedgerAnalytics{}, fmt.Errorf("close ledger history profile: %w", err)
	}
	return profileAccumulator.result(), countsAccumulator.result(), nil
}

type ledgerAnalyticsAccumulator struct {
	monthCounts        map[string]int
	dayCounts          map[string]int
	weekdayCounts      map[int]int
	hourCounts         map[int]int
	projectMonthCounts map[ledgerProjectMonthKey]int
}

func newLedgerAnalyticsAccumulator() ledgerAnalyticsAccumulator {
	return ledgerAnalyticsAccumulator{
		monthCounts:        map[string]int{},
		dayCounts:          map[string]int{},
		weekdayCounts:      map[int]int{},
		hourCounts:         map[int]int{},
		projectMonthCounts: map[ledgerProjectMonthKey]int{},
	}
}

func (a *ledgerAnalyticsAccumulator) add(rawTimestamp, project string, location *time.Location) {
	value, ok := ledgerLocalTimestamp(rawTimestamp, location)
	if !ok {
		a.monthCounts["unknown"]++
		a.dayCounts["unknown"]++
		a.weekdayCounts[-1]++
		a.hourCounts[-1]++
		a.projectMonthCounts[ledgerProjectMonthKey{Project: project, Month: "unknown"}]++
		return
	}
	month := value.Format("2006-01")
	day := value.Format("2006-01-02")
	a.monthCounts[month]++
	a.dayCounts[day]++
	a.weekdayCounts[int(value.Weekday())]++
	a.hourCounts[value.Hour()]++
	a.projectMonthCounts[ledgerProjectMonthKey{Project: project, Month: month}]++
}

func (a ledgerAnalyticsAccumulator) result() LedgerAnalytics {
	return LedgerAnalytics{
		Months:        sortedLedgerMonths(a.monthCounts),
		Days:          sortedLedgerDays(a.dayCounts),
		Weekdays:      sortedLedgerBuckets(a.weekdayCounts),
		Hours:         sortedLedgerBuckets(a.hourCounts),
		ProjectMonths: sortedLedgerProjectMonths(a.projectMonthCounts),
	}
}

type ledgerProjectMonthKey struct {
	Project string
	Month   string
}

func ledgerLocalTimestamp(raw string, location *time.Location) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return value.In(location), true
}

func sortedLedgerMonths(values map[string]int) []LedgerMonthStat {
	result := make([]LedgerMonthStat, 0, len(values))
	for month, sessions := range values {
		result = append(result, LedgerMonthStat{Month: month, Sessions: sessions})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Month < result[right].Month })
	return result
}

func sortedLedgerDays(values map[string]int) []LedgerDayStat {
	result := make([]LedgerDayStat, 0, len(values))
	for day, sessions := range values {
		result = append(result, LedgerDayStat{Day: day, Sessions: sessions})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Day < result[right].Day })
	return result
}

func sortedLedgerBuckets(values map[int]int) []LedgerProfileStat {
	result := make([]LedgerProfileStat, 0, len(values))
	for bucket, sessions := range values {
		result = append(result, LedgerProfileStat{Bucket: bucket, Sessions: sessions})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Bucket < result[right].Bucket })
	return result
}

func sortedLedgerProjectMonths(values map[ledgerProjectMonthKey]int) []LedgerProjectMonthStat {
	result := make([]LedgerProjectMonthStat, 0, len(values))
	for key, sessions := range values {
		result = append(result, LedgerProjectMonthStat{Project: key.Project, Month: key.Month, Sessions: sessions})
	}
	sort.Slice(result, func(left, right int) bool {
		if strings.ToLower(result[left].Project) != strings.ToLower(result[right].Project) {
			return strings.ToLower(result[left].Project) < strings.ToLower(result[right].Project)
		}
		if result[left].Project != result[right].Project {
			return result[left].Project < result[right].Project
		}
		return result[left].Month < result[right].Month
	})
	return result
}

func normalizeLedgerAnalyticsQuery(query CatalogQuery) (CatalogQuery, error) {
	if query.Source == "" {
		query.Source = CatalogSourceAny
	}
	if !validCatalogSource(query.Source) {
		return CatalogQuery{}, fmt.Errorf("invalid history source %q", query.Source)
	}
	query.ThreadKind = normalizedThreadKindFilter(query.ThreadKind)
	if !validThreadKindFilter(query.ThreadKind) {
		return CatalogQuery{}, fmt.Errorf("invalid history thread kind %q", query.ThreadKind)
	}
	return query, nil
}
