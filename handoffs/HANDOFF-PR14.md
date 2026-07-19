# HANDOFF — PR 14: First-run skill offer in the dashboard

You are Codex, the implementer for this PR. You have no memory of prior
work. Everything you need is here plus `DESIGN.md` §10 and the existing
`internal/skill` package. **Prerequisite: PRs 1–13 merged (main is
feature-complete).** Claude reviews; **Janior approves and merges — never
merge yourself.** List every deviation in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) is feature-complete. The agent skill
(PR 12) installs only via the explicit `install-skill` command, which only
README readers discover. The sibling project better-git-review offers its
skill during first-run interactive setup; tokenomnom has no setup flow
(zero config by design), so its natural "first-run moment" is the first
interactive dashboard launch. This PR adds a ONE-TIME, opt-in offer there.
Principle unchanged: opt-in, never automatic — just offered at the right
moment.

## Current repo state (main after PR 13)

- `internal/tui`: bubbletea dashboard (bare TTY invocation), initial-sync
  progress view, background quiet sync, status line.
- `internal/skill`: embedded SKILL.md, install/remove/inspect logic with
  per-provider results (installed / up to date / skipped: no root /
  refused: foreign), version marker parsing.
- `internal/store`: `meta` table with `SetMeta`/`DeleteMeta` (tx) and
  read via `Info()`/direct query.
- Full CI, extended `make verify`. Deps frozen — no new ones.

## Behavior

### When the offer appears

On dashboard launch (bare `tokenomnom`/`nomnom`, TTY), after the initial
data load completes (first-run sync finished, or store opened), show the
offer overlay IFF ALL of:

1. meta key `skill_offer` is absent, AND
2. the skill is not already installed for any provider (if it IS
   installed anywhere: silently write `skill_offer=preinstalled` and never
   prompt), AND
3. at least one provider root exists (a machine with neither `~/.codex`
   nor `~/.claude` never sees the offer — and does not burn it either:
   leave meta unset so a future launch after an agent appears can ask).

Errors reading meta or probing install state → do not prompt (fail
quiet, never block the dashboard).

The offer NEVER appears anywhere else: no report commands, no `sync`, no
JSON mode, no piped/non-TTY invocation (inherent — the TUI already only
launches on a TTY — but keep an explicit test).

### The overlay

Modal over the dashboard, theme-styled, restrained:

```
Teach your agents to use tokenomnom?

Installs an agent skill into the skills directory of your detected
coding agents (~/.claude, ~/.codex) so they can answer token-spend
questions themselves. Opt-in either way:
install later anytime with `tokenomnom install-skill`,
remove anytime with `tokenomnom install-skill --remove`.

[y] install   [n] not now   (this prompt appears only once)
```

- `y`/`Y`: run the install through the SAME code path as `install-skill`
  (a tea.Cmd — no blocking I/O in Update), then swap the overlay content
  to the per-provider result lines for a beat (dismiss on any key), write
  `skill_offer=accepted`.
- `n`/`N`/`esc`/`enter`: dismiss, write `skill_offer=declined`, and show
  a status-line note for this session: `skill not installed — run
  'tokenomnom install-skill' anytime`. Declining ONLY suppresses the
  prompt; the command works forever.
- `q` still quits the app from the overlay (counts as decline — record it).
- The PROMPT never returns after any answer — but nothing else is final:
  `install-skill` and `--remove` remain fully functional regardless of
  the recorded answer. `install-skill --remove` does NOT reset
  `skill_offer` (removal is a decision, not amnesia). A fresh state dir
  may legitimately re-ask — document that in a code comment.
- If a user who declined later runs `install-skill` manually, update
  `skill_offer` to `accepted` so doctor reflects reality.
- While the overlay is up, other keys are inert (no tab switching
  underneath); resizing keeps the overlay centered and legible.

### Meta values

`skill_offer` ∈ `accepted` | `declined` | `preinstalled`, written through
the normal store transaction path. Add the value to `doctor`'s Skills
section (pretty + JSON, additive: `"offer": "declined"` or null when
unset) so support questions are answerable.

## Docs

One README touch (sanctioned): in the Agents section, one sentence noting
the dashboard offers the skill once on first run and that
`install-skill`/`--remove` remain the manual path. Nothing else.

## Tests

- Model-level (feed Msgs to Update): offer appears when meta unset +
  roots exist + not installed; `y` → install Cmd issued, result state,
  meta `accepted`; each decline key → meta `declined` + status-line hint
  present; `q` from overlay → quit + `declined`; already-installed →
  `preinstalled` written, no overlay; no roots → no overlay AND meta
  stays unset; meta-read error → no overlay; overlay swallows tab/filter
  keys; second launch with each meta value → no overlay.
- Decline-then-manual-install: `install-skill` after `declined` still
  installs and flips `skill_offer` to `accepted`.
- Non-TTY bare invocation: byte-identical help (existing guard, keep it
  passing).
- doctor Skills offer field (pretty + JSON).
- Race/verify as always.

## Out of scope — do NOT touch

- No changes to `install-skill` CLI behavior or skill content. No new
  dependencies. No store schema changes (meta key only). No other README
  edits. Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`,
  CI, Makefile, adapters, syncer, pricing, discover.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real machine): fresh temp state dir + real roots → first
  dashboard launch shows the offer; `n` declines and never re-asks; a
  second fresh state dir with `y` installs into disposable roots
  (`--codex-dir`/`--claude-dir` overrides) and shows results; doctor
  reports the offer state; piped bare invocation unchanged.

## Process

1. Branch from `main`: `pr14-skill-offer`.
2. Conventional commits.
3. PR title `feat: first-run skill offer in dashboard (PR 14)` via
   `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
