# HANDOFF — PR 12: Agent skill (`install-skill` + embedded SKILL.md)

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` (§10) and
`docs/agent-api.md` (the JSON contract — your content source).
**Prerequisite: PRs 1–11 merged to `main`.** Claude reviews; **Janior
approves and merges — never merge yourself.** List every deviation in the
PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reports local coding-agent token usage
and API list-price-equivalent costs, with a full CLI, JSON contract, and
TUI. This PR makes the tool discoverable BY the agents it meters: an
embedded skill document installed into Claude Code's and Codex's skill
directories, so an agent asked "how much did I spend on tokens this
week?" knows to run `tokenomnom daily --last 7 --format json`.

## Current repo state (main after PR 11)

- CLI: full command set incl. `--format json` everywhere
  (`tokenomnom.report/v1` envelope, documented in `docs/agent-api.md`).
- `internal/discover`: provider root resolution (flags →
  `TOKENOMNOM_*_DIR` → `CODEX_HOME`/`CLAUDE_CONFIG_DIR` → `~/.codex`,
  `~/.claude`) — **reuse it**; skill dirs derive from the same roots.
- `internal/version`: build version. Deps frozen — no new ones.

## Part 1 — the skill document

`internal/skill/SKILL.md`, embedded via `go:embed`. Structure (final prose
is yours — match the terse, factual register of `docs/agent-api.md`; the
reviewer checks accuracy against actual command behavior):

1. **Frontmatter** (YAML): `name: tokenomnom`, `description:` one
   sentence starting with what the skill does and when to use it —
   mention token usage, spend/cost questions, and both `tokenomnom` and
   `nomnom` as trigger contexts.
2. **What it is** (2–3 sentences) incl. the non-negotiable framing:
   dollar figures are API list-price equivalents of subscription usage,
   NOT actual bills — instruct the agent to always relay that caveat.
3. **How to call it**: always `--format json`; note `nomnom` alias;
   `--no-sync` for fast repeated queries within one conversation; how to
   detect the binary is absent (and say so instead of guessing numbers).
4. **Task → command map** (the core): typical questions with exact
   command lines — spend today / this week / this month
   (`daily`/`monthly` with flags), per-model breakdown (`models`),
   overall (`summary`), usage patterns (`heatmap --format json`), rates
   (`pricing`), data export (`export`), health (`doctor`). One line each:
   command + which JSON fields answer the question.
5. **Reading the JSON**: envelope essentials (`schema`, `data`,
   `warnings` — surface warnings to the user), token integers vs
   `cost_usd` numbers.
6. **Version marker**: an HTML comment on the last line:
   `<!-- tokenomnom-skill vX.Y.Z -->` (injected at install time from
   `internal/version`, `dev` when unreleased) — the install logic keys on
   this marker.

Add a unit-testable guard: the embedded document mentions every command
name and the string `--format json` (same style as the agent-api doc
guard).

## Part 2 — `tokenomnom install-skill`

- Targets: `<claude-root>/skills/tokenomnom/SKILL.md` and
  `<codex-root>/skills/tokenomnom/SKILL.md`, roots from
  `discover.Resolve` (so `--codex-dir`/`--claude-dir`/env overrides work).
- Per provider: if the provider ROOT does not exist → skip with a notice
  (never create `~/.codex` on a codex-less machine). If the root exists →
  create `skills/tokenomnom/` as needed and write atomically.
- **Idempotent + safe**: if the target file exists WITH our version
  marker → overwrite freely (upgrades). If it exists WITHOUT the marker
  (user-authored/foreign) → refuse with a clear message; `--force`
  overrides. `--remove` deletes our file (marker required unless
  `--force`) and cleans up the empty dir.
- Output: per-provider result lines (installed / updated vX → vY /
  skipped: no root / refused: foreign file / removed). `--format json`
  supported: envelope `"command": "install-skill"`, providers[] {provider,
  path, action, version}.
- `doctor` gains one line per provider under a Skills section: installed
  version or `not installed` (parse the marker; foreign file → `foreign
  file present`). Include in doctor's JSON (additive).

## Tests

- Temp-root installs: fresh install, re-install idempotence, upgrade
  rewrites marker version, missing root skipped, foreign-file refusal +
  `--force`, `--remove` (+ refusal), atomic write, doctor Skills section
  (pretty + json), embedded-content guard, install-skill JSON envelope.

## Out of scope — do NOT touch

- No new dependencies. No changes to existing command output beyond the
  additive doctor Skills section. No README (PR 13 documents the skill).
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile, adapters, syncer, store, pricing, TUI.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real machine): `install-skill` installs into the real
  `~/.claude/skills/` and `~/.codex/skills/`; re-run reports up-to-date
  idempotence; `doctor` shows both; SKILL.md content is accurate against
  actual `--format json` output (reviewer cross-checks each mapped
  command); `--remove` cleans up.

## Process

1. Branch from `main`: `pr12-skill`.
2. Conventional commits.
3. PR title `feat: agent skill install (PR 12)` via `gh pr create`; list
   any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
