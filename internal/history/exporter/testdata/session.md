---
session_id: "ses_root"
native_session_id: "native-root"
provider: "codex"
first_timestamp: "2026-07-20T12:00:00Z"
last_timestamp: "2026-07-20T12:00:02Z"
project: "demo"
cwd: "/workspace/demo"
thread_kind: "root"
source_sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
source_origin: "provider_live"
tokenomnom_version: "v0.4.0"
exported_at: "2026-07-21T01:02:03Z"
sessions:
  - session_id: "ses_root"
    native_session_id: "native-root"
    provider: "codex"
    first_timestamp: "2026-07-20T12:00:00Z"
    last_timestamp: "2026-07-20T12:00:02Z"
    project: "demo"
    cwd: "/workspace/demo"
    thread_kind: "root"
    source_sha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    source_origin: "provider_live"
  - session_id: "ses_child"
    native_session_id: "native-child"
    provider: "claude"
    first_timestamp: "2026-07-20T12:01:00Z"
    last_timestamp: "2026-07-20T12:01:00Z"
    project: "demo"
    cwd: "/workspace/demo"
    thread_kind: "subagent"
    source_sha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    source_origin: "vault"
---

# Full session export

## Root session `ses_root`

- Provider: `codex`
- Native session ID: `native-root`
- Time range: `2026-07-20T12:00:00Z` to `2026-07-20T12:00:02Z`
- Project: `demo`
- CWD: `/workspace/demo`
- Thread kind: `root`
- Source: `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` (`provider_live`)

[metadata record: session metadata] - 2026-07-20T12:00:00Z

### User - 2026-07-20T12:00:01Z

hello

[tool call: shell, 13 bytes] - 2026-07-20T12:00:02Z

[delegated subagent session `ses_child`; spawn record not identified]

## Subagent session `ses_child`

- Provider: `claude`
- Native session ID: `native-child`
- Time range: `2026-07-20T12:01:00Z` to `2026-07-20T12:01:00Z`
- Project: `demo`
- CWD: `/workspace/demo`
- Thread kind: `subagent`
- Source: `bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb` (`vault`)

### Assistant - 2026-07-20T12:01:00Z

child done
