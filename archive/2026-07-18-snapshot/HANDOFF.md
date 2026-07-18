# Token Count Analysis Handoff

## Purpose

This directory contains a fixed snapshot and the scripts used to reconstruct Janior's local Codex and Claude token usage, attribute it by day and model, apply standard API token prices, and build an auditable Excel workbook.

The analysis answers four related questions:

1. How many tokens were used each day?
2. Which model generated those tokens?
3. How do input, cache reads, cache writes, output, and reasoning tokens break down?
4. What would that usage cost at published standard API list prices?

The dollar values are API list-price equivalents. They are not actual ChatGPT or Claude subscription charges and do not represent credits, plan fees, or an invoice.

## Directory contents

| File | Purpose |
| --- | --- |
| `codex_daily_token_usage_by_model_2026-02-03_to_2026-07-18.csv` | Codex usage grouped by local date and model. |
| `claude_daily_token_usage_by_model_2026-06-11_to_2026-07-18.csv` | Claude usage grouped by local date and model. |
| `ai_token_usage_cost_analysis_through_2026-07-18.xlsx` | Final workbook with pricing, daily cost math, summaries, and charts. |
| `build_codex_usage_csv.mjs` | Extracts Codex token events from `~/.codex`. |
| `build_claude_usage_csv.mjs` | Extracts and deduplicates Claude usage from `~/.claude`. |
| `build_ai_cost_analysis.mjs` | Combines both sources, applies pricing, and creates the workbook. |
| `HANDOFF.md` | This document. |

## Current snapshot

All dates use `America/New_York` when assigning events to a day.

### Codex

- Detailed range: `2026-02-03` through `2026-07-18`
- 133 date/model rows
- 116 active dates
- 8 models
- Input tokens: 204,287,540,053
- Output tokens: 376,284,722
- Total tokens: 204,663,824,775

Models present:

- `gpt-5.2`
- `gpt-5.2-codex`
- `gpt-5.3-codex`
- `gpt-5.3-codex-spark`
- `gpt-5.4`
- `gpt-5.5`
- `gpt-5.6-luna`
- `gpt-5.6-sol`

### Claude

- Detailed transcript range: `2026-06-11` through `2026-07-18`
- 42 date/model rows
- 27 active dates
- 4 models
- Input tokens: 4,589,312,863
- Output tokens: 22,819,396
- Total tokens: 4,612,132,259

Models present:

- `claude-fable-5`
- `claude-haiku-4-5-20251001`
- `claude-opus-4-8`
- `claude-sonnet-5`

`~/.claude/stats-cache.json` remembers activity back to `2025-12-23`, but it does not contain the daily input/cache/output/model detail needed for this workbook. The detailed Claude CSV therefore begins on the earliest date recoverable from the available JSONL transcripts, `2026-06-11`.

### Combined

- Total detailed tokens: 209,275,957,034
- Calculated list-price equivalent: $133,927.87
- OpenAI subtotal: $127,050.05
- Anthropic subtotal: $6,877.82
- Base input cost: $26,101.93
- Cache-read cost: $95,001.42
- Cache-write cost: $2,271.29
- Output cost: $10,553.23

These totals reflect the source files and pricing assumptions frozen in the current workbook.

## CSV schema

Both CSVs use the same columns:

| Column | Meaning |
| --- | --- |
| `date` | Local date in `YYYY-MM-DD` format. |
| `month` | English month name. |
| `year` | Four-digit year. |
| `model` | Model identifier recorded in the local log. |
| `input_tokens` | All input tokens, including cache reads and cache creation when applicable. |
| `cached_input_tokens` | Input tokens read from cache. |
| `cache_write_input_tokens` | Input tokens used to create/write cache entries. |
| `uncached_input_tokens` | Codex: input minus cache reads. Claude: raw/base input plus cache writes. |
| `output_tokens` | All output tokens. OpenAI reasoning output is already inside this number. |
| `reasoning_output_tokens` | Informational subset exposed by Codex. Claude logs do not expose this separately, so Claude rows contain zero. |
| `total_tokens` | `input_tokens + output_tokens`. Cache columns are components of input and are not added again. |

There is one row per date and model. A date can therefore have multiple rows when more than one model was used.

## How Codex extraction works

`build_codex_usage_csv.mjs` searches these locations:

- `~/.codex/sessions`
- `~/.codex/archived_sessions`

It uses `rg` to stream only `turn_context` and `token_count` JSONL events instead of loading every session file into memory.

For each session file, the script:

1. Reads `turn_context.payload.model` to determine the active model.
2. Reads `token_count.payload.info.last_token_usage` for the incremental usage associated with that event.
3. Uses `total_token_usage` only for diagnostic checks such as counter resets and duplicate cumulative snapshots.
4. Buffers token events that occur before a model context and attributes them when the model context appears.
5. Falls back to `unknown` only when no model can be recovered. The final snapshot had no unknown-model rows.
6. Recomputes `total_tokens` as input plus output for consistency.
7. Groups the result by local date and model.

The important choice was to use `last_token_usage`, not cumulative session totals. Summing cumulative snapshots would overcount usage repeatedly.

## How Claude extraction works

`build_claude_usage_csv.mjs` searches `~/.claude/projects/**/*.jsonl` for assistant messages containing usage objects.

Claude transcripts can contain multiple progressive copies of the same assistant message. The script therefore:

1. Deduplicates globally by `message.id`.
2. Keeps the copy with the largest complete usage score when duplicate snapshots differ.
3. Keeps the earliest timestamp for the retained message so the usage remains on the original day.
4. Uses `usage.iterations` when available so fallback or multi-model work is attributed to the model that actually performed each iteration.
5. Separates raw/base input, cache reads, cache creation, 5-minute cache writes, 1-hour cache writes, and output.
6. Groups the result by local date and model.

The current detailed extraction found 31,854 unique messages, 61,222 duplicate records, and 15,341 duplicate records whose progressive usage snapshots differed.

The Claude builder also writes an intermediate pricing input file containing the exact 5-minute versus 1-hour cache-write split:

`/Users/janiorvalle/Documents/Codex/2026-07-18/ar/work/claude-token-usage-by-model-qa/pricing-input.json`

The workbook builder needs that JSON because the public CSV has one combined `cache_write_input_tokens` column while Anthropic charges different rates for 5-minute and 1-hour cache writes.

## Unattributed tokens

An earlier day-only reconciliation could produce "unattributed" tokens when a daily aggregate could not be tied safely to a model. Typical causes are token events appearing before model context or trying to reconcile cumulative totals against per-event data.

We decided not to leave that residual as a permanent bucket. The final scripts attribute usage at the event/message level before aggregating it. Codex buffers events until it sees the session model, and Claude uses the actual model on each message or iteration. The final model-segmented CSVs contain no unattributed or unknown rows.

Do not redistribute an unexplained residual proportionally across models. If an `unknown` row appears on a future run, inspect the source event and keep it explicit unless the model can be proven.

## Pricing decisions

Pricing is stored visibly in `build_ai_cost_analysis.mjs` and on the workbook's `Pricing` sheet. Rates are USD per one million tokens.

| Model | Base input | Cache read | 5m write | 1h write | Output | Status |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| `gpt-5.2` | $1.75 | $0.175 | n/a | n/a | $14 | Published |
| `gpt-5.2-codex` | $1.75 | $0.175 | n/a | n/a | $14 | Published |
| `gpt-5.3-codex` | $1.75 | $0.175 | n/a | n/a | $14 | Published |
| `gpt-5.3-codex-spark` | $1.75 | $0.175 | n/a | n/a | $14 | User-selected proxy |
| `gpt-5.4` | $2.50 | $0.25 | n/a | n/a | $15 | Published |
| `gpt-5.5` | $5.00 | $0.50 | n/a | n/a | $30 | Published |
| `gpt-5.6-luna` | $1.00 | $0.10 | $1.25 | $1.25 | $6 | Published |
| `gpt-5.6-sol` | $5.00 | $0.50 | $6.25 | $6.25 | $30 | Published |
| `claude-fable-5` | $10.00 | $1.00 | $12.50 | $20.00 | $50 | Published |
| `claude-haiku-4-5-20251001` | $1.00 | $0.10 | $1.25 | $2.00 | $5 | Published |
| `claude-opus-4-8` | $5.00 | $0.50 | $6.25 | $10.00 | $25 | Published |
| `claude-sonnet-5` | $2.00 | $0.20 | $2.50 | $4.00 | $10 | Published through 2026-08-31 |

### Spark decision

OpenAI did not publish a separate API token price for `gpt-5.3-codex-spark`; it was described as a ChatGPT Pro research preview with separate usage limits. Janior explicitly chose to price Spark using the standard `gpt-5.3-codex` rates.

The workbook labels this as `User-selected proxy`. The proxy covers 3,905,043,180 tokens and contributes $1,250.58 to the calculated total.

Do not silently relabel the Spark rate as published pricing. If OpenAI later publishes a Spark-specific rate, replace the proxy and update the status and source.

### Cost formulas

Every detailed cost follows the same visible formula:

```text
cost_usd = tokens / 1,000,000 * rate_per_1m
```

Example for all Spark usage in this snapshot:

```text
base input: 157,893,707 / 1,000,000 * $1.75  = $276.31
cache read: 3,724,110,336 / 1,000,000 * $0.175 = $651.72
output:      23,039,137 / 1,000,000 * $14 = $322.55
total: $1,250.58
```

OpenAI reasoning tokens are included in `output_tokens` and are not charged a second time. The current Codex data recorded zero cache-write tokens. Claude cache writes are charged separately at the 5-minute or 1-hour rate.

### Pricing scope

The workbook uses standard, first-party, global API list prices. It does not apply:

- ChatGPT or Claude subscription fees
- included credits or usage allowances
- Batch API discounts
- priority processing premiums
- long-context premiums
- regional endpoint premiums
- fast-mode premiums
- tool-call or web-search charges

The Claude logs inspected for this snapshot contained standard or unspecified speed records and no fast-mode records.

Pricing sources are saved as plain URLs beside each model on the workbook's `Pricing` sheet. Recheck those sources before refreshing the workbook because pricing is time-sensitive. In particular, the Claude Sonnet 5 introductory rate is documented only through `2026-08-31`.

## Workbook structure

### `Summary`

- Combined list-price equivalent
- OpenAI and Anthropic subtotals
- Token count priced with the Spark proxy
- Per-model totals for base input, cache reads, cache writes, output, and total cost
- Daily cost by model chart for the trailing 30 days
- Monthly cost by model chart from the beginning of detailed tracking

For this snapshot, the daily chart covers `2026-06-19` through `2026-07-18`. The monthly chart covers `2026-02` through `2026-07`.

### `Pricing`

Contains every model rate, pricing status, notes, rate basis, and source URL. This sheet is the visible assumptions table used by the detailed formulas.

### `Codex Costs`

One row per Codex date/model combination. Each token count is placed beside its rate and calculated cost so the math can be audited directly.

### `Claude Costs`

One row per Claude date/model combination. The 5-minute and 1-hour cache-write token counts and costs are separate.

### `Chart Data`

Formula-backed helper tables used by the two Summary charts. Daily values use `SUMIFS` against the detailed sheets by date and model. Monthly values use `SUMIFS` by year, month, and model. The charts do not contain manually copied totals.

The workbook builder also performs an independent JavaScript cost aggregation, scans the workbook for formula errors, renders every sheet for visual review, and exports the final `.xlsx` with `@oai/artifact-tool`.

## How to run the scripts

### Requirements

- macOS paths matching this machine
- `rg` (ripgrep)
- bundled Codex Node runtime
- bundled `@oai/artifact-tool`
- readable local `~/.codex` and `~/.claude` data

The runtime used to create this snapshot was:

```text
Node:
/Users/janiorvalle/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node

Bundled node_modules:
/Users/janiorvalle/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules
```

Do not run `npm install` to replace the bundled spreadsheet library. Create a local symlink instead:

```bash
cd /Users/janiorvalle/Documents/GitHub/token-count
ln -s /Users/janiorvalle/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules node_modules
```

Then run the builders in this order:

```bash
NODE=/Users/janiorvalle/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/bin/node

"$NODE" build_codex_usage_csv.mjs
"$NODE" build_claude_usage_csv.mjs
"$NODE" build_ai_cost_analysis.mjs
```

The order matters:

1. Codex builder creates the Codex CSV.
2. Claude builder creates the Claude CSV and the detailed cache-write pricing JSON.
3. Workbook builder reads the Codex CSV and Claude pricing JSON, then creates the XLSX.

### Important path behavior

These are exact copies of the scripts used during the analysis, not yet converted into a portable command-line project. They contain absolute paths pointing to:

```text
/Users/janiorvalle/Documents/Codex/2026-07-18/ar
```

Running the copied scripts from this directory still writes generated files and QA artifacts to that original Codex workspace. After a successful run, copy the deliverables back here:

```bash
cp /Users/janiorvalle/Documents/Codex/2026-07-18/ar/outputs/codex_daily_token_usage_by_model_2026-02-03_to_2026-07-18.csv .
cp /Users/janiorvalle/Documents/Codex/2026-07-18/ar/outputs/claude_daily_token_usage_by_model_2026-06-11_to_2026-07-18.csv .
cp /Users/janiorvalle/Documents/Codex/2026-07-18/ar/outputs/ai_token_usage_cost_analysis_through_2026-07-18.xlsx .
```

If this directory is going to become a maintained repository, the first cleanup should be replacing absolute constants with paths based on `import.meta.url` and adding command-line options for start date, end date, source directories, and output directory.

## Refreshing for a later date

The current scripts intentionally freeze the snapshot at `2026-07-18`. To extend it:

1. Recheck all official pricing sources.
2. Update `END_DATE` in `build_codex_usage_csv.mjs`.
3. Update the Codex `OUTPUT_PATH` filename to match the new end date.
4. Update `END_DATE` in `build_claude_usage_csv.mjs`.
5. Update `CODEX_CSV` and `OUTPUT_PATH` filenames in `build_ai_cost_analysis.mjs`.
6. Update any time-limited price, especially Claude Sonnet 5 after `2026-08-31`.
7. Run all three scripts in order.
8. Confirm the scripts report zero unknown models and zero unclassified Claude cache-write tokens, or investigate why.
9. Confirm the workbook formula error scan reports zero matches.
10. Visually inspect Summary, Pricing, Codex Costs, Claude Costs, and Chart Data.
11. Copy the refreshed CSVs, XLSX, and updated scripts into this directory.

The chart window does not need a manually entered date. `build_ai_cost_analysis.mjs` finds the latest date in the combined detailed data, uses that date plus the prior 29 days for the daily chart, and builds the monthly chart from the earliest detailed month through the latest month.

## QA artifacts

The scripts write inspection files, rendered PNG previews, and summary JSON under the original workspace's `work` directory. These files were used for verification but were not copied here because they are reproducible support artifacts rather than deliverables.

Relevant locations:

```text
/Users/janiorvalle/Documents/Codex/2026-07-18/ar/work/token-usage-by-model-qa
/Users/janiorvalle/Documents/Codex/2026-07-18/ar/work/claude-token-usage-by-model-qa
/Users/janiorvalle/Documents/Codex/2026-07-18/ar/work/ai-token-cost-analysis-qa
```

The final workbook passed these checks:

- Formula error scan found no `#REF!`, `#DIV/0!`, `#VALUE!`, `#NAME?`, or `#N/A` results.
- Independent JavaScript cost totals reconciled with workbook formulas.
- All five sheets were rendered and visually inspected.
- Daily and monthly chart helper formulas returned expected model costs.
- The copied workbook and builder in this directory matched the verified originals byte for byte at handoff time.

## Known limitations

1. This measures locally recorded usage, not server-side billing records.
2. Missing, deleted, or rotated session/transcript files cannot be reconstructed from these scripts.
3. Claude daily component detail is only available from `2026-06-11` in the transcripts currently on disk.
4. Model identifiers and log schemas are implementation details and may change in future Codex or Claude releases.
5. Pricing can change, and special processing modes can modify price.
6. Spark uses the explicitly chosen GPT-5.3-Codex proxy rate.
7. The copied scripts are machine-specific until the absolute paths are removed.
8. Re-running with the same fixed end date can still change historical totals if additional archived logs appear or existing local files change.

## Decisions made together

- Report token usage rather than credits or subscription/API billing records.
- Aggregate by local day using `America/New_York`.
- Include separate date, month, and year columns.
- Segment by model, allowing multiple rows per day.
- Keep input, cache read, cache write, output, reasoning, and total token columns explicit.
- Eliminate unexplained attribution by assigning events/messages before daily aggregation rather than spreading residuals proportionally.
- Use official standard API list prices as an equivalent-cost calculation.
- Keep token count, rate, and cost beside each other for auditability.
- Price `gpt-5.3-codex-spark` using GPT-5.3-Codex rates and label it as a user-selected proxy.
- Separate Claude 5-minute and 1-hour cache writes because their rates differ.
- Do not charge OpenAI reasoning output twice.
- Add a trailing 30-day daily cost-by-model chart.
- Add a monthly cost-by-model chart from the beginning of detailed tracking.
- Keep chart source data formula-backed and visible on its own sheet.
- Preserve the CSVs, workbook, and exact builder scripts together in this directory.
