// Package claude parses Claude Code JSONL transcripts and globally deduplicates
// progressive assistant-message snapshots.
package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
	"github.com/janiorvalle/tokenomnom/internal/ingest"
)

var usageMarker = []byte(`"usage":{`)

// Adapter parses individual Claude Code transcript files into candidates.
// Final usage events must be produced through a shared Deduper so duplicates
// spanning multiple files are counted only once.
type Adapter struct{}

// Name returns the provider name.
func (Adapter) Name() string {
	return string(discover.ProviderClaude)
}

// FileStats reports diagnostics from parsing one Claude transcript file.
type FileStats struct {
	Lines             int
	MatchedLines      int // Lines containing the exact usage marker.
	Candidates        int
	MalformedLines    int
	MissingMessageIDs int
}

// Iteration is one normalized model-specific usage iteration.
type Iteration struct {
	Model         string
	RawInput      int64
	CacheRead     int64
	CacheCreation int64
	CacheWrite5m  int64
	CacheWrite1h  int64
	Output        int64
}

// Candidate is one assistant message snapshot awaiting global deduplication.
// Score is the complete usage score used to compare progressive snapshots.
type Candidate struct {
	MessageID  string
	Timestamp  time.Time
	Iterations []Iteration
	Score      int64
}

// ParseFile streams candidates from one Claude Code JSONL transcript.
func (Adapter) ParseFile(f discover.SourceFile, collect func(Candidate)) (FileStats, error) {
	file, err := os.Open(f.Path)
	if err != nil {
		return FileStats{}, fmt.Errorf("open Claude transcript %q: %w", f.Path, err)
	}
	defer file.Close()

	var stats FileStats
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			stats.Lines++
			parseLine(line, collect, &stats)
		}

		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return stats, nil
		}
		return stats, fmt.Errorf("read Claude transcript %q: %w", f.Path, readErr)
	}
}

type envelope struct {
	Timestamp string   `json:"timestamp"`
	Type      string   `json:"type"`
	Message   *message `json:"message"`
}

type message struct {
	ID    string    `json:"id"`
	Model string    `json:"model"`
	Usage *rawUsage `json:"usage"`
}

type rawUsage struct {
	InputTokens              int64           `json:"input_tokens"`
	CacheReadInputTokens     int64           `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64           `json:"cache_creation_input_tokens"`
	OutputTokens             int64           `json:"output_tokens"`
	CacheCreation            cacheCreation   `json:"cache_creation"`
	Iterations               json.RawMessage `json:"iterations"`
}

type rawIteration struct {
	Model                    string        `json:"model"`
	InputTokens              int64         `json:"input_tokens"`
	CacheReadInputTokens     int64         `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64         `json:"cache_creation_input_tokens"`
	OutputTokens             int64         `json:"output_tokens"`
	CacheCreation            cacheCreation `json:"cache_creation"`
}

type cacheCreation struct {
	Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
}

func parseLine(line []byte, collect func(Candidate), stats *FileStats) {
	if !bytes.Contains(line, usageMarker) {
		return
	}
	stats.MatchedLines++

	var event envelope
	if err := json.Unmarshal(line, &event); err != nil {
		stats.MalformedLines++
		return
	}
	if event.Type != "assistant" || event.Message == nil || event.Message.Usage == nil || event.Timestamp == "" {
		return
	}
	if event.Message.ID == "" {
		stats.MissingMessageIDs++
		return
	}

	timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
	if err != nil {
		stats.MalformedLines++
		return
	}

	iterations, err := normalizeIterations(event.Message)
	if err != nil {
		stats.MalformedLines++
		return
	}
	candidate := Candidate{
		MessageID:  event.Message.ID,
		Timestamp:  timestamp.UTC(),
		Iterations: iterations,
		Score:      usageScore(iterations),
	}
	collect(candidate)
	stats.Candidates++
}

func normalizeIterations(message *message) ([]Iteration, error) {
	usage := message.Usage
	if len(usage.Iterations) > 0 && firstNonSpace(usage.Iterations) == '[' {
		var raw []rawIteration
		if err := json.Unmarshal(usage.Iterations, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			iterations := make([]Iteration, 0, len(raw))
			for _, item := range raw {
				iterations = append(iterations, normalizeIteration(item, message.Model))
			}
			return iterations, nil
		}
	}

	return []Iteration{normalizeIteration(rawIteration{
		InputTokens:              usage.InputTokens,
		CacheReadInputTokens:     usage.CacheReadInputTokens,
		CacheCreationInputTokens: usage.CacheCreationInputTokens,
		OutputTokens:             usage.OutputTokens,
		CacheCreation:            usage.CacheCreation,
	}, message.Model)}, nil
}

func firstNonSpace(value []byte) byte {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

func normalizeIteration(item rawIteration, fallbackModel string) Iteration {
	model := item.Model
	if model == "" {
		model = fallbackModel
	}
	if model == "" {
		model = "unknown"
	}
	return Iteration{
		Model:         model,
		RawInput:      item.InputTokens,
		CacheRead:     item.CacheReadInputTokens,
		CacheCreation: item.CacheCreationInputTokens,
		CacheWrite5m:  item.CacheCreation.Ephemeral5mInputTokens,
		CacheWrite1h:  item.CacheCreation.Ephemeral1hInputTokens,
		Output:        item.OutputTokens,
	}
}

func usageScore(iterations []Iteration) int64 {
	var score int64
	for _, item := range iterations {
		score += item.RawInput + item.CacheRead + item.CacheCreation + item.Output
	}
	return score
}

// DedupeStats reports diagnostics across all candidates added to a Deduper.
type DedupeStats struct {
	RetainedMessages             int
	DuplicateRecords             int
	DifferingDuplicateRecords    int
	CacheWriteUnclassifiedTokens int64
}

// Deduper retains one highest-scoring candidate per message ID.
type Deduper struct {
	byID                      map[string]Candidate
	order                     []string
	duplicateRecords          int
	differingDuplicateRecords int
}

// NewDeduper creates an empty global Claude message deduper.
func NewDeduper() *Deduper {
	return &Deduper{byID: make(map[string]Candidate)}
}

// Add applies the global progressive-snapshot dedupe rules to a candidate.
func (d *Deduper) Add(candidate Candidate) {
	candidate = cloneCandidate(candidate)
	candidate.Score = usageScore(candidate.Iterations)

	existing, found := d.byID[candidate.MessageID]
	if !found {
		d.byID[candidate.MessageID] = candidate
		d.order = append(d.order, candidate.MessageID)
		return
	}

	d.duplicateRecords++
	if candidate.Score != existing.Score {
		d.differingDuplicateRecords++
	}
	if candidate.Score <= existing.Score {
		return
	}
	if existing.Timestamp.Before(candidate.Timestamp) {
		candidate.Timestamp = existing.Timestamp
	}
	d.byID[candidate.MessageID] = candidate
}

// Stats returns diagnostics for the currently retained candidate set.
func (d *Deduper) Stats() DedupeStats {
	stats := DedupeStats{
		RetainedMessages:          len(d.byID),
		DuplicateRecords:          d.duplicateRecords,
		DifferingDuplicateRecords: d.differingDuplicateRecords,
	}
	d.eachRetained(func(candidate Candidate) {
		for _, item := range candidate.Iterations {
			stats.CacheWriteUnclassifiedTokens += unclassifiedCacheWrite(item)
		}
	})
	return stats
}

// Retained visits a copy of each retained candidate in first-seen order.
func (d *Deduper) Retained(visit func(Candidate)) {
	d.eachRetained(func(candidate Candidate) {
		visit(cloneCandidate(candidate))
	})
}

// Emit converts retained candidates to normalized usage events in first-seen
// message order and iteration order.
func (d *Deduper) Emit(emit func(ingest.UsageEvent)) {
	d.eachRetained(func(candidate Candidate) {
		for _, item := range candidate.Iterations {
			totalInput := item.RawInput + item.CacheRead + item.CacheCreation
			if totalInput+item.Output <= 0 {
				continue
			}
			emit(ingest.UsageEvent{
				Timestamp:              candidate.Timestamp.UTC(),
				Provider:               discover.ProviderClaude,
				Model:                  item.Model,
				Input:                  totalInput,
				CacheRead:              item.CacheRead,
				CacheWrite5m:           item.CacheWrite5m,
				CacheWrite1h:           item.CacheWrite1h,
				CacheWriteUnclassified: unclassifiedCacheWrite(item),
				Output:                 item.Output,
				Reasoning:              0,
			})
		}
	})
}

func (d *Deduper) eachRetained(visit func(Candidate)) {
	for _, messageID := range d.order {
		visit(d.byID[messageID])
	}
}

func cloneCandidate(candidate Candidate) Candidate {
	candidate.Iterations = append([]Iteration(nil), candidate.Iterations...)
	return candidate
}

func unclassifiedCacheWrite(item Iteration) int64 {
	unclassified := item.CacheCreation - item.CacheWrite5m - item.CacheWrite1h
	if unclassified < 0 {
		return 0
	}
	return unclassified
}
