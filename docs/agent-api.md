# Agent API

`tokenomnom` exposes a stable machine-readable contract for coding agents. Use
`--format json` with `summary`, `daily`, `monthly`, `models`, `heatmap`,
`pricing`, `doctor`, `sync`, `export`, `install-skill`, `config show`, every
`vault` subcommand, every `history` subcommand, and every `schedule` subcommand. The `export` command defaults to CSV;
all other commands default to the human-readable `pretty` format.

## Compatibility

Every JSON response uses schema `tokenomnom.report/v1`. New fields may be added
without changing that version. Renaming or removing a field, changing its type,
or changing its meaning requires a new schema version and is a semver event.

Token counts are JSON integers. `cost_usd` and pricing rates are JSON numbers.
Costs are rounded to cents; agents that need exact arithmetic should use the
token counts and the rates returned by `pricing`.

## Envelope

Every JSON response is one object with these fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `schema` | string | Always `tokenomnom.report/v1`. |
| `command` | string | The command that produced the response. |
| `generated_at` | string | RFC 3339 timestamp in UTC. |
| `timezone` | string | Timezone used to bucket usage dates. This is an IANA name when the operating system exposes one; otherwise `Local` means the process system timezone. |
| `filters` | object | `provider`, `model`, `since`, and `until`; each is a string or `null`. |
| `disclaimer` | string | API list-price-equivalent disclaimer. |
| `warnings` | string[] | Human-readable pricing, classification, or freshness warnings. |
| `data` | object | Command-specific data described below. |

Warnings are also represented by structured counters in `data`. Empty results
return the full envelope, an empty `rows`/`providers`/`models` array, and zeroed
totals. Errors are written to stderr and exit nonzero; successful JSON output
contains nothing before or after the envelope.

## Summary

`tokenomnom summary --format json`

`data` contains `date_range` (`first_date` and `last_date`, each nullable),
`active_days`, `totals`, `providers`, `top_models`, `unpriced_tokens`,
`unclassified_cache_write_tokens`, and `unknown_model_tokens`.

`totals` contains `input_tokens`, `cache_read_tokens`, `cache_write_tokens`,
`output_tokens`, `total_tokens`, and `cost_usd`. Each `providers` item adds
`provider` to that same shape. Each `top_models` item contains `provider`,
`model`, `total_tokens`, and `cost_usd`. Providers sort by provider ID; top
models sort by total tokens descending, then provider and model.

## Daily And Monthly

`tokenomnom daily --format json` returns `data.rows` ordered by ascending
`date`. `tokenomnom monthly --format json` returns rows ordered by ascending
`month`. Each row has the period field plus `input_tokens`,
`cache_read_tokens`, `cache_write_tokens`, `output_tokens`, `total_tokens`, and
`cost_usd`.

Both data objects also contain `unpriced_tokens`,
`unclassified_cache_write_tokens`, and `unknown_model_tokens`. `daily` defaults
to the most recent 30 active dates; use `--last N` or an explicit date range.

## Models

`tokenomnom models --format json`

`data.rows` is ordered by `total_tokens` descending, then provider and model.
Each row contains `provider`, `model`, all five combined token fields,
`share`, `active_days`, `first_date`, `last_date`, `cost_usd`, `cost_share`, and
`priced`. Shares are percentages between 0 and 100. `cost_share` is `null` when no
tokens in the row were priced. The data object also has the three diagnostic
token counters used by daily and monthly.

## Heatmap

`tokenomnom heatmap [--year YYYY] --format json`

`data.window` contains the inclusive `from` and `to` dates. Without `--year`,
the window is the trailing 12 months ending today; `--year` selects that full
calendar year. `data.metric` is `cost_usd`, or `tokens` when every usage row in
the window is unpriced. `data.days` contains one item per calendar date, ordered
oldest first, with `date`, `cost_usd`, `total_tokens`, and contribution `level`
from 0 through 4.

`data.stats` contains `active_days`, `total_cost_usd`, `busiest_day` (`date`,
`cost_usd`, and `total_tokens`), and `longest_streak`. Active days and streaks
use the selected nonzero metric and are bounded by `data.window`.

## Pricing

`tokenomnom pricing --format json`

`data.models` is ordered by model name. Each item contains `model` and an
`entries` array ordered by effective start. Entries contain the nullable
USD-per-million-token rates `base_input`, `cache_read`, `write_5m`, `write_1h`,
and `output`, plus `status`, nullable `effective_from`, nullable
`effective_until`, `source`, `notes`, and `overridden`.

## Doctor

`tokenomnom doctor --format json`

`data.providers` contains `provider`, `path`, `source`, `exists`,
`jsonl_files`, `total_bytes`, nullable `oldest`, nullable `newest`, and
`walk_errors`. `data.store` contains `path`, `exists`, `size_bytes`, nullable
`schema_version`, nullable `timezone`, nullable `last_sync`, `usage_rows`,
`distinct_models`, `date_range`, and `missing_files`.

`data.store.missing_files` counts synced transcript files whose source file is
no longer present. It is a usage-store denominator, not a history-index count.

`data.skills` contains one item per provider with `provider`, skill `path`,
`status`, nullable installed `version`, `current_version`, and
`update_available`. An outdated owned skill adds a warning that points to
`tokenomnom install-skill`; foreign files are never replaced by doctor.

`data.vault` contains `dir`, `initialized`, nullable `format`, nullable
`encryption`, `files`, `raw_bytes`, `stored_bytes`, nullable `last_archive`,
`reclaimable_bytes`, and nullable `reclaimable_cached_at`. It also separates
`last_usage_sync`, `last_deep_verification`, and `last_status_scan`, and reports
`vaulted_sources`, `settled_unvaulted_sources`, `recent_unsettled_sources`,
`known_broken_bundles`, `auto_vault_enabled`, `scheduler_installed`, and
`scheduler_current`. `reclaimable_cached_at` is the last status scan, not the
last explicit deep verification. Routine doctor output does not hash the
transcript corpus; run `vault status` to refresh reclaimability.

When `data.store.missing_files` is nonzero, doctor warns that retained usage is
unchanged and raw transcript availability depends on vault coverage. A missing
optional provider root alone does not produce that warning.

`data.schedule` contains `installed`, `definition_exists`, `mechanism`, `unit_path`, optional `task_name`, `binary_path`,
`binary_exists`, `configured_interval`, nullable `installed_interval`,
`interval_drift`, and nullable `last_sync`, `last_backup`, and
`last_auto_vault` timestamps.

`data.history` reports the rebuildable transcript index without creating it.
It contains `status`, `database_path`, `database_size_bytes`, schema and
extractor versions, logical session/source-head/prompt/occurrence counts,
`user_logical_prompts`, `searchable_user_prompts`, assistant prompt counts,
and per-role coverage bounds,
live, provider-archive, preserved-snapshot, and vault-location counts;
provider-live-only, provider-archive-only, vault-only, exact-live-and-vaulted,
and unavailable-metadata coverage; indexed vault bundle/version counts;
broken/skipped bundle counts; stale/error/missing counts; nullable coverage and
last-index/attempt/complete-success timestamps, `next_due`, and
`index_generation`. `sampling_ready` is false after an upgrade that still needs
the explicit corpus-sized sampling backfill; `history index` performs it while
database open remains bounded. It also reports `auto_index_enabled`,
`index_assistant_enabled`, whether the stored corpus is `assistant_indexed`,
`auto_interval`, configured `providers`, and nullable `last_error_summary`
without prompt text.
`data.history.missing_sources` counts indexed source heads whose file is gone;
it is intentionally different from `data.store.missing_files`.
`last_run_error_count` makes an incomplete most-recent run explicit.
`inspection_error` is nullable and lets doctor report a corrupt optional index
without aborting its other diagnostics.

`user_logical_prompts` counts every user-role logical record, including
classified provider metadata or agent-instruction records that are retained
but not searchable. `searchable_user_prompts` counts the human user corpus used
by default search, prompt enumeration, sampling, and stats. The fields are
intentionally separate and existing count fields keep their original meaning.

## History Index

`tokenomnom history index [--provider codex|claude] [--source all|provider|vault]
[--full] [--verbose] --format json`

The first explicit index creates `history.db`. `--source all` is the default and
combines Codex `sessions/`, Codex `archived_sessions/`, Claude Code `projects/`,
and every selected validated vault manifest version. `provider` and `vault`
narrow that scope. `--full` rebuilds the selected source kinds. Indexing is not
triggered by usage reports or ordinary syncs.

`history.index_assistant` defaults to false and is the only consent switch; it
has no environment or flag override. Enabling it marks existing sources stale,
and the next explicit or scheduled index adds assistant text blocks. Disabling
it prunes assistant prompt and FTS rows on that index run. Assistant text often
dwarfs user text and can multiply plaintext storage. `history purge` removes
all indexed roles. Indexing remains local-only with no model calls or embeddings.
A narrowed or failed rebuild remains `assistant_indexed=false`; assistant
queries keep returning the not-indexed warning until a complete `history index`
run succeeds across the selected providers and their provider/vault sources.
Scheduled completion follows the configured `history.providers` scope; role
coverage warnings disclose materially different or missing provider coverage.

Only mutating history operations take `history.db.lock`. Search, list, show,
prompts, stats, sample, status, and doctor use SQLite read-only URI connections
with foreign keys enabled and a five-second busy timeout. They do not create or
migrate the database, change its permissions, or create a process lock; WAL
provides visibility while an index run is active. A writer lock records its PID
and process start time. The advisory lock file is persistent for compatibility
with older binaries; the next writer replaces an orphaned ownership record left
by a dead or PID-reused owner.

Vault traversal holds the vault lock, then the history lock, then one SQLite
transaction per bundle. Each archive is decompressed once and every yielded
member is matched by path, size, and SHA-256. A bad member rolls back that
bundle, independent bundles continue, and the command exits nonzero without
advancing `last_complete_success`. Retrying an unchanged successful bundle is
idempotent.

`data` contains aggregate `scanned_sources`, `indexed_sources`, `new_sources`,
`skipped_sources`, `appended_sources`, `rewritten_sources`, `missing_sources`,
`indexed_prompts`, `oversized_prompts`, `reclassified_prompts`,
`prompt_kind_counts`, `error_count`, bounded `errors`, `full`, and
`duration_ms`. Routine record exclusions are grouped by classification and
reason in `exclusion_counts`; each entry contains `classification`, `reason`,
and `count`, without prompt text. `warnings` is `[]` by default. `--verbose`
restores bounded per-record path-and-line details there, while source and
integrity failures remain individually visible in `errors` in either mode.
Here `missing_sources` is the number of indexed source heads whose file was
found gone during the index run.
Vault fields include selected,
traversed, indexed, skipped, and failed bundle/version counts. Independent source failures do not roll
back successful sources, but the command exits nonzero and does not update the
complete-success timestamp.

## History List

`tokenomnom history list [--provider codex|claude] [--since YYYY-MM-DD]
[--until YYYY-MM-DD] [--cwd PATH] [--repo NAME] [--project NAME] [--branch NAME]
[--source any|provider|provider-live|provider-archive|vault] [--limit N]
[--cursor OPAQUE] [--root-only | --thread-kind root|subagent|unknown|all]
--format json`

The default page contains at most 100 current logical sessions; the maximum is
500. Results use descending activity time and stable session-ID tie-breaking.
`data.sessions` contains stable `ses_` IDs, provider/native IDs, first/last
timestamps, cwd/repository/branch metadata, derived `project` and its
`project_source` (`git`, `cwd`, or `unknown`), `src_` and `snap_` IDs and counts,
logical prompt and occurrence counts, availability components, preferred exact
retrieval source, explicit thread classification and evidence, relationship
details, and a byte/line-bounded first-human-prompt preview. Relationship
objects contain nullable resolved parent and child session IDs, provider-native
parent/message values, evidence, confidence, rule version, and resolution
state. Relationship arrays are capped and disclose `relationships_truncated`.
Exact
provider and vault copies remain one logical prompt with multiple occurrences.

`data.coverage` reports known and unknown repository and branch counts plus a
project breakdown by `git`, `cwd`, and `unknown` source. Claude
Code repository and branch values remain unknown; `--repo` or `--branch`
excludes those sessions and adds an envelope warning with the excluded count.
Use `--cwd` when cross-provider completeness matters. Page cursors are opaque,
filter-bound, and rejected after `index_generation` changes.
`root` requires direct evidence or a versioned deterministic provider rule;
absence of an observed parent remains `unknown`. `--root-only` is shorthand
for `--thread-kind root`, and combining it with an explicit `--thread-kind` is
an error. The default and `all` include root, subagent, and unknown sessions.

Codex repository identity is derived from its stored repository URL by a
versioned rule that removes scheme, credentials, a trailing `.git`, and trailing
slashes while preserving case. Repository name is the final path segment; it is
never inferred from cwd.
`project` is separate: it uses the repository name when proven, otherwise the
final path segment of a known non-temporary cwd, otherwise `unknown`. Cwds
under the runtime OS temp directory, `/private/tmp`, `/tmp`, `/var/folders`,
or standard Windows user/system temp roots remain unknown. `--project` is the
cross-provider name filter; `--repo` remains strictly git-proven.

## History Search

`tokenomnom history search <query> [--provider codex|claude] [--since
YYYY-MM-DD] [--until YYYY-MM-DD] [--cwd PATH] [--repo NAME] [--project NAME] [--branch NAME]
[--source any|provider|provider-live|provider-archive|vault] [--limit N]
[--cursor OPAQUE] [--include-text] [--fts-query] [--role user|assistant|any]
[--prompt-kind human|delegation|agent_message|command|control|unknown[,KIND...]]
[--exclude-control] [--all-occurrences]
[--root-only | --thread-kind root|subagent|unknown|all] --format json`

The default limit is 50 and the maximum is 500. Default search quotes the
input as one FTS5 `unicode61` phrase: tokenizer terms must be adjacent and in
order, punctuation separates terms, and words such as `OR` remain literal.
`--fts-query` is the only raw boolean, NEAR, or prefix syntax route.
The role default is `user`. `assistant` searches only explicitly indexed
assistant text blocks and `any` combines both roles. When assistant indexing is
off or has not yet run, assistant/any queries return an explicit envelope
warning; assistant hits remain empty rather than implying the agent never said
something.

`data.hits` is always an array. Each logical-prompt hit contains `prompt_id`,
`session_id`, `role`, `prompt_kind`, provider and session metadata, nullable timestamp/repository/
branch, raw FTS5 `rank` with `rank_direction: "lower_is_better"`, a bounded
highlighted `snippet`, exact occurrence counts, availability, preferred
retrieval source, and `preferred_location`. Stable source/snapshot ID and
bounded `occurrences` arrays are included only with `--all-occurrences`. `text` is present
only with `--include-text`. Rank is not a normalized confidence score.
Every hit also includes derived `project` and its `project_source` label.
`data.page` contains `limit`, `has_more`, and `next_cursor`. Search cursors bind
the exact query, literal/raw mode, filters, rank bits, stable tie-breakers, and
index generation.

`data.coverage` contains nullable first/last indexed prompt timestamps plus
known/unknown repository and branch session counts, project counts by source,
plus root, subagent, and
unknown relationship counts. `coverage.roles` reports independent user and
assistant prompt counts/date bounds plus `assistant_indexed` and the completed
`assistant_providers` scope; materially
different coverage under `--role any` adds a warning. Requests outside date
coverage and repository/branch filters that exclude unknown metadata add
envelope warnings.

## History Show And Prompts

`tokenomnom history show <prompt-id> --format json` returns one clean indexed
prompt with full `text` and metadata. `history show <session-id> --format json`
returns bounded session metadata. Adding `--prompts [--limit N] [--cursor
OPAQUE]` returns `data.prompts` with full clean prompt text and `data.page`.

`history show <session-id> --raw [--snapshot snap_ID] --format json` accepts
only stored IDs. It returns the selected indexed location, `encoding`, nullable
UTF-8 `content`, and always-populated `content_base64`. Provider bytes are
size/hash revalidated; a file changed since indexing is rejected or skipped in
favor of another exact location. Missing or broken vault content never returns
success. Pretty raw mode writes only exact bytes to stdout and warnings to
stderr.
Raw JSON is capped at 64 MiB to bound encoding memory; omit `--format json` to
stream larger exact transcripts directly to stdout.

`tokenomnom history prompts` accepts the shared search filters plus
`--role user|assistant|any`, `--include-text`, `--all-occurrences`, `--limit`,
and `--cursor`. It defaults to 100 deduplicated clean user prompts and bounded
snippets. `--all-occurrences`
adds at most 20 provenance objects per logical prompt; total occurrence counts
remain exact and truncation is explicit. Its `data.page`, `coverage`, cursor,
warning, and optional-text contracts match search.

The default user corpus is `prompt_kind: "human"`. Complete, versioned provider
envelopes may instead be `delegation`, `agent_message`, `command`, `control`,
or `unknown`. `--prompt-kind` accepts a comma-separated explicit selection;
`--exclude-control` composes with it. Prompt-kind and control filters are bound
into cursors.

## History Sample

`tokenomnom history sample [--unit prompt|session] [--strategy
random|stratified] [--group-by month,project,cwd,repo,thread-kind] [--count N] [--seed
STRING] [shared filters] [--include-text] --format json`

Prompt samples also accept `--min-length N`, `--one-per-session`, and
`--all-occurrences`. Minimum length uses cleaned Unicode characters. These
filters compose with the seed, grouping, and shared prompt filters without
changing deterministic order.
Session samples reject prompt-kind, minimum-length, one-per-session, control,
and occurrence-expansion flags rather than silently ignoring them.

The default unit is `prompt`, the default count is 25, and the maximum is 100.
Without grouping, the default strategy is deterministic random sampling; with
grouping it is stratified. The omitted seed is the constant `tokenomnom`.
Logical prompts and sessions are sampled once regardless of how many live or
vault occurrences they have. Default items contain metadata and bounded
snippets; complete clean text requires `--include-text`.
Default prompt items compact provenance to exact occurrence and availability
counts plus `preferred_location`. Compact availability omits zero-valued source
kinds; the preferred location contains its kind, opaque source/snapshot ID,
and vault version when relevant. Default snippets are capped at 64 UTF-8 bytes.
Relationship evidence, paths, offsets, and occurrence arrays are omitted.
`--all-occurrences` restores the bounded full prompt object.
Sampling deliberately remains user-only in this campaign: it has no `--role`
filter, so larger assistant messages cannot silently change existing sampling
behavior. Use assistant search/prompts plus bounded pagination instead.

Each unit has an indexed 64-bit key from SHA-256 of its stable opaque ID. The
seed hashes to a pivot, selection walks keys at or after the pivot, then wraps
once. There is no `ORDER BY random()` or whole-FTS-corpus random sort.
Stratification sorts nonempty normalized groups, gives each group one unit
while capacity remains, then distributes the remainder round-robin without
duplicates. If groups outnumber the count, the seed pivot deterministically
selects groups. Project strata include `project_source` so matching git- and
cwd-derived names remain labeled. Project labels represented by fewer than two
logical sessions in the index fold into `project: "other"` with
`project_source: "unknown"`; this changes only presentation strata, not stored
session project values. Missing month, project, repository, or thread metadata
is the explicit `unknown` group; session month uses its first known timestamp.

`data.items` is always an array. Each item has `unit` and either a `prompt` or
`session` object; `groups` is present when grouping was requested. Session samples may add `text` only with
`--include-text`. The response also includes the effective `strategy`, sorted
`group_by`, returned `count`, effective `seed`, `index_generation`, and
`coverage`. Sample coverage describes the returned logical sessions, not a
corpus-wide aggregate; use `history status` or `history stats` when full index
coverage is needed. Repository and branch filters add bounded provider-uneven
warnings without scanning excluded rows.

## History Stats

`tokenomnom history stats [shared filters] [--group-by provider|project|repo|cwd|thread-kind|weekday|hour|role] [--top N]
--format json` returns SQL-computed, text-free aggregates labeled with
`scope: "searchable_prompt_corpus"`: logical session,
source-head, snapshot, prompt, and occurrence counts; date coverage and active
days; total/median prompt byte lengths; provider-live, provider-archive, and
vault availability; index bytes; and stale/error/oversized counts.
`data.role_counts` discloses text-free user and assistant logical-prompt,
occurrence, and byte totals for the consented corpus.
`data.groups` contains dimension `values` and session/prompt/occurrence/length
aggregates. Project groups also carry `project_source`; labels represented by
fewer than two sessions fold into the visible `other` project group before
top-N truncation. Groups sort by logical prompt count and default to the top 20
(maximum 100). `groups_truncated` and the aggregate `other` object explicitly
disclose any remainder. Before top-N truncation, repository/CWD group sets
include an explicit `unknown` group; a zero-count synthetic unknown can be
folded into `other`, while observed unknown groups are retained when capacity allows.
Weekday/hour grouping remains UTC-normalized; RFC 3339 coverage timestamps use
the effective presentation timezone described below.
Coverage and warnings use the same provider-uneven metadata rules as search.
Filtered stats exclude index errors that cannot be associated with filterable
session metadata, report their count as `unscoped_errors_excluded`, and add a
warning instead of mixing unrelated failures into `error_count`.

`tokenomnom history status --format json` returns the same bounded history
health object used by doctor. An absent index returns `status: "not_indexed"`
without creating a database. Additive `status_reasons` is always an array and
lists every reason status is not ready: `not_indexed`, `error_sources`,
`last_run_errors`, `stale_sources`, `settled_drift`, and/or
`sampling_not_ready`. Missing sources are reported separately and do not count
as stale or degrade an otherwise clean index. Status and doctor
also stat the configured live provider trees and add
`changed_sources_since_index`, `new_sources_since_index`, additive
`active_changed_sources`, `active_new_sources`, `settled_changed_sources`, and
`settled_new_sources`, nullable
`newest_source_change`, and `source_drift_as_of`. The changed count includes
modified, new, and no-longer-present files under provider roots that still
exist. The probe reads no transcript content, takes no history writer
lock, and never creates or migrates `history.db`; roots that are not present
do not create false drift for vault-only history. Active means the source mtime
is within the fixed, inclusive 10-minute settle window at probe time; settled
means older and actionable. Query commands do not run this probe. Active-only
drift is informational and leaves status `ready`; settled drift reports
`degraded` with `status_reasons: ["settled_drift"]`.

`history index` additively returns `thread_kind_deltas` with signed `root`,
`subagent`, and `unknown` logical-session count changes. This makes versioned
relationship reclassification visible while stable public session and prompt
IDs survive extractor rebuilds.

History command envelopes and emitted RFC 3339 timestamps use the same
effective timezone precedence as reports: `--tz`, then `sync.timezone`, then
the system timezone. Cursor payloads and internal sort keys remain normalized
to UTC.

`tokenomnom history purge --format json` acquires the history lock and removes
only `history.db` plus its SQLite WAL/SHM files. It returns `path` and
`files_removed`. Usage data, provider transcripts, vault bundles, and config
are untouched.

## Vault

All vault JSON commands use the standard envelope. Command values are
`vault archive`, `vault verify`, `vault list`, `vault cat`, and `vault status`.

`vault archive [--all]` returns per-provider `archived`, `input_bytes`,
`stored_bytes`, `deduplicated`, `skipped`, and `changed_during_read` counts.
`--all` ignores settle age and rechecks source hashes; `stored_bytes` is the
change in on-disk bundle bytes for the archive run.
Discovery problems are also returned as envelope warnings. Successful syncs
run this settled-file pass when `vault.auto` is enabled and its
`vault.auto_interval` guard is due.

`vault verify [--deep]` returns `deep`, `checked`, `verified`, and `failures`.
Each failure identifies `source_path`, `version`, `archive`, and `error`; any
failure also produces a nonzero exit.

`vault list [--provider] [--since] [--until]` returns `data.files`. With no
pagination flags it preserves the complete, all-version manifest response.
Each row
contains the manifest fields (`source_path`, `provider`, `rel_path`, `archive`,
`content_sha256`, `size`, `mtime_unix`, optional `first_ts`/`last_ts`,
`line_count`, `vaulted_at`, and `version`) plus `original_exists`.

`--limit N` enables SQL-backed keyset pagination with a range of 1 through 500.
`--cursor OPAQUE` continues a page and reuses that page's limit unless a new
valid limit is supplied. `--latest` enables page mode and returns only the
newest version for each source. Page mode defaults to 100 rows and
`last_ts` descending; `--sort` accepts `source` ascending or `first_ts`,
`last_ts`, and `size` descending. Unknown timestamps sort after valid
timestamps. Every order uses source and version tie-breakers.

Page-mode responses add `data.page` with `limit`, `has_more`, and
`next_cursor`. Cursors are opaque and may only be reused with the same filters,
sort, and latest-version setting. Pretty page output includes provider and a
continuation command when more rows exist.

`vault cat <source-path | rel-path> [--version N]` returns the selected source
and relative paths, version, `encoding`, nullable `content`, and the byte-exact
content as the always-populated `content_base64` string. Valid UTF-8 returns
`encoding: "utf-8"` and readable `content` while retaining base64. Arbitrary
bytes return `encoding: "base64"` and `content: null`. Without JSON format it
writes the original bytes directly to stdout.

`vault status` returns vault format details, total and per-provider files,
`raw_bytes`, `stored_bytes`, `ratio`, `reclaimable_bytes`, and
`reclaimable_paths`. `never_deletes_sources` is always true and
`reclaimable_instruction` states that deleting a listed original is a manual
action.

## Install Skill

`tokenomnom install-skill --format json`

`data.providers` contains one result per provider with `provider`, `path`,
`action`, and `version`. `action` is one of `installed`, `updated`,
`up_to_date`, `skipped_no_root`, `refused_foreign`, `removed`, or
`not_installed`.

## Schedule

`tokenomnom schedule install --format json`, `schedule status --format json`,
and `schedule uninstall --format json` use command values `schedule install`,
`schedule status`, and `schedule uninstall`. Data contains `installed`,
`mechanism`, `definition_exists`, `unit_path`, optional `task_name`, `binary_path`, `binary_exists`,
`configured_interval`, nullable `installed_interval`, `interval_drift`, and
nullable `last_sync`, `last_backup`, and `last_auto_vault`. Uninstall also
returns `uninstalled: true`.

The scheduler is per-user: launchd on macOS, a systemd user timer on Linux,
and Windows Task Scheduler on Windows. It runs the installed absolute binary
as `sync --scheduled`; no daemon remains resident. Scheduled maintenance runs
usage sync, due backup, due vault archive, then due history indexing. History
indexing runs only when `history.auto_index = true`, after the usage process
lock is released. Its failure is one bounded warning and does not make an
otherwise successful scheduled usage sync exit nonzero.

## Config Show

`tokenomnom config show --format json` uses command `config`. `data.config`
contains the effective `discovery`, `sync`, `reports`, `backup`, `vault`,
`history`, and `schedule` sections. `data.sources` maps every supported dotted key to
`default`, `config`, an environment source, or `flag`; `path` is the resolved
config path and `found` says whether the file existed.

## Sync

`tokenomnom sync --format json`

`data` contains `files_scanned`, `files_skipped`, `files_appended`,
`files_rewritten`, `files_missing`, `events_applied`, `usage_rows`,
`unknown_model_tokens`, `unclassified_cache_write_tokens`, `full_reingest`, and
`duration_ms`, `scheduled`, `skipped`, optional `skip_reason`, and optional
`auto_vault`, and optional scheduled `auto_history`. Auto-vault data contains
`ran`, `archived`, per-provider archive counts, and warnings. Auto-history data
contains `ran`, provider scan/index counts, indexed prompt and vault-bundle
counts, and `error_count`. Its `warnings` array repeats the sync-specific envelope
warnings so the sync result remains self-contained. A scheduled tick that
finds the store lock held succeeds with `skipped: true` and `skip_reason:
"store in use"`.

## Export

`tokenomnom export [--format csv|json] [--out FILE]`

Export supports `--provider`, `--model`, `--since`, `--until`, and `--no-sync`.
There is one row per `(date, provider, model)`, ordered by date, provider, and
model. With `--out`, output is atomically replaced and stdout is empty.

CSV is the default and always includes this header, even for empty results:

```text
provider,date,month,year,model,input_tokens,cached_input_tokens,cache_write_5m_tokens,cache_write_1h_tokens,cache_write_unclassified_tokens,cache_write_input_tokens,uncached_input_tokens,output_tokens,reasoning_output_tokens,total_tokens,cost_usd
```

`month` is the English month name. `cache_write_input_tokens` is the sum of the
5-minute, 1-hour, and unclassified write buckets. `uncached_input_tokens` is
`input_tokens - cached_input_tokens`. `total_tokens` is input plus output. CSV
costs have two decimal places and are empty when the row is entirely unpriced.
To prevent spreadsheet formula execution, a model value beginning with `=`,
`+`, `-`, `@`, tab, carriage return, or line feed is prefixed with a single
quote in CSV output. JSON preserves the original model value.

JSON export uses the standard envelope with `command: "export"`.
`data.rows` carries fields matching the CSV columns; `cost_usd` is a number or
`null` when entirely unpriced. The data object also contains
`unpriced_tokens`, `unclassified_cache_write_tokens`, and
`unknown_model_tokens`.
