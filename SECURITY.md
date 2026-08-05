# Security

Report security issues through GitHub's private vulnerability reporting for
this repository. Do not open a public issue containing exploit details,
credentials, private logs, or other sensitive data.

You should receive an initial response within three business days. Fixes
target the latest release.

## Trust Model

tokenomnom is local software. Reports, sync, history, and the dashboard read
local Codex and Claude Code logs and write local SQLite databases without
making network calls. They send no session content, token counts, model names,
or pricing data anywhere.

`upgrade` and `upgrade --check` are explicit network operations. They contact
the GitHub releases API and GitHub release download URLs, send the installed
tokenomnom version in the HTTP User-Agent, and optionally send `GITHUB_TOKEN`
as authorization. They do not read or send transcript, usage, or pricing data.

`install.sh` is another explicit network touchpoint: it downloads release archives and
checksums from GitHub (or an explicit mirror), verifies SHA-256, and installs
two binaries without sudo. The Go installer for the optional agent skill
writes only under existing Codex and Claude skill directories.

Pricing overrides are parsed as data, not executed as code. They should not
contain secrets.

The optional `history.db` contains normalized user prompt text in plaintext for
local search. Assistant indexing is off by default. Enabling
`history.index_assistant` is explicit consent to store assistant text blocks,
which typically dwarf user prompts and can multiply plaintext storage. The
next explicit or scheduled index applies the change; disabling it prunes
assistant rows on that run. It is created only by a history index command;
status, doctor, reports, and normal syncs do not create it. `history purge`
removes all indexed roles and SQLite sidecars, but does not promise forensic
secure deletion. No role uses model calls, embeddings, or network access.

`history export` is a separate, explicit per-invocation plaintext operation.
It reads hash-validated provider or vault bytes and can write a complete root
session plus delegated subagent sessions outside tokenomnom's state directory.
Exports can contain prompts, assistant responses, local paths, injected
context, and, when requested, full tool output or thinking. They are not
redacted or encrypted by tokenomnom and are never created by scheduled
maintenance. Treat the destination as sensitive and apply appropriate file
permissions, retention, and sharing controls.

Useful reports include the affected version, concrete impact, and a minimal
reproduction. Scanner output without an impact path is less useful, but
uncertain reports are still welcome through the private channel.
