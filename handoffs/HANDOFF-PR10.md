# HANDOFF — PR 10: GitHub-style spend heatmap

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` (§8, §9). Claude
reviews; **Janior approves and merges — never merge yourself.** List every
deviation in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reports local coding-agent token usage and
API list-price-equivalent costs, with a styled terminal UI (theme package,
provider hues, bar charts — PR 9). This PR adds the signature visual: a
GitHub-contributions-style calendar heatmap of daily spend.

## Current repo state (main after PR 9)

- CLI: `doctor`, `sync`, `summary`, `daily`, `monthly`, `models`,
  `pricing`, `export`; global `--format pretty|json`, `--no-color`,
  `--tz`; report flags `--provider/--model/--since/--until/--no-sync`.
- `internal/theme`: adaptive palette, Styled/Plain resolution (single
  authority), and a **5-step heatmap intensity ramp already defined** —
  use it, do not invent colors.
- `internal/store` queries incl. `Daily(filter)`; `internal/pricing` for
  per-row costs. JSON contract `tokenomnom.report/v1` (additive changes
  allowed). Deps: cobra, sqlite, lipgloss, ntcharts, x/term — no new ones.

## The command

`tokenomnom heatmap [--year YYYY] [--provider ...] [--model ...]
[--no-sync]`

- Default window: trailing 12 months ending today (inclusive), aligned to
  whole weeks. `--year 2026` selects that calendar year instead.
  `--year` with `--since/--until` is an error (no since/until on this
  command otherwise — keep it simple; add them only if free).
- Metric: **cost per day**; when every day in the window is unpriced,
  fall back to total tokens and say so in the caption line.

### Layout (GitHub-style)

- Grid: columns = weeks (Sunday-start), rows = the 7 weekdays; row labels
  `Mon`/`Wed`/`Fri` only; month abbreviations across the top aligned to
  the week where the month starts.
- Cells: theme heatmap ramp. Level 0 = zero-cost day (dim empty cell);
  levels 1–4 = quartiles of the NONZERO daily costs in the window
  (GitHub's approach — robust to your $28k outlier days). Days outside
  the window (leading/trailing partial weeks) render blank.
- Cell width: 2 columns when the terminal fits the full window at 2
  (≈ 115+ cols), else 1 column. If even 1-column cells cannot fit the
  window, show the most recent whole months that fit plus a subtle
  `showing MMM–MMM of <window>` line.
- Legend + caption below: `Less ▪▪▪▪▪ More`, plus one line with active
  days, total cost in window, busiest day (`2026-07-13 · $28,474.65`),
  and longest streak of consecutive active days.
- **Plain mode** (piped / NO_COLOR / --no-color): same grid using density
  glyphs `·░▒▓█` for levels 0–4 — the command stays useful without color.
- **JSON mode**: standard envelope, `"command": "heatmap"`, data: window
  {from, to}, metric (`cost_usd` | `tokens`), days[] {date, cost_usd,
  total_tokens, level}, and the caption stats as structured fields.
  Document in `docs/agent-api.md` (additive — no version bump).

### Correctness details

- Day bucketing uses the store's dates as-is (already tz-bucketed at
  ingest) — do NOT re-bucket.
- Quartile edges: nonzero costs sorted; level n = value ≤ quartile n
  boundary (define precisely in code comments + tests; ties bias upward).
  One nonzero day → it is level 4.
- Streak = consecutive calendar days with nonzero metric, computed within
  the window only.

## Tests

- Level bucketing: quartiles with outliers, single-active-day, all-zero
  (tokens fallback), ties.
- Layout goldens (fixed width, Plain glyphs): month label alignment,
  week alignment for a window starting mid-week, blank out-of-window
  cells, 1-col fallback, months-truncation line.
- Caption stats: busiest day, streak (incl. streak crossing the window
  edge — counts only in-window days).
- `--year` validation; `--year` + `--since` error.
- JSON: envelope + days array + level values; agent-api.md mentions
  heatmap (extend the existing doc guard test).
- Race/verify as always.

## Out of scope — do NOT touch

- No TUI (PR 11), no README (PR 13). No new dependencies. No SQLite
  schema changes. No changes to existing commands' output. Do not modify
  `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI, Makefile, README,
  adapters, syncer, pricing rates. Theme: additive only if something is
  genuinely missing (call it out).

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real data): `heatmap` renders the trailing year with
  sane month labels and a busiest-day caption matching known data
  (2026-07-13 should dominate); piped output shows the glyph grid;
  `--format json | python3 -m json.tool` parses; `--year 2026` matches.

## Process

1. Branch from `main`: `pr10-heatmap`.
2. Conventional commits.
3. PR title `feat: github-style spend heatmap (PR 10)` via `gh pr
   create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
