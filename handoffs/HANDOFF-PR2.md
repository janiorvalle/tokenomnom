# HANDOFF — PR 2: Discovery layer + `doctor` command

You are Codex, the implementer for this PR. You have no memory of prior work on
this project — everything you need is in this file plus `DESIGN.md` at the repo
root (read §4 Discovery, §8 CLI surface). Claude reviews your PR; **Janior
approves and merges — never merge yourself.**

## Project summary (context only)

**tokenomnom** (alias `nomnom`) is a cross-platform Go CLI that scans local
Codex (`~/.codex`) and Claude Code (`~/.claude`) logs, reconstructs token usage
per day/model, and prices it at API list rates. Parsing, storage, pricing, and
charts are LATER PRs. This PR only teaches the tool to *find* the logs and
report what it found.

## Current repo state (main after PR 1)

- Go module `github.com/janiorvalle/tokenomnom` (Go 1.25.x), dependency: cobra.
- `cmd/tokenomnom` + `cmd/nomnom` → `internal/cli.Execute()`; root command has
  no subcommands yet. `internal/version` holds the version var.
- Makefile (`verify` = build+vet+test), CI on ubuntu/macos/windows, gitleaks
  hooks. All green on `main`.

## The portability rule (top priority)

This must behave correctly on machines that are nothing like the author's:
Linux, macOS, and Windows; machines with only Codex, only Claude, both, or
**neither**; empty or partially populated log dirs; unreadable dirs.

- Never hardcode absolute paths or a username. Home = `os.UserHomeDir()`.
  All joins via `path/filepath`.
- Zero providers found is a **valid, non-error state**: commands report it
  plainly and exit 0.
- A root that exists but has no session files is valid (fresh install).
- Permission errors while walking must not crash the walk: record the error,
  keep going, surface it in `doctor` output.
- Follow the top-level root if it is a symlink (`filepath.EvalSymlinks` on the
  root only is fine); do not recurse into cyclic symlinks.
- All tests must pass on Windows CI: no `/` string concatenation, no
  Unix-only assumptions, use `t.TempDir()`.

## Deliverables

### 1. `internal/discover` package

Types (shape is yours; keep exported surface small and documented):

- `Provider` — `"codex"` / `"claude"` constants.
- `Root` — provider, absolute path, `Source` (how it was chosen: `flag`,
  `env:TOKENOMNOM_*`, `env:<native>`, `default`), and whether it exists.
- `SourceFile` — path, size, mod time, provider.

Functions:

- `Resolve(...)` — compute each provider's root using this precedence
  (first set wins), per DESIGN §4:
  1. explicit flag value (`--codex-dir` / `--claude-dir`)
  2. `TOKENOMNOM_CODEX_DIR` / `TOKENOMNOM_CLAUDE_DIR`
  3. native env: `CODEX_HOME` (codex), `CLAUDE_CONFIG_DIR` (claude)
  4. default: `<home>/.codex`, `<home>/.claude` (same dotted names on
     Windows under the user profile — `os.UserHomeDir()` covers it)
- `ListSourceFiles(root)` — enumerate `*.jsonl` files per provider:
  - codex: `sessions/` (recursively — real installs nest `YYYY/MM/DD/`) and
    `archived_sessions/` (recursively — real installs are flat, but do not
    assume flatness). Both subdirs optional.
  - claude: `projects/` recursively.
  - Enumerate with `stat` info only — never open/read file contents. Log dirs
    can be many GB; enumeration must stay fast.
  - Return files plus a list of non-fatal walk errors.

**Testability requirement:** the resolver must not read the process
environment or home dir directly. Inject them (e.g. an options struct with
`Home string` and `Getenv func(string) string`, defaulted in production).
Unit tests cover the full precedence chain and never touch the real `~`.

Observed real-world layouts, as examples only — treat structure defensively,
do not require it:

```
~/.codex/sessions/2026/06/13/rollout-2026-06-13T21-10-04-<uuid>.jsonl
~/.codex/archived_sessions/rollout-2026-02-23T23-50-10-<uuid>.jsonl
~/.claude/projects/-Users-jdoe-Documents-github-myapp/<uuid>.jsonl
```

### 2. Global flags

Add persistent flags `--codex-dir` and `--claude-dir` (string, default empty)
to the root command. They apply to all current and future subcommands.

### 3. `doctor` subcommand

`tokenomnom doctor` prints, per provider: resolved path and its source
(flag/env/default), whether it exists, `*.jsonl` file count, total size
(human-readable), oldest and newest file mod-time dates, and any walk errors.
End with a one-line overall status (e.g. both found / one found / none found —
with a friendly hint about flags/env vars when none found). Always exit 0
unless a genuinely unexpected internal error occurs.

Plain, uncolored, aligned text output — styling arrives in PR 9, and cache
state joins doctor in PR 5. Write output to the command's `OutOrStdout()`
writer (not `os.Stdout` directly) so it's testable.

### 4. Tests

- Resolver precedence: flag > tokenomnom env > native env > default, per
  provider, via injected home/getenv.
- Enumeration against `t.TempDir()` fixtures: nested codex layout, flat
  archived layout, claude projects, empty root, missing root, non-jsonl files
  ignored.
- `doctor` golden-ish test: run the cobra command against fixture dirs and
  assert on key output lines (avoid brittle full-output comparison —
  mod-times vary).

## Out of scope — do NOT touch

- No JSONL parsing or reading file contents. No SQLite. No pricing. No color.
- No `--format json` yet (PR 8 adds the JSON contract everywhere).
- No new dependencies. Standard library + cobra only.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI, Makefile,
  LICENSE, or README.

## Acceptance criteria

- `make verify` green locally; `gofmt -l .` clean; CI green on all three OSes.
- On a machine with neither provider: `tokenomnom doctor` exits 0 with a clear
  message. With flags/env pointing at a fixture dir: doctor reflects it and
  labels the source correctly.
- No file contents are ever read; `doctor` on a multi-GB tree completes in
  seconds.

## Process

1. Branch from `main`: `pr2-discovery`.
2. Conventional commits.
3. Push and open a PR titled `feat: discovery layer and doctor command (PR 2)`
   via `gh pr create`. In the body: summary + an explicit list of any
   deviations from this handoff (the reviewer checks for drift).
4. **Do not merge.** Claude reviews, Janior approves and merges.
