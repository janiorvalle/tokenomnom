# tokenomnom

<p align="center">
  <img src="assets/hero.png" alt="tokenomnom - your agents nom tokens. this shows the bill." width="840">
</p>

Your coding agents nom tokens all day under a subscription, so the cost stays
invisible. `tokenomnom` reads their local logs and shows what the bill would
have been at API list prices: daily and monthly patterns, model breakdowns,
and a spend heatmap, all in the terminal.

Those same logs are also the full record of how you and your agents actually
work — and they don't survive on their own. Claude Code deletes transcripts
after about 30 days, and a busy Codex directory grows by tens of GB until you
wipe it. Either way the history is gone. tokenomnom's vault compresses every
session roughly 9x into a local, verified, byte-exact archive, so you can
reclaim the disk without losing anything.

On top of that archive sits a local history engine: index your sessions once,
then search years of your own prompts by exact phrase, list and filter
sessions, separate your work from delegated agent work, and pull
representative samples — all offline, all bounded. It's how you (or your
agents) answer "what did I work on in March" or "find where I said do not
implement" without grepping raw transcript directories. The bundled agent
skill teaches Codex and Claude Code to do exactly that.

Those dollar figures are API list-price equivalents. They are not your actual
Codex or Claude subscription bill.

## Install

macOS or Linux - the installer verifies checksums, writes to `~/.local/bin`,
and never uses sudo:

```sh
curl -fsSL https://raw.githubusercontent.com/janiorvalle/tokenomnom/main/install.sh | sh
```

Windows - download the zip from
[Releases](https://github.com/janiorvalle/tokenomnom/releases) and put both
executables on your `PATH`.

Go users:

```sh
go install github.com/janiorvalle/tokenomnom/cmd/tokenomnom@latest
go install github.com/janiorvalle/tokenomnom/cmd/nomnom@latest
```

Release archives ship both `tokenomnom` and its shorter `nomnom` alias. Check
either one with `tokenomnom --version` or `nomnom --version`.

## Use It

Run the dashboard:

```sh
tokenomnom
```

Get the overall picture:

```sh
$ tokenomnom summary
Active days: 116
Total: $134,655.61
```

See the last 30 active days or compare models:

```sh
tokenomnom daily --last 30
tokenomnom models
```

The calendar makes the expensive streaks obvious:

```text
$ tokenomnom heatmap --year 2026
    Jan  Feb Mar Apr May  Jun Jul Aug  Sep Oct  Nov Dec
     ·····▒▓▓▓▒░·····▒▒·▒▓░··████························
Mon  ······▒▓▓▒······░·▒▒█░▒▓██▓█························
Less ·░▒▓█ More
116 active days · total cost $134,655.61 · busiest 2026-07-13 · $28,474.65
```

Export one row per date, provider, and model:

```sh
tokenomnom export --out usage.csv
```

Every report accepts provider, model, and date filters. `--no-sync` uses the
stored data immediately when you are making several queries in a row.

Search your own session history — see [History](#history) for the full story:

```sh
tokenomnom history index
tokenomnom history search "do not implement" --since 2026-07-01
```

## History

Your transcripts are a searchable record of how you work. Build the index
once, explicitly, then query it:

```sh
tokenomnom history index
tokenomnom history search "do not implement" --since 2026-07-01
tokenomnom history search "delegated task" --thread-kind subagent
tokenomnom history search "worker status" --prompt-kind control
tokenomnom history search "proposed approach" --role assistant
tokenomnom history list --root-only
tokenomnom history show prm_123
tokenomnom history prompts --limit 100
tokenomnom history stats --group-by provider --top 20
tokenomnom history sample --group-by month,repo --count 25 --min-length 40 --one-per-session
tokenomnom history status
tokenomnom history purge
```

Indexing covers Codex live and archived files, Claude Code project files, and
every verified vault version by default. It resumes growing transcripts and
detects rewrites and missing sources, so live and vaulted copies of the same
session show up as one logical session with availability and version counts
in `history list`.

`history index --format json` groups routine record exclusions under
`data.exclusion_counts`; it does not emit one warning per excluded record.
Add `--verbose` only when you need the bounded path-and-line details in
`data.warnings`. Source and integrity failures remain individually listed in
`data.errors` in either mode.

Search is a literal adjacent-token phrase by default; `--fts-query` explicitly
enables raw FTS5 syntax. Results are bounded snippets unless you ask for
`--include-text` or `history show`, and raw retrieval revalidates the exact
indexed bytes before returning them.

Complete, versioned provider envelopes classify user-role records as `human`,
`delegation`, `agent_message`, `command`, `control`, or `unknown`. Human prompts
remain the default corpus. Use `--prompt-kind` for an explicit comma-separated
selection or `--exclude-control` when combining kinds. Search, prompt, and
sample JSON use compact provenance by default: exact occurrence counts plus a
preferred location. `--all-occurrences` opts into bounded occurrence arrays.

A few honesty rules are built in. Repository and branch filters are complete
for Codex but partial for Claude Code — use `--cwd` when you need
cross-provider completeness, and read the JSON coverage warnings. Root versus
subagent classification comes from direct provider evidence or versioned
deterministic rules; when the evidence is missing, sessions stay explicitly
`unknown` instead of being guessed. For Codex 0.93.0 and newer, the legacy
`session_meta.source` values `cli`, `vscode`, `exec`, and `mcp` are also
versioned root evidence because those producers serialize delegated sessions
with a distinct subagent source shape.

`history status` and doctor perform a metadata-only freshness check. They
compare current provider file sizes and modification times with the stored
checkpoints and report changed and new source counts, the newest change, and
the probe time. A ready index can therefore say `ready (N sources changed
since last index)` without reading transcript content or updating the index.

`history sample` pulls a representative, deterministic sample — same seed,
same corpus, same sample. It walks indexed SHA-256 keys instead of sorting
the corpus randomly, defaults to 25 logical prompts, caps at 100, and
stratifies when you pass `--group-by month,repo,thread-kind`.
`--min-length` filters by cleaned Unicode characters and `--one-per-session`
prevents one long conversation from dominating a prompt sample.

On privacy: indexing never runs implicitly from usage reports or normal
syncs, and user prompts are the only corpus by default. Setting
`history.index_assistant = true` is explicit consent to store assistant text
too — expect it to multiply plaintext storage, since assistant output dwarfs
user prompts. Disabling it prunes assistant rows on the next index run.
`history.db` can be more sensitive than `usage.db`, so it's excluded from
automatic backups; protect the state directory as you would the transcripts
themselves. `history purge` removes all indexed plaintext without touching
`usage.db`, provider transcripts, vault bundles, or config.

## Agents

`--format json` is the stable machine interface. It returns one
`tokenomnom.report/v1` envelope; the complete contract is in
[docs/agent-api.md](docs/agent-api.md).

tokenomnom also ships an opt-in skill that teaches Codex and Claude Code which
commands answer common token and spend questions, and how to search indexed
session history for workflow analysis — check readiness with `doctor`, index
deliberately, search or sample with bounded filters, and retrieve only
selected evidence:

```sh
tokenomnom install-skill
tokenomnom install-skill --remove
```

The dashboard offers this skill once on first run; `install-skill` and
`install-skill --remove` remain available anytime as the manual path.

The installer only writes under existing agent roots. It refuses to overwrite
a foreign `SKILL.md` unless you pass `--force`.

## Keep It Fresh

Install a per-user maintenance schedule, inspect it, or remove it:

```sh
tokenomnom schedule install
tokenomnom schedule status
tokenomnom schedule uninstall
```

Each tick runs one quiet `sync --scheduled`. Maintenance order is usage sync,
due database backup, due settled-transcript auto-vault, then due history
indexing when explicitly enabled. History failures produce one warning but do
not discard successful usage, backup, or vault work. tokenomnom uses launchd
on macOS, a systemd user timer on Linux, and Windows Task Scheduler on Windows.
There is no daemon, watcher, or resident tokenomnom process.

The installed unit embeds the current absolute binary path and
`schedule.interval`. Re-run `schedule install` after moving or upgrading the
binary, or after changing that interval. Other config is read fresh by every
tick.

## Configuration

User config lives at `~/.config/tokenomnom/config.toml` on macOS and Linux,
or `%APPDATA%\tokenomnom\config.toml` on Windows. `XDG_CONFIG_HOME` and
`TOKENOMNOM_CONFIG_DIR` are honored. Precedence is command-line flag >
environment variable > config file > built-in default. `tokenomnom config
path` prints the path; `tokenomnom config show` prints the effective values
and where each one came from.

Every supported key and its default:

```toml
[discovery]
codex_dir = ""
claude_dir = ""

[sync]
timezone = ""

[reports]
color = "auto"
charts = true
daily_last = 30
default_provider = ""

[backup]
enabled = true
interval = "24h"
dir = ""
keep = 14

[vault]
dir = ""
min_age = "168h"
providers = ["codex", "claude"]
auto = true
auto_interval = "24h"

[history]
auto_index = false
index_assistant = false
auto_interval = "24h"
providers = ["codex", "claude"]

[schedule]
interval = "24h"
```

An empty discovery directory uses automatic detection. The existing
`TOKENOMNOM_CODEX_DIR`, `TOKENOMNOM_CLAUDE_DIR`, `CODEX_HOME`, and
`CLAUDE_CONFIG_DIR` environment variables still work. Reports use the system
timezone when `sync.timezone` is empty; otherwise it must be an IANA name such
as `America/New_York`. Changing the stored timezone triggers a safe rebuild
from the source logs.

`reports.color` accepts `auto`, `always`, or `never`. Set `NO_COLOR` or pass
`--no-color` for plain output; `--format json` is always unstyled.
`reports.charts = false` is the config equivalent of `--no-chart`,
`reports.daily_last` supplies Daily's default `--last`, and
`reports.default_provider` may be empty, `codex`, or `claude`.
`history.index_assistant` has no environment variable or command-line override;
enable it only in config, then run `history index`.

After each successful sync, tokenomnom creates a due online SQLite backup.
The default directory is `~/.local/share/tokenomnom/backups` on macOS and
Linux, or the OS user-config data directory on Windows. `XDG_DATA_HOME` and
`TOKENOMNOM_DATA_DIR` replace that base. An empty `backup.dir` uses the
default; `backup.interval` is a Go duration; `backup.keep = 0` keeps every
backup. Backup failures warn but never block a report.

An empty `vault.dir` uses `<data-dir>/vault`. `vault.min_age` is the settle
time before a transcript is eligible for archiving, and `vault.providers`
selects `codex`, `claude`, or both. When `vault.auto` is true, successful syncs
run a settled-file archive pass at most once per `vault.auto_interval`.
Failures warn and retry on a later tick; source transcripts are never deleted.

`history.auto_index` is explicit consent for scheduled plaintext indexing and
defaults to `false`. When enabled, only `sync --scheduled` runs a due index pass
at most once per `history.auto_interval`; ordinary reports and ordinary `sync`
never do. `history.providers` selects `codex`, `claude`, or both.

`schedule.interval` controls the installed OS schedule and defaults to 24
hours. It must be a whole-second Go duration. Changing it requires another
`schedule install`; `schedule status`
flags drift between the config and installed unit. Windows Task Scheduler
supports intervals from 1 minute through 31 days.

The SQLite store lives at `~/.local/state/tokenomnom/usage.db` on macOS and
Linux, or `%LOCALAPPDATA%\tokenomnom\usage.db` on Windows. Use
`TOKENOMNOM_STATE_DIR` to replace that directory. `XDG_STATE_HOME` is also
honored on Unix. The explicit transcript index uses `history.db` beside it and
is deliberately excluded from automatic usage-database backups.

Pricing overrides live at `~/.config/tokenomnom/pricing.json`, or under
`TOKENOMNOM_CONFIG_DIR`. `XDG_CONFIG_HOME` is honored on Unix. An override
replaces the complete entry list for each model it names:

```json
{
  "my-model": [
    {
      "base_input": 2.5,
      "cache_read": 0.25,
      "output": 10,
      "status": "estimated",
      "source": "https://example.com/pricing"
    }
  ]
}
```

Rates are USD per million tokens. Keep secrets out of this file; pricing
overrides are data and need no credentials.

`--provider`, `--model`, `--since`, and `--until` narrow reports; `--year`
selects a heatmap calendar year, and `--no-sync` reports stored data without a
refresh. Flags remain authoritative when they are set.

Attribution and dedupe rules, day-bucketing semantics, the store schema, the
JSON contract, pricing math and cost formulas, and disclaimer text are
deliberately not configurable. Those are correctness contracts, not display
preferences.

The standalone installer supports
`TOKENOMNOM_INSTALL_REPO`, `TOKENOMNOM_INSTALL_DIR`,
`TOKENOMNOM_INSTALL_BASE_URL`, `TOKENOMNOM_INSTALL_VERSION`, and
`TOKENOMNOM_INSTALL_ARCHIVE` for mirrors and local verification.

## Vault

Coding agents throw their own history away. The vault is where it survives:
every archived source is preserved byte-for-byte for later inspection. The
vault manifest contains provider, source path, archive, version, size, and
timestamp metadata; it is not yet project or session search. Inspect a bounded
page of the manifest, then read one archived source with `vault cat`:

```sh
tokenomnom vault list --provider codex --since 2026-06-01 --limit 100 --latest --format json
tokenomnom vault cat ~/.codex/sessions/2026/06/13/rollout-….jsonl | jq .
```

Follow `data.page.next_cursor` with the same filters when `has_more` is true.
JSON `vault cat` returns readable `content` for UTF-8 transcripts and always
retains the compatible `content_base64` form. The installed agent skill uses
this bounded flow and explains the temporary live-directory fallback for
recent transcripts that have not settled or been archived yet.

The transcript vault stores byte-for-byte source content in monthly,
provider-specific `.tar.zst` bundles while keeping a versioned manifest in the
usage database. Run `tokenomnom vault archive` for settled files or add `--all`
to ignore the settle age and recheck source hashes. `vault verify [--deep]`
checks bundles, `vault list` shows the manifest,
`vault cat <source-path | rel-path>` restores original
bytes to stdout, and `vault status` reports compression and reclaimable
originals.

`doctor` reports usage sync, archive, deep-verification, and status-scan times
as separate facts, along with vaulted, settled-unvaulted, recent-unsettled, and
known-broken counts. A missing synced source keeps its usage totals; raw access
then depends on whether the source was vaulted.

tokenomnom never modifies or deletes source transcripts. Reclaiming a verified
original listed by `vault status` is always a manual decision.

## How It Counts

tokenomnom reads local JSONL session logs. Nothing leaves the machine. Codex
cumulative counters are converted to deltas, rewrites and moved archives are
reconciled, and Claude's progressive message snapshots are deduplicated across
files before daily totals are stored. Ambiguous cache writes and unknown models
stay explicit instead of being guessed.

The local store preserves already-ingested history when an agent deletes or
archives a source file. See [DESIGN.md](DESIGN.md) for the detailed attribution,
pricing, and retention rules.

## Development

[CONTRIBUTING.md](CONTRIBUTING.md) has setup, test policy, and the provider
adapter guide. [SECURITY.md](SECURITY.md) has the disclosure process and local
trust model.

## License

MIT. See [LICENSE](LICENSE).
