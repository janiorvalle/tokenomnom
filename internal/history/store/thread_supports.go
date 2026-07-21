package store

import (
	"database/sql"
	"fmt"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func (tx *Tx) reconcileSessionThreadSupport(sessionID int64, value history.Session, sourceID, snapshotID int64) (bool, error) {
	if (sourceID == 0) == (snapshotID == 0) {
		return false, fmt.Errorf("history thread support must identify exactly one source or snapshot")
	}
	supportColumn, supportID := "source_head_id", sourceID
	if snapshotID != 0 {
		supportColumn, supportID = "snapshot_id", snapshotID
	}
	var previousSessionID int64
	err := tx.tx.QueryRow(`SELECT session_id FROM session_thread_supports WHERE `+supportColumn+`=?`, supportID).Scan(&previousSessionID)
	if err != nil && err != sql.ErrNoRows {
		return false, fmt.Errorf("read previous history thread support: %w", err)
	}
	if _, err := tx.tx.Exec(`DELETE FROM session_thread_supports WHERE `+supportColumn+`=?`, supportID); err != nil {
		return false, fmt.Errorf("replace history thread support: %w", err)
	}
	if _, err := tx.tx.Exec(`INSERT INTO session_thread_supports(
		session_id,source_head_id,snapshot_id,thread_kind,evidence,confidence,rule_version,parent_native_session_id,
		forked_from_session_id,forked_from_message_id,originator) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		sessionID, nullableID(sourceID), nullableID(snapshotID), normalizedThreadKind(value.ThreadKind), value.ThreadEvidence,
		normalizedConfidence(value.ThreadConfidence), value.ThreadRuleVersion, value.ParentNativeSessionID,
		value.ForkedFromSessionID, value.ForkedFromMessageID, value.Originator); err != nil {
		return false, fmt.Errorf("store history thread support: %w", err)
	}
	changed, err := tx.refreshCanonicalSessionThreadSupport(sessionID)
	if err != nil {
		return false, err
	}
	if previousSessionID != 0 && previousSessionID != sessionID {
		previousChanged, err := tx.refreshCanonicalSessionThreadSupport(previousSessionID)
		if err != nil {
			return false, err
		}
		changed = changed || previousChanged
	}
	return changed, nil
}

func (tx *Tx) refreshCanonicalSessionThreadSupport(sessionID int64) (bool, error) {
	var canonical history.Session
	err := tx.tx.QueryRow(`SELECT thread_kind,evidence,confidence,rule_version,parent_native_session_id,
		forked_from_session_id,forked_from_message_id,originator FROM session_thread_supports WHERE session_id=?
		ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,
		rule_version DESC,CASE thread_kind WHEN 'subagent' THEN 2 WHEN 'root' THEN 1 ELSE 0 END DESC,
		evidence DESC,parent_native_session_id DESC,forked_from_session_id DESC,forked_from_message_id DESC,originator DESC LIMIT 1`, sessionID).Scan(
		&canonical.ThreadKind, &canonical.ThreadEvidence, &canonical.ThreadConfidence, &canonical.ThreadRuleVersion,
		&canonical.ParentNativeSessionID, &canonical.ForkedFromSessionID, &canonical.ForkedFromMessageID, &canonical.Originator)
	if err == sql.ErrNoRows {
		result, resetErr := tx.tx.Exec(`UPDATE sessions SET thread_kind='unknown',thread_evidence='',thread_confidence='unknown',
			thread_rule_version=0,parent_native_session_id=NULL,forked_from_session_id=NULL,forked_from_message_id=NULL,originator=NULL
			WHERE id=? AND (thread_kind<>'unknown' OR thread_evidence<>'' OR thread_confidence<>'unknown' OR thread_rule_version<>0 OR
			parent_native_session_id IS NOT NULL OR forked_from_session_id IS NOT NULL OR forked_from_message_id IS NOT NULL OR originator IS NOT NULL)`, sessionID)
		if resetErr != nil {
			return false, fmt.Errorf("reset unsupported history thread metadata: %w", resetErr)
		}
		changed, resetErr := result.RowsAffected()
		if resetErr != nil {
			return false, fmt.Errorf("count reset history thread metadata: %w", resetErr)
		}
		return changed > 0, nil
	}
	if err != nil {
		return false, fmt.Errorf("select canonical history thread support: %w", err)
	}
	result, err := tx.tx.Exec(`UPDATE sessions SET thread_kind=?,thread_evidence=?,thread_confidence=?,thread_rule_version=?,
		parent_native_session_id=?,forked_from_session_id=?,forked_from_message_id=?,originator=? WHERE id=? AND (
		thread_kind<>? OR thread_evidence<>? OR thread_confidence<>? OR thread_rule_version<>? OR
		COALESCE(parent_native_session_id,'')<>? OR COALESCE(forked_from_session_id,'')<>? OR
		COALESCE(forked_from_message_id,'')<>? OR COALESCE(originator,'')<>?)`,
		canonical.ThreadKind, canonical.ThreadEvidence, canonical.ThreadConfidence, canonical.ThreadRuleVersion,
		nullText(canonical.ParentNativeSessionID), nullText(canonical.ForkedFromSessionID), nullText(canonical.ForkedFromMessageID), nullText(canonical.Originator), sessionID,
		canonical.ThreadKind, canonical.ThreadEvidence, canonical.ThreadConfidence, canonical.ThreadRuleVersion,
		canonical.ParentNativeSessionID, canonical.ForkedFromSessionID, canonical.ForkedFromMessageID, canonical.Originator)
	if err != nil {
		return false, fmt.Errorf("update canonical history thread support: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count canonical history thread support update: %w", err)
	}
	return changed > 0, nil
}
