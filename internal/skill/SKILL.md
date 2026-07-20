---
name: tokenomnom
description: Reports local coding-agent (Codex, Claude Code) token usage and API-equivalent spend, and mines vaulted session-transcript history for workflow analysis. Use for token, cost, or spend questions, for "what did I work on" session-history mining, or whenever tokenomnom or nomnom is mentioned.
---

# tokenomnom

tokenomnom summarizes local Codex and Claude Code token usage. Dollar figures
are API list-price equivalents of subscription usage, not actual bills. Always
relay that caveat when reporting a cost.

## Calling The CLI

Use `--format json` for every query. `nomnom` is an alias for `tokenomnom`.
`--no-sync` is supported only by `summary`, `daily`, `monthly`, `models`,
`heatmap`, and `export`. Use it for fast repeated report queries after the first
query has refreshed the usage store. Do not pass `--no-sync` to `doctor`,
`sync`, `vault`, `schedule`, or `install-skill`. If neither binary is available,
say that tokenomnom is not installed instead of guessing numbers.

## Task Map

- Spend today: `tokenomnom daily --since YYYY-MM-DD --until YYYY-MM-DD --format json`; read `data.rows[].cost_usd` and `total_tokens`.
- Spend this week: `tokenomnom daily --last 7 --format json`; read and sum `data.rows[].cost_usd`, while noting these are the latest seven active days.
- Spend this month: `tokenomnom monthly --since YYYY-MM-01 --until YYYY-MM-DD --format json`; read `data.rows[].cost_usd`.
- Overall usage: `tokenomnom summary --format json`; read `data.totals`, `data.active_days`, and `data.top_models`.
- Per-model breakdown: `tokenomnom models --format json`; read `data.rows`, especially `model`, `total_tokens`, `cost_usd`, and `cost_share`.
- Usage patterns: `tokenomnom heatmap --format json`; read `data.window`, `data.metric`, `data.days`, and `data.stats`.
- Effective rates: `tokenomnom pricing --format json`; read `data.models[].entries` and their status and source.
- Full data export: `tokenomnom export --format json`; read `data.rows` and the diagnostic token counters.
- Discovery and store health: `tokenomnom doctor --format json`; read `data.providers`, `data.skills`, and `data.store`.
- Refresh stored usage: `tokenomnom sync --format json`; read the scan and ingestion counters in `data`.
- Install or update this skill: `tokenomnom install-skill --format json`; read `data.providers`.
- Freshness schedule: `tokenomnom schedule status --format json`; read `data.installed`, `mechanism`, interval fields, binary validity, and maintenance timestamps.

Provider, model, and explicit date filters are available on report commands.

## Freshness

- Usage sync freshness says when token accounting last scanned provider logs.
- Vault archive freshness says which byte-exact transcripts have been preserved;
  settled-file rules and the archive schedule can make it lag recent activity.
- History-index freshness will describe the future searchable prompt index. No
  history command exists yet, so do not imply that vault metadata is session or
  project search.

## Mining

For "what did I work on" or "how did I prompt X", use
`tokenomnom vault list --limit 100 --latest --format json` to inspect the
bounded storage manifest. Follow `data.page.next_cursor` with the same filters
until `has_more` is false. Then use
`tokenomnom vault cat <source-path> --format json` to read one archived source;
read `data.content` when `encoding` is `utf-8`, otherwise decode
`data.content_base64`.

As a temporary fallback, check the live Codex or Claude transcript directories
when the bounded vault manifest is not enough. Recent activity may not be
vaulted yet because it is still inside the settle window or the archive pass has
not run. This fallback goes away once first-class history commands exist.

## Reading JSON

Every response is one `tokenomnom.report/v1` envelope. Confirm `schema`, read
the command-specific `data`, and surface every item in `warnings` to the user.
Token counts are integers. `cost_usd` values and pricing rates are JSON numbers.

<!-- tokenomnom-skill v{{VERSION}} -->
