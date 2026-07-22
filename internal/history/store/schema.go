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
	repository_rule_version INTEGER NOT NULL DEFAULT 0,
    branch TEXT,
	thread_kind TEXT NOT NULL DEFAULT 'unknown',
	thread_evidence TEXT NOT NULL DEFAULT '',
	thread_confidence TEXT NOT NULL DEFAULT 'unknown',
	thread_rule_version INTEGER NOT NULL DEFAULT 0,
	parent_native_session_id TEXT,
	forked_from_session_id TEXT,
	forked_from_message_id TEXT,
	originator TEXT,
    evidence TEXT,
    confidence TEXT NOT NULL DEFAULT 'unknown',
    first_ts TEXT,
    last_ts TEXT,
	sample_key BLOB NOT NULL,
    UNIQUE(provider, identity_key)
);

CREATE INDEX IF NOT EXISTS sessions_sample_key_idx ON sessions(sample_key, public_id);
CREATE INDEX IF NOT EXISTS sessions_sample_month_idx ON sessions(COALESCE(strftime('%Y-%m', first_ts), 'unknown'), sample_key, public_id);
CREATE INDEX IF NOT EXISTS sessions_sample_repo_idx ON sessions(COALESCE(NULLIF(lower(repository_name), ''), 'unknown'), sample_key, public_id);
CREATE INDEX IF NOT EXISTS sessions_sample_thread_idx ON sessions(thread_kind, sample_key, public_id);

CREATE TABLE IF NOT EXISTS session_relations (
	id INTEGER PRIMARY KEY,
	provider TEXT NOT NULL,
	parent_session_id INTEGER REFERENCES sessions(id) ON DELETE SET NULL,
	child_session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	relation_kind TEXT NOT NULL CHECK (relation_kind IN ('subagent', 'fork')),
	parent_native_session_id TEXT NOT NULL DEFAULT '',
	parent_native_message_id TEXT NOT NULL DEFAULT '',
	provider_native_value TEXT NOT NULL DEFAULT '',
	evidence TEXT NOT NULL,
	confidence TEXT NOT NULL CHECK (confidence IN ('exact', 'derived', 'unknown')),
	rule_version INTEGER NOT NULL,
	resolution_state TEXT NOT NULL CHECK (resolution_state IN ('resolved', 'unresolved')),
	CHECK ((parent_session_id IS NOT NULL AND resolution_state='resolved') OR
	       (parent_session_id IS NULL AND resolution_state='unresolved'))
);

CREATE UNIQUE INDEX IF NOT EXISTS session_relations_resolved_unique
	ON session_relations(parent_session_id, child_session_id, relation_kind)
	WHERE parent_session_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS session_relations_unresolved_unique
	ON session_relations(provider, child_session_id, relation_kind, parent_native_session_id)
	WHERE parent_session_id IS NULL;
CREATE INDEX IF NOT EXISTS session_relations_parent_native_idx
	ON session_relations(provider, parent_native_session_id)
	WHERE parent_session_id IS NULL AND parent_native_session_id<>'';
CREATE INDEX IF NOT EXISTS session_relations_child_idx ON session_relations(child_session_id);

CREATE TRIGGER IF NOT EXISTS session_relations_parent_delete BEFORE DELETE ON sessions BEGIN
	UPDATE session_relations SET parent_session_id=NULL,resolution_state='unresolved'
		WHERE parent_session_id=old.id;
END;

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

CREATE TABLE IF NOT EXISTS session_relation_supports (
	id INTEGER PRIMARY KEY,
	relation_id INTEGER NOT NULL REFERENCES session_relations(id) ON DELETE CASCADE,
	source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	snapshot_id INTEGER REFERENCES preserved_snapshots(id) ON DELETE CASCADE,
	parent_native_message_id TEXT NOT NULL DEFAULT '',
	provider_native_value TEXT NOT NULL DEFAULT '',
	evidence TEXT NOT NULL,
	confidence TEXT NOT NULL CHECK (confidence IN ('exact','derived','unknown')),
	rule_version INTEGER NOT NULL,
	CHECK ((source_head_id IS NOT NULL AND snapshot_id IS NULL) OR
	       (source_head_id IS NULL AND snapshot_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS session_relation_supports_source_unique
	ON session_relation_supports(relation_id,source_head_id) WHERE source_head_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS session_relation_supports_snapshot_unique
	ON session_relation_supports(relation_id,snapshot_id) WHERE snapshot_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS session_thread_supports (
	id INTEGER PRIMARY KEY,
	session_id INTEGER NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
	source_head_id INTEGER REFERENCES source_heads(id) ON DELETE CASCADE,
	snapshot_id INTEGER REFERENCES preserved_snapshots(id) ON DELETE CASCADE,
	thread_kind TEXT NOT NULL CHECK (thread_kind IN ('root','subagent','unknown')),
	evidence TEXT NOT NULL DEFAULT '',
	confidence TEXT NOT NULL CHECK (confidence IN ('exact','derived','unknown')),
	rule_version INTEGER NOT NULL DEFAULT 0,
	parent_native_session_id TEXT NOT NULL DEFAULT '',
	forked_from_session_id TEXT NOT NULL DEFAULT '',
	forked_from_message_id TEXT NOT NULL DEFAULT '',
	originator TEXT NOT NULL DEFAULT '',
	CHECK ((source_head_id IS NOT NULL AND snapshot_id IS NULL) OR
	       (source_head_id IS NULL AND snapshot_id IS NOT NULL))
);
CREATE UNIQUE INDEX IF NOT EXISTS session_thread_supports_source_unique
	ON session_thread_supports(source_head_id) WHERE source_head_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS session_thread_supports_snapshot_unique
	ON session_thread_supports(snapshot_id) WHERE snapshot_id IS NOT NULL;

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
	sample_key BLOB NOT NULL,
	occurrence_count INTEGER NOT NULL DEFAULT 0,
    UNIQUE(session_id, logical_key)
);

CREATE INDEX IF NOT EXISTS prompts_sample_key_idx ON prompts(sample_key, public_id);
CREATE INDEX IF NOT EXISTS prompts_session_sample_key_idx ON prompts(session_id, sample_key, public_id);
CREATE INDEX IF NOT EXISTS prompts_role_timestamp_idx ON prompts(role, timestamp, public_id);

CREATE TABLE IF NOT EXISTS sample_groups (
	unit_kind TEXT NOT NULL CHECK (unit_kind IN ('prompt','session')),
	dimensions TEXT NOT NULL,
	group_values TEXT NOT NULL,
	group_key BLOB NOT NULL,
	member_count INTEGER NOT NULL CHECK (member_count > 0),
	PRIMARY KEY(unit_kind,dimensions,group_values)
);

CREATE INDEX IF NOT EXISTS sample_groups_key_idx ON sample_groups(unit_kind,dimensions,group_key,group_values);

CREATE TABLE IF NOT EXISTS sample_strata (
	unit_kind TEXT NOT NULL CHECK (unit_kind IN ('prompt','session')),
	unit_id INTEGER NOT NULL,
	dimensions TEXT NOT NULL,
	group_values TEXT NOT NULL,
	group_key BLOB NOT NULL,
	sample_key BLOB NOT NULL,
	PRIMARY KEY(unit_kind,unit_id,dimensions,group_values)
);

CREATE INDEX IF NOT EXISTS sample_strata_group_key_idx ON sample_strata(unit_kind,dimensions,group_key,sample_key,unit_id);
CREATE INDEX IF NOT EXISTS sample_strata_member_idx ON sample_strata(unit_kind,dimensions,group_values,sample_key,unit_id);

CREATE TRIGGER IF NOT EXISTS sample_strata_group_insert AFTER INSERT ON sample_strata
	WHEN new.dimensions IN ('month','cwd','repo','thread-kind','cwd,month','month,repo','month,thread-kind','repo,thread-kind','month,repo,thread-kind') BEGIN
	INSERT INTO sample_groups(unit_kind,dimensions,group_values,group_key,member_count)
		VALUES(new.unit_kind,new.dimensions,new.group_values,new.group_key,1)
		ON CONFLICT(unit_kind,dimensions,group_values) DO UPDATE SET member_count=member_count+1;
END;

CREATE TRIGGER IF NOT EXISTS sample_strata_group_delete AFTER DELETE ON sample_strata
	WHEN old.dimensions IN ('month','cwd','repo','thread-kind','cwd,month','month,repo','month,thread-kind','repo,thread-kind','month,repo,thread-kind') BEGIN
	DELETE FROM sample_groups WHERE unit_kind=old.unit_kind AND dimensions=old.dimensions AND group_values=old.group_values AND member_count=1;
	UPDATE sample_groups SET member_count=member_count-1
		WHERE unit_kind=old.unit_kind AND dimensions=old.dimensions AND group_values=old.group_values AND member_count>1;
END;

CREATE TRIGGER IF NOT EXISTS sample_strata_session_delete AFTER DELETE ON sessions BEGIN
	DELETE FROM sample_strata WHERE unit_kind='session' AND unit_id=old.id;
END;

CREATE TRIGGER IF NOT EXISTS sample_strata_prompt_delete AFTER DELETE ON prompts BEGIN
	DELETE FROM sample_strata WHERE unit_kind='prompt' AND unit_id=old.id;
END;

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

CREATE TABLE IF NOT EXISTS source_errors (
	provider TEXT NOT NULL,
	source_path TEXT NOT NULL,
	last_attempt_unix INTEGER NOT NULL,
	last_error TEXT NOT NULL,
	PRIMARY KEY(provider, source_path)
);

CREATE TABLE IF NOT EXISTS vault_bundle_state (
	archive TEXT PRIMARY KEY,
	manifest_fingerprint TEXT NOT NULL DEFAULT '',
	member_count INTEGER NOT NULL DEFAULT 0,
	extractor_version INTEGER NOT NULL DEFAULT 0,
	last_attempt_unix INTEGER NOT NULL DEFAULT 0,
	last_success_unix INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	last_error_invalidates INTEGER NOT NULL DEFAULT 0 CHECK (last_error_invalidates IN (0, 1))
);

CREATE TABLE IF NOT EXISTS vault_prompt_tombstones (
	archive TEXT NOT NULL,
	provider TEXT NOT NULL,
	session_public_id TEXT NOT NULL,
	logical_key TEXT NOT NULL,
	prompt_public_id TEXT NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY(archive, provider, session_public_id, logical_key)
);

CREATE INDEX IF NOT EXISTS source_heads_session_idx ON source_heads(session_id);
CREATE INDEX IF NOT EXISTS snapshots_session_idx ON preserved_snapshots(session_id);
CREATE INDEX IF NOT EXISTS session_relation_supports_source_idx ON session_relation_supports(source_head_id);
CREATE INDEX IF NOT EXISTS session_relation_supports_snapshot_idx ON session_relation_supports(snapshot_id);
CREATE INDEX IF NOT EXISTS session_thread_supports_session_idx ON session_thread_supports(session_id);
CREATE INDEX IF NOT EXISTS prompts_session_idx ON prompts(session_id);
CREATE INDEX IF NOT EXISTS occurrences_prompt_idx ON occurrences(prompt_id);
CREATE INDEX IF NOT EXISTS occurrences_source_idx ON occurrences(source_head_id);
CREATE INDEX IF NOT EXISTS occurrences_snapshot_idx ON occurrences(snapshot_id);
CREATE INDEX IF NOT EXISTS occurrences_role_idx ON occurrences(role, prompt_id);
CREATE INDEX IF NOT EXISTS prompt_tombstones_source_idx ON prompt_tombstones(source_head_id, deleted_at DESC);

CREATE VIEW IF NOT EXISTS searchable_prompts AS
SELECT id, clean_text FROM prompts WHERE searchable = 1 AND role IN ('user','assistant');

CREATE VIRTUAL TABLE IF NOT EXISTS prompt_fts USING fts5(
    clean_text,
    content='searchable_prompts',
    content_rowid='id',
    tokenize='unicode61'
);

CREATE TRIGGER IF NOT EXISTS prompts_ai AFTER INSERT ON prompts WHEN new.searchable = 1 AND new.role IN ('user','assistant') BEGIN
    INSERT INTO prompt_fts(rowid, clean_text) VALUES (new.id, new.clean_text);
END;
CREATE TRIGGER IF NOT EXISTS prompts_ad AFTER DELETE ON prompts WHEN old.searchable = 1 AND old.role IN ('user','assistant') BEGIN
    INSERT INTO prompt_fts(prompt_fts, rowid, clean_text) VALUES ('delete', old.id, old.clean_text);
END;
CREATE TRIGGER IF NOT EXISTS prompts_au AFTER UPDATE OF clean_text, searchable, role ON prompts BEGIN
	INSERT INTO prompt_fts(prompt_fts, rowid, clean_text)
		SELECT 'delete', old.id, old.clean_text WHERE old.searchable = 1 AND old.role IN ('user','assistant');
	INSERT INTO prompt_fts(rowid, clean_text)
		SELECT new.id, new.clean_text WHERE new.searchable = 1 AND new.role IN ('user','assistant');
END;

CREATE TRIGGER IF NOT EXISTS occurrences_ai AFTER INSERT ON occurrences BEGIN
    UPDATE prompts SET occurrence_count = occurrence_count + 1 WHERE id = new.prompt_id;
END;
CREATE TRIGGER IF NOT EXISTS occurrences_ad AFTER DELETE ON occurrences BEGIN
    UPDATE prompts SET occurrence_count = occurrence_count - 1 WHERE id = old.prompt_id;
END;
`
