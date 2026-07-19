# HANDOFF — PR 16: Transcript vault (archive · verify · list · cat)

You are Codex, the implementer for this PR. You have no memory of prior
work. Read this file and `DESIGN.md`. **Prerequisite: PR 15 merged**
(config package + data dir exist). Claude reviews; **Janior approves and
merges — never merge yourself.** List every deviation in the PR body.

## Why this exists (context)

Coding agents delete their own history: Claude Code's ~30-day cleanup
erased a full day of transcripts within hours of this project's original
snapshot. Janior mines these transcripts for workflow insights, and the
Codex log dir is 40+ GB he wants to eventually reclaim. The vault is
tokenomnom's answer: compress raw session files losslessly into a local
archive with a queryable manifest, verify them, and report what is safe
to reclaim — **raw bytes are ground truth; future agents mine the vault
instead of the live dirs.** The vault NEVER deletes source files; wiping
is always the human's act, informed by `vault status`.

## Current repo state (main after PR 15)

- `internal/config` (TOML, precedence flag > env > config > default,
  `config show` with sources). `xdg.DataDir()`. SQLite backups.
- `internal/discover` (roots + file enumeration), `internal/store`
  (SQLite + meta + tx helpers), full CLI/TUI/skill.
- One new dep sanctioned HERE: `github.com/klauspost/compress` (zstd,
  pure Go — CGO stays off).

## Design

### Storage layout

- Vault root: `[vault] dir` config; default `<data-dir>/vault`.
- Archives: `<vault>/<provider>/<YYYY-MM>.tar.zst` — one tar.zst bundle
  per provider per month (month of the source file's mtime). Bundles are
  append-managed: adding files to a month rewrites that bundle
  atomically (temp + rename); zstd long-range matching ON (the repeated
  instruction blobs in codex transcripts are why 40 GB should crush to a
  few GB).
- Tar member name = source path made relative to the provider root
  (e.g. `sessions/2026/06/13/rollout-….jsonl`), preserving layout.
- Format-versioned: a `vault.json` marker at the vault root with
  `{"vault_format": 1, "encryption": "none"}` — encryption is designed
  for later; refuse to operate on an unknown format/encryption value.

### Manifest (tables in the existing SQLite — backups protect it)

```sql
CREATE TABLE vault_files (
  source_path TEXT NOT NULL,     -- absolute path at archive time
  provider TEXT NOT NULL,
  rel_path TEXT NOT NULL,        -- tar member name
  archive TEXT NOT NULL,         -- e.g. codex/2026-06.tar.zst
  content_sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  mtime_unix INTEGER NOT NULL,
  first_ts TEXT, last_ts TEXT,   -- min/max event timestamps (cheap scan)
  line_count INTEGER,
  vaulted_at INTEGER NOT NULL,
  version INTEGER NOT NULL,      -- bumps when a changed file re-vaults
  PRIMARY KEY (source_path, version)
);
```

`first_ts`/`last_ts`/`line_count` come from the single pass you are
already making to compress — no second read. A source file whose content
hash changed since its last vaulting re-vaults as `version+1` (resumed
sessions); `cat`/`verify` default to the latest version.

### Commands

- `vault archive [--all]` — vault SETTLED files: mtime older than
  `[vault] min_age` (default `"168h"`), not already vaulted at current
  content. `--all` ignores min_age. Prints per-provider counts, bytes
  in, bytes stored, dedupe/skip counts. Also runs opportunistically
  after sync when `[vault] auto = true` — BUT auto-mode wiring is PR 17;
  in this PR the command is manual-only.
- `vault verify [--deep]` — default: every manifest entry's archive
  exists and tar member is present with the recorded size; `--deep`:
  decompress and recompute sha256. Nonzero exit on any failure, loud
  per-file detail.
- `vault list [--provider] [--since] [--until]` — manifest table view:
  source path, month archive, size, first/last timestamps, version,
  and whether the original still exists on disk.
- `vault cat <source-path | rel-path>` — stream the ORIGINAL bytes to
  stdout (latest version; `--version N` selects). This is the mining
  primitive: `tokenomnom vault cat …/session.jsonl | jq …`.
- `vault status` — totals: files, raw bytes, stored bytes, ratio,
  per-provider breakdown, and **reclaimable**: bytes of on-disk
  originals whose latest version is vaulted AND verified (shallow
  verify at status time is fine). Ends with the explicit line that
  tokenomnom never deletes sources and the human may reclaim the listed
  paths.
- All support `--format json` (envelope, additive; document in
  `docs/agent-api.md` — these are mining-agent surfaces).

### Config (add the section PR 15 prepared for)

```toml
[vault]
dir = ""                # "" = <data-dir>/vault
min_age = "168h"        # settle time before a file is vault-eligible
providers = ["codex", "claude"]
auto = true             # read in PR 17; store+show now, do not act on it
```

### Safety rules

- Never modify or delete source files. Never overwrite a bundle
  non-atomically. A file being appended mid-archive (size/mtime changes
  under us): skip it this pass, note it in output.
- `doctor` gains a Vault section: dir, format, files, stored/raw bytes,
  last archive run (meta), reclaimable bytes (additive, pretty + json).

## Tests

- Round-trip: archive fixtures → cat returns byte-identical content;
  verify (shallow + deep) passes; corrupt a bundle byte → deep verify
  fails loudly.
- Versioning: append to a vaulted file → re-vault bumps version, cat
  returns latest, `--version 1` returns original.
- Settled filter (min_age) incl. `--all`; mid-archive-change skip.
- Reclaimable math: only verified-current originals count; deleting a
  source flips its `vault list` presence column but nothing else.
- Windows paths (rel_path separators normalized to `/` in tar).
- Config: vault section precedence + defaults; unknown provider value
  rejected.
- Race/verify as always.

## Out of scope — do NOT touch

- No auto-vault wiring into sync, no scheduler (PR 17). No encryption
  (format field only). No FTS/search index. No deletion of anything.
- Only `klauspost/compress` added. README: add a Vault subsection under
  Configuration + a short "Vault" section describing archive/verify/
  list/cat/status and the never-deletes rule (sanctioned).
- Do not modify `DESIGN.md`, `archive/`, `assets/`, `handoffs/`, CI,
  Makefile beyond nothing, adapters, syncer, pricing, skill content
  (PR 17 updates the skill).

## Acceptance criteria

- `make verify` + `go test -race ./...` green; gofmt clean; CI green on
  all three OSes.
- Reviewer gate (real machine): `vault archive` on the real logs
  completes; compression ratio reported (expect single-digit GB from
  40+); `vault cat` of a real session byte-matches the original
  (`cmp`); `vault verify --deep` passes; `vault status` reclaimable
  matches manual math on a spot-check; sync/report performance
  unaffected when not archiving.

## Process

1. Branch from `main`: `pr16-vault`.
2. Conventional commits.
3. PR title `feat: transcript vault (PR 16)` via `gh pr create`; list
   any deviations explicitly.
4. **Do not merge.** Claude reviews, Janior approves and merges.
