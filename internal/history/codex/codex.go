// Package codex extracts provider-neutral history from Codex rollout JSONL.
package codex

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMeta struct {
	SessionID      string          `json:"session_id"`
	ID             string          `json:"id"`
	ForkedFromID   string          `json:"forked_from_id"`
	ParentThreadID string          `json:"parent_thread_id"`
	CWD            string          `json:"cwd"`
	Originator     string          `json:"originator"`
	ThreadSource   string          `json:"thread_source"`
	Timestamp      string          `json:"timestamp"`
	Source         json.RawMessage `json:"source"`
	Git            *struct {
		Branch        string `json:"branch"`
		RepositoryURL string `json:"repository_url"`
	} `json:"git"`
}

type eventMessage struct {
	Type     string `json:"type"`
	ClientID string `json:"client_id"`
	Message  string `json:"message"`
}

type responseItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// State carries session identity and a possible paired response representation
// across positioned incremental reads.
type State struct {
	Session         history.Session
	PendingResponse PendingResponse
	Stopped         bool
}

// PendingResponse identifies the immediately preceding user response item.
type PendingResponse struct {
	Signature  string
	LogicalKey string
	NativeID   string
	LineNumber int64
	Timestamp  string
}

// Extract normalizes complete positioned rollout records without DB writes.
func Extract(source history.SourceReference, records []jsonl.Record) history.Extraction {
	result, _ := ExtractWithStateOptions(source, records, State{}, history.ExtractionOptions{})
	return result
}

// ExtractWithOptions normalizes records using explicitly consented roles.
func ExtractWithOptions(source history.SourceReference, records []jsonl.Record, options history.ExtractionOptions) history.Extraction {
	result, _ := ExtractWithStateOptions(source, records, State{}, options)
	return result
}

// ExtractWithSession carries a previously established session identity across
// a positioned suffix read where session_meta is no longer present.
func ExtractWithSession(source history.SourceReference, records []jsonl.Record, prior history.Session) history.Extraction {
	result, _ := ExtractWithStateOptions(source, records, State{Session: prior}, history.ExtractionOptions{})
	return result
}

// ExtractWithState preserves provider pairing state across a suffix read.
func ExtractWithState(source history.SourceReference, records []jsonl.Record, state State) (history.Extraction, State) {
	return ExtractWithStateOptions(source, records, state, history.ExtractionOptions{})
}

// ExtractWithStateOptions preserves state while applying explicit role consent.
func ExtractWithStateOptions(source history.SourceReference, records []jsonl.Record, state State, options history.ExtractionOptions) (history.Extraction, State) {
	result := history.Extraction{Provider: history.ProviderCodex, Source: source, Session: state.Session}
	if result.Session.ThreadKind == "" {
		result.Session.ThreadKind = history.ThreadUnknown
	}
	if result.Session.ThreadConfidence == "" {
		result.Session.ThreadConfidence = history.ConfidenceUnknown
	}
	if result.Session.Confidence == "" {
		result.Session.Confidence = history.ConfidenceUnknown
	}
	if state.Stopped {
		result.Relationships = sessionRelationships(result.Session)
		return result, state
	}
	var firstRecord []byte
	seen := make(map[string]int)
	foundSessionMeta := false

recordsLoop:
	for _, record := range records {
		if len(firstRecord) == 0 {
			firstRecord = append([]byte(nil), record.Raw...)
		}
		var item envelope
		if err := json.Unmarshal(record.Raw, &item); err != nil {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "malformed JSON"})
			continue
		}
		if item.Type != "session_meta" {
			updateRange(&result.Session, parseTime(item.Timestamp))
		}
		switch item.Type {
		case "session_meta":
			var meta sessionMeta
			if json.Unmarshal(item.Payload, &meta) != nil {
				result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "malformed session_meta payload excluded"})
				continue
			}
			nativeSessionID := firstNonEmpty(meta.SessionID, meta.ID)
			if nativeSessionID != "" && result.Session.NativeSessionID != "" && result.Session.NativeSessionID != nativeSessionID {
				result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "conflicting session_meta record excluded"})
				state.Stopped = true
				break recordsLoop
			}
			updateRange(&result.Session, parseTime(item.Timestamp))
			if result.Session.NativeSessionID == "" {
				result.Session.NativeSessionID = nativeSessionID
			}
			foundSessionMeta = foundSessionMeta || nativeSessionID != ""
			if meta.CWD != "" {
				result.Session.CWD = meta.CWD
			}
			if meta.Originator != "" {
				result.Session.Originator = meta.Originator
			}
			if meta.ForkedFromID != "" {
				result.Session.ForkedFromSessionID = meta.ForkedFromID
			}
			if meta.ThreadSource == "user" {
				result.Session.ThreadKind = history.ThreadRoot
				result.Session.ThreadEvidence = "session_meta.thread_source=user"
				result.Session.ThreadConfidence = history.ConfidenceExact
				result.Session.ThreadRuleVersion = history.RelationshipRuleVersion
			}
			if meta.ThreadSource == "subagent" || isSubagentSource(meta.Source) {
				result.Session.ThreadKind = history.ThreadSubagent
				result.Session.ParentNativeSessionID = meta.ParentThreadID
				result.Session.ThreadEvidence = "session_meta.thread_source=subagent"
				if isSubagentSource(meta.Source) {
					result.Session.ThreadEvidence = "session_meta.source.subagent"
				}
				result.Session.ThreadConfidence = history.ConfidenceExact
				result.Session.ThreadRuleVersion = history.RelationshipRuleVersion
			}
			if meta.Git != nil {
				if meta.Git.Branch != "" {
					result.Session.Branch = meta.Git.Branch
				}
				if meta.Git.RepositoryURL != "" {
					result.Session.RepositoryIdentity, result.Session.RepositoryName = history.DeriveRepository(meta.Git.RepositoryURL)
					if result.Session.RepositoryIdentity != "" {
						result.Session.RepositoryRuleVersion = history.RepositoryRuleVersion
					}
				}
			}
			updateRange(&result.Session, parseTime(firstNonEmpty(meta.Timestamp, item.Timestamp)))
			result.Session.Evidence = "session_meta"
			if nativeSessionID != "" {
				result.Session.Confidence = history.ConfidenceExact
			} else {
				result.Session.Confidence = history.ConfidenceDerived
			}
		case "event_msg":
			var event eventMessage
			if json.Unmarshal(item.Payload, &event) != nil {
				result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "malformed event_msg payload excluded"})
				continue
			}
			if event.Type != "user_message" {
				continue
			}
			clean, _, _, _ := history.CleanHumanText(event.Message)
			logicalKey := ""
			nativeID := ""
			lineDistance := record.LineNumber - state.PendingResponse.LineNumber
			if clean != "" && pairingSignature(clean) == state.PendingResponse.Signature && lineDistance > 0 && lineDistance <= 4 && pairedTimestamps(state.PendingResponse.Timestamp, item.Timestamp) {
				logicalKey = state.PendingResponse.LogicalKey
				if state.PendingResponse.NativeID != "" {
					nativeID = state.PendingResponse.NativeID
				}
				state.PendingResponse = PendingResponse{}
			}
			state.PendingResponse = PendingResponse{}
			addPrompt(&result, seen, record, logicalKey, nativeID, history.RoleUser, event.Message, item.Timestamp, "event_msg.user_message", true)
		case "response_item":
			var response responseItem
			if json.Unmarshal(item.Payload, &response) != nil {
				result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "malformed response_item payload excluded"})
				continue
			}
			if response.Type == "message" && response.Role == "assistant" && options.IndexAssistant {
				var text []string
				for _, block := range response.Content {
					if block.Type == "output_text" && block.Text != "" {
						text = append(text, block.Text)
					}
				}
				addPrompt(&result, seen, record, "", response.ID, history.RoleAssistant, strings.Join(text, "\n"), item.Timestamp, "response_item.message.output_text", true)
				continue
			}
			if response.Type != "message" || response.Role != "user" {
				classification := history.ClassificationProviderMetadata
				if strings.Contains(response.Type, "output") {
					classification = history.ClassificationToolResult
				}
				result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: classification, Message: "non-user response item excluded"})
				continue
			}
			var text []string
			for _, block := range response.Content {
				if block.Type == "input_text" && block.Text != "" {
					text = append(text, block.Text)
				}
			}
			prompt, ok := addPrompt(&result, seen, record, "", response.ID, history.RoleUser, strings.Join(text, "\n"), item.Timestamp, "response_item.message", false)
			if ok {
				state.PendingResponse = PendingResponse{
					Signature:  pairingSignature(prompt.CleanText),
					LogicalKey: prompt.LogicalKey,
					NativeID:   prompt.NativeMessageID,
					LineNumber: record.LineNumber,
					Timestamp:  item.Timestamp,
				}
			}
		}
	}

	if result.Session.IdentityKey == "" || foundSessionMeta || (strings.HasPrefix(result.Session.FallbackKey, "source-path:") && len(firstRecord) > 0) {
		result.Session.IdentityKey, result.Session.FallbackKey = history.SessionIdentityKey(history.ProviderCodex, result.Session.NativeSessionID, source.Path, firstRecord)
	}
	result.Relationships = sessionRelationships(result.Session)
	state.Session = result.Session
	return result, state
}

func sessionRelationships(session history.Session) []history.Relationship {
	relationships := []history.Relationship{}
	if session.ThreadKind == history.ThreadSubagent && session.ParentNativeSessionID != "" {
		relationships = append(relationships, history.Relationship{
			Kind: history.RelationSubagent, ParentNativeSessionID: session.ParentNativeSessionID,
			ProviderNativeValue: session.ParentNativeSessionID, Evidence: session.ThreadEvidence,
			Confidence: session.ThreadConfidence, RuleVersion: history.RelationshipRuleVersion,
		})
	}
	if session.ForkedFromSessionID != "" {
		relationships = append(relationships, history.Relationship{
			Kind: history.RelationFork, ParentNativeSessionID: session.ForkedFromSessionID,
			ProviderNativeValue: session.ForkedFromSessionID, Evidence: "session_meta.forked_from_id",
			Confidence: history.ConfidenceExact, RuleVersion: history.RelationshipRuleVersion,
		})
	}
	return relationships
}

func pairingSignature(cleanText string) string {
	return history.MessageIdentityKey("", 0, cleanText)
}

func pairedTimestamps(left, right string) bool {
	leftTime, rightTime := parseTime(left), parseTime(right)
	if leftTime == nil || rightTime == nil {
		return left != "" && left == right
	}
	return leftTime.Equal(*rightTime)
}

func isSubagentSource(raw json.RawMessage) bool {
	var source map[string]json.RawMessage
	if json.Unmarshal(raw, &source) != nil {
		return false
	}
	_, ok := source["subagent"]
	return ok
}

func addPrompt(result *history.Extraction, seen map[string]int, record jsonl.Record, logicalKey, nativeID string, role history.Role, value, timestamp, evidence string, confirmedHuman bool) (history.Prompt, bool) {
	clean, classification, searchable, oversized := history.CleanHumanText(value)
	if clean == "" {
		result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: classification, Message: "empty searchable text excluded"})
		return history.Prompt{}, false
	}
	if role == history.RoleAssistant && classification == history.ClassificationHuman {
		classification = history.ClassificationAssistant
	}
	if !confirmedHuman {
		classification = history.ClassificationProviderMetadata
		searchable = false
	}
	key := logicalKey
	if key == "" {
		if evidence == "response_item.message" && nativeID == "" {
			key = history.TimestampedMessageIdentityKey(timestamp, record.LineNumber, clean)
		} else {
			key = history.MessageIdentityKey(nativeID, record.LineNumber, clean)
		}
	}
	prompt := history.Prompt{LogicalKey: key, NativeMessageID: nativeID, Role: role, CleanText: clean, Classification: classification, PromptKind: history.ClassifyPromptKind(clean, role, classification), Searchable: searchable, Oversized: oversized, Timestamp: parseTime(timestamp), Evidence: evidence, Confidence: history.ConfidenceExact}
	if index, ok := seen[key]; ok {
		if prompt.NativeMessageID == "" {
			prompt.NativeMessageID = result.Prompts[index].NativeMessageID
		}
		if (confirmedHuman && !result.Prompts[index].Searchable) || history.CanonicalPromptWins(prompt, result.Prompts[index]) {
			result.Prompts[index] = prompt
		}
	} else {
		seen[key] = len(result.Prompts)
		result.Prompts = append(result.Prompts, prompt)
	}
	result.Occurrences = append(result.Occurrences, history.Occurrence{PromptKey: key, Variant: prompt, LineNumber: record.LineNumber, StartOffset: record.StartOffset, EndOffset: record.EndOffset})
	return prompt, true
}

func parseTime(value string) *time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func updateRange(session *history.Session, value *time.Time) {
	if value == nil {
		return
	}
	if session.FirstTimestamp == nil || value.Before(*session.FirstTimestamp) {
		copy := *value
		session.FirstTimestamp = &copy
	}
	if session.LastTimestamp == nil || value.After(*session.LastTimestamp) {
		copy := *value
		session.LastTimestamp = &copy
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
