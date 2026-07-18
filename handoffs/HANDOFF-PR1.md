# HANDOFF — PR 1: Repository scaffold

You are Codex, the implementer for this PR. You have no memory of prior work on
this project — everything you need is in this file plus the two references
below. Claude reviews your PR; expect review feedback in the PR thread.

Read these before writing code:

1. `DESIGN.md` at the repo root — the full architecture. Your PR is row "1" in
   the §13 PR table. Do not implement anything from later PRs.
2. `/Users/janiorvalle/Documents/github/better-git-review` — a sibling local
   repo by the same owner. It is the **style template** for Makefile, CI
   workflows, hooks, and README tone. Copy its patterns (and its SHA-pinned
   action versions), not its business logic.

## Project summary (context only)

**tokenomnom** (short binary alias: **nomnom**) is a colorful, cross-platform
Go CLI/TUI that scans local Codex (`~/.codex`) and Claude Code (`~/.claude`)
logs, reconstructs token usage per day and model, prices it at standard API
list rates (clearly labeled as list-price *equivalents*, not real bills), and
visualizes spend with terminal charts. Stack: Go + cobra + Charm (Bubble Tea /
Lip Gloss / ntcharts) + pure-Go SQLite. None of that behavior is built yet —
this PR is only the skeleton it will grow in.

## Current repo state

- Fresh git repo on `main`, remote `origin` =
  `https://github.com/janiorvalle/tokenomnom.git` (private).
- Contents: `DESIGN.md`, `archive/2026-07-18-snapshot/` (frozen prior
  analysis — CSVs, an XLSX, three `.mjs` scripts, `HANDOFF.md`),
  `assets/hero.png` (+ untracked `assets/concepts/`), `handoffs/` (this file),
  `.gitignore`.
- **No Go code, no Makefile, no CI, no LICENSE, no README yet.** You create
  those.

## Deliverables

### 1. Go module

- `go.mod`: module `github.com/janiorvalle/tokenomnom`, Go version = the
  stable toolchain installed on this machine (check `go version`).
- Only external dependency allowed in this PR: `github.com/spf13/cobra`.

### 2. Binaries and packages

```
cmd/tokenomnom/main.go     # thin main: calls internal/cli.Execute()
cmd/nomnom/main.go         # identical thin main — same CLI, short name
internal/cli/root.go       # cobra root command
internal/version/version.go
```

- `internal/version`: `var Version = "dev"` (a var, not const — release
  builds inject it via `-ldflags -X`), plus a small helper if useful.
- Root command: `Use: "tokenomnom"`, one-line Short, a Long description that
  includes the tagline "Your agents nom tokens. This shows the bill they would
  have run up." and states that dollar figures are API list-price equivalents,
  not actual bills. `--version` prints the version. No subcommands yet —
  running with no args prints help and exits 0.
- At least one real test (e.g. root command executes, `--version` output
  contains the version) so `go test ./...` exercises something.

### 3. Makefile

Model on better-git-review's Makefile. Targets for this PR:

- `build` → `go build ./...`
- `vet` → `go vet ./...`
- `test` → `go test ./...`
- `verify` → build + vet + test (this is the CI gate; `release-check` and
  installer smokes join it in PR 13 — leave a comment noting that)
- `install-hooks` → `git config core.hooksPath .githooks` (require `gitleaks`
  on PATH like bgr does)

### 4. Hooks and secret scanning

- `.githooks/pre-commit`: POSIX sh, `exec gitleaks protect --staged --redact`.
- `.gitleaks.toml`: extend default ruleset (`useDefault = true`); add an
  allowlist entry only if something in-repo actually needs it (likely nothing).

### 5. CI — `.github/workflows/ci.yml`

Mirror better-git-review's `ci.yml` structure and hygiene exactly, minus the
installer jobs (no `install.sh` exists yet):

- Triggers: `pull_request`, `push` to `main`. Concurrency group keyed on
  PR/ref with cancel-in-progress.
- Job `changes`: docs-only path filter (skip code jobs when every changed file
  is `*.md`), defaulting to code=true when unsure — copy bgr's approach.
- Job `verify` (ubuntu-latest): setup-go with `go-version-file: go.mod`,
  cache on; run `make verify`.
- Job `platform` matrix (macos-latest, windows-latest, `fail-fast: false`):
  `go build ./...`, `go vet ./...`, `go test ./...`.
- Job `workflow-lint`: actionlint (reviewdog action) + zizmor, same
  settings/pins as bgr.
- Job `secret-scan`: gitleaks action, `fetch-depth: 0`.

Hygiene requirements (non-negotiable, all copied from bgr):

- Every third-party action **SHA-pinned** with a trailing `# vX.Y.Z` comment —
  reuse the exact pins from bgr's workflows.
- Top-level `permissions: {}`; minimal per-job permissions.
- `timeout-minutes` on every job; `persist-credentials: false` on checkouts.

Do **not** add release.yml, cla.yml, or scorecard.yml — those come at
open-source flip time (PR 13 / later).

### 6. LICENSE

MIT, `Copyright (c) 2026 Janior Valle`.

### 7. README.md stub

Written plainly, matching bgr's README voice (short punchy sentences, no
marketing fluff). For now only:

- Centered hero: `<p align="center"><img src="assets/hero.png"
  alt="tokenomnom — your agents nom tokens. this shows the bill." width="840"></p>`
- One short paragraph: what tokenomnom is, the list-price-equivalent
  disclaimer, and that it ships as `tokenomnom` and `nomnom`.
- A "Status: under construction — see DESIGN.md" line.
- License line linking LICENSE.

Full README (install, usage, config tables, gag images) is PR 13. Keep the
stub honest — don't document commands that don't exist.

## Out of scope — do NOT touch

- No parsing/ingestion, discovery, SQLite, pricing, charts, TUI, skill, or
  goreleaser/install.sh code. No Charm dependencies yet.
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, or
  `.gitignore` (if the scaffold genuinely needs a new ignore entry, add it and
  call it out in the PR description).
- No new external dependencies beyond cobra.

## Acceptance criteria

- `make verify` passes locally; `gofmt -l .` is clean.
- `go run ./cmd/tokenomnom --version` and `go run ./cmd/nomnom --version`
  both print a version; no-args prints help, exit 0.
- CI workflow passes `actionlint` locally if available.
- No secrets, no absolute machine paths in committed code.

## Process

1. Branch from `main`: `pr1-scaffold`.
2. Conventional commits (`feat: ...`, `ci: ...`, `chore: ...`).
3. Push the branch and open a PR titled `feat: repository scaffold (PR 1)`
   with `gh pr create`, body summarizing deliverables and any deviations from
   this handoff (deviations must be listed explicitly — the reviewer checks
   for drift).
4. Do not merge — Claude reviews first.
