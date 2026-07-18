// Package codex parses Codex JSONL session files into normalized usage events.
package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
)

var _ ingest.Adapter = Adapter{}

var (
	turnContextMarker = []byte(`"turn_context"`)
	tokenCountMarker  = []byte(`"token_count"`)
)

// Adapter parses Codex session files.
type Adapter struct{}

// Name returns the provider name.
func (Adapter) Name() string {
	return string(discover.ProviderCodex)
}

// ParseFile streams normalized usage from one Codex JSONL session file.
func (Adapter) ParseFile(f discover.SourceFile, emit func(ingest.UsageEvent)) (ingest.Stats, error) {
	file, err := os.Open(f.Path)
	if err != nil {
		return ingest.Stats{}, fmt.Errorf("open Codex session %q: %w", f.Path, err)
	}
	defer file.Close()

	parser := fileParser{emit: emit}
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			parser.stats.Lines++
			parser.parseLine(line)
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			parser.flushUnknown()
			return parser.stats, nil
		}
		return parser.stats, fmt.Errorf("read Codex session %q: %w", f.Path, readErr)
	}
}

type fileParser struct {
	emit             func(ingest.UsageEvent)
	stats            ingest.Stats
	model            string
	hasModel         bool
	pending          []ingest.UsageEvent
	previousSnapshot *tokenUsage
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type turnContextPayload struct {
	Model string `json:"model"`
}

type eventPayload struct {
	Type string     `json:"type"`
	Info *tokenInfo `json:"info"`
}

type tokenInfo struct {
	TotalTokenUsage *tokenUsage `json:"total_token_usage"`
	LastTokenUsage  *tokenUsage `json:"last_token_usage"`
}

type tokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

func (p *fileParser) parseLine(line []byte) {
	if !bytes.Contains(line, turnContextMarker) && !bytes.Contains(line, tokenCountMarker) {
		return
	}

	var event envelope
	if err := json.Unmarshal(line, &event); err != nil {
		p.stats.MalformedLines++
		return
	}

	switch event.Type {
	case "turn_context":
		p.parseTurnContext(event.Payload)
	case "event_msg":
		p.parseEventMessage(event)
	}
}

func (p *fileParser) parseTurnContext(raw json.RawMessage) {
	var payload turnContextPayload
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &payload); err != nil {
			p.stats.MalformedLines++
			return
		}
	}

	p.model = strings.TrimSpace(payload.Model)
	if p.model == "" {
		p.model = "unknown"
	}
	p.hasModel = true
	for _, event := range p.pending {
		p.emitEvent(event, p.model)
	}
	p.pending = nil
}

func (p *fileParser) parseEventMessage(event envelope) {
	var payload eventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		p.stats.MalformedLines++
		return
	}
	if payload.Type != "token_count" || payload.Info == nil ||
		payload.Info.TotalTokenUsage == nil || payload.Info.LastTokenUsage == nil {
		return
	}

	p.stats.TokenEvents++
	p.recordSnapshot(*payload.Info.TotalTokenUsage)

	usage := *payload.Info.LastTokenUsage
	total := usage.InputTokens + usage.OutputTokens
	if usage.TotalTokens != total {
		p.stats.LastUsageMismatches++
	}
	if total <= 0 {
		return
	}

	timestamp, err := time.Parse(time.RFC3339, event.Timestamp)
	if err != nil {
		p.stats.MalformedLines++
		return
	}

	normalized := ingest.UsageEvent{
		Timestamp:    timestamp.UTC(),
		Provider:     discover.ProviderCodex,
		Input:        usage.InputTokens,
		CacheRead:    usage.CachedInputTokens,
		CacheWrite5m: usage.CacheWriteInputTokens,
		Output:       usage.OutputTokens,
		Reasoning:    usage.ReasoningOutputTokens,
	}
	if p.hasModel {
		p.emitEvent(normalized, p.model)
		return
	}

	p.pending = append(p.pending, normalized)
	p.stats.BufferedBeforeModel++
}

func (p *fileParser) recordSnapshot(current tokenUsage) {
	if p.previousSnapshot != nil {
		if current.lessThan(*p.previousSnapshot) {
			p.stats.CounterResets++
		}
		if current == *p.previousSnapshot {
			p.stats.DuplicateSnapshots++
		}
	}
	p.previousSnapshot = &current
}

func (u tokenUsage) lessThan(other tokenUsage) bool {
	return u.InputTokens < other.InputTokens ||
		u.CachedInputTokens < other.CachedInputTokens ||
		u.CacheWriteInputTokens < other.CacheWriteInputTokens ||
		u.OutputTokens < other.OutputTokens ||
		u.ReasoningOutputTokens < other.ReasoningOutputTokens ||
		u.TotalTokens < other.TotalTokens
}

func (p *fileParser) flushUnknown() {
	for _, event := range p.pending {
		p.emitEvent(event, "unknown")
	}
	p.stats.UnknownModelEvents += len(p.pending)
	p.pending = nil
}

func (p *fileParser) emitEvent(event ingest.UsageEvent, model string) {
	event.Model = model
	p.emit(event)
	p.stats.EmittedEvents++
}
