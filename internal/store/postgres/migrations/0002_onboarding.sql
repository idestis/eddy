-- 0002_onboarding: cluster join tokens and rejected agent connections
-- (docs/adr/0005-cluster-onboarding.md). Additive only.

-- One-time tokens that let an agent claim a cluster. LOGGED, so the trail
-- of who issued which token and when it was used survives a crash.
CREATE TABLE join_tokens (
  id         TEXT COLLATE "C" PRIMARY KEY,   -- 12-character public id embedded in the token
  cluster    TEXT COLLATE "C" NOT NULL,
  hash       BYTEA NOT NULL UNIQUE,          -- HMAC-SHA256(pepper, secret), computed by the caller
  created_by TEXT NOT NULL DEFAULT '',
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  used_at    BIGINT,
  revoked_at BIGINT,                         -- replaced by a newer token, or the cluster was deleted
  CONSTRAINT join_tokens_cluster_len CHECK (octet_length(cluster) <= 63)
);
CREATE INDEX join_tokens_cluster ON join_tokens (cluster, created_at);
CREATE INDEX join_tokens_live ON join_tokens (cluster) WHERE used_at IS NULL AND revoked_at IS NULL;
CREATE INDEX join_tokens_expires ON join_tokens (expires_at);

-- The last rejected agent connections per cluster (store.MaxAttemptsPerCluster),
-- shared by every replica. Throwaway, so UNLOGGED.
CREATE UNLOGGED TABLE connection_attempts (
  id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  cluster TEXT COLLATE "C" NOT NULL,
  at      BIGINT NOT NULL,
  reason  TEXT NOT NULL,
  detail  TEXT NOT NULL DEFAULT '',
  peer    TEXT NOT NULL DEFAULT '',
  hub_pod TEXT NOT NULL DEFAULT ''
);
CREATE INDEX connection_attempts_cluster ON connection_attempts (cluster, at, id);
