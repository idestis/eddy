-- 0001_init: initial hub schema. See docs/adr/0004-hub-high-availability.md.
--
-- * Times are unix milliseconds in BIGINT.
-- * JSON (string lists, message meta, audit detail, prefs) is stored as TEXT
--   encoded by the Go store; queries never use jsonb operators.
-- * Ids and other columns used for ordering or keyset cursors are
--   COLLATE "C", so they sort bytewise exactly like the Go backends,
--   whatever the database's default collation is.
-- * Durable user data is in LOGGED tables. Sessions, rate-limit counters and
--   the agent session registry are UNLOGGED: they skip the WAL (and so
--   replicas), and a crash truncates them, which only signs people out,
--   resets limit windows and makes agents re-register.
-- * CHECK constraints named *_len or *_max are size limits (store.ErrLimit);
--   the other CHECKs validate enums (store.ErrInvalid).

-- ---- throwaway state (UNLOGGED) ----

CREATE UNLOGGED TABLE sessions (
  id_hash      BYTEA PRIMARY KEY,     -- sha256(cookie value); the raw id is never stored
  subject      TEXT NOT NULL,
  display      TEXT NOT NULL DEFAULT '',
  groups       TEXT NOT NULL,         -- JSON array, post-mapping (eddy:*)
  provider     TEXT NOT NULL,         -- local | proxy | dev
  created_at   BIGINT NOT NULL,
  last_seen_at BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL,
  user_agent   TEXT NOT NULL DEFAULT '',
  remote_ip    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_expires ON sessions (expires_at);
CREATE INDEX sessions_subject ON sessions (subject, last_seen_at);

CREATE UNLOGGED TABLE rate_limits (
  key          TEXT PRIMARY KEY,
  window_start BIGINT NOT NULL,       -- first hit of the current window
  reset_at     BIGINT NOT NULL,       -- window end; the next hit at or after it starts a new window
  count        BIGINT NOT NULL
);
CREATE INDEX rate_limits_reset ON rate_limits (reset_at);

CREATE UNLOGGED TABLE agent_sessions (
  cluster        TEXT COLLATE "C" NOT NULL,
  agent_instance TEXT COLLATE "C" NOT NULL, -- random id per agent process
  hub_pod        TEXT COLLATE "C" NOT NULL, -- replica holding the WebSocket
  hub_addr       TEXT NOT NULL DEFAULT '',  -- host:port of that replica's peer listener
  seq            BIGINT NOT NULL,           -- agent's dial counter; higher replaces lower
  connected_at   BIGINT NOT NULL,
  heartbeat_at   BIGINT NOT NULL,
  PRIMARY KEY (cluster, agent_instance)
);
CREATE INDEX agent_sessions_hub ON agent_sessions (hub_pod);
CREATE INDEX agent_sessions_heartbeat ON agent_sessions (heartbeat_at);

-- ---- durable data (LOGGED) ----

CREATE TABLE api_tokens (
  id           TEXT COLLATE "C" PRIMARY KEY, -- 12-character public id embedded in the token
  hash         BYTEA NOT NULL UNIQUE,        -- HMAC-SHA256(pepper, secret), computed by the caller
  subject      TEXT NOT NULL,
  display      TEXT NOT NULL DEFAULT '',
  provider     TEXT NOT NULL DEFAULT '',
  groups       TEXT NOT NULL,                -- JSON array, snapshot at issue time
  name         TEXT NOT NULL,
  scopes       TEXT NOT NULL,                -- JSON array: ["read"] or ["read","operate"]
  created_at   BIGINT NOT NULL,
  expires_at   BIGINT NOT NULL,
  last_used_at BIGINT,
  revoked_at   BIGINT
);
CREATE INDEX api_tokens_subject ON api_tokens (subject, created_at);
CREATE INDEX api_tokens_expires ON api_tokens (expires_at);
CREATE INDEX api_tokens_revoked ON api_tokens (revoked_at) WHERE revoked_at IS NOT NULL;

CREATE TABLE threads (
  id                 TEXT COLLATE "C" PRIMARY KEY, -- UUIDv7
  cluster            TEXT NOT NULL,
  api_group          TEXT NOT NULL DEFAULT '',
  kind               TEXT NOT NULL DEFAULT '',     -- '' = cluster-level thread
  namespace          TEXT NOT NULL DEFAULT '',
  name               TEXT NOT NULL DEFAULT '',
  type               TEXT NOT NULL CONSTRAINT threads_type_valid CHECK (type IN ('discussion', 'ask')),
  visibility         TEXT NOT NULL CONSTRAINT threads_visibility_valid CHECK (visibility IN ('resource', 'private')),
  status             TEXT NOT NULL DEFAULT 'open' CONSTRAINT threads_status_valid CHECK (status IN ('open', 'resolved')),
  title              TEXT NOT NULL CONSTRAINT threads_title_len CHECK (char_length(title) <= 200),
  created_by         TEXT NOT NULL,
  created_by_type    TEXT NOT NULL CONSTRAINT threads_created_by_type_valid CHECK (created_by_type IN ('human', 'ai', 'system')),
  created_by_display TEXT NOT NULL DEFAULT '',
  created_via        TEXT NOT NULL,
  created_client     TEXT NOT NULL DEFAULT '',
  created_at         BIGINT NOT NULL,
  updated_at         BIGINT NOT NULL,
  resolved_by        TEXT,
  resolved_at        BIGINT,
  message_count      INTEGER NOT NULL DEFAULT 0 CONSTRAINT threads_message_count_max CHECK (message_count BETWEEN 0 AND 1000)
);
CREATE INDEX threads_target   ON threads (cluster, namespace, kind, name, updated_at DESC);
CREATE INDEX threads_recent   ON threads (updated_at DESC, id DESC);
CREATE INDEX threads_creator  ON threads (created_by, updated_at DESC);
CREATE INDEX threads_resolved ON threads (resolved_at) WHERE status = 'resolved';
CREATE INDEX threads_ask      ON threads (updated_at) WHERE type = 'ask';

CREATE TABLE messages (
  id             TEXT COLLATE "C" PRIMARY KEY, -- UUIDv7
  thread_id      TEXT COLLATE "C" NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
  author         TEXT NOT NULL,                -- the accountable human's subject
  author_type    TEXT NOT NULL CONSTRAINT messages_author_type_valid CHECK (author_type IN ('human', 'ai', 'system')),
  author_display TEXT NOT NULL DEFAULT '',
  via            TEXT NOT NULL,                -- web | mcp | askai
  client         TEXT NOT NULL DEFAULT '',     -- MCP clientInfo.name or model id
  body           TEXT NOT NULL CONSTRAINT messages_body_len CHECK (octet_length(body) <= 65536),
  meta           TEXT,                         -- JSON: AI tool steps, usage; never raw objects
  created_at     BIGINT NOT NULL
);
CREATE INDEX messages_thread ON messages (thread_id, created_at, id);

CREATE TABLE audit_events (
  id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  ts         BIGINT NOT NULL,
  request_id TEXT NOT NULL DEFAULT '',
  subject    TEXT NOT NULL,
  groups     TEXT NOT NULL,                    -- JSON array
  via        TEXT NOT NULL,                    -- web | mcp | askai | system
  token_id   TEXT NOT NULL DEFAULT '',         -- PAT id when via = mcp
  action     TEXT NOT NULL,
  cluster    TEXT NOT NULL DEFAULT '',
  api_group  TEXT NOT NULL DEFAULT '',
  kind       TEXT NOT NULL DEFAULT '',
  namespace  TEXT NOT NULL DEFAULT '',
  name       TEXT NOT NULL DEFAULT '',
  result     TEXT NOT NULL CONSTRAINT audit_events_result_valid CHECK (result IN ('ok', 'denied', 'error')),
  detail     TEXT CONSTRAINT audit_events_detail_len CHECK (octet_length(detail) <= 4096) -- JSON
);
CREATE INDEX audit_ts      ON audit_events (ts, id);
CREATE INDEX audit_subject ON audit_events (subject, ts, id);
CREATE INDEX audit_target  ON audit_events (cluster, namespace, kind, name, ts);

CREATE TABLE user_prefs (
  subject    TEXT PRIMARY KEY,
  data       TEXT NOT NULL CONSTRAINT user_prefs_data_len CHECK (octet_length(data) <= 16384),
  updated_at BIGINT NOT NULL
);
