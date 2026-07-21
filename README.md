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
reclaim the disk and still hand your agents years of raw history to mine:
how you prompt, which iterations worked, what patterns are worth turning
into skills. The bundled agent skill teaches them to do exactly that.

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

Build the local human-prompt history index explicitly and inspect its health:

```sh
tokenomnom history index
tokenomnom history search "do not implement" --since 2026-07-01
tokenomnom history show prm_123
tokenomnom history prompts --limit 100
tokenomnom history stats --group-by provider
tokenomnom history status
```

Indexing resumes growing transcripts, detects rewrites and missing sources,
and includes Codex live/archive files, Claude Code project files, and every
verified vault version by default. `history list` returns one stable logical
session row with provider/vault availability and preserved-version counts.
Search is a literal adjacent-token phrase by default; `--fts-query` explicitly
enables raw FTS5 syntax. Results are bounded snippets unless `--include-text` or
`history show` is requested, and raw retrieval revalidates exact indexed bytes.
Repository/branch filters are complete for Codex but partial for Claude Code;
use `--cwd` for cross-provider completeness and read JSON coverage warnings.
Indexing is never run implicitly by usage reports or normal syncs. `history.db` is derived
plaintext local data; `tokenomnom history purge` removes it without touching
`usage.db`, provider transcripts, vault bundles, or config.

## Agents

`--format json` is the stable machine interface. It returns one
`tokenomnom.report/v1` envelope; the complete contract is in
[docs/agent-api.md](docs/agent-api.md).

tokenomnom also ships an opt-in skill that teaches Codex and Claude Code which
commands answer common token and spend questions, and how to mine vaulted
session history for workflow analysis:

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

Each tick runs one quiet `sync --scheduled`, then performs a due database
backup and a due settled-transcript auto-vault pass. tokenomnom uses launchd
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
