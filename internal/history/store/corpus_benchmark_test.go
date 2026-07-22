package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/history/testcorpus"
)

func BenchmarkStatisticsQueryPlan(b *testing.B) {
	corpus := testcorpus.Generate(testcorpus.Spec{Sessions: 100, Prompts: 500, Seed: testcorpus.DefaultSeed})
	database, err := Open(filepath.Join(b.TempDir(), DatabaseName))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = database.Close() })
	populateCorpusStore(b, database, corpus)
	for _, groupBy := range [][]string{{"provider", "thread-kind"}, {"weekday", "hour"}} {
		b.Logf("group-by %s", strings.Join(groupBy, ","))
		for _, detail := range corpusStatisticsQueryPlan(b, database, groupBy) {
			b.Log(detail)
		}
	}
}

func BenchmarkCorpusStatisticsAndSearch(b *testing.B) {
	corpus := testcorpus.Generate(testcorpus.DefaultSpec())
	database, err := Open(filepath.Join(b.TempDir(), DatabaseName))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = database.Close() })
	populateCorpusStore(b, database, corpus)
	statistics := []struct {
		name    string
		groupBy []string
	}{
		{"single-provider", []string{"provider"}},
		{"provider-thread-kind", []string{"provider", "thread-kind"}},
		{"weekday-hour", []string{"weekday", "hour"}},
		{"repo", []string{"repo"}},
		{"project", []string{"project"}},
	}
	for _, benchmark := range statistics {
		b.Run(benchmark.name, func(b *testing.B) {
			for range b.N {
				if _, err := database.Statistics(StatisticsQuery{PromptQuery: PromptQuery{Source: CatalogSourceAny}, GroupBy: benchmark.groupBy}); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(len(corpus.Sessions)), "sessions")
			b.ReportMetric(float64(corpus.Prompts), "prompts")
		})
	}
	b.Run("filtered-search", func(b *testing.B) {
		for range b.N {
			if _, err := database.Search(SearchQuery{PromptQuery: PromptQuery{Provider: history.ProviderCodex, Project: "repo-001", Source: CatalogSourceAny, Limit: 25}, Query: "needle"}); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(corpus.Sessions)), "sessions")
		b.ReportMetric(float64(corpus.Prompts), "prompts")
	})
}

func populateCorpusStore(b *testing.B, database *Store, corpus testcorpus.Corpus) {
	b.Helper()
	for _, session := range corpus.Sessions {
		if session.Live {
			source := corpusSource(session, false)
			extraction := corpusExtraction(session, source)
			head := history.SourceHead{
				Source: source, ContentSHA256: fmt.Sprintf("live-%064x", session.Index),
				Size: int64(corpusSessionBytes(session)), CompleteOffset: int64(corpusSessionBytes(session)),
				LineCount: int64(len(session.Prompts) + 1), Available: true,
			}
			if _, err := database.ApplySourceWithGeneration(extraction, head, ApplyReplace, false); err != nil {
				b.Fatalf("apply corpus session %d: %v", session.Index, err)
			}
		}
		if session.Vault {
			source := corpusSource(session, true)
			extraction := corpusExtraction(session, source)
			snapshot := history.PreservedSnapshot{
				Provider: session.Provider, ContentSHA256: fmt.Sprintf("vault-%063x", session.Index),
				Size: int64(corpusSessionBytes(session)), FirstTS: &session.FirstTimestamp,
			}
			last := session.Prompts[len(session.Prompts)-1].Timestamp
			snapshot.LastTS = &last
			if _, err := database.PreserveSnapshot(extraction, snapshot); err != nil {
				b.Fatalf("preserve corpus session %d: %v", session.Index, err)
			}
		}
	}
}

func corpusSource(session testcorpus.Session, vault bool) history.SourceReference {
	path := filepath.ToSlash(filepath.Join("/provider", string(session.Provider), session.NativeID+".jsonl"))
	if vault {
		return history.SourceReference{
			Provider: session.Provider, Kind: history.LocationVault, Path: path,
			Archive:      filepath.ToSlash(filepath.Join(string(session.Provider), fmt.Sprintf("bundle-%03d.tar.zst", session.Index/100))),
			RelativePath: session.NativeID + ".jsonl", VaultVersion: 1,
		}
	}
	kind := history.LocationProviderLive
	if session.Archived && session.Provider == history.ProviderCodex {
		kind = history.LocationProviderArchive
	}
	return history.SourceReference{Provider: session.Provider, Kind: kind, Path: path}
}

func corpusExtraction(session testcorpus.Session, source history.SourceReference) history.Extraction {
	confidence := history.ConfidenceExact
	if session.ThreadKind == history.ThreadUnknown {
		confidence = history.ConfidenceUnknown
	}
	value := history.Extraction{
		Provider: session.Provider, Source: source,
		Session: history.Session{
			IdentityKey: "native:" + session.NativeID, NativeSessionID: session.NativeID,
			CWD: session.CWD, RepositoryName: session.RepositoryName, Branch: session.Branch,
			ThreadKind: session.ThreadKind, ThreadConfidence: confidence,
			Confidence: history.ConfidenceExact, FirstTimestamp: &session.FirstTimestamp,
		},
		Prompts:     make([]history.Prompt, 0, len(session.Prompts)),
		Occurrences: make([]history.Occurrence, 0, len(session.Prompts)),
	}
	if session.RepositoryName != "" {
		value.Session.RepositoryIdentity = "https://example.invalid/acme/" + session.RepositoryName
		value.Session.RepositoryRuleVersion = history.RepositoryRuleVersion
	}
	last := session.Prompts[len(session.Prompts)-1].Timestamp
	value.Session.LastTimestamp = &last
	offset := int64(0)
	for index, corpusPrompt := range session.Prompts {
		prompt := history.Prompt{
			LogicalKey: "native:" + corpusPrompt.NativeID, NativeMessageID: corpusPrompt.NativeID,
			Role: history.RoleUser, CleanText: corpusPrompt.Text,
			Classification: history.ClassificationHuman, PromptKind: history.PromptKindHuman,
			Searchable: true, Timestamp: &corpusPrompt.Timestamp, Confidence: history.ConfidenceExact,
		}
		value.Prompts = append(value.Prompts, prompt)
		end := offset + int64(len(corpusPrompt.Text))
		value.Occurrences = append(value.Occurrences, history.Occurrence{
			PromptKey: prompt.LogicalKey, Variant: prompt, LineNumber: int64(index + 2),
			StartOffset: offset, EndOffset: end,
		})
		offset = end + 1
	}
	return value
}

func corpusSessionBytes(session testcorpus.Session) int {
	total := 256
	for _, prompt := range session.Prompts {
		total += len(prompt.Text) + 160
	}
	return total
}

func corpusStatisticsQueryPlan(b *testing.B, database *Store, groupBy []string) []string {
	b.Helper()
	sessionWhere, sessionArgs := catalogWhere(CatalogQuery{Source: CatalogSourceAny}, true)
	promptFilters, promptArgs := promptWhere(PromptQuery{Source: CatalogSourceAny, Role: string(history.RoleUser)}, true, "p", "s")
	_, expressions, err := statisticsDimensions(groupBy)
	if err != nil {
		b.Fatal(err)
	}
	groupCTE := `WITH selected_sessions AS (SELECT s.* FROM sessions s WHERE ` + strings.Join(sessionWhere, " AND ") + `),
		available_prompts AS (
			SELECT p.*,(SELECT COUNT(*) FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.available=1) AS available_occurrences
			FROM prompts p JOIN selected_sessions s ON s.id=p.session_id WHERE ` + strings.Join(promptFilters, " AND ") + `)`
	selectExpressions := strings.Join(expressions, ",")
	statement := groupCTE + ` SELECT ` + selectExpressions + `,
		COUNT(DISTINCT s.id),COUNT(DISTINCT p.id),COALESCE(SUM(p.available_occurrences),0),
		COALESCE(SUM(length(CAST(p.clean_text AS BLOB))),0)
		FROM available_prompts p JOIN selected_sessions s ON s.id=p.session_id
		GROUP BY ` + selectExpressions + ` ORDER BY ` + selectExpressions
	args := append(sessionArgs, promptArgs...)
	rows, err := database.db.Query("EXPLAIN QUERY PLAN "+statement, args...)
	if err != nil {
		b.Fatal(err)
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			b.Fatal(err)
		}
		result = append(result, fmt.Sprintf("%d/%d %s", id, parent, detail))
	}
	if err := rows.Err(); err != nil && err != sql.ErrNoRows {
		b.Fatal(err)
	}
	return result
}
