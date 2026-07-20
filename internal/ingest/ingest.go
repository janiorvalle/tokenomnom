// Package ingest defines provider-neutral token usage observations and parsers.
package ingest

import (
	"time"

	"github.com/janiorvalle/tokenomnom/internal/discover"
)

// UsageEvent is one normalized token-usage observation. Providers with a
// single cache-write class record it in CacheWrite5m and leave CacheWrite1h
// zero; cache writes that cannot be classified by duration remain explicit in
// CacheWriteUnclassified for later pricing decisions.
type UsageEvent struct {
	Timestamp              time.Time // Event time as a UTC instant.
	Provider               discover.Provider
	Model                  string // Never empty; "unknown" when unrecoverable.
	Input                  int64  // Total input, including cache components.
	CacheRead              int64
	CacheWrite5m           int64
	CacheWrite1h           int64
	CacheWriteUnclassified int64 // Cache creation not identified as 5m or 1h.
	Output                 int64 // Includes reasoning tokens.
	Reasoning              int64 // Informational subset; zero when unavailable.
}

// Stats reports diagnostics from parsing one source file.
type Stats struct {
	Lines               int
	TokenEvents         int // Token-count events with usable usage objects.
	EmittedEvents       int
	MalformedLines      int
	CounterResets       int
	DuplicateSnapshots  int
	LastUsageMismatches int
	BufferedBeforeModel int
	UnknownModelEvents  int
}

// Adapter streams normalized usage events from provider session files.
type Adapter interface {
	Name() string
	ParseFile(f discover.SourceFile, emit func(UsageEvent)) (Stats, error)
}
