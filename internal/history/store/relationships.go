package store

import (
	"database/sql"
	"fmt"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

const maxSessionRelationships = 20

// SessionRelationship is one inspectable conversational edge. ParentSessionID
// is nil until the provider-native parent is indexed.
type SessionRelationship struct {
	RelationKind          history.RelationKind    `json:"relation_kind"`
	ParentSessionID       *string                 `json:"parent_session_id"`
	ChildSessionID        string                  `json:"child_session_id"`
	ParentNativeSessionID string                  `json:"parent_native_session_id,omitempty"`
	ParentNativeMessageID string                  `json:"parent_native_message_id,omitempty"`
	ProviderNativeValue   string                  `json:"provider_native_value,omitempty"`
	Evidence              string                  `json:"evidence"`
	Confidence            history.Confidence      `json:"confidence"`
	RuleVersion           int                     `json:"rule_version"`
	ResolutionState       history.ResolutionState `json:"resolution_state"`
}

func (tx *Tx) reconcileSessionRelationships(provider history.Provider, childID int64, relationships []history.Relationship, sourceID, snapshotID int64) (bool, error) {
	if (sourceID == 0) == (snapshotID == 0) {
		return false, fmt.Errorf("history relationship support must identify exactly one source or snapshot")
	}
	changed := false
	supportColumn, supportID := "source_head_id", sourceID
	if snapshotID != 0 {
		supportColumn, supportID = "snapshot_id", snapshotID
	}
	if _, err := tx.tx.Exec(`DELETE FROM session_relation_supports WHERE `+supportColumn+`=?`, supportID); err != nil {
		return false, fmt.Errorf("replace history relationship support: %w", err)
	}
	for _, relationship := range relationships {
		if relationship.Kind != history.RelationSubagent && relationship.Kind != history.RelationFork {
			return false, fmt.Errorf("unsupported history relationship kind %q", relationship.Kind)
		}
		if relationship.ParentNativeSessionID == "" {
			continue
		}
		if relationship.Evidence == "" {
			return false, fmt.Errorf("history relationship evidence is required")
		}
		if relationship.RuleVersion == 0 {
			relationship.RuleVersion = history.RelationshipRuleVersion
		}
		parentID, err := tx.parentSessionID(provider, relationship.ParentNativeSessionID, childID)
		if err != nil {
			return false, err
		}
		if parentID == 0 {
			result, err := tx.tx.Exec(`INSERT INTO session_relations(
				provider,parent_session_id,child_session_id,relation_kind,parent_native_session_id,parent_native_message_id,
				provider_native_value,evidence,confidence,rule_version,resolution_state)
				VALUES(?,NULL,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, provider, childID, relationship.Kind,
				relationship.ParentNativeSessionID, relationship.ParentNativeMessageID, relationship.ProviderNativeValue,
				relationship.Evidence, normalizedConfidence(relationship.Confidence), relationship.RuleVersion, history.ResolutionUnresolved)
			if err != nil {
				return false, fmt.Errorf("store unresolved history relationship: %w", err)
			}
			inserted, err := result.RowsAffected()
			if err != nil {
				return false, fmt.Errorf("count stored unresolved history relationship: %w", err)
			}
			changed = changed || inserted > 0
			relationID, err := tx.unresolvedRelationshipID(provider, childID, relationship)
			if err != nil {
				return false, err
			}
			if err := tx.attachRelationshipSupport(relationID, sourceID, snapshotID, relationship); err != nil {
				return false, err
			}
			continue
		}
		relationID, resolved, err := tx.resolveRelationship(provider, parentID, childID, relationship)
		if err != nil {
			return false, err
		}
		changed = changed || resolved
		if err := tx.attachRelationshipSupport(relationID, sourceID, snapshotID, relationship); err != nil {
			return false, err
		}
	}
	result, err := tx.tx.Exec(`DELETE FROM session_relations WHERE child_session_id=?
		AND NOT EXISTS(SELECT 1 FROM session_relation_supports support WHERE support.relation_id=session_relations.id)`, childID)
	if err != nil {
		return false, fmt.Errorf("remove unsupported history relationships: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count removed unsupported history relationships: %w", err)
	}
	metadataChanged, err := tx.refreshChildRelationshipMetadata(childID)
	if err != nil {
		return false, err
	}
	return changed || removed > 0 || metadataChanged, nil
}

func (tx *Tx) unresolvedRelationshipID(provider history.Provider, childID int64, relationship history.Relationship) (int64, error) {
	var id int64
	err := tx.tx.QueryRow(`SELECT id FROM session_relations WHERE provider=? AND child_session_id=? AND relation_kind=?
		AND parent_session_id IS NULL AND parent_native_session_id=?`, provider, childID, relationship.Kind,
		relationship.ParentNativeSessionID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("read unresolved history relationship: %w", err)
	}
	return id, nil
}

func (tx *Tx) attachRelationshipSupport(relationID, sourceID, snapshotID int64, relationship history.Relationship) error {
	_, err := tx.tx.Exec(`INSERT OR IGNORE INTO session_relation_supports(
		relation_id,source_head_id,snapshot_id,parent_native_message_id,provider_native_value,evidence,confidence,rule_version)
		VALUES(?,?,?,?,?,?,?,?)`, relationID, nullableID(sourceID), nullableID(snapshotID), relationship.ParentNativeMessageID,
		relationship.ProviderNativeValue, relationship.Evidence, normalizedConfidence(relationship.Confidence), relationship.RuleVersion)
	if err != nil {
		return fmt.Errorf("store history relationship support: %w", err)
	}
	return nil
}

func (tx *Tx) refreshChildRelationshipMetadata(childID int64) (bool, error) {
	result, err := tx.tx.Exec(`UPDATE session_relations SET
		parent_native_message_id=(SELECT parent_native_message_id FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,
			parent_native_message_id DESC,provider_native_value DESC LIMIT 1),
		provider_native_value=(SELECT provider_native_value FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,
			parent_native_message_id DESC,provider_native_value DESC LIMIT 1),
		evidence=(SELECT evidence FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,
			parent_native_message_id DESC,provider_native_value DESC LIMIT 1),
		confidence=(SELECT confidence FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,
			parent_native_message_id DESC,provider_native_value DESC LIMIT 1),
		rule_version=(SELECT rule_version FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,
			parent_native_message_id DESC,provider_native_value DESC LIMIT 1)
		WHERE child_session_id=? AND EXISTS(SELECT 1 FROM session_relation_supports support WHERE support.relation_id=session_relations.id)
		AND (parent_native_message_id<>(SELECT parent_native_message_id FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,parent_native_message_id DESC,provider_native_value DESC LIMIT 1)
		OR provider_native_value<>(SELECT provider_native_value FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,parent_native_message_id DESC,provider_native_value DESC LIMIT 1)
		OR evidence<>(SELECT evidence FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,parent_native_message_id DESC,provider_native_value DESC LIMIT 1)
		OR confidence<>(SELECT confidence FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,parent_native_message_id DESC,provider_native_value DESC LIMIT 1)
		OR rule_version<>(SELECT rule_version FROM session_relation_supports support WHERE support.relation_id=session_relations.id
			ORDER BY CASE confidence WHEN 'exact' THEN 2 WHEN 'derived' THEN 1 ELSE 0 END DESC,rule_version DESC,evidence DESC,parent_native_message_id DESC,provider_native_value DESC LIMIT 1))`, childID)
	if err != nil {
		return false, fmt.Errorf("refresh canonical history relationship evidence: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count canonical history relationship evidence update: %w", err)
	}
	return changed > 0, nil
}

func (tx *Tx) parentSessionID(provider history.Provider, nativeID string, childID int64) (int64, error) {
	var parentID int64
	err := tx.tx.QueryRow(`SELECT id FROM sessions WHERE provider=? AND native_session_id=? AND id<>? ORDER BY id LIMIT 1`, provider, nativeID, childID).Scan(&parentID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("resolve provider-native parent session: %w", err)
	}
	return parentID, nil
}

func (tx *Tx) resolveRelationship(provider history.Provider, parentID, childID int64, relationship history.Relationship) (int64, bool, error) {
	changed := false
	var unresolvedID int64
	_ = tx.tx.QueryRow(`SELECT id FROM session_relations WHERE provider=? AND child_session_id=? AND relation_kind=?
		AND parent_session_id IS NULL AND parent_native_session_id=?`, provider, childID, relationship.Kind,
		relationship.ParentNativeSessionID).Scan(&unresolvedID)
	confidence := normalizedConfidence(relationship.Confidence)
	result, err := tx.tx.Exec(`UPDATE OR IGNORE session_relations SET parent_session_id=?,parent_native_message_id=?,provider_native_value=?,
		evidence=?,confidence=?,rule_version=?,resolution_state='resolved'
		WHERE provider=? AND child_session_id=? AND relation_kind=? AND parent_session_id IS NULL AND parent_native_session_id=?`,
		parentID, relationship.ParentNativeMessageID, relationship.ProviderNativeValue, relationship.Evidence, confidence,
		relationship.RuleVersion, provider, childID, relationship.Kind, relationship.ParentNativeSessionID)
	if err != nil {
		return 0, false, fmt.Errorf("resolve history relationship: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("count resolved history relationship: %w", err)
	}
	changed = changed || updated > 0
	result, err = tx.tx.Exec(`INSERT INTO session_relations(
		provider,parent_session_id,child_session_id,relation_kind,parent_native_session_id,parent_native_message_id,
		provider_native_value,evidence,confidence,rule_version,resolution_state)
		VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, provider, parentID, childID, relationship.Kind,
		relationship.ParentNativeSessionID, relationship.ParentNativeMessageID, relationship.ProviderNativeValue,
		relationship.Evidence, confidence, relationship.RuleVersion, history.ResolutionResolved)
	if err != nil {
		return 0, false, fmt.Errorf("store resolved history relationship: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("count stored resolved history relationship: %w", err)
	}
	changed = changed || inserted > 0
	var resolvedID int64
	if err := tx.tx.QueryRow(`SELECT id FROM session_relations WHERE parent_session_id=? AND child_session_id=? AND relation_kind=?`,
		parentID, childID, relationship.Kind).Scan(&resolvedID); err != nil {
		return 0, false, fmt.Errorf("read resolved history relationship: %w", err)
	}
	if unresolvedID != 0 && unresolvedID != resolvedID {
		if _, err := tx.tx.Exec(`INSERT OR IGNORE INTO session_relation_supports(
			relation_id,source_head_id,snapshot_id,parent_native_message_id,provider_native_value,evidence,confidence,rule_version)
			SELECT ?,source_head_id,snapshot_id,parent_native_message_id,provider_native_value,evidence,confidence,rule_version
			FROM session_relation_supports WHERE relation_id=?`, resolvedID, unresolvedID); err != nil {
			return 0, false, fmt.Errorf("move resolved history relationship support: %w", err)
		}
	}
	result, err = tx.tx.Exec(`DELETE FROM session_relations WHERE provider=? AND child_session_id=? AND relation_kind=?
		AND parent_session_id IS NULL AND parent_native_session_id=? AND EXISTS(
			SELECT 1 FROM session_relations resolved WHERE resolved.parent_session_id=?
			AND resolved.child_session_id=? AND resolved.relation_kind=?)`, provider, childID, relationship.Kind,
		relationship.ParentNativeSessionID, parentID, childID, relationship.Kind)
	if err != nil {
		return 0, false, fmt.Errorf("deduplicate resolved history relationship: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("count deduplicated history relationship: %w", err)
	}
	return resolvedID, changed || deleted > 0, nil
}

func (tx *Tx) resolveRelationshipsForParent(sessionID int64) (bool, error) {
	var provider history.Provider
	var nativeID sql.NullString
	if err := tx.tx.QueryRow(`SELECT provider,native_session_id FROM sessions WHERE id=?`, sessionID).Scan(&provider, &nativeID); err != nil {
		return false, fmt.Errorf("read relationship parent session: %w", err)
	}
	if !nativeID.Valid || nativeID.String == "" {
		return false, nil
	}
	rows, err := tx.tx.Query(`SELECT child_session_id,relation_kind,parent_native_message_id,provider_native_value,evidence,confidence,rule_version
		FROM session_relations WHERE provider=? AND parent_session_id IS NULL AND parent_native_session_id=?
		AND EXISTS(SELECT 1 FROM session_relation_supports support WHERE support.relation_id=session_relations.id) ORDER BY id`, provider, nativeID.String)
	if err != nil {
		return false, fmt.Errorf("list deferred history relationships: %w", err)
	}
	type deferred struct {
		childID int64
		value   history.Relationship
	}
	var values []deferred
	for rows.Next() {
		var value deferred
		value.value.ParentNativeSessionID = nativeID.String
		if err := rows.Scan(&value.childID, &value.value.Kind, &value.value.ParentNativeMessageID,
			&value.value.ProviderNativeValue, &value.value.Evidence, &value.value.Confidence, &value.value.RuleVersion); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan deferred history relationship: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	changed := false
	for _, value := range values {
		if value.childID == sessionID {
			continue
		}
		_, resolved, err := tx.resolveRelationship(provider, sessionID, value.childID, value.value)
		if err != nil {
			return false, err
		}
		metadataChanged, err := tx.refreshChildRelationshipMetadata(value.childID)
		if err != nil {
			return false, err
		}
		changed = changed || resolved || metadataChanged
	}
	return changed, nil
}

func (tx *Tx) rehomeSessionRelationships(recipientID, donorID int64) error {
	rows, err := tx.tx.Query(`SELECT DISTINCT child_session_id FROM session_relations WHERE child_session_id=? OR parent_session_id=?`, donorID, donorID)
	if err != nil {
		return fmt.Errorf("list rehomed history relationship children: %w", err)
	}
	affectedChildren := []int64{recipientID}
	for rows.Next() {
		var childID int64
		if err := rows.Scan(&childID); err != nil {
			rows.Close()
			return fmt.Errorf("scan rehomed history relationship child: %w", err)
		}
		if childID != donorID {
			affectedChildren = append(affectedChildren, childID)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.tx.Exec(`UPDATE OR IGNORE session_relations SET child_session_id=? WHERE child_session_id=?`, recipientID, donorID); err != nil {
		return fmt.Errorf("move child history relationships: %w", err)
	}
	if _, err := tx.tx.Exec(`INSERT OR IGNORE INTO session_relation_supports(
		relation_id,source_head_id,snapshot_id,parent_native_message_id,provider_native_value,evidence,confidence,rule_version)
		SELECT target.id,support.source_head_id,support.snapshot_id,support.parent_native_message_id,
			support.provider_native_value,support.evidence,support.confidence,support.rule_version FROM session_relations donor
		JOIN session_relation_supports support ON support.relation_id=donor.id
		JOIN session_relations target ON target.child_session_id=? AND target.relation_kind=donor.relation_kind AND (
			(target.parent_session_id=donor.parent_session_id AND target.parent_session_id IS NOT NULL) OR
			(target.parent_session_id IS NULL AND donor.parent_session_id IS NULL AND target.provider=donor.provider
			 AND target.parent_native_session_id=donor.parent_native_session_id))
		WHERE donor.child_session_id=?`, recipientID, donorID); err != nil {
		return fmt.Errorf("merge child history relationship support: %w", err)
	}
	if _, err := tx.tx.Exec(`DELETE FROM session_relations WHERE child_session_id=?`, donorID); err != nil {
		return fmt.Errorf("remove duplicate child history relationships: %w", err)
	}
	if _, err := tx.tx.Exec(`UPDATE OR IGNORE session_relations SET parent_session_id=? WHERE parent_session_id=?`, recipientID, donorID); err != nil {
		return fmt.Errorf("move parent history relationships: %w", err)
	}
	if _, err := tx.tx.Exec(`INSERT OR IGNORE INTO session_relation_supports(
		relation_id,source_head_id,snapshot_id,parent_native_message_id,provider_native_value,evidence,confidence,rule_version)
		SELECT target.id,support.source_head_id,support.snapshot_id,support.parent_native_message_id,
			support.provider_native_value,support.evidence,support.confidence,support.rule_version FROM session_relations donor
		JOIN session_relation_supports support ON support.relation_id=donor.id
		JOIN session_relations target ON target.parent_session_id=? AND target.child_session_id=donor.child_session_id
			AND target.relation_kind=donor.relation_kind WHERE donor.parent_session_id=?`, recipientID, donorID); err != nil {
		return fmt.Errorf("merge parent history relationship support: %w", err)
	}
	if _, err := tx.tx.Exec(`DELETE FROM session_relations WHERE parent_session_id=?`, donorID); err != nil {
		return fmt.Errorf("remove duplicate parent history relationships: %w", err)
	}
	if _, err := tx.tx.Exec(`DELETE FROM session_relations WHERE parent_session_id=child_session_id`); err != nil {
		return fmt.Errorf("remove self-referential history relationships: %w", err)
	}
	for _, childID := range affectedChildren {
		if _, err := tx.refreshChildRelationshipMetadata(childID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) sessionRelationships(sessionID int64) ([]SessionRelationship, bool, error) {
	rows, err := s.db.Query(`SELECT r.relation_kind,parent.public_id,child.public_id,r.parent_native_session_id,
		r.parent_native_message_id,r.provider_native_value,r.evidence,r.confidence,r.rule_version,r.resolution_state
		FROM session_relations r JOIN sessions child ON child.id=r.child_session_id
			LEFT JOIN sessions parent ON parent.id=r.parent_session_id WHERE r.child_session_id=?
			AND EXISTS(SELECT 1 FROM session_relation_supports support WHERE support.relation_id=r.id)
			ORDER BY r.relation_kind,COALESCE(parent.public_id,''),r.parent_native_session_id LIMIT ?`, sessionID, maxSessionRelationships+1)
	if err != nil {
		return nil, false, fmt.Errorf("list history relationships: %w", err)
	}
	defer rows.Close()
	result := []SessionRelationship{}
	for rows.Next() {
		var value SessionRelationship
		var parent sql.NullString
		if err := rows.Scan(&value.RelationKind, &parent, &value.ChildSessionID, &value.ParentNativeSessionID,
			&value.ParentNativeMessageID, &value.ProviderNativeValue, &value.Evidence, &value.Confidence,
			&value.RuleVersion, &value.ResolutionState); err != nil {
			return nil, false, fmt.Errorf("scan history relationship: %w", err)
		}
		value.ParentSessionID = optionalCatalogString(parent)
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(result) > maxSessionRelationships
	if truncated {
		result = result[:maxSessionRelationships]
	}
	return result, truncated, nil
}
