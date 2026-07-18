# HANDOFF — PR 3: Ingestion interface + Codex adapter

You are Codex, the implementer for this PR. You have no memory of prior work.
Everything you need is here plus two in-repo references:

1. `DESIGN.md` — architecture; you are PR 3 in the §13 table. Read §5
   (Ingestion) closely.
2. `archive/2026-07-18-snapshot/build_codex_usage_csv.mjs` — the **proven
   reference implementation** of Codex extraction (JavaScript). Your Go
   adapter must replicate its attribution semantics exactly. The archived
   `archive/2026-07-18-snapshot/HANDOFF.md` explains the reasoning behind
   those semantics ("How Codex extraction works").

Claude reviews your PR; **Janior approves and merges — never merge yourself.**

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reconstructs local coding-agent token usage
and prices it at API list rates. PR 2 built discovery (finding `*.jsonl`
session files). This PR teaches the tool to *parse Codex session files* into
normalized usage events. Claude parsing is PR 4; storage/aggregation are PRs
5–6. No CLI changes in this PR — it is a pure library + tests.

## Current repo state (main after PR 2)

- Module `github.com/janiorvalle/tokenomnom`, dependency: cobra only.
- `internal/discover`: `Provider` (`ProviderCodex`/`ProviderClaude`), `Root`,
  `SourceFile{Provider, Path, Size, ModTime}`, `Resolve(...)`,
  `ListSourceFiles(Root) ([]SourceFile, []error)`.
- `internal/cli`: root command + `doctor`. `internal/version`.
- Makefile `verify` = build+vet+test; CI on ubuntu/macos/windows. All green.

## Deliverables

### 1. `internal/ingest` — shared types (provider-agnostic)

```go
// UsageEvent is one normalized token-usage observation.
type UsageEvent struct {
    Timestamp    time.Time // event time, parsed as UTC instant
    Provider     discover.Provider
    Model        string // never empty; "unknown" when unrecoverable
    Input        int64  // total input incl. cache components
    CacheRead    int64
    CacheWrite5m int64
    CacheWrite1h int64
    Output       int64 // includes reasoning
    Reasoning    int64 // informational subset (Codex exposes; Claude zero)
}

// Stats reports per-file parse diagnostics.
type Stats struct {
    Lines               int
    TokenEvents         int   // token_count events with usable usage
    EmittedEvents       int
    MalformedLines      int
    CounterResets       int
    DuplicateSnapshots  int
    LastUsageMismatches int
    BufferedBeforeModel int
    UnknownModelEvents  int
}

type Adapter interface {
    Name() string
    // ParseFile streams one session file, calling emit for each usage event.
    // It never reads other files and keeps memory bounded.
    ParseFile(f discover.SourceFile, emit func(UsageEvent)) (Stats, error)
}
```

Shape may be refined, but keep: streaming emit (no returned slices of
events), per-file stats, and no cross-file state inside an adapter call.

Cache-write bucket rule (document on the type): providers with a single
cache-write class put the combined value in `CacheWrite5m` and leave
`CacheWrite1h` zero — the OpenAI pricing table charges one write rate, so the
split only matters for Claude (PR 4).

### 2. `internal/ingest/codex` — the Codex adapter

Codex session files are JSONL. Only two line types matter; skip everything
else cheaply (`bytes.Contains` pre-filter for `"turn_context"` /
`"token_count"` before JSON-unmarshaling a line).

Real event shapes (observed 2026-07-18; parse defensively, unknown extra
fields are always allowed):

Model context — sets the active model for subsequent token events:

```json
{"timestamp":"2026-03-02T10:07:58.522Z","type":"turn_context",
 "payload":{"model":"gpt-5.3-codex-spark", "...":"..."}}
```

Token count — NOTE the envelope: top-level `type` is `"event_msg"`, and the
event kind is `payload.type`. `info` may be **null** (rate-limit-only ping):

```json
{"timestamp":"2026-04-23T22:13:04.356Z","type":"event_msg",
 "payload":{"type":"token_count","info":{
   "total_token_usage":{"input_tokens":36522,"cached_input_tokens":2432,
     "output_tokens":606,"reasoning_output_tokens":516,"total_tokens":37128},
   "last_token_usage":{"input_tokens":36522,"cached_input_tokens":2432,
     "output_tokens":606,"reasoning_output_tokens":516,"total_tokens":37128},
   "model_context_window":258400},
  "rate_limits":{"...":"..."}}}
```

A `cache_write_input_tokens` field may appear inside the usage objects;
treat every usage field as optional, defaulting to 0.

Attribution rules — mirror `build_codex_usage_csv.mjs` exactly:

1. `turn_context`: model = `payload.model` trimmed; empty → `"unknown"`.
   Becomes the file's current model. Flush any buffered pending usage to it.
2. `token_count`: skip unless `payload.type == "token_count"` AND `info` is
   non-null AND both `info.total_token_usage` and `info.last_token_usage`
   exist. Usage comes from **`last_token_usage` only** — never sum
   `total_token_usage` (cumulative; summing double-counts).
3. Recompute `total = input + output`; count a `LastUsageMismatch` when the
   recorded total differs. Skip events whose recomputed total is ≤ 0.
4. Diagnostics vs the previous `total_token_usage` snapshot in the same file:
   any field lower than before → `CounterResets` +1; all fields equal →
   `DuplicateSnapshots` +1. Diagnostics only — never changes emission.
5. Token events arriving before any `turn_context` are buffered; flushed to
   the first model seen. Any still buffered at EOF emit with model
   `"unknown"` and count in `UnknownModelEvents`. Never redistribute or drop.
6. Unparseable lines (bad JSON on a pre-filtered line, envelope missing the
   split): `MalformedLines` +1, continue. A malformed line never aborts the
   file.
7. Timestamps: RFC 3339 (`event.timestamp`). Unparsable timestamp on an
   otherwise-valid token event → treat the event as malformed (count, skip).
   Do NOT bucket into days here — later layers own timezone policy.

Field mapping to `UsageEvent`: Input=`input_tokens`,
CacheRead=`cached_input_tokens`, CacheWrite5m=`cache_write_input_tokens`
(usually absent → 0), CacheWrite1h=0, Output=`output_tokens`,
Reasoning=`reasoning_output_tokens`.

### 3. Robustness requirements

- **Long lines are real.** `turn_context` embeds full user instructions;
  lines exceed `bufio.Scanner`'s 64 KiB default. Use a reader strategy with
  no fixed line limit (or a Scanner buffer grown to a documented generous
  cap, treating overflow as a malformed line — not a crash). A fixture must
  include a >64 KiB line that parses correctly.
- Streaming only: O(1) memory in file size (buffered pending events are the
  only per-file accumulation). Never `os.ReadFile` a session.
- Portability: no absolute paths, fixtures via `t.TempDir()` or
  `testdata/`, tests green on ubuntu/macos/windows CI.

### 4. Fixtures + tests (`testdata/` under the codex package)

Hand-written synthetic JSONL fixtures (do NOT copy real session content —
real logs contain private data). Cover at least:

- model context before token events (normal flow)
- token events before model context (buffer + flush)
- file ending with buffered events (→ `unknown`)
- `info: null` token_count (skipped)
- malformed JSON line mid-file (counted, parsing continues)
- counter reset and duplicate snapshot (diagnostic counters)
- recorded total ≠ input+output (mismatch counter, recomputed emission)
- zero/negative recomputed total (skipped)
- unrelated event types interleaved (ignored)
- the >64 KiB line case
- multiple models in one file (later `turn_context` switches attribution)

Assert both emitted events (timestamp, model, all six token fields) and the
full `Stats` struct per fixture.

## Out of scope — do NOT touch

- No Claude adapter (PR 4). No SQLite, aggregation, pricing, CLI wiring, or
  day-bucketing/timezone logic.
- No new dependencies — stdlib + existing only.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile, README, or `internal/discover`. (If the adapter interface truly
  needs a discover change, stop and flag it in the PR body instead.)

## Acceptance criteria

- `make verify` and `go test -race ./...` green locally; gofmt clean; CI
  green on all three OSes.
- Every attribution rule above has a fixture proving it.
- Reviewer will additionally run the adapter against real local logs and
  diff aggregate totals per model against the archived reference CSV over
  the snapshot window — semantic drift from the `.mjs` reference will fail
  review, so when in doubt, match the reference.

## Process

1. Branch from `main`: `pr3-codex-adapter`.
2. Conventional commits.
3. PR title `feat: ingestion interface and codex adapter (PR 3)` via
   `gh pr create`; list any deviations from this handoff explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
