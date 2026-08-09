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
	sessionMetaMarker = []byte(`"session_meta"`)
	turnContextMarker = []byte(`"turn_context"`)
	tokenCountMarker  = []byte(`"token_count"`)
)

// ParserStateVersion changes whenever persisted Codex state must be rebuilt
// from the beginning of a rollout to preserve usage accounting semantics.
const ParserStateVersion = 1

// Codex writes copied parent history in one re-stamped burst before a fork or
// subagent's first genuine turn. T3 Code observes gaps below 40ms in that burst
// and 5s or more before genuine usage; one second keeps the boundary explicit.
const forkCopyMaxGap = time.Second

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

	parser := NewParser(State{}, emit)
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			parser.ParseLine(line)
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			parser.FlushUnknown()
			return parser.Stats(), nil
		}
		return parser.Stats(), fmt.Errorf("read Codex session %q: %w", f.Path, readErr)
	}
}

// State is the parser context required to resume a growing Codex session.
type State struct {
	Version                int                 `json:"version"`
	Model                  string              `json:"model"`
	HasModel               bool                `json:"has_model"`
	Pending                []ingest.UsageEvent `json:"pending,omitempty"`
	PreviousSnapshot       *Snapshot           `json:"previous_snapshot,omitempty"`
	SawSessionMeta         bool                `json:"saw_session_meta,omitempty"`
	SuppressingForkCopies  bool                `json:"suppressing_fork_copies,omitempty"`
	ForkCopyAnchorUnixNano int64               `json:"fork_copy_anchor_unix_nano,omitempty"`
	AliasOf                string              `json:"alias_of,omitempty"`
	SplitContribution      bool                `json:"split_contribution,omitempty"`
	PrefixHash             string              `json:"prefix_hash,omitempty"`
	PrefixHashState        string              `json:"prefix_hash_state,omitempty"`
}

// Snapshot is a cumulative usage observation retained only for diagnostics.
type Snapshot struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

// Parser incrementally parses Codex JSONL records.
type Parser struct {
	emit                   func(ingest.UsageEvent)
	stats                  ingest.Stats
	model                  string
	hasModel               bool
	pending                []ingest.UsageEvent
	previousSnapshot       *tokenUsage
	sawSessionMeta         bool
	suppressingForkCopies  bool
	forkCopyAnchorUnixNano int64
	aliasOf                string
	splitContribution      bool
}

// NewParser restores a resumable parser state.
func NewParser(state State, emit func(ingest.UsageEvent)) *Parser {
	parser := &Parser{
		emit:                   emit,
		model:                  state.Model,
		hasModel:               state.HasModel,
		pending:                append([]ingest.UsageEvent(nil), state.Pending...),
		sawSessionMeta:         state.SawSessionMeta,
		suppressingForkCopies:  state.SuppressingForkCopies,
		forkCopyAnchorUnixNano: state.ForkCopyAnchorUnixNano,
		aliasOf:                state.AliasOf,
		splitContribution:      state.SplitContribution,
	}
	if state.PreviousSnapshot != nil {
		value := tokenUsage(*state.PreviousSnapshot)
		parser.previousSnapshot = &value
	}
	return parser
}

// ParseLine consumes one JSONL record.
func (p *Parser) ParseLine(line []byte) {
	p.stats.Lines++
	p.parseLine(line)
}

// Stats returns diagnostics accumulated since this Parser was created.
func (p *Parser) Stats() ingest.Stats { return p.stats }

// State snapshots the context needed to resume at the next complete line.
func (p *Parser) State() State {
	state := State{
		Version:                ParserStateVersion,
		Model:                  p.model,
		HasModel:               p.hasModel,
		Pending:                append([]ingest.UsageEvent(nil), p.pending...),
		SawSessionMeta:         p.sawSessionMeta,
		SuppressingForkCopies:  p.suppressingForkCopies,
		ForkCopyAnchorUnixNano: p.forkCopyAnchorUnixNano,
		AliasOf:                p.aliasOf,
		SplitContribution:      p.splitContribution,
	}
	if p.previousSnapshot != nil {
		value := Snapshot(*p.previousSnapshot)
		state.PreviousSnapshot = &value
	}
	return state
}

type envelope struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ForkedFromID string          `json:"forked_from_id"`
	ThreadSource string          `json:"thread_source"`
	Source       json.RawMessage `json:"source"`
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

func (p *Parser) parseLine(line []byte) {
	if !bytes.Contains(line, sessionMetaMarker) && !bytes.Contains(line, turnContextMarker) && !bytes.Contains(line, tokenCountMarker) {
		return
	}

	var event envelope
	if err := json.Unmarshal(line, &event); err != nil {
		p.stats.MalformedLines++
		return
	}

	switch event.Type {
	case "session_meta":
		p.parseSessionMeta(event)
	case "turn_context":
		p.parseTurnContext(event.Payload)
	case "event_msg":
		p.parseEventMessage(event)
	}
}

func (p *Parser) parseSessionMeta(event envelope) {
	// A fork repeats ancestor metadata after its own first record. Only the first
	// session_meta describes the rollout being parsed.
	if p.sawSessionMeta {
		return
	}
	var payload sessionMetaPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		p.stats.MalformedLines++
		return
	}
	p.sawSessionMeta = true
	if !isForkedSessionMeta(payload) {
		return
	}

	p.suppressingForkCopies = true
	timestamp, err := time.Parse(time.RFC3339, event.Timestamp)
	if err != nil {
		p.stats.MalformedLines++
		return
	}
	p.forkCopyAnchorUnixNano = timestamp.UnixNano()
}

func isForkedSessionMeta(payload sessionMetaPayload) bool {
	if payload.ForkedFromID != "" || payload.ThreadSource == "subagent" {
		return true
	}
	if len(payload.Source) == 0 || bytes.Equal(payload.Source, []byte("null")) {
		return false
	}
	var source struct {
		Subagent *struct {
			ThreadSpawn *struct {
				ParentThreadID string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	if json.Unmarshal(payload.Source, &source) != nil || source.Subagent == nil || source.Subagent.ThreadSpawn == nil {
		return false
	}
	return source.Subagent.ThreadSpawn.ParentThreadID != ""
}

// StartsForkedSession reports whether a rollout's first JSONL record identifies
// a fork or delegated subagent. It is intentionally bounded to one record so a
// sync can find old affected checkpoints without reparsing every Codex file.
func StartsForkedSession(line []byte) bool {
	var event envelope
	if json.Unmarshal(line, &event) != nil || event.Type != "session_meta" {
		return false
	}
	var payload sessionMetaPayload
	return json.Unmarshal(event.Payload, &payload) == nil && isForkedSessionMeta(payload)
}

func (p *Parser) parseTurnContext(raw json.RawMessage) {
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

func (p *Parser) parseEventMessage(event envelope) {
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
	if p.shouldSuppressForkCopy(timestamp) {
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

func (p *Parser) shouldSuppressForkCopy(timestamp time.Time) bool {
	if !p.suppressingForkCopies {
		return false
	}
	if p.forkCopyAnchorUnixNano == 0 {
		p.forkCopyAnchorUnixNano = timestamp.UnixNano()
		return true
	}

	anchor := time.Unix(0, p.forkCopyAnchorUnixNano)
	if timestamp.Sub(anchor) < forkCopyMaxGap {
		p.forkCopyAnchorUnixNano = timestamp.UnixNano()
		return true
	}
	p.suppressingForkCopies = false
	p.forkCopyAnchorUnixNano = 0
	return false
}

func (p *Parser) recordSnapshot(current tokenUsage) {
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

// FlushUnknown emits buffered pre-model events as unknown at a definitive EOF.
func (p *Parser) FlushUnknown() {
	for _, event := range p.pending {
		p.emitEvent(event, "unknown")
	}
	p.stats.UnknownModelEvents += len(p.pending)
	p.pending = nil
}

func (p *Parser) emitEvent(event ingest.UsageEvent, model string) {
	event.Model = model
	p.emit(event)
	p.stats.EmittedEvents++
}
