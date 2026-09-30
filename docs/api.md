# Hub API (v1)

This is the contract between the web UI (`web/`), MCP clients and the hub (`internal/hub`).
All JSON uses camelCase. Types come from `internal/model`, `internal/store` and `internal/identity`.

## Conventions

- **Auth:** the browser uses the session cookie (`__Host-eddy_session`, or `eddy_session`
  on plain-http localhost). `/mcp` accepts only `Authorization: Bearer eddy_pat_…`.
  PATs get 401 on `/api`.
- **CSRF:** every unsafe method on `/api` and `/auth` needs `X-Eddy-CSRF: <csrf from /me>`,
  and either an `Origin` equal to `publicURL` or `Sec-Fetch-Site: same-origin`.
  Login uses the pre-session token (`GET /auth/csrf`).
- **Errors:** `{"error": {"code": "forbidden", "message": "…"}}` with the matching HTTP
  status. The codes are:
  - `bad_request` 400
  - `unauthorized` 401
  - `forbidden` 403
  - `not_found` 404
  - `conflict` 409
  - `confirm_required` 428
  - `rate_limited` 429
  - `disconnected` 503
  - `disabled` 503
  - `internal` 500
- **Resource path:** `{kind}/{ns}/{name}` uses the Flux or workload Kind (for example
  `Kustomization`). Cluster-scoped objects use `_` as the namespace. The API group is
  inferred from the kind table in `internal/flux/kinds.go`.
- **Pagination:** list endpoints accept `?cursor=&limit=` and return `{items, next}`.

## Session

| Method and path | Body / query | Response |
|---|---|---|
| `GET /auth/csrf` | | `{csrf}` and sets the `__Host-eddy_pre` cookie (10 min) |
| `GET /auth/providers` | | `{local: bool, proxy: bool, dev: bool}` |
| `POST /auth/local/login` | `{username, password, returnTo?}` | 204 and sets the session cookie. Bad credentials or lockout: 401 with a generic message. CSRF or Origin failure: 403. Per-IP limit: 429. Invalid `returnTo`: 400. |
| `POST /auth/logout` | | 204 |
| `GET /auth/dev/login?user=&groups=&returnTo=` | dev builds only | 302 to `returnTo` or `/` |

With proxy auth, the first request that carries valid proxy headers creates a session.
There is no separate login step. A trusted proxy that sends an invalid or denied identity gets 401.

`GET /api/v1/me` →
```json
{
  "user": "local:alice", "display": "alice", "groups": ["eddy:platform","eddy:authenticated"],
  "provider": "local", "csrf": "…",
  "features": {"ai": true, "aiProvider": "bedrock", "mcp": true, "mcpWrites": true,
               "logs": true, "ephemeralStore": false, "devMode": false},
  "version": "v0.1.0"
}
```

## Clusters and resources

| Method and path | Response |
|---|---|
| `GET /api/v1/clusters` | `{items: ClusterInfo[]}`, counts filtered by RBAC |
| `GET /api/v1/clusters/{c}/resources?kind=&namespace=&status=&q=` | `{items: Resource[], resourceVersion}`, RBAC-filtered snapshot |
| `GET /api/v1/clusters/{c}/objects/{kind}/{ns}/{name}` | `Resource` |
| `GET …/objects/{kind}/{ns}/{name}/children` | `{items: Resource[]}` |
| `GET …/objects/{kind}/{ns}/{name}/yaml` | `{yaml}`, redacted, with a kind allowlist |
| `GET …/objects/{kind}/{ns}/{name}/events` | `{items: Event[]}` |
| `POST …/objects/{kind}/{ns}/{name}/reconcile` | body `{withSource?: bool, confirm?: string}` → 202 |
| `POST …/objects/{kind}/{ns}/{name}/suspend` | body `{confirm?: string}` → 202. Protected cluster without `confirm == cluster` gives 428. |
| `POST …/objects/{kind}/{ns}/{name}/resume` | body `{confirm?: string}` → 202 |
| `GET /api/v1/clusters/{c}/pods/{ns}/{name}/logs?container=&tail=&follow=` | SSE `log` events `{lines: string[]}`, then `end` |

## Live updates: `GET /api/v1/stream` (SSE)

| Event | Data |
|---|---|
| `hello` | `{}` |
| `clusters` | `{items: ClusterInfo[]}` when connection state or counts change (at most once a second) |
| `change` | `{cluster, upserts: Resource[], deletes: string[]}`, filtered per user |
| `resync` | `{cluster}`: the client refetches that cluster's resources |
| `thread` | `{threadId, ref}`: a thread the user can see changed |

The hub sends a comment line every 20 s as a keepalive. Clients reconnect with backoff.

## Threads

A thread attaches to a resource (`ref` = `{cluster, group, kind, namespace, name}`) or to
a whole cluster (`kind: ""`).

- You can see a thread only if you can `get` its target, or it is a private thread you created.
- To resolve a thread you must be its author or be allowed to `patch` the target.

| Method and path | Body / query | Response |
|---|---|---|
| `GET /api/v1/threads?cluster=&kind=&namespace=&name=&status=&type=&cursor=&limit=` | | `{items: Thread[], next}` |
| `POST /api/v1/threads` | `{ref, title, body, type?: "discussion"}` | `{thread, message}` |
| `GET /api/v1/threads/{id}` | | `{thread, messages: Message[], next}` |
| `POST /api/v1/threads/{id}/messages` | `{body}` | `Message` |
| `POST /api/v1/threads/{id}/resolve` | | `Thread` |
| `POST /api/v1/threads/{id}/reopen` | | `Thread` |
| `DELETE /api/v1/threads/{id}` | author only | 204 |

Bodies are plain text or Markdown. The UI renders them with no raw HTML and no images.

- **Size limits:** a message body is at most 64 KiB (8 KiB over MCP).
- **Paging:** threads default to 50 per page (maximum 200) and messages to 100 (maximum 500).
- **Status codes:** a thread you cannot see gives 404. A visible thread you may not resolve or delete gives 403.

## Ask AI

`POST /api/v1/ai/ask`
- **Body:** `{cluster, resourceId?, threadId?, question}`
- **Response:** `{threadId, message: Message, steps: [{tool, args, bytes}]}`

Each ask is stored in a private thread of type `ask`. The reply comes back in one piece,
with no streaming in v0.1.

## Personal access tokens

| Method and path | Body | Response |
|---|---|---|
| `GET /api/v1/tokens` | | `{items: [{id, name, scopes, createdAt, expiresAt, lastUsedAt}]}` |
| `POST /api/v1/tokens` | `{name, scopes: ["read"] or ["read","operate"], ttl: "720h" or "30d"}` | 201 `{token: "eddy_pat_…", item}`. The token is shown once. A TTL over the maximum gives 400; the per-user token limit gives 409. |
| `DELETE /api/v1/tokens/{id}` | | 204 |

## Audit

`GET /api/v1/audit?subject=&cluster=&cursor=&limit=`
- **Response:** `{items: AuditEvent[], next}`
- **Access:** you see only your own events. Users in any of `auth.auditViewerGroups`
  (for example `eddy:platform`) can query everyone's events.

## Health

- **`/healthz`** (all listeners): liveness.
- **`/readyz`** (metrics listener): ready once the store has migrated and the cluster registry has synced.
- **`/metrics`**: Prometheus metrics.

## MCP: `POST /mcp`

Streamable HTTP, stateless, JSON responses, `Authorization: Bearer <PAT>`. See ADR-0003 §5.

**Read tools:**

| Tool | Args | Notes |
|---|---|---|
| `list_clusters` | | |
| `list_resources` | `cluster?, kind?, namespace?, status?, query?, limit?` | Fleet-wide when `cluster` is empty |
| `list_unhealthy` | `cluster?` | Failed or suspended Flux objects across the fleet |
| `get_resource` | `cluster, kind, namespace, name, include_yaml?` | |
| `get_events` | `cluster, kind, namespace, name` | |
| `get_logs` | `cluster, namespace, pod, container?, tail?` | Only when `mcp.allowLogs` is set |
| `list_threads` | `cluster?, kind?, namespace?, name?, status?` | |
| `get_thread` | `id` | |

**Thread writes** need the `read` scope, or `operate` if `threadWriteScope: operate` is set:

| Tool | Args |
|---|---|
| `create_thread` | `cluster, kind?, namespace?, name?, title, body` |
| `reply_thread` | `id, body` |
| `resolve_thread` | `id` |

**Actions** need the `operate` scope and `mcp.writes`. On a protected cluster, `confirm_cluster` must equal the cluster name.

| Tool | Args |
|---|---|
| `reconcile` | `cluster, kind, namespace, name, with_source?, confirm_cluster?` |
| `suspend` | `cluster, kind, namespace, name, confirm_cluster?` |
| `resume` | `cluster, kind, namespace, name, confirm_cluster?` |
