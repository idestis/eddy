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
  - `disconnected` 503: the cluster's agent is not connected. No stale data is served.
  - `unavailable` 503: the agent timed out or is busy. It is also returned when PostgreSQL is unreachable, for sign-in, rate-limited writes, Ask AI, and session checks after the 30 s session cache expires. Reads of cluster data keep working.
  - `disabled` 503
  - `internal` 500
- **`?group=`:** object, YAML and events requests always send the API group (`core` for the core group), because a kind name can exist in several groups. UI detail URLs carry it only for kinds outside the registry.
- **Resource path:** `{kind}/{ns}/{name}` uses the Flux or workload Kind (for example
  `Kustomization`). Cluster-scoped objects use `_` as the namespace. The API group is
  inferred from the kind table in `internal/flux/kinds.go`.
- **Pagination:** list endpoints accept `?cursor=&limit=` and return `{items, next}`.
- **Body limits:**
  - JSON bodies are capped at 64 KiB, thread bodies at 512 KiB and requests at 1 MiB.
  - Anything larger gets 413 with code `bad_request`.
  - A store limit, such as too many messages in a thread, gives 409 `conflict`.
- **Rate limits** are global across hub replicas: login, MCP calls, logs and writes, thread writes, and the Ask AI hourly quota. Concurrency caps are per replica: SSE streams, log streams, MCP in-flight requests and Ask AI concurrency.
- **Unknown routes:** an unknown `/api` route returns a 404 JSON body, or 401 when the caller is not signed in.

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
  "features": {"ai": true, "aiProvider": "bedrock" /* only when ai is on */, "aiLogs": false /* ai.allowLogs */, "mcp": true, "mcpWrites": true,
               "logs": true, "ephemeralStore": false, "devMode": false,
               "onboarding": true /* Cluster CRs watched and onboarding.enabled */},
  "version": "v1.0.0"
}
```

## Clusters and resources

| Method and path | Response |
|---|---|
| `GET /api/v1/clusters` | `{items: ClusterInfo[]}`, counts filtered by RBAC. Agents in dev local mode add `mode: "local"`, `readOnly` and `context`. Writes to a `readOnly` cluster return 403. |
| `GET /api/v1/clusters/{c}/resources?kind=&namespace=&status=&q=` | `{items: Resource[], resourceVersion}`, RBAC-filtered snapshot. `kind` may repeat or be comma-separated. A kind outside the table (for example `ConfigMap`) matches inventory-only rows by exact name; a malformed kind or an unknown status gives 400. `resourceVersion` is an opaque hub counter. Finished Jobs the agent hides are not in it; see [Jobs](#jobs-completed-status-hidden-jobs-and-findings). |
| `GET …/resources?kind=Job&includeHidden=1&namespace=&limit=&cursor=` | `{items, resourceVersion, hidden: {total, next?}}`. Without `cursor`: the listed Jobs plus the first page of hidden ones. With `cursor=hidden.next`: hidden Jobs only (`resourceVersion` is `""`). `limit` is 1–1000 (default 500) hidden Jobs per page. `includeHidden` without `kind=Job` gives 400. |
| `GET /api/v1/clusters/{c}/findings` | `{items: Finding[]}`, the cluster's findings the user may see. The same list is `ClusterInfo.findings`. |
| `GET /api/v1/clusters/{c}/kinds` | `{items: KindInfo[], projects: Project[], presets: string[]}` for navigation; see [Kinds and projects](#kinds-and-projects). |
| `GET /api/v1/clusters/{c}/objects/{kind}/{ns}/{name}?group=` | `Resource`. Every `…/objects/…` path takes an optional `group` (the API group; `core` for the core group) to pick between kinds of the same name in several groups. Without it the group comes from the kind table, or from the one visible inventory-only row that matches (several groups give 400). `ns` is `_` for cluster-scoped objects. |
| `GET …/objects/{kind}/{ns}/{name}/children` | `{items: Resource[]}`. Mainly for MCP. The UI builds trees from each summary's `owner`, which is filled from ownerReferences, Flux labels and inventory when known. |
| `GET …/objects/{kind}/{ns}/{name}/yaml` | `{yaml}`, sanitized by the agent and redacted by the hub; see [YAML and events](#yaml-and-events). Secrets: always 403. |
| `GET …/objects/{kind}/{ns}/{name}/events` | `{items: Event[]}`, read as the user. |
| `POST …/objects/{kind}/{ns}/{name}/reconcile` | body `{withSource?: bool, confirm?: string}` → 202 |
| `POST …/objects/{kind}/{ns}/{name}/suspend` | body `{confirm?: string}` → 202. Protected cluster without `confirm == cluster` gives 428. |
| `POST …/objects/{kind}/{ns}/{name}/resume` | body `{confirm?: string}` → 202 |
| `GET /api/v1/clusters/{c}/pods/{ns}/{name}/logs?container=&tail=&follow=` | SSE `log` events `{lines: string[]}`, then `end` with `{}` or `{error:{code,message}}` (including when a followed pod stops). `tail` defaults to 500 and must be 1–5000. `follow` is a boolean. At most 4 streams per user, beyond that 429. Works with the session cookie alone, since EventSource cannot send headers. Pod summaries list `containers` for the picker. |
| `GET /api/v1/clusters/{c}/workloads/{kind}/{ns}/{name}/logs?container=&pods=&allContainers=&tail=&since=&follow=` | SSE logs of every pod of a Deployment, StatefulSet, DaemonSet or Job; see [Workload logs](#workload-logs). |

### Resource fields and inventory-only rows

- **New `Resource` fields:** `ports` (Service), `hosts` (Ingress), `schedule` (CronJob) and `inventoryOnly`.
- **Inventory-only rows** are objects a Kustomization manages but Eddy does not watch. They carry kind, namespace and name only, with status `unknown`.
  - **Visibility:** you see them if you can list the parent Kustomization, and, for known kinds, also list the row's own kind in its namespace.
  - **Endpoints:** `GET …/objects/{kind}/…`, `/yaml` and `/events` work for them (pass `?group=` when the kind name is ambiguous). YAML and events are impersonated reads, so the user's RBAC decides; see [YAML and events](#yaml-and-events).
  - **Counts** exclude them.
- **Kinds added** to the table: Service, PersistentVolumeClaim (core), Ingress (networking.k8s.io), Job, CronJob (batch) and HorizontalPodAutoscaler (autoscaling).
- **Statuses:** `ready`, `failed`, `reconciling`, `suspended`, `unknown` and `completed`. `completed` is a finished run: a Job with the `Complete` condition or a Pod in phase `Succeeded`. It is healthy (not "needs attention") and has its own bucket in `ClusterInfo.counts`. `?status=completed` filters on it.
- **OCI HelmRepositories** (`spec.type: oci`) without a `Ready` condition are `ready` with the message "OCI repository · not reconciled by source-controller": since Flux 2.1 source-controller leaves them alone and helm-controller pulls their charts directly. Suspended ones stay `suspended`.

### YAML and events

- **Who may read:** any watched object, and any object named in a Kustomization inventory, once the
  user can see its row (the list rules above; otherwise 404). The agent then reads it
  **impersonating the user**; an API server `Forbidden` is returned as 403 `forbidden` with its message.
- **Kinds outside the table** are resolved by the agent through discovery (a cached RESTMapper,
  refreshed on a miss at most every 30 s). A kind the cluster does not serve is 404. Older agents
  answer 400 for kinds outside the table.
- **Secrets:** `/yaml` is always 403 (any group, any spelling, whether or not the object exists).
  `/events` is allowed.
- **Sanitising** (every kind): `managedFields` and the last-applied annotation are dropped; any
  top-level `data`, `binaryData` and `stringData` is removed, so a ConfigMap keeps only its
  metadata; container `env[].value` is `[REDACTED]` wherever a pod spec sits (`spec`,
  `spec.template.spec`, `spec.jobTemplate.spec.template.spec`); an EC2NodeClass's `spec.userData`
  and an External Secrets fake provider's values are redacted. The hub runs `redact.YAML` on the
  result as well.
- **Events** of cluster-scoped objects are read from the `default` namespace, where Kubernetes
  records them. Events whose involved object is in another API group are dropped.

### New kinds, presets and details

- **Always watched:** Namespace (Active → `ready`, Terminating → `reconciling`), StorageClass,
  PodDisruptionBudget, ServiceAccount and NetworkPolicy (all `ready`, with details). A
  PodDisruptionBudget with fewer healthy pods than required is `reconciling`; zero disruptions
  allowed is `ready` with the message "0 disruptions allowed · blocks voluntary evictions"; a
  `DisruptionAllowed=False` condition for a reason other than `InsufficientPods` is `failed`.
  `Replicas` is `currentHealthy/desiredHealthy`. ConfigMaps and Secrets stay inventory-only rows.
- **Watch presets** (agent `watch.presets` / `EDDY_WATCH_PRESETS`, opt-in; `task dev` enables both):
  - `karpenter`: NodePool, NodeClaim (`karpenter.sh`) and EC2NodeClass (`karpenter.k8s.aws`), all cluster-scoped.
  - `externalSecrets`: ExternalSecret, ClusterExternalSecret, SecretStore, ClusterSecretStore and PushSecret (`external-secrets.io`).
  A preset kind the agent does not watch (preset off) still appears as an inventory-only row.
  `ClusterInfo.presets` lists the agent's enabled presets.
- **Preset status rules:** `spec.suspend` → `suspended`; the first of the `Ready`, `Available`
  and `Synced` conditions decides (True → `ready`, False → `failed` with "Reason: message",
  Unknown → `reconciling`); a controller behind `metadata.generation` (status or condition
  `observedGeneration`) → `reconciling`. A NodeClaim follows Launched → Registered → Initialized
  → Ready; a False `Launched` is `failed`. A ClusterExternalSecret with failed namespaces is `failed`.
- **`Resource.details`** (new, optional): `[{label, value}]`, at most 12, one line each, in display
  order. For example NodePool `Nodes`, `CPU` ("12 / 1000"), `Memory`, `Node class`; NodeClaim
  `Instance type`, `Capacity type`, `Zone`, `Node`, `Node pool`; ExternalSecret `Target secret`
  (name only), `Store`, `Refresh interval`, `Last refresh`; StorageClass `Provisioner`,
  `Default class`, `Reclaim policy`, `Volume binding mode`.
- ExternalSecret and ClusterExternalSecret put the store in `source` (`{group: external-secrets.io,
  kind: SecretStore|ClusterSecretStore, …}`) and `spec.refreshInterval` in `interval`.
- **Labels:** Namespaces also keep `pod-security.kubernetes.io/*`; Karpenter kinds keep
  `karpenter.sh/nodepool`, `karpenter.sh/capacity-type`, `node.kubernetes.io/instance-type`,
  `topology.kubernetes.io/zone` and `karpenter.k8s.aws/instance-family`.

### Kinds and projects

Every `Resource` (inventory-only rows too) carries `project`: `kubernetes` for built-in groups
(core, apps, batch, networking.k8s.io, policy, storage.k8s.io, rbac.authorization.k8s.io,
autoscaling, …), `flux` for `*.fluxcd.io`, `karpenter` for `karpenter.sh` and `karpenter.k8s.aws`,
`external-secrets` for `external-secrets.io`, and the API group itself for any other group. The
hub sets it.

`GET /api/v1/clusters/{c}/kinds`:

```json
{
  "items": [
    {"group": "karpenter.sh", "kind": "NodePool", "plural": "nodepools", "namespaced": false,
     "project": "karpenter", "watched": true, "preset": "karpenter", "count": 3},
    {"group": "", "kind": "ConfigMap", "plural": "configmaps", "namespaced": true,
     "project": "kubernetes", "watched": false, "count": 12}
  ],
  "projects": [{"id": "kubernetes", "name": "Kubernetes"}, {"id": "karpenter", "name": "Karpenter"}],
  "presets": ["karpenter"]
}
```

- `items` holds every kind the agent watches (count 0 included) and every other kind with at
  least one row the user may see. `count` is RBAC-filtered like the resource list.
- `plural` is omitted when Eddy does not know it. `watched: false` means inventory-only rows.
- Items are sorted by project (Kubernetes, Flux, Karpenter, External Secrets, then others by id),
  then kind. `projects` lists the projects of `items` in that order.

### Jobs: completed status, hidden Jobs and findings

- **Job summaries:**
  - A completed Job has status `completed` and a message such as `Completed in 2m14s`; its age is `lastChanged`, the finish time.
  - A running Job is `reconciling`, `Running, 1 active`. A failed Job is `failed` with the reason.
  - `completions` is `"succeeded/completions"` (`"1/1"`) on every Job. `replicas` carries the same only while the Job has not finished, so a finished Job does not look like it has live pods.
- **Hidden Jobs:** clusters that never clean up finished Jobs can hold tens of thousands of them. The agent watches every Job but lists only:
  - Jobs that have not finished;
  - failed Jobs that finished within `EDDY_JOB_FAILED_MAX_AGE` (24h);
  - the newest `EDDY_JOB_HISTORY` (5) finished Jobs of each group, and at most 10× that per namespace.

  A group is, in this order: the controlling owner (a CronJob); a well-known label (`batch.kubernetes.io/cronjob-name`, `prefect.io/deployment-name`, `prefect.io/work-pool-name`, the release of a Helm hook Job, `app.kubernetes.io/instance` with `app.kubernetes.io/name`, `app.kubernetes.io/name`, `helm.sh/chart`); `metadata.generateName`; the name without a numeric or random suffix; the namespace. Every other finished Job is hidden: it is not in snapshots, deltas or counts, and `includeHidden=1` fetches it. A hidden Job's pods, if any, are still listed and still name it as their owner.
- **Findings** are not resources and never appear as rows. `ClusterInfo.findings` and `GET …/findings` carry them, filtered: a `job-buildup` finding is visible to whoever may list Jobs in its namespace.

  ```jsonc
  {
    "id": "job-buildup/prefect",
    "kind": "job-buildup",
    "severity": "warning",        // "warning" needs attention; "info" is context only
    "namespace": "prefect",
    "message": "15,083 finished Jobs in prefect (15,068 hidden), 15,083 without ttlSecondsAfterFinished; 84 failed standalone Jobs are never garbage-collected",
    "recommendation": "set ttlSecondsAfterFinished on the Job template (for Prefect, in the work pool's job variables) or add a cleanup policy",
    "jobs": {
      "hidden": 15068, "finished": 15083, "succeeded": 14999, "failed": 84,
      "withoutTTL": 15083, "standaloneFailed": 84,
      "oldest": "2025-11-02T04:00:00Z", "newest": "2026-09-30T11:58:00Z",
      "threshold": 100,
      "groups": [   // the largest five
        {"by": "label", "name": "prefect.io/deployment-name=application-domain-expire-applications-job",
         "label": "prefect deployment application-domain-expire-applications-job", "count": 15078, "failed": 84, "withoutTTL": 15078}
      ]
    }
  }
  ```

  There is one `job-buildup` finding per namespace with hidden Jobs. It is a `warning` once more than `EDDY_JOB_BUILDUP_THRESHOLD` (100) are hidden, and `info` (message `12 older finished Jobs hidden`, no recommendation) otherwise. `groups[].by` is `owner` (with `owner`), `label`, `generateName`, `prefix` or `namespace`. Counts cover every finished Job in the namespace except `hidden`. Warning findings count toward "needs attention"; the UI shows them on the cluster card and above the Jobs list. Findings change live: the SSE `clusters` event carries them.

### Workload logs

`GET /api/v1/clusters/{c}/workloads/{kind}/{ns}/{name}/logs` streams the logs of every current pod of a `Deployment`, `StatefulSet`, `DaemonSet` or `Job` (any other kind gives 400) as SSE. `features.workloadLogs` in `/me` says the hub has it; an older agent answers `end` with a 400 error.

| Query | Meaning |
|---|---|
| `container` | Only this container of each pod (pods without it are skipped). |
| `allContainers` | Default `true`: every container of each pod when `container` is empty; `false` streams the first container only. |
| `pods` | Comma-separated or repeated pod names, at most 20: only these pods of the workload. |
| `tail` | Lines per pod and container, 1–1000, default 100. |
| `since` | Start each stream this many seconds back (1 to 30 days). |
| `follow` | Keep streaming, pick up new pods and end the streams of deleted ones. |

Events:

| Event | Data |
|---|---|
| `pods` | `{pods: [{name, containers: string[], status, createdAt}], total, limit}`: the streamed pods, newest first. Sent first and again whenever the set (or a pod's status) changes. `total > limit` means only the newest `limit` pods are streamed (`EDDY_MAX_LOG_PODS`, default 20). |
| `log` | `{entries: [{pod, container, line, ts?, marker?}]}`. `ts` is the kubelet timestamp (RFC 3339) when the line had one, so the UI can merge pods in time order. |
| `end` | `{}`, or `{error: {code, message}}`. |

Entries with a `marker` are not log lines; `line` explains them:

- `forbidden`: the user may not read this pod's logs (`pods/log`). The pod is skipped; the marker is sent once.
- `ended`: the pod was deleted (`line: "pod deleted"`) or left the newest `limit` pods.
- `error`: a container stream failed (for example too many containers for one request).
- `dropped`: `pod` is empty; `line` says how many lines the stream's rate limit dropped (`EDDY_LOG_LINE_RATE`, default 2000 lines per second per stream, with a burst of two seconds).

Access and limits:

- The user must be able to get or list the workload. Each pod's logs are read impersonating the user, so `pods/log` RBAC applies per pod. Pods the user may neither list nor get are removed from `pods` and their lines are dropped at the hub.
- A workload stream counts as one of the user's 4 log streams on the hub, and as one of the agent's `EDDY_MAX_LOG_STREAMS`. Inside it the agent opens one API server stream per pod and container, at most 3 × `EDDY_MAX_LOG_PODS`.
- Lines are not redacted for the browser, exactly like pod logs. The stream works across hub replicas through the peer relay.

## Clusters: onboarding (ADR-0005)

- **Permissions:** each call is checked with a SubjectAccessReview as the user, in the **management** cluster, on `clusters.gitops.eddy.dev`.
- **Onboarding off:** when onboarding is disabled or clusters are static, write calls return 409 `conflict`.
- **Audit:** every change is recorded as `cluster.create`, `cluster.update`, `cluster.join_token`, `cluster.delete` or `cluster.joined`.

| Method and path | Body | Response |
|---|---|---|
| `GET /api/v1/clusters/permissions` | | `{onboarding, create}` |
| `POST /api/v1/clusters` | `{name (DNS-1123 ≤63), displayName?, environment?, region?, color? (#RRGGBB), protected?, order?, ttl? (5m–24h, default 1h)}` | 201 `{cluster: OnboardedCluster, joinToken: {token, expiresAt}, guide: InstallGuide}`. The token is shown once. |
| `PATCH /api/v1/clusters/{c}` | same fields, optional, plus `confirm` (required to turn `protected` off) | `{cluster}`. 409 for Helm-managed clusters, 428 without `confirm` |
| `POST /api/v1/clusters/{c}/join-token` | `{ttl?}` | 201 `{joinToken, guide}`. The unused predecessor is revoked. |
| `DELETE /api/v1/clusters/{c}` | `{confirm?}` | 204. 428 on a protected cluster without `confirm == c`, 409 for Helm-managed clusters |
| `GET /api/v1/clusters/{c}/connection` | | `{cluster, checks[], agents, joinToken?, attempts[] (newest 20), permissions, guide}` |

**Types and codes:**
- **Check ids:** `connected, protocol, flux, informers, sar, impersonation, namespaces, credentials`. Each check has a state: `ok`, `warn`, `fail`, `pending` or `info`.
- **Attempt reasons:** `bad_token, join_expired, join_used, wrong_cluster, protocol_mismatch, hello_rejected, credentials_failed`.
- **`OnboardedCluster`:** `{name, displayName, environment?, region?, color?, protected, order, phase: Pending|Connected|Disconnected, managedBy?: "helm"}`.
- **`InstallGuide`:** `{hubURL, namespace, helm, values, manifests, clusterResource, networkDocs, warnings?}`.

**Join flow on the agent endpoint:**
1. The agent connects with `Authorization: Bearer eddy_join_…`.
2. After the hello, the hub sends `credentials {token}`.
3. The agent stores the token and answers `response {stored, error?}`.
4. The hub closes the connection with status 1000.
5. The agent reconnects with the permanent token.

## Live updates: `GET /api/v1/stream` (SSE)

| Event | Data |
|---|---|
| `hello` | `{}` |
| `clusters` | `{items: ClusterInfo[]}` when connection state, counts or findings change (at most once a second) |
| `change` | `{cluster, upserts: Resource[], deletes: string[]}`, filtered per user |
| `resync` | `{cluster}`: the client refetches that cluster's resources |
| `thread` | `{threadId, ref}`: a thread the user can see changed |
| `connection` | `{cluster}`: that cluster's onboarding or connection state changed. Refetch `…/connection`. |

- **Keepalive:** the hub sends a comment line every 20 s. Clients treat 45 s without data as a dead stream and reconnect with backoff.
- **On connect:** a `clusters` event follows `hello` straight away.
- **Slow clients** get `resync` for every cluster instead of the missed deltas.
- **Disconnects:** when an agent disconnects, clients get `resync {cluster}`.
- **Limits:** at most 8 streams per user, beyond that 429.

## Threads

A thread attaches to a resource (`ref` = `{cluster, group, kind, namespace, name}`) or to
a whole cluster (`kind: ""`).

- You can see a thread only if you can `get` its target, or it is a private thread you created.
- To resolve a thread you must be its author or be allowed to `patch` the target.

| Method and path | Body / query | Response |
|---|---|---|
| `GET /api/v1/threads?cluster=&kind=&namespace=&name=&status=&type=&cursor=&limit=` | | `{items: Thread[], next}`. An empty `kind` means no filter. |
| `POST /api/v1/threads` | `{ref, title, body, type?: "discussion"}` | 201 `{thread, message}`. Any other `type` gives 400. |
| `GET /api/v1/threads/{id}` | | `{thread, messages: Message[], next}` |
| `POST /api/v1/threads/{id}/messages` | `{body}` | 201 `Message` |
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

Optional `attachments: [{kind: "logs", source, lines[]}]` carry log lines the user selected in the UI:
- **Limits:** at most 3 attachments, 500 lines and 32 KiB in total.
- **When accepted:** only when `ai.allowLogs` is on (see `features.aiLogs`). Otherwise the request gets 400.
- **Handling:** lines are redacted and passed to the model as untrusted data, never as part of the question. They are not stored in the thread.

Each ask is stored in a private thread of type `ask`. The AI message's `author.client` is the model id, and `meta.provider` names the provider. If you are over the per-user limit you get 429 `rate_limited`, and invalid input gives 400. The reply comes back in one piece,
with no streaming in v1.0.

## Personal access tokens

| Method and path | Body | Response |
|---|---|---|
| `GET /api/v1/tokens` | | `{items: [{id, name, scopes, createdAt, expiresAt, lastUsedAt}]}` |
| `POST /api/v1/tokens` | `{name, scopes: ["read"] or ["read","operate"], ttl: "720h" or "30d"}` | 201 `{token: "eddy_pat_…", item}`. The token is shown once. A TTL over the maximum gives 400; the per-user token limit gives 409. |
| `DELETE /api/v1/tokens/{id}` | | 204 |

## Preferences

Per-user UI preferences (for example cluster pins and visit history). They belong to the
signed-in user and are never shared.

| Method and path | Body | Response |
|---|---|---|
| `GET /api/v1/prefs` | | `{data: object}`. `data` is `{}` when nothing is stored. |
| `PUT /api/v1/prefs` | `{data: object}` | 200 `{data}`. Replaces the stored object. `data` must be a JSON object of at most 16 KiB, otherwise 400 (not an object) or 413. |

The endpoints follow the usual `/api` rules: session cookie only (PATs get 401) and CSRF on `PUT`.

## Audit

`GET /api/v1/audit?subject=&cluster=&cursor=&limit=`
- **Response:** `{items: AuditEvent[], next}`
- **Paging:** `limit` defaults to 50, maximum 200.
- **Access:** you see only your own events. A non-viewer passing another `subject` gets 403. Users in any of `auth.auditViewerGroups`
  (for example `eddy:platform`) can query everyone's events.

## Health

- **`/healthz`** (all listeners): liveness.
- **`/readyz`** (metrics listener): ready once the store has migrated and the cluster registry has synced.
- **`/metrics`**: Prometheus metrics.

## MCP: `POST /mcp`

Streamable HTTP, stateless, JSON responses, `Authorization: Bearer <PAT>`. See ADR-0003 §5.

Every tool result is wrapped in an envelope: `{untrusted_data: …, truncated: bool}`.
- The content comes from clusters and users. Clients must treat it as data, never as instructions.
- Results are redacted and capped at `mcp.maxResultBytes`.
- The `mcp.writes` and `mcp.allowLogs` flags are checked on each call, so a tool can stay listed even while it is switched off.

**Read tools:**

| Tool | Args | Notes |
|---|---|---|
| `list_clusters` | | |
| `list_resources` | `cluster?, kind?, namespace?, status?, query?, limit?, cursor?` | Fleet-wide when `cluster` is empty. Returns `next`; at most 200 items |
| `list_unhealthy` | `cluster?, kind?` | Failed or suspended Flux objects across the fleet, plus `disconnected` clusters |
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
