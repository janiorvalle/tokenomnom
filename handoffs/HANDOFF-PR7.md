# HANDOFF — PR 7: Pricing engine, `pricing` command, cost columns

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` (§7 Pricing engine) and
`archive/2026-07-18-snapshot/HANDOFF.md` ("Pricing decisions" — the
methodology of record). Claude reviews; **Janior approves and merges —
never merge yourself.** List every deviation from this handoff in the PR
body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reconstructs local coding-agent token usage
and — starting with this PR — prices it at standard API list rates. The
dollar figures are **API list-price equivalents**, not actual bills (usage
came from subscriptions); every user-facing surface must keep that framing.
Pipeline and token reports exist (PRs 2–6). JSON output is PR 8; color is
PR 9.

## Current repo state (main after PR 6)

- `internal/store`: `usage_daily(date, provider, model, input, cache_read,
  cache_write_5m, cache_write_1h, cache_write_unclassified, output,
  reasoning)` + typed queries `Daily`/`Monthly`/`ByModel`/`Totals`
  (`internal/store/aggregate.go`) with `Filter{Since, Until, Provider,
  Model}`.
- CLI: root, `doctor`, `sync`, `summary`, `daily`, `monthly`, `models`
  (plain tables, tokens only, quiet freshness sync, `--no-sync`).
- `internal/xdg`: `StateDir` only — you add `ConfigDir`.
- `internal/parity`: env-gated golden-master test.
- Deps: cobra + modernc.org/sqlite. CI green on ubuntu/macos/windows.

## Part 1 — `internal/pricing`

### Rate table (embed as `pricing.json` via `go:embed`)

Rates are USD per **1,000,000 tokens**. This table is the frozen 2026-07-18
analysis table — reproduce it exactly (statuses too):

| model | base_input | cache_read | write_5m | write_1h | output | status |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| gpt-5.2 | 1.75 | 0.175 | — | — | 14 | published |
| gpt-5.2-codex | 1.75 | 0.175 | — | — | 14 | published |
| gpt-5.3-codex | 1.75 | 0.175 | — | — | 14 | published |
| gpt-5.3-codex-spark | 1.75 | 0.175 | — | — | 14 | **proxy** |
| gpt-5.4 | 2.50 | 0.25 | — | — | 15 | published |
| gpt-5.5 | 5.00 | 0.50 | — | — | 30 | published |
| gpt-5.6-luna | 1.00 | 0.10 | 1.25 | 1.25 | 6 | published |
| gpt-5.6-sol | 5.00 | 0.50 | 6.25 | 6.25 | 30 | published |
| claude-fable-5 | 10.00 | 1.00 | 12.50 | 20.00 | 50 | published |
| claude-haiku-4-5-20251001 | 1.00 | 0.10 | 1.25 | 2.00 | 5 | published |
| claude-opus-4-8 | 5.00 | 0.50 | 6.25 | 10.00 | 25 | published |
| claude-sonnet-5 | 2.00 | 0.20 | 2.50 | 4.00 | 10 | published (effective_until 2026-08-31) |

Schema per model entry: the five bucket rates (each optional — "—" means
absent, not zero), `status` (`published` | `proxy` | `estimated`), `source`
(URL string; use the vendor pricing-page URLs from the archived workbook's
Pricing sheet or the vendors' canonical pricing pages), optional `notes`,
optional `effective_from` / `effective_until` (`YYYY-MM-DD`, inclusive).
Multiple entries per model are allowed with non-overlapping effective
ranges (that is how future rate changes get modeled); validate
non-overlap at load.

**The Spark rule (verbatim from the analysis, preserve it):** OpenAI never
published a Spark-specific API rate; Janior explicitly chose the
gpt-5.3-codex rates as a proxy. `status: "proxy"` with a note saying so.
Never silently relabel a proxy as published.

### Override file

`<ConfigDir>/pricing.json` where `ConfigDir()` joins `internal/xdg`:
`TOKENOMNOM_CONFIG_DIR` override, else `$XDG_CONFIG_HOME/tokenomnom`, else
`~/.config/tokenomnom` (unix/macOS), else `os.UserConfigDir()`-based on
Windows. Merge semantics: an override entry for a model **replaces that
model's entry list entirely** (simple and predictable — no field-level
merging); unknown models in the override are additions. A malformed
override file is a hard, clearly-worded error (bad pricing must never be
silently ignored). Overridden models are flagged in the `pricing` command.

### Cost computation

For one `usage_daily` row on `date` with the rate entry in force that day:

```
base_input_tokens = input − cache_read − cache_write_5m − cache_write_1h − cache_write_unclassified
cost(bucket)      = tokens / 1_000_000 × rate(bucket)
row_cost = cost(base_input) + cost(cache_read) + cost(write_5m)
         + cost(write_1h) + cost(unclassified) + cost(output)
```

- Reasoning tokens are inside output — never charged separately.
- **Unclassified cache writes** price at the model's **1h rate** (the
  conservative/higher Claude rate) and are ALSO reported as a loud note
  wherever costs are shown. Zero today; the policy exists so a future
  format change is visible, not mispriced silently.
- A nonzero bucket whose rate is absent (e.g. cache writes on a model with
  no published write rate), a date outside every effective range, or a
  model not in the table at all (including `unknown`) → those tokens are
  **unpriced**: contribute $0, tracked in an `UnpricedTokens` tally per
  model, surfaced as a loud note. Never guess a rate.

API shape (yours to refine): `Load(override io.Reader…) (Table, error)`,
`Table.RateFor(model, date)`, `Table.Cost(row store.Usage) CostBreakdown`
where `CostBreakdown` carries per-bucket costs, total, and unpriced token
counts. Money in **cents or micro-dollars as integers internally** (avoid
float accumulation; convert to float only for display) — or use exact
decimal math another way; document the choice. Display rounding: 2 decimal
places, round half up, thousands separators.

## Part 2 — `pricing` command

`tokenomnom pricing` prints the effective table: model, the five rates,
status (with proxy/estimated visibly marked), effective window (`always`
when unbounded), source, and an `override` marker on models replaced by the
user file. Ends with the list-price-equivalent disclaimer line. No sync
needed (`--no-sync` not applicable). Plain text.

## Part 3 — cost columns in the report commands

- `summary`: add a Cost section — grand total, per-provider subtotals, top
  5 models by cost — plus the disclaimer line "Dollar figures are API
  list-price equivalents, not actual bills." Loud note lines when the range
  contains unpriced or unclassified-cache-write tokens.
- `daily`, `monthly`: add a `COST` column (rightmost).
- `models`: add `COST` and `COST SHARE` columns.
- Formatting: `$1,234.56`. A row that is entirely unpriced shows `—` in
  cost columns, not `$0.00`.

## Tests

- Table validation: 12 models load; spark is proxy; sonnet-5 effective
  window ends 2026-08-31; overlapping-range rejection; malformed override
  rejection.
- Cost math: hand-computed fixtures for each bucket incl. 5m/1h split,
  unclassified-at-1h policy, unpriced buckets, effective-date boundaries
  (day before/on/after 2026-08-31 for sonnet-5).
- **Golden numbers from the frozen analysis (CI-runnable, hermetic):**
  1. Spark, whole snapshot (from archived HANDOFF): base 157,893,707 →
     $276.31; cache read 3,724,110,336 → $651.72; output 23,039,137 →
     $322.55; total **$1,250.58**.
  2. Parse `archive/2026-07-18-snapshot/codex_daily_token_usage_by_model_…csv`,
     price every row (base = input − cached; no cache writes), sum:
     **$127,050.05** (the workbook's OpenAI subtotal, ±$0.01 for rounding
     policy differences — document the achieved delta in a test comment).
  (The Anthropic subtotal $6,877.82 needs the 5m/1h split absent from the
  public CSV — do NOT try to reproduce it in CI; Claude cost math is
  covered by the hand-computed fixtures instead.)
- Command output tests: pricing table rendering incl. proxy + override
  markers; summary cost section; `—` for unpriced rows; disclaimer lines.

## Out of scope — do NOT touch

- No `--format json` (PR 8), no color (PR 9), no TUI, no export.
- No new dependencies (stdlib `embed` + `encoding/json` suffice).
- No schema changes. Do not modify `DESIGN.md`, `archive/`, `assets/`,
  `handoffs/`, CI, Makefile, README, adapters, syncer, discover.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate: on real data, `summary` shows a cost total in the
  neighborhood of the frozen analysis' $133,927.87 (plus post-snapshot
  usage, minus the eroded 06-17 row); `pricing` renders the full table
  with spark marked proxy; an override file round-trips (override a rate,
  see it flagged, remove it, back to embedded).

## Process

1. Branch from `main`: `pr7-pricing`.
2. Conventional commits.
3. PR title `feat: pricing engine and cost reporting (PR 7)` via
   `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
