# Security

Report security issues through GitHub's private vulnerability reporting for
this repository. Do not open a public issue containing exploit details,
credentials, private logs, or other sensitive data.

You should receive an initial response within three business days. Fixes
target the latest release.

## Trust Model

tokenomnom is local software. At runtime it reads local Codex and Claude Code
logs and writes a local SQLite database. It makes no network calls and sends
no session content, token counts, model names, or pricing data anywhere.

`install.sh` is the only network touchpoint: it downloads release archives and
checksums from GitHub (or an explicit mirror), verifies SHA-256, and installs
two binaries without sudo. The Go installer for the optional agent skill
writes only under existing Codex and Claude skill directories.

Pricing overrides are parsed as data, not executed as code. They should not
contain secrets.

Useful reports include the affected version, concrete impact, and a minimal
reproduction. Scanner output without an impact path is less useful, but
uncertain reports are still welcome through the private channel.
