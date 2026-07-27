package exporter

import (
	"encoding/json"
	"strings"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

type claudeEnvelope struct {
	Type       string          `json:"type"`
	UUID       string          `json:"uuid"`
	ParentUUID string          `json:"parentUuid"`
	Timestamp  string          `json:"timestamp"`
	IsMeta     bool            `json:"isMeta"`
	Compact    bool            `json:"isCompactSummary"`
	Message    json.RawMessage `json:"message"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	Data      json.RawMessage `json:"data"`
}

func parseClaude(sessionID string, positioned []jsonl.Record) []Record {
	result := []Record{}
	toolNames := map[string]string{}
	for _, source := range positioned {
		var item claudeEnvelope
		if json.Unmarshal(source.Raw, &item) != nil || item.Type == "" {
			result = append(result, record(sessionID, history.ProviderClaude, history.RoleUnknown, KindUnrecognized, "", "", "", "", "", source.LineNumber))
			continue
		}
		switch item.Type {
		case "user", "assistant":
			var message claudeMessage
			if json.Unmarshal(item.Message, &message) != nil {
				result = append(result, record(sessionID, history.ProviderClaude, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", item.UUID, item.ParentUUID, source.LineNumber))
				continue
			}
			role := history.Role(message.Role)
			if role != history.RoleUser && role != history.RoleAssistant {
				role = history.Role(item.Type)
			}
			if item.IsMeta || item.Compact {
				label := "injected metadata"
				if item.Compact {
					label = "compact summary"
				}
				result = append(result, record(sessionID, history.ProviderClaude, history.RoleSystem, KindSystem, item.Timestamp, "", label, item.UUID, item.ParentUUID, source.LineNumber))
				continue
			}
			var scalar string
			if json.Unmarshal(message.Content, &scalar) == nil {
				result = append(result, messageRecord(sessionID, history.ProviderClaude, role, item.Timestamp, scalar, item.UUID, item.ParentUUID, source.LineNumber))
				continue
			}
			var blocks []claudeBlock
			if json.Unmarshal(message.Content, &blocks) != nil {
				result = append(result, record(sessionID, history.ProviderClaude, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", item.UUID, item.ParentUUID, source.LineNumber))
				continue
			}
			if len(blocks) == 0 {
				result = append(result, record(sessionID, history.ProviderClaude, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", item.UUID, item.ParentUUID, source.LineNumber))
				continue
			}
			text := []string{}
			for _, block := range blocks {
				switch block.Type {
				case "text":
					text = append(text, block.Text)
				case "tool_use", "server_tool_use":
					result = appendTextRecord(result, sessionID, role, item, source.LineNumber, &text)
					toolNames[block.ID] = block.Name
					result = append(result, record(sessionID, history.ProviderClaude, history.RoleAssistant, KindToolCall, item.Timestamp, rawString(block.Input), block.Name, first(block.ID, item.UUID), item.ParentUUID, source.LineNumber))
				case "tool_result", "server_tool_result":
					result = appendTextRecord(result, sessionID, role, item, source.LineNumber, &text)
					result = append(result, record(sessionID, history.ProviderClaude, history.RoleTool, KindToolResult, item.Timestamp, rawString(block.Content), first(block.Name, toolNames[block.ToolUseID]), item.UUID, block.ToolUseID, source.LineNumber))
				case "thinking", "redacted_thinking":
					result = appendTextRecord(result, sessionID, role, item, source.LineNumber, &text)
					value := first(block.Thinking, block.Text, rawString(block.Data))
					result = append(result, record(sessionID, history.ProviderClaude, history.RoleAssistant, KindThinking, item.Timestamp, value, block.Type, item.UUID, item.ParentUUID, source.LineNumber))
				case "image", "document":
					result = appendTextRecord(result, sessionID, role, item, source.LineNumber, &text)
					result = append(result, record(sessionID, history.ProviderClaude, history.RoleSystem, KindMetadata, item.Timestamp, "", block.Type+" attachment", item.UUID, item.ParentUUID, source.LineNumber))
				default:
					result = appendTextRecord(result, sessionID, role, item, source.LineNumber, &text)
					result = append(result, record(sessionID, history.ProviderClaude, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", item.UUID, item.ParentUUID, source.LineNumber))
				}
			}
			result = appendTextRecord(result, sessionID, role, item, source.LineNumber, &text)
		case "system", "summary", "queue-operation", "file-history-snapshot", "progress":
			result = append(result, record(sessionID, history.ProviderClaude, history.RoleSystem, KindMetadata, item.Timestamp, "", strings.ReplaceAll(item.Type, "-", " "), item.UUID, item.ParentUUID, source.LineNumber))
		default:
			result = append(result, record(sessionID, history.ProviderClaude, history.RoleUnknown, KindUnrecognized, item.Timestamp, "", "", item.UUID, item.ParentUUID, source.LineNumber))
		}
	}
	return result
}

func appendTextRecord(result []Record, sessionID string, role history.Role, item claudeEnvelope, line int64, text *[]string) []Record {
	if len(*text) == 0 {
		return result
	}
	result = append(result, messageRecord(sessionID, history.ProviderClaude, role, item.Timestamp, strings.Join(*text, "\n"), item.UUID, item.ParentUUID, line))
	*text = nil
	return result
}
