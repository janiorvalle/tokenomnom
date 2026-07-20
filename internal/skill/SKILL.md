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
Use `--no-sync` for fast repeated report queries within one conversation after
the first query has refreshed the store. If neither binary is available, say
that tokenomnom is not installed instead of guessing numbers.

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
For repeated reads, add `--no-sync` before `--format json`.

## Mining

For "what did I work on" or "how did I prompt X", use
`tokenomnom vault list --format json` to locate sessions, then
`tokenomnom vault cat <source-path> --format json` to read one. Check the live
Codex or Claude transcript directories for recent sessions not vaulted yet.

## Reading JSON

Every response is one `tokenomnom.report/v1` envelope. Confirm `schema`, read
the command-specific `data`, and surface every item in `warnings` to the user.
Token counts are integers. `cost_usd` values and pricing rates are JSON numbers.

<!-- tokenomnom-skill v{{VERSION}} -->
