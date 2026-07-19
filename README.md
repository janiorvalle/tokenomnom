# tokenomnom

<p align="center">
  <img src="assets/hero.png" alt="tokenomnom - your agents nom tokens. this shows the bill." width="840">
</p>

Your coding agents nom tokens all day under a subscription, so the cost stays
invisible. `tokenomnom` reads their local logs and shows what the bill would
have been at API list prices: daily and monthly patterns, model breakdowns,
and a spend heatmap, all in the terminal.

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

## Agents

`--format json` is the stable machine interface. It returns one
`tokenomnom.report/v1` envelope; the complete contract is in
[docs/agent-api.md](docs/agent-api.md).

tokenomnom also ships an opt-in skill that teaches Codex and Claude Code which
commands answer common token and spend questions:

```sh
tokenomnom install-skill
tokenomnom install-skill --remove
```

The dashboard offers this skill once on first run; `install-skill` and
`install-skill --remove` remain available anytime as the manual path.

The installer only writes under existing agent roots. It refuses to overwrite
a foreign `SKILL.md` unless you pass `--force`.

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

The SQLite store lives at `~/.local/state/tokenomnom/usage.db` on macOS and
Linux, or `%LOCALAPPDATA%\tokenomnom\usage.db` on Windows. Use
`TOKENOMNOM_STATE_DIR` to replace that directory. `XDG_STATE_HOME` is also
honored on Unix.

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
