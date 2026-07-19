# HANDOFF — PR 9: Theme, Lip Gloss styling, terminal bar charts

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` (§9 Presentation).
Claude reviews; **Janior approves and merges — never merge yourself.**
List every deviation from this handoff in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reports local coding-agent token usage and
API list-price-equivalent costs. The full pipeline, plain-text reports,
pricing, and a JSON contract exist (PRs 2–8). This PR is where "very
colorful" happens: a theme system, styled tables, and terminal bar charts.
The GitHub-style heatmap is PR 10; the TUI is PR 11.

## Current repo state (main after PR 8)

- CLI: `doctor`, `sync`, `summary`, `daily`, `monthly`, `models`,
  `pricing`, `export`; global `--format pretty|json`. Pretty output is
  plain aligned tables written to `cmd.OutOrStdout()`.
- JSON contract (`tokenomnom.report/v1`) is FROZEN — this PR must not
  change a single byte of `--format json` or `export` output.
- Deps: cobra + modernc.org/sqlite. CI green on ubuntu/macos/windows.

## New dependencies (allowed, exactly these + transitives)

- `github.com/charmbracelet/lipgloss` — styling.
- `github.com/NimbleMarkets/ntcharts` — bar charts (lipgloss-native).
- `golang.org/x/term` — TTY detection.

## Part 1 — `internal/theme`

One package owns every color decision (PRs 10–11 reuse it):

- **Provider hue families**: Codex = teal family, Claude = orange family.
  Per-provider ramps of 4–5 shades for per-model coloring: deterministic
  shade assignment by model rank within provider (rank by total tokens,
  computed by the caller — the theme just maps provider+index → color).
- **Semantic styles**: header, subtle/dim, emphasis, warning (for the
  unpriced/unclassified notes), money, and a `proxy`/`estimated` badge
  style for pricing statuses.
- **Heatmap ramp**: 5-step intensity ramp (used by PR 10) — define it now
  so the palette lives in one place.
- All colors defined as lipgloss adaptive colors (light + dark variants)
  so both terminal backgrounds look intentional.
- **Render modes**: `Styled` vs `Plain`. Resolution: `Plain` when
  `--no-color` (new persistent root flag), or `NO_COLOR` env is set (any
  value), or stdout is not a TTY, or `--format json`. Styled otherwise.
  Centralize this decision — commands must not probe the environment
  themselves. In Plain mode every current output stays BYTE-IDENTICAL to
  today's tables (existing tests already write to buffers — non-TTY — and
  must keep passing unchanged; that is the regression guard).

## Part 2 — styled tables

When Styled: headers styled, provider names tinted with their hue, money
right-aligned in the money style, warning notes in the warning style,
proxy/estimated/override markers badged, subtle row separators or
alternate-row dimming where it aids scanning (taste call — keep it
restrained; a table that looks like a fruit salad is a fail). Applies to
`summary`, `daily`, `monthly`, `models`, `pricing`, `doctor`.

Also fix (from PR 7 review, applies in BOTH modes): rate display pads to
two decimals — `$12.50`, `$0.10` — in the pricing table. This is the one
sanctioned Plain-mode output change; update the affected test goldens
deliberately and call it out in the PR body.

## Part 3 — bar charts

- `daily` (pretty+Styled only): above the table, a horizontal-axis bar
  chart of **cost per day** over the selected rows — one bar per day,
  stacked codex/claude segments in provider colors, y-axis auto-scaled,
  compact legend (`■ Codex  ■ Claude`), currency-formatted axis labels.
  When every cost is zero (all unpriced), chart tokens instead and say so
  in the legend line.
- `monthly`: same treatment per month.
- Width: fit the terminal width (via x/term size; default 80 when
  unknown); bars ~2 columns wide with a documented minimum — when there
  are more periods than fit, show the most recent that do and print one
  subtle line: `showing last N of M days`.
- `--no-chart` flag on `daily`/`monthly` suppresses the chart. Charts
  never appear in Plain mode (piped output stays script-stable).
- Chart rendering must be pure string generation (testable with a forced
  width and color profile — termenv/lipgloss profile override in tests).

## Tests

- Plain-mode byte-stability: existing report tests pass unchanged (except
  the sanctioned `$12.50` goldens).
- Theme: mode resolution matrix (--no-color / NO_COLOR / non-TTY / json);
  deterministic model-shade assignment.
- Charts: fixed-width fixed-profile golden-ish renders for daily+monthly
  (assert structure — bar rows present, legend line, last-N notice — not
  exact ANSI bytes); zero-cost fallback; --no-chart.
- Money padding: `$12.50`, `$0.10`, `—` unchanged for unpriced.
- Race/verify as always; CI must stay green on Windows (lipgloss/termenv
  handle Windows terminals — do not add platform conditionals yourself).

## Out of scope — do NOT touch

- No heatmap (PR 10), no TUI (PR 11), no README/screenshots (PR 13).
- No JSON/export output changes whatsoever.
- No SQLite schema or query changes; no adapter/syncer/pricing changes
  beyond the `$12.50` formatting fix.
- No dependencies beyond the three listed.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real data, real terminal): `summary`, `daily --last 30`,
  `monthly`, `models`, `pricing` look coherent and restrained in both a
  dark and light terminal; `daily | cat` (piped) is byte-identical to
  pre-PR plain output; `NO_COLOR=1` likewise; charts render at 80 and 200
  columns without wrapping artifacts.

## Process

1. Branch from `main`: `pr9-theme-charts`.
2. Conventional commits.
3. PR title `feat: theme, styled tables, and terminal bar charts (PR 9)`
   via `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
