// Package claude extracts provider-neutral history from Claude Code JSONL.
package claude

import (
	"encoding/json"
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
		SessionID string `json:"sessionId"`
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

// Extract normalizes complete positioned transcript records without DB writes.
func Extract(source history.SourceReference, records []jsonl.Record) history.Extraction {
	return ExtractWithSession(source, records, history.Session{})
}

// ExtractWithSession carries a previously established session identity across
// a positioned suffix read.
func ExtractWithSession(source history.SourceReference, records []jsonl.Record, prior history.Session) history.Extraction {
	result := history.Extraction{Provider: history.ProviderClaude, Source: source, Session: prior}
	if result.Session.ThreadKind == "" {
		result.Session.ThreadKind = history.ThreadUnknown
	}
	if result.Session.Confidence == "" {
		result.Session.Confidence = history.ConfidenceUnknown
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
		if item.SessionID != "" && result.Session.NativeSessionID != "" && result.Session.NativeSessionID != item.SessionID {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "conflicting sessionId record excluded"})
			break recordsLoop
		}
		updateRange(&result.Session, parseTime(item.Timestamp))
		if item.SessionID != "" && result.Session.NativeSessionID == "" {
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
		}
		if item.IsSidechain {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationProviderMetadata, Message: "sidechain record excluded"})
			continue
		}
		if item.Type != "user" {
			classification := history.ClassificationProviderMetadata
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: classification, Message: "non-user transcript record excluded"})
			continue
		}

		var msg message
		if json.Unmarshal(item.Message, &msg) != nil {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "malformed nested message excluded"})
			continue
		}
		if msg.Role != "user" {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: history.ClassificationUnknown, Message: "non-user nested message role excluded"})
			continue
		}
		text, toolResult := textContent(msg.Content)
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
		if clean == "" {
			result.Diagnostics = append(result.Diagnostics, history.Diagnostic{LineNumber: record.LineNumber, Classification: classification, Message: "empty user text excluded"})
			continue
		}
		key := history.MessageIdentityKey(item.UUID, record.LineNumber, clean)
		prompt := history.Prompt{LogicalKey: key, NativeMessageID: item.UUID, ParentNativeMessageID: item.ParentUUID, Role: history.RoleUser, CleanText: clean, Classification: classification, Searchable: searchable, Oversized: oversized, Timestamp: parseTime(item.Timestamp), Model: msg.Model, Evidence: "user.message.content", Confidence: history.ConfidenceExact}
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
		result.Session.IdentityKey, result.Session.FallbackKey = history.SessionIdentityKey(history.ProviderClaude, result.Session.NativeSessionID, source.Path, firstRecord)
	}
	return result
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
