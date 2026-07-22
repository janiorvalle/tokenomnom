package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
	_ "modernc.org/sqlite"
)

func TestInspectAbsentDoesNotCreateStorage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", DatabaseName)
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Exists || info.SchemaVersion != 0 || info.ExtractorVersion != 0 {
		t.Fatalf("info = %#v", info)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("inspection created parent directory: %v", err)
	}
}

func TestOpenCreatesPrivatePermissionsAndIndependentSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Exists || info.SchemaVersion != SchemaVersion || info.ExtractorVersion != history.ExtractorVersion || database.Path() != path {
		t.Fatalf("info = %#v path=%q", info, database.Path())
	}
	var journal string
	var foreignKeys bool
	if err := database.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journal) != "wal" || !foreignKeys {
		t.Fatalf("journal=%q foreign_keys=%v", journal, foreignKeys)
	}
	if runtime.GOOS != "windows" {
		for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
			stat, err := os.Stat(candidate)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil || stat.Mode().Perm() != 0o600 {
				t.Fatalf("mode for %s = %v, %v", candidate, stat.Mode().Perm(), err)
			}
		}
	}
}

func TestFutureSchemaVersionIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO meta VALUES('schema_version','99')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "unsupported history store schema 99") {
		t.Fatalf("Open error = %v", err)
	}
}

func TestSchemaThreeMigrationPreservesStableIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceRef("/provider/migration.jsonl", history.LocationProviderLive)
	before, err := database.ApplySource(extraction("native:migration", "migration", source, prompt("native:p", "p", "kept", 1)), head(source, "hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`DROP TRIGGER sample_strata_session_delete; DROP TRIGGER sample_strata_prompt_delete; DROP TRIGGER sample_strata_group_insert; DROP TRIGGER sample_strata_group_delete; DROP TABLE sample_strata; DROP TABLE sample_groups;
		DROP INDEX sessions_sample_key_idx; DROP INDEX sessions_sample_month_idx; DROP INDEX sessions_sample_repo_idx; DROP INDEX sessions_sample_thread_idx; DROP INDEX prompts_sample_key_idx; DROP INDEX prompts_session_sample_key_idx;
		ALTER TABLE sessions DROP COLUMN sample_key;
		ALTER TABLE prompts DROP COLUMN sample_key;
		DROP TABLE vault_bundle_state; DROP TABLE vault_prompt_tombstones; DROP TABLE session_thread_supports; DROP TABLE session_relation_supports; DROP TRIGGER session_relations_parent_delete; DROP TABLE session_relations;
		ALTER TABLE sessions DROP COLUMN thread_evidence;
		ALTER TABLE sessions DROP COLUMN thread_confidence;
		ALTER TABLE sessions DROP COLUMN thread_rule_version;
		ALTER TABLE sessions DROP COLUMN forked_from_message_id;
		ALTER TABLE sessions DROP COLUMN repository_rule_version;
		DELETE FROM meta WHERE key='sampling_ready';
		UPDATE meta SET value='2' WHERE key='schema_version';
		UPDATE meta SET value='1' WHERE key='extractor_version'`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].SessionID != before.SessionID || page.Sessions[0].SourceHeadIDs[0] != before.SourceID {
		t.Fatalf("migration changed stable IDs: before=%+v page=%+v", before, page)
	}
	promptPage, err := database.ListPrompts(PromptQuery{Source: CatalogSourceAny})
	if err != nil || len(promptPage.Prompts) != 1 || promptPage.Prompts[0].PromptID != before.PromptIDs["native:p"] || promptPage.Prompts[0].Role != history.RoleUser || matchCount(t, database, "kept") != 1 {
		t.Fatalf("migration changed prompt role/ID/FTS: before=%+v page=%+v err=%v", before, promptPage, err)
	}
	if extractorVersion, err := database.Meta("extractor_version"); err != nil || extractorVersion != fmt.Sprint(history.ExtractorVersion) {
		t.Fatalf("migrated extractor version=%q err=%v", extractorVersion, err)
	}
	if ready, err := database.Meta("sampling_ready"); err != nil || ready != "0" {
		t.Fatalf("migration sampling readiness=%q err=%v", ready, err)
	}
	if _, err := database.Sample(SampleQuery{Count: 1}); err == nil || !strings.Contains(err.Error(), "run history index") {
		t.Fatalf("sample before explicit preparation error=%v", err)
	}
	if err := database.PrepareSampling(); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sessions", "prompts"} {
		var publicID string
		var key []byte
		if err := database.db.QueryRow(`SELECT public_id,sample_key FROM `+table+` LIMIT 1`).Scan(&publicID, &key); err != nil || !bytes.Equal(key, sampleKey(publicID)) {
			t.Fatalf("migrated %s sample key=%x public_id=%q err=%v", table, key, publicID, err)
		}
	}
	var strata int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sample_strata`).Scan(&strata); err != nil || strata < 14 {
		t.Fatalf("migrated sample strata=%d err=%v", strata, err)
	}
}

func TestRepositoryMigrationBackfillsStoredCodexURLWithoutIDChurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	source := sourceRef("/provider/repository-migration.jsonl", history.LocationProviderLive)
	value := extraction("native:repository-migration", "repository-migration", source, prompt("native:p", "p", "kept", 1))
	value.Session.RepositoryIdentity = "https://build:secret@git.example/Unusual.Org/TokenOmNom.git/"
	before, err := database.ApplySource(value, head(source, "hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`ALTER TABLE sessions DROP COLUMN repository_rule_version;
		UPDATE meta SET value='8' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	database, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var sessionID, promptID, repositoryName, repositoryIdentity string
	var ruleVersion int
	if err := database.db.QueryRow(`SELECT s.public_id,p.public_id,s.repository_name,s.repository_identity,s.repository_rule_version
		FROM sessions s JOIN prompts p ON p.session_id=s.id`).Scan(&sessionID, &promptID, &repositoryName, &repositoryIdentity, &ruleVersion); err != nil {
		t.Fatal(err)
	}
	if sessionID != before.SessionID || promptID != before.PromptIDs["native:p"] {
		t.Fatalf("repository backfill changed stable IDs: before=%+v session=%q prompt=%q", before, sessionID, promptID)
	}
	if repositoryName != "TokenOmNom" || repositoryIdentity != "git.example/Unusual.Org/TokenOmNom" || ruleVersion != history.RepositoryRuleVersion {
		t.Fatalf("repository backfill = name %q identity %q rule %d", repositoryName, repositoryIdentity, ruleVersion)
	}
	if matchCount(t, database, "kept") != 1 {
		t.Fatal("repository backfill changed searchable prompt content")
	}
}

func TestHealthSeparatesUserAndSearchableUserPromptCounts(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/counts.jsonl", history.LocationProviderLive)
	searchable := prompt("native:searchable", "searchable", "human prompt", 1)
	classified := prompt("native:classified", "classified", "provider metadata", 2)
	classified.Classification = history.ClassificationProviderMetadata
	classified.Searchable = false
	if _, err := database.ApplySource(extraction("native:counts", "counts", source, searchable, classified), head(source, "hash", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	health, err := database.Health()
	if err != nil {
		t.Fatal(err)
	}
	if health.UserPrompts != 2 || health.SearchableUserPrompts != 1 {
		t.Fatalf("history user counts = all %d searchable %d", health.UserPrompts, health.SearchableUserPrompts)
	}
}

func TestHealthCountsStaleVaultSnapshotsAndBundleCheckpoints(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: "/gone/stale.jsonl", Archive: "codex/stale.tar.zst", RelativePath: "stale.jsonl", VaultVersion: 1}
	extract := extraction("native:stale-vault", "stale-vault", source, prompt("native:p", "p", "stale prompt", 1))
	_, err := database.ApplySnapshotBundle(source.Archive, "fingerprint", []SnapshotInput{{
		Extraction: extract, Snapshot: history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "stale-hash", Size: 10},
	}}, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE preserved_snapshots SET extractor_version=0; UPDATE vault_bundle_state SET extractor_version=0`); err != nil {
		t.Fatal(err)
	}
	health, err := database.Health()
	if err != nil || health.StaleSources != 2 {
		t.Fatalf("stale vault health=%+v err=%v", health, err)
	}
}

func TestExtractorVersionRehomesStaleSnapshotSessionIdentity(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := history.SourceReference{
		Provider: history.ProviderClaude, Kind: history.LocationVault, Path: "/vault/subagent.jsonl",
		Archive: "claude/relationships.tar.zst", RelativePath: "project/parent/subagents/agent-child.jsonl", VaultVersion: 1,
	}
	snapshot := history.PreservedSnapshot{Provider: history.ProviderClaude, ContentSHA256: "relationship-snapshot", Size: 42}
	parent := extraction("native:parent", "parent", source, prompt("native:p", "p", "old identity", 1))
	if _, err := database.ApplySnapshotBundle(source.Archive, "before", []SnapshotInput{{Extraction: parent, Snapshot: snapshot}}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	var snapshotPublicID, parentPublicID, promptPublicID string
	if err := database.db.QueryRow(`SELECT ps.public_id,s.public_id,p.public_id FROM preserved_snapshots ps JOIN sessions s ON s.id=ps.session_id
		JOIN prompts p ON p.session_id=s.id WHERE ps.content_sha256=?`, snapshot.ContentSHA256).Scan(&snapshotPublicID, &parentPublicID, &promptPublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE preserved_snapshots SET extractor_version=1; UPDATE vault_bundle_state SET extractor_version=1`); err != nil {
		t.Fatal(err)
	}
	child := extraction("native:parent/subagents/agent-child", "parent/subagents/agent-child", source, prompt("native:p", "p", "old identity", 1))
	child.Session.ThreadKind = history.ThreadSubagent
	child.Session.ParentNativeSessionID = "parent"
	if _, err := database.ApplySnapshotBundle(source.Archive, "after", []SnapshotInput{{Extraction: child, Snapshot: snapshot}}, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	var afterSnapshotID, childPublicID, afterPromptID string
	if err := database.db.QueryRow(`SELECT ps.public_id,s.public_id,p.public_id FROM preserved_snapshots ps JOIN sessions s ON s.id=ps.session_id
		JOIN prompts p ON p.session_id=s.id WHERE ps.content_sha256=?`, snapshot.ContentSHA256).Scan(&afterSnapshotID, &childPublicID, &afterPromptID); err != nil {
		t.Fatal(err)
	}
	if afterSnapshotID != snapshotPublicID || childPublicID != parentPublicID || afterPromptID != promptPublicID {
		t.Fatalf("snapshot identity before=%q/%q/%q after=%q/%q/%q", snapshotPublicID, parentPublicID, promptPublicID, afterSnapshotID, childPublicID, afterPromptID)
	}
	var oldSessionCount int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE public_id=?`, parentPublicID).Scan(&oldSessionCount); err != nil || oldSessionCount != 1 {
		t.Fatalf("stale snapshot session count=%d err=%v", oldSessionCount, err)
	}
}

func TestBundleCoalescesIdenticalEmptyPathFallbackSnapshots(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	inputs := make([]SnapshotInput, 0, 2)
	for index, name := range []string{"one", "two"} {
		source := history.SourceReference{
			Provider: history.ProviderCodex, Kind: history.LocationVault, Path: "/gone/" + name + ".jsonl",
			Archive: "codex/empty.tar.zst", RelativePath: name + ".jsonl", VaultVersion: index + 1,
		}
		inputs = append(inputs, SnapshotInput{
			Extraction: history.Extraction{Provider: history.ProviderCodex, Source: source, Session: history.Session{
				IdentityKey: "fallback:source-path:" + source.Path, FallbackKey: "source-path:" + source.Path, Confidence: history.ConfidenceUnknown,
			}},
			Snapshot: history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "empty-hash", Size: 0},
		})
	}
	result, err := database.ApplySnapshotBundle("codex/empty.tar.zst", "empty-fingerprint", inputs, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	stats, _ := database.Stats()
	if !result.Changed || result.Snapshots != 2 || stats.Sessions != 1 || stats.Snapshots != 1 || stats.Locations != 2 {
		t.Fatalf("empty snapshot result=%+v stats=%+v", result, stats)
	}
}

func TestFailedVaultBundleRestoresPromptIDAndExcludesCoverageUntilRepair(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: "/gone/repair.jsonl", Archive: "codex/repair.tar.zst", RelativePath: "repair.jsonl", VaultVersion: 1}
	extract := extraction("native:repair", "repair", source, prompt("native:p", "p", "repair me", 1))
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &when, &when
	input := SnapshotInput{Extraction: extract, Snapshot: history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "repair-hash", Size: 10, FirstTS: &when, LastTS: &when}}
	if _, err := database.ApplySnapshotBundle(source.Archive, "repair-fingerprint", []SnapshotInput{input}, false, when); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := database.db.QueryRow(`SELECT public_id FROM prompts`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordVaultBundleError(source.Archive, when.Add(time.Minute), errors.New("missing")); err != nil {
		t.Fatal(err)
	}
	health, err := database.Health()
	if err != nil || health.CoverageFirst != "" || health.CoverageLast != "" || health.IndexedVaultBundles != 0 || health.IndexedVaultVersions != 0 {
		t.Fatalf("failed bundle coverage=%+v err=%v", health, err)
	}
	if err := database.RecordVaultBundleIndexError(source.Archive, when.Add(90*time.Second), errors.New("history retry failed")); err != nil {
		t.Fatal(err)
	}
	health, err = database.Health()
	if err != nil || health.IndexedVaultBundles != 0 || health.IndexedVaultVersions != 0 {
		t.Fatalf("consumer retry cleared integrity invalidation: health=%+v err=%v", health, err)
	}
	var prompts, tombstones int
	if err := database.db.QueryRow(`SELECT (SELECT COUNT(*) FROM prompts),(SELECT COUNT(*) FROM vault_prompt_tombstones)`).Scan(&prompts, &tombstones); err != nil || prompts != 0 || tombstones != 1 {
		t.Fatalf("failed bundle prompts=%d tombstones=%d err=%v", prompts, tombstones, err)
	}
	liveWhen := when.Add(time.Hour)
	liveSource := sourceRef("/provider/repair.jsonl", history.LocationProviderLive)
	liveExtract := extraction("native:repair", "repair", liveSource, prompt("native:p", "p", "repair me", 1))
	liveExtract.Session.FirstTimestamp, liveExtract.Session.LastTimestamp = &liveWhen, &liveWhen
	liveExtract.Prompts[0].Timestamp = &liveWhen
	liveResult, err := database.ApplySource(liveExtract, head(liveSource, "live-repair-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	outageID := liveResult.PromptIDs["native:p"]
	health, err = database.Health()
	if err != nil || health.CoverageFirst != liveWhen.Format(time.RFC3339Nano) || health.CoverageLast != liveWhen.Format(time.RFC3339Nano) {
		t.Fatalf("live-only repair coverage=%+v err=%v", health, err)
	}
	if _, err := database.ApplySnapshotBundle(source.Archive, "repair-fingerprint", []SnapshotInput{input}, false, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := database.db.QueryRow(`SELECT public_id FROM prompts`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("repaired prompt ID changed %q -> %q", before, after)
	}
	resolved, err := database.ResolvePublicID(outageID)
	if err != nil || resolved != before {
		t.Fatalf("outage prompt alias %q resolved to %q, err=%v", outageID, resolved, err)
	}
	health, err = database.Health()
	if err != nil || health.IndexedVaultBundles != 1 || health.IndexedVaultVersions != 1 {
		t.Fatalf("repaired bundle health=%+v err=%v", health, err)
	}
}

func TestVaultBackfillRestoresPromptIDFromMissingProviderTombstone(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	liveSource := sourceRef("/provider/missing-before-vault.jsonl", history.LocationProviderLive)
	liveExtract := extraction("native:missing-before-vault", "missing-before-vault", liveSource, prompt("native:p", "p", "restore provider ID", 1))
	live, err := database.ApplySource(liveExtract, head(liveSource, "provider-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.MarkSourceMissing(history.ProviderCodex, liveSource.Path); err != nil {
		t.Fatal(err)
	}
	vaultSource := history.SourceReference{
		Provider: history.ProviderCodex, Kind: history.LocationVault, Path: liveSource.Path,
		Archive: "codex/provider-recovery.tar.zst", RelativePath: "missing-before-vault.jsonl", VaultVersion: 1,
	}
	vaultExtract := extraction("native:missing-before-vault", "missing-before-vault", vaultSource, prompt("native:p", "p", "restore provider ID", 1))
	restored, err := database.PreserveSnapshot(vaultExtract, history.PreservedSnapshot{
		Provider: history.ProviderCodex, ContentSHA256: "vault-provider-hash", Size: 10, FirstTS: &when, LastTS: &when,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.PromptIDs["native:p"] != live.PromptIDs["native:p"] {
		t.Fatalf("vault backfill changed missing provider prompt ID %q -> %q", live.PromptIDs["native:p"], restored.PromptIDs["native:p"])
	}
}

func TestExactSnapshotReindexDoesNotRestoreUnavailableLocationOccurrences(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	sourceA := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: "/gone/shared.jsonl", Archive: "codex/a.tar.zst", RelativePath: "shared.jsonl", VaultVersion: 1}
	extractA := extraction("native:shared", "shared", sourceA, prompt("native:p", "p", "shared prompt", 1))
	snapshot := history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "shared-hash", Size: 10, FirstTS: &when, LastTS: &when}
	if _, err := database.ApplySnapshotBundle(sourceA.Archive, "fingerprint-a", []SnapshotInput{{Extraction: extractA, Snapshot: snapshot}}, false, when); err != nil {
		t.Fatal(err)
	}
	var originalPromptID string
	if err := database.db.QueryRow(`SELECT public_id FROM prompts`).Scan(&originalPromptID); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordVaultBundleError(sourceA.Archive, when.Add(time.Minute), errors.New("missing a")); err != nil {
		t.Fatal(err)
	}
	sourceB := sourceA
	sourceB.Archive = "codex/b.tar.zst"
	extractB := extractA
	extractB.Source = sourceB
	if _, err := database.ApplySnapshotBundle(sourceB.Archive, "fingerprint-b", []SnapshotInput{{Extraction: extractB, Snapshot: snapshot}}, false, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var restoredPromptID string
	if err := database.db.QueryRow(`SELECT public_id FROM prompts`).Scan(&restoredPromptID); err != nil || restoredPromptID != originalPromptID {
		t.Fatalf("alternate archive restored prompt ID=%q want=%q err=%v", restoredPromptID, originalPromptID, err)
	}
	var unavailableOccurrences int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE l.available=0`).Scan(&unavailableOccurrences); err != nil || unavailableOccurrences != 0 {
		t.Fatalf("unavailable occurrences=%d err=%v", unavailableOccurrences, err)
	}
	if err := database.RecordVaultBundleError(sourceB.Archive, when.Add(3*time.Minute), errors.New("missing b")); err != nil {
		t.Fatal(err)
	}
	var prompts int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM prompts`).Scan(&prompts); err != nil || prompts != 0 {
		t.Fatalf("failed exact copies retained prompts=%d err=%v", prompts, err)
	}
}

func TestVaultBundleIndexErrorKeepsCoverageButDegradesHealth(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	when := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Path: "/gone/index-error.jsonl", Archive: "codex/index-error.tar.zst", RelativePath: "index-error.jsonl", VaultVersion: 1}
	extract := extraction("native:index-error", "index-error", source, prompt("native:p", "p", "still available", 1))
	if _, err := database.ApplySnapshotBundle(source.Archive, "index-error-fingerprint", []SnapshotInput{{
		Extraction: extract, Snapshot: history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "index-error-hash", Size: 10},
	}}, false, when); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordVaultBundleIndexError(source.Archive, when.Add(time.Minute), errors.New("sqlite apply failed")); err != nil {
		t.Fatal(err)
	}
	health, err := database.Health()
	page, listErr := database.ListCatalog(CatalogQuery{Source: CatalogSourceVault})
	if err != nil || listErr != nil || health.ErrorSources != 1 || health.IndexedVaultBundles != 1 || len(page.Sessions) != 1 {
		t.Fatalf("non-invalidating error health=%+v page=%+v err=%v listErr=%v", health, page, err, listErr)
	}
	generation := health.IndexGeneration
	if _, err := database.ApplySnapshotBundle(source.Archive, "index-error-fingerprint", []SnapshotInput{{
		Extraction: extract, Snapshot: history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "index-error-hash", Size: 10},
	}}, false, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	health, err = database.Health()
	if err != nil || health.ErrorSources != 0 || health.IndexGeneration != generation {
		t.Fatalf("unchanged consumer retry health=%+v generation=%d err=%v", health, generation, err)
	}
}

func TestMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = applySchemaStep(db, 1, `CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); CREATE TABLE broken (`)
	if err == nil {
		t.Fatal("broken migration succeeded")
	}
	var exists bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='meta')`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("failed migration retained schema or version")
	}
}

func TestConcurrentOpenIsSerializedBySQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	var wait sync.WaitGroup
	errorsFound := make(chan error, 12)
	for range 12 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			database, err := Open(path)
			if err == nil {
				err = database.Close()
			}
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileDSNEnablesForeignKeysOnEveryConnection(t *testing.T) {
	dsn, err := fileDSN(filepath.Join(t.TempDir(), "connections.db"), false)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(2)
	connections := make([]*sql.Conn, 0, 2)
	for range 2 {
		connection, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			connection.Close()
		}
	}()
	for _, connection := range connections {
		var enabled bool
		if err := connection.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&enabled); err != nil || !enabled {
			t.Fatalf("foreign keys enabled=%v err=%v", enabled, err)
		}
	}
}

func TestLockFailsFastAndPreservesNonContentionErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), DatabaseName)
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Lock(path); !errors.Is(err, ErrStoreInUse) {
		t.Fatalf("second lock error = %v", err)
	}
	if _, err := Lock(filepath.Join(t.TempDir(), "bad\x00path")); err == nil || errors.Is(err, ErrStoreInUse) {
		t.Fatalf("non-contention lock error = %v", err)
	}
}

func TestIdentityAppendFullReindexAndSourceRewrite(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	first := extraction("native:session-1", "session-1", source, prompt("native:p1", "p1", "first alpha", 1))
	result1, err := database.ApplySource(first, head(source, "hash-1", 12, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	assertPublicID(t, result1.SessionID, "ses_")
	assertPublicID(t, result1.SourceID, "src_")
	assertPublicID(t, result1.PromptIDs["native:p1"], "prm_")

	appendOnly := extraction("native:session-1", "session-1", source, prompt("native:p2", "p2", "second beta", 2))
	result2, err := database.ApplySource(appendOnly, head(source, "hash-2", 24, 2), ApplyAppend)
	if err != nil {
		t.Fatal(err)
	}
	if result2.SessionID != result1.SessionID || result2.SourceID != result1.SourceID {
		t.Fatalf("append changed stable IDs: %#v %#v", result1, result2)
	}
	stats, _ := database.Stats()
	if stats.Prompts != 2 || stats.Occurrences != 2 || stats.Sources != 1 {
		t.Fatalf("append stats = %#v", stats)
	}

	full := extraction("native:session-1", "session-1", source,
		prompt("native:p1", "p1", "first alpha", 1), prompt("native:p2", "p2", "second beta", 2))
	result3, err := database.ApplySource(full, head(source, "hash-2", 24, 2), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if result3.PromptIDs["native:p1"] != result1.PromptIDs["native:p1"] || result3.PromptIDs["native:p2"] != result2.PromptIDs["native:p2"] {
		t.Fatal("full reindex changed prompt IDs")
	}

	rewritten := extraction("native:session-1", "session-1", source, prompt("native:p3", "p3", "replacement gamma", 1))
	if _, err := database.ApplySource(rewritten, head(source, "hash-3", 14, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	stats, _ = database.Stats()
	if stats.Prompts != 1 || stats.Occurrences != 1 || matchCount(t, database, "alpha") != 0 || matchCount(t, database, "gamma") != 1 {
		t.Fatalf("rewrite retained unpreserved head: %#v", stats)
	}
}

func TestRewriteRetainsBoundedTombstonesWithoutPromptText(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/tombstones.jsonl", history.LocationProviderLive)
	prompts := make([]history.Prompt, 0, 300)
	for index := range 300 {
		prompts = append(prompts, prompt(fmt.Sprintf("native:old-%03d", index), fmt.Sprintf("old-%03d", index), fmt.Sprintf("private prompt %03d", index), int64(index+1)))
	}
	if _, err := database.ApplySource(extraction("native:old-session", "old-session", source, prompts...), head(source, "old-hash", 1000, 300), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	replacement := extraction("native:new-session", "new-session", source, prompt("native:new", "new", "replacement", 1))
	if _, err := database.ApplySource(replacement, head(source, "new-hash", 20, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM prompt_tombstones`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 256 {
		t.Fatalf("tombstone count = %d, want 256", count)
	}
	rows, err := database.db.Query(`PRAGMA table_info(prompt_tombstones)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if name == "clean_text" {
			t.Fatal("tombstones retain prompt text")
		}
	}
}

func TestNewSourceErrorsRemainVisibleUntilResolved(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	path := "/provider/unpublished.jsonl"
	if err := database.RecordSourceError(history.ProviderCodex, path, errors.New("source disappeared")); err != nil {
		t.Fatal(err)
	}
	if err := database.RecordRun(time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	health, err := database.Health()
	if err != nil {
		t.Fatal(err)
	}
	if health.ErrorSources != 1 || health.LastRunErrorCount != 0 {
		t.Fatalf("unpublished error health = %+v", health)
	}
	retained, err := database.SourceErrors()
	if err != nil || len(retained) != 1 || retained[0].Path != path {
		t.Fatalf("retained source errors = %+v err=%v", retained, err)
	}
	if err := database.ClearSourceError(history.ProviderCodex, path); err != nil {
		t.Fatal(err)
	}
	health, err = database.Health()
	if err != nil || health.ErrorSources != 0 {
		t.Fatalf("resolved source error health = %+v err=%v", health, err)
	}
}

func TestApplySourceRejectsMismatchedExtractionReference(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	extractedSource := sourceRef("/provider/extracted.jsonl", history.LocationProviderLive)
	headSource := sourceRef("/provider/other.jsonl", history.LocationProviderArchive)
	extract := extraction("native:session", "session", extractedSource, prompt("native:p1", "p1", "alpha", 1))
	if _, err := database.ApplySource(extract, head(headSource, "hash", 10, 1), ApplyReplace); err == nil {
		t.Fatal("mismatched extraction and source head succeeded")
	}
	stats, err := database.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != (Stats{}) {
		t.Fatalf("mismatched source mutated store: %#v", stats)
	}
}

func TestApplyAppendRejectsSourceSessionChange(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	first := extraction("native:session-1", "session-1", source, prompt("native:p1", "p1", "alpha", 1))
	if _, err := database.ApplySource(first, head(source, "one", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	second := extraction("native:session-2", "session-2", source, prompt("native:p2", "p2", "beta", 2))
	if _, err := database.ApplySource(second, head(source, "two", 20, 2), ApplyAppend); err == nil {
		t.Fatal("append reassigned source to another session")
	}
	stats, err := database.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sessions != 1 || stats.Prompts != 1 || stats.Occurrences != 1 || matchCount(t, database, "alpha") != 1 || matchCount(t, database, "beta") != 0 {
		t.Fatalf("rejected append mutated store: %#v", stats)
	}
}

func TestApplyReplaceCleansUpPreviousSourceSession(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/reused.jsonl", history.LocationProviderLive)
	january := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	first := extraction("native:session-1", "session-1", source, prompt("native:p1", "p1", "alpha", 1))
	first.Session.FirstTimestamp, first.Session.LastTimestamp = &january, &january
	if _, err := database.ApplySource(first, head(source, "one", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	july := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	second := extraction("native:session-2", "session-2", source, prompt("native:p2", "p2", "beta", 1))
	second.Session.FirstTimestamp, second.Session.LastTimestamp = &july, &july
	if _, err := database.ApplySource(second, head(source, "two", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	stats, err := database.Stats()
	if err != nil {
		t.Fatal(err)
	}
	var identity, firstTimestamp string
	if err := database.db.QueryRow(`SELECT identity_key,first_ts FROM sessions`).Scan(&identity, &firstTimestamp); err != nil {
		t.Fatal(err)
	}
	if stats.Sessions != 1 || identity != "native:session-2" || firstTimestamp != july.Format(time.RFC3339Nano) || matchCount(t, database, "alpha") != 0 || matchCount(t, database, "beta") != 1 {
		t.Fatalf("replacement session cleanup stats=%#v identity=%q first=%q", stats, identity, firstTimestamp)
	}
}

func TestSourceLocationAvailabilityTracksHead(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	extract := extraction("native:session", "session", source, prompt("native:p1", "p1", "first alpha", 1))
	currentHead := head(source, "hash-1", 12, 1)
	for _, available := range []bool{true, false, true} {
		currentHead.Available = available
		if _, err := database.ApplySource(extract, currentHead, ApplyReplace); err != nil {
			t.Fatal(err)
		}
		var sourceAvailable, locationAvailable bool
		if err := database.db.QueryRow(`SELECT s.available,l.available FROM source_heads s JOIN locations l ON l.source_head_id=s.id WHERE s.source_path=?`, source.Path).Scan(&sourceAvailable, &locationAvailable); err != nil {
			t.Fatal(err)
		}
		if sourceAvailable != available || locationAvailable != available {
			t.Fatalf("available=%v source=%v location=%v", available, sourceAvailable, locationAvailable)
		}
		stats, err := database.Stats()
		if err != nil {
			t.Fatal(err)
		}
		wantPrompts := 0
		if available {
			wantPrompts = 1
		}
		if stats.Prompts != wantPrompts || matchCount(t, database, "alpha") != wantPrompts {
			t.Fatalf("available=%v retained derived prompt content: %#v", available, stats)
		}
	}
}

func TestSourceReplacementCanonicalUsesCurrentBytesWhenUnpreserved(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	newer := time.Date(2026, 7, 20, 14, 0, 0, 0, time.UTC)
	oldCanonical := prompt("native:p1", "p1", "long stale alpha text", 1)
	oldCanonical.Timestamp = &newer
	initial, err := database.ApplySource(extraction("native:session", "session", source, oldCanonical), head(source, "old", 20, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	earlier := newer.Add(-time.Hour)
	current := prompt("native:p1", "p1", "beta", 1)
	current.Timestamp = &earlier
	replaced, err := database.ApplySource(extraction("native:session", "session", source, current), head(source, "new", 4, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.PromptIDs["native:p1"] != initial.PromptIDs["native:p1"] || matchCount(t, database, "alpha") != 0 || matchCount(t, database, "beta") != 1 {
		t.Fatalf("replacement retained stale canonical: initial=%#v replaced=%#v", initial, replaced)
	}
}

func TestOccurrenceVariantsPreserveLineSpecificText(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/progressive.jsonl", history.LocationProviderLive)
	draft := prompt("native:p1", "p1", "draft alpha", 1)
	final := prompt("native:p1", "p1", "final beta", 2)
	extract := extraction("native:session", "session", source, final)
	extract.Occurrences = []history.Occurrence{
		{PromptKey: "native:p1", Variant: draft, LineNumber: 1, EndOffset: 10},
		{PromptKey: "native:p1", Variant: final, LineNumber: 2, StartOffset: 10, EndOffset: 20},
	}
	if _, err := database.ApplySource(extract, head(source, "progressive", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	rows, err := database.db.Query(`SELECT clean_text FROM occurrences ORDER BY line_number`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if len(values) != 2 || values[0] != "draft alpha" || values[1] != "final beta" {
		t.Fatalf("occurrence variants = %#v", values)
	}
}

func TestUnavailableSourceRestoresCanonicalFromSurvivingSnapshot(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "session.jsonl", VaultVersion: 1}
	draft := prompt("native:p1", "p1", "draft alpha", 1)
	draft.Timestamp = &base
	if _, err := database.PreserveSnapshot(extraction("native:session", "session", vault, draft), history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "draft", Size: 10}); err != nil {
		t.Fatal(err)
	}
	live := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	final := prompt("native:p1", "p1", "final beta", 1)
	finalTime := base.Add(time.Hour)
	final.Timestamp = &finalTime
	if _, err := database.ApplySource(extraction("native:session", "session", live, final), head(live, "final", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "beta") != 1 {
		t.Fatal("newer live canonical was not selected")
	}
	missing := head(live, "", 0, 0)
	missing.Available = false
	if _, err := database.ApplySource(extraction("native:session", "session", live), missing, ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "beta") != 0 || matchCount(t, database, "alpha") != 1 {
		t.Fatal("unavailable live source retained unreconstructible canonical text")
	}
}

func TestCanonicalRefreshPrefersConfirmedCodexOccurrence(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	timestamp := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	response := prompt("native:p1", "p1", "paired alpha", 1)
	response.Timestamp = &timestamp
	response.Classification = history.ClassificationProviderMetadata
	response.Searchable = false
	event := response
	event.Classification = history.ClassificationHuman
	event.Searchable = true
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "paired.jsonl", VaultVersion: 1}
	preserved := extraction("native:session", "session", vault, event)
	preserved.Occurrences = []history.Occurrence{
		{PromptKey: event.LogicalKey, Variant: response, LineNumber: 1, EndOffset: 10},
		{PromptKey: event.LogicalKey, Variant: event, LineNumber: 2, StartOffset: 10, EndOffset: 20},
	}
	if _, err := database.PreserveSnapshot(preserved, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "paired", Size: 20}); err != nil {
		t.Fatal(err)
	}
	live := sourceRef("/provider/paired.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:session", "session", live, event), head(live, "live", 20, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	missing := head(live, "", 0, 0)
	missing.Available = false
	if _, err := database.ApplySource(extraction("native:session", "session", live), missing, ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "alpha") != 1 {
		t.Fatal("canonical refresh selected unconfirmed paired representation")
	}
}

func TestCanonicalRefreshOrdersFractionalTimestampsChronologically(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	for index, variant := range []struct {
		text      string
		timestamp time.Time
	}{
		{text: "early alpha", timestamp: base},
		{text: "later gamma", timestamp: base.Add(time.Millisecond)},
	} {
		source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: fmt.Sprintf("%d.jsonl", index), VaultVersion: 1}
		p := prompt("native:p1", "p1", variant.text, 1)
		p.Timestamp = &variant.timestamp
		if _, err := database.PreserveSnapshot(extraction("native:session", "session", source, p), history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: fmt.Sprintf("hash-%d", index), Size: 10}); err != nil {
			t.Fatal(err)
		}
	}
	live := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	latest := prompt("native:p1", "p1", "live beta", 1)
	liveTime := base.Add(time.Hour)
	latest.Timestamp = &liveTime
	if _, err := database.ApplySource(extraction("native:session", "session", live, latest), head(live, "live", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	missing := head(live, "", 0, 0)
	missing.Available = false
	if _, err := database.ApplySource(extraction("native:session", "session", live), missing, ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "gamma") != 1 || matchCount(t, database, "alpha") != 0 || matchCount(t, database, "beta") != 0 {
		t.Fatal("fractional timestamp canonical refresh selected stale text")
	}
}

func TestIdentityPromotesFallbackSessionWhenNativeIDAppears(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	initial := extraction("fallback:first-record:abc", "", source, prompt("native:p1", "p1", "first", 1))
	first, err := database.ApplySource(initial, head(source, "h1", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	native := extraction("native:session-1", "session-1", source, prompt("native:p1", "p1", "first", 1), prompt("native:p2", "p2", "second", 2))
	nativeHead := head(source, "h2", 20, 2)
	nativeHead.VerifiedContinuity = true
	promoted, err := database.ApplySource(native, nativeHead, ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.SessionID != first.SessionID || promoted.PromptIDs["native:p1"] != first.PromptIDs["native:p1"] {
		t.Fatalf("promotion changed IDs: %#v -> %#v", first, promoted)
	}
	stats, _ := database.Stats()
	if stats.Sessions != 1 || stats.Prompts != 2 || stats.Occurrences != 2 {
		t.Fatalf("promotion stats = %#v", stats)
	}
}

func TestReplacementDoesNotPromoteUnrelatedFallbackSession(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	live := sourceRef("/provider/reused.jsonl", history.LocationProviderLive)
	old := extraction("fallback:first-record:old", "", live, prompt("record:old", "", "old alpha", 1))
	old.Session.FallbackKey = "first-record:old"
	fallback, err := database.ApplySource(old, head(live, "old-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "old.jsonl", VaultVersion: 1}
	old.Source = vault
	if _, err := database.PreserveSnapshot(old, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "old-hash", Size: 10}); err != nil {
		t.Fatal(err)
	}
	native := extraction("native:new-session", "new-session", live, prompt("native:new", "new", "new beta", 1))
	replaced, err := database.ApplySource(native, head(live, "new-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := database.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if replaced.SessionID == fallback.SessionID || stats.Sessions != 2 || stats.Snapshots != 1 || matchCount(t, database, "alpha") != 1 || matchCount(t, database, "beta") != 1 {
		t.Fatalf("replacement merged unrelated fallback: fallback=%#v replacement=%#v stats=%#v", fallback, replaced, stats)
	}
}

func TestIdentityPromotionMergesAnExistingNativeSession(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	nativeSource := sourceRef("/provider/native.jsonl", history.LocationProviderLive)
	nativePrompt := prompt("native:p1", "p1", "canonical", 1)
	nativeTime := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	nativePrompt.Timestamp = &nativeTime
	nativeExtraction := extraction("native:session-1", "session-1", nativeSource, nativePrompt)
	firstTime, lastTime := nativeTime.Add(-2*time.Hour), nativeTime.Add(2*time.Hour)
	nativeExtraction.Session.CWD = "/native"
	nativeExtraction.Session.Branch = "native-branch"
	nativeExtraction.Session.RepositoryIdentity = "https://example.invalid/native.git"
	nativeExtraction.Session.ThreadKind = history.ThreadSubagent
	nativeExtraction.Session.ParentNativeSessionID = "parent"
	nativeExtraction.Session.FirstTimestamp, nativeExtraction.Session.LastTimestamp = &firstTime, &lastTime
	nativeResult, err := database.ApplySource(nativeExtraction, head(nativeSource, "native-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	fallbackSource := sourceRef("/provider/fallback.jsonl", history.LocationProviderLive)
	oldPrompt := prompt("native:p1", "p1", "old", 1)
	oldTime := nativeTime.Add(-time.Hour)
	oldPrompt.Timestamp = &oldTime
	fallback, err := database.ApplySource(extraction("fallback:first-record:abc", "", fallbackSource, oldPrompt), head(fallbackSource, "fallback-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	promotionExtraction := extraction("native:session-1", "session-1", fallbackSource, nativePrompt)
	promotionExtraction.Session.ThreadKind = history.ThreadUnknown
	promotionExtraction.Session.Confidence = history.ConfidenceUnknown
	promoted, err := database.ApplySource(promotionExtraction, head(fallbackSource, "promoted-hash", 10, 1), ApplyAppend)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.SessionID != fallback.SessionID {
		t.Fatalf("promotion did not preserve fallback public ID: %q != %q", promoted.SessionID, fallback.SessionID)
	}
	resolvedSession, err := database.ResolvePublicID(nativeResult.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	resolvedPrompt, err := database.ResolvePublicID(nativeResult.PromptIDs["native:p1"])
	if err != nil {
		t.Fatal(err)
	}
	if resolvedSession != promoted.SessionID || resolvedPrompt != promoted.PromptIDs["native:p1"] {
		t.Fatalf("promotion aliases session=%q prompt=%q", resolvedSession, resolvedPrompt)
	}
	stats, _ := database.Stats()
	if stats.Sessions != 1 || stats.Sources != 2 || stats.Prompts != 1 || stats.Occurrences != 2 || matchCount(t, database, "canonical") != 1 {
		t.Fatalf("merged promotion stats = %#v", stats)
	}
	var cwd, branch, repository, threadKind, parent, first, last string
	if err := database.db.QueryRow(`SELECT cwd,branch,repository_identity,thread_kind,parent_native_session_id,first_ts,last_ts FROM sessions`).Scan(&cwd, &branch, &repository, &threadKind, &parent, &first, &last); err != nil {
		t.Fatal(err)
	}
	if cwd != "/native" || branch != "native-branch" || repository != "https://example.invalid/native.git" || threadKind != string(history.ThreadSubagent) || parent != "parent" || first != firstTime.Format(time.RFC3339Nano) || last != lastTime.Format(time.RFC3339Nano) {
		t.Fatalf("merged metadata = cwd=%q branch=%q repo=%q thread=%q parent=%q range=%q..%q", cwd, branch, repository, threadKind, parent, first, last)
	}
}

func TestPreservedSnapshotKeepsHistoricalPromptAcrossRewrite(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	live := sourceRef("/provider/session.jsonl", history.LocationProviderLive)
	old := extraction("native:session-1", "session-1", live, prompt("native:old", "old", "historical phrase", 1))
	if _, err := database.ApplySource(old, head(live, "old-hash", 20, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault.tar.zst", RelativePath: "sessions/session.jsonl", VaultVersion: 1}
	old.Source = vault
	preserved, err := database.PreserveSnapshot(old, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "old-hash", Size: 20})
	if err != nil {
		t.Fatal(err)
	}
	assertPublicID(t, preserved.SourceID, "snap_")
	current := extraction("native:session-1", "session-1", live, prompt("native:new", "new", "current phrase", 1))
	if _, err := database.ApplySource(current, head(live, "new-hash", 18, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	stats, _ := database.Stats()
	if stats.Prompts != 2 || stats.Snapshots != 1 || stats.Occurrences != 2 || matchCount(t, database, "historical") != 1 {
		t.Fatalf("preserved stats = %#v", stats)
	}
}

func TestPreservedSnapshotReextractReplacesStaleOccurrences(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault.tar.zst", RelativePath: "session.jsonl", VaultVersion: 1}
	first := extraction("native:session", "session", vault, prompt("native:p1", "p1", "keep alpha", 1), prompt("native:p2", "p2", "stale beta", 2))
	snapshot := history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "snapshot-hash", Size: 30}
	if _, err := database.PreserveSnapshot(first, snapshot); err != nil {
		t.Fatal(err)
	}
	second := extraction("native:session", "session", vault, prompt("native:p1", "p1", "updated gamma", 1))
	if _, err := database.PreserveSnapshot(second, snapshot); err != nil {
		t.Fatal(err)
	}
	stats, _ := database.Stats()
	if stats.Prompts != 1 || stats.Occurrences != 1 || matchCount(t, database, "beta") != 0 || matchCount(t, database, "gamma") != 1 {
		t.Fatalf("snapshot reextract stats = %#v", stats)
	}
}

func TestPreservedSnapshotReextractFillsTimestampBoundsAndRejectsConflicts(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "session.jsonl", VaultVersion: 1}
	extract := extraction("native:session", "session", vault, prompt("native:p1", "p1", "prompt", 1))
	snapshot := history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "timestamp-hash", Size: 10}
	if _, err := database.PreserveSnapshot(extract, snapshot); err != nil {
		t.Fatal(err)
	}
	first, last := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC), time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	snapshot.FirstTS, snapshot.LastTS = &first, &last
	if _, err := database.PreserveSnapshot(extract, snapshot); err != nil {
		t.Fatal(err)
	}
	var storedFirst, storedLast string
	if err := database.db.QueryRow(`SELECT first_ts,last_ts FROM preserved_snapshots WHERE content_sha256=?`, snapshot.ContentSHA256).Scan(&storedFirst, &storedLast); err != nil {
		t.Fatal(err)
	}
	if storedFirst != first.Format(time.RFC3339Nano) || storedLast != last.Format(time.RFC3339Nano) {
		t.Fatalf("snapshot range = %q..%q", storedFirst, storedLast)
	}
	conflicting := first.Add(time.Minute)
	snapshot.FirstTS = &conflicting
	if _, err := database.PreserveSnapshot(extract, snapshot); err == nil {
		t.Fatal("conflicting immutable snapshot timestamp succeeded")
	}
}

func TestSharedSnapshotReextractUpdatesEveryLocation(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	snapshot := history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "shared-hash", Size: 30}
	locations := []history.SourceReference{
		{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "one", RelativePath: "session.jsonl", VaultVersion: 1},
		{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "two", RelativePath: "session.jsonl", VaultVersion: 2},
	}
	for _, source := range locations {
		full := extraction("native:session", "session", source, prompt("native:p1", "p1", "keep alpha", 1), prompt("native:p2", "p2", "stale beta", 2))
		if _, err := database.PreserveSnapshot(full, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	current := extraction("native:session", "session", locations[0], prompt("native:p1", "p1", "keep alpha", 1))
	if _, err := database.PreserveSnapshot(current, snapshot); err != nil {
		t.Fatal(err)
	}
	stats, _ := database.Stats()
	if stats.Locations != 2 || stats.Prompts != 1 || stats.Occurrences != 2 || matchCount(t, database, "beta") != 0 {
		t.Fatalf("shared reextract stats = %#v", stats)
	}
}

func TestPromptCanonicalizationIsIndependentOfIndexOrder(t *testing.T) {
	older := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	orders := []bool{false, true}
	for _, olderFirst := range orders {
		database := openTestStore(t)
		live := sourceRef("/live.jsonl", history.LocationProviderLive)
		vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "session.jsonl", VaultVersion: 1}
		oldPrompt := prompt("native:p1", "p1", "older alpha", 1)
		oldPrompt.Timestamp = &older
		newPrompt := prompt("native:p1", "p1", "newer gamma", 1)
		newPrompt.Timestamp = &newer
		applyOld := func() {
			if _, err := database.PreserveSnapshot(extraction("native:session", "session", vault, oldPrompt), history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "old-hash", Size: 10}); err != nil {
				t.Fatal(err)
			}
		}
		applyNew := func() {
			if _, err := database.ApplySource(extraction("native:session", "session", live, newPrompt), head(live, "new-hash", 10, 1), ApplyReplace); err != nil {
				t.Fatal(err)
			}
		}
		if olderFirst {
			applyOld()
			applyNew()
		} else {
			applyNew()
			applyOld()
		}
		if matchCount(t, database, "gamma") != 1 || matchCount(t, database, "alpha") != 0 {
			t.Fatalf("olderFirst=%v produced order-dependent FTS", olderFirst)
		}
		database.Close()
	}
}

func TestExactSnapshotAtTwoPathsSharesContentNotLocations(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	p := prompt("native:p1", "p1", "shared exact bytes", 1)
	for index, rel := range []string{"a/session.jsonl", "b/session.jsonl"} {
		source := history.SourceReference{Provider: history.ProviderClaude, Kind: history.LocationVault, Archive: "vault.tar.zst", RelativePath: rel, VaultVersion: index + 1}
		extract := extraction("native:session", "session", source, p)
		if _, err := database.PreserveSnapshot(extract, history.PreservedSnapshot{Provider: history.ProviderClaude, ContentSHA256: "same-hash", Size: 50}); err != nil {
			t.Fatal(err)
		}
	}
	stats, _ := database.Stats()
	if stats.Snapshots != 1 || stats.Locations != 2 || stats.Prompts != 1 || stats.Occurrences != 2 || matchCount(t, database, "shared") != 1 {
		t.Fatalf("exact-copy stats = %#v", stats)
	}
}

func TestVaultLocationIdentityIsUnambiguous(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	p := prompt("native:p1", "p1", "shared", 1)
	locations := []history.SourceReference{
		{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "a:b", RelativePath: "c", VaultVersion: 1},
		{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "a", RelativePath: "b:c", VaultVersion: 1},
	}
	for _, source := range locations {
		if _, err := database.PreserveSnapshot(extraction("native:session", "session", source, p), history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "same", Size: 10}); err != nil {
			t.Fatal(err)
		}
	}
	stats, _ := database.Stats()
	if stats.Locations != 2 || stats.Occurrences != 2 {
		t.Fatalf("ambiguous location stats = %#v", stats)
	}
}

func TestVaultLocationRejectsConflictingSnapshotContent(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "session.jsonl", VaultVersion: 1}
	first := extraction("native:session", "session", source, prompt("native:p1", "p1", "first", 1))
	if _, err := database.PreserveSnapshot(first, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "hash-one", Size: 10}); err != nil {
		t.Fatal(err)
	}
	second := extraction("native:session", "session", source, prompt("native:p2", "p2", "second", 1))
	if _, err := database.PreserveSnapshot(second, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "hash-two", Size: 10}); err == nil {
		t.Fatal("physical vault location accepted conflicting content")
	}
}

func TestSessionTimestampRangeIsIndependentOfIndexOrder(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	newer := time.Date(2026, 7, 20, 14, 0, 0, 0, time.UTC)
	newest := newer.Add(time.Hour)
	older := newer.Add(-2 * time.Hour)
	oldest := older.Add(-time.Hour)
	source := sourceRef("/newer.jsonl", history.LocationProviderLive)
	extract := extraction("native:session", "session", source, prompt("native:p1", "p1", "newer", 1))
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &newer, &newest
	extract.Session.ThreadKind = history.ThreadSubagent
	if _, err := database.ApplySource(extract, head(source, "new", 1, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	vault := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "older", VaultVersion: 1}
	extract.Source = vault
	extract.Session.FirstTimestamp, extract.Session.LastTimestamp = &oldest, &older
	extract.Session.ThreadKind = history.ThreadUnknown
	extract.Session.Confidence = history.ConfidenceUnknown
	if _, err := database.PreserveSnapshot(extract, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "old", Size: 1}); err != nil {
		t.Fatal(err)
	}
	var first, last, threadKind, confidence string
	if err := database.db.QueryRow(`SELECT first_ts,last_ts,thread_kind,confidence FROM sessions WHERE identity_key='native:session'`).Scan(&first, &last, &threadKind, &confidence); err != nil {
		t.Fatal(err)
	}
	if first != oldest.Format(time.RFC3339Nano) || last != newest.Format(time.RFC3339Nano) || threadKind != string(history.ThreadSubagent) || confidence != string(history.ConfidenceExact) {
		t.Fatalf("range = %q..%q thread=%q confidence=%q", first, last, threadKind, confidence)
	}
}

func TestSourceReplacementRecomputesSessionTimestampBounds(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/rewritten.jsonl", history.LocationProviderLive)
	januaryFirst := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	januaryLast := januaryFirst.Add(time.Hour)
	initial := extraction("native:session", "session", source, prompt("native:p1", "p1", "january", 1))
	initial.Session.FirstTimestamp, initial.Session.LastTimestamp = &januaryFirst, &januaryLast
	if _, err := database.ApplySource(initial, head(source, "january", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	julyFirst := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	julyLast := julyFirst.Add(time.Hour)
	replacement := extraction("native:session", "session", source, prompt("native:p2", "p2", "july", 1))
	replacement.Session.FirstTimestamp, replacement.Session.LastTimestamp = &julyFirst, &julyLast
	if _, err := database.ApplySource(replacement, head(source, "july", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	var first, last string
	if err := database.db.QueryRow(`SELECT first_ts,last_ts FROM sessions WHERE identity_key='native:session'`).Scan(&first, &last); err != nil {
		t.Fatal(err)
	}
	if first != julyFirst.Format(time.RFC3339Nano) || last != julyLast.Format(time.RFC3339Nano) {
		t.Fatalf("replacement bounds = %q..%q", first, last)
	}
}

func TestMissingSourceRecomputesSessionTimestampBounds(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	olderSource := sourceRef("/older.jsonl", history.LocationProviderLive)
	older := extraction("native:session", "session", olderSource, prompt("native:old", "old", "older", 1))
	olderFirst, olderLast := base.Add(-2*time.Hour), base.Add(-time.Hour)
	older.Session.FirstTimestamp, older.Session.LastTimestamp = &olderFirst, &olderLast
	if _, err := database.ApplySource(older, head(olderSource, "older", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	newerSource := sourceRef("/newer.jsonl", history.LocationProviderLive)
	newer := extraction("native:session", "session", newerSource, prompt("native:new", "new", "newer", 1))
	newerFirst, newerLast := base, base.Add(time.Hour)
	newer.Session.FirstTimestamp, newer.Session.LastTimestamp = &newerFirst, &newerLast
	if _, err := database.ApplySource(newer, head(newerSource, "newer", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	changed, err := database.MarkSourceMissing(history.ProviderCodex, olderSource.Path)
	if err != nil || !changed {
		t.Fatalf("mark missing changed=%v err=%v", changed, err)
	}
	var first, last string
	if err := database.db.QueryRow(`SELECT first_ts,last_ts FROM sessions WHERE identity_key='native:session'`).Scan(&first, &last); err != nil {
		t.Fatal(err)
	}
	if first != newerFirst.Format(time.RFC3339Nano) || last != newerLast.Format(time.RFC3339Nano) {
		t.Fatalf("bounds after missing source = %q..%q", first, last)
	}
}

func TestSessionMetadataIsIndependentOfIndexOrder(t *testing.T) {
	type metadata struct {
		cwd, repositoryRoot, repositoryName, repositoryIdentity string
		branch, threadKind, parent, originator, evidence        string
		confidence, first, last                                 string
	}
	index := func(t *testing.T, olderFirst bool) metadata {
		t.Helper()
		database := openTestStore(t)
		defer database.Close()
		base := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
		olderSource := history.SourceReference{Provider: history.ProviderCodex, Kind: history.LocationVault, Archive: "vault", RelativePath: "older.jsonl", VaultVersion: 1}
		newerSource := sourceRef("/live/newer.jsonl", history.LocationProviderLive)
		older := extraction("native:session", "session", olderSource, prompt("native:old", "old", "old", 1))
		older.Session.CWD = "/older"
		older.Session.RepositoryRoot = "/repo/older"
		older.Session.RepositoryName = "older"
		older.Session.RepositoryIdentity = "https://example.invalid/older.git"
		older.Session.Branch = "older"
		older.Session.ThreadKind = history.ThreadUnknown
		older.Session.Originator = "older-origin"
		older.Session.Evidence = "older-evidence"
		older.Session.Confidence = history.ConfidenceDerived
		olderFirstTime, olderLastTime := base, base.Add(time.Hour)
		older.Session.FirstTimestamp, older.Session.LastTimestamp = &olderFirstTime, &olderLastTime
		newer := extraction("native:session", "session", newerSource, prompt("native:new", "new", "new", 1))
		newer.Session.CWD = "/newer"
		newer.Session.RepositoryRoot = "/repo/newer"
		newer.Session.RepositoryName = "newer"
		newer.Session.RepositoryIdentity = "https://example.invalid/newer.git"
		newer.Session.Branch = "newer"
		newer.Session.ThreadKind = history.ThreadSubagent
		newer.Session.ParentNativeSessionID = "parent"
		newer.Session.Originator = "newer-origin"
		newer.Session.Evidence = "newer-evidence"
		newer.Session.Confidence = history.ConfidenceExact
		newerFirstTime, newerLastTime := base.Add(2*time.Hour), base.Add(3*time.Hour)
		newer.Session.FirstTimestamp, newer.Session.LastTimestamp = &newerFirstTime, &newerLastTime
		applyOlder := func() {
			if _, err := database.PreserveSnapshot(older, history.PreservedSnapshot{Provider: history.ProviderCodex, ContentSHA256: "older", Size: 1}); err != nil {
				t.Fatal(err)
			}
		}
		applyNewer := func() {
			if _, err := database.ApplySource(newer, head(newerSource, "newer", 1, 1), ApplyReplace); err != nil {
				t.Fatal(err)
			}
		}
		if olderFirst {
			applyOlder()
			applyNewer()
		} else {
			applyNewer()
			applyOlder()
		}
		var got metadata
		if err := database.db.QueryRow(`SELECT cwd,repository_root,repository_name,repository_identity,branch,thread_kind,parent_native_session_id,originator,evidence,confidence,first_ts,last_ts FROM sessions WHERE identity_key='native:session'`).Scan(
			&got.cwd, &got.repositoryRoot, &got.repositoryName, &got.repositoryIdentity, &got.branch, &got.threadKind, &got.parent,
			&got.originator, &got.evidence, &got.confidence, &got.first, &got.last); err != nil {
			t.Fatal(err)
		}
		return got
	}
	forward := index(t, true)
	reverse := index(t, false)
	if forward != reverse {
		t.Fatalf("session metadata depends on index order:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	if forward.cwd != "/newer" || forward.threadKind != string(history.ThreadSubagent) || forward.first != "2026-07-20T12:00:00Z" || forward.last != "2026-07-20T15:00:00Z" {
		t.Fatalf("unexpected canonical metadata: %#v", forward)
	}
}

func TestProviderArchiveMovePreservesSourceIDAndCopyDoesNot(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	live := sourceRef("/provider/live.jsonl", history.LocationProviderLive)
	extract := extraction("native:session", "session", live, prompt("native:p1", "p1", "move me", 1))
	initial, err := database.ApplySource(extract, head(live, "hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	archive := sourceRef("/provider/archive/session.jsonl", history.LocationProviderArchive)
	if err := database.RelocateSource(history.ProviderCodex, live.Path, archive); err != nil {
		t.Fatal(err)
	}
	var occurrencePath string
	if err := database.db.QueryRow(`SELECT l.source_path FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.source_head_id=(SELECT id FROM source_heads WHERE public_id=?)`, initial.SourceID).Scan(&occurrencePath); err != nil {
		t.Fatal(err)
	}
	if occurrencePath != archive.Path {
		t.Fatalf("relocated occurrence path = %q", occurrencePath)
	}
	archiveSample, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{Source: CatalogSourceProviderArchive}, Count: 1})
	if err != nil || len(archiveSample.Items) != 1 {
		t.Fatalf("archive sample after relocation=%+v err=%v", archiveSample, err)
	}
	liveSample, err := database.Sample(SampleQuery{PromptQuery: PromptQuery{Source: CatalogSourceProviderLive}, Count: 1})
	if err != nil || len(liveSample.Items) != 0 {
		t.Fatalf("live sample after relocation=%+v err=%v", liveSample, err)
	}
	extract.Source = archive
	moved, err := database.ApplySource(extract, head(archive, "hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if moved.SourceID != initial.SourceID {
		t.Fatalf("move changed source ID %q -> %q", initial.SourceID, moved.SourceID)
	}
	copySource := sourceRef("/provider/archive/copy.jsonl", history.LocationProviderArchive)
	extract.Source = copySource
	copied, err := database.ApplySource(extract, head(copySource, "hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if copied.SourceID == initial.SourceID {
		t.Fatal("copy reused mutable source ID")
	}
	reused := extraction("native:replacement-session", "replacement-session", live, prompt("native:replacement", "replacement", "new transcript", 1))
	reusedResult, err := database.ApplySource(reused, head(live, "replacement", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if reusedResult.SourceID == initial.SourceID {
		t.Fatal("reused provider path retained relocated source identity")
	}
}

func TestSourceUpdateRefusesNewerExtractorVersion(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/future.jsonl", history.LocationProviderLive)
	initial := extraction("native:session", "session", source, prompt("native:p1", "p1", "alpha", 1))
	if _, err := database.ApplySource(initial, head(source, "one", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE source_heads SET extractor_version=? WHERE source_path=?`, history.ExtractorVersion+1, source.Path); err != nil {
		t.Fatal(err)
	}
	replacement := extraction("native:session", "session", source, prompt("native:p1", "p1", "beta", 1))
	if _, err := database.ApplySource(replacement, head(source, "two", 10, 1), ApplyReplace); err == nil {
		t.Fatal("older extractor replaced newer source state")
	}
	if matchCount(t, database, "alpha") != 1 || matchCount(t, database, "beta") != 0 {
		t.Fatal("failed downgrade changed prompt content")
	}
}

func TestFallbackAndNativeSessionIdentityRules(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	first := sourceRef("/one.jsonl", history.LocationProviderLive)
	a := extraction("fallback:first-record:abc", "", first, prompt("record:a:1", "", "fallback", 1))
	resultA, err := database.ApplySource(a, head(first, "h1", 1, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	second := sourceRef("/two.jsonl", history.LocationProviderArchive)
	a.Source = second
	resultB, err := database.ApplySource(a, head(second, "h2", 2, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if resultA.SessionID != resultB.SessionID {
		t.Fatal("fallback identity did not reconcile across paths")
	}

	nativeSource := sourceRef("/native.jsonl", history.LocationProviderLive)
	native := extraction("native:reused", "reused", nativeSource, prompt("native:n1", "n1", "v1", 1))
	n1, _ := database.ApplySource(native, head(nativeSource, "v1", 1, 1), ApplyReplace)
	native.Prompts = []history.Prompt{prompt("native:n2", "n2", "v2", 1)}
	native.Occurrences = []history.Occurrence{{PromptKey: "native:n2", LineNumber: 1, EndOffset: 1}}
	n2, _ := database.ApplySource(native, head(nativeSource, "v2", 1, 1), ApplyReplace)
	if n1.SessionID != n2.SessionID {
		t.Fatal("native session ID reuse split logical session")
	}
}

func TestPathFallbackPromotionPreservesSessionID(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/empty-then-populated.jsonl", history.LocationProviderLive)
	empty := extraction("fallback:source-path:"+source.Path, "", source)
	empty.Session.FallbackKey = "source-path:" + source.Path
	initial, err := database.ApplySource(empty, head(source, "empty", 0, 0), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	populated := extraction("fallback:first-record:abc", "", source, prompt("record:p1", "", "first", 1))
	populated.Session.FallbackKey = "first-record:abc"
	promoted, err := database.ApplySource(populated, head(source, "content", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if promoted.SessionID != initial.SessionID {
		t.Fatalf("path fallback promotion changed session ID: %#v -> %#v", initial, promoted)
	}
}

func TestFTSTriggersRollbackCascadeAndRebuild(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/fts.jsonl", history.LocationProviderLive)
	extract := extraction("native:fts", "fts", source, prompt("native:p", "p", "alpha token", 1))
	initialTimestamp := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	extract.Prompts[0].Timestamp = &initialTimestamp
	result, err := database.ApplySource(extract, head(source, "h1", 1, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "alpha") != 1 {
		t.Fatal("FTS insert trigger missed prompt")
	}
	extract.Prompts[0].CleanText = "beta token"
	updatedTimestamp := initialTimestamp.Add(time.Minute)
	extract.Prompts[0].Timestamp = &updatedTimestamp
	if _, err := database.ApplySource(extract, head(source, "h1", 1, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "alpha") != 0 || matchCount(t, database, "beta") != 1 {
		t.Fatal("FTS update trigger stale")
	}

	err = database.Transaction(func(tx *Tx) error {
		_, err := tx.tx.Exec(`UPDATE prompts SET clean_text='rollback token' WHERE public_id=?`, result.PromptIDs["native:p"])
		if err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil || matchCount(t, database, "rollback") != 0 || matchCount(t, database, "beta") != 1 {
		t.Fatal("FTS transaction rollback failed")
	}
	if err := database.CheckFTS(); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO prompt_fts(prompt_fts) VALUES('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if err := database.RebuildFTS(); err != nil || matchCount(t, database, "beta") != 1 {
		t.Fatalf("rebuild = %v", err)
	}
	if _, err := database.db.Exec(`DELETE FROM sessions WHERE public_id=?`, result.SessionID); err != nil {
		t.Fatal(err)
	}
	stats, _ := database.Stats()
	if stats.Prompts != 0 || stats.Occurrences != 0 || stats.Sources != 0 || matchCount(t, database, "beta") != 0 {
		t.Fatalf("foreign-key cascade = %#v", stats)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	database, err := Open(filepath.Join(t.TempDir(), DatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func sourceRef(path string, kind history.LocationKind) history.SourceReference {
	return history.SourceReference{Provider: history.ProviderCodex, Kind: kind, Path: path}
}

func head(source history.SourceReference, hash string, size, lines int64) history.SourceHead {
	return history.SourceHead{Source: source, ContentSHA256: hash, Size: size, CompleteOffset: size, LineCount: lines, Available: true}
}

func TestAssistantConsentDisablePrunesRowsAndFTS(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	if err := database.ConfigureAssistantIndexing(true); err != nil {
		t.Fatal(err)
	}
	source := sourceRef("/provider/assistant-prune.jsonl", history.LocationProviderLive)
	user := prompt("native:user", "user", "user retained", 1)
	assistant := prompt("native:assistant", "assistant", "assistant removed", 2)
	assistant.Role = history.RoleAssistant
	assistant.Classification = history.ClassificationAssistant
	if _, err := database.ApplySource(extraction("native:assistant-prune", "assistant-prune", source, user, assistant), head(source, "hash", 20, 2), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if matchCount(t, database, "assistant") != 1 {
		t.Fatal("assistant prompt did not enter FTS")
	}
	if err := database.ConfigureAssistantIndexing(false); err != nil {
		t.Fatal(err)
	}
	stats, err := database.Stats()
	indexed, indexedErr := database.AssistantIndexingEnabled()
	if err != nil || indexedErr != nil || indexed || stats.Prompts != 1 || matchCount(t, database, "assistant") != 0 || matchCount(t, database, "retained") != 1 {
		t.Fatalf("assistant prune stats=%+v indexed=%v err=%v indexedErr=%v", stats, indexed, err, indexedErr)
	}
}

func prompt(key, nativeID, text string, line int64) history.Prompt {
	return history.Prompt{LogicalKey: key, NativeMessageID: nativeID, Role: history.RoleUser, CleanText: text, Classification: history.ClassificationHuman, Searchable: true, Confidence: history.ConfidenceExact}
}

func extraction(identityKey, nativeID string, source history.SourceReference, prompts ...history.Prompt) history.Extraction {
	value := history.Extraction{Provider: source.Provider, Source: source, Session: history.Session{IdentityKey: identityKey, NativeSessionID: nativeID, ThreadKind: history.ThreadRoot, Confidence: history.ConfidenceExact}, Prompts: prompts}
	for index, prompt := range prompts {
		value.Occurrences = append(value.Occurrences, history.Occurrence{PromptKey: prompt.LogicalKey, Variant: prompt, LineNumber: int64(index + 1), StartOffset: int64(index * 10), EndOffset: int64(index*10 + 9)})
	}
	return value
}

func matchCount(t *testing.T, database *Store, query string) int {
	t.Helper()
	var count int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM prompt_fts WHERE prompt_fts MATCH ?`, query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertPublicID(t *testing.T, value, prefix string) {
	t.Helper()
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+32 {
		t.Fatalf("public ID %q does not use %s + 128 bits", value, prefix)
	}
}
