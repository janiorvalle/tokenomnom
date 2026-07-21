package store

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS public_id_aliases (
	alias_public_id TEXT PRIMARY KEY,
	canonical_public_id TEXT NOT NULL,
	entity_kind TEXT NOT NULL CHECK (entity_kind IN ('session', 'prompt'))
);

CREATE TABLE IF NOT EXISTS sessions (
    id INTEGER PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL,
    identity_key TEXT NOT NULL,
    native_session_id TEXT,
    fallback_key TEXT NOT NULL DEFAULT '',
    cwd TEXT,
    repository_root TEXT,
    repository_name TEXT,
    repository_identity TEXT,
    branch TEXT,
	thread_kind TEXT NOT NULL DEFAULT 'unknown',
	parent_native_session_id TEXT,
	forked_from_session_id TEXT,
	originator TEXT,
    evidence TEXT,
    confidence TEXT NOT NULL DEFAULT 'unknown',
    first_ts TEXT,
    last_ts TEXT,
    UNIQUE(provider, identity_key)
);

CREATE TABLE IF NOT EXISTS source_heads (
    id INTEGER PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL,
    source_path TEXT NOT NULL,
	source_kind TEXT NOT NULL DEFAULT 'codex_live' CHECK (source_kind IN ('codex_live', 'codex_archive', 'claude_project')),
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    current_sha256 TEXT NOT NULL DEFAULT '',
	content_hash_state TEXT NOT NULL DEFAULT '',
	prefix_fingerprint TEXT NOT NULL DEFAULT '',
	tail_fingerprint TEXT NOT NULL DEFAULT '',
	extractor_state TEXT NOT NULL DEFAULT '',
    size INTEGER NOT NULL DEFAULT 0,
    mtime_unix INTEGER NOT NULL DEFAULT 0,
    complete_offset INTEGER NOT NULL DEFAULT 0,
	line_count INTEGER NOT NULL DEFAULT 0,
	available INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
	first_ts TEXT,
	last_ts TEXT,
    extractor_version INTEGER NOT NULL,
	indexed_at INTEGER NOT NULL,
	last_attempt_unix INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
    UNIQUE(provider, source_path)
);

CREATE TABLE IF NOT EXISTS preserved_snapshots (
    id INTEGER PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    provider TEXT NOT NULL,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    content_sha256 TEXT NOT NULL,
    size INTEGER NOT NULL,
    first_ts TEXT,
    last_ts TEXT,
    extractor_version INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    UNIQUE(provider, content_sha256)
);

CREATE TABLE IF NOT EXISTS locations (
    id INTEGER PRIMARY KEY,
    location_key TEXT NOT NULL UNIQUE,
    kind TEXT NOT NULL CHECK (kind IN ('provider_live', 'provider_archive', 'vault')),
    source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
    snapshot_id INTEGER REFERENCES preserved_snapshots(id) ON DELETE CASCADE,
    source_path TEXT NOT NULL DEFAULT '',
    relative_path TEXT NOT NULL DEFAULT '',
    archive TEXT NOT NULL DEFAULT '',
    vault_version INTEGER NOT NULL DEFAULT 0,
    available INTEGER NOT NULL DEFAULT 1 CHECK (available IN (0, 1)),
    CHECK ((source_head_id IS NOT NULL AND snapshot_id IS NULL) OR
           (source_head_id IS NULL AND snapshot_id IS NOT NULL))
);

CREATE TABLE IF NOT EXISTS prompts (
    id INTEGER PRIMARY KEY,
    public_id TEXT NOT NULL UNIQUE,
    session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    logical_key TEXT NOT NULL,
    native_message_id TEXT,
    parent_native_message_id TEXT,
    role TEXT NOT NULL DEFAULT 'unknown',
    clean_text TEXT NOT NULL DEFAULT '',
    classification TEXT NOT NULL DEFAULT 'unknown',
    searchable INTEGER NOT NULL DEFAULT 0 CHECK (searchable IN (0, 1)),
    oversized INTEGER NOT NULL DEFAULT 0 CHECK (oversized IN (0, 1)),
    timestamp TEXT,
    model TEXT,
    evidence TEXT,
    confidence TEXT NOT NULL DEFAULT 'unknown',
    extractor_version INTEGER NOT NULL,
    occurrence_count INTEGER NOT NULL DEFAULT 0,
    UNIQUE(session_id, logical_key)
);

CREATE TABLE IF NOT EXISTS occurrences (
    id INTEGER PRIMARY KEY,
    prompt_id INTEGER NOT NULL REFERENCES prompts(id) ON DELETE CASCADE,
    location_id INTEGER NOT NULL REFERENCES locations(id) ON DELETE CASCADE,
    source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	snapshot_id INTEGER REFERENCES preserved_snapshots(id) ON DELETE CASCADE,
	native_message_id TEXT,
	parent_native_message_id TEXT,
	role TEXT NOT NULL,
	clean_text TEXT NOT NULL,
	classification TEXT NOT NULL,
	searchable INTEGER NOT NULL CHECK (searchable IN (0, 1)),
	oversized INTEGER NOT NULL CHECK (oversized IN (0, 1)),
	timestamp TEXT,
	timestamp_unix_nano INTEGER,
	model TEXT,
	evidence TEXT,
	confidence TEXT NOT NULL,
	extractor_version INTEGER NOT NULL,
	line_number INTEGER NOT NULL,
    start_offset INTEGER NOT NULL,
    end_offset INTEGER NOT NULL,
    CHECK (start_offset >= 0 AND end_offset >= start_offset),
    CHECK ((source_head_id IS NOT NULL AND snapshot_id IS NULL) OR
           (source_head_id IS NULL AND snapshot_id IS NOT NULL)),
    UNIQUE(prompt_id, location_id, line_number, start_offset, end_offset)
);

CREATE TABLE IF NOT EXISTS prompt_tombstones (
	id INTEGER PRIMARY KEY,
	source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	provider TEXT NOT NULL,
	source_path TEXT NOT NULL,
	prompt_public_id TEXT NOT NULL,
	logical_key TEXT NOT NULL,
	reason TEXT NOT NULL,
	deleted_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS source_heads_session_idx ON source_heads(session_id);
CREATE INDEX IF NOT EXISTS snapshots_session_idx ON preserved_snapshots(session_id);
CREATE INDEX IF NOT EXISTS prompts_session_idx ON prompts(session_id);
CREATE INDEX IF NOT EXISTS occurrences_prompt_idx ON occurrences(prompt_id);
CREATE INDEX IF NOT EXISTS occurrences_source_idx ON occurrences(source_head_id);
CREATE INDEX IF NOT EXISTS occurrences_snapshot_idx ON occurrences(snapshot_id);
CREATE INDEX IF NOT EXISTS prompt_tombstones_source_idx ON prompt_tombstones(source_head_id, deleted_at DESC);

CREATE VIEW IF NOT EXISTS searchable_prompts AS
SELECT id, clean_text FROM prompts WHERE searchable = 1;

CREATE VIRTUAL TABLE IF NOT EXISTS prompt_fts USING fts5(
    clean_text,
    content='searchable_prompts',
    content_rowid='id',
    tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS prompts_ai AFTER INSERT ON prompts WHEN new.searchable = 1 BEGIN
    INSERT INTO prompt_fts(rowid, clean_text) VALUES (new.id, new.clean_text);
END;
CREATE TRIGGER IF NOT EXISTS prompts_ad AFTER DELETE ON prompts WHEN old.searchable = 1 BEGIN
    INSERT INTO prompt_fts(prompt_fts, rowid, clean_text) VALUES ('delete', old.id, old.clean_text);
END;
CREATE TRIGGER IF NOT EXISTS prompts_au AFTER UPDATE OF clean_text, searchable ON prompts BEGIN
    INSERT INTO prompt_fts(prompt_fts, rowid, clean_text)
        SELECT 'delete', old.id, old.clean_text WHERE old.searchable = 1;
    INSERT INTO prompt_fts(rowid, clean_text)
        SELECT new.id, new.clean_text WHERE new.searchable = 1;
END;

CREATE TRIGGER IF NOT EXISTS occurrences_ai AFTER INSERT ON occurrences BEGIN
    UPDATE prompts SET occurrence_count = occurrence_count + 1 WHERE id = new.prompt_id;
END;
CREATE TRIGGER IF NOT EXISTS occurrences_ad AFTER DELETE ON occurrences BEGIN
    UPDATE prompts SET occurrence_count = occurrence_count - 1 WHERE id = old.prompt_id;
END;
`
