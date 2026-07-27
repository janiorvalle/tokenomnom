package exporter

import (
	"encoding/json"
	"strings"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

type codexEnvelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	ClientID  string          `json:"client_id"`
	CallID    string          `json:"call_id"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	Message   string          `json:"message"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Action    json.RawMessage `json:"action"`
	Output    json.RawMessage `json:"output"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
}

func parseCodex(sessionID string, positioned []jsonl.Record) []Record {
	result := []Record{}
	var pendingUser *Record
	toolNames := map[string]string{}
	for _, source := range positioned {
		var item codexEnvelope
		if json.Unmarshal(source.Raw, &item) != nil || item.Type == "" {
			result = appendPending(result, &pendingUser)
			result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, "", "", "", "", "", source.LineNumber))
			continue
		}
		var payload codexPayload
		if len(item.Payload) > 0 && json.Unmarshal(item.Payload, &payload) != nil {
			result = appendPending(result, &pendingUser)
			result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", "", "", source.LineNumber))
			continue
		}
		switch item.Type {
		case "session_meta":
			result = appendPending(result, &pendingUser)
			result = append(result, record(sessionID, history.ProviderCodex, history.RoleSystem, KindMetadata, item.Timestamp, "", "session metadata", payload.ID, "", source.LineNumber))
		case "turn_context":
			result = appendPending(result, &pendingUser)
			result = append(result, record(sessionID, history.ProviderCodex, history.RoleSystem, KindSystem, item.Timestamp, "", "turn context", payload.ID, "", source.LineNumber))
		case "event_msg":
			switch payload.Type {
			case "user_message":
				current := messageRecord(sessionID, history.ProviderCodex, history.RoleUser, item.Timestamp, payload.Message, first(payload.ClientID, payload.ID), "", source.LineNumber)
				if pendingUser != nil && sameMessage(*pendingUser, current) {
					if current.NativeID == "" {
						current.NativeID = pendingUser.NativeID
					}
					pendingUser = nil
				}
				result = append(result, current)
			case "agent_message":
				result = appendPending(result, &pendingUser)
				result = append(result, messageRecord(sessionID, history.ProviderCodex, history.RoleAssistant, item.Timestamp, payload.Message, payload.ID, "", source.LineNumber))
			case "agent_reasoning", "reasoning":
				result = appendPending(result, &pendingUser)
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleAssistant, KindThinking, item.Timestamp, payload.Message, "reasoning", payload.ID, "", source.LineNumber))
			case "token_count", "task_started", "task_complete", "context_compacted":
				result = appendPending(result, &pendingUser)
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleSystem, KindMetadata, item.Timestamp, "", payload.Type, payload.ID, "", source.LineNumber))
			default:
				result = appendPending(result, &pendingUser)
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", payload.ID, "", source.LineNumber))
			}
		case "response_item":
			switch payload.Type {
			case "message":
				text := codexText(payload.Content)
				role := history.Role(payload.Role)
				if role != history.RoleUser && role != history.RoleAssistant && role != history.RoleSystem && role != history.RoleDeveloper {
					role = history.RoleUnknown
				}
				current := messageRecord(sessionID, history.ProviderCodex, role, item.Timestamp, text, payload.ID, "", source.LineNumber)
				if role == history.RoleUser {
					result = appendPending(result, &pendingUser)
					if text != "" {
						pendingUser = &current
					}
				} else {
					result = appendPending(result, &pendingUser)
					if text != "" {
						result = append(result, current)
					}
				}
				for _, block := range payload.Content {
					switch block.Type {
					case "input_text", "output_text", "text":
					case "input_image", "output_image":
						result = append(result, record(sessionID, history.ProviderCodex, history.RoleSystem, KindMetadata, item.Timestamp, "", "image attachment", payload.ID, "", source.LineNumber))
					default:
						result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", payload.ID, "", source.LineNumber))
					}
				}
				if text == "" && len(payload.Content) == 0 {
					result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", payload.ID, "", source.LineNumber))
				}
			case "function_call", "custom_tool_call", "local_shell_call", "web_search_call":
				result = appendPending(result, &pendingUser)
				callID := first(payload.CallID, payload.ID)
				name := first(payload.Name, payload.Type)
				toolNames[callID] = name
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleAssistant, KindToolCall, item.Timestamp, first(rawString(payload.Arguments), rawString(payload.Input), rawString(payload.Action)), name, callID, "", source.LineNumber))
			case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
				result = appendPending(result, &pendingUser)
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleTool, KindToolResult, item.Timestamp, rawString(payload.Output), first(payload.Name, toolNames[payload.CallID]), payload.ID, payload.CallID, source.LineNumber))
			case "reasoning":
				result = appendPending(result, &pendingUser)
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleAssistant, KindThinking, item.Timestamp, codexReasoning(payload), "reasoning", payload.ID, "", source.LineNumber))
			default:
				result = appendPending(result, &pendingUser)
				result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", payload.ID, "", source.LineNumber))
			}
		default:
			result = appendPending(result, &pendingUser)
			result = append(result, record(sessionID, history.ProviderCodex, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", payload.ID, "", source.LineNumber))
		}
	}
	return appendPending(result, &pendingUser)
}

func appendPending(result []Record, pending **Record) []Record {
	if *pending != nil {
		result = append(result, **pending)
		*pending = nil
	}
	return result
}

func sameMessage(left, right Record) bool {
	if left.pairText == "" || left.pairText != right.pairText {
		return false
	}
	return left.Timestamp == nil || right.Timestamp == nil || *left.Timestamp == *right.Timestamp
}

func codexText(blocks []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	values := []string{}
	for _, block := range blocks {
		switch block.Type {
		case "input_text", "output_text", "text":
			values = append(values, block.Text)
		}
	}
	return strings.Join(values, "\n")
}

func codexReasoning(payload codexPayload) string {
	values := []string{}
	for _, block := range payload.Summary {
		if block.Text != "" {
			values = append(values, block.Text)
		}
	}
	if len(values) == 0 {
		values = append(values, codexText(payload.Content))
	}
	return strings.Join(values, "\n")
}

func rawString(value json.RawMessage) string {
	if len(value) == 0 || string(value) == "null" {
		return ""
	}
	var scalar string
	if json.Unmarshal(value, &scalar) == nil {
		return scalar
	}
	return string(value)
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
