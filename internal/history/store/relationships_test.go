package store

import (
	"fmt"
	"strings"
	"testing"

	"github.com/janiorvalle/tokenomnom/internal/history"
)

func TestRelationshipChildBeforeParentResolvesAndInvalidatesCursor(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()

	childSource := sourceRef("/provider/child.jsonl", history.LocationProviderLive)
	child := extraction("native:child", "child", childSource, prompt("native:p", "p", "delegated prompt", 1))
	child.Session.ThreadKind = history.ThreadSubagent
	child.Session.ThreadEvidence = "session_meta.source.subagent"
	child.Session.ThreadConfidence = history.ConfidenceExact
	child.Session.ThreadRuleVersion = history.RelationshipRuleVersion
	child.Relationships = []history.Relationship{{
		Kind: history.RelationSubagent, ParentNativeSessionID: "parent", ProviderNativeValue: "parent",
		Evidence: "session_meta.source.subagent", Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion,
	}}
	childResult, err := database.ApplySource(child, head(childSource, "child-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	before, err := database.GetSession(childResult.SessionID)
	if err != nil || len(before.Relationships) != 1 || before.Relationships[0].ResolutionState != history.ResolutionUnresolved || before.Relationships[0].ParentSessionID != nil {
		t.Fatalf("unresolved child err=%v session=%+v", err, before)
	}
	fillerSource := sourceRef("/provider/filler.jsonl", history.LocationProviderLive)
	if _, err := database.ApplySource(extraction("native:filler", "filler", fillerSource, prompt("native:p", "p", "filler prompt", 1)), head(fillerSource, "filler-hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Limit: 1})
	if err != nil || page.NextCursor == "" {
		t.Fatalf("cursor fixture err=%v page=%+v", err, page)
	}

	parentSource := sourceRef("/provider/parent.jsonl", history.LocationProviderLive)
	parent := extraction("native:parent", "parent", parentSource, prompt("native:p", "p", "root prompt", 1))
	parent.Session.ThreadKind = history.ThreadRoot
	parent.Session.ThreadEvidence = "session_meta.thread_source=user"
	parent.Session.ThreadConfidence = history.ConfidenceExact
	parent.Session.ThreadRuleVersion = history.RelationshipRuleVersion
	parentResult, err := database.ApplySourceWithGeneration(parent, head(parentSource, "parent-hash", 10, 1), ApplyReplace, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := database.GetSession(childResult.SessionID)
	if err != nil || len(after.Relationships) != 1 || after.Relationships[0].ResolutionState != history.ResolutionResolved ||
		after.Relationships[0].ParentSessionID == nil || *after.Relationships[0].ParentSessionID != parentResult.SessionID {
		t.Fatalf("resolved child err=%v session=%+v parent=%+v", err, after, parentResult)
	}
	if _, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, Cursor: page.NextCursor}); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("relationship resolution cursor error=%v", err)
	}
	if _, err := database.db.Exec(`DELETE FROM sessions WHERE public_id=?`, parentResult.SessionID); err != nil {
		t.Fatal(err)
	}
	var parentID any
	var state history.ResolutionState
	if err := database.db.QueryRow(`SELECT parent_session_id,resolution_state FROM session_relations`).Scan(&parentID, &state); err != nil || parentID != nil || state != history.ResolutionUnresolved {
		t.Fatalf("deleted parent state parent=%v state=%q err=%v", parentID, state, err)
	}
}

func TestRelationshipDuplicatePreventionAndUnknownParentVisibility(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/fork.jsonl", history.LocationProviderLive)
	value := extraction("native:fork", "fork", source, prompt("native:p", "p", "fork prompt", 1))
	value.Relationships = []history.Relationship{{
		Kind: history.RelationFork, ParentNativeSessionID: "missing", ParentNativeMessageID: "message-1",
		ProviderNativeValue: "missing", Evidence: "forkedFrom.sessionId", Confidence: history.ConfidenceExact,
		RuleVersion: history.RelationshipRuleVersion,
	}}
	result, err := database.ApplySource(value, head(source, "fork-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	beforeRetry, err := database.Health()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApplySourceWithGeneration(value, head(source, "fork-hash", 10, 1), ApplyReplace, false); err != nil {
		t.Fatal(err)
	}
	afterRetry, err := database.Health()
	if err != nil || afterRetry.IndexGeneration != beforeRetry.IndexGeneration {
		t.Fatalf("idempotent relationship retry changed generation: before=%+v after=%+v err=%v", beforeRetry, afterRetry, err)
	}
	var count int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM session_relations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("relationship count=%d err=%v", count, err)
	}
	session, err := database.GetSession(result.SessionID)
	if err != nil || session.Relationships[0].ParentNativeSessionID != "missing" || session.Relationships[0].ParentNativeMessageID != "message-1" {
		t.Fatalf("unresolved relationship err=%v session=%+v", err, session)
	}
}

func TestRelationshipReplaceRemovesSupersededAndAbsentEdges(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/rewritten-fork.jsonl", history.LocationProviderLive)
	value := extraction("native:rewritten-fork", "rewritten-fork", source, prompt("native:p", "p", "rewritten fork", 1))
	value.Relationships = []history.Relationship{{
		Kind: history.RelationFork, ParentNativeSessionID: "parent-a", ProviderNativeValue: "parent-a",
		Evidence: "session_meta.forked_from_id", Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion,
	}}
	if _, err := database.ApplySource(value, head(source, "hash-a", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	value.Relationships[0].ParentNativeSessionID = "parent-b"
	value.Relationships[0].ProviderNativeValue = "parent-b"
	if _, err := database.ApplySource(value, head(source, "hash-b", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	var count int
	var parent string
	if err := database.db.QueryRow(`SELECT COUNT(*),COALESCE(MAX(parent_native_session_id),'') FROM session_relations`).Scan(&count, &parent); err != nil || count != 1 || parent != "parent-b" {
		t.Fatalf("superseded relationship count=%d parent=%q err=%v", count, parent, err)
	}
	value.Relationships = nil
	if _, err := database.ApplySource(value, head(source, "hash-none", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM session_relations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("absent relationship count=%d err=%v", count, err)
	}
}

func TestRelationshipReplacePreservesOtherSourceSupport(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	relationship := history.Relationship{
		Kind: history.RelationSubagent, ParentNativeSessionID: "shared-parent", ProviderNativeValue: "shared-parent",
		Evidence: "session_meta.source.subagent", Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion,
	}
	sources := []history.SourceReference{
		sourceRef("/provider/shared-live.jsonl", history.LocationProviderLive),
		sourceRef("/provider/shared-archive.jsonl", history.LocationProviderArchive),
	}
	for index, source := range sources {
		value := extraction("native:shared-child", "shared-child", source, prompt("native:p", "p", "shared relation", 1))
		if index == 0 {
			value.Session.ThreadKind = history.ThreadSubagent
			value.Session.ThreadEvidence = relationship.Evidence
			value.Session.ThreadConfidence = history.ConfidenceExact
			value.Session.ThreadRuleVersion = history.RelationshipRuleVersion
		} else {
			value.Session.ThreadKind = history.ThreadUnknown
			value.Session.ThreadEvidence = ""
			value.Session.ThreadConfidence = history.ConfidenceUnknown
			value.Session.ThreadRuleVersion = 0
		}
		value.Relationships = []history.Relationship{relationship}
		if _, err := database.ApplySource(value, head(source, fmt.Sprintf("shared-%d", index), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	withoutRelationship := extraction("native:shared-child", "shared-child", sources[1], prompt("native:p", "p", "shared relation", 1))
	withoutRelationship.Session.ThreadKind = history.ThreadUnknown
	withoutRelationship.Session.ThreadEvidence = ""
	withoutRelationship.Session.ThreadConfidence = history.ConfidenceUnknown
	withoutRelationship.Session.ThreadRuleVersion = 0
	if _, err := database.ApplySource(withoutRelationship, head(sources[1], "shared-rewrite", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	var relations, supports int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM session_relations`).Scan(&relations); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM session_relation_supports`).Scan(&supports); err != nil || relations != 1 || supports != 1 {
		t.Fatalf("cross-source support relations=%d supports=%d err=%v", relations, supports, err)
	}
	session, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny, ThreadKind: "subagent"})
	if err != nil || len(session.Sessions) != 1 || session.Sessions[0].ThreadKind != history.ThreadSubagent {
		t.Fatalf("cross-source thread support page=%+v err=%v", session, err)
	}
}

func TestRelationshipEvidenceFollowsRemainingSupport(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	sources := []history.SourceReference{
		sourceRef("/provider/evidence-exact.jsonl", history.LocationProviderLive),
		sourceRef("/provider/evidence-derived.jsonl", history.LocationProviderArchive),
	}
	for index, source := range sources {
		confidence, evidence := history.ConfidenceExact, "exact-evidence"
		if index == 1 {
			confidence, evidence = history.ConfidenceDerived, "derived-evidence"
		}
		value := extraction("native:evidence-child", "evidence-child", source, prompt("native:p", "p", "evidence relation", 1))
		value.Relationships = []history.Relationship{{
			Kind: history.RelationFork, ParentNativeSessionID: "evidence-parent", ProviderNativeValue: evidence,
			Evidence: evidence, Confidence: confidence, RuleVersion: history.RelationshipRuleVersion,
		}}
		if _, err := database.ApplySource(value, head(source, fmt.Sprintf("evidence-%d", index), 10, 1), ApplyReplace); err != nil {
			t.Fatal(err)
		}
	}
	page, err := database.ListCatalog(CatalogQuery{Source: CatalogSourceAny})
	if err != nil || page.Sessions[0].Relationships[0].Evidence != "exact-evidence" {
		t.Fatalf("canonical exact relationship page=%+v err=%v", page, err)
	}
	replacement := extraction("native:evidence-child", "evidence-child", sources[0], prompt("native:p", "p", "evidence relation", 1))
	if _, err := database.ApplySource(replacement, head(sources[0], "evidence-removed", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	page, err = database.ListCatalog(CatalogQuery{Source: CatalogSourceAny})
	if err != nil || page.Sessions[0].Relationships[0].Evidence != "derived-evidence" || page.Sessions[0].Relationships[0].Confidence != history.ConfidenceDerived {
		t.Fatalf("remaining relationship support page=%+v err=%v", page, err)
	}
}

func TestRelationshipReplaceCorrectsSubagentToRoot(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/corrected-thread.jsonl", history.LocationProviderLive)
	value := extraction("native:corrected-thread", "corrected-thread", source, prompt("native:p", "p", "corrected thread", 1))
	value.Session.ThreadKind = history.ThreadSubagent
	value.Session.ThreadEvidence = "session_meta.source.subagent"
	value.Session.ThreadConfidence = history.ConfidenceExact
	value.Session.ThreadRuleVersion = history.RelationshipRuleVersion
	value.Relationships = []history.Relationship{{
		Kind: history.RelationSubagent, ParentNativeSessionID: "old-parent", ProviderNativeValue: "old-parent",
		Evidence: value.Session.ThreadEvidence, Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion,
	}}
	result, err := database.ApplySource(value, head(source, "subagent", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	value.Session.ThreadKind = history.ThreadRoot
	value.Session.ThreadEvidence = "session_meta.thread_source=user"
	value.Session.ParentNativeSessionID = ""
	value.Relationships = nil
	if _, err := database.ApplySourceWithGeneration(value, head(source, "root", 10, 1), ApplyReplace, false); err != nil {
		t.Fatal(err)
	}
	session, err := database.GetSession(result.SessionID)
	if err != nil || session.ThreadKind != history.ThreadRoot || len(session.Relationships) != 0 {
		t.Fatalf("corrected relationship session=%+v err=%v", session, err)
	}
}

func TestRelationshipOutputIsBounded(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/bounded-relations.jsonl", history.LocationProviderLive)
	value := extraction("native:bounded-relations", "bounded-relations", source, prompt("native:p", "p", "bounded relations", 1))
	for index := 0; index < maxSessionRelationships+1; index++ {
		parent := fmt.Sprintf("parent-%02d", index)
		value.Relationships = append(value.Relationships, history.Relationship{
			Kind: history.RelationFork, ParentNativeSessionID: parent, ProviderNativeValue: parent,
			Evidence: "fixture", Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion,
		})
	}
	result, err := database.ApplySource(value, head(source, "bounded", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}
	session, err := database.GetSession(result.SessionID)
	if err != nil || len(session.Relationships) != maxSessionRelationships || !session.RelationshipsTruncated {
		t.Fatalf("bounded relationships session=%+v err=%v", session, err)
	}
}

func TestCodexSourceAliasIsolationDoesNotCreateConversationalEdge(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()
	source := sourceRef("/provider/alias.jsonl", history.LocationProviderLive)
	value := extraction("native:alias", "alias", source, prompt("native:p", "p", "physical copy", 1))
	value.Session.ParentNativeSessionID = "physical-owner"
	if _, err := database.ApplySource(value, head(source, "same-bytes", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM session_relations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("physical alias created %d relationship(s), err=%v", count, err)
	}
}

func TestRehomeSessionRelationshipsPreservesEdgesSupportsAndResolution(t *testing.T) {
	database := openTestStore(t)
	defer database.Close()

	parentSource := sourceRef("/provider/merge-parent.jsonl", history.LocationProviderLive)
	parent := extraction("native:merge-parent", "merge-parent", parentSource, prompt("native:p", "p", "parent", 1))
	if _, err := database.ApplySource(parent, head(parentSource, "parent-hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	relations := []history.Relationship{
		{Kind: history.RelationFork, ParentNativeSessionID: "merge-parent", ProviderNativeValue: "merge-parent", Evidence: "resolved", Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion},
		{Kind: history.RelationSubagent, ParentNativeSessionID: "missing-parent", ProviderNativeValue: "missing-parent", Evidence: "unresolved", Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion},
	}
	donorSource := sourceRef("/provider/merge-donor.jsonl", history.LocationProviderLive)
	donor := extraction("native:merge-target", "merge-target", donorSource, prompt("native:p", "p", "donor", 1))
	donor.Relationships = relations
	if _, err := database.ApplySource(donor, head(donorSource, "donor-hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}

	recipientSource := sourceRef("/provider/merge-recipient.jsonl", history.LocationProviderLive)
	recipient := extraction("fallback:source-path:/provider/merge-recipient.jsonl", "", recipientSource, prompt("native:p", "p", "recipient", 1))
	recipient.Relationships = relations
	if _, err := database.ApplySource(recipient, head(recipientSource, "recipient-hash", 10, 1), ApplyReplace); err != nil {
		t.Fatal(err)
	}
	promoted := recipient
	promoted.Session.IdentityKey = "native:merge-target"
	promoted.Session.NativeSessionID = "merge-target"
	result, err := database.ApplySource(promoted, head(recipientSource, "recipient-hash", 10, 1), ApplyReplace)
	if err != nil {
		t.Fatal(err)
	}

	var sessions, edges, supports, resolved, unresolved int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE native_session_id='merge-target'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT COUNT(*),SUM(resolution_state='resolved'),SUM(resolution_state='unresolved') FROM session_relations`).Scan(&edges, &resolved, &unresolved); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM session_relation_supports`).Scan(&supports); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || edges != 2 || supports != 4 || resolved != 1 || unresolved != 1 {
		t.Fatalf("merge relationships sessions=%d edges=%d supports=%d resolved=%d unresolved=%d", sessions, edges, supports, resolved, unresolved)
	}
	session, err := database.GetSession(result.SessionID)
	if err != nil || len(session.Relationships) != 2 {
		t.Fatalf("merged session=%+v err=%v", session, err)
	}
}
