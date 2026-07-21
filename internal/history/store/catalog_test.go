package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestListCatalogPaginationCoverageAvailabilityAndPreviewBounds(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	newerTime := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	olderTime := newerTime.Add(-time.Hour)
	longPreview := strings.Repeat("é", 400) + "\nsecond\nthird\nfourth\nfifth"

	codexSource := sourceRef("/provider/codex.jsonl", history.LocationProviderLive)
	codex := extraction("native:codex", "codex", codexSource, prompt("native:p1", "p1", longPreview, 1))
	codex.Session.CWD = "/repo"
	codex.Session.RepositoryName = "tokenomnom"
	codex.Session.Branch = "main"
	codex.Session.FirstTimestamp, codex.Session.LastTimestamp = &newerTime, &newerTime
	codex.Prompts[0].Timestamp, codex.Occurrences[0].Variant.Timestamp = &newerTime, &newerTime
	codexResult, err := database.ApplySource(codex, head(codexSource, "live-current", int64(len(longPreview)), 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}

	vaultSource := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: codexSource.Path, Archive: "codex/2026-07.tar.zst", RelativePath: "codex.jsonl", VaultVersion: 1}
	codex.Source = vaultSource
	if _, err := database.PreserveSnapshot(codex, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "live-current", Size: int64(len(longPreview)), FirstTS: &newerTime, LastTS: &newerTime}); err != nil {
		t.Fatal(err)
	}

	claudeSource := history.SourceReference{Provider: history.ProviderClaude, Kind: history.LocationProviderLive, Path: "/provider/claude.jsonl"}
	claude := extraction("native:claude", "claude", claudeSource, prompt("native:c1", "c1", "claude prompt", 1))
	claude.Provider = history.ProviderClaude
	claude.Session.CWD = "/repo"
	claude.Session.RepositoryName = ""
	claude.Session.Branch = ""
	claude.Session.FirstTimestamp, claude.Session.LastTimestamp = &olderTime, &olderTime
	claude.Prompts[0].Timestamp, claude.Occurrences[0].Variant.Timestamp = &olderTime, &olderTime
	if _, err := database.ApplySource(claude, head(claudeSource, "claude-current", 20, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}

	first, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 1 || !first.HasMore || first.NextCursor == "" || first.Sessions[0].SessionID != codexResult.SessionID {
		t.Fatalf("first page = %+v", first)
	}
	row := first.Sessions[0]
	if row.SourceHeadCount != 1 || row.PreservedSnapshotCount != 1 || row.LogicalPromptCount != 1 || row.OccurrenceCount != 2 || !row.Availability.ExactLiveAndVaulted || row.PreferredRetrievalSource != "provider-live" {
		t.Fatalf("catalog row = %+v", row)
	}
	if len(row.Preview) > maxPreviewBytes || strings.Count(row.Preview, "\n") >= maxPreviewLines || !utf8.ValidString(row.Preview) {
		t.Fatalf("preview not bounded: bytes=%d lines=%d valid=%v", len(row.Preview), strings.Count(row.Preview, "\n")+1, utf8.ValidString(row.Preview))
	}
	if first.Coverage.Repository.Known != 1 || first.Coverage.Repository.Unknown != 1 || first.Coverage.Branch.Known != 1 || first.Coverage.Branch.Unknown != 1 {
		t.Fatalf("coverage = %+v", first.Coverage)
	}

	second, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Cursor: first.NextCursor})
	if err != nil || len(second.Sessions) != 1 || second.Sessions[0].Provider != history.ProviderClaude {
		t.Fatalf("second page err=%v page=%+v", err, second)
	}
	encodedSecond, err := json.Marshal(second.Sessions[0])
	if err != nil || second.Sessions[0].RepositoryName != nil || second.Sessions[0].Branch != nil ||
		!strings.Contains(string(encodedSecond), `"repository_name":null`) || !strings.Contains(string(encodedSecond), `"branch":null`) {
		t.Fatalf("unknown metadata JSON=%s row=%+v err=%v", encodedSecond, second.Sessions[0], err)
	}
	filtered, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Repo: "tokenomnom"})
	if err != nil || len(filtered.Sessions) != 1 || len(filtered.Warnings) != 1 || !strings.Contains(filtered.Warnings[0], "excluded 1") {
		t.Fatalf("repo coverage warning err=%v page=%+v", err, filtered)
	}
	vaultOnly, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceVault})
	if err != nil || len(vaultOnly.Sessions) != 1 || vaultOnly.Sessions[0].SessionID != codexResult.SessionID {
		t.Fatalf("vault filter err=%v page=%+v", err, vaultOnly)
	}

	thirdSource := sourceRef("/provider/third.jsonl", history.LocationProviderArchive)
	third := extraction("native:third", "third", thirdSource, prompt("native:t1", "t1", "third", 1))
	if _, err := database.ApplySource(third, head(thirdSource, "third", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Cursor: first.NextCursor}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("stale cursor error = %v", err)
	}
}

func TestListCatalogRejectsCursorFilterReuseAndBounds(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/one.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:one", "one", source, prompt("native:p", "p", "prompt", 1)), head(source, "one", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 501}); err == nil {
		t.Fatal("oversized page succeeded")
	}
	if page.NextCursor != "" {
		if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceVault, Cursor: page.NextCursor}); err == nil {
			t.Fatal("cursor filter reuse succeeded")
		}
		if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, ThreadKind: "root", Cursor: page.NextCursor}); err == nil {
			t.Fatal("cursor thread-kind reuse succeeded")
		}
	}
}

func TestListCatalogSourceFiltersRequireAvailabilityAndArchiveIsNotLive(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	archive := sourceRef("/provider/archive.jsonl", history.LocationProviderArchive)
	extract := extraction("native:archive", "archive", archive, prompt("native:p", "p", "archive prompt", 1))
	if _, err := database.ApplySource(extract, head(archive, "same", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	vaultSource := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: archive.Path, Archive: "codex/archive.tar.zst", RelativePath: "archive.jsonl", VaultVersion: 1}
	extract.Source = vaultSource
	if _, err := database.PreserveSnapshot(extract, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "same", Size: 10}); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].Availability.ExactLiveAndVaulted || page.Sessions[0].PreferredRetrievalSource != "provider-archive" {
		t.Fatalf("archive availability err=%v page=%+v", err, page)
	}
	if env := mustHealth(t, database); env.ExactLiveAndVaulted != 0 {
		t.Fatalf("archive counted as exact live and vaulted: %+v", env)
	}
	if _, err := database.MarkSourceMissing(history.ProviderCodex, archive.Path); err != nil {
		t.Fatal(err)
	}
	provider, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceProvider})
	if err != nil || len(provider.Sessions) != 0 {
		t.Fatalf("missing provider matched availability filter err=%v page=%+v", err, provider)
	}
	if err := database.RecordVaultBundleError(vaultSource.Archive, time.Now(), errors.New("broken")); err != nil {
		t.Fatal(err)
	}
	vaultPage, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceVault})
	if err != nil || len(vaultPage.Sessions) != 0 {
		t.Fatalf("broken vault matched availability filter err=%v page=%+v", err, vaultPage)
	}
	any, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny})
	if err != nil || len(any.Sessions) != 1 || !any.Sessions[0].Availability.Unavailable || any.Sessions[0].LogicalPromptCount != 0 || any.Sessions[0].OccurrenceCount != 0 || any.Sessions[0].Preview != "" {
		t.Fatalf("metadata-only session missing err=%v page=%+v", err, any)
	}
}

func TestCatalogCursorRejectsInvalidTimestamp(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index := range 2 {
		source := sourceRef("/provider/cursor-"+string(rune('a'+index))+".jsonl", history.LocationProviderLive)
		when := time.Date(2026, 7, 21, 12-index, 0, 0, 0, time.UTC)
		extract := extraction("native:cursor-"+string(rune('a'+index)), "cursor", source, prompt("native:p", "p", "prompt", 1))
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
		if _, err := database.ApplySource(extract, head(source, "hash", 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("cursor fixture err=%v page=%+v", err, page)
	}
	cursor, err := decodeCatalogCursor(page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	cursor.Timestamp = "not-a-date"
	malformed, err := encodeCatalogCursor(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Cursor: malformed}); err == nil || !strings.Contains(err.Error(), "invalid history cursor") {
		t.Fatalf("invalid timestamp cursor error = %v", err)
	}
}

func TestCatalogCursorUsesSQLiteSortKeyForOffsetTimestamp(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	ids := []string{}
	for index := range 3 {
		source := sourceRef(fmt.Sprintf("/provider/offset-%d.jsonl", index), history.LocationProviderLive)
		when := time.Date(2026, 7, 21, 12-index, 0, 0, 0, time.UTC)
		extract := extraction(fmt.Sprintf("native:offset-%d", index), fmt.Sprintf("offset-%d", index), source, prompt("native:p", "p", "offset", 1))
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
		result, err := database.ApplySource(extract, head(source, fmt.Sprintf("hash-%d", index), 10, 1), ApplyReplace)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, result.SessionID)
	}
	if _, err := database.db.Exec(`UPDATE sessions SET first_ts='2026-07-21T12:00:00.123-04:00',last_ts='2026-07-21T12:00:00.123-04:00' WHERE native_session_id='offset-0'`); err != nil {
		t.Fatal(err)
	}
	var sortKey string
	if err := database.db.QueryRow(`SELECT ` + sqliteTimestampKey("'2026-07-21T12:00:00.123456789-04:00'")).Scan(&sortKey); err != nil || sortKey != "2026-07-21T16:00:00.123456789Z" {
		t.Fatalf("offset sort key=%q err=%v", sortKey, err)
	}
	first, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil || len(first.Sessions) != 1 || first.Sessions[0].SessionID != ids[0] || first.NextCursor == "" {
		t.Fatalf("first offset page err=%v page=%+v", err, first)
	}
	second, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Cursor: first.NextCursor})
	if err != nil || len(second.Sessions) != 1 || second.Sessions[0].SessionID == ids[0] {
		t.Fatalf("offset continuation duplicated/skipped row err=%v page=%+v", err, second)
	}
	since := time.Date(2026, 7, 21, 15, 0, 0, 0, time.UTC)
	filtered, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Since: &since})
	if err != nil || len(filtered.Sessions) != 1 || filtered.Sessions[0].SessionID != ids[0] {
		t.Fatalf("offset instant filter err=%v page=%+v", err, filtered)
	}
}

func TestCatalogBoundsNestedSourceAndSnapshotIDs(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	for index := range 105 {
		source := sourceRef(fmt.Sprintf("/provider/bounded-%03d.jsonl", index), history.LocationProviderLive)
		extract := extraction("native:bounded", "bounded", source, prompt("native:p", "p", "bounded", 1))
		if _, err := database.ApplySource(extract, head(source, fmt.Sprintf("head-%03d", index), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}

		vaultSource := history.SourceReference{
			Provider: history.ProviderCodex, Kind: history.LocationVault,
			Path: fmt.Sprintf("/gone/bounded-%03d.jsonl", index), Archive: fmt.Sprintf("codex/bounded-%03d.tar.zst", index),
			RelativePath: fmt.Sprintf("bounded-%03d.jsonl", index), VaultVersion: index + 1,
		}
		vaultExtract := extraction("native:bounded", "bounded", vaultSource, prompt("native:p", "p", "bounded", 1))
		if _, err := database.PreserveSnapshot(vaultExtract, history.PreservedSnapshot{
			Provider: history.ProviderCodex, ContentSHA256: fmt.Sprintf("snapshot-%03d", index), Size: 10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("bounded catalog err=%v page=%+v", err, page)
	}
	row := page.Sessions[0]
	if row.SourceHeadCount != 105 || len(row.SourceHeadIDs) != 100 || row.PreservedSnapshotCount != 105 || len(row.PreservedSnapshotIDs) != 100 {
		t.Fatalf("unbounded nested IDs: %+v", row)
	}
}

func TestCatalogAndHealthOrderFractionalTimestampsChronologically(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	whole := time.Date(2026, 7, 21, 12, 0, 59, 0, time.UTC)
	fractional := whole.Add(500 * time.Millisecond)
	for _, fixture := range []struct {
		name string
		when time.Time
	}{
		{name: "whole", when: whole},
		{name: "fractional", when: fractional},
	} {
		source := sourceRef("/provider/"+fixture.name+".jsonl", history.LocationProviderLive)
		extract := extraction("native:"+fixture.name, fixture.name, source, prompt("native:p", "p", fixture.name, 1))
		extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &fixture.when, &fixture.when
		extract.Prompts[0].Timestamp = &fixture.when
		if _, err := database.ApplySource(extract, head(source, fixture.name+"-hash", 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].NativeSessionID != "fractional" || page.NextCursor == "" {
		t.Fatalf("fractional order err=%v page=%+v", err, page)
	}
	next, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1, Cursor: page.NextCursor})
	if err != nil || len(next.Sessions) != 1 || next.Sessions[0].NativeSessionID != "whole" {
		t.Fatalf("fractional cursor err=%v page=%+v", err, next)
	}
	boundary := whole.Add(250 * time.Millisecond)
	since, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Since: &boundary})
	if err != nil || len(since.Sessions) != 1 || since.Sessions[0].NativeSessionID != "fractional" {
		t.Fatalf("fractional since err=%v page=%+v", err, since)
	}
	until, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Until: &boundary})
	if err != nil || len(until.Sessions) != 1 || until.Sessions[0].NativeSessionID != "whole" {
		t.Fatalf("fractional until err=%v page=%+v", err, until)
	}
	health, err := database.Health()
	if err != nil || health.CoverageFirst != whole.Format(time.RFC3339Nano) || health.CoverageLast != fractional.Format(time.RFC3339Nano) {
		t.Fatalf("fractional coverage health=%+v err=%v", health, err)
	}
}

func TestCatalogDateFiltersExcludeUnknownTimestamps(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/unknown-date.jsonl", history.LocationProviderLive)
	extract := extraction("native:unknown-date", "unknown-date", source, prompt("native:p", "p", "unknown date", 1))
	if _, err := database.ApplySource(extract, head(source, "unknown-date-hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	boundary := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	for name, query := range map[string]CatalogQuery{
		"since": {Source: CatalogSourceAny, Since: &boundary},
		"until": {Source: CatalogSourceAny, Until: &boundary},
	} {
		page, err := database.ListCatalog(query)
		if err != nil || len(page.Sessions) != 0 {
			t.Fatalf("%s unknown-date filter err=%v page=%+v", name, err, page)
		}
	}
}

func mustHealth(t *testing.T, database *Store) Health {
	t.Helper()
	value, err := database.Health()
	if err != nil {
		t.Fatal(err)
	}
	return value
}
