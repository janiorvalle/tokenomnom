# Contributing

Thanks for pitching in. Here's what you need to get going.

## Development Setup

Requirements:

- Go 1.25.6 or newer
- Git
- POSIX shell tools for the installer smoke test
- Optional: `gitleaks` for the pre-commit hook

Clone the repository and run the same gate CI runs:

```sh
make verify
```

That builds and vets every package, runs the test suite, validates the
GoReleaser configuration, builds snapshot archives, and exercises the
checksum-verifying installer against those local artifacts. Run it before
opening a pull request.

Install the optional secret-scan hook after installing `gitleaks`:

```sh
make install-hooks
```

## Test Policy

Tests must never read or write the real `~/.codex`, `~/.claude`, state, or
configuration directories. Use `t.TempDir` and the existing environment and
flag overrides. Normal tests must not require network access.

Add focused fixtures for parser changes and broader subprocess coverage when
behavior crosses discovery, ingestion, storage, pricing, or CLI boundaries.
`internal/parity` is machine-specific and environment-gated; it skips cleanly
unless its explicit fixture environment is present.

## Writing A Provider Adapter

A provider adapter turns one provider's session format into normalized local
usage. Implement `ingest.Adapter`:

```go
type Adapter interface {
    Name() string
    ParseFile(file discover.SourceFile, emit func(UsageEvent)) (Stats, error)
}
```

Each `UsageEvent` has a UTC timestamp, stable provider and model, total input,
cache-read, classified 5-minute and 1-hour cache writes, unclassified cache
writes, output, and an optional reasoning subset. Input includes its cache
components. Output includes reasoning. Use `unknown` when the model cannot be
recovered; do not smear ambiguous tokens across models or cache classes.

The basic adapter checklist:

1. Add the provider ID and root selection in `internal/discover`.
2. Implement the parser under `internal/ingest/<provider>`.
3. Add small JSONL fixtures for normal records, malformed lines, counter
   resets or progressive snapshots, unknown models, and long lines.
4. Wire the adapter into `internal/syncer` without changing the store's
   provider-neutral `Usage` rows.
5. Add discovery, parser, incremental sync, rewrite, deletion-retention, and
   timezone tests.
6. Document the provider's native root and any explicit override.

Codex is the direct example: cumulative snapshots become non-negative deltas,
and resumable parser state follows growing files. Claude is the complex
two-stage example: files emit message candidates first, then a shared deduper
chooses the most complete progressive snapshot across every project file.
Keep that global step when a provider can duplicate the same logical event in
more than one transcript.

## Release Setup

The repository owner must create a GitHub environment named `release` and add
the desired deployment protection rules. The tag workflow is already bound to
that environment; it does not change repository settings itself.

## Licensing

Contributions are licensed under the MIT License and the contributor license
agreement below. The CLA check runs on a contributor's first pull request.

## Contributor License Agreement

By commenting `I have read the CLA Document and I hereby sign the CLA` on a
pull request, you grant Janior Valle and recipients of this project a
perpetual, worldwide, non-exclusive, royalty-free, irrevocable license to use,
reproduce, modify, display, perform, sublicense, and distribute your
contribution and derivative works under the project's license.

You represent that you are legally entitled to grant this license and that,
to your knowledge, the contribution is your original work or is submitted
with permission. You are not expected to provide support for the contribution
unless you agree to do so separately.
