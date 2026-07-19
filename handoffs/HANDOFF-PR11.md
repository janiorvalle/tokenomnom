# HANDOFF — PR 11: Interactive TUI dashboard

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` (§9 Presentation).
**Prerequisite: PRs 1–10 are merged to `main`** — verify `heatmap` exists
before starting; if not, stop and say so. Claude reviews; **Janior
approves and merges — never merge yourself.** List every deviation in the
PR body. If this PR grows past ~2,500 added lines, split it (shell/
navigation first, chart views second) and say so in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) reports local coding-agent token usage
and API list-price-equivalent costs. Everything exists as styled CLI
commands: `summary`, `daily`/`monthly` (stacked bar charts), `models`,
`heatmap` (GitHub-style calendar), `pricing`, `doctor`, `sync`, `export`,
plus a frozen JSON contract. This PR wraps it all in a full-screen
Bubble Tea dashboard — the bare `tokenomnom` / `nomnom` experience.

## Current repo state (main after PR 10)

- `internal/theme`: single authority for palette + Styled/Plain
  resolution; provider hues (codex teal, claude orange), heatmap ramp.
- `internal/cli`: pure-string renderers for tables, bar charts (PR 9),
  and the heatmap grid (PR 10) — **reuse these**; if they need exporting/
  refactoring out of `cli` into a shared render package, that refactor is
  in scope but must be behavior-preserving (piped CLI output stays
  byte-identical — the existing tests are the guard).
- `internal/store` queries + `internal/pricing` costs + `internal/syncer`
  (fast incremental sync, ~100ms warm).
- Deps: bubbletea/bubbles already in go.mod as ntcharts transitives —
  promote to direct. No other new dependencies.

## Behavior

### Launch

- Bare `tokenomnom`/`nomnom` on a TTY (Styled mode per theme rules) →
  the dashboard. Non-TTY / `--no-color` / `NO_COLOR` / `--format json`
  bare invocation → exactly today's behavior (help, exit 0). Never ship
  a TUI to a pipe.
- On launch: if the store is missing/empty → run the initial full sync
  INSIDE the TUI with a progress view (spinner, files scanned, provider,
  elapsed). Otherwise render immediately from the store and kick a quiet
  incremental sync in the background (a bubbletea `Cmd`); refresh views
  and show a subtle `synced · 2s ago` status when it lands. Sync failure:
  status-line warning, data stays stale, app keeps working.

### Layout

- Header cards (one row): total cost (list-price equivalent), total
  tokens, active days, top model — respecting the active filters.
- Tab bar: **Daily · Monthly · Models · Heatmap**. Active tab underlined/
  highlighted per theme.
- Body: the active view. Footer: key hints + the disclaimer ("API
  list-price equivalents, not actual bills") + sync status.

### Views (reuse the CLI renderers)

- **Daily**: stacked cost bar chart + compact table for the visible
  window. `←`/`→` pan by week; `Home`/`End` jump to range edges.
- **Monthly**: same, panning by month.
- **Models**: sortable table — `s` cycles sort column (total ↓ default,
  cost, name); arrow keys scroll rows (bubbles table or equivalent).
- **Heatmap**: the PR 10 grid + caption; `←`/`→` shift the 12-month
  window by one month; `y` snaps to calendar year.

### Global keys

- `tab`/`shift+tab` or `1`–`4`: switch views. `p`: cycle provider filter
  (all → codex → claude). `r`: presets cycle for date range (30d → 90d →
  1y → all). `R`: force refresh (incremental sync now). `?`: help
  overlay listing all keys. `q`/`ctrl+c`: quit cleanly (restore
  terminal).
- Filters apply to every view and the header cards; active filters shown
  in the header (`provider: codex · range: 90d`).

### Sizing

- Handle `WindowSizeMsg`: reflow charts/heatmap to width, truncate
  tables with an indicator, minimum sane size (below ~60×15 show a
  "terminal too small" message rather than corrupt output).

## Structure

`internal/tui/` — `app.go` (root model/update/view, tab routing),
`views/` per view, `sync.go` (background sync Cmd + progress messages).
Keep the bubbletea Model pure: all I/O via Cmds; every state transition
unit-testable by feeding Msgs to Update.

## Tests

- Update-loop unit tests: tab switching, filter cycling, range presets,
  help overlay toggle, quit keys, WindowSizeMsg reflow state, sync
  progress → loaded transitions, sync-failure warning state.
- View render tests with forced size + Plain-profile lipgloss: each view
  renders (structure assertions, not ANSI bytes); too-small message.
- Launch policy test: non-TTY bare invocation still prints help
  (byte-identical to today).
- Race/verify as always; CI stays green on Windows (bubbletea supports
  it; no platform conditionals).

## Out of scope — do NOT touch

- No new dependencies beyond promoting bubbletea/bubbles to direct.
- No changes to CLI command output (piped byte-stability), JSON
  contract, store schema, pricing, syncer semantics, adapters.
- No README (PR 13), no skill (PR 12).
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real data, real terminal): bare `tokenomnom` opens the
  dashboard with correct header numbers (cross-checked against `summary`);
  all four views render and navigate; filters change every view + cards
  consistently; resize behaves; `q` restores the terminal; piped bare
  invocation still prints help byte-identically.

## Process

1. Branch from `main`: `pr11-tui`.
2. Conventional commits.
3. PR title `feat: interactive tui dashboard (PR 11)` via `gh pr create`;
   list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
