# Agent API

`tokenomnom` exposes a stable machine-readable contract for coding agents. Use
`--format json` with `summary`, `daily`, `monthly`, `models`, `heatmap`,
`pricing`, `doctor`, `sync`, `export`, and `install-skill`. The `export` command defaults to CSV;
all other commands default to the human-readable `pretty` format.

## Compatibility

Every JSON response uses schema `tokenomnom.report/v1`. New fields may be added
without changing that version. Renaming or removing a field, changing its type,
or changing its meaning requires a new schema version and is a semver event.

Token counts are JSON integers. `cost_usd` and pricing rates are JSON numbers.
Costs are rounded to cents; agents that need exact arithmetic should use the
token counts and the rates returned by `pricing`.

## Envelope

Every JSON response is one object with these fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema` | string | Always `tokenomnom.report/v1`. |
| `command` | string | The command that produced the response. |
| `generated_at` | string | RFC 3339 timestamp in UTC. |
| `timezone` | string | Timezone used to bucket usage dates. This is an IANA name when the operating system exposes one; otherwise `Local` means the process system timezone. |
| `filters` | object | `provider`, `model`, `since`, and `until`; each is a string or `null`. |
| `disclaimer` | string | API list-price-equivalent disclaimer. |
| `warnings` | string[] | Human-readable pricing, classification, or freshness warnings. |
| `data` | object | Command-specific data described below. |

Warnings are also represented by structured counters in `data`. Empty results
return the full envelope, an empty `rows`/`providers`/`models` array, and zeroed
totals. Errors are written to stderr and exit nonzero; successful JSON output
contains nothing before or after the envelope.

## Summary

`tokenomnom summary --format json`

`data` contains `date_range` (`first_date` and `last_date`, each nullable),
`active_days`, `totals`, `providers`, `top_models`, `unpriced_tokens`,
`unclassified_cache_write_tokens`, and `unknown_model_tokens`.

`totals` contains `input_tokens`, `cache_read_tokens`, `cache_write_tokens`,
`output_tokens`, `total_tokens`, and `cost_usd`. Each `providers` item adds
`provider` to that same shape. Each `top_models` item contains `provider`,
`model`, `total_tokens`, and `cost_usd`. Providers sort by provider ID; top
models sort by total tokens descending, then provider and model.

## Daily And Monthly

`tokenomnom daily --format json` returns `data.rows` ordered by ascending
`date`. `tokenomnom monthly --format json` returns rows ordered by ascending
`month`. Each row has the period field plus `input_tokens`,
`cache_read_tokens`, `cache_write_tokens`, `output_tokens`, `total_tokens`, and
`cost_usd`.

Both data objects also contain `unpriced_tokens`,
`unclassified_cache_write_tokens`, and `unknown_model_tokens`. `daily` defaults
to the most recent 30 active dates; use `--last N` or an explicit date range.

## Models

`tokenomnom models --format json`

`data.rows` is ordered by `total_tokens` descending, then provider and model.
Each row contains `provider`, `model`, all five combined token fields,
`share`, `active_days`, `first_date`, `last_date`, `cost_usd`, `cost_share`, and
`priced`. Shares are percentages between 0 and 100. `cost_share` is `null` when no
tokens in the row were priced. The data object also has the three diagnostic
token counters used by daily and monthly.

## Heatmap

`tokenomnom heatmap [--year YYYY] --format json`

`data.window` contains the inclusive `from` and `to` dates. Without `--year`,
the window is the trailing 12 months ending today; `--year` selects that full
calendar year. `data.metric` is `cost_usd`, or `tokens` when every usage row in
the window is unpriced. `data.days` contains one item per calendar date, ordered
oldest first, with `date`, `cost_usd`, `total_tokens`, and contribution `level`
from 0 through 4.

`data.stats` contains `active_days`, `total_cost_usd`, `busiest_day` (`date`,
`cost_usd`, and `total_tokens`), and `longest_streak`. Active days and streaks
use the selected nonzero metric and are bounded by `data.window`.

## Pricing

`tokenomnom pricing --format json`

`data.models` is ordered by model name. Each item contains `model` and an
`entries` array ordered by effective start. Entries contain the nullable
USD-per-million-token rates `base_input`, `cache_read`, `write_5m`, `write_1h`,
and `output`, plus `status`, nullable `effective_from`, nullable
`effective_until`, `source`, `notes`, and `overridden`.

## Doctor

`tokenomnom doctor --format json`

`data.providers` contains `provider`, `path`, `source`, `exists`,
`jsonl_files`, `total_bytes`, nullable `oldest`, nullable `newest`, and
`walk_errors`. `data.store` contains `path`, `exists`, `size_bytes`, nullable
`schema_version`, nullable `timezone`, nullable `last_sync`, `usage_rows`,
`distinct_models`, `date_range`, and `missing_files`.

`data.skills` contains one item per provider with `provider`, skill `path`,
`status`, and nullable installed `version`.

## Install Skill

`tokenomnom install-skill --format json`

`data.providers` contains one result per provider with `provider`, `path`,
`action`, and `version`. `action` is one of `installed`, `updated`,
`up_to_date`, `skipped_no_root`, `refused_foreign`, `removed`, or
`not_installed`.

## Sync

`tokenomnom sync --format json`

`data` contains `files_scanned`, `files_skipped`, `files_appended`,
`files_rewritten`, `files_missing`, `events_applied`, `usage_rows`,
`unknown_model_tokens`, `unclassified_cache_write_tokens`, `full_reingest`, and
`duration_ms`. Its `warnings` array repeats the sync-specific envelope warnings
so the sync result remains self-contained.

## Export

`tokenomnom export [--format csv|json] [--out FILE]`

Export supports `--provider`, `--model`, `--since`, `--until`, and `--no-sync`.
There is one row per `(date, provider, model)`, ordered by date, provider, and
model. With `--out`, output is atomically replaced and stdout is empty.

CSV is the default and always includes this header, even for empty results:

```text
provider,date,month,year,model,input_tokens,cached_input_tokens,cache_write_5m_tokens,cache_write_1h_tokens,cache_write_unclassified_tokens,cache_write_input_tokens,uncached_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cost_usd
```

`month` is the English month name. `cache_write_input_tokens` is the sum of the
5-minute, 1-hour, and unclassified write buckets. `uncached_input_tokens` is
`input_tokens - cached_input_tokens`. `total_tokens` is input plus output. CSV
costs have two decimal places and are empty when the row is entirely unpriced.
To prevent spreadsheet formula execution, a model value beginning with `=`,
`+`, `-`, `@`, tab, carriage return, or line feed is prefixed with a single
quote in CSV output. JSON preserves the original model value.

JSON export uses the standard envelope with `command: "export"`.
`data.rows` carries fields matching the CSV columns; `cost_usd` is a number or
`null` when entirely unpriced. The data object also contains
`unpriced_tokens`, `unclassified_cache_write_tokens`, and
`unknown_model_tokens`.
