# HANDOFF — PR 4: Claude adapter (parse + global dedupe)

You are Codex, the implementer for this PR. You have no memory of prior work.
Everything you need is here plus two in-repo references:

1. `DESIGN.md` — architecture; you are PR 4 in the §13 table (§5 Ingestion).
2. `archive/2026-07-18-snapshot/build_claude_usage_csv.mjs` — the **proven
   reference implementation** (JavaScript). Your Go code must replicate its
   semantics exactly. The archived `HANDOFF.md` ("How Claude extraction
   works") explains why.

Claude reviews your PR; **Janior approves and merges — never merge yourself.**

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reconstructs local coding-agent token usage.
PR 3 built `internal/ingest` (shared `UsageEvent`, `Stats`, `Adapter`) and the
Codex adapter. This PR adds the Claude Code adapter. Storage and aggregation
are PRs 5–6; no CLI changes here — pure library + tests.

## Current repo state (main after PR 3)

- `internal/discover`: `Provider`, `Root`, `SourceFile{Provider, Path, Size,
  ModTime}`, `Resolve`, `ListSourceFiles`.
- `internal/ingest`: `UsageEvent{Timestamp, Provider, Model, Input,
  CacheRead, CacheWrite5m, CacheWrite1h, Output, Reasoning}`, `Stats`,
  `Adapter` (per-file streaming `ParseFile(f, emit)`).
- `internal/ingest/codex`: the Codex adapter (good style reference for
  streaming reader with no line cap, marker pre-filter, fixtures layout).
- CI green on ubuntu/macos/windows; Makefile `verify`; cobra-only deps.

## Why Claude is architecturally different (read this first)

Claude transcripts contain **multiple progressive copies of the same
assistant message** — replayed across writes and even across files. Usage
must be counted **once per unique `message.id`**, keeping the copy with the
largest usage score. That dedupe is GLOBAL (cross-file), so the Claude
adapter cannot emit final `UsageEvent`s file-by-file the way Codex does.

Therefore this PR ships a **two-stage API** in `internal/ingest/claude`
(it deliberately does not implement `ingest.Adapter`):

```go
// Stage 1 — per file, streaming:
func (Adapter) ParseFile(f discover.SourceFile, collect func(Candidate)) (FileStats, error)

// Stage 2 — global:
type Deduper struct{ ... }
func NewDeduper() *Deduper
func (d *Deduper) Add(c Candidate)             // apply dedupe rules
func (d *Deduper) Stats() DedupeStats
func (d *Deduper) Emit(emit func(ingest.UsageEvent)) // convert retained → events
```

Exact shapes are yours, but keep: streaming stage 1, explicit global stage 2,
and stats for both stages. (PR 5 will persist dedupe state; do not build
persistence now, just don't hide the retained set behind an unexportable
design — an iteration/accessor method is enough.)

## Transcript format (observed 2026-07-18; parse defensively)

Files: `projects/<escaped-project-dir>/<session-uuid>.jsonl`. One JSON object
per line. Only lines with an assistant message carrying usage matter —
pre-filter with `bytes.Contains(line, []byte("\"usage\":{"))` (same fixed
string the reference used), then unmarshal.

```json
{"timestamp":"2026-07-18T14:22:31.123Z","type":"assistant","uuid":"...",
 "sessionId":"...","requestId":"...","isSidechain":false,
 "message":{"id":"msg_011Cd24v...","type":"message","role":"assistant",
   "model":"claude-fable-5",
   "usage":{
     "input_tokens":2,
     "cache_creation_input_tokens":23249,
     "cache_read_input_tokens":19426,
     "output_tokens":344,
     "service_tier":"standard","speed":"standard",
     "cache_creation":{"ephemeral_1h_input_tokens":23249,
                       "ephemeral_5m_input_tokens":0},
     "iterations":[
       {"input_tokens":2,"output_tokens":344,
        "cache_read_input_tokens":19426,
        "cache_creation_input_tokens":23249,
        "cache_creation":{"ephemeral_5m_input_tokens":0,
                          "ephemeral_1h_input_tokens":23249},
        "type":"message"}]}}}
```

Notes: iteration entries MAY carry their own `model` (fallback/multi-model
turns) and often omit it; `cache_creation` may be absent; extra fields are
always allowed; do not filter on `isSidechain` (the reference did not).

## Rules — mirror `build_claude_usage_csv.mjs` exactly

### Line acceptance (stage 1)

Accept only: top-level `type == "assistant"` AND `message.usage` present AND
top-level `timestamp` present. Then require `message.id`: missing id →
`MissingMessageIDs` +1, skip. Bad JSON on a pre-filtered line →
`MalformedLines` +1, continue. Unparsable timestamp → malformed, skip.

### Normalized iterations

`iters = usage.iterations` if it is a non-empty array, else `[usage]` (one
pseudo-iteration from the top-level usage). For each: model =
`iteration.model`, else `message.model`, else `"unknown"`; numeric fields
(default 0): `input_tokens` (raw), `cache_read_input_tokens`,
`cache_creation_input_tokens`, `cache_creation.ephemeral_5m_input_tokens`,
`cache_creation.ephemeral_1h_input_tokens`, `output_tokens`.

### Usage score & dedupe (stage 2)

`score = Σ over iterations of (raw + cacheRead + cacheCreation + output)`.

Per `message.id`: first candidate is retained. A later candidate with score
**strictly greater** replaces the retained one, but the retained timestamp
becomes the **earlier** of the two (usage stays on the original day). Equal
or lower score → keep existing. Count every non-first sighting in
`DuplicateRecords`; count those whose score differs from the retained one at
comparison time in `DifferingDuplicateRecords`.

### Event conversion (stage 2 emit)

Per retained message, per normalized iteration:

- `totalInput = raw + cacheRead + cacheCreation`
- skip the iteration when `totalInput + output <= 0`
- emit `ingest.UsageEvent{Timestamp: message timestamp (UTC),
  Provider: claude, Model: iteration model, Input: totalInput,
  CacheRead: cacheRead, CacheWrite5m: 5m, CacheWrite1h: 1h,
  Output: output, Reasoning: 0}`

Claude does not expose reasoning separately — always 0. No day-bucketing, no
date filtering, no aggregation here (later layers own those).

### Unclassified cache writes — keep explicit, never fold

`unclassified = max(0, cacheCreation - 5m - 1h)` per iteration. The frozen
snapshot had zero unclassified tokens, but if a future log breaks the
invariant it must be VISIBLE, not silently mispriced. Add
`CacheWriteUnclassified int64` to `ingest.UsageEvent` (document it; Codex
adapter leaves it 0 — one-line touch to that file's doc comment is in scope)
and a dedupe-stats counter for total unclassified tokens. Pricing (PR 7)
decides how to charge it; your job is only to surface it.

## Robustness requirements

- **Huge lines are routine** — Claude transcript lines can be multiple MB
  (embedded file contents, base64 images). No line-length cap; follow the
  Codex adapter's reader approach.
- Streaming stage 1: O(1) memory in file size. Stage 2 holds one retained
  candidate per unique message id (that is the design, not a leak).
- Portability: no absolute paths; `t.TempDir()`/`testdata/` fixtures;
  green on ubuntu/macos/windows CI. Fixtures are synthetic — never copy
  real transcript content (private data).

## Fixtures + tests

Cover at least: normal message; duplicate with equal score (keep first);
later higher-score duplicate (replace, keep earliest timestamp); later
lower-score duplicate (ignore); missing `message.id`; missing usage;
non-assistant lines; malformed JSON mid-file; `iterations` with per-iteration
models (multi-model attribution); iterations lacking `model` (message.model
fallback); missing/empty `iterations` (top-level usage fallback — cover BOTH
absent and `[]`); absent `cache_creation` (5m/1h zero → all unclassified);
zero-total iteration skipped; model missing everywhere → `"unknown"`;
cross-file duplicate (same id in two files, dedupe across ParseFile calls);
a >64 KiB line. Assert emitted events (all fields) and both stats structs.

## Out of scope — do NOT touch

- No SQLite, aggregation, pricing, CLI wiring, timezone logic.
- No new dependencies.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile, README, `internal/discover`, or `internal/ingest/codex` beyond
  the single documented `UsageEvent` field addition + doc-comment touch.

## Acceptance criteria

- `make verify` + `go test -race ./...` green locally; gofmt clean; CI green
  on all three OSes.
- Every rule above has a fixture proving it.
- Reviewer gate: I will run stage 1 + stage 2 over the real local
  `~/.claude/projects` tree and compare per-date/model aggregates against
  `archive/2026-07-18-snapshot/claude_daily_token_usage_by_model_2026-06-11_to_2026-07-18.csv`
  (expecting byte-exact rows except 2026-07-18, which grew after the
  snapshot). The reference reported 31,854 unique messages, 61,222 duplicate
  records, 15,341 differing duplicates at snapshot time — same-ballpark
  counters are a sanity signal, not exact targets, since logs have grown.

## Process

1. Branch from `main`: `pr4-claude-adapter`.
2. Conventional commits.
3. PR title `feat: claude adapter with global dedupe (PR 4)` via
   `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
