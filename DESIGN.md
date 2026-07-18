# tokenomnom — Design Document

> *Your agents nom tokens. This shows the bill they would have run up.*

A colorful, cross-platform (macOS / Linux / Windows) terminal app that discovers local
Codex and Claude Code logs, reconstructs token usage per day and model, prices it at
standard API list rates, and visualizes spend patterns with terminal charts — plus an
agent-facing skill so Codex/Claude agents can query it themselves.

Status: **planning**. This document is the agreed architecture before implementation.

**Repo plan:** this directory (formerly `token-count`, renamed `tokenomnom`) is
the project. Remote (private for now):
`https://github.com/janiorvalle/tokenomnom.git`. The frozen 2026-07-18 analysis
lives in `archive/2026-07-18-snapshot/` (CSVs, workbook, the three `.mjs`
builders, and the original `HANDOFF.md`); the CSVs are additionally copied into
`testdata/` as golden fixtures when PR 6 needs them (§11).

**Working model:** Claude (reviewer) owns architecture, system design, drift
control, and code review. Codex (implementer) builds each PR from a standalone
brief in `handoffs/HANDOFF-PR<n>.md`. Codex starts every PR with zero memory of
previous ones, so each handoff must restate all context it needs: project
summary, current repo state, exact scope, contracts, conventions, acceptance
criteria, and an explicit out-of-scope list. Claude performed the Phase 0
bootstrap directly (folder rename, `git init` + remote, archive restructure,
hero promotion, this document, `handoffs/`); PR numbering starts with Codex's
scaffold PR.

**Merge protocol:** Codex implements → Claude reviews and reports findings →
**Janior approves and merges.** Claude never merges. (PR 1 was merged by Claude
before this rule was set; every PR from 2 on follows it.)

---

## 1. Goals and non-goals

### Goals

- One command, zero config: auto-discover `~/.codex` and `~/.claude` on all three OSes.
- Faithful port of the analysis logic proven in `HANDOFF.md` (the spec of record for
  extraction semantics).
- API list-price *equivalents* for subscription usage — clearly labeled as such.
- Beautiful terminal output: interactive TUI dashboard plus colorful static reports,
  including daily/monthly bar charts and a GitHub-style calendar heatmap.
- Instant startup after first run via an incremental SQLite cache.
- Agent-friendly: `--format json` on every report and an installable `SKILL.md` for
  `~/.claude/skills/` and `~/.codex/skills/`.

### Non-goals

- Reading server-side billing APIs or actual invoices.
- Real-time streaming/live metering (a `watch` mode may come later).
- Web UI. Terminal only.

## 2. Tech stack

| Concern | Choice | Why |
| --- | --- | --- |
| Language | Go (latest stable) | Single static binary per OS; fast JSONL streaming; trivial cross-compilation. |
| CLI framework | `cobra` | Standard subcommand/flag ergonomics, shell completions for free. |
| TUI | `bubbletea` + `bubbles` | The Charm ecosystem; best-in-class interactive terminals. |
| Styling | `lipgloss` | Gradients, adaptive light/dark palettes, `NO_COLOR` respected. |
| Charts | `ntcharts` (Charm-compatible) | Bar charts, sparklines, heatmap grids in the TUI and static output. |
| Cache | `modernc.org/sqlite` | Pure-Go SQLite — **no CGO**, keeps cross-compilation one-command. |
| Release | `goreleaser` | Homebrew tap, Scoop bucket, GitHub release binaries from one config. |

No external runtime dependencies. No `rg` — parsing is in-process streaming.

## 3. Architecture

Layered core with thin presentation. Every layer below Presentation is a plain Go
package with no terminal knowledge, so reports, TUI, and JSON output share one engine.

```
┌────────────────────────────────────────────────────────────┐
│ Presentation                                               │
│   TUI dashboard · static reports · exporters (csv/json/md) │
├────────────────────────────────────────────────────────────┤
│ Query & Aggregation                                        │
│   day/week/month × model × provider, tz-aware bucketing    │
├────────────────────────────────────────────────────────────┤
│ Pricing Engine                                             │
│   versioned rates, effective dates, proxy status, override │
├────────────────────────────────────────────────────────────┤
│ Usage Store (SQLite)                                       │
│   aggregated rows + per-file ingest checkpoints            │
├────────────────────────────────────────────────────────────┤
│ Ingestion (provider adapters)                              │
│   codex adapter · claude adapter · future: gemini, etc.    │
├────────────────────────────────────────────────────────────┤
│ Discovery                                                  │
│   OS-aware log roots, env/flag overrides                   │
└────────────────────────────────────────────────────────────┘
```

### Repo layout

```
tokenomnom/
├── cmd/tokenomnom/main.go
├── internal/
│   ├── discover/          # log-root discovery per OS
│   ├── ingest/
│   │   ├── adapter.go     # Provider interface + UsageEvent
│   │   ├── codex/         # ~/.codex sessions parser
│   │   └── claude/        # ~/.claude projects parser
│   ├── store/             # SQLite schema, checkpoints, upserts
│   ├── pricing/           # embedded rate table + overrides
│   ├── aggregate/         # grouping, date math, timezones
│   ├── report/            # static colored reports + charts
│   ├── export/            # csv / json / markdown writers
│   └── tui/               # bubbletea dashboard
├── skills/tokenomnom/SKILL.md   # embedded via go:embed, installed on demand
├── testdata/                    # golden fixtures (see §10)
└── .goreleaser.yaml
```

## 4. Discovery layer

Resolution order per provider (first hit wins):

1. Explicit flag: `--codex-dir`, `--claude-dir`.
2. Env: `TOKENOMNOM_CODEX_DIR`, `TOKENOMNOM_CLAUDE_DIR`, and provider-native vars
   (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`).
3. Defaults: `$HOME/.codex` and `$HOME/.claude` (macOS/Linux),
   `%USERPROFILE%\.codex` and `%USERPROFILE%\.claude` (Windows).

Scanned subtrees:

- Codex: `sessions/`, `archived_sessions/` (`*.jsonl`).
- Claude: `projects/**/*.jsonl`.

`tokenomnom doctor` prints what was found, per-provider file counts, date coverage,
and cache state. Missing providers degrade gracefully — one provider is enough.

## 5. Ingestion — provider adapters

One interface:

```go
type Adapter interface {
    Name() string                       // "codex", "claude"
    DiscoverFiles(root string) ([]SourceFile, error)
    ParseFile(f SourceFile, emit func(UsageEvent)) error
}

type UsageEvent struct {
    Timestamp     time.Time
    Provider      string
    Model         string
    Input         int64  // total input incl. cache components
    CacheRead     int64
    CacheWrite5m  int64
    CacheWrite1h  int64
    Output        int64  // includes reasoning for OpenAI
    Reasoning     int64  // informational subset (Codex only)
    SessionID     string
    MessageID     string // for cross-file dedupe (Claude)
}
```

### Codex adapter (rules from HANDOFF)

- Stream only `turn_context` and `token_count` events.
- Model from `turn_context.payload.model`; usage from
  `token_count.payload.info.last_token_usage` — **never** sum cumulative
  `total_token_usage` (double-counts).
- Buffer token events that precede the first model context; attribute them once the
  model appears. `unknown` only when genuinely unrecoverable.
- Recompute `total = input + output`.

### Claude adapter (rules from HANDOFF)

- Scan assistant messages with `usage` objects.
- **Global dedupe by `message.id`** (progressive snapshots repeat messages across
  writes). Keep the copy with the largest complete usage score; keep the earliest
  timestamp so usage stays on its original day.
- Use `usage.iterations` when present so fallback/multi-model turns are attributed to
  the model that actually ran each iteration.
- Split cache writes into 5-minute vs 1-hour buckets (different prices).

Claude dedupe is cross-file, so the Claude adapter dedupes within a full sync pass
(the store also enforces `MessageID` uniqueness as a backstop — see §6).

### Attribution policy (non-negotiable, carried over)

Never redistribute residual/unattributed tokens proportionally. Unknown models remain
explicit rows, surfaced in `doctor` and reports, until proven.

## 6. Usage store — SQLite incremental cache

Location: the **state** directory, not a cache directory — because of the
deletion-retention rule below, `usage.db` holds history that cannot be rebuilt from
logs and must not live somewhere users routinely wipe. Resolved by `internal/xdg`
(bgr pattern): `TOKENOMNOM_STATE_DIR` env override, else `$XDG_STATE_HOME/tokenomnom`,
else `~/.local/state/tokenomnom` (unix) / `%LocalAppData%\tokenomnom` (Windows).
`tokenomnom sync --full` re-ingests from scratch but still never deletes rows for
vanished files.

```sql
CREATE TABLE files (            -- ingest checkpoints
  path TEXT PRIMARY KEY, provider TEXT, mtime INTEGER,
  size INTEGER, byte_offset INTEGER, last_synced INTEGER
);
CREATE TABLE messages (         -- Claude dedupe backstop
  message_id TEXT PRIMARY KEY, usage_score INTEGER, ts INTEGER
);
CREATE TABLE usage_daily (      -- the query workhorse
  date TEXT, provider TEXT, model TEXT,
  input INTEGER, cache_read INTEGER,
  cache_write_5m INTEGER, cache_write_1h INTEGER,
  output INTEGER, reasoning INTEGER,
  PRIMARY KEY (date, provider, model)
);
```

Incremental logic per file: unchanged (`mtime`+`size` match) → skip; grown append-only
JSONL → resume from `byte_offset`; shrunk or rewritten → re-parse file after reversing
its prior contribution (tracked per file via a `file_daily` contribution table, or by
full-file reparse — decided at implementation, correctness first).

**Deletion is retention erosion, never reversal** (learned in PR 4 review): coding
agents actively delete old transcripts — Claude Code's ~30-day retention erased a full
day's usage from disk *within hours* of the frozen snapshot being taken. When a
previously-ingested file disappears, its contribution **stays** in the store; the
checkpoint row is marked missing (surfaced in `doctor`), never reversed. Once
synced regularly, tokenomnom's cache preserves history the agents' own logs throw
away — reversal applies only to files that still exist but were rewritten in place.

Raw events are *not* stored — only day×model aggregates and dedupe keys. Keeps the DB
tiny (thousands of rows, not millions).

**Timezone note:** aggregates are keyed by local date at ingest time using the
configured tz (default: system tz; `--tz` / config override). Changing tz triggers an
automatic full resync (tz is stored in a `meta` table and compared on startup).

## 7. Pricing engine

Rates are data, not code:

- **Embedded default table** (`go:embed pricing.json`): per model — base input, cache
  read, 5m write, 1h write, output (USD per 1M tokens), plus `status`
  (`published` | `proxy` | `estimated`), `source` URL, and an **effective date range**
  (models like Sonnet 5's intro rate through 2026-08-31 are modeled, and a rate change
  mid-history prices each day with the rate in force that day).
- **User override file**: `<config-dir>/tokenomnom/pricing.json`, deep-merged over the
  embedded table. `tokenomnom pricing` prints the effective table with status + source,
  flagging overrides and proxies.
- **Unknown models**: priced at $0 and flagged loudly in output (never guessed).
- The Spark precedent carries over: `gpt-5.3-codex-spark` ships priced at
  `gpt-5.3-codex` rates with `status: "proxy"` — never silently relabeled as published.

Cost formula everywhere: `tokens / 1_000_000 × rate`. Reasoning tokens are already in
output and never charged twice.

## 8. CLI surface

```
tokenomnom                    # interactive TUI dashboard
tokenomnom summary            # totals card: spend, tokens, top models
tokenomnom daily   [--last 30]
tokenomnom monthly
tokenomnom models             # per-model breakdown table
tokenomnom heatmap [--year 2026]   # GitHub-style calendar of daily spend
tokenomnom export  --format csv|json|md [--out FILE]
tokenomnom pricing            # effective rate table + sources
tokenomnom sync    [--full]   # force cache refresh
tokenomnom doctor             # discovered paths, coverage, cache health
tokenomnom install-skill      # write SKILL.md into agent skill dirs
```

Global flags: `--provider codex|claude|all`, `--model`, `--since`/`--until`, `--tz`,
`--format pretty|json`, `--no-color` (also honors `NO_COLOR`).

Every report command supports `--format json` with a stable schema — this is the
agent contract.

## 9. Presentation

### Static reports

Lip Gloss-styled tables and ntcharts bar charts; consistent palette with one hue per
provider (e.g. Claude = orange family, Codex = teal family) and shades per model.
Heatmap uses a 5-step intensity ramp like GitHub contributions. Adaptive to light/dark
terminals; degrades to plain tables when not a TTY or when `NO_COLOR` is set.

### TUI dashboard (bubbletea)

- Header cards: total list-price equivalent, total tokens, active days, top model.
- Tabs: **Daily** (30-day bar chart, arrow keys to pan) · **Monthly** ·
  **Models** (sortable table) · **Heatmap** (calendar year view).
- Filters: provider toggle (`c`/`x`/`a`), date-range presets.
- First launch runs the initial full scan inside the TUI with a progress bar;
  subsequent launches are instant (incremental sync in the background).

## 10. Agent integration — the skill

`tokenomnom install-skill` writes an embedded `SKILL.md` (plus keeps it current on
upgrade) to:

- `~/.claude/skills/tokenomnom/SKILL.md`
- `~/.codex/skills/tokenomnom/SKILL.md`

The skill teaches agents: what tokenomnom is, that dollar figures are API list-price
*equivalents* (not bills), and the exact commands to answer questions like "how much
did I nom this week?" — always via `--format json` (e.g.
`tokenomnom daily --last 7 --format json`). The installer is idempotent, respects the
same dir-discovery logic as §4, and `doctor` reports installed skill versions.

## 11. Testing strategy

- **Golden masters:** the frozen 2026-07-18 snapshot CSVs and workbook totals in this
  repo are regression fixtures. Synthetic-but-schema-exact JSONL fixtures in
  `testdata/` must reproduce known aggregates; and a local (untracked) integration test
  reconciles a real run against the workbook's $133,927.87 within rounding.
- **Adapter unit tests:** cumulative-vs-incremental Codex traps, pre-model buffering,
  Claude progressive-snapshot dedupe, iterations attribution, 5m/1h cache splits.
- **Pricing tests:** effective-date boundaries, override merging, proxy labeling.
- **Store tests:** append-resume, rewrite detection, tz-change resync.
- **Cross-platform CI:** GitHub Actions matrix (ubuntu/macos/windows) — parsing, path
  discovery, and build on all three.

## 12. Distribution

Follows the `better-git-review` pattern exactly (no brew/scoop taps):

- `goreleaser` GitHub Releases: darwin/linux/windows × amd64/arm64, tar.gz
  (zip on Windows), `checksums.txt`, `CGO_ENABLED=0`, `-trimpath -s -w`,
  version injected via ldflags.
- **Dual binary**: `tokenomnom` plus the short alias `nomnom` (`nomnom daily`,
  `nomnom heatmap`). Both ship in every archive. (`nom` was the first pick but
  it collides with guyfedwards/nom, an RSS reader in Homebrew core; `nomnom`
  is free — checked 2026-07-18.)
- A transactional, checksum-verifying `install.sh` targeting `~/.local/bin`
  (no sudo), with rollback-on-failure trap, env overrides
  (`NOM_INSTALL_DIR`, etc.), and a `--version` smoke test — modeled on bgr's.
- `go install github.com/janiorvalle/tokenomnom/cmd/tokenomnom@latest` as the
  third path. Windows: grab the zip from Releases.

## 13. Milestones and PR breakdown

1. **M1 — Correct core:** discovery, both adapters, SQLite cache, `summary`/`daily`/
   `models` as plain tables. Exit: golden-master parity with the frozen snapshot.
2. **M2 — Money:** pricing engine (effective dates, overrides, proxy status),
   `pricing`, `export`, `--format json` everywhere.
3. **M3 — Pretty:** Lip Gloss styling, bar charts, `heatmap`, palette system.
4. **M4 — TUI:** bubbletea dashboard with tabs, filters, first-run progress.
5. **M5 — Ship:** `install-skill`, goreleaser + install.sh, README polish.

~13 PRs, each CI-green and reviewable in one sitting:

| PR | Milestone | Scope |
| --- | --- | --- |
| 0 | M1 | Phase 0 bootstrap (Claude, direct to `main`): rename, `git init` + remote, archive snapshot, promote hero, DESIGN.md, `handoffs/` |
| 1 | M1 | Scaffold (Codex): Go module, `cmd/` skeleton (`tokenomnom` + `nomnom`), Makefile `verify`, CI workflows, gitleaks hooks, LICENSE, README stub with hero |
| 2 | M1 | Discovery layer + `doctor` |
| 3 | M1 | Codex adapter + fixture tests |
| 4 | M1 | Claude adapter + fixture tests |
| 5 | M1 | SQLite store + incremental sync |
| 6 | M1 | Aggregation + `summary`/`daily`/`models` plain tables + golden-master parity test |
| 7 | M2 | Pricing engine + `pricing` command |
| 8 | M2 | `export` + `--format json` everywhere |
| 9 | M3 | Palette + Lip Gloss styling + bar charts |
| 10 | M3 | `heatmap` |
| 11 | M4 | TUI dashboard (split into two PRs if large) |
| 12 | M5 | `install-skill` + embedded SKILL.md |
| 13 | M5 | goreleaser + `install.sh` + release workflow + README polish |

**Hero image:** `assets/concepts/hero-v3.png` (pixel monster nomming the spend
chart) is the chosen README hero direction — promote to `assets/hero.png` in
PR 1; other concepts stay in `assets/concepts/` until flip-time cleanup.

## 14. Open-source readiness (deferred until Janior says go)

The repo starts private and is built public-ready from day one, following the
patterns already proven in `better-git-review` (style/CI template) and
`oss-baseline` (GitHub settings scaffolding). Nothing here blocks M1–M4.

### README pattern (match bgr)

- Written in Janior's voice: plainspoken, lead with the problem, short
  paragraphs, no marketing fluff.
- Funny hero image at top — generated by Codex via `$imagegen` in MCP —
  referenced as `<p align="center"><img src="assets/hero.png" ...
  width="840">` with a punchy alt text. One or two more gag images under a
  "Why" section (bgr has `review-by-intent.png`, `no-skeletons.png`).
- Section shape: hero → problem paragraph → Install → Why (images) → Use it →
  Configuration (every key with its default) → Agents → Development → License.
- Clear "these are API list-price *equivalents*, not your bill" framing up top.

### Configurability methodology (match bgr)

- TOML user config at `~/.config/tokenomnom/config.toml`
  (`%APPDATA%\tokenomnom\` on Windows), XDG-aware via an `internal/xdg`
  package (`XDG_CONFIG_HOME`, `XDG_STATE_HOME`; cache under state dir).
- Precedence: flags > user config. (No repo-level config — tokenomnom isn't
  per-project, so bgr's repo-config trust gating doesn't apply. If `--by
  project` grouping later wants per-repo settings, revisit with the same
  trust model.)
- Every knob documented in the README with its default. Deliberately
  non-configurable invariants listed explicitly (e.g. attribution rules,
  no-proportional-smearing, dedupe keys).
- Nothing secret goes in config; if a future feature needs a credential it
  follows bgr's `api_key_env` pattern (config names the env var, never holds
  the value).
- Pricing override file (§7) already follows this methodology.

### CI / supply chain (match bgr)

- `make verify` is the single source-of-truth gate (build + vet + test +
  release-check + install-smoke), mirrored 1:1 in `ci.yml`.
- `ci.yml`: docs-only path filter job, verify on ubuntu, build/vet/test matrix
  on macOS + Windows, shellcheck + installer smoke, actionlint + zizmor
  (pedantic), gitleaks secret scan.
- Every action SHA-pinned with `# vX.Y.Z` comment; top-level
  `permissions: {}` with minimal per-job grants; `concurrency` +
  `timeout-minutes` everywhere.
- `release.yml`: tag `v*`, guarded to the canonical repo, `release`
  environment (owner-reviewed), goreleaser, build-provenance attestation over
  archives + checksums.
- `scorecard.yml`: dormant until the repo is public (same visibility guard).
- `cla.yml`: contributor-assistant v2, signatures on a `signatures` branch,
  CLA text lives in CONTRIBUTING.md, bots allowlisted.
- `.githooks/pre-commit` running `gitleaks protect --staged --redact`, opt-in
  via `make install-hooks`; `.gitleaks.toml` extending defaults.
- MIT license. CONTRIBUTING.md (setup, test policy, "writing a provider
  adapter" as the flagship contribution path — mirrors bgr's provider guide).
  SECURITY.md (private vulnerability reporting, response window, trust model:
  everything is local, nothing leaves the machine).

### GitHub settings via oss-baseline

When ready, add a `module "tokenomnom"` block to oss-baseline's `main.tf`
with matching `import {}` blocks in `imports.tf`:

- `required_status_checks` must exactly match this repo's CI job names.
- `dependabot_ecosystems`: `gomod`, `github-actions` (Dependabot config files
  stay repo-owned, per the baseline's split).
- `allowed_action_patterns`: the SHA-pinned actions this repo actually uses.
- `is_public = false` until flip day; follow a PREFLIGHT.md checklist like
  bgr's (squashed public-baseline commit, gitleaks over history, `make verify`
  green, flip visibility, enable secret scanning + release environment via
  the `is_public` toggle, private-vuln-reporting shim script, first tag).

### Agent-notes trail

Keep an `llm-docs/` directory like bgr's (specs, implementation logs,
handoffs, knob inventory) — gitignored or public per Janior's call at flip
time. `HANDOFF.md` and this `DESIGN.md` seed it.

## 15. Open questions (deferred, not blocking)

- `watch`/live mode tailing logs in near-real-time.
- Budget lines / "pace vs last month" projections.
- Additional adapters (Gemini CLI, opencode, Cursor CLI) — the adapter interface is
  designed for this.
- Whether to track per-project (Claude `projects/<dir>` gives this for free) as an
  extra grouping dimension. Likely yes in M2+ as `--by project`.
