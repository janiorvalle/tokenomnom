package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

// ApplyMode controls whether source occurrences are appended or reconciled to
// the complete current source contents.
type ApplyMode int

const (
	ApplyAppend ApplyMode = iota
	ApplyReplace
)

// ApplyResult returns persisted opaque IDs for the reconciled source.
type ApplyResult struct {
	SessionID string
	SourceID  string
	PromptIDs map[string]string
}

// SnapshotInput is one verified immutable member prepared for an atomic bundle
// commit.
type SnapshotInput struct {
	Extraction history.Extraction
	Snapshot   history.PreservedSnapshot
}

// BundleApplyResult reports whether a validated bundle changed query-visible
// history state.
type BundleApplyResult struct {
	Changed   bool
	Snapshots int
	Prompts   int
}

// SnapshotBundleWriter holds one bundle-scoped SQLite transaction so callers
// can stream verified members without retaining a monthly archive in memory.
type SnapshotBundleWriter struct {
	tx          *Tx
	sqlTx       *sql.Tx
	archive     string
	fingerprint string
	memberCount int
	attempt     time.Time
	result      BundleApplyResult
	skipped     bool
	unchanged   bool
	done        bool
}

// Stats summarizes normalized rows without exposing content.
type Stats struct {
	Sessions    int
	Sources     int
	Snapshots   int
	Locations   int
	Prompts     int
	Occurrences int
}

// ApplySource reconciles one mutable provider source head and its occurrences.
func (s *Store) ApplySource(extraction history.Extraction, head history.SourceHead, mode ApplyMode) (ApplyResult, error) {
	return s.ApplySourceWithGeneration(extraction, head, mode, true)
}

// ApplySourceWithGeneration reconciles a source and optionally advances the
// query cursor generation in the same commit.
func (s *Store) ApplySourceWithGeneration(extraction history.Extraction, head history.SourceHead, mode ApplyMode, advanceGeneration bool) (ApplyResult, error) {
	if mode != ApplyAppend && mode != ApplyReplace {
		return ApplyResult{}, fmt.Errorf("invalid history source apply mode %d", mode)
	}
	if head.Source.Provider == "" {
		head.Source.Provider = extraction.Provider
	}
	if extraction.Source.Provider == "" {
		extraction.Source.Provider = extraction.Provider
	}
	if head.Source.Provider != extraction.Provider {
		return ApplyResult{}, fmt.Errorf("history source provider %q does not match extraction provider %q", head.Source.Provider, extraction.Provider)
	}
	if normalizedSourceKind(head.Source.Kind) != normalizedSourceKind(extraction.Source.Kind) || head.Source.Path != extraction.Source.Path {
		return ApplyResult{}, fmt.Errorf("history source head %q (%s) does not match extraction source %q (%s)", head.Source.Path, normalizedSourceKind(head.Source.Kind), extraction.Source.Path, normalizedSourceKind(extraction.Source.Kind))
	}
	var result ApplyResult
	err := s.Transaction(func(tx *Tx) error {
		preferredSessionID, err := tx.sourceSessionID(extraction.Provider, head.Source.Path)
		if err != nil {
			return err
		}
		var beforeSampleMetadata storedSessionMetadata
		if preferredSessionID != 0 {
			beforeSampleMetadata, err = tx.readSessionMetadata(preferredSessionID)
			if err != nil {
				return err
			}
		}
		if err := tx.promoteClaudeSubagentSessionIdentity(extraction.Provider, extraction.Session, preferredSessionID); err != nil {
			return err
		}
		allowPromotion, err := tx.sourceAllowsFallbackPromotion(extraction.Provider, head, mode)
		if err != nil {
			return err
		}
		sessionID, sessionPublicID, err := tx.ensureSession(extraction.Provider, extraction.Session, preferredSessionID, allowPromotion)
		if err != nil {
			return err
		}
		if mode == ApplyAppend && preferredSessionID != 0 && sessionID != preferredSessionID {
			return fmt.Errorf("history append cannot change source session")
		}
		sourceID, sourcePublicID, err := tx.ensureSourceHead(sessionID, extraction.Provider, head, extraction.Session.FirstTimestamp, extraction.Session.LastTimestamp, mode)
		if err != nil {
			return err
		}
		threadChanged, err := tx.reconcileSessionThreadSupport(sessionID, extraction.Session, sourceID, 0)
		if err != nil {
			return err
		}
		reconciledRelationship, err := tx.reconcileSessionRelationships(extraction.Provider, sessionID, extraction.Relationships, sourceID, 0)
		if err != nil {
			return err
		}
		resolvedRelationship, err := tx.resolveRelationshipsForParent(sessionID)
		if err != nil {
			return err
		}
		relationshipChanged := threadChanged || reconciledRelationship || resolvedRelationship
		locationID, err := tx.ensureSourceLocation(sourceID, extraction.Provider, head.Source, head.Available)
		if err != nil {
			return err
		}
		if mode == ApplyReplace || !head.Available {
			reason := "rewrite"
			if !head.Available {
				reason = "missing"
			}
			if err := tx.tombstoneSourceOrphans(sourceID, reason); err != nil {
				return err
			}
			if err := tx.deleteSourceOccurrences(sourceID); err != nil {
				return err
			}
		}
		if !head.Available {
			if _, err := tx.tx.Exec(`DELETE FROM prompts WHERE occurrence_count=0`); err != nil {
				return fmt.Errorf("remove prompts from unavailable source: %w", err)
			}
			result = ApplyResult{SessionID: sessionPublicID, SourceID: sourcePublicID, PromptIDs: map[string]string{}}
			if err := tx.clearSourceError(extraction.Provider, head.Source.Path); err != nil {
				return err
			}
			if err := tx.finishSourceSessionReconciliation(preferredSessionID, sessionID); err != nil {
				return err
			}
			if err := tx.refreshAllSampleStrata(sessionID); err != nil {
				return err
			}
			return tx.advanceGenerationIf(advanceGeneration || relationshipChanged)
		}
		promptIDs, promptDBIDs, err := tx.ensurePrompts(sessionID, extraction.Prompts)
		if err != nil {
			return err
		}
		if err := tx.restoreSourcePromptIDs(sourceID, promptIDs, promptDBIDs); err != nil {
			return err
		}
		if err := tx.addOccurrences(locationID, sourceID, 0, promptDBIDs, extraction.Prompts, extraction.Occurrences); err != nil {
			return err
		}
		if mode == ApplyReplace {
			if err := tx.removeRestoredTombstones(sourceID); err != nil {
				return err
			}
			if _, err := tx.tx.Exec(`DELETE FROM prompts WHERE occurrence_count=0`); err != nil {
				return fmt.Errorf("remove unpreserved prompts: %w", err)
			}
		}
		result = ApplyResult{SessionID: sessionPublicID, SourceID: sourcePublicID, PromptIDs: promptIDs}
		if err := tx.clearSourceError(extraction.Provider, head.Source.Path); err != nil {
			return err
		}
		if err := tx.finishSourceSessionReconciliation(preferredSessionID, sessionID); err != nil {
			return err
		}
		afterSampleMetadata, err := tx.readSessionMetadata(sessionID)
		if err != nil {
			return err
		}
		promptDatabaseIDs := promptIDValues(promptDBIDs)
		if mode == ApplyAppend && preferredSessionID == sessionID && !sampleRelevantSessionMetadataChanged(beforeSampleMetadata, afterSampleMetadata) && !threadChanged {
			if err := tx.refreshPromptSampleStrata(promptDatabaseIDs); err != nil {
				return err
			}
		} else if err := tx.refreshAllSampleStrata(sessionID); err != nil {
			return err
		}
		return tx.advanceGenerationIf(advanceGeneration || relationshipChanged)
	})
	return result, err
}

func normalizedSourceKind(kind history.LocationKind) history.LocationKind {
	if kind == "" {
		return history.LocationProviderLive
	}
	return kind
}

func providerSourceKind(provider history.Provider, kind history.LocationKind) string {
	if provider == history.ProviderClaude {
		return "claude_project"
	}
	if normalizedSourceKind(kind) == history.LocationProviderArchive {
		return "codex_archive"
	}
	return "codex_live"
}

// PreserveSnapshot records immutable exact bytes at a durable location.
func (s *Store) PreserveSnapshot(extraction history.Extraction, snapshot history.PreservedSnapshot) (ApplyResult, error) {
	var result ApplyResult
	err := s.Transaction(func(tx *Tx) error {
		var err error
		result, err = tx.preserveSnapshot(extraction, snapshot)
		return err
	})
	return result, err
}

// ApplySnapshotBundle commits all members from one verified archive in a
// single transaction. A retry with the same manifest fingerprint is a no-op
// unless full is requested.
func (s *Store) ApplySnapshotBundle(archive, fingerprint string, inputs []SnapshotInput, full bool, attempt time.Time) (BundleApplyResult, error) {
	writer, err := s.BeginSnapshotBundle(archive, fingerprint, len(inputs), full, attempt)
	if err != nil {
		return BundleApplyResult{}, err
	}
	defer writer.Rollback()
	for _, input := range inputs {
		if err := writer.Apply(input); err != nil {
			return BundleApplyResult{}, err
		}
	}
	return writer.Commit()
}

// BeginSnapshotBundle starts one atomic streamed bundle reconciliation.
func (s *Store) BeginSnapshotBundle(archive, fingerprint string, memberCount int, full bool, attempt time.Time) (*SnapshotBundleWriter, error) {
	sqlTx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, fmt.Errorf("begin vault bundle history transaction: %w", err)
	}
	writer := &SnapshotBundleWriter{tx: &Tx{tx: sqlTx}, sqlTx: sqlTx, archive: archive, fingerprint: fingerprint, memberCount: memberCount, attempt: attempt}
	var existing string
	var lastError string
	var count, extractorVersion int
	var lastErrorInvalidates bool
	err = sqlTx.QueryRow(`SELECT manifest_fingerprint,member_count,extractor_version,last_error,last_error_invalidates
		FROM vault_bundle_state WHERE archive=?`, archive).Scan(&existing, &count, &extractorVersion, &lastError, &lastErrorInvalidates)
	if err == nil && existing == fingerprint && count == memberCount && extractorVersion == history.ExtractorVersion {
		if lastError != "" && !lastErrorInvalidates {
			writer.unchanged = true
		} else if lastError == "" && full {
			writer.unchanged = true
		} else if lastError == "" {
			writer.skipped = true
		}
		if writer.unchanged || writer.skipped {
			return writer, nil
		}
	}
	if err != nil && err != sql.ErrNoRows {
		_ = sqlTx.Rollback()
		return nil, fmt.Errorf("read vault bundle checkpoint: %w", err)
	}
	return writer, nil
}

// VaultBundleCurrent reports whether an archive's immutable manifest and
// extractor already have a successful, non-error checkpoint.
func (s *Store) VaultBundleCurrent(archive, fingerprint string, memberCount int) (bool, error) {
	var existing string
	var count, extractorVersion int
	err := s.runner.QueryRow(`SELECT manifest_fingerprint,member_count,extractor_version
		FROM vault_bundle_state WHERE archive=? AND last_error=''`, archive).Scan(&existing, &count, &extractorVersion)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read vault bundle checkpoint: %w", err)
	}
	return existing == fingerprint && count == memberCount && extractorVersion == history.ExtractorVersion, nil
}

// Apply reconciles one verified member into the open bundle transaction.
func (w *SnapshotBundleWriter) Apply(input SnapshotInput) error {
	if w.done {
		return errors.New("vault bundle transaction is closed")
	}
	if w.skipped {
		return nil
	}
	applied, err := w.tx.preserveSnapshot(input.Extraction, input.Snapshot)
	if err != nil {
		return err
	}
	w.result.Snapshots++
	w.result.Prompts += len(applied.PromptIDs)
	return nil
}

// Commit publishes the complete verified bundle and advances generation once.
func (w *SnapshotBundleWriter) Commit() (BundleApplyResult, error) {
	if w.done {
		return BundleApplyResult{}, errors.New("vault bundle transaction is closed")
	}
	w.done = true
	if w.skipped {
		if _, err := w.sqlTx.Exec(`UPDATE vault_bundle_state SET last_attempt_unix=? WHERE archive=?`, w.attempt.Unix(), w.archive); err != nil {
			_ = w.sqlTx.Rollback()
			return BundleApplyResult{}, err
		}
		if err := w.sqlTx.Commit(); err != nil {
			return BundleApplyResult{}, fmt.Errorf("commit unchanged vault bundle history: %w", err)
		}
		return w.result, nil
	}
	if _, err := w.sqlTx.Exec(`INSERT INTO vault_bundle_state(archive,manifest_fingerprint,member_count,extractor_version,last_attempt_unix,last_success_unix,last_error,last_error_invalidates)
		VALUES(?,?,?,?,?,?, '',0) ON CONFLICT(archive) DO UPDATE SET manifest_fingerprint=excluded.manifest_fingerprint,
		member_count=excluded.member_count,extractor_version=excluded.extractor_version,last_attempt_unix=excluded.last_attempt_unix,
		last_success_unix=excluded.last_success_unix,last_error='',last_error_invalidates=0`, w.archive, w.fingerprint, w.memberCount, history.ExtractorVersion, w.attempt.Unix(), w.attempt.Unix()); err != nil {
		_ = w.sqlTx.Rollback()
		return BundleApplyResult{}, fmt.Errorf("record vault bundle checkpoint: %w", err)
	}
	if err := w.tx.advanceGenerationIf(!w.unchanged); err != nil {
		_ = w.sqlTx.Rollback()
		return BundleApplyResult{}, err
	}
	if err := w.sqlTx.Commit(); err != nil {
		return BundleApplyResult{}, fmt.Errorf("commit vault bundle history: %w", err)
	}
	w.result.Changed = !w.unchanged
	return w.result, nil
}

// Rollback abandons an incomplete bundle. It is safe after Commit.
func (w *SnapshotBundleWriter) Rollback() {
	if w == nil || w.done {
		return
	}
	w.done = true
	_ = w.sqlTx.Rollback()
}

// RecordVaultBundleError retains a bounded non-content failure without
// changing the last successful manifest checkpoint.
func (s *Store) RecordVaultBundleError(archive string, attempt time.Time, bundleErr error) error {
	message := bundleErr.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	return s.Transaction(func(tx *Tx) error {
		_, err := tx.tx.Exec(`INSERT INTO vault_bundle_state(archive,last_attempt_unix,last_error,last_error_invalidates) VALUES(?,?,?,1)
			ON CONFLICT(archive) DO UPDATE SET last_attempt_unix=excluded.last_attempt_unix,last_error=excluded.last_error,last_error_invalidates=1`, archive, attempt.Unix(), message)
		if err != nil {
			return fmt.Errorf("record vault bundle error: %w", err)
		}
		result, err := tx.tx.Exec(`UPDATE locations SET available=0 WHERE kind='vault' AND archive=? AND available=1`, archive)
		if err != nil {
			return fmt.Errorf("mark failed vault bundle locations unavailable: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("count failed vault bundle locations: %w", err)
		}
		if changed == 0 {
			return nil
		}
		rows, err := tx.tx.Query(`SELECT DISTINCT ps.session_id FROM preserved_snapshots ps
			JOIN locations l ON l.snapshot_id=ps.id WHERE l.kind='vault' AND l.archive=?`, archive)
		if err != nil {
			return fmt.Errorf("list sessions from failed vault bundle: %w", err)
		}
		var sessionIDs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("scan session from failed vault bundle: %w", err)
			}
			sessionIDs = append(sessionIDs, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`INSERT INTO vault_prompt_tombstones(archive,provider,session_public_id,logical_key,prompt_public_id,deleted_at)
			SELECT ?,s.provider,s.public_id,p.logical_key,p.public_id,? FROM prompts p JOIN sessions s ON s.id=p.session_id
			WHERE EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND l.kind='vault' AND l.archive=?)
			AND NOT EXISTS(SELECT 1 FROM occurrences o JOIN locations l ON l.id=o.location_id WHERE o.prompt_id=p.id AND NOT (l.kind='vault' AND l.archive=?))
			ON CONFLICT(archive,provider,session_public_id,logical_key) DO UPDATE SET prompt_public_id=excluded.prompt_public_id,deleted_at=excluded.deleted_at`,
			archive, attempt.Unix(), archive, archive); err != nil {
			return fmt.Errorf("tombstone prompts from failed vault bundle: %w", err)
		}
		rows, err = tx.tx.Query(`SELECT DISTINCT prompt_id FROM occurrences WHERE location_id IN (SELECT id FROM locations WHERE kind='vault' AND archive=?)`, archive)
		if err != nil {
			return fmt.Errorf("list prompts from failed vault bundle: %w", err)
		}
		var promptIDs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("scan prompt from failed vault bundle: %w", err)
			}
			promptIDs = append(promptIDs, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`DELETE FROM occurrences WHERE location_id IN (SELECT id FROM locations WHERE kind='vault' AND archive=?)`, archive); err != nil {
			return fmt.Errorf("remove occurrences from failed vault bundle: %w", err)
		}
		for _, promptID := range promptIDs {
			if err := tx.refreshPromptCanonical(promptID); err != nil {
				return err
			}
		}
		if _, err := tx.tx.Exec(`DELETE FROM prompts WHERE occurrence_count=0`); err != nil {
			return fmt.Errorf("remove orphaned prompts from failed vault bundle: %w", err)
		}
		for _, sessionID := range sessionIDs {
			if err := tx.recomputeSessionBounds(sessionID); err != nil {
				return err
			}
			if err := tx.refreshAllSampleStrata(sessionID); err != nil {
				return err
			}
		}
		return tx.advanceGenerationIf(true)
	})
}

// RecordVaultBundleIndexError records a retryable consumer failure without
// revoking byte-verified vault locations from a previous successful index.
func (s *Store) RecordVaultBundleIndexError(archive string, attempt time.Time, bundleErr error) error {
	message := bundleErr.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	return s.Transaction(func(tx *Tx) error {
		if _, err := tx.tx.Exec(`INSERT INTO vault_bundle_state(archive,last_attempt_unix,last_error,last_error_invalidates) VALUES(?,?,?,0)
			ON CONFLICT(archive) DO UPDATE SET last_attempt_unix=excluded.last_attempt_unix,last_error=excluded.last_error,
			last_error_invalidates=vault_bundle_state.last_error_invalidates`, archive, attempt.Unix(), message); err != nil {
			return fmt.Errorf("record vault bundle index error: %w", err)
		}
		return nil
	})
}

func (tx *Tx) ensureSnapshotSession(provider history.Provider, value history.Session, preferredID int64) (int64, string, error) {
	if preferredID == 0 {
		return tx.ensureSession(provider, value, 0, true)
	}
	var preferredPublicID, preferredIdentity string
	if err := tx.tx.QueryRow(`SELECT public_id,identity_key FROM sessions WHERE id=? AND provider=?`, preferredID, provider).Scan(&preferredPublicID, &preferredIdentity); err != nil {
		return 0, "", fmt.Errorf("read preserved snapshot session: %w", err)
	}
	if strings.HasPrefix(preferredIdentity, "fallback:source-path:") && strings.HasPrefix(value.IdentityKey, "fallback:source-path:") {
		var candidateID int64
		err := tx.tx.QueryRow(`SELECT id FROM sessions WHERE provider=? AND identity_key=?`, provider, value.IdentityKey).Scan(&candidateID)
		if err != nil && err != sql.ErrNoRows {
			return 0, "", fmt.Errorf("find path-fallback snapshot session: %w", err)
		}
		if err == nil && candidateID != preferredID {
			if err := tx.mergeSessions(preferredID, candidateID); err != nil {
				return 0, "", err
			}
		}
		existing, err := tx.readSessionMetadata(preferredID)
		if err != nil {
			return 0, "", err
		}
		if err := tx.writeSessionMetadata(preferredID, mergeStoredSessionMetadata(existing, sessionMetadata(value))); err != nil {
			return 0, "", err
		}
		return preferredID, preferredPublicID, nil
	}
	return tx.ensureSession(provider, value, preferredID, true)
}

func (tx *Tx) promoteClaudeSubagentSessionIdentity(provider history.Provider, value history.Session, preferredID int64) error {
	if provider != history.ProviderClaude || value.ThreadKind != history.ThreadSubagent || preferredID == 0 || value.IdentityKey == "" {
		return nil
	}
	var currentIdentity string
	if err := tx.tx.QueryRow(`SELECT identity_key FROM sessions WHERE id=?`, preferredID).Scan(&currentIdentity); err != nil {
		return fmt.Errorf("read Claude subagent session identity: %w", err)
	}
	if currentIdentity == value.IdentityKey {
		return nil
	}
	var targetExists bool
	if err := tx.tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sessions WHERE provider=? AND identity_key=? AND id<>?)`,
		provider, value.IdentityKey, preferredID).Scan(&targetExists); err != nil {
		return fmt.Errorf("find corrected Claude subagent session identity: %w", err)
	}
	if targetExists {
		return nil
	}
	var nonSubagentSupports int
	if err := tx.tx.QueryRow(`SELECT
		(SELECT COUNT(*) FROM source_heads WHERE session_id=? AND replace(source_path,'\','/') NOT LIKE '%/subagents/%')+
		(SELECT COUNT(*) FROM preserved_snapshots ps WHERE ps.session_id=? AND NOT EXISTS(
			SELECT 1 FROM locations l WHERE l.snapshot_id=ps.id AND replace(
				CASE WHEN l.relative_path<>'' THEN l.relative_path ELSE l.source_path END,'\','/') LIKE '%/subagents/%'))`,
		preferredID, preferredID).Scan(&nonSubagentSupports); err != nil {
		return fmt.Errorf("classify existing Claude subagent session supports: %w", err)
	}
	if nonSubagentSupports != 0 {
		return nil
	}
	if _, err := tx.tx.Exec(`UPDATE sessions SET identity_key=?,native_session_id=?,fallback_key=? WHERE id=?`,
		value.IdentityKey, nullText(value.NativeSessionID), value.FallbackKey, preferredID); err != nil {
		return fmt.Errorf("promote corrected Claude subagent session identity: %w", err)
	}
	return nil
}

func (tx *Tx) preserveSnapshot(extraction history.Extraction, snapshot history.PreservedSnapshot) (ApplyResult, error) {
	if snapshot.Provider == "" {
		snapshot.Provider = extraction.Provider
	}
	if extraction.Source.Provider == "" {
		extraction.Source.Provider = extraction.Provider
	}
	if snapshot.Provider != extraction.Provider || extraction.Source.Provider != extraction.Provider {
		return ApplyResult{}, fmt.Errorf("history snapshot and source providers must match extraction provider %q", extraction.Provider)
	}
	if snapshot.FirstTS == nil {
		snapshot.FirstTS = extraction.Session.FirstTimestamp
	}
	if snapshot.LastTS == nil {
		snapshot.LastTS = extraction.Session.LastTimestamp
	}
	preferredSessionID, err := tx.snapshotSessionID(snapshot.Provider, snapshot.ContentSHA256)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := tx.promoteClaudeSubagentSessionIdentity(extraction.Provider, extraction.Session, preferredSessionID); err != nil {
		return ApplyResult{}, err
	}
	sessionID, sessionPublicID, err := tx.ensureSnapshotSession(extraction.Provider, extraction.Session, preferredSessionID)
	if err != nil {
		return ApplyResult{}, err
	}
	snapshotID, snapshotPublicID, err := tx.ensureSnapshot(sessionID, snapshot)
	if err != nil {
		return ApplyResult{}, err
	}
	if _, err := tx.reconcileSessionThreadSupport(sessionID, extraction.Session, 0, snapshotID); err != nil {
		return ApplyResult{}, err
	}
	if _, err := tx.reconcileSessionRelationships(extraction.Provider, sessionID, extraction.Relationships, 0, snapshotID); err != nil {
		return ApplyResult{}, err
	}
	if _, err := tx.resolveRelationshipsForParent(sessionID); err != nil {
		return ApplyResult{}, err
	}
	_, err = tx.ensureSnapshotLocation(snapshotID, extraction.Source)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := tx.deleteSnapshotOccurrences(snapshotID); err != nil {
		return ApplyResult{}, err
	}
	promptIDs, promptDBIDs, err := tx.ensurePrompts(sessionID, extraction.Prompts)
	if err != nil {
		return ApplyResult{}, err
	}
	if extraction.Source.Kind == history.LocationVault {
		if err := tx.restoreVaultPromptIDs(extraction.Provider, sessionID, sessionPublicID, promptIDs, promptDBIDs); err != nil {
			return ApplyResult{}, err
		}
	}
	locationIDs, err := tx.snapshotLocationIDs(snapshotID)
	if err != nil {
		return ApplyResult{}, err
	}
	for _, currentLocationID := range locationIDs {
		if err := tx.addOccurrences(currentLocationID, 0, snapshotID, promptDBIDs, extraction.Prompts, extraction.Occurrences); err != nil {
			return ApplyResult{}, err
		}
	}
	if _, err := tx.tx.Exec(`DELETE FROM prompts WHERE occurrence_count=0`); err != nil {
		return ApplyResult{}, fmt.Errorf("remove unpreserved prompts: %w", err)
	}
	if err := tx.recomputeSessionBounds(sessionID); err != nil {
		return ApplyResult{}, err
	}
	if err := tx.finishSourceSessionReconciliation(preferredSessionID, sessionID); err != nil {
		return ApplyResult{}, err
	}
	if err := tx.refreshAllSampleStrata(sessionID); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{SessionID: sessionPublicID, SourceID: snapshotPublicID, PromptIDs: promptIDs}, nil
}

func (tx *Tx) restoreVaultPromptIDs(provider history.Provider, sessionID int64, sessionPublicID string, publicIDs map[string]string, databaseIDs map[string]int64) error {
	rows, err := tx.tx.Query(`SELECT logical_key,prompt_public_id FROM (
		SELECT logical_key,prompt_public_id,deleted_at,0 AS source_order FROM vault_prompt_tombstones
			WHERE provider=? AND session_public_id=?
		UNION ALL
		SELECT pt.logical_key,pt.prompt_public_id,pt.deleted_at,1 AS source_order FROM prompt_tombstones pt
			JOIN source_heads sh ON sh.id=pt.source_head_id WHERE sh.provider=? AND sh.session_id=?
	) ORDER BY logical_key,deleted_at,source_order,prompt_public_id`, provider, sessionPublicID, provider, sessionID)
	if err != nil {
		return fmt.Errorf("list prompt tombstones for vault restore: %w", err)
	}
	tombstones := make(map[string][]string)
	for rows.Next() {
		var logicalKey, publicID string
		if err := rows.Scan(&logicalKey, &publicID); err != nil {
			rows.Close()
			return fmt.Errorf("scan vault prompt tombstone: %w", err)
		}
		ids := tombstones[logicalKey]
		if len(ids) == 0 || ids[len(ids)-1] != publicID {
			tombstones[logicalKey] = append(ids, publicID)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for logicalKey, candidates := range tombstones {
		databaseID, found := databaseIDs[logicalKey]
		if !found {
			continue
		}
		restoredID := candidates[0]
		for _, retiredID := range candidates[1:] {
			if retiredID != restoredID {
				if err := tx.recordPublicIDAlias(retiredID, restoredID, "prompt"); err != nil {
					return err
				}
			}
		}
		if currentID := publicIDs[logicalKey]; currentID != restoredID {
			if err := tx.recordPublicIDAlias(currentID, restoredID, "prompt"); err != nil {
				return err
			}
			if _, err := tx.tx.Exec(`UPDATE prompts SET public_id=?,sample_key=? WHERE id=?`, restoredID, sampleKey(restoredID), databaseID); err != nil {
				return fmt.Errorf("restore vault prompt ID: %w", err)
			}
			publicIDs[logicalKey] = restoredID
		}
		if _, err := tx.tx.Exec(`DELETE FROM vault_prompt_tombstones WHERE provider=? AND session_public_id=? AND logical_key=?`, provider, sessionPublicID, logicalKey); err != nil {
			return fmt.Errorf("remove restored vault prompt tombstone: %w", err)
		}
		if _, err := tx.tx.Exec(`DELETE FROM prompt_tombstones WHERE logical_key=? AND source_head_id IN (
			SELECT id FROM source_heads WHERE provider=? AND session_id=?
		)`, logicalKey, provider, sessionID); err != nil {
			return fmt.Errorf("remove restored provider prompt tombstone: %w", err)
		}
	}
	return nil
}

func (tx *Tx) restoreSourcePromptIDs(sourceID int64, publicIDs map[string]string, databaseIDs map[string]int64) error {
	rows, err := tx.tx.Query(`SELECT logical_key,prompt_public_id FROM prompt_tombstones
		WHERE source_head_id=? ORDER BY deleted_at,prompt_public_id`, sourceID)
	if err != nil {
		return fmt.Errorf("list source prompt tombstones: %w", err)
	}
	tombstones := make(map[string]string)
	for rows.Next() {
		var logicalKey, publicID string
		if err := rows.Scan(&logicalKey, &publicID); err != nil {
			return fmt.Errorf("scan source prompt tombstone: %w", err)
		}
		if _, found := tombstones[logicalKey]; !found {
			tombstones[logicalKey] = publicID
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for logicalKey, restoredID := range tombstones {
		databaseID, found := databaseIDs[logicalKey]
		if !found {
			continue
		}
		if currentID := publicIDs[logicalKey]; currentID != restoredID {
			if err := tx.recordPublicIDAlias(currentID, restoredID, "prompt"); err != nil {
				return err
			}
			if _, err := tx.tx.Exec(`UPDATE prompts SET public_id=?,sample_key=? WHERE id=?`, restoredID, sampleKey(restoredID), databaseID); err != nil {
				return fmt.Errorf("restore source prompt ID: %w", err)
			}
			publicIDs[logicalKey] = restoredID
		}
		if _, err := tx.tx.Exec(`DELETE FROM prompt_tombstones WHERE source_head_id=? AND logical_key=?`, sourceID, logicalKey); err != nil {
			return fmt.Errorf("remove restored source prompt tombstone: %w", err)
		}
	}
	return nil
}

// RelocateSource moves a source head while preserving its opaque ID.
func (s *Store) RelocateSource(provider history.Provider, oldPath string, source history.SourceReference) error {
	return s.Transaction(func(tx *Tx) error {
		var sourceID int64
		if err := tx.tx.QueryRow(`SELECT id FROM source_heads WHERE provider=? AND source_path=?`, provider, oldPath).Scan(&sourceID); err != nil {
			return fmt.Errorf("find source to relocate: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE source_heads SET source_path=?,source_kind=?,available=1 WHERE id=?`, source.Path, providerSourceKind(provider, source.Kind), sourceID); err != nil {
			return fmt.Errorf("relocate source head: %w", err)
		}
		if _, err := tx.tx.Exec(`UPDATE locations SET available=0 WHERE source_head_id=?`, sourceID); err != nil {
			return fmt.Errorf("retire source location: %w", err)
		}
		locationID, err := tx.ensureSourceLocation(sourceID, provider, source, true)
		if err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`UPDATE occurrences SET location_id=? WHERE source_head_id=?`, locationID, sourceID); err != nil {
			return fmt.Errorf("remap relocated source occurrences: %w", err)
		}
		if _, err := tx.tx.Exec(`DELETE FROM locations WHERE source_head_id=? AND id<>? AND available=0 AND NOT EXISTS (SELECT 1 FROM occurrences WHERE occurrences.location_id=locations.id)`, sourceID, locationID); err != nil {
			return fmt.Errorf("remove retired source locations: %w", err)
		}
		var sessionID int64
		if err := tx.tx.QueryRow(`SELECT session_id FROM source_heads WHERE id=?`, sourceID).Scan(&sessionID); err != nil {
			return err
		}
		if err := tx.refreshAllSampleStrata(sessionID); err != nil {
			return err
		}
		return tx.advanceGenerationIf(true)
	})
}

func (tx *Tx) sourceSessionID(provider history.Provider, path string) (int64, error) {
	var id int64
	err := tx.tx.QueryRow(`SELECT session_id FROM source_heads WHERE provider=? AND source_path=?`, provider, path).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find source session: %w", err)
	}
	return id, nil
}

func (tx *Tx) sourceAllowsFallbackPromotion(provider history.Provider, head history.SourceHead, mode ApplyMode) (bool, error) {
	if mode == ApplyAppend || head.VerifiedContinuity {
		return true, nil
	}
	var existingHash string
	var existingSize int64
	err := tx.tx.QueryRow(`SELECT current_sha256,size FROM source_heads WHERE provider=? AND source_path=?`, provider, head.Source.Path).Scan(&existingHash, &existingSize)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read history source continuity: %w", err)
	}
	return existingSize == 0 || (existingHash != "" && existingHash == head.ContentSHA256), nil
}

func (tx *Tx) snapshotSessionID(provider history.Provider, hash string) (int64, error) {
	var id int64
	err := tx.tx.QueryRow(`SELECT session_id FROM preserved_snapshots WHERE provider=? AND content_sha256=?`, provider, hash).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find snapshot session: %w", err)
	}
	return id, nil
}

func (tx *Tx) snapshotLocationIDs(snapshotID int64) ([]int64, error) {
	rows, err := tx.tx.Query(`SELECT id FROM locations WHERE snapshot_id=? AND available=1 ORDER BY id`, snapshotID)
	if err != nil {
		return nil, fmt.Errorf("list snapshot locations: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan snapshot location: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (tx *Tx) recomputeSessionBounds(sessionID int64) error {
	rows, err := tx.tx.Query(`SELECT first_ts,last_ts FROM source_heads WHERE session_id=? AND available=1
		UNION ALL SELECT ps.first_ts,ps.last_ts FROM preserved_snapshots ps WHERE ps.session_id=?
			AND EXISTS(SELECT 1 FROM locations l WHERE l.snapshot_id=ps.id AND l.available=1)`, sessionID, sessionID)
	if err != nil {
		return fmt.Errorf("list history session timestamp bounds: %w", err)
	}
	var firstValues, lastValues []string
	for rows.Next() {
		var first, last sql.NullString
		if err := rows.Scan(&first, &last); err != nil {
			rows.Close()
			return fmt.Errorf("scan history session timestamp bounds: %w", err)
		}
		if first.Valid {
			firstValues = append(firstValues, first.String)
		}
		if last.Valid {
			lastValues = append(lastValues, last.String)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`UPDATE sessions SET first_ts=?,last_ts=? WHERE id=?`, nullableTimestamp(earliestTimestamp(firstValues...)), nullableTimestamp(latestTimestamp(lastValues...)), sessionID); err != nil {
		return fmt.Errorf("recompute history session timestamp bounds: %w", err)
	}
	return tx.refreshSessionSampleStratum(sessionID)
}

func sampleRelevantSessionMetadataChanged(before, after storedSessionMetadata) bool {
	return before.cwd != after.cwd || before.repositoryName != after.repositoryName || before.branch != after.branch || before.threadKind != after.threadKind
}

func promptIDValues(values map[string]int64) []int64 {
	result := make([]int64, 0, len(values))
	seen := make(map[int64]bool, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

func (tx *Tx) finishSourceSessionReconciliation(previousSessionID, sessionID int64) error {
	if err := tx.recomputeSessionBounds(sessionID); err != nil {
		return err
	}
	if previousSessionID == 0 || previousSessionID == sessionID {
		return nil
	}
	if err := tx.recomputeSessionBounds(previousSessionID); err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`DELETE FROM sessions WHERE id=?
		AND NOT EXISTS (SELECT 1 FROM source_heads WHERE session_id=sessions.id)
		AND NOT EXISTS (SELECT 1 FROM preserved_snapshots WHERE session_id=sessions.id)
		AND NOT EXISTS (SELECT 1 FROM prompts WHERE session_id=sessions.id)`, previousSessionID); err != nil {
		return fmt.Errorf("remove orphaned history session: %w", err)
	}
	return nil
}

func (tx *Tx) promoteFallbackSession(provider history.Provider, value history.Session, preferredID int64, allowed bool) error {
	if preferredID == 0 || !allowed {
		return nil
	}
	var currentIdentity string
	if err := tx.tx.QueryRow(`SELECT identity_key FROM sessions WHERE id=? AND provider=?`, preferredID, provider).Scan(&currentIdentity); err != nil {
		return fmt.Errorf("read preferred history session: %w", err)
	}
	isNativePromotion := strings.HasPrefix(value.IdentityKey, "native:") && strings.HasPrefix(currentIdentity, "fallback:")
	isFirstRecordPromotion := strings.HasPrefix(value.IdentityKey, "fallback:first-record:") && strings.HasPrefix(currentIdentity, "fallback:source-path:")
	if !isNativePromotion && !isFirstRecordPromotion {
		return nil
	}

	var targetID int64
	err := tx.tx.QueryRow(`SELECT id FROM sessions WHERE provider=? AND identity_key=?`, provider, value.IdentityKey).Scan(&targetID)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("find native history session: %w", err)
	}
	if err == nil && targetID != preferredID {
		if err := tx.mergeSessions(preferredID, targetID); err != nil {
			return err
		}
	}
	nativeSessionID := value.NativeSessionID
	if nativeSessionID == "" && isNativePromotion {
		nativeSessionID = strings.TrimPrefix(value.IdentityKey, "native:")
	}
	if _, err := tx.tx.Exec(`UPDATE sessions SET identity_key=?,native_session_id=?,fallback_key=? WHERE id=?`, value.IdentityKey, nullText(nativeSessionID), value.FallbackKey, preferredID); err != nil {
		return fmt.Errorf("promote fallback history session: %w", err)
	}
	return nil
}

func (tx *Tx) mergeSessions(recipientID, donorID int64) error {
	var recipientPublicID, donorPublicID string
	if err := tx.tx.QueryRow(`SELECT public_id FROM sessions WHERE id=?`, recipientID).Scan(&recipientPublicID); err != nil {
		return fmt.Errorf("read recipient history session ID: %w", err)
	}
	if err := tx.tx.QueryRow(`SELECT public_id FROM sessions WHERE id=?`, donorID).Scan(&donorPublicID); err != nil {
		return fmt.Errorf("read donor history session ID: %w", err)
	}
	if err := tx.mergeSessionMetadata(recipientID, donorID); err != nil {
		return err
	}
	rows, err := tx.tx.Query(`SELECT id,logical_key,public_id FROM prompts WHERE session_id=? ORDER BY id`, donorID)
	if err != nil {
		return fmt.Errorf("list donor history prompts: %w", err)
	}
	type donorPrompt struct {
		id       int64
		key      string
		publicID string
	}
	var donors []donorPrompt
	for rows.Next() {
		var prompt donorPrompt
		if err := rows.Scan(&prompt.id, &prompt.key, &prompt.publicID); err != nil {
			rows.Close()
			return fmt.Errorf("scan donor history prompt: %w", err)
		}
		donors = append(donors, prompt)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, donor := range donors {
		var recipientPromptID int64
		var recipientPromptPublicID string
		err := tx.tx.QueryRow(`SELECT id,public_id FROM prompts WHERE session_id=? AND logical_key=?`, recipientID, donor.key).Scan(&recipientPromptID, &recipientPromptPublicID)
		if err == sql.ErrNoRows {
			if _, err := tx.tx.Exec(`UPDATE prompts SET session_id=? WHERE id=?`, recipientID, donor.id); err != nil {
				return fmt.Errorf("move donor history prompt: %w", err)
			}
			continue
		}
		if err != nil {
			return fmt.Errorf("find matching history prompt: %w", err)
		}
		if _, err := tx.tx.Exec(`INSERT INTO occurrences(prompt_id,location_id,source_head_id,snapshot_id,native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,timestamp_unix_nano,model,evidence,confidence,extractor_version,line_number,start_offset,end_offset)
			SELECT ?,location_id,source_head_id,snapshot_id,native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,timestamp_unix_nano,model,evidence,confidence,extractor_version,line_number,start_offset,end_offset FROM occurrences WHERE prompt_id=?
			ON CONFLICT DO NOTHING`, recipientPromptID, donor.id); err != nil {
			return fmt.Errorf("merge history prompt occurrences: %w", err)
		}
		if err := tx.refreshPromptCanonical(recipientPromptID); err != nil {
			return err
		}
		if _, err := tx.tx.Exec(`DELETE FROM prompts WHERE id=?`, donor.id); err != nil {
			return fmt.Errorf("remove merged history prompt: %w", err)
		}
		if err := tx.recordPublicIDAlias(donor.publicID, recipientPromptPublicID, "prompt"); err != nil {
			return err
		}
	}
	for _, table := range []string{"source_heads", "preserved_snapshots"} {
		if _, err := tx.tx.Exec(`UPDATE `+table+` SET session_id=? WHERE session_id=?`, recipientID, donorID); err != nil {
			return fmt.Errorf("move donor %s: %w", table, err)
		}
	}
	if _, err := tx.tx.Exec(`UPDATE session_thread_supports SET session_id=? WHERE session_id=?`, recipientID, donorID); err != nil {
		return fmt.Errorf("move history thread supports: %w", err)
	}
	if _, err := tx.refreshCanonicalSessionThreadSupport(recipientID); err != nil {
		return err
	}
	if err := tx.rehomeSessionRelationships(recipientID, donorID); err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`DELETE FROM sessions WHERE id=?`, donorID); err != nil {
		return fmt.Errorf("remove merged history session: %w", err)
	}
	if err := tx.recordPublicIDAlias(donorPublicID, recipientPublicID, "session"); err != nil {
		return err
	}
	return nil
}

func (tx *Tx) recordPublicIDAlias(alias, canonical, kind string) error {
	if _, err := tx.tx.Exec(`UPDATE public_id_aliases SET canonical_public_id=? WHERE canonical_public_id=? AND entity_kind=?`, canonical, alias, kind); err != nil {
		return fmt.Errorf("flatten %s public ID aliases: %w", kind, err)
	}
	if _, err := tx.tx.Exec(`INSERT INTO public_id_aliases(alias_public_id,canonical_public_id,entity_kind) VALUES(?,?,?) ON CONFLICT(alias_public_id) DO UPDATE SET canonical_public_id=excluded.canonical_public_id,entity_kind=excluded.entity_kind`, alias, canonical, kind); err != nil {
		return fmt.Errorf("record %s public ID alias: %w", kind, err)
	}
	return nil
}

type storedSessionMetadata struct {
	nativeSessionID, cwd, repositoryRoot, repositoryName, repositoryIdentity sql.NullString
	branch, threadEvidence, parentNativeSessionID, forkedFromSessionID       sql.NullString
	forkedFromMessageID, originator, evidence                                sql.NullString
	fallbackKey, threadKind, threadConfidence, confidence, firstTS, lastTS   string
	repositoryRuleVersion, threadRuleVersion                                 int
}

func (tx *Tx) mergeSessionMetadata(recipientID, donorID int64) error {
	recipient, err := tx.readSessionMetadata(recipientID)
	if err != nil {
		return fmt.Errorf("read recipient history session metadata: %w", err)
	}
	donor, err := tx.readSessionMetadata(donorID)
	if err != nil {
		return fmt.Errorf("read donor history session metadata: %w", err)
	}
	return tx.writeSessionMetadata(recipientID, mergeStoredSessionMetadata(recipient, donor))
}

func (tx *Tx) readSessionMetadata(id int64) (storedSessionMetadata, error) {
	var value storedSessionMetadata
	err := tx.tx.QueryRow(`SELECT native_session_id,fallback_key,cwd,repository_root,repository_name,repository_identity,repository_rule_version,branch,
		thread_kind,thread_evidence,thread_confidence,thread_rule_version,parent_native_session_id,forked_from_session_id,
		forked_from_message_id,originator,evidence,confidence,COALESCE(first_ts,''),COALESCE(last_ts,'') FROM sessions WHERE id=?`, id).Scan(
		&value.nativeSessionID, &value.fallbackKey, &value.cwd, &value.repositoryRoot, &value.repositoryName,
		&value.repositoryIdentity, &value.repositoryRuleVersion, &value.branch, &value.threadKind, &value.threadEvidence, &value.threadConfidence,
		&value.threadRuleVersion, &value.parentNativeSessionID, &value.forkedFromSessionID, &value.forkedFromMessageID,
		&value.originator, &value.evidence, &value.confidence, &value.firstTS, &value.lastTS)
	return value, err
}

func (tx *Tx) writeSessionMetadata(id int64, value storedSessionMetadata) error {
	_, err := tx.tx.Exec(`UPDATE sessions SET native_session_id=?,fallback_key=?,cwd=?,repository_root=?,repository_name=?,repository_identity=?,repository_rule_version=?,branch=?,
		thread_kind=?,thread_evidence=?,thread_confidence=?,thread_rule_version=?,parent_native_session_id=?,forked_from_session_id=?,forked_from_message_id=?,
		originator=?,evidence=?,confidence=?,first_ts=?,last_ts=? WHERE id=?`,
		nullStringValue(value.nativeSessionID), value.fallbackKey, nullStringValue(value.cwd), nullStringValue(value.repositoryRoot),
		nullStringValue(value.repositoryName), nullStringValue(value.repositoryIdentity), value.repositoryRuleVersion, nullStringValue(value.branch), value.threadKind,
		value.threadEvidence.String, value.threadConfidence, value.threadRuleVersion, nullStringValue(value.parentNativeSessionID),
		nullStringValue(value.forkedFromSessionID), nullStringValue(value.forkedFromMessageID), nullStringValue(value.originator), nullStringValue(value.evidence), value.confidence,
		nullableTimestamp(value.firstTS), nullableTimestamp(value.lastTS), id)
	if err != nil {
		return fmt.Errorf("write deterministic history session metadata: %w", err)
	}
	return nil
}

func mergeStoredSessionMetadata(existing, candidate storedSessionMetadata) storedSessionMetadata {
	candidateWins := storedSessionMetadataWins(candidate, existing)
	choose := func(current, next sql.NullString) sql.NullString {
		if !current.Valid {
			return next
		}
		if next.Valid && candidateWins {
			return next
		}
		return current
	}
	existing.nativeSessionID = choose(existing.nativeSessionID, candidate.nativeSessionID)
	existing.cwd = choose(existing.cwd, candidate.cwd)
	existing.repositoryRoot = choose(existing.repositoryRoot, candidate.repositoryRoot)
	existing.repositoryName = choose(existing.repositoryName, candidate.repositoryName)
	existing.repositoryIdentity = choose(existing.repositoryIdentity, candidate.repositoryIdentity)
	if candidate.repositoryRuleVersion > existing.repositoryRuleVersion && candidate.repositoryIdentity.Valid && candidate.repositoryName.Valid {
		existing.repositoryRuleVersion = candidate.repositoryRuleVersion
	}
	existing.branch = choose(existing.branch, candidate.branch)
	existing.parentNativeSessionID = choose(existing.parentNativeSessionID, candidate.parentNativeSessionID)
	existing.forkedFromSessionID = choose(existing.forkedFromSessionID, candidate.forkedFromSessionID)
	existing.forkedFromMessageID = choose(existing.forkedFromMessageID, candidate.forkedFromMessageID)
	existing.originator = choose(existing.originator, candidate.originator)
	existing.evidence = choose(existing.evidence, candidate.evidence)
	if existing.fallbackKey == "" || (candidate.fallbackKey != "" && candidateWins) {
		existing.fallbackKey = candidate.fallbackKey
	}
	threadWins := candidate.threadKind != string(history.ThreadUnknown) && (existing.threadKind == string(history.ThreadUnknown) ||
		confidenceRank(candidate.threadConfidence) > confidenceRank(existing.threadConfidence) ||
		(confidenceRank(candidate.threadConfidence) == confidenceRank(existing.threadConfidence) &&
			(threadKindRank(candidate.threadKind) > threadKindRank(existing.threadKind) ||
				(threadKindRank(candidate.threadKind) == threadKindRank(existing.threadKind) && candidateWins))))
	if threadWins {
		existing.threadKind = candidate.threadKind
		existing.threadEvidence = candidate.threadEvidence
		existing.threadConfidence = candidate.threadConfidence
		existing.threadRuleVersion = candidate.threadRuleVersion
	}
	if confidenceRank(candidate.confidence) > confidenceRank(existing.confidence) || (candidate.confidence == existing.confidence && candidateWins) {
		existing.confidence = candidate.confidence
	}
	existing.firstTS = earliestTimestamp(existing.firstTS, candidate.firstTS)
	existing.lastTS = latestTimestamp(existing.lastTS, candidate.lastTS)
	return existing
}

func threadKindRank(value string) int {
	switch history.ThreadKind(value) {
	case history.ThreadSubagent:
		return 2
	case history.ThreadRoot:
		return 1
	default:
		return 0
	}
}

func storedSessionMetadataWins(candidate, existing storedSessionMetadata) bool {
	if confidenceRank(candidate.confidence) != confidenceRank(existing.confidence) {
		return confidenceRank(candidate.confidence) > confidenceRank(existing.confidence)
	}
	candidateLast, candidateValid := parseTimestamp(candidate.lastTS)
	existingLast, existingValid := parseTimestamp(existing.lastTS)
	if candidateValid != existingValid {
		return candidateValid
	}
	if candidateValid && !candidateLast.Equal(existingLast) {
		return candidateLast.After(existingLast)
	}
	return sessionMetadataKey(candidate) > sessionMetadataKey(existing)
}

func confidenceRank(value string) int {
	switch history.Confidence(value) {
	case history.ConfidenceExact:
		return 2
	case history.ConfidenceDerived:
		return 1
	default:
		return 0
	}
}

func sessionMetadataKey(value storedSessionMetadata) string {
	parts := []string{
		value.nativeSessionID.String, value.cwd.String, value.repositoryRoot.String, value.repositoryName.String,
		value.repositoryIdentity.String, value.branch.String, value.threadKind, value.parentNativeSessionID.String, value.forkedFromSessionID.String,
		value.forkedFromMessageID.String, value.originator.String, value.evidence.String,
	}
	return strings.Join(parts, "\x00")
}

func parseTimestamp(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func nullStringValue(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}

func sessionMetadata(value history.Session) storedSessionMetadata {
	return storedSessionMetadata{
		nativeSessionID:       toNullString(value.NativeSessionID),
		fallbackKey:           value.FallbackKey,
		cwd:                   toNullString(value.CWD),
		repositoryRoot:        toNullString(value.RepositoryRoot),
		repositoryName:        toNullString(value.RepositoryName),
		repositoryIdentity:    toNullString(value.RepositoryIdentity),
		repositoryRuleVersion: value.RepositoryRuleVersion,
		branch:                toNullString(value.Branch),
		threadKind:            string(normalizedThreadKind(value.ThreadKind)),
		threadEvidence:        toNullString(value.ThreadEvidence),
		threadConfidence:      string(normalizedConfidence(value.ThreadConfidence)),
		threadRuleVersion:     value.ThreadRuleVersion,
		parentNativeSessionID: toNullString(value.ParentNativeSessionID),
		forkedFromSessionID:   toNullString(value.ForkedFromSessionID),
		forkedFromMessageID:   toNullString(value.ForkedFromMessageID),
		originator:            toNullString(value.Originator),
		evidence:              toNullString(value.Evidence),
		confidence:            string(normalizedConfidence(value.Confidence)),
		firstTS:               timestampString(value.FirstTimestamp),
		lastTS:                timestampString(value.LastTimestamp),
	}
}

func toNullString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func timestampString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func earliestTimestamp(values ...string) string {
	return extremeTimestamp(true, values...)
}

func latestTimestamp(values ...string) string {
	return extremeTimestamp(false, values...)
}

func extremeTimestamp(earliest bool, values ...string) string {
	var selected string
	var selectedTime time.Time
	selectedValid := false
	for _, value := range values {
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			if selectedValid {
				continue
			}
			if selected == "" || (earliest && value < selected) || (!earliest && value > selected) {
				selected = value
			}
			continue
		}
		if !selectedValid || (earliest && parsed.Before(selectedTime)) || (!earliest && parsed.After(selectedTime)) {
			selected, selectedTime = value, parsed
			selectedValid = true
		}
	}
	return selected
}

func nullableTimestamp(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (tx *Tx) ensureSession(provider history.Provider, value history.Session, preferredID int64, allowPromotion bool) (int64, string, error) {
	if provider == "" || value.IdentityKey == "" {
		return 0, "", fmt.Errorf("history session provider and identity key are required")
	}
	if err := tx.promoteFallbackSession(provider, value, preferredID, allowPromotion); err != nil {
		return 0, "", err
	}
	var id int64
	var publicID string
	err := tx.tx.QueryRow(`SELECT id, public_id FROM sessions WHERE provider=? AND identity_key=?`, provider, value.IdentityKey).Scan(&id, &publicID)
	if err == sql.ErrNoRows {
		publicID, err = newPublicID("ses_")
		if err != nil {
			return 0, "", err
		}
		result, err := tx.tx.Exec(`INSERT INTO sessions(
			public_id, provider, identity_key, native_session_id, fallback_key, cwd,
				repository_root, repository_name, repository_identity, repository_rule_version, branch, thread_kind, thread_evidence, thread_confidence, thread_rule_version,
			parent_native_session_id, forked_from_session_id, forked_from_message_id, originator, evidence, confidence, first_ts, last_ts, sample_key)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, publicID, provider, value.IdentityKey,
			nullText(value.NativeSessionID), value.FallbackKey, nullText(value.CWD),
			nullText(value.RepositoryRoot), nullText(value.RepositoryName), nullText(value.RepositoryIdentity), value.RepositoryRuleVersion,
			nullText(value.Branch), normalizedThreadKind(value.ThreadKind), value.ThreadEvidence, normalizedConfidence(value.ThreadConfidence), value.ThreadRuleVersion,
			nullText(value.ParentNativeSessionID), nullText(value.ForkedFromSessionID), nullText(value.ForkedFromMessageID),
			nullText(value.Originator), nullText(value.Evidence), normalizedConfidence(value.Confidence),
			timeText(value.FirstTimestamp), timeText(value.LastTimestamp), sampleKey(publicID))
		if err != nil {
			return 0, "", fmt.Errorf("insert history session: %w", err)
		}
		id, err = result.LastInsertId()
		return id, publicID, err
	}
	if err != nil {
		return 0, "", fmt.Errorf("find history session: %w", err)
	}
	existingMetadata, err := tx.readSessionMetadata(id)
	if err != nil {
		return 0, "", fmt.Errorf("read existing history session metadata: %w", err)
	}
	if err := tx.writeSessionMetadata(id, mergeStoredSessionMetadata(existingMetadata, sessionMetadata(value))); err != nil {
		return 0, "", err
	}
	return id, publicID, nil
}

func (tx *Tx) ensureSourceHead(sessionID int64, provider history.Provider, head history.SourceHead, firstTimestamp, lastTimestamp *time.Time, mode ApplyMode) (int64, string, error) {
	if head.Source.Path == "" {
		return 0, "", fmt.Errorf("history source path is required")
	}
	var id int64
	var publicID string
	var existingFirstTS, existingLastTS sql.NullString
	var existingExtractorVersion int
	err := tx.tx.QueryRow(`SELECT id,public_id,first_ts,last_ts,extractor_version FROM source_heads WHERE provider=? AND source_path=?`, provider, head.Source.Path).Scan(&id, &publicID, &existingFirstTS, &existingLastTS, &existingExtractorVersion)
	if err == sql.ErrNoRows {
		publicID, err = newPublicID("src_")
		if err != nil {
			return 0, "", err
		}
		result, err := tx.tx.Exec(`INSERT INTO source_heads(public_id,provider,source_path,source_kind,session_id,current_sha256,content_hash_state,prefix_fingerprint,tail_fingerprint,extractor_state,size,mtime_unix,complete_offset,line_count,available,first_ts,last_ts,extractor_version,indexed_at,last_attempt_unix,last_error)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, publicID, provider, head.Source.Path, providerSourceKind(provider, head.Source.Kind), sessionID, head.ContentSHA256,
			head.ContentHashState, head.PrefixFingerprint, head.TailFingerprint, head.ExtractorState, head.Size,
			head.ModTimeUnix, head.CompleteOffset, head.LineCount, boolInt(head.Available), timeText(firstTimestamp), timeText(lastTimestamp), history.ExtractorVersion, time.Now().Unix(), time.Now().Unix(), "")
		if err != nil {
			return 0, "", fmt.Errorf("insert history source head: %w", err)
		}
		id, err = result.LastInsertId()
		return id, publicID, err
	}
	if err != nil {
		return 0, "", fmt.Errorf("find history source head: %w", err)
	}
	if existingExtractorVersion > history.ExtractorVersion {
		return 0, "", fmt.Errorf("history source %q uses newer extractor version %d", head.Source.Path, existingExtractorVersion)
	}
	firstTS, lastTS := timestampString(firstTimestamp), timestampString(lastTimestamp)
	if mode == ApplyAppend {
		firstTS = earliestTimestamp(existingFirstTS.String, firstTS)
		lastTS = latestTimestamp(existingLastTS.String, lastTS)
	}
	_, err = tx.tx.Exec(`UPDATE source_heads SET session_id=?,source_kind=?,current_sha256=?,content_hash_state=?,prefix_fingerprint=?,tail_fingerprint=?,extractor_state=?,size=?,mtime_unix=?,complete_offset=?,line_count=?,available=?,first_ts=?,last_ts=?,extractor_version=?,indexed_at=?,last_attempt_unix=?,last_error='' WHERE id=?`,
		sessionID, providerSourceKind(provider, head.Source.Kind), head.ContentSHA256, head.ContentHashState, head.PrefixFingerprint, head.TailFingerprint, head.ExtractorState,
		head.Size, head.ModTimeUnix, head.CompleteOffset, head.LineCount, boolInt(head.Available), nullableTimestamp(firstTS), nullableTimestamp(lastTS), history.ExtractorVersion, time.Now().Unix(), time.Now().Unix(), id)
	if err != nil {
		return 0, "", fmt.Errorf("update history source head: %w", err)
	}
	return id, publicID, nil
}

func (tx *Tx) ensureSourceLocation(sourceID int64, provider history.Provider, source history.SourceReference, available bool) (int64, error) {
	kind := source.Kind
	if kind == "" {
		kind = history.LocationProviderLive
	}
	if kind != history.LocationProviderLive && kind != history.LocationProviderArchive {
		return 0, fmt.Errorf("source head location must be provider_live or provider_archive")
	}
	key := structuredLocationKey("source", string(provider), string(kind), source.Path)
	return tx.ensureLocation(key, kind, sourceID, 0, source, available)
}

func (tx *Tx) ensureSnapshot(sessionID int64, value history.PreservedSnapshot) (int64, string, error) {
	if value.ContentSHA256 == "" {
		return 0, "", fmt.Errorf("preserved snapshot hash is required")
	}
	var id, existingSessionID, existingSize int64
	var publicID string
	var existingFirstTS, existingLastTS sql.NullString
	var existingExtractorVersion int
	err := tx.tx.QueryRow(`SELECT id,public_id,session_id,size,first_ts,last_ts,extractor_version FROM preserved_snapshots WHERE provider=? AND content_sha256=?`, value.Provider, value.ContentSHA256).Scan(
		&id, &publicID, &existingSessionID, &existingSize, &existingFirstTS, &existingLastTS, &existingExtractorVersion)
	if err == nil {
		if existingSize != value.Size {
			return 0, "", fmt.Errorf("preserved snapshot %s conflicts with immutable session or size", value.ContentSHA256)
		}
		if existingExtractorVersion > history.ExtractorVersion {
			return 0, "", fmt.Errorf("preserved snapshot %s uses newer extractor version %d", value.ContentSHA256, existingExtractorVersion)
		}
		if existingSessionID != sessionID {
			if existingExtractorVersion == history.ExtractorVersion {
				return 0, "", fmt.Errorf("preserved snapshot %s conflicts with immutable session or size", value.ContentSHA256)
			}
			if _, err := tx.tx.Exec(`UPDATE preserved_snapshots SET session_id=? WHERE id=?`, sessionID, id); err != nil {
				return 0, "", fmt.Errorf("rehome stale preserved snapshot session: %w", err)
			}
		}
		firstTS, lastTS := timestampString(value.FirstTS), timestampString(value.LastTS)
		if existingExtractorVersion == history.ExtractorVersion {
			var err error
			if firstTS, err = reconcileSnapshotTimestamp("first", existingFirstTS, firstTS); err != nil {
				return 0, "", err
			}
			if lastTS, err = reconcileSnapshotTimestamp("last", existingLastTS, lastTS); err != nil {
				return 0, "", err
			}
		}
		if _, err := tx.tx.Exec(`UPDATE preserved_snapshots SET first_ts=?,last_ts=?,extractor_version=? WHERE id=?`, nullableTimestamp(firstTS), nullableTimestamp(lastTS), history.ExtractorVersion, id); err != nil {
			return 0, "", fmt.Errorf("update preserved snapshot extractor version: %w", err)
		}
		return id, publicID, nil
	}
	if err != sql.ErrNoRows {
		return 0, "", fmt.Errorf("find preserved snapshot: %w", err)
	}
	publicID, err = newPublicID("snap_")
	if err != nil {
		return 0, "", err
	}
	result, err := tx.tx.Exec(`INSERT INTO preserved_snapshots(public_id,provider,session_id,content_sha256,size,first_ts,last_ts,extractor_version,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		publicID, value.Provider, sessionID, value.ContentSHA256, value.Size, timeText(value.FirstTS), timeText(value.LastTS), history.ExtractorVersion, time.Now().Unix())
	if err != nil {
		return 0, "", fmt.Errorf("insert preserved snapshot: %w", err)
	}
	id, err = result.LastInsertId()
	return id, publicID, err
}

func reconcileSnapshotTimestamp(label string, existing sql.NullString, candidate string) (string, error) {
	if !existing.Valid || existing.String == "" {
		return candidate, nil
	}
	if candidate == "" || candidate == existing.String {
		return existing.String, nil
	}
	return "", fmt.Errorf("preserved snapshot %s timestamp conflicts for immutable content", label)
}

func (tx *Tx) ensureSnapshotLocation(snapshotID int64, source history.SourceReference) (int64, error) {
	if source.Kind != history.LocationVault {
		return 0, fmt.Errorf("preserved snapshot location must be vault")
	}
	key := structuredLocationKey("vault", string(source.Provider), source.Archive, source.RelativePath, fmt.Sprint(source.VaultVersion))
	return tx.ensureLocation(key, history.LocationVault, 0, snapshotID, source, true)
}

func (tx *Tx) ensureLocation(key string, kind history.LocationKind, sourceID, snapshotID int64, source history.SourceReference, available bool) (int64, error) {
	var id int64
	var existingSourceID, existingSnapshotID sql.NullInt64
	err := tx.tx.QueryRow(`SELECT id,source_head_id,snapshot_id FROM locations WHERE location_key=?`, key).Scan(&id, &existingSourceID, &existingSnapshotID)
	if err == nil {
		if existingSourceID.Int64 != sourceID || existingSourceID.Valid != (sourceID != 0) || existingSnapshotID.Int64 != snapshotID || existingSnapshotID.Valid != (snapshotID != 0) {
			return 0, fmt.Errorf("history location key %q is associated with different content", key)
		}
		_, err = tx.tx.Exec(`UPDATE locations SET available=? WHERE id=?`, boolInt(available), id)
		return id, err
	}
	if err != sql.ErrNoRows {
		return 0, fmt.Errorf("find history location: %w", err)
	}
	result, err := tx.tx.Exec(`INSERT INTO locations(location_key,kind,source_head_id,snapshot_id,source_path,relative_path,archive,vault_version,available) VALUES(?,?,?,?,?,?,?,?,?)`,
		key, kind, nullableID(sourceID), nullableID(snapshotID), source.Path, source.RelativePath, source.Archive, source.VaultVersion, boolInt(available))
	if err != nil {
		return 0, fmt.Errorf("insert history location: %w", err)
	}
	return result.LastInsertId()
}

func structuredLocationKey(parts ...string) string {
	encoded, err := json.Marshal(parts)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func (tx *Tx) ensurePrompts(sessionID int64, prompts []history.Prompt) (map[string]string, map[string]int64, error) {
	publicIDs := make(map[string]string, len(prompts))
	databaseIDs := make(map[string]int64, len(prompts))
	for _, prompt := range prompts {
		if prompt.LogicalKey == "" {
			return nil, nil, fmt.Errorf("logical prompt key is required")
		}
		var id int64
		var publicID string
		var existingTimestamp sql.NullString
		var existingText, existingClassification string
		var existingSearchable bool
		var existingExtractorVersion, occurrenceCount int
		err := tx.tx.QueryRow(`SELECT id,public_id,timestamp,clean_text,classification,searchable,extractor_version,occurrence_count FROM prompts WHERE session_id=? AND logical_key=?`, sessionID, prompt.LogicalKey).Scan(&id, &publicID, &existingTimestamp, &existingText, &existingClassification, &existingSearchable, &existingExtractorVersion, &occurrenceCount)
		if err == sql.ErrNoRows {
			publicID, err = newPublicID("prm_")
			if err != nil {
				return nil, nil, err
			}
			result, err := tx.tx.Exec(`INSERT INTO prompts(public_id,session_id,logical_key,native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,model,evidence,confidence,extractor_version,sample_key)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, publicID, sessionID, prompt.LogicalKey, nullText(prompt.NativeMessageID), nullText(prompt.ParentNativeMessageID), normalizedRole(prompt.Role), prompt.CleanText,
				normalizedClassification(prompt.Classification), normalizedPromptKind(prompt), history.PromptKindVersion, boolInt(searchablePrompt(prompt)), boolInt(prompt.Oversized), timeText(prompt.Timestamp), nullText(prompt.Model), nullText(prompt.Evidence), normalizedConfidence(prompt.Confidence), history.ExtractorVersion, sampleKey(publicID))
			if err != nil {
				return nil, nil, fmt.Errorf("insert logical prompt: %w", err)
			}
			id, err = result.LastInsertId()
			if err != nil {
				return nil, nil, err
			}
		} else if err != nil {
			return nil, nil, fmt.Errorf("find logical prompt: %w", err)
		} else if occurrenceCount == 0 || canonicalPromptWins(prompt, existingTimestamp, existingText, existingClassification, existingSearchable, existingExtractorVersion) {
			_, err = tx.tx.Exec(`UPDATE prompts SET native_message_id=?,parent_native_message_id=?,role=?,clean_text=?,classification=?,prompt_kind=?,prompt_kind_version=?,searchable=?,oversized=?,timestamp=?,model=?,evidence=?,confidence=?,extractor_version=? WHERE id=?`,
				nullText(prompt.NativeMessageID), nullText(prompt.ParentNativeMessageID), normalizedRole(prompt.Role), prompt.CleanText, normalizedClassification(prompt.Classification), normalizedPromptKind(prompt), history.PromptKindVersion,
				boolInt(searchablePrompt(prompt)), boolInt(prompt.Oversized), timeText(prompt.Timestamp), nullText(prompt.Model), nullText(prompt.Evidence), normalizedConfidence(prompt.Confidence), history.ExtractorVersion, id)
			if err != nil {
				return nil, nil, fmt.Errorf("update logical prompt: %w", err)
			}
		}
		publicIDs[prompt.LogicalKey], databaseIDs[prompt.LogicalKey] = publicID, id
	}
	return publicIDs, databaseIDs, nil
}

func canonicalPromptWins(candidate history.Prompt, existingTimestamp sql.NullString, existingText, existingClassification string, existingSearchable bool, existingExtractorVersion int) bool {
	if existingExtractorVersion != history.ExtractorVersion {
		return existingExtractorVersion < history.ExtractorVersion
	}
	if promptSemanticRank(candidate.Classification) != promptSemanticRank(history.Classification(existingClassification)) {
		return promptSemanticRank(candidate.Classification) > promptSemanticRank(history.Classification(existingClassification))
	}
	if candidate.Timestamp != nil {
		if !existingTimestamp.Valid {
			return true
		}
		parsed, err := time.Parse(time.RFC3339Nano, existingTimestamp.String)
		if err != nil || candidate.Timestamp.After(parsed) {
			return true
		}
		if candidate.Timestamp.Before(parsed) {
			return false
		}
	} else if existingTimestamp.Valid {
		return false
	}
	if len(candidate.CleanText) != len(existingText) {
		return len(candidate.CleanText) > len(existingText)
	}
	return candidate.CleanText >= existingText
}

func promptSemanticRank(classification history.Classification) int {
	if classification == history.ClassificationProviderMetadata {
		return 0
	}
	return 1
}

func (tx *Tx) addOccurrences(locationID, sourceID, snapshotID int64, promptIDs map[string]int64, prompts []history.Prompt, occurrences []history.Occurrence) error {
	promptValues := make(map[string]history.Prompt, len(prompts))
	for _, prompt := range prompts {
		if existing, ok := promptValues[prompt.LogicalKey]; !ok || history.CanonicalPromptWins(prompt, existing) {
			promptValues[prompt.LogicalKey] = prompt
		}
	}
	for _, occurrence := range occurrences {
		promptID, ok := promptIDs[occurrence.PromptKey]
		if !ok {
			return fmt.Errorf("occurrence references unknown prompt %q", occurrence.PromptKey)
		}
		prompt := occurrence.Variant
		if prompt.CleanText == "" {
			prompt = promptValues[occurrence.PromptKey]
		}
		if prompt.CleanText == "" {
			return fmt.Errorf("occurrence references missing prompt value %q", occurrence.PromptKey)
		}
		if _, err := tx.tx.Exec(`INSERT INTO occurrences(prompt_id,location_id,source_head_id,snapshot_id,native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,timestamp_unix_nano,model,evidence,confidence,extractor_version,line_number,start_offset,end_offset)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO UPDATE SET native_message_id=excluded.native_message_id,parent_native_message_id=excluded.parent_native_message_id,role=excluded.role,clean_text=excluded.clean_text,classification=excluded.classification,prompt_kind=excluded.prompt_kind,prompt_kind_version=excluded.prompt_kind_version,searchable=excluded.searchable,oversized=excluded.oversized,timestamp=excluded.timestamp,timestamp_unix_nano=excluded.timestamp_unix_nano,model=excluded.model,evidence=excluded.evidence,confidence=excluded.confidence,extractor_version=excluded.extractor_version`,
			promptID, locationID, nullableID(sourceID), nullableID(snapshotID), nullText(prompt.NativeMessageID), nullText(prompt.ParentNativeMessageID), normalizedRole(prompt.Role), prompt.CleanText,
			normalizedClassification(prompt.Classification), normalizedPromptKind(prompt), history.PromptKindVersion, boolInt(searchablePrompt(prompt)), boolInt(prompt.Oversized), timeText(prompt.Timestamp), timeUnixNano(prompt.Timestamp), nullText(prompt.Model), nullText(prompt.Evidence), normalizedConfidence(prompt.Confidence), history.ExtractorVersion,
			occurrence.LineNumber, occurrence.StartOffset, occurrence.EndOffset); err != nil {
			return fmt.Errorf("insert prompt occurrence: %w", err)
		}
	}
	return nil
}

func (tx *Tx) deleteSourceOccurrences(sourceID int64) error {
	return tx.deleteOccurrences(`source_head_id=?`, sourceID, "source")
}

func (tx *Tx) deleteSnapshotOccurrences(snapshotID int64) error {
	return tx.deleteOccurrences(`snapshot_id=?`, snapshotID, "snapshot")
}

func (tx *Tx) deleteOccurrences(predicate string, id int64, label string) error {
	rows, err := tx.tx.Query(`SELECT DISTINCT prompt_id FROM occurrences WHERE `+predicate, id)
	if err != nil {
		return fmt.Errorf("list %s occurrence prompts: %w", label, err)
	}
	var promptIDs []int64
	for rows.Next() {
		var promptID int64
		if err := rows.Scan(&promptID); err != nil {
			rows.Close()
			return fmt.Errorf("scan %s occurrence prompt: %w", label, err)
		}
		promptIDs = append(promptIDs, promptID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`DELETE FROM occurrences WHERE `+predicate, id); err != nil {
		return fmt.Errorf("delete %s occurrences: %w", label, err)
	}
	for _, promptID := range promptIDs {
		if err := tx.refreshPromptCanonical(promptID); err != nil {
			return err
		}
	}
	return nil
}

func (tx *Tx) tombstoneSourceOrphans(sourceID int64, reason string) error {
	if _, err := tx.tx.Exec(`INSERT INTO prompt_tombstones(source_head_id,provider,source_path,prompt_public_id,logical_key,reason,deleted_at)
		SELECT sh.id,sh.provider,sh.source_path,p.public_id,p.logical_key,?,?
		FROM source_heads sh JOIN occurrences o ON o.source_head_id=sh.id JOIN prompts p ON p.id=o.prompt_id
		WHERE sh.id=? GROUP BY sh.id,p.id HAVING p.occurrence_count=COUNT(o.id)`, reason, time.Now().Unix(), sourceID); err != nil {
		return fmt.Errorf("record bounded history prompt tombstones: %w", err)
	}
	if _, err := tx.tx.Exec(`DELETE FROM prompt_tombstones WHERE source_head_id=? AND reason<>'role-disabled' AND id NOT IN (
		SELECT id FROM prompt_tombstones WHERE source_head_id=? AND reason<>'role-disabled' ORDER BY deleted_at DESC,id DESC LIMIT 256
	)`, sourceID, sourceID); err != nil {
		return fmt.Errorf("bound history prompt tombstones: %w", err)
	}
	return nil
}

func (tx *Tx) tombstoneAssistantPrompts() error {
	deletedAt := time.Now().Unix()
	if _, err := tx.tx.Exec(`INSERT INTO prompt_tombstones(source_head_id,provider,source_path,prompt_public_id,logical_key,reason,deleted_at)
		SELECT sh.id,sh.provider,sh.source_path,p.public_id,p.logical_key,'role-disabled',?
		FROM prompts p JOIN occurrences o ON o.prompt_id=p.id JOIN source_heads sh ON sh.id=o.source_head_id
		WHERE p.role='assistant' GROUP BY sh.id,p.id`, deletedAt); err != nil {
		return fmt.Errorf("tombstone disabled assistant source prompts: %w", err)
	}
	if _, err := tx.tx.Exec(`INSERT INTO vault_prompt_tombstones(archive,provider,session_public_id,logical_key,prompt_public_id,deleted_at)
		SELECT l.archive,ps.provider,s.public_id,p.logical_key,p.public_id,?
		FROM prompts p JOIN sessions s ON s.id=p.session_id JOIN occurrences o ON o.prompt_id=p.id
		JOIN locations l ON l.id=o.location_id JOIN preserved_snapshots ps ON ps.id=l.snapshot_id
		WHERE p.role='assistant' AND l.archive<>''
		GROUP BY l.archive,ps.provider,s.public_id,p.logical_key,p.public_id
		ON CONFLICT(archive,provider,session_public_id,logical_key) DO UPDATE SET prompt_public_id=excluded.prompt_public_id,deleted_at=excluded.deleted_at`, deletedAt); err != nil {
		return fmt.Errorf("tombstone disabled assistant vault prompts: %w", err)
	}
	return nil
}

func (tx *Tx) removeRestoredTombstones(sourceID int64) error {
	if _, err := tx.tx.Exec(`DELETE FROM prompt_tombstones WHERE source_head_id=? AND EXISTS (
		SELECT 1 FROM prompts WHERE prompts.public_id=prompt_tombstones.prompt_public_id AND prompts.occurrence_count>0
	)`, sourceID); err != nil {
		return fmt.Errorf("remove restored history prompt tombstones: %w", err)
	}
	return nil
}

func (tx *Tx) advanceGenerationIf(advance bool) error {
	if !advance {
		return nil
	}
	_, err := tx.tx.Exec(`INSERT INTO meta(key,value) VALUES('index_generation','1')
		ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`)
	if err != nil {
		return fmt.Errorf("advance history index generation: %w", err)
	}
	return nil
}

func (tx *Tx) clearSourceError(provider history.Provider, path string) error {
	if _, err := tx.tx.Exec(`DELETE FROM source_errors WHERE provider=? AND source_path=?`, provider, path); err != nil {
		return fmt.Errorf("clear history source error: %w", err)
	}
	return nil
}

func (tx *Tx) refreshPromptCanonical(promptID int64) error {
	_, err := tx.tx.Exec(`WITH canonical AS (
		SELECT native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,model,evidence,confidence,extractor_version
		FROM occurrences WHERE prompt_id=?
		ORDER BY extractor_version DESC,(classification <> 'provider_metadata') DESC,timestamp_unix_nano IS NOT NULL DESC,timestamp_unix_nano DESC,length(CAST(clean_text AS BLOB)) DESC,clean_text DESC LIMIT 1
	)
	UPDATE prompts SET (native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,model,evidence,confidence,extractor_version)=
		(SELECT native_message_id,parent_native_message_id,role,clean_text,classification,prompt_kind,prompt_kind_version,searchable,oversized,timestamp,model,evidence,confidence,extractor_version FROM canonical)
	WHERE id=? AND occurrence_count>0`, promptID, promptID)
	if err != nil {
		return fmt.Errorf("refresh source-backed prompt canonical: %w", err)
	}
	return nil
}

// CheckFTS validates the FTS index, including external-content agreement.
func (s *Store) CheckFTS() error {
	if _, err := s.runner.Exec(`INSERT INTO prompt_fts(prompt_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		return fmt.Errorf("check history FTS integrity: %w", err)
	}
	return nil
}

// RebuildFTS rebuilds the external-content index from searchable prompts.
func (s *Store) RebuildFTS() error {
	if _, err := s.runner.Exec(`INSERT INTO prompt_fts(prompt_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild history FTS: %w", err)
	}
	return s.CheckFTS()
}

// Stats returns normalized row counts.
func (s *Store) Stats() (Stats, error) {
	var value Stats
	err := s.runner.QueryRow(`SELECT (SELECT COUNT(*) FROM sessions),(SELECT COUNT(*) FROM source_heads),(SELECT COUNT(*) FROM preserved_snapshots),(SELECT COUNT(*) FROM locations),(SELECT COUNT(*) FROM prompts),(SELECT COUNT(*) FROM occurrences)`).Scan(
		&value.Sessions, &value.Sources, &value.Snapshots, &value.Locations, &value.Prompts, &value.Occurrences)
	return value, err
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableID(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeText(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func timeUnixNano(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixNano()
}

func normalizedThreadKind(value history.ThreadKind) history.ThreadKind {
	if value == "" {
		return history.ThreadUnknown
	}
	return value
}

func normalizedConfidence(value history.Confidence) history.Confidence {
	if value == "" {
		return history.ConfidenceUnknown
	}
	return value
}

func normalizedRole(value history.Role) history.Role {
	if value == "" {
		return history.RoleUnknown
	}
	return value
}

func normalizedClassification(value history.Classification) history.Classification {
	if value == "" {
		return history.ClassificationUnknown
	}
	return value
}

func normalizedPromptKind(value history.Prompt) history.PromptKind {
	if value.PromptKind == "" {
		return history.ClassifyPromptKind(value.CleanText, value.Role, value.Classification)
	}
	return value.PromptKind
}

func searchablePrompt(value history.Prompt) bool {
	kind := normalizedPromptKind(value)
	classifiedEnvelope := kind != history.PromptKindHuman && kind != history.PromptKindUnknown
	searchableRole := (value.Role == history.RoleUser && ((value.Classification == history.ClassificationHuman && value.Searchable) || classifiedEnvelope)) ||
		(value.Role == history.RoleAssistant && value.Classification == history.ClassificationAssistant && value.Searchable)
	return !value.Oversized && len([]byte(value.CleanText)) <= history.MaxPromptBytes && searchableRole && value.CleanText != ""
}
