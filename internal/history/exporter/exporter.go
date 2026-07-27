// Package exporter parses exact provider transcript bytes into deterministic,
// provider-neutral records and renders full-session artifacts.
package exporter

import (
	"bytes"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/history"
	"github.com/janiorvalle/tokenomnom/internal/ingest/jsonl"
	"github.com/yuin/goldmark"
	goldast "github.com/yuin/goldmark/ast"
	goldtext "github.com/yuin/goldmark/text"
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
	StructureNonce    string
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
	nonce, err := exportStructureNonce(options.StructureNonce)
	if err != nil {
		return counts, err
	}
	structure := structureSuffix(nonce)
	root := sessions[0]
	fmt.Fprintln(writer, "---")
	fmt.Fprintf(writer, "session_id: %s\n", yamlString(root.SessionID))
	fmt.Fprintf(writer, "native_session_id: %s\n", yamlString(root.NativeSessionID))
	fmt.Fprintf(writer, "provider: %s\n", yamlString(string(root.Provider)))
	fmt.Fprintf(writer, "first_timestamp: %s\n", yamlOptional(root.FirstTimestamp))
	fmt.Fprintf(writer, "last_timestamp: %s\n", yamlOptional(root.LastTimestamp))
	fmt.Fprintf(writer, "project: %s\n", yamlString(root.Project))
	fmt.Fprintf(writer, "cwd: %s\n", yamlString(fallback(root.CWD, "unknown")))
	fmt.Fprintf(writer, "thread_kind: %s\n", yamlString(string(root.ThreadKind)))
	fmt.Fprintf(writer, "source_sha256: %s\n", yamlString(root.SourceSHA256))
	fmt.Fprintf(writer, "source_origin: %s\n", yamlString(root.Origin))
	fmt.Fprintf(writer, "tokenomnom_version: %s\n", yamlString(options.Version))
	fmt.Fprintf(writer, "exported_at: %s\n", yamlString(options.ExportedAt.UTC().Format(time.RFC3339)))
	fmt.Fprintf(writer, "structure_nonce: %s\n", yamlString(nonce))
	fmt.Fprintln(writer, "sessions:")
	for _, session := range sessions {
		fmt.Fprintf(writer, "  - session_id: %s\n", yamlString(session.SessionID))
		fmt.Fprintf(writer, "    native_session_id: %s\n", yamlString(session.NativeSessionID))
		fmt.Fprintf(writer, "    provider: %s\n", yamlString(string(session.Provider)))
		fmt.Fprintf(writer, "    first_timestamp: %s\n", yamlOptional(session.FirstTimestamp))
		fmt.Fprintf(writer, "    last_timestamp: %s\n", yamlOptional(session.LastTimestamp))
		fmt.Fprintf(writer, "    project: %s\n", yamlString(session.Project))
		fmt.Fprintf(writer, "    cwd: %s\n", yamlString(fallback(session.CWD, "unknown")))
		fmt.Fprintf(writer, "    thread_kind: %s\n", yamlString(string(session.ThreadKind)))
		fmt.Fprintf(writer, "    source_sha256: %s\n", yamlString(session.SourceSHA256))
		fmt.Fprintf(writer, "    source_origin: %s\n", yamlString(session.Origin))
	}
	fmt.Fprintln(writer, "---")
	fmt.Fprintln(writer)
	fmt.Fprintf(writer, "# Full session export%s\n", structure)

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
			fmt.Fprintf(writer, "## Root session `%s`%s\n", session.SessionID, structure)
		} else {
			fmt.Fprintf(writer, "## Subagent session `%s`%s\n", session.SessionID, structure)
		}
		fmt.Fprintln(writer)
		writeSessionProvenance(writer, session, structure)
		if session.ContentWarning != "" {
			fmt.Fprintf(writer, "\n[transcript unavailable: %s]%s\n", oneLine(session.ContentWarning), structure)
			continue
		}
		records, err := Parse(session.Provider, session.SessionID, session.Raw)
		if err != nil {
			return counts, err
		}
		for _, record := range records {
			if err := writeMarkdownRecord(writer, record, options, structure, &counts); err != nil {
				return counts, err
			}
			for _, child := range childrenAt[session.SessionID][record.NativeID] {
				fmt.Fprintf(writer, "\n[delegated to subagent session `%s`]%s\n", child.SessionID, structure)
			}
		}
		for _, child := range childrenEnd[session.SessionID] {
			fmt.Fprintf(writer, "\n[delegated subagent session `%s`; spawn record not identified]%s\n", child.SessionID, structure)
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

func writeMarkdownRecord(writer io.Writer, record Record, options Options, structure string, counts *Counts) error {
	switch record.Kind {
	case KindMessage:
		fmt.Fprintf(writer, "\n### %s%s%s\n\n", titleRole(record.Role), timestampSuffix(record.Timestamp), structure)
		if record.Text != nil {
			if err := writeMessageBody(writer, *record.Text, structure); err != nil {
				return err
			}
		}
	case KindToolCall, KindToolResult:
		label := "tool call"
		if record.Kind == KindToolResult {
			label = "tool result"
		}
		fmt.Fprintf(writer, "\n[%s: %s, %d bytes]%s%s\n", label, oneLine(fallback(record.Name, "unknown")), record.Bytes, timestampSuffix(record.Timestamp), structure)
		if !options.IncludeToolOutput {
			counts.CollapsedToolRecords++
			return nil
		}
		writeFenced(writer, record.Text)
	case KindThinking:
		if !options.IncludeThinking {
			fmt.Fprintf(writer, "\n[thinking omitted: %d bytes]%s%s\n", record.Bytes, timestampSuffix(record.Timestamp), structure)
			counts.ExcludedThinking++
			return nil
		}
		fmt.Fprintf(writer, "\n[thinking]%s%s\n", timestampSuffix(record.Timestamp), structure)
		writeFenced(writer, record.Text)
	case KindSystem, KindMetadata:
		fmt.Fprintf(writer, "\n[%s record: %s]%s%s\n", record.Kind, oneLine(fallback(record.Name, "provider metadata")), timestampSuffix(record.Timestamp), structure)
	case KindUnrecognized:
		fmt.Fprintf(writer, "\n[unrecognized record]%s%s\n", timestampSuffix(record.Timestamp), structure)
		counts.UnrecognizedRecords++
	}
	return nil
}

func writeSessionProvenance(writer io.Writer, session Session, structure string) {
	fmt.Fprintf(writer, "- Provider: %s%s\n", markdownCodeSpan(string(session.Provider)), structure)
	fmt.Fprintf(writer, "- Native session ID: %s%s\n", markdownCodeSpan(fallback(session.NativeSessionID, "unknown")), structure)
	fmt.Fprintf(writer, "- Time range: %s to %s%s\n", markdownCodeSpan(optionalValue(session.FirstTimestamp)), markdownCodeSpan(optionalValue(session.LastTimestamp)), structure)
	fmt.Fprintf(writer, "- Project: %s%s\n", markdownCodeSpan(fallback(session.Project, "unknown")), structure)
	fmt.Fprintf(writer, "- CWD: %s%s\n", markdownCodeSpan(fallback(session.CWD, "unknown")), structure)
	fmt.Fprintf(writer, "- Thread kind: %s%s\n", markdownCodeSpan(string(session.ThreadKind)), structure)
	fmt.Fprintf(writer, "- Source: %s (%s)%s\n", markdownCodeSpan(fallback(session.SourceSHA256, "unavailable")), markdownCodeSpan(fallback(session.Origin, "unavailable")), structure)
}

func writeMessageBody(writer io.Writer, value, structure string) error {
	if _, err := io.WriteString(writer, value); err != nil {
		return err
	}
	if value != "" && !strings.HasSuffix(value, "\n") {
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
	}
	closeLine, label := markdownBlockAutoClose(value, structure)
	if closeLine != "" {
		if _, err := fmt.Fprintf(writer, "%s\n[%s auto-closed by exporter]%s\n", closeLine, label, structure); err != nil {
			return err
		}
	}
	return nil
}

func unterminatedMarkdownFence(value string) string {
	closeLine, label := markdownBlockAutoClose(value, "test-structure")
	if label != "fence" {
		return ""
	}
	return closeLine
}

var markdownStructureParser = goldmark.DefaultParser()

func markdownBlockAutoClose(value, structure string) (string, string) {
	marker := "tokenomnom-" + strings.Trim(structure, " {#}") + "-message-boundary"
	source := append([]byte(nil), value...)
	source = append(source, '\n', '\n')
	source = append(source, "# "+marker+"\n"...)
	document := markdownStructureParser.Parse(goldtext.NewReader(source))
	var visible bool
	var swallowedFence *goldast.FencedCodeBlock
	var swallowedHTML *goldast.HTMLBlock
	_ = goldast.Walk(document, func(node goldast.Node, entering bool) (goldast.WalkStatus, error) {
		if !entering {
			return goldast.WalkContinue, nil
		}
		switch typed := node.(type) {
		case *goldast.Heading:
			if string(typed.Text(source)) == marker {
				visible = true
			}
		case *goldast.FencedCodeBlock:
			if bytes.Contains(typed.Lines().Value(source), []byte(marker)) {
				swallowedFence = typed
			}
		case *goldast.HTMLBlock:
			if bytes.Contains(typed.Lines().Value(source), []byte(marker)) {
				swallowedHTML = typed
			}
		}
		return goldast.WalkContinue, nil
	})
	if visible {
		return "", ""
	}
	if swallowedFence != nil {
		return fencedBlockClose(source, swallowedFence), "fence"
	}
	if swallowedHTML != nil && !swallowedHTML.HasClosure() {
		return htmlBlockClose(source, swallowedHTML), "HTML block"
	}
	return "", ""
}

func fencedBlockClose(source []byte, block *goldast.FencedCodeBlock) string {
	if block.Lines().Len() == 0 {
		return ""
	}
	searchBefore := block.Lines().At(0).Start
	if block.Info != nil {
		searchBefore = block.Info.Segment.Start
	}
	lineEnd := lineStart(source, searchBefore)
	if block.Info != nil {
		lineEnd = lineEnd + bytes.IndexByte(source[lineEnd:], '\n')
		if lineEnd < lineStart(source, searchBefore) {
			lineEnd = len(source)
		}
	}
	for lineEnd > 0 {
		previousEnd := lineEnd
		if previousEnd > 0 && source[previousEnd-1] == '\n' {
			previousEnd--
		}
		lineBegin := lineStart(source, previousEnd)
		line := bytes.TrimSuffix(source[lineBegin:previousEnd], []byte{'\r'})
		for index := 0; index < len(line); index++ {
			marker := line[index]
			if marker != '`' && marker != '~' {
				continue
			}
			run := 1
			for index+run < len(line) && line[index+run] == marker {
				run++
			}
			if run < 3 {
				index += run - 1
				continue
			}
			prefix := line[:index]
			if len(bytes.Trim(prefix, " \t")) != 0 {
				prefix = bytes.Repeat([]byte{' '}, index)
			}
			return string(prefix) + strings.Repeat(string(marker), run)
		}
		lineEnd = lineBegin
	}
	return ""
}

func htmlBlockClose(source []byte, block *goldast.HTMLBlock) string {
	switch block.HTMLBlockType {
	case goldast.HTMLBlockType1:
		if block.Lines().Len() == 0 {
			return ""
		}
		firstLine := block.Lines().At(0)
		line := strings.TrimLeft(strings.ToLower(string(firstLine.Value(source))), " ")
		if !strings.HasPrefix(line, "<") {
			return ""
		}
		tagEnd := 1
		for tagEnd < len(line) && line[tagEnd] >= 'a' && line[tagEnd] <= 'z' {
			tagEnd++
		}
		switch tag := line[1:tagEnd]; tag {
		case "script", "pre", "style", "textarea":
			return "</" + tag + ">"
		}
	case goldast.HTMLBlockType2:
		return "-->"
	case goldast.HTMLBlockType3:
		return "?>"
	case goldast.HTMLBlockType4:
		return ">"
	case goldast.HTMLBlockType5:
		return "]]>"
	}
	return ""
}

func lineStart(source []byte, offset int) int {
	if offset > len(source) {
		offset = len(source)
	}
	if index := bytes.LastIndexByte(source[:offset], '\n'); index >= 0 {
		return index + 1
	}
	return 0
}

func exportStructureNonce(value string) (string, error) {
	if value == "" {
		raw := make([]byte, 8)
		if _, err := cryptorand.Read(raw); err != nil {
			return "", fmt.Errorf("generate Markdown structure nonce: %w", err)
		}
		return hex.EncodeToString(raw), nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) < 4 || len(decoded) > 16 || value != strings.ToLower(value) {
		return "", errors.New("Markdown structure nonce must be 8-32 lowercase hexadecimal characters")
	}
	return value, nil
}

func structureSuffix(nonce string) string {
	return " {#tok-" + nonce + "}"
}

func markdownCodeSpan(value string) string {
	value = strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(value)
	delimiter := "`"
	for strings.Contains(value, delimiter) {
		delimiter += "`"
	}
	padding := ""
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		padding = " "
	}
	return delimiter + padding + value + padding + delimiter
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
