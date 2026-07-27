// Package exporter parses exact provider transcript bytes into deterministic,
// provider-neutral records and renders full-session artifacts.
package exporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
)

// Kind classifies one exportable transcript record.
type Kind string

const (
	KindMessage      Kind = "message"
	KindToolCall     Kind = "tool_call"
	KindToolResult   Kind = "tool_result"
	KindThinking     Kind = "thinking"
	KindSystem       Kind = "system"
	KindMetadata     Kind = "metadata"
	KindUnrecognized Kind = "unrecognized"
)

// Record is the provider-neutral full-transcript schema used by normalized
// exports. Text is omitted for collapsed tool and thinking records.
type Record struct {
	SessionID string           `json:"session_id"`
	Provider  history.Provider `json:"provider"`
	Role      history.Role     `json:"role"`
	Kind      Kind             `json:"kind"`
	Timestamp *string          `json:"timestamp"`
	Text      *string          `json:"text,omitempty"`
	Name      string           `json:"name,omitempty"`
	NativeID  string           `json:"native_id,omitempty"`
	ParentID  string           `json:"parent_id,omitempty"`
	Bytes     int              `json:"bytes"`
	Line      int64            `json:"line"`
	pairText  string
}

// Session supplies indexed provenance plus exact raw bytes for rendering.
type Session struct {
	SessionID             string
	Provider              history.Provider
	NativeSessionID       string
	FirstTimestamp        *string
	LastTimestamp         *string
	Project               string
	CWD                   string
	ThreadKind            history.ThreadKind
	SourceSHA256          string
	Origin                string
	Raw                   []byte
	ParentSessionID       string
	ParentNativeMessageID string
	ContentWarning        string
}

// Options controls transcript volume and deterministic export metadata.
type Options struct {
	IncludeToolOutput bool
	IncludeThinking   bool
	ExportedAt        time.Time
	Version           string
}

// Counts are accumulated into the command report.
type Counts struct {
	CollapsedToolRecords int `json:"collapsed_tool_records"`
	ExcludedThinking     int `json:"excluded_thinking_records"`
	UnrecognizedRecords  int `json:"unrecognized_records"`
}

// Parse reads one exact JSONL transcript without storing any derived content.
func Parse(provider history.Provider, sessionID string, raw []byte) ([]Record, error) {
	var positioned []jsonl.Record
	if _, err := jsonl.ReadPositionedReader(bytes.NewReader(raw), jsonl.Position{}, func(record jsonl.Record) {
		record.Raw = append([]byte(nil), record.Raw...)
		positioned = append(positioned, record)
	}); err != nil {
		return nil, err
	}
	switch provider {
	case history.ProviderCodex:
		return parseCodex(sessionID, positioned), nil
	case history.ProviderClaude:
		return parseClaude(sessionID, positioned), nil
	default:
		return nil, fmt.Errorf("unsupported history provider %q", provider)
	}
}

// RenderMarkdown renders one root-first session tree.
func RenderMarkdown(writer io.Writer, sessions []Session, options Options) (Counts, error) {
	counts := Counts{}
	if len(sessions) == 0 {
		return counts, nil
	}
	if options.ExportedAt.IsZero() {
		options.ExportedAt = time.Now().UTC()
	}
	root := sessions[0]
	fmt.Fprintln(writer, "---")
	fmt.Fprintf(writer, "session_id: %s\n", yamlString(root.SessionID))
	fmt.Fprintf(writer, "native_session_id: %s\n", yamlString(root.NativeSessionID))
	fmt.Fprintf(writer, "provider: %s\n", yamlString(string(root.Provider)))
	fmt.Fprintf(writer, "first_timestamp: %s\n", yamlOptional(root.FirstTimestamp))
	fmt.Fprintf(writer, "last_timestamp: %s\n", yamlOptional(root.LastTimestamp))
	fmt.Fprintf(writer, "project: %s\n", yamlString(root.Project))
	fmt.Fprintf(writer, "cwd: %s\n", yamlString(root.CWD))
	fmt.Fprintf(writer, "thread_kind: %s\n", yamlString(string(root.ThreadKind)))
	fmt.Fprintf(writer, "source_sha256: %s\n", yamlString(root.SourceSHA256))
	fmt.Fprintf(writer, "source_origin: %s\n", yamlString(root.Origin))
	fmt.Fprintf(writer, "tokenomnom_version: %s\n", yamlString(options.Version))
	fmt.Fprintf(writer, "exported_at: %s\n", yamlString(options.ExportedAt.UTC().Format(time.RFC3339)))
	fmt.Fprintln(writer, "sessions:")
	for _, session := range sessions {
		fmt.Fprintf(writer, "  - session_id: %s\n", yamlString(session.SessionID))
		fmt.Fprintf(writer, "    native_session_id: %s\n", yamlString(session.NativeSessionID))
		fmt.Fprintf(writer, "    provider: %s\n", yamlString(string(session.Provider)))
		fmt.Fprintf(writer, "    first_timestamp: %s\n", yamlOptional(session.FirstTimestamp))
		fmt.Fprintf(writer, "    last_timestamp: %s\n", yamlOptional(session.LastTimestamp))
		fmt.Fprintf(writer, "    project: %s\n", yamlString(session.Project))
		fmt.Fprintf(writer, "    cwd: %s\n", yamlString(session.CWD))
		fmt.Fprintf(writer, "    thread_kind: %s\n", yamlString(string(session.ThreadKind)))
		fmt.Fprintf(writer, "    source_sha256: %s\n", yamlString(session.SourceSHA256))
		fmt.Fprintf(writer, "    source_origin: %s\n", yamlString(session.Origin))
	}
	fmt.Fprintln(writer, "---")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "# Full session export")

	childrenAt := make(map[string]map[string][]Session)
	childrenEnd := make(map[string][]Session)
	for _, child := range sessions[1:] {
		if child.ParentNativeMessageID == "" {
			childrenEnd[child.ParentSessionID] = append(childrenEnd[child.ParentSessionID], child)
			continue
		}
		if childrenAt[child.ParentSessionID] == nil {
			childrenAt[child.ParentSessionID] = make(map[string][]Session)
		}
		childrenAt[child.ParentSessionID][child.ParentNativeMessageID] = append(childrenAt[child.ParentSessionID][child.ParentNativeMessageID], child)
	}
	for index, session := range sessions {
		fmt.Fprintln(writer)
		if index == 0 {
			fmt.Fprintf(writer, "## Root session `%s`\n", session.SessionID)
		} else {
			fmt.Fprintf(writer, "## Subagent session `%s`\n", session.SessionID)
		}
		fmt.Fprintln(writer)
		writeSessionProvenance(writer, session)
		if session.ContentWarning != "" {
			fmt.Fprintf(writer, "\n[transcript unavailable: %s]\n", oneLine(session.ContentWarning))
			continue
		}
		records, err := Parse(session.Provider, session.SessionID, session.Raw)
		if err != nil {
			return counts, err
		}
		for _, record := range records {
			if err := writeMarkdownRecord(writer, record, options, &counts); err != nil {
				return counts, err
			}
			for _, child := range childrenAt[session.SessionID][record.NativeID] {
				fmt.Fprintf(writer, "\n[delegated to subagent session `%s`]\n", child.SessionID)
			}
		}
		for _, child := range childrenEnd[session.SessionID] {
			fmt.Fprintf(writer, "\n[delegated subagent session `%s`; spawn record not identified]\n", child.SessionID)
		}
	}
	return counts, nil
}

// RenderNormalized renders one JSONL stream using the same inclusion policy as
// markdown output.
func RenderNormalized(writer io.Writer, sessions []Session, options Options) (Counts, error) {
	counts := Counts{}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, session := range sessions {
		if session.ContentWarning != "" {
			text := session.ContentWarning
			record := Record{SessionID: session.SessionID, Provider: session.Provider, Role: history.RoleUnknown, Kind: KindMetadata, Text: &text, Bytes: len(text)}
			if err := encoder.Encode(record); err != nil {
				return counts, err
			}
			continue
		}
		records, err := Parse(session.Provider, session.SessionID, session.Raw)
		if err != nil {
			return counts, err
		}
		for _, record := range records {
			applyVolumePolicy(&record, options, &counts)
			if err := encoder.Encode(record); err != nil {
				return counts, err
			}
		}
	}
	return counts, nil
}

func applyVolumePolicy(record *Record, options Options, counts *Counts) {
	switch record.Kind {
	case KindToolCall, KindToolResult:
		if !options.IncludeToolOutput {
			record.Text = nil
			counts.CollapsedToolRecords++
		}
	case KindThinking:
		if !options.IncludeThinking {
			record.Text = nil
			counts.ExcludedThinking++
		}
	case KindUnrecognized:
		counts.UnrecognizedRecords++
	}
}

func writeMarkdownRecord(writer io.Writer, record Record, options Options, counts *Counts) error {
	switch record.Kind {
	case KindMessage:
		fmt.Fprintf(writer, "\n### %s%s\n\n", titleRole(record.Role), timestampSuffix(record.Timestamp))
		if record.Text != nil {
			fmt.Fprintln(writer, *record.Text)
		}
	case KindToolCall, KindToolResult:
		label := "tool call"
		if record.Kind == KindToolResult {
			label = "tool result"
		}
		fmt.Fprintf(writer, "\n[%s: %s, %d bytes]%s\n", label, fallback(record.Name, "unknown"), record.Bytes, timestampSuffix(record.Timestamp))
		if !options.IncludeToolOutput {
			counts.CollapsedToolRecords++
			return nil
		}
		writeFenced(writer, record.Text)
	case KindThinking:
		if !options.IncludeThinking {
			fmt.Fprintf(writer, "\n[thinking omitted: %d bytes]%s\n", record.Bytes, timestampSuffix(record.Timestamp))
			counts.ExcludedThinking++
			return nil
		}
		fmt.Fprintf(writer, "\n[thinking]%s\n", timestampSuffix(record.Timestamp))
		writeFenced(writer, record.Text)
	case KindSystem, KindMetadata:
		fmt.Fprintf(writer, "\n[%s record: %s]%s\n", record.Kind, fallback(record.Name, "provider metadata"), timestampSuffix(record.Timestamp))
	case KindUnrecognized:
		fmt.Fprintf(writer, "\n[unrecognized record]%s\n", timestampSuffix(record.Timestamp))
		counts.UnrecognizedRecords++
	}
	return nil
}

func writeSessionProvenance(writer io.Writer, session Session) {
	fmt.Fprintf(writer, "- Provider: `%s`\n", session.Provider)
	fmt.Fprintf(writer, "- Native session ID: `%s`\n", fallback(session.NativeSessionID, "unknown"))
	fmt.Fprintf(writer, "- Time range: `%s` to `%s`\n", optionalValue(session.FirstTimestamp), optionalValue(session.LastTimestamp))
	fmt.Fprintf(writer, "- Project: `%s`\n", fallback(session.Project, "unknown"))
	fmt.Fprintf(writer, "- CWD: `%s`\n", fallback(session.CWD, "unknown"))
	fmt.Fprintf(writer, "- Thread kind: `%s`\n", session.ThreadKind)
	fmt.Fprintf(writer, "- Source: `%s` (`%s`)\n", fallback(session.SourceSHA256, "unavailable"), fallback(session.Origin, "unavailable"))
}

func writeFenced(writer io.Writer, text *string) {
	value := ""
	if text != nil {
		value = *text
	}
	fence := "```"
	for strings.Contains(value, fence) {
		fence += "`"
	}
	fmt.Fprintf(writer, "\n%s\n%s", fence, value)
	if !strings.HasSuffix(value, "\n") {
		fmt.Fprintln(writer)
	}
	fmt.Fprintf(writer, "%s\n", fence)
}

func record(sessionID string, provider history.Provider, role history.Role, kind Kind, timestamp, text, name, nativeID, parentID string, line int64) Record {
	var timestampValue *string
	if parsed, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		formatted := parsed.UTC().Format(time.RFC3339Nano)
		timestampValue = &formatted
	}
	var textValue *string
	if text != "" {
		copy := text
		textValue = &copy
	}
	return Record{
		SessionID: sessionID, Provider: provider, Role: role, Kind: kind,
		Timestamp: timestampValue, Text: textValue, Name: name, NativeID: nativeID,
		ParentID: parentID, Bytes: len([]byte(text)), Line: line, pairText: text,
	}
}

func messageRecord(sessionID string, provider history.Provider, role history.Role, timestamp, text, nativeID, parentID string, line int64) Record {
	value := record(sessionID, provider, role, KindMessage, timestamp, text, "", nativeID, parentID, line)
	if role != history.RoleUser {
		return value
	}
	_, classification, _, _ := history.CleanHumanText(text)
	promptKind := history.ClassifyPromptKind(text, role, classification)
	switch {
	case classification == history.ClassificationSystemInjected,
		classification == history.ClassificationAgentInstruction,
		classification == history.ClassificationLocalCommand:
		value.Kind, value.Role, value.Name, value.Text = KindSystem, history.RoleSystem, string(classification), nil
	case promptKind != history.PromptKindHuman && promptKind != history.PromptKindUnknown:
		value.Kind, value.Role, value.Name, value.Text = KindSystem, history.RoleSystem, "provider "+string(promptKind), nil
	}
	return value
}

func titleRole(role history.Role) string {
	switch role {
	case history.RoleUser:
		return "User"
	case history.RoleAssistant:
		return "Assistant"
	default:
		value := string(role)
		if value == "" {
			return "Unknown"
		}
		return strings.ToUpper(value[:1]) + value[1:]
	}
}

func timestampSuffix(value *string) string {
	if value == nil {
		return ""
	}
	return " - " + *value
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func yamlOptional(value *string) string {
	if value == nil {
		return "null"
	}
	return yamlString(*value)
}

func optionalValue(value *string) string {
	if value == nil || *value == "" {
		return "unknown"
	}
	return *value
}

func fallback(value, fallbackValue string) string {
	if value == "" {
		return fallbackValue
	}
	return value
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
