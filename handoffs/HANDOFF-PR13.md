# HANDOFF — PR 13: Ship — goreleaser, install.sh, release CI, full README

You are Codex, the implementer for this PR. You have no memory of prior
work. Read, in order: this file, `DESIGN.md` (§12 Distribution, §14
Open-source readiness), and the sibling repo
`/Users/janiorvalle/Documents/github/better-git-review` — its
`.goreleaser.yaml`, `install.sh`, `Makefile`, `.github/workflows/*`,
`CONTRIBUTING.md`, `SECURITY.md`, and `README.md` are the proven
templates this PR adapts. **Prerequisite: PRs 1–12 merged to `main`.**
Claude reviews; **Janior approves and merges — never merge yourself.**
List every deviation in the PR body.

## Project summary (context only)

**tokenomnom** (alias `nomnom`) is feature-complete: pipeline, pricing,
JSON contract, charts, heatmap, TUI, agent skill. This PR makes it
shippable and public-ready: reproducible release artifacts, a safe
installer, release/security CI, and the real README. The repo stays
PRIVATE — publishing is a separate later step (oss-baseline + PREFLIGHT),
but after this PR nothing should need to change except the visibility
flip.

## Current repo state (main after PR 12)

- Full CLI + TUI; `internal/version.Version` var ready for ldflags.
- Makefile: `build vet test verify install-hooks` (verify = build+vet+
  test — this PR extends it; a comment in the Makefile already promises
  that).
- CI: `ci.yml` (changes filter, verify, macOS/Windows matrix,
  workflow-lint, secret-scan), all actions SHA-pinned. `.githooks/` +
  `.gitleaks.toml` exist. MIT LICENSE. README is a stub.
- Committed assets: `assets/hero.png`, `assets/why-receipt.png`,
  `assets/why-brrr.png`.

## Part 1 — GoReleaser (`.goreleaser.yaml`, bgr-modeled)

- `version: 2`. Two builds: `tokenomnom` (`./cmd/tokenomnom`) and
  `nomnom` (`./cmd/nomnom`). Each: `CGO_ENABLED=0`, `-trimpath`,
  ldflags `-s -w -X github.com/janiorvalle/tokenomnom/internal/version.Version={{.Version}}`,
  goos darwin/linux/windows × goarch amd64/arm64.
- Archives: tar.gz (zip on Windows), both binaries per archive, name
  template `{{.ProjectName}}_{{.Version}}_{{.Os}}_{{.Arch}}`,
  `checksums.txt`. Changelog grouped by conventional-commit prefix,
  excludes `^test:`. **No brew/scoop/nfpm/docker publishers.**

## Part 2 — `install.sh` (bgr-modeled, adapt not copy-blind)

POSIX sh, `set -eu`. Env overrides `TOKENOMNOM_INSTALL_REPO` /
`_DIR` / `_BASE_URL` / `_VERSION` / `_ARCHIVE`. Darwin/Linux only
(point Windows users to the zip). Detect arch (amd64/arm64). Resolve
latest release via GitHub API, download archive + `checksums.txt`,
verify SHA-256, extract, smoke-test both binaries with `--version`,
install BOTH `tokenomnom` and `nomnom` to `$HOME/.local/bin` (no sudo)
with the transactional staging/backup/rollback trap pattern from bgr's
installer. Warn when the install dir is not on `PATH`.

## Part 3 — Makefile + CI extensions

- Makefile: add `release-check` (`goreleaser check` via pinned
  `go run github.com/goreleaser/goreleaser/v2@<pin bgr's version>`),
  `snapshot` (`release --snapshot --clean`), `install-smoke`
  (snapshot + `scripts/install-smoke.sh` exercising install.sh against
  the local dist archives via `TOKENOMNOM_INSTALL_*` overrides into a
  temp dir, asserting both binaries run). Extend `verify` to
  build+vet+test+release-check+install-smoke.
- `ci.yml`: add an `install-smoke` job — `shellcheck install.sh
  scripts/install-smoke.sh` then `make install-smoke` (ubuntu). Keep all
  hygiene rules (SHA-pinned + version comment, minimal permissions,
  concurrency, timeouts).
- `release.yml` (bgr-modeled): on tag `v*`; `if: github.repository ==
  'janiorvalle/tokenomnom'`; `environment: release`; permissions
  contents:write, id-token:write, attestations:write; steps: checkout
  (full history, no persisted creds), setup-go, `make build vet test
  release-check`, goreleaser action (pin bgr's SHA+version), then
  build-provenance attestation over `dist/*.tar.gz dist/*.zip
  dist/checksums.txt`.
- `scorecard.yml` (bgr-modeled): push main + weekly cron +
  workflow_dispatch, guarded to repo AND `visibility == 'public'` —
  dormant while private.
- `cla.yml` (bgr-modeled): contributor-assistant v2 (same pin),
  signatures on `signatures` branch, CLA text lives in CONTRIBUTING.md,
  `*[bot]` allowlist, zizmor-justified `pull_request_target`.
- `.gitignore`: add `/dist/` and `walkthrough-*.html`.

## Part 4 — README (the real one)

Written in Janior's voice — study bgr's README register: plainspoken,
problem-first, short sentences, zero marketing fluff. Shape:

1. `# tokenomnom` + centered hero (`assets/hero.png`, width 840, punchy
   alt text).
2. Problem paragraph: your coding agents nom tokens on a subscription;
   this shows what the bill WOULD have been at API list prices — daily/
   monthly patterns, per-model breakdowns, a spend heatmap, in the
   terminal. State the list-price-equivalent caveat here in prose.
3. **Install**: curl|sh one-liner (verifies checksums, `~/.local/bin`,
   no sudo), Releases zip for Windows, `go install` for Go users. Both
   `tokenomnom` and `nomnom` ship; `--version` to check.
4. **Why** section with the two gag images
   (`assets/why-receipt.png`, `assets/why-brrr.png`, centered, 840).
5. **Use it**: bare `tokenomnom` → dashboard; then real command examples
   with real-shaped output snippets: `summary`, `daily --last 30`,
   `models`, `heatmap`, `export --out usage.csv`. Keep snippets short.
6. **Agents**: `--format json` contract (link `docs/agent-api.md`),
   `install-skill` (opt-in, what it does, `--remove`).
7. **Configuration**: data discovery (auto + `--codex-dir/--claude-dir` +
   env vars), `--tz`, state dir location + `TOKENOMNOM_STATE_DIR`,
   pricing override file with a short example + the never-store-secrets
   note, `NO_COLOR`/`--no-color`. Document every knob that exists — and
   nothing that doesn't.
8. **How it counts** (short): local logs only, nothing leaves the
   machine, dedupe/attribution in one paragraph, retention note (the
   store preserves history agents delete), link `DESIGN.md` for depth.
9. Development (CONTRIBUTING/SECURITY links) + License (MIT).

## Part 5 — CONTRIBUTING.md + SECURITY.md (bgr-modeled)

- CONTRIBUTING: dev setup (`make verify` = the CI gate), test policy
  (tests never touch real `~/.codex`/`~/.claude`, never require network;
  parity test is env-gated and machine-specific), **"Writing a provider
  adapter"** as the flagship contribution guide (interface, UsageEvent
  semantics, fixture expectations, wiring into discover/syncer — the
  Claude two-stage variant as the complex example), release setup note
  (owner creates the `release` environment manually), licensing: MIT +
  CLA-on-first-PR paragraph (CLA text section included — adapt bgr's).
- SECURITY: GitHub private vulnerability reporting, 3-business-day
  response, fixes target latest release, **Trust model**: everything is
  local — tokenomnom reads local logs, writes a local DB, and makes NO
  network calls at runtime (install.sh is the only network touchpoint);
  pricing overrides are data not code; skill install writes only to
  agent skill dirs.

## Tests / verification

- `make verify` green with the extended gate (release-check +
  install-smoke).
- `make snapshot` produces archives for all 5 platform combos, each
  containing both binaries; `tokenomnom --version` in an extracted
  archive prints the snapshot version (ldflags path proven).
- shellcheck clean. actionlint/zizmor clean on the new workflows.
- README image links resolve; every documented flag/env var exists
  (spot-check script or careful manual pass — reviewer will diff docs
  against `--help` output).

## Out of scope — do NOT touch

- No behavior changes to any command. No new runtime dependencies.
- Do not modify `DESIGN.md`, `archive/`, `assets/` (reference only),
  `handoffs/`, adapters, syncer, store, pricing, TUI, skill content.
- Do not create the GitHub `release` environment, oss-baseline module
  entry, or flip visibility — owner tasks, out of band.

## Acceptance criteria

- All CI jobs green including the new install-smoke; `make verify`
  green locally end-to-end.
- Reviewer gate: snapshot archives inspected; installer exercised
  against local dist via overrides into a temp dir on the real machine;
  README rendered and fact-checked line-by-line against `--help` and
  actual behavior; workflows diffed against bgr's for hygiene parity.

## Process

1. Branch from `main`: `pr13-ship`.
2. Conventional commits.
3. PR title `feat: release tooling and public-ready docs (PR 13)` via
   `gh pr create`; list any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
