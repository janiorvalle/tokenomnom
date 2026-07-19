# HANDOFF — PR 17: Auto-vault, OS scheduler, skill/docs polish

You are Codex, the implementer for this PR. You have no memory of prior
work. Read this file, `DESIGN.md`, and the PR 15/16 config + vault
packages. **Prerequisite: PRs 15–16 merged.** Claude reviews; **Janior
approves and merges — never merge yourself.** List every deviation in
the PR body.

## Project summary (context only)

**tokenomnom** now has a config file, SQLite backups, and a manual
transcript vault. This PR closes the lifecycle loop: vaulting happens
automatically, and a per-user OS scheduler keeps everything fresh on
machines that go days without an interactive run. Explicit decision
from Janior + Claude: **no daemon** — the OS scheduler (launchd /
systemd user timer / Windows Task Scheduler) runs a one-shot
maintenance tick instead. Do not implement a resident process, file
watcher, or long-running mode.

## Current repo state (main after PR 16)

- `internal/config` (flag > env > config > default; `[backup]`,
  `[vault]` incl. `auto = true` stored but not yet acted on).
- Backups trigger after successful syncs when due. `internal/vault`:
  archive/verify/list/cat/status, settled-file policy (`min_age`),
  manifest in SQLite, never deletes sources.
- Single-process store lock (WAL + lock file) — a second invocation
  fails fast. Deps frozen — NO new dependencies in this PR.

## Part 1 — auto-vault wiring

- After every SUCCESSFUL sync (same hook point as backups), when
  `[vault] auto = true`: run the settled-file archive pass for the
  configured providers. Order: sync → backup-if-due → auto-vault.
- Frequency guard: at most once per `[vault] auto_interval` (new config
  key, default `"24h"`) via a meta key — archiving is cheap when there
  is nothing to do, but don't even scan bundles on every report.
- Quiet by default in report commands (one status line only when files
  were actually vaulted; details in `--format json` warnings/data);
  full output in explicit `sync` and `vault archive`.
- Failure is non-fatal everywhere: warning line + json warning, command
  still succeeds, next tick retries.

## Part 2 — `tokenomnom schedule` (OS scheduler integration)

- `schedule install` — writes the platform-appropriate per-USER unit
  (never system-wide, never sudo):
  - macOS: `~/Library/LaunchAgents/com.janiorvalle.tokenomnom.plist`
    (label matches filename), `launchctl bootstrap gui/$UID` (fall back
    to `load` on older macOS).
  - Linux: `~/.config/systemd/user/tokenomnom.service` + `.timer`
    (`OnUnitActiveSec` from interval, `Persistent=true`), enable via
    `systemctl --user enable --now tokenomnom.timer`.
  - Windows: `schtasks /Create` per-user task.
  - The job runs: `<absolute path to current executable> sync
    --scheduled` at `[schedule] interval` (new config section, default
    `"24h"`). Embed the absolute binary path at install time; re-running
    `schedule install` after moving/upgrading the binary refreshes it
    (say so in the install output).
- `schedule status` — installed?, platform mechanism, configured
  interval, unit/task path, binary path the unit points at (flag when
  it no longer exists), last sync + last backup + last auto-vault times
  from meta.
- `schedule uninstall` — removes exactly what install created; clean
  errors when not installed. All three support `--format json`
  (additive).
- `sync --scheduled` behavior: quiet single-line summary; if the store
  lock is held (interactive session running), exit 0 with a
  `skipped: store in use` line — a scheduled tick must never fight the
  user or report failure noise to the OS scheduler.
- Config:

```toml
[schedule]
interval = "24h"     # how often the scheduled tick runs
```

- `schedule install` with the config missing/default just works; the
  unit re-reads config at each run (interval is the only install-time
  baked value besides the binary path — document that changing
  `schedule.interval` requires `schedule install` again, and make
  status say when the installed interval differs from config).

## Part 3 — skill, agent-api, README, doctor

- `docs/agent-api.md`: document vault + schedule + config-show JSON
  shapes (additive, no version bump).
- `internal/skill/SKILL.md`: add a Mining section — agents answering
  "what did I work on / how did I prompt X" should use
  `vault list --format json` to locate sessions and `vault cat` to read
  them, falling back to live transcript dirs for unvaulted recency; and
  a line for `schedule status` when asked whether tokenomnom is keeping
  itself fresh. Keep the terse register; bump nothing else.
- README: "Keep it fresh" section — schedule install/status/uninstall,
  what the tick does (sync → backup → auto-vault), the no-daemon
  design note, plus config keys. Extend Configuration section with
  `[schedule]` + `vault.auto_interval`.
- `doctor`: Schedule section (mechanism, installed, interval,
  binary-path validity, last tick) — pretty + json, additive.

## Tests

- Auto-vault: fires after sync when due + auto on; interval guard
  suppresses; disabled config never fires; failure is non-fatal with
  warning; report-command quiet vs sync verbose output.
- Scheduler: unit/plist/task content goldens per platform (generate to
  a temp dir with injected paths — do NOT install units in CI);
  install/uninstall round-trip on the host OS where safe to do in a
  temp-override location; status parses installed vs not, stale binary
  path, interval drift.
- `sync --scheduled`: quiet output; lock-held → exit 0 skip line.
- Skill/agent-api guard tests extended (mention vault, schedule).
- Race/verify as always.

## Out of scope — do NOT touch

- No daemon/watcher/resident anything. No new dependencies. No
  encryption. No deletion of source files. No store schema changes
  (meta keys only). No JSON version bump.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  adapters, syncer semantics, pricing.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real machine, macOS): `schedule install` creates a
  valid LaunchAgent pointing at the real binary; `schedule status`
  reports it; a manually-triggered `sync --scheduled` runs quietly and
  skips politely while the TUI holds the lock; `schedule uninstall`
  removes it cleanly; auto-vault demonstrably vaults a settled fixture
  after sync and respects `auto_interval`; skill + agent-api accuracy
  spot-checked against real output.

## Process

1. Branch from `main`: `pr17-autovault-schedule`.
2. Conventional commits.
3. PR title `feat: auto-vault and os scheduler (PR 17)` via `gh pr
   create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
