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
structure_nonce: "0123456789abcdef"
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

# Full session export {#tok-0123456789abcdef}

## Root session `ses_root` {#tok-0123456789abcdef}

- Provider: `codex` {#tok-0123456789abcdef}
- Native session ID: `native-root` {#tok-0123456789abcdef}
- Time range: `2026-07-20T12:00:00Z` to `2026-07-20T12:00:02Z` {#tok-0123456789abcdef}
- Project: `demo` {#tok-0123456789abcdef}
- CWD: `/workspace/demo` {#tok-0123456789abcdef}
- Thread kind: `root` {#tok-0123456789abcdef}
- Source: `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa` (`provider_live`) {#tok-0123456789abcdef}

[metadata record: session metadata] - 2026-07-20T12:00:00Z {#tok-0123456789abcdef}

### User - 2026-07-20T12:00:01Z {#tok-0123456789abcdef}

hello

[tool call: shell, 13 bytes] - 2026-07-20T12:00:02Z {#tok-0123456789abcdef}

[delegated subagent session `ses_child`; spawn record not identified] {#tok-0123456789abcdef}

## Subagent session `ses_child` {#tok-0123456789abcdef}

- Provider: `claude` {#tok-0123456789abcdef}
- Native session ID: `native-child` {#tok-0123456789abcdef}
- Time range: `2026-07-20T12:01:00Z` to `2026-07-20T12:01:00Z` {#tok-0123456789abcdef}
- Project: `demo` {#tok-0123456789abcdef}
- CWD: `/workspace/demo` {#tok-0123456789abcdef}
- Thread kind: `subagent` {#tok-0123456789abcdef}
- Source: `bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb` (`vault`) {#tok-0123456789abcdef}

### Assistant - 2026-07-20T12:01:00Z {#tok-0123456789abcdef}

child done
