---
name: tokenomnom
description: Reports local coding-agent (Codex, Claude Code) token usage and API-equivalent spend, and searches indexed transcript history for workflow analysis. Use for token, cost, or spend questions, for "what did I work on" session-history mining, or whenever tokenomnom or nomnom is mentioned.
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
- Transcript search: `tokenomnom history search "literal phrase" --limit 50 --format json`; inspect bounded snippets, then retrieve selected evidence with `history show`.
- Agent proposal/claim search: first check `doctor.data.history.index_assistant_enabled` and `assistant_indexed`, then use `history search "literal phrase" --role assistant --format json`; report the not-indexed warning honestly.
- User-initiated transcript search: add `--root-only` only when the question is specifically about sessions the user started.
- Delegated-work search: add `--thread-kind subagent`; keep the default/all view when root and delegated work both matter.
- Prompt enumeration: `tokenomnom history prompts --limit 100 --format json`; use `--include-text` only when complete clean prompts are necessary.
- Corpus statistics: `tokenomnom history stats --group-by provider --format json`; never infer conclusions from counts without checking coverage and warnings.
- Broad corpus analysis: `tokenomnom history sample --strategy stratified --group-by month,repo --count 25 --format json`; use a different `--seed` only when another deterministic sample is needed.

Provider, model, and explicit date filters are available on report commands.

## Freshness

- Usage sync freshness says when token accounting last scanned provider logs.
- Vault archive freshness says which byte-exact transcripts have been preserved;
  settled-file rules and the archive schedule can make it lag recent activity.
- History-index freshness says which clean user and explicitly consented assistant prompts are currently covered
  by `history search`, `history list`, `history prompts`, and `history stats`.

## Mining

For "what did I work on" or "how did I prompt X":

1. Run `tokenomnom doctor --format json` and `tokenomnom history status
   --format json`; surface readiness, coverage, and warnings.
2. If the index is missing,
   stale, degraded, or does not cover the needed dates, run
   `tokenomnom history index --format json` and surface partial-index errors.
3. Apply bounded provider, date, cwd, repo, branch, source, and thread filters
   before retrieving text.
4. Use `tokenomnom history search <query> --limit 50 --format json` for known
   exact adjacent language. Follow `data.page.next_cursor` with identical filters.
   Literal search treats punctuation as token separation; use `--fts-query`
   only when boolean, NEAR, or prefix syntax is actually required.
   Use `--role assistant` only for what the agent proposed or claimed;
   `--role any` combines indexed roles. The default remains `--role user`.
5. For broad corpus questions without known language, use deterministic
   stratified sampling: `tokenomnom history sample --group-by month,repo
   --count 25 --format json`. The default seed is stable; state the strata and
   returned-sample coverage rather than treating the sample as a generated
   topic model. Use status or stats for full-index coverage.
6. Use `tokenomnom history list --limit 100 --format json` to discover
   sessions when session metadata is the evidence needed.
   Add `--root-only` for the user's own initiated sessions, `--thread-kind
   subagent` for delegated work only, and no relationship filter when both
   matter.
7. Retrieve only selected evidence with `tokenomnom history show <prompt-id>
   --format json`, `history show <session-id> --prompts --limit 100 --format
   json`, or explicit `history show <session-id> --raw --format json`.
8. Read `data.coverage` and surface every envelope warning. Date requests can
   extend outside indexed coverage. Repository and branch metadata are
   Codex-complete but Claude-partial; prefer `--cwd` for cross-provider
   completeness and disclose that limitation in the final answer.
9. Read `data.coverage.roles` and `data.coverage.thread_kind.unknown`, disclose unknown relationship
   coverage. Root/subagent classification is evidence-backed but is not
   complete for every provider version or transcript, and state the searched
   index coverage in the final answer.

Do not traverse provider directories unless tokenomnom reports an unsupported schema or index failure
that prevents this workflow. Assistant coverage exists only after explicit
consent and indexing. Never claim that search covers system, developer,
thinking, tool-call, or tool-result text.

## Reading JSON

Every response is one `tokenomnom.report/v1` envelope. Confirm `schema`, read
the command-specific `data`, and surface every item in `warnings` to the user.
Token counts are integers. `cost_usd` values and pricing rates are JSON numbers.

<!-- tokenomnom-skill v{{VERSION}} -->
