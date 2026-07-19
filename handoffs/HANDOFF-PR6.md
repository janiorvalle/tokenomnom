# HANDOFF — PR 6: Aggregation, report commands, tz fix, golden-master parity

You are Codex, the implementer for this PR. You have no memory of prior work.
Everything you need is here plus `DESIGN.md` (§6 store schema, §8 CLI
surface). Claude reviews; **Janior approves and merges — never merge
yourself.** List every deviation from this handoff in the PR body — the
reviewer checks for undeclared drift, and undeclared deviations were flagged
on PR 5.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reconstructs local coding-agent token usage.
The pipeline exists end-to-end: discovery → codex/claude adapters → SQLite
store (`usage_daily` day×model aggregates) with incremental `sync`. This PR
makes the data *visible*: token-count report commands as plain text tables.
Pricing/dollars are PR 7; JSON output is PR 8; color/charts are PR 9 — no
dollars, no color, no JSON here.

## Current repo state (main after PR 5)

- `internal/store`: SQLite (`modernc.org/sqlite`), tables `meta`, `files`,
  `messages`, `usage_daily(date, provider, model, input, cache_read,
  cache_write_5m, cache_write_1h, cache_write_unclassified, output,
  reasoning)`, `file_daily`. `Store.UsageRows()` returns everything;
  richer queries do not exist yet — you add them.
- `internal/syncer`: full incremental sync (append/rewrite/rename/dedupe/
  retention). `internal/xdg`: state dir. CLI: root, `doctor`,
  `sync [--full]`, global `--codex-dir`, `--claude-dir`, `--tz`.
- CI green on ubuntu/macos/windows. Deps: cobra + modernc.org/sqlite.

## Part 0 — carried-over fix: spurious tz full re-ingest

`internal/syncer/syncer.go` (~line 117) treats the timezone as changed when
the stored NAME differs from the current name, even when the offset
fingerprints are identical. Live repro from the PR 5 review:
`sync --tz America/New_York` then plain `sync` (name `Local`, identical
fingerprint) forces a ~100-second full re-ingest in each direction.

Fix: the fingerprint (offset behavior over 1970–2101, already computed in
`internal/cli/sync.go`) is the tz identity. When only the name differs and
the fingerprint matches, update the stored `timezone` meta and continue —
no re-ingest. When the fingerprint differs, keep today's full-re-ingest
behavior. Add a regression test that alternates name/default with equal
fingerprints and asserts no `FullReingest`.

## Part 1 — query layer

Add store-side queries (in `internal/store`, or a thin `internal/aggregate`
package over it — your call, keep it typed and unit-tested):

- Filter type: date range [since, until] (inclusive, `YYYY-MM-DD`),
  optional provider, optional model.
- `Daily(filter)` — one row per date: summed token columns.
- `Monthly(filter)` — one row per `YYYY-MM`.
- `ByModel(filter)` — one row per provider+model: summed columns, active
  days, first/last date.
- `Totals(filter)` — grand totals plus per-provider totals, date range,
  active-day count.

Derived value used everywhere: `total = input + output`. Also expose
`cache_write = 5m + 1h + unclassified` as a combined display column.
SQL-side aggregation is fine (the table is small); don't load-and-loop in
Go except where genuinely simpler.

## Part 2 — report commands

New subcommands, all plain uncolored aligned text (PR 9 restyles), all
writing to `OutOrStdout()`:

- `tokenomnom summary` — date range covered, active days, grand totals
  (input / cache read / cache write / output / total), per-provider
  subtotals, top 5 models by total tokens. TOKENS ONLY — no dollars yet.
- `tokenomnom daily [--last N]` (default 30) — one row per date, most
  recent last: date, input, cache read, cache write, output, total.
- `tokenomnom monthly` — same shape per month, full history.
- `tokenomnom models` — per provider+model: tokens, share of grand total
  (e.g. `42.1%`), active days, first–last date range.

Shared flags on all four: `--provider codex|claude`, `--model NAME`,
`--since` / `--until` (`YYYY-MM-DD`; validate and error politely on
nonsense like until<since). `--last` and `--since/--until` are mutually
exclusive on `daily` (error if both).

Large numbers print with thousands separators (e.g. `204,663,824,775`) —
implement a small helper; no new deps.

**Freshness**: each report command first runs an incremental sync
(quiet — no sync summary printed; PR 5 made this ~100ms on a warm
checkpoint set) unless `--no-sync` is passed. Sync errors: report the error
and continue with stored data, with a one-line warning. If the DB does not
exist and no providers are found, print the friendly doctor-style hint and
exit 0.

**Empty results** (fresh machine, no usage in range): a plain one-line
message, exit 0 — never an error, never an empty table skeleton.

If unknown-model rows or unclassified cache-write tokens appear in the
requested range, `summary` appends one visible note line for each (the
attribution policy: residuals stay visible).

## Part 3 — golden-master parity test

Add `internal/parity/parity_test.go`, env-gated
(`TOKENOMNOM_PARITY=1`, otherwise skipped — it needs this machine's real
logs and does NOT run in CI). It automates what the reviewer has run by
hand since PR 3:

1. Sync real logs into a `t.TempDir()` state dir with `--tz
   America/New_York` equivalent options.
2. Load both frozen CSVs from `archive/2026-07-18-snapshot/`.
3. Compare per (date, model): input, cached, cache_write (combined),
   output, total, over each CSV's date window.
4. Classify each row: `exact`; `today-growth` (date == 2026-07-18 AND every
   got-column ≥ want — post-snapshot usage); `eroded` (every got-column ==
   0 — source transcripts deleted by agent retention since the snapshot);
   anything else is a FAILURE. Over-counting relative to the reference is
   ALWAYS a failure (that is the double-count guard).
5. Log a classification tally; fail with per-row detail on any failure.

Known-current state, for your expectations while developing: codex matches
byte-exact through 07-17; claude has exactly one eroded row (2026-06-17,
claude-opus-4-8); 07-18 rows show growth. More claude rows may erode over
time — that is what the `eroded` class absorbs, and why it requires
all-zero, not merely smaller.

## Tests (beyond the parity harness)

- Query-layer unit tests against a seeded store (filters, ranges, month
  boundaries, provider/model filters, active-day counts).
- Command tests: golden-ish output assertions for all four commands with a
  seeded store (fixed data, no real logs); `--last`/`--since`/`--until`
  validation errors; empty-state; `--no-sync`; unknown-model and
  unclassified note lines.
- The Part 0 tz regression test.

## Out of scope — do NOT touch

- No pricing/dollars (PR 7), no `--format json` (PR 8), no color/charts
  (PR 9), no heatmap, no TUI.
- No new dependencies.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile, README, adapters, or `internal/discover`. Syncer changes only
  as scoped in Part 0. Store: additive queries only — no schema change.

## Acceptance criteria

- `make verify` + `go test -race ./...` green locally; gofmt clean; CI
  green on all three OSes.
- `TOKENOMNOM_PARITY=1 go test ./internal/parity -run Parity -v` passes on
  this machine (reviewer will run it).
- Alternating `sync --tz America/New_York` / `sync` on a warm store causes
  zero full re-ingests (reviewer will time it).
- `summary`/`daily`/`monthly`/`models` produce sensible aligned tables on
  real data (reviewer eyeballs) and stay tokens-only.

## Process

1. Branch from `main`: `pr6-aggregate-reports`.
2. Conventional commits.
3. PR title `feat: aggregation queries and report commands (PR 6)` via
   `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
