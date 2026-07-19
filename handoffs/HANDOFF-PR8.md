# HANDOFF — PR 8: `--format json` everywhere + `export` (the agent contract)

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` (§8 CLI surface, §10
Agent integration). Claude reviews; **Janior approves and merges — never
merge yourself.** List every deviation from this handoff in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reports local coding-agent token usage and
API list-price-equivalent costs. All commands exist as plain-text tables
(PRs 2–7). This PR adds machine-readable output: a stable JSON contract
that coding agents will consume (a SKILL.md in PR 12 will teach agents to
call these commands with `--format json`), plus an `export` command for
full day×model data. Color/charts are PR 9 — still no styling.

## Current repo state (main after PR 7)

- CLI commands: `doctor`, `sync [--full]`, `summary`, `daily [--last N]`,
  `monthly`, `models`, `pricing`. Report commands share
  `--provider/--model/--since/--until/--no-sync` and quiet-sync freshness;
  costs come from `internal/pricing` (integer nanodollar math,
  `CostBreakdown`, unpriced tallies, proxy/override statuses).
- `internal/store` queries: `Daily`/`Monthly`/`ByModel`/`Totals` + filters.
- Deps: cobra + modernc.org/sqlite. CI green everywhere.

## Part 1 — global `--format` flag

Persistent root flag `--format pretty|json` (default `pretty`). Commands
honoring it: `summary`, `daily`, `monthly`, `models`, `pricing`, `doctor`,
`sync`. Unknown value → polite error listing valid values.

## Part 2 — the JSON contract

Design rules (this is a CONTRACT — agents will parse it; breaking it later
is a semver event):

- Envelope on every JSON response:

```json
{
  "schema": "tokenomnom.report/v1",
  "command": "daily",
  "generated_at": "2026-07-18T21:04:05Z",
  "timezone": "America/New_York",
  "filters": {"provider": null, "model": null, "since": null, "until": null},
  "disclaimer": "Dollar figures are API list-price equivalents, not actual bills.",
  "warnings": [],
  "data": { ... }
}
```

- `generated_at` in RFC 3339 UTC. `timezone` = the bucketing tz in effect.
- snake_case field names everywhere; token counts as JSON integers; costs
  as `"cost_usd"` JSON numbers rounded to cents (document that agents
  needing exact arithmetic should use the token counts + `pricing` rates).
- Unpriced/unclassified/unknown-model conditions appear BOTH as structured
  fields (e.g. `"unpriced_tokens": 123`) and as human-readable strings in
  `warnings`. The quiet-freshness-sync failure warning also goes into
  `warnings` — **stdout must be pure parseable JSON; nothing else prints
  to stdout in json mode** (errors still go to stderr with exit 1).
- Stable ordering: arrays sorted (dates ascending, models by total
  descending — same as pretty output).
- Empty results: valid envelope with empty `data` arrays — never a bare
  message, never exit 1.

Per-command `data` shapes (field names are yours to finalize, but keep
these contents; document EVERYTHING in `docs/agent-api.md` — that file
becomes the source for PR 12's SKILL.md):

- `summary`: date range, active_days, totals {tokens by bucket, total,
  cost_usd}, providers[] (same shape), top_models[] (provider, model,
  total_tokens, cost_usd), unpriced_tokens, unclassified_cache_write_tokens.
- `daily` / `monthly`: rows[] {date | month, input_tokens,
  cache_read_tokens, cache_write_tokens, output_tokens, total_tokens,
  cost_usd}.
- `models`: rows[] {provider, model, ...token buckets..., total_tokens,
  share, active_days, first_date, last_date, cost_usd, cost_share,
  priced: bool}.
- `pricing`: models[] {model, entries[] {rates per bucket (null when
  absent), status, effective_from, effective_until, source, notes,
  overridden: bool}}.
- `doctor`: providers[] {provider, path, source, exists, jsonl_files,
  total_bytes, oldest, newest, walk_errors[]}, store {path, exists,
  size_bytes, schema_version, timezone, last_sync, usage_rows,
  distinct_models, date_range, missing_files}, skills install state N/A
  until PR 12 — omit.
- `sync`: the summary counters + full_reingest + duration_ms + warnings.

`schema` version starts at `tokenomnom.report/v1` for all commands.
Additive changes (new fields) are allowed without a version bump;
renames/removals are not — write this rule into `docs/agent-api.md`.

## Part 3 — `export`

`tokenomnom export --format csv|json [--out FILE]` plus the shared report
filters (`--provider/--model/--since/--until/--no-sync`). One record per
(date, provider, model) — the full-resolution data.

CSV columns (heritage schema from the frozen analysis, extended —
keep exact order):

```
provider,date,month,year,model,input_tokens,cached_input_tokens,
cache_write_5m_tokens,cache_write_1h_tokens,cache_write_unclassified_tokens,
cache_write_input_tokens,uncached_input_tokens,output_tokens,
reasoning_output_tokens,total_tokens,cost_usd
```

- `month` = English month name, `year` = 4-digit (heritage columns for
  spreadsheet pivots), `cache_write_input_tokens` = 5m+1h+unclassified,
  `uncached_input_tokens` = input − cached (combined-write inclusive, i.e.
  raw + writes, matching the archived CSVs' semantics), `total_tokens` =
  input + output, `cost_usd` with 2 decimals; `` (empty) when the row is
  entirely unpriced.
- Proper CSV quoting/escaping; `\n` line endings; header always present.
- JSON export: envelope (`"command": "export"`) with rows[] carrying the
  same fields.
- `--format` for export defaults to `csv` (its whole point), and export
  ignores the root default; `--out` writes atomically (temp file + rename)
  and prints nothing to stdout on success in csv mode except nothing —
  the file is the output. Without `--out`, rows stream to stdout.

## Tests

- Envelope invariants for every json command: valid JSON on stdout with
  NOTHING else, schema/command/timezone fields, warnings array present,
  stable ordering, empty-state envelopes.
- Round-trip: seeded store → `daily --format json` numbers equal the
  pretty-table numbers.
- Export: CSV golden against a seeded store (all columns, quoting edge
  with a model name containing a comma — synthetic), month/year derivation,
  unpriced empty cost cell, --out atomic write, filters.
- `docs/agent-api.md` exists and documents every command's schema (a test
  asserting the file mentions each command name is a cheap guard).
- Race/verify as always.

## Out of scope — do NOT touch

- No color/styling (PR 9), no charts, no heatmap, no TUI, no SKILL.md
  (PR 12).
- No new dependencies. No schema (SQLite) changes.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile, README, adapters, syncer, discover, pricing rates.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate: on real data — `summary --format json | python3 -m
  json.tool` parses; `daily --format json` totals match the pretty table;
  `export | head` produces valid CSV whose per-model sums over the frozen
  window reconcile with the parity numbers; agent-api.md is accurate
  against actual output.

## Process

1. Branch from `main`: `pr8-json-export`.
2. Conventional commits.
3. PR title `feat: json output contract and export command (PR 8)` via
   `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
