// Package history defines provider-neutral transcript history records.
package history

import "time"

const (
	// ExtractorVersion changes when normalized history semantics change.
	ExtractorVersion = 3
	// RelationshipRuleVersion identifies the deterministic provider rules used
	// to classify threads and extract conversational relationships.
	RelationshipRuleVersion = 1
	// MaxPromptBytes is the largest prompt indexed as complete text.
	MaxPromptBytes = 1 << 20
)

// Provider identifies the source transcript format.
type Provider string

const (
	ProviderCodex  Provider = "codex"
	ProviderClaude Provider = "claude"
)

// LocationKind identifies where exact transcript bytes can be read.
type LocationKind string

const (
	LocationProviderLive    LocationKind = "provider_live"
	LocationProviderArchive LocationKind = "provider_archive"
	LocationVault           LocationKind = "vault"
)

// Role is the normalized message role.
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleTool      Role = "tool"
	RoleUnknown   Role = "unknown"
)

// Classification explains why a record is or is not a searchable prompt.
type Classification string

const (
	ClassificationHuman            Classification = "human"
	ClassificationAssistant        Classification = "assistant"
	ClassificationSystemInjected   Classification = "system_injected"
	ClassificationToolResult       Classification = "tool_result"
	ClassificationLocalCommand     Classification = "local_command"
	ClassificationAgentInstruction Classification = "agent_instruction"
	ClassificationProviderMetadata Classification = "provider_metadata"
	ClassificationUnknown          Classification = "unknown"
)

// ExtractionOptions controls explicitly consented searchable roles.
type ExtractionOptions struct {
	IndexAssistant bool
}

// Confidence describes how directly a normalized value is supported.
type Confidence string

const (
	ConfidenceExact   Confidence = "exact"
	ConfidenceDerived Confidence = "derived"
	ConfidenceUnknown Confidence = "unknown"
)

// ThreadKind records a provider-proven conversation relationship.
type ThreadKind string

const (
	ThreadRoot     ThreadKind = "root"
	ThreadSubagent ThreadKind = "subagent"
	ThreadUnknown  ThreadKind = "unknown"
)

// RelationKind identifies a provider-evidenced conversational relationship.
type RelationKind string

const (
	RelationSubagent RelationKind = "subagent"
	RelationFork     RelationKind = "fork"
)

// ResolutionState reports whether a provider-native parent has been matched
// to an indexed logical session.
type ResolutionState string

const (
	ResolutionResolved   ResolutionState = "resolved"
	ResolutionUnresolved ResolutionState = "unresolved"
)

// SourceReference identifies a provider file or immutable vault member.
type SourceReference struct {
	Provider     Provider
	Kind         LocationKind
	Path         string
	RelativePath string
	Archive      string
	VaultVersion int
}

// Session is provider-neutral logical session metadata. IdentityKey is a
// normalized private reconciliation key, not a public identifier.
type Session struct {
	IdentityKey           string
	NativeSessionID       string
	FallbackKey           string
	CWD                   string
	RepositoryRoot        string
	RepositoryName        string
	RepositoryIdentity    string
	Branch                string
	ThreadKind            ThreadKind
	ThreadEvidence        string
	ThreadConfidence      Confidence
	ThreadRuleVersion     int
	ParentNativeSessionID string
	ForkedFromSessionID   string
	ForkedFromMessageID   string
	Originator            string
	Evidence              string
	Confidence            Confidence
	FirstTimestamp        *time.Time
	LastTimestamp         *time.Time
}

// Relationship is one provider-native edge emitted by an extractor. The
// child is the extraction's logical session and is assigned by the store.
type Relationship struct {
	Kind                  RelationKind
	ParentNativeSessionID string
	ParentNativeMessageID string
	ProviderNativeValue   string
	Evidence              string
	Confidence            Confidence
	RuleVersion           int
}

// SourceHead is the mutable current state of a provider transcript.
type SourceHead struct {
	Source            SourceReference
	ContentSHA256     string
	ContentHashState  string
	PrefixFingerprint string
	TailFingerprint   string
	ExtractorState    string
	Size              int64
	ModTimeUnix       int64
	CompleteOffset    int64
	LineCount         int64
	Available         bool
	// VerifiedContinuity permits identity promotion during a full reindex when
	// the caller has proven this head evolves the previously indexed source.
	VerifiedContinuity bool
}

// PreservedSnapshot is immutable exact content backed by a durable location.
type PreservedSnapshot struct {
	Provider      Provider
	ContentSHA256 string
	Size          int64
	FirstTS       *time.Time
	LastTS        *time.Time
}

// Prompt is one logical normalized message. Searchable is deliberately
// explicit so oversized or non-human text cannot enter FTS by accident.
type Prompt struct {
	LogicalKey            string
	NativeMessageID       string
	ParentNativeMessageID string
	Role                  Role
	CleanText             string
	Classification        Classification
	Searchable            bool
	Oversized             bool
	Timestamp             *time.Time
	Model                 string
	Evidence              string
	Confidence            Confidence
}

// Occurrence proves where one logical prompt appears in exact source bytes.
type Occurrence struct {
	PromptKey   string
	Variant     Prompt
	LineNumber  int64
	StartOffset int64
	EndOffset   int64
}

// Diagnostic records a non-fatal extraction decision.
type Diagnostic struct {
	LineNumber     int64
	Classification Classification
	Message        string
}

// Extraction is the complete provider-neutral result for one source read.
type Extraction struct {
	Provider      Provider
	Source        SourceReference
	Session       Session
	Relationships []Relationship
	Prompts       []Prompt
	Occurrences   []Occurrence
	Diagnostics   []Diagnostic
}

// CanonicalPromptWins applies the provider-neutral ordering used when more
// than one record represents the same native message.
func CanonicalPromptWins(candidate, existing Prompt) bool {
	if canonicalPromptSemanticRank(candidate) != canonicalPromptSemanticRank(existing) {
		return canonicalPromptSemanticRank(candidate) > canonicalPromptSemanticRank(existing)
	}
	switch {
	case candidate.Timestamp != nil && existing.Timestamp == nil:
		return true
	case candidate.Timestamp == nil && existing.Timestamp != nil:
		return false
	case candidate.Timestamp != nil && existing.Timestamp != nil:
		if candidate.Timestamp.After(*existing.Timestamp) {
			return true
		}
		if candidate.Timestamp.Before(*existing.Timestamp) {
			return false
		}
	}
	if len(candidate.CleanText) != len(existing.CleanText) {
		return len(candidate.CleanText) > len(existing.CleanText)
	}
	return candidate.CleanText > existing.CleanText
}

func canonicalPromptSemanticRank(prompt Prompt) int {
	if prompt.Classification == ClassificationProviderMetadata {
		return 0
	}
	return 1
}
