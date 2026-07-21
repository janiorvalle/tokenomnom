// Package claude extracts provider-neutral history from Claude Code JSONL.
package claude

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

type envelope struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	CWD         string          `json:"cwd"`
	Timestamp   string          `json:"timestamp"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	Compact     bool            `json:"isCompactSummary"`
	Message     json.RawMessage `json:"message"`
	ForkedFrom  *struct {
		SessionID   string `json:"sessionId"`
		MessageUUID string `json:"messageUuid"`
	} `json:"forkedFrom"`
}

type message struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// State carries session identity and a terminal conflict decision across
// positioned incremental reads.
type State struct {
	history.Session
	Stopped bool
}

// Extract normalizes complete positioned transcript records without DB writes.
func Extract(source history.SourceReference, records []jsonl.Record) history.Extraction {
	return ExtractWithOptions(source, records, history.ExtractionOptions{})
}

// ExtractWithOptions normalizes records using explicitly consented roles.
func ExtractWithOptions(source history.SourceReference, records []jsonl.Record, options history.ExtractionOptions) history.Extraction {
	result, _ := ExtractWithStateOptions(source, records, State{}, options)
	return result
}

// ExtractWithSession carries a previously established session identity across
// a positioned suffix read.
func ExtractWithSession(source history.SourceReference, records []jsonl.Record, prior history.Session) history.Extraction {
	result, _ := ExtractWithStateOptions(source, records, State{Session: prior}, history.ExtractionOptions{})
	return result
}

// ExtractWithState preserves the extractor's stop decision across streamed
// records and later suffix reads.
func ExtractWithState(source history.SourceReference, records []jsonl.Record, state State) (history.Extraction, State) {
	return ExtractWithStateOptions(source, records, state, history.ExtractionOptions{})
}

// ExtractWithStateOptions preserves state while applying explicit role consent.
func ExtractWithStateOptions(source history.SourceReference, records []jsonl.Record, state State, options history.ExtractionOptions) (history.Extraction, State) {
	result := history.Extraction{Provider: history.ProviderClaude, Source: source, Session: state.Session}
	sourceFact := claudeSourceFact(source.Path)
	if result.Session.ThreadKind == "" {
		result.Session.ThreadKind = history.ThreadUnknown
	}
	if result.Session.ThreadConfidence == "" {
		result.Session.ThreadConfidence = history.ConfidenceUnknown
	}
	if result.Session.Confidence == "" {
		result.Session.Confidence = history.ConfidenceUnknown
	}
	applyClaudeSourceFact(&result.Session, sourceFact)
	if state.Stopped {
		result.Relationships = claudeRelationships(result.Session, sourceFact)
		return result, state
	}
	var firstRecord []byte
	seen := make(map[string]int)
	foundSessionID := false

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
		if sourceFact.threadKind != history.ThreadSubagent && item.SessionID != "" && result.Session.NativeSessionID != "" && result.Session.NativeSessionID != item.SessionID {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "conflicting sessionId record excluded"})
			state.Stopped = true
			break recordsLoop
		}
		updateRange(&result.Session, parseTime(item.Timestamp))
		if sourceFact.threadKind != history.ThreadSubagent && item.SessionID != "" && result.Session.NativeSessionID == "" {
			result.Session.NativeSessionID = item.SessionID
			foundSessionID = true
			result.Session.Evidence = "sessionId"
			result.Session.Confidence = history.ConfidenceExact
		}
		if result.Session.CWD == "" && item.CWD != "" {
			result.Session.CWD = item.CWD
		}
		if item.ForkedFrom != nil && result.Session.ForkedFromSessionID == "" {
			result.Session.ForkedFromSessionID = item.ForkedFrom.SessionID
			result.Session.ForkedFromMessageID = item.ForkedFrom.MessageUUID
		}
		if item.IsSidechain && sourceFact.threadKind != history.ThreadSubagent {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationProviderMetadata, Message: "sidechain record excluded"})
			continue
		}
		assistant := options.IndexAssistant && item.Type == "assistant"
		if item.Type != "user" && !assistant {
			classification := history.ClassificationProviderMetadata
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: classification, Message: "non-searchable transcript record excluded"})
			continue
		}
		if assistant && (item.IsMeta || item.Compact) {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationAgentInstruction, Message: "assistant metadata record excluded"})
			continue
		}

		var msg message
		if json.Unmarshal(item.Message, &msg) != nil {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "malformed nested message excluded"})
			continue
		}
		expectedRole := "user"
		role := history.RoleUser
		if assistant {
			expectedRole = "assistant"
			role = history.RoleAssistant
		}
		if msg.Role != expectedRole {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "mismatched nested message role excluded"})
			continue
		}
		text, toolResult := textContent(msg.Content)
		if assistant {
			text = textBlockContent(msg.Content)
			toolResult = false
		}
		classification := history.ClassificationHuman
		searchable := true
		oversized := false
		clean := ""
		switch {
		case toolResult:
			clean = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
			classification = history.ClassificationToolResult
			searchable = false
		case item.IsMeta || item.Compact:
			clean = strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
			classification = history.ClassificationAgentInstruction
			searchable = false
		default:
			clean, classification, searchable, oversized = history.CleanHumanText(text)
		}
		if assistant && classification == history.ClassificationHuman {
			classification = history.ClassificationAssistant
		}
		if clean == "" {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: classification, Message: "empty searchable text excluded"})
			continue
		}
		key := history.MessageIdentityKey(item.UUID, record.LineNumber, clean)
		evidence := "user.message.content"
		if assistant {
			evidence = "assistant.message.content.text"
		}
		prompt := history.Prompt{LogicalKey: key, NativeMessageID: item.UUID, ParentNativeMessageID: item.ParentUUID, Role: role, CleanText: clean, Classification: classification, Searchable: searchable, Oversized: oversized, Timestamp: parseTime(item.Timestamp), Model: msg.Model, Evidence: evidence, Confidence: history.ConfidenceExact}
		if index, ok := seen[key]; ok {
			if history.CanonicalPromptWins(prompt, result.Prompts[index]) {
				result.Prompts[index] = prompt
			}
		} else {
			seen[key] = len(result.Prompts)
			result.Prompts = append(result.Prompts, prompt)
		}
		result.Occurrences = append(result.Occurrences, history.Occurrence{PromptKey: key, Variant: prompt, LineNumber: record.LineNumber, StartOffset: record.StartOffset, EndOffset: record.EndOffset})
	}

	if result.Session.IdentityKey == "" || foundSessionID || (strings.HasPrefix(result.Session.FallbackKey, "source-path:") && len(firstRecord) > 0) {
		if result.Session.NativeSessionID == "" && sourceFact.threadKind == history.ThreadRoot && len(firstRecord) > 0 {
			result.Session.NativeSessionID = sourceFact.nativeSessionID
		}
		result.Session.IdentityKey, result.Session.FallbackKey = history.SessionIdentityKey(history.ProviderClaude, result.Session.NativeSessionID, source.Path, firstRecord)
	}
	result.Relationships = claudeRelationships(result.Session, sourceFact)
	state.Session = result.Session
	return result, state
}

type sourceRelationshipFact struct {
	threadKind      history.ThreadKind
	nativeSessionID string
	parentSessionID string
	originator      string
	providerValue   string
	evidence        string
}

func claudeSourceFact(path string) sourceRelationshipFact {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(path)), "/")
	projects := -1
	for index, part := range parts {
		if part == "projects" {
			projects = index
		}
	}
	if projects < 0 || len(parts) < projects+3 {
		return sourceRelationshipFact{}
	}
	relative := parts[projects+1:]
	if len(relative) == 2 && strings.HasSuffix(relative[1], ".jsonl") {
		return sourceRelationshipFact{
			threadKind: history.ThreadRoot, nativeSessionID: strings.TrimSuffix(relative[1], ".jsonl"),
			evidence: "source_path.main_transcript",
		}
	}
	if len(relative) >= 4 && relative[2] == "subagents" && strings.HasSuffix(relative[len(relative)-1], ".jsonl") {
		subpath := append([]string(nil), relative[2:]...)
		subpath[len(subpath)-1] = strings.TrimSuffix(subpath[len(subpath)-1], ".jsonl")
		providerValue := strings.Join(subpath, "/")
		return sourceRelationshipFact{
			threadKind: history.ThreadSubagent, nativeSessionID: relative[1] + "/" + providerValue,
			parentSessionID: relative[1], originator: subpath[len(subpath)-1], providerValue: providerValue,
			evidence: "source_path.subagent_transcript",
		}
	}
	return sourceRelationshipFact{}
}

func applyClaudeSourceFact(session *history.Session, fact sourceRelationshipFact) {
	if fact.threadKind == "" {
		return
	}
	if fact.threadKind == history.ThreadSubagent {
		session.NativeSessionID = fact.nativeSessionID
	}
	session.ThreadKind = fact.threadKind
	session.ThreadEvidence = fact.evidence
	session.ThreadConfidence = history.ConfidenceDerived
	session.ThreadRuleVersion = history.RelationshipRuleVersion
	if fact.parentSessionID != "" {
		session.ParentNativeSessionID = fact.parentSessionID
	}
	if fact.originator != "" {
		session.Originator = fact.originator
	}
}

func claudeRelationships(session history.Session, fact sourceRelationshipFact) []history.Relationship {
	relationships := []history.Relationship{}
	if session.ThreadKind == history.ThreadSubagent && session.ParentNativeSessionID != "" {
		relationships = append(relationships, history.Relationship{
			Kind: history.RelationSubagent, ParentNativeSessionID: session.ParentNativeSessionID,
			ProviderNativeValue: fact.providerValue, Evidence: session.ThreadEvidence,
			Confidence: session.ThreadConfidence, RuleVersion: history.RelationshipRuleVersion,
		})
	}
	if session.ForkedFromSessionID != "" {
		relationships = append(relationships, history.Relationship{
			Kind: history.RelationFork, ParentNativeSessionID: session.ForkedFromSessionID,
			ParentNativeMessageID: session.ForkedFromMessageID, ProviderNativeValue: session.ForkedFromSessionID,
			Evidence: "forkedFrom.sessionId", Confidence: history.ConfidenceExact,
			RuleVersion: history.RelationshipRuleVersion,
		})
	}
	return relationships
}

func textContent(raw json.RawMessage) (string, bool) {
	var scalar string
	if json.Unmarshal(raw, &scalar) == nil {
		return scalar, false
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	var text []string
	toolResult := false
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text = append(text, block.Text)
		case "tool_result", "server_tool_result":
			toolResult = true
		}
	}
	return strings.Join(text, "\n"), toolResult
}

func textBlockContent(raw json.RawMessage) string {
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	text := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			text = append(text, block.Text)
		}
	}
	return strings.Join(text, "\n")
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
