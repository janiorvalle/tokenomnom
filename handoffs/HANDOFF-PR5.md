# HANDOFF — PR 5: SQLite usage store + incremental `sync`

You are Codex, the implementer for this PR. You have no memory of prior work.
Everything you need is here plus `DESIGN.md` (§6 Usage store is your spec;
also skim §5). This is the largest PR in the project — read this file twice
before coding. Claude reviews; **Janior approves and merges — never merge
yourself.**

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reconstructs local coding-agent token usage.
Discovery (PR 2) finds `*.jsonl` files; the Codex adapter (PR 3) and Claude
adapter (PR 4) parse them into normalized events. This PR persists day×model
aggregates in SQLite with incremental per-file checkpoints, so every later
command starts instantly instead of re-reading many GB of logs. Aggregation
queries and report commands are PR 6.

## Current repo state (main after PR 4)

- `internal/discover`: `Root`, `SourceFile{Provider, Path, Size, ModTime}`,
  `Resolve`, `ListSourceFiles`.
- `internal/ingest`: `UsageEvent{Timestamp, Provider, Model, Input,
  CacheRead, CacheWrite5m, CacheWrite1h, CacheWriteUnclassified, Output,
  Reasoning}`, `Stats`, `Adapter`.
- `internal/ingest/codex`: `Adapter.ParseFile(f, emit)` — stateless per file.
- `internal/ingest/claude`: two-stage — `Adapter.ParseFile(f, collect
  func(Candidate))` + `Deduper` (`Add`, `Stats`, `Retained`, `Emit`).
  `Candidate{MessageID, Timestamp, Iterations, Score}`,
  `Iteration{Model, RawInput, CacheRead, CacheCreation, CacheWrite5m,
  CacheWrite1h, Output}`.
- CLI: root + `doctor`; global `--codex-dir`/`--claude-dir`. CI green on
  ubuntu/macos/windows; deps: cobra only.

## The prime directive: deletion is retention erosion, never reversal

Discovered during PR 4 review: Claude Code's ~30-day transcript retention
deleted a full day of history from disk within hours of our frozen snapshot.
**The store is therefore not a rebuildable cache — it is the surviving
record.** Rules:

1. A previously-ingested file that has vanished keeps its full contribution.
   Mark its checkpoint row missing (for `doctor`); never subtract.
2. Reversal happens ONLY for files that still exist but were rewritten
   in place.
3. `sync --full` re-ingests everything from scratch and refreshes
   checkpoints, but still never deletes aggregate rows contributed by files
   that no longer exist.

Because of this, the DB lives in the **state** directory, not cache.

## Deliverables

### 1. `internal/xdg`

Small path-resolution package (modeled on better-git-review's
`internal/xdg`): `StateDir()` → `TOKENOMNOM_STATE_DIR` env override, else
`$XDG_STATE_HOME/tokenomnom`, else `~/.local/state/tokenomnom` on
unix/macOS, `%LocalAppData%\tokenomnom` on Windows (`os.UserCacheDir` is an
acceptable Windows base). Injectable getenv/home for tests, same style as
`internal/discover`.

### 2. New dependency: `modernc.org/sqlite`

Pure-Go SQLite driver (no CGO — cross-compilation must stay one-command).
This is the ONLY new direct dependency allowed. Open with
`_busy_timeout` set and `journal_mode=WAL`. Single-process usage is the
design assumption; a second concurrent invocation should fail fast with a
clear error, not corrupt (WAL + busy timeout handles this).

### 3. `internal/store` — schema + persistence

Schema v1 (all writes transactional; one transaction per synced file so a
crash mid-sync loses at most one file's progress):

```sql
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT);
-- keys: schema_version, timezone, last_sync_unix

CREATE TABLE files (
  path TEXT PRIMARY KEY, provider TEXT NOT NULL,
  size INTEGER NOT NULL, mtime_unix INTEGER NOT NULL,
  byte_offset INTEGER NOT NULL,     -- end of last fully-parsed line
  tail_hash TEXT NOT NULL,          -- hex SHA-256 of last min(4096, offset) bytes ending at offset
  parser_state TEXT,                -- codex resume state JSON (see below); NULL for claude
  missing INTEGER NOT NULL DEFAULT 0,
  last_synced_unix INTEGER NOT NULL
);

CREATE TABLE messages (             -- claude global dedupe authority
  message_id TEXT PRIMARY KEY,
  score INTEGER NOT NULL,
  ts_unix_ms INTEGER NOT NULL,
  iterations TEXT NOT NULL          -- JSON of retained normalized iterations
);

CREATE TABLE usage_daily (          -- query workhorse (PR 6 reads this)
  date TEXT NOT NULL, provider TEXT NOT NULL, model TEXT NOT NULL,
  input INTEGER NOT NULL DEFAULT 0, cache_read INTEGER NOT NULL DEFAULT 0,
  cache_write_5m INTEGER NOT NULL DEFAULT 0,
  cache_write_1h INTEGER NOT NULL DEFAULT 0,
  cache_write_unclassified INTEGER NOT NULL DEFAULT 0,
  output INTEGER NOT NULL DEFAULT 0, reasoning INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (date, provider, model)
);

CREATE TABLE file_daily (           -- per-file contribution, enables rewrite reversal (codex only)
  path TEXT NOT NULL, date TEXT NOT NULL, provider TEXT NOT NULL,
  model TEXT NOT NULL,
  input INTEGER NOT NULL DEFAULT 0, cache_read INTEGER NOT NULL DEFAULT 0,
  cache_write_5m INTEGER NOT NULL DEFAULT 0,
  cache_write_1h INTEGER NOT NULL DEFAULT 0,
  cache_write_unclassified INTEGER NOT NULL DEFAULT 0,
  output INTEGER NOT NULL DEFAULT 0, reasoning INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (path, date, provider, model)
);
```

Raw events are never stored. Day-bucketing happens at ingest: `date` =
`event.Timestamp.In(tz).Format("2006-01-02")`.

### 4. `internal/syncer` (or inside `store`) — the sync engine

Orchestrates one sync pass:

1. Resolve roots (reuse `discover`), list source files.
2. Load the `files` checkpoint map; mark rows whose path was not listed as
   `missing=1` (aggregates untouched). A missing file that reappears at the
   same path with matching size/mtime/tail_hash resumes as if never gone;
   otherwise it is treated as rewritten (reverse for codex, reparse).
3. Per file, classify:
   - **unchanged**: size + mtime match checkpoint → skip.
   - **appended**: size > checkpoint size AND the tail fingerprint still
     matches (SHA-256 of the stored window re-read at [offset−win, offset])
     → resume parsing from `byte_offset`.
   - **rewritten**: anything else (smaller, same-size-different-mtime with
     differing tail, tail mismatch) → codex: subtract this path's
     `file_daily` rows from `usage_daily`, delete them, reset checkpoint,
     reparse from 0. claude: reset checkpoint and reparse from 0 — NO
     reversal (see Claude semantics below; the messages table makes
     re-adding idempotent).
4. Parse with the right adapter, apply events to `usage_daily` (and
   `file_daily` for codex), advance the checkpoint.
5. `byte_offset` only ever advances past COMPLETE lines. Sessions are
   written live; a trailing partial line (no `\n`) must be left for the
   next sync, not parsed, not counted, not included in the offset. (The
   PR 3/4 adapters read whole files; you will need a resumable line-reader
   wrapper — put it where both providers can share it. Do not modify the
   adapters' parsing semantics; wrapping/refactoring their file-opening
   entry points is in scope if needed, with behavior-preserving tests.)

**Codex resume state** (`files.parser_state` JSON): the parser context that
would otherwise be lost when resuming mid-file — current model + hasModel,
previous `total_token_usage` snapshot (for reset/duplicate diagnostics), and
any buffered pre-model events. Without this, a resumed file whose
`turn_context` came before the checkpoint would wrongly emit `unknown`.
Cleanest shape: expose the PR 3 `fileParser` as a resumable component
(export a state snapshot/restore); keep its rules bit-identical.

**Claude semantics**: candidates stream through the same dedupe rules as PR
4's `Deduper`, but the authority is the `messages` table: load the
id→(score, ts, iterations) index at sync start (tens of thousands of rows —
fine in memory), apply strict-greater-replace / earliest-timestamp rules,
and when a retained message is REPLACED, subtract the old iterations'
event values from `usage_daily` and add the new ones (this is why
`messages.iterations` stores the retained normalized iterations). Claude
events do NOT write `file_daily` — dedupe makes per-file attribution
meaningless. Flush changed messages back in the file's transaction.

**Timezone**: sync takes a tz (default: system local). Store the tz name in
`meta`. If a sync starts with a different tz than stored → automatic full
re-ingest (aggregates and file_daily and messages rebuilt; the
retention rule still applies to vanished files' existing rows — document
this limitation in a code comment: retained-but-vanished history keeps its
original tz bucketing).

### 5. CLI wiring

- `tokenomnom sync [--full]` — runs a sync pass; prints a plain summary:
  files scanned/skipped/appended/rewritten/missing, events applied, rows in
  `usage_daily`, duration, plus loud lines when unknown-model tokens or
  unclassified cache-write tokens were ingested (attribution policy: keep
  residuals visible). `--full` = reset checkpoints/messages/aggregates per
  the prime directive, re-ingest.
- Global `--tz` string flag (IANA name, e.g. `America/New_York`; empty =
  system). Validate with `time.LoadLocation`.
- `doctor` gains a Store section: DB path, exists, size, schema version,
  stored tz, last sync time, usage_daily row count, distinct models, date
  range, missing-file count. Keep output plain and testable (bytes via
  `OutOrStdout`).

### 6. Tests (all with `t.TempDir()` state dirs + fixture logs)

Must cover at least:

- initial sync then no-change resync (second pass skips everything).
- codex append across a checkpoint whose model context precedes it (the
  parser_state test — resumed events attribute to the right model, not
  `unknown`).
- trailing partial line: not counted, offset stays at last newline, next
  sync picks it up once completed.
- claude progressive rewrite: same message id reappears with higher score →
  aggregates reflect exactly the delta (subtract old, add new).
- claude duplicate with equal/lower score → aggregates unchanged.
- codex rewrite in place → old contribution reversed via file_daily,
  reparse correct.
- file deletion → aggregates untouched, checkpoint marked missing, doctor
  reports it; file reappearing identical → resumes cleanly.
- tz change → full re-ingest under new tz.
- `--full` → identical aggregates to a fresh initial sync (idempotence).
- sync summary + doctor store section golden-ish assertions.
- unknown-model and unclassified-cache-write ingestion produce the loud
  summary lines.

## Out of scope — do NOT touch

- No aggregation/query commands (`summary`/`daily`/`models` are PR 6), no
  pricing, no color, no `--format json`.
- No dependencies beyond `modernc.org/sqlite` (+ its transitive modules).
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile, README, `internal/discover`. Adapter packages: only the
  minimal, behavior-preserving refactors described above (resumable reader
  entry points / exported parser state), each covered by existing-behavior
  tests.

## Acceptance criteria

- `make verify` + `go test -race ./...` green locally; gofmt clean; CI
  green on all three OSes (WAL + modernc on Windows included).
- Every scenario above has a test.
- Reviewer gate: I will run `sync` on the real machine (multi-GB logs),
  verify a second `sync` is near-instant (all files skipped), spot-check
  `usage_daily` totals against the PR 3/4 parity numbers, then delete a
  fixture file in a temp-state rerun to watch retention hold. First full
  sync wall time on ~45 GB of logs should be in the minutes-not-hours
  range; second pass sub-second on checkpoints alone.

## Process

1. Branch from `main`: `pr5-store-sync`.
2. Conventional commits.
3. PR title `feat: sqlite usage store and incremental sync (PR 5)` via
   `gh pr create`; list any deviations explicitly — especially any adapter
   refactors, which get extra review scrutiny.
4. **Do not merge.** Claude reviews, Janior approves and merges.
