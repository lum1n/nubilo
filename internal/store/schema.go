package store

const schema = `
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;

CREATE TABLE IF NOT EXISTS schema_meta (
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    public_key BLOB,
    role TEXT NOT NULL,
    permissions TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_seen INTEGER,
    revoked_at INTEGER
);

CREATE TABLE IF NOT EXISTS app_passwords (
    id TEXT PRIMARY KEY,
    device_id TEXT NOT NULL REFERENCES devices(id),
    hash BLOB NOT NULL,
    salt BLOB NOT NULL,
    scope TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    last_used INTEGER,
    revoked_at INTEGER
);

CREATE TABLE IF NOT EXISTS pairing_sessions (
    id TEXT PRIMARY KEY,
    code_hash BLOB NOT NULL,
    code_salt BLOB NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    completed_at INTEGER,
    device_id TEXT,
    challenge BLOB,
    pending_pubkey BLOB,
    pending_name TEXT,
    pending_role TEXT
);

CREATE TABLE IF NOT EXISTS nonces (
    device_id TEXT NOT NULL,
    nonce TEXT NOT NULL,
    ts INTEGER NOT NULL,
    PRIMARY KEY (device_id, nonce)
);

CREATE TABLE IF NOT EXISTS collections (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    parent_id TEXT,
    revision INTEGER NOT NULL DEFAULT 1,
    metadata TEXT NOT NULL DEFAULT '{}',
    metadata_hash TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE TABLE IF NOT EXISTS objects (
    id TEXT PRIMARY KEY,
    collection_id TEXT NOT NULL REFERENCES collections(id),
    kind TEXT NOT NULL,
    revision INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    metadata_hash TEXT NOT NULL,
    blob_id TEXT,
    size INTEGER NOT NULL DEFAULT 0,
    origin_device TEXT,
    metadata TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    deleted_at INTEGER
);

CREATE TABLE IF NOT EXISTS object_history (
    object_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    content_hash TEXT NOT NULL,
    metadata_hash TEXT NOT NULL,
    blob_id TEXT,
    device_id TEXT,
    ts INTEGER NOT NULL,
    PRIMARY KEY (object_id, revision)
);

CREATE TABLE IF NOT EXISTS blobs (
    id TEXT PRIMARY KEY,
    size INTEGER NOT NULL,
    refcount INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS journal (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    object_id TEXT NOT NULL,
    collection_id TEXT NOT NULL,
    op TEXT NOT NULL,
    revision INTEGER NOT NULL,
    content_hash TEXT,
    metadata_hash TEXT,
    device_id TEXT,
    ts INTEGER NOT NULL,
    idempotency_key TEXT
);

CREATE TABLE IF NOT EXISTS cursors (
    device_id TEXT NOT NULL,
    scope TEXT NOT NULL,
    last_ack_seq INTEGER NOT NULL,
    last_ack_at INTEGER NOT NULL,
    PRIMARY KEY (device_id, scope)
);

CREATE TABLE IF NOT EXISTS push_idempotency (
    key TEXT PRIMARY KEY,
    device_id TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS blob_uploads (
    blob_id TEXT NOT NULL,
    device_id TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (blob_id, device_id)
);

CREATE TABLE IF NOT EXISTS audit (
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    ts INTEGER NOT NULL,
    device_id TEXT,
    event TEXT NOT NULL,
    fields TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS journal_seq ON journal(seq);
CREATE INDEX IF NOT EXISTS journal_object ON journal(object_id, revision);
CREATE INDEX IF NOT EXISTS objects_collection ON objects(collection_id);
CREATE INDEX IF NOT EXISTS nonces_ts ON nonces(ts);
CREATE INDEX IF NOT EXISTS pairing_expires ON pairing_sessions(expires_at);
`
