-- 0001_init: initial hub schema. See docs/adr/0002-hub-storage.md.
-- All times are unix milliseconds. JSON columns hold arrays or objects
-- encoded by the Go store.

CREATE TABLE sessions (
  id_hash      BLOB PRIMARY KEY,      -- sha256(cookie value); the raw id is never stored
  subject      TEXT NOT NULL,
  display      TEXT NOT NULL DEFAULT '',
  groups       TEXT NOT NULL,         -- JSON array, post-mapping (eddy:*)
  provider     TEXT NOT NULL,         -- local | proxy | dev
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  user_agent   TEXT NOT NULL DEFAULT '',
  remote_ip    TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX sessions_expires ON sessions(expires_at);
CREATE INDEX sessions_subject ON sessions(subject, last_seen_at);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,      -- 12-character public id embedded in the token
  hash         BLOB NOT NULL UNIQUE,  -- HMAC-SHA256(pepper, secret), computed by the caller
  subject      TEXT NOT NULL,
  display      TEXT NOT NULL DEFAULT '',
  provider     TEXT NOT NULL DEFAULT '',
  groups       TEXT NOT NULL,         -- JSON array, snapshot at issue time
  name         TEXT NOT NULL,
  scopes       TEXT NOT NULL,         -- JSON array: ["read"] or ["read","operate"]
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at   INTEGER
) STRICT;
CREATE INDEX api_tokens_subject ON api_tokens(subject, created_at);
CREATE INDEX api_tokens_expires ON api_tokens(expires_at);

CREATE TABLE threads (
  id                 TEXT PRIMARY KEY, -- UUIDv7
  cluster            TEXT NOT NULL,
  api_group          TEXT NOT NULL DEFAULT '',
  kind               TEXT NOT NULL DEFAULT '', -- '' = cluster-level thread
  namespace          TEXT NOT NULL DEFAULT '',
  name               TEXT NOT NULL DEFAULT '',
  type               TEXT NOT NULL CHECK (type IN ('discussion','ask')),
  visibility         TEXT NOT NULL CHECK (visibility IN ('resource','private')),
  status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  title              TEXT NOT NULL CHECK (length(title) <= 200),
  created_by         TEXT NOT NULL,
  created_by_type    TEXT NOT NULL CHECK (created_by_type IN ('human','ai','system')),
  created_by_display TEXT NOT NULL DEFAULT '',
  created_via        TEXT NOT NULL,
  created_client     TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  resolved_by        TEXT,
  resolved_at        INTEGER,
  message_count      INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX threads_target   ON threads(cluster, namespace, kind, name, updated_at DESC);
CREATE INDEX threads_recent   ON threads(updated_at DESC, id DESC);
CREATE INDEX threads_creator  ON threads(created_by, updated_at DESC);
CREATE INDEX threads_resolved ON threads(resolved_at) WHERE status = 'resolved';
CREATE INDEX threads_ask      ON threads(updated_at) WHERE type = 'ask';

CREATE TABLE messages (
  id             TEXT PRIMARY KEY,    -- UUIDv7
  thread_id      TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  author         TEXT NOT NULL,       -- the accountable human's subject
  author_type    TEXT NOT NULL CHECK (author_type IN ('human','ai','system')),
  author_display TEXT NOT NULL DEFAULT '',
  via            TEXT NOT NULL,       -- web | mcp | askai
  client         TEXT NOT NULL DEFAULT '', -- MCP clientInfo.name or model id
  body           TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) <= 65536),
  meta           TEXT,                -- JSON: AI tool steps, usage; never raw objects
  created_at     INTEGER NOT NULL
) STRICT;
CREATE INDEX messages_thread ON messages(thread_id, created_at, id);

CREATE TABLE audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts         INTEGER NOT NULL,
  request_id TEXT NOT NULL DEFAULT '',
  subject    TEXT NOT NULL,
  groups     TEXT NOT NULL,           -- JSON array
  via        TEXT NOT NULL,           -- web | mcp | askai | system
  token_id   TEXT NOT NULL DEFAULT '', -- PAT id when via = mcp
  action     TEXT NOT NULL,
  cluster    TEXT NOT NULL DEFAULT '',
  api_group  TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT '',
  namespace  TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  result     TEXT NOT NULL CHECK (result IN ('ok','denied','error')),
  detail     TEXT                     -- JSON, at most 4 KiB
) STRICT;
CREATE INDEX audit_ts      ON audit_events(ts, id);
CREATE INDEX audit_subject ON audit_events(subject, ts, id);
CREATE INDEX audit_target  ON audit_events(cluster, namespace, kind, name, ts);

CREATE TABLE user_prefs (
  subject    TEXT PRIMARY KEY,
  data       TEXT NOT NULL CHECK (length(CAST(data AS BLOB)) <= 16384),
  updated_at INTEGER NOT NULL
) STRICT;
