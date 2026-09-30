# ADR-0002: Hub storage layer

- **Status:** Accepted
- **Date:** 2026-09-30
- **Scope:** `eddy-hub` only. Agents stay stateless apart from their informer caches.

## Context

Kubernetes is the source of truth for cluster state. Flux keeps its specs, status, conditions, inventory and events in the API server of each workload cluster, and the hub only ever sees summaries (`internal/model.Resource`) streamed by agents. **None of that is copied into a database.**

The MVP adds hub-owned data that Kubernetes does not hold:

| Data | Shape | Write rate | Lifetime |
|---|---|---|---|
| Sessions | small row per login | touch on activity (throttled) | hours to days |
| Personal access tokens (PATs) for MCP clients | small, hashed | rare, plus `last_used` touch | up to 90 days |
| Threads and messages (human, MCP/Claude Code, Ask AI) | append-heavy, can be long (AI output) | chatty | kept until deleted |
| Audit log | append-only | every action, MCP call and AI tool step | 90 days |
| User prefs | small JSON per user | rare | kept |

The goals are `helm install` into one namespace with no mandatory external infra, a single replica for v0.1 with a real path to HA, easy backups, and testability. Thread reads must be filtered by the **same SAR check as the target resource** in the workload cluster, not by RBAC in the hub cluster.

## Decision

**Hybrid. Kubernetes keeps configuration and desired state. An embedded SQLite database keeps hub-owned application data, behind a small `internal/store` interface with interchangeable backends.**

1. **Kubernetes (unchanged, GitOps-managed):** the `Cluster` CRD (spec, plus status written by the hub), agent token Secrets, the session/signing key Secret, the local-users Secret (bcrypt hashes from Helm values) and the hub config ConfigMap.
2. **`store/sqlite` is the default backend.** It uses `modernc.org/sqlite` (pure Go, no CGO, currently v1.60.1) with a file on a PVC at `/var/lib/eddy/eddy.db`, WAL mode and forward-only embedded migrations.
3. **`store/memory`** backs unit tests and `make dev-hub`. It is never the Helm default.
4. **`store/postgres`** (pgx/v5) is planned for v0.2 HA. It is not built in v0.1, but the interface, schema and conformance tests are written to be portable to it.
5. **The store knows nothing about authorization.** The hub service layer filters every thread read through the existing `Authorizer` (SAR cache).

This replaces the spec's "sessions in memory, Redis later". Sessions move into the store, so a restart no longer logs everyone out, and Redis is no longer planned.

## Consequences

**Positive**
- Zero mandatory infra: one binary, one PVC. The default StorageClass works on kind (local-path), EKS, GKE and AKS.
- Chatty data (messages, audit) stays off etcd, so the management cluster's API server and watch streams are untouched.
- Real SQL: keyset pagination, indexes by target resource and cheap retention deletes.
- Backups are one file: `VACUUM INTO`, a CronJob `kubectl cp`, or an optional Litestream sidecar to S3/GCS.
- One conformance suite (`storetest.Run(t, newStore)`) runs against every backend.

**Negative / accepted**
- An RWO PVC means one writer, so `replicaCount` must be 1 while `driver=sqlite`. The chart `fail`s otherwise and uses `strategy: Recreate`, which causes a few seconds of downtime on upgrade.
- The PVC is stateful and outside GitOps, so its lifecycle has to be documented. The chart annotates it `helm.sh/resource-policy: keep`.
- Filtering after the query complicates pagination: the service over-fetches until `limit` visible rows are found or it has done 5 pages.
- HA needs Postgres. It also needs agent/SSE fan-out across replicas, which is out of scope for this ADR.

## Alternatives considered

| Option | Verdict | Why |
|---|---|---|
| (a) In-memory only | Rejected as default, kept for tests and dev | A restart loses PATs (Claude Code breaks), threads and audit. Unacceptable once threads exist. |
| (b) SQLite on PVC | **Chosen** | Best ratio of simplicity to capability. Pure-Go driver keeps static builds and distroless images. |
| (c) PostgreSQL | Deferred to v0.2 as an option | Right for HA, but a mandatory Postgres (or a bundled chart) is the heaviest ask for OSS adopters. It stays opt-in (`store.driver: postgres`, `existingSecret` DSN). |
| (d) K8s API (Thread CRD, ConfigMaps) | Rejected for app data | See below. |
| (e) Hybrid | **Chosen**, as (b) plus K8s for config | |

**Why not a Thread CRD or ConfigMaps**
- **Write amplification.** Every reply rewrites the whole object in etcd and fans out to every watcher. Messages as separate CRs would mean thousands of objects plus GC. Audit in etcd would mean one etcd write per click.
- **Size limit.** The 1.5 MiB etcd object limit is reachable by a long Ask AI thread with tool steps.
- **"RBAC-native" is illusory.** The RBAC that matters is on the target resource in the *workload* cluster. Hub-cluster RBAC on `threads.gitops.eddy.dev` says nothing about who may see `prod-eu/flux-system/HelmRelease/podinfo`, and users usually have no hub-cluster access anyway. The hub would still need its own SAR filter.
- **GitOps conflict.** Threads are runtime conversation, not desired state. Flux pruning or `kubectl apply` drift would fight them.
- **Other gaps.** No pagination by `updated_at`, no full-text search later, and the hub SA would need broad write RBAC in its namespace.

## 1. `internal/store` interface

```go
package store

var (ErrNotFound = errors.New("store: not found"); ErrConflict = errors.New("store: conflict"))

type Store interface {
	Sessions() Sessions
	Tokens() Tokens
	Threads() Threads
	Audit() Audit
	Prefs() Prefs
	Prune(ctx context.Context, now time.Time, r Retention) (PruneStats, error) // janitor, every 10 min
	Ping(ctx context.Context) error
	Close() error
}

type Sessions interface {
	Create(ctx context.Context, s Session) error                 // s.IDHash = sha256(cookie id)
	Get(ctx context.Context, idHash []byte) (Session, error)     // ErrNotFound if expired
	Touch(ctx context.Context, idHash []byte, seen time.Time) error // caller throttles to ≤1/min
	Delete(ctx context.Context, idHash []byte) error
	DeleteBySubject(ctx context.Context, subject string) error   // "log out everywhere"
}

type Tokens interface {
	Create(ctx context.Context, t Token) error
	GetByHash(ctx context.Context, hash []byte) (Token, error)   // excludes revoked/expired
	List(ctx context.Context, subject string) ([]Token, error)   // never returns Hash
	Revoke(ctx context.Context, subject, id string, at time.Time) error
	MarkUsed(ctx context.Context, id string, at time.Time) error // throttled
}

type Threads interface {
	Create(ctx context.Context, t Thread, first Message) (Thread, error)
	Get(ctx context.Context, id string) (Thread, error)
	List(ctx context.Context, f ThreadFilter) (items []Thread, next string, err error) // keyset cursor
	AddMessage(ctx context.Context, threadID string, m Message) (Message, error) // bumps updated_at, count
	Messages(ctx context.Context, threadID, cursor string, limit int) ([]Message, string, error)
	SetStatus(ctx context.Context, id string, st ThreadStatus, by string, at time.Time) error
	Delete(ctx context.Context, id string) error
}

type Audit interface {
	Append(ctx context.Context, e AuditEvent) error // also mirrored to slog JSON stdout
	Query(ctx context.Context, f AuditFilter) ([]AuditEvent, string, error)
}

type Prefs interface {
	Get(ctx context.Context, subject string) (json.RawMessage, error)
	Put(ctx context.Context, subject string, data json.RawMessage) error
}

type ResourceRef struct{ Cluster, Group, Kind, Namespace, Name string } // Kind "" = cluster-level thread
type Author struct {
	Type    AuthorType // human | ai | system
	Subject string     // the accountable human (PAT owner / session user)
	Via     string     // web | mcp | askai
	Client  string     // MCP clientInfo.name, e.g. "claude-code"; model id for askai
}
type Thread struct {
	ID string; Ref ResourceRef; Type ThreadType /* discussion|ask */; Visibility Visibility /* resource|private */
	Title string; Status ThreadStatus; CreatedBy Author; CreatedAt, UpdatedAt time.Time
	ResolvedBy string; ResolvedAt *time.Time; MessageCount int
}
type Message struct{ ID, ThreadID string; Author Author; Body string; Meta json.RawMessage; CreatedAt time.Time }
type ThreadFilter struct{ Ref ResourceRef /* zero fields = wildcard */; Status ThreadStatus; Type ThreadType; Viewer string; Cursor string; Limit int }
// Session, Token, AuditEvent, AuditFilter, Retention, PruneStats: plain structs mirroring the DDL below.
```

IDs are UUIDv7 (`github.com/google/uuid` `NewV7`), which sort by time and are safe to expose. Consumers depend on the narrow sub-interface they need (for example `auth` takes `store.Sessions`). `store.Open(ctx, cfg)` selects the backend.

**Authorization wrapper** (`internal/hub/threads`, not in the store):
- **Read and reply** require the SAR `get` on `Ref`, mapped to group and plural resource via `internal/flux/kinds.go` and reusing the 45 s cache.
- **Private threads** (Ask AI default) additionally require `viewer == CreatedBy.Subject`.
- **Resolve** requires being the author or passing a `patch` SAR on `Ref`. **Delete** requires being the author.
- **Cluster-level threads** (`Kind == ""`) are visible to anyone who can see the cluster.
- A thread whose target was deleted stays readable under the same SAR, since a SAR for a missing name still evaluates.

## 2. Schema (SQLite, migration `0001_init.sql`)

```sql
PRAGMA foreign_keys = ON;  -- also set per connection, with journal_mode=WAL, busy_timeout=5000, synchronous=NORMAL
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);

CREATE TABLE sessions (
  id_hash      BLOB PRIMARY KEY,               -- sha256(cookie value); raw id never stored
  subject      TEXT NOT NULL, groups TEXT NOT NULL,  -- groups: JSON array, post-mapping (eddy:*)
  provider     TEXT NOT NULL,                  -- local | proxy | dev (oidc/saml later)
  created_at   INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, -- unix ms
  user_agent   TEXT, remote_ip TEXT
);
CREATE INDEX sessions_expires ON sessions(expires_at);
CREATE INDEX sessions_subject ON sessions(subject);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,               -- uuidv7, shown in UI/audit
  hash         BLOB NOT NULL UNIQUE,           -- sha256(full token)
  subject      TEXT NOT NULL, groups TEXT NOT NULL,  -- groups snapshot at issue time
  name         TEXT NOT NULL, scopes TEXT NOT NULL,  -- JSON: ["read","threads","act"]
  created_at   INTEGER NOT NULL, expires_at INTEGER NOT NULL,
  last_used_at INTEGER, revoked_at INTEGER
);
CREATE INDEX api_tokens_subject ON api_tokens(subject);

CREATE TABLE threads (
  id TEXT PRIMARY KEY,
  cluster TEXT NOT NULL, api_group TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL DEFAULT '',
  namespace TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '',
  type       TEXT NOT NULL CHECK (type IN ('discussion','ask')),
  visibility TEXT NOT NULL CHECK (visibility IN ('resource','private')),
  status     TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  title      TEXT NOT NULL CHECK (length(title) <= 200),
  created_by TEXT NOT NULL, created_by_type TEXT NOT NULL, created_via TEXT NOT NULL,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL,
  resolved_by TEXT, resolved_at INTEGER,
  message_count INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX threads_target  ON threads(cluster, namespace, kind, name, updated_at DESC);
CREATE INDEX threads_recent  ON threads(updated_at DESC, id);
CREATE INDEX threads_creator ON threads(created_by, updated_at DESC);

CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  thread_id   TEXT NOT NULL REFERENCES threads(id) ON DELETE CASCADE,
  author      TEXT NOT NULL,
  author_type TEXT NOT NULL CHECK (author_type IN ('human','ai','system')),
  via TEXT NOT NULL, client TEXT,              -- client: MCP clientInfo.name or model id
  body TEXT NOT NULL CHECK (length(body) <= 65536),
  meta TEXT,                                   -- JSON: AI tool steps, usage; no raw objects
  created_at INTEGER NOT NULL
);
CREATE INDEX messages_thread ON messages(thread_id, created_at, id);

CREATE TABLE audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL, request_id TEXT,
  subject TEXT NOT NULL, groups TEXT NOT NULL,
  via TEXT NOT NULL,                           -- web | mcp | askai | system
  token_id TEXT,                               -- PAT id when via=mcp
  action TEXT NOT NULL,                        -- reconcile, suspend, thread.create, token.create, login...
  cluster TEXT, api_group TEXT, kind TEXT, namespace TEXT, name TEXT,
  result TEXT NOT NULL CHECK (result IN ('ok','denied','error')),
  detail TEXT                                  -- JSON, bounded 4 KiB
);
CREATE INDEX audit_ts      ON audit_events(ts);
CREATE INDEX audit_subject ON audit_events(subject, ts);
CREATE INDEX audit_target  ON audit_events(cluster, namespace, kind, name, ts);

CREATE TABLE user_prefs (subject TEXT PRIMARY KEY, data TEXT NOT NULL CHECK (length(data) <= 16384), updated_at INTEGER NOT NULL);
```

`message_count` and `updated_at` are updated inside the `AddMessage` transaction. Service-level caps: 1000 messages per thread and 200 open threads per target (409 beyond). A migration that adds FTS5 on `messages.body` can come later.

**Connections.** A single writer `*sql.DB` (`SetMaxOpenConns(1)`, `_txlock=immediate`) plus a separate read pool (N connections, `mode=ro`), so busy errors don't happen under WAL.

**Migrations: hand-rolled, about 80 lines, no dependency.**
- Files live in `//go:embed migrations/sqlite/*.sql` and are named `NNNN_desc.sql`.
- On start, the hub takes a write transaction, reads `max(version)` from `schema_migrations` and applies each pending file in its own transaction.
- Migrations are forward-only; there are no down migrations.
- Before applying any migration, the hub runs `VACUUM INTO '/var/lib/eddy/pre-NNNN.db'`, keeping the last 2 copies.
- The hub refuses to start if the database version is newer than the binary knows, which protects against downgrades.
- Postgres later gets `migrations/postgres/` with the same version numbers.
- Considered: `pressly/goose/v3` (v3.28.0; its `Provider` API supports `embed.FS` and uses modernc) is a fine fallback if down migrations or Go migrations are needed. `golang-migrate/v4` (v4.20.1) is heavier and its sqlite driver is CGO-based by default. Both were rejected for v0.1 to keep the dependency surface small.

## 3. Retention and what stays in Kubernetes

| Data | Rule (defaults, all configurable) |
|---|---|
| Sessions | Idle timeout 12h (sliding `expires_at`), absolute limit 7d. Logout deletes the row. The janitor deletes expired rows. |
| PATs | `expires_at` is mandatory: default 30d, maximum 90d (`tokenMaxTTL`). Revoked or expired rows are purged 30d after expiry, so audit still resolves `token_id` → name. |
| Threads | Kept until deleted (`threadsDays: 0`). Optionally, resolved threads are purged N days after `resolved_at`. Ask threads can have their own TTL (default 30d, since they are private scratch). |
| Audit | 90 days, pruned in batches of 5k to avoid long write locks. Every event is also a slog JSON line on stdout, so log shipping (Loki etc.) remains the long-term audit path. An audit webhook comes later. |
| Prefs | Kept. Deleted on user deletion (local users). |

The janitor runs every 10 minutes and runs `PRAGMA optimize` daily.

**Stays in Kubernetes and is never mirrored into the store:**
- The `Cluster` CRD. Connection state, versions and counts live in its `status` subresource. It is rebuilt from agent heartbeats, so it needs no backup.
- Agent token Secrets. The hub compares sha256 hashes in constant time.
- The session/CSRF/signing key Secret. CSRF tokens are `HMAC(key, session id)`, so they are derived rather than stored.
- The local users Secret.
- All Flux and workload state.

A thread stores only a `ResourceRef`, never a snapshot of the resource.

## 4. Helm values

```yaml
store:
  driver: sqlite            # sqlite | memory (dev only) | postgres (v0.2)
  sqlite: {path: /var/lib/eddy/eddy.db}
  postgres: {existingSecret: "", key: dsn}
  retention: {auditDays: 90, sessionIdle: 12h, sessionMax: 168h, tokenMaxTTL: 2160h, resolvedThreadsDays: 0, askThreadsDays: 30}
persistence:
  enabled: true
  storageClass: ""          # "" = cluster default, "-" = no class (static PV)
  accessModes: [ReadWriteOnce]
  size: 1Gi                 # ~1M messages + 90d of audit fit comfortably
  existingClaim: ""
  annotations: {}           # chart always adds helm.sh/resource-policy: keep
backup:
  litestream: {enabled: false, replicaURL: "", existingSecret: ""}  # optional sidecar
```

- **Chart logic.** `persistence.enabled` renders a PVC, `strategy: Recreate` and `podSecurityContext.fsGroup` so the non-root user can write. The root filesystem stays read-only because only `/var/lib/eddy` is writable. `replicaCount > 1` with `driver != postgres` fails at template time.
- **Persistence disabled.** The same SQLite code runs on an `emptyDir`; the memory backend is not used, so there is one code path. Any pod restart or reschedule logs out all users, invalidates every PAT (MCP clients get 401) and loses threads and in-DB audit (stdout audit survives in log shipping). NOTES.txt and a startup `WARN` log say so, and `/api/v1/me` exposes `features.ephemeralStore=true` so the UI shows a banner.
- `driver: memory` behaves the same as persistence disabled, but it is intended only for `make dev-hub`.

## 5. Security notes

- **Hashing.** Tokens are `eddy_pat_` + base62(32 random bytes). The prefix enables secret scanning. Only `sha256(token)` is stored, and the token is shown once. Session ids are stored the same way. Plain sha256 is sufficient for 256-bit random secrets; bcrypt/argon2 is only for human passwords (local users, in their Secret). Lookup is by unique hash index, so no timing oracle exists on the secret.
- **PAT scopes.** Scopes are `read`, `threads` and `act`. The default excludes `act`, and MCP action tools also require an explicit `confirm` argument (the cluster name on protected clusters). The PAT's group snapshot is bounded by its TTL. Revocation takes effect immediately, since there is no token cache longer than 30s.
- **No Secret data, ever.** The store never holds Secret or ConfigMap contents or raw objects, only refs and user- or AI-authored text. Ask AI tools only see agent summaries, which already exclude Secret data. Users can still paste credentials into a message, so the hub runs a best-effort redaction pass (common token regexes) on AI-authored messages and warns in the composer.
- **Prompt injection.** Thread bodies, and also events, logs and annotations, are **untrusted user-generated input** whenever they are fed to Ask AI or returned by MCP `threads.read`. The failure mode is a confused deputy: user A plants instructions in a thread, and user B's Claude Code then acts on them with B's privileges. Mitigations:
  - Wrap untrusted content in clearly delimited blocks marked as data.
  - Never put it in system prompts.
  - Label AI-authored messages (`author_type=ai`, plus client and model) in the UI and the MCP output.
  - Require human confirmation for every mutating tool.
  - Record the originating thread id in the audit `detail` for AI-initiated actions.
  - Default Ask AI threads to `private`.
- **Authorization at the service layer.** The store is authz-agnostic, and every read path goes through the SAR filter. The conformance tests assert that the store never returns hashes from `List`.
- **Database file.** Mode 0600, owned by the pod UID. Backups (Litestream, or `VACUUM INTO` copies) contain PII (emails, message text) but no credentials. Document encryption at rest via the StorageClass or bucket.
- **Audit integrity.** The app has no update or delete API for audit, only the retention janitor. Log fields are structured (slog), never string-concatenated, so user text cannot forge log lines.

## Reconciliation with ADR-0003 (main session, 2026-09-30)

- PAT scopes are `read` and `operate` (not `read`/`threads`/`act`). `api_tokens.hash` is `HMAC-SHA256(pepper, secret)`, not plain sha256, and `api_tokens.id` is the 12-character public id embedded in the token.
- Session timeouts are 8h idle and 24h absolute.
- For local users, token groups are resolved live from `users.yaml` on each use; the stored snapshot is only used for proxy users, intersected with their latest session's groups.
- Migrations are hand-rolled as described; goose stays the fallback.
