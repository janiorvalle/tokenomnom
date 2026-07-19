# HANDOFF — PR 15: config.toml (full configurability) + SQLite backups

You are Codex, the implementer for this PR. You have no memory of prior
work. Read this file, `DESIGN.md` §14 (configurability methodology), and
better-git-review's `internal/config` + README Configuration section —
the proven pattern this PR adapts. **Prerequisite: PR 14 merged.**
Claude reviews; **Janior approves and merges — never merge yourself.**
List every deviation in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) is feature-complete but has no config
file — every knob is a flag or env var. This PR introduces
`~/.config/tokenomnom/config.toml` following the better-git-review
methodology (every knob configurable, every key documented with its
default, deliberate non-configurables listed), and adds automatic SQLite
backups. PRs 16–17 (vault, scheduler) will add their sections to this
config — design the package so sections are cheap to add.

## Current repo state (main after PR 14)

- Full CLI + TUI + skill. `internal/xdg`: `StateDir`, `ConfigDir`.
- Existing knob surface (this is the config inventory): `--codex-dir`,
  `--claude-dir`, `--tz`, `--no-color`, `--format`, `--no-sync`,
  `--last`, `--no-chart`, `--provider`, `--model`, `--since`, `--until`;
  env `TOKENOMNOM_CODEX_DIR/_CLAUDE_DIR/_STATE_DIR/_CONFIG_DIR`,
  `CODEX_HOME`, `CLAUDE_CONFIG_DIR`, `NO_COLOR`.
- `meta` table with tx `SetMeta`/`DeleteMeta`. Pricing override already
  reads `<ConfigDir>/pricing.json` (unchanged by this PR).
- Deps: cobra, modernc sqlite, lipgloss, ntcharts, x/term, klauspost —
  no, klauspost arrives in PR 16. One new dep sanctioned HERE:
  `github.com/BurntSushi/toml`.

## Part 1 — `internal/config`

- Load `<ConfigDir>/config.toml` (`TOKENOMNOM_CONFIG_DIR` honored via
  xdg). Missing file = all defaults, no error. Malformed file or invalid
  value = hard, clearly-worded error (bad config must never be silently
  ignored). Unknown keys = warning to stderr listing them (typo guard),
  not an error.
- **Precedence, applied uniformly and documented:** flag > environment
  variable > config file > built-in default. Existing env vars keep
  working exactly as today.
- Typed struct with defaults in one place (`Defaults()`), validation
  after merge. Injectable ConfigDir/env for tests (house style).

### Config surface v1 (every existing knob, bgr-style)

```toml
[discovery]
codex_dir = ""            # override Codex data root (flag --codex-dir)
claude_dir = ""           # override Claude data root (flag --claude-dir)

[sync]
timezone = ""             # IANA name; "" = system local (flag --tz)

[reports]
color = "auto"            # auto | always | never (--no-color = never)
charts = true             # false = --no-chart everywhere
daily_last = 30           # default for daily --last
default_provider = ""     # "" | codex | claude — default report filter

[backup]
enabled = true
interval = "24h"          # Go duration; how often a backup is due
dir = ""                  # "" = <data-dir>/backups (see Part 2)
keep = 14                 # retained backups; oldest pruned; 0 = keep all
```

- `[vault]` and `[schedule]` sections are PR 16/17 — do NOT add them,
  but structure the package so they slot in.
- Flags stay authoritative when set; a flag's help text does not change.
- **Deliberately non-configurable** (document in README, enforce by
  absence): attribution/dedupe rules, day-bucketing semantics, store
  schema, JSON contract, pricing math, cost formulas, disclaimer texts.

### `tokenomnom config` command

- `config path` — prints the config file path (whether it exists).
- `config show` — prints the EFFECTIVE config as TOML with a trailing
  comment per key showing its source (`# default`, `# config`,
  `# env TOKENOMNOM_...`, `# flag`). `--format json` supported
  (envelope, additive).
- No interactive editor (bgr's `configure` is out of scope).

## Part 2 — SQLite backups

- **Data dir**: add `xdg.DataDir()` — `TOKENOMNOM_DATA_DIR` override,
  else `$XDG_DATA_HOME/tokenomnom`, else `~/.local/share/tokenomnom`
  (unix/macOS), `os.UserConfigDir`-based on Windows. Default backup dir
  = `<data-dir>/backups` — intentionally NOT under the state dir.
- **Trigger**: after every SUCCESSFUL sync (any entry point — commands'
  quiet sync, `sync`, TUI background sync), if `backup.enabled` and
  `now − meta.last_backup_unix ≥ interval` → back up. Never on failed
  sync; never blocks reporting (backup failure = one warning line +
  `warnings` array in json, command still succeeds).
- **Method**: SQLite online backup via `VACUUM INTO` to a temp name in
  the backup dir, then atomic rename to
  `usage-YYYYMMDD-HHMMSS.db`. Update `meta.last_backup_unix` only on
  success.
- **Retention**: after a successful backup, prune oldest files beyond
  `keep` (match only our filename pattern — never delete anything else).
- `doctor` gains a Backups section (pretty + json, additive): enabled,
  dir, interval, last backup time, count, total size, newest file.

## Tests

- Precedence matrix per knob (flag/env/config/default) with injected
  env + temp config files; malformed config hard-errors; unknown-key
  warning; `config show` source annotations (golden-ish).
- Backup: due/not-due logic across meta states; VACUUM INTO produces an
  openable DB with identical `usage_daily` totals; retention prunes
  oldest only ours; failed-backup warning path; disabled = never runs.
- Existing behavior unchanged when no config file exists (full suite is
  the guard).
- Race/verify as always.

## Out of scope — do NOT touch

- No vault, no scheduler (PRs 16–17). Only `BurntSushi/toml` added.
- No store schema changes (meta keys only). No pricing changes. No JSON
  contract changes beyond additive `config show`/doctor sections.
- README: update the Configuration section to document every config key
  with its default + the precedence rule + non-configurables list
  (sanctioned; keep the section's voice).
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  adapters, syncer semantics, discover.

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real machine): with no config file, all behavior
  byte-identical to pre-PR; a config setting `reports.daily_last=7` and
  `backup.interval="1h"` demonstrably takes effect; flag still wins over
  config; `config show` annotates sources correctly; a real backup file
  appears after a sync, opens in sqlite3, matches live totals; pruning
  respects `keep`.

## Process

1. Branch from `main`: `pr15-config-backups`.
2. Conventional commits.
3. PR title `feat: config file and sqlite backups (PR 15)` via `gh pr
   create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
