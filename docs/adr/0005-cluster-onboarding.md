# ADR-0005: Cluster onboarding wizard and join tokens

- **Status:** Accepted
- **Date:** 2026-09-30
- **Target:** v1.0.0, built with ADR-0004
- **Replaces:** the `eddy register` CLI on the roadmap

## Context

Today a cluster must be declared in the hub's Helm values (`clusters[]`) before its agent
can connect. Onboarding takes two Helm steps and a hand-copied long-lived token, and a
failed connection is silent in the UI: only the logs show it. The owner wants the whole
flow in the UI for v1.0: **add cluster → get the installation guide → watch it connect →
finish**.

## Decision

### Flow

1. **Add cluster.** On the fleet page, "Add cluster" takes name, display name,
   environment, region, color, protected and order. The hub creates the `Cluster` CR with
   `status.phase: Pending` and a **one-time join token**.
2. **Install guide.** The connect screen shows the install command for this cluster,
   prefilled with `cluster.name`, the hub's agent URL (`agentsPublicURL`) and the join
   token. It also offers a `values.yaml` and plain manifests for GitOps-managed clusters,
   plus network guidance (outbound 443 to the agent endpoint, PrivateLink or peering).
3. **Join.** The agent connects with `Authorization: Bearer eddy_join_…`. The hub
   validates it, marks it used, mints the permanent agent token into the cluster's token
   Secret, and sends the token to the agent in a new `credentials` frame. The agent stores
   it in its own token Secret, then reconnects with the permanent token. The join token is
   now worthless.
4. **Checklist.** The connect screen updates live over SSE, so the operator watches each
   step as it happens. Afterwards the same checks and the guide stay available as a
   "Connection" panel on the cluster card, for reinstalls, rotation and later
   disconnects. The checks:
   - The agent connected.
   - The protocol version is compatible.
   - Flux was detected, with its version and served kinds.
   - Informers have synced, with a resource count.
   - SubjectAccessReview works.
   - Impersonation is pinned: the agent asks itself whether it may impersonate
     `system:masters`, and a "yes" is a warning with a link to the fix.
   - `watch.namespaces` is shown.
   - **Rejected attempts** for this cluster name are listed with their reason: bad or
     expired token, join token already used, wrong cluster name, protocol mismatch. The
     hub keeps the last 20 per cluster, and only for clusters that exist, so unknown names
     cannot fill memory.
5. **Finish.** "Finish" closes the wizard. The cluster is Connected and appears on the
   fleet page.

Clusters declared in Git (the chart's `clusters[]` or plain CRs) keep working exactly as
before. The wizard writes the same CR. Existing clusters can generate a join token too, to
reinstall an agent or rotate its token.

### Join tokens

- **Format:** `eddy_join_<id:12><secret:32><crc:6>` in base62, the same scheme as PATs, so
  secret scanners catch it.
- **Storage:** an HMAC with the pepper goes in the Postgres table `join_tokens(id, cluster,
  hash, created_by, created_at, expires_at, used_at)`. The table is LOGGED so an audit
  trail survives.
- **Validity:** one use only. The default TTL is 1 h, configurable up to 24 h. Creating a
  new join token for a cluster revokes its unused predecessor.
- **Scope:** a join token can only claim the cluster it was issued for.
- **Audit:** `cluster.create`, `cluster.join_token`, `cluster.joined` and `cluster.delete`
  are recorded with the user, `via` and the result.
- **GitOps:** committing a join token to Git is acceptable, because it is short-lived and
  single-use. The guide still recommends a SOPS or External Secrets reference.

### Who may do it (no Eddy roles)

Permissions follow the management cluster's RBAC. The hub asks a SubjectAccessReview, as
the signed-in user, in the **management** cluster:
- `create clusters.gitops.eddy.dev` to add a cluster
- `update` to issue a join token for an existing cluster
- `delete` to remove one (with a typed confirmation on protected clusters)

The UI hides the actions a user may not perform. The recipe in `docs/install.md` binds
`eddy:platform` to an `eddy-cluster-admin` ClusterRole in the management cluster.

### RBAC and chart changes

- **Hub** (`onboarding.enabled`, default `true`):
  - Add `create`, `update` and `delete` on `clusters`.
  - Add `create`, `update` and `delete` on Secrets in the hub namespace only.
  - Add `create subjectaccessreviews` in the management cluster.

  With `onboarding.enabled: false`, the hub keeps today's read-only RBAC and the wizard
  is hidden.
- **Agent:**
  - `joinToken` value: the chart creates the token Secret from it.
  - A namespaced Role allows `get` and `update` on **that one Secret**, pinned by
    `resourceNames`. This is the agent's only write permission, and it cannot touch any
    other object.
  - Setting `token.existingSecret` for a pre-provisioned token still works, and then the
    Role is not created.

### API and protocol additions

- **HTTP API** (see `docs/api.md`):

  | Method and path | Purpose |
  |---|---|
  | `POST /api/v1/clusters` | Add a cluster; returns the cluster, the join token and the install guide |
  | `PATCH /api/v1/clusters/{c}` | Change display fields and `protected` |
  | `POST /api/v1/clusters/{c}/join-token` | Issue a join token for an existing cluster |
  | `GET /api/v1/clusters/{c}/connection` | Checklist and rejected attempts |
  | `DELETE /api/v1/clusters/{c}` | Remove a cluster |

  The install guide comes back in three forms: helm, values and manifests.
- **SSE:** a new event, `connection {cluster}`.
- **Protocol (additive):**
  - `Hello.diagnostics`: flux version, served kinds, namespaces, SAR ok, impersonation
    pinned, informer sync progress.
  - A hub→agent `credentials {token}` frame, answered with a `response`.
  - Agents that don't know the frame keep using a pre-provisioned token.

## Consequences

- Onboarding takes a single Helm command, no long-lived secret passes through a person,
  and the UI explains failures.
- The hub gains write RBAC on Clusters and Secrets in its own namespace. It is gated by
  `onboarding.enabled` and by the users' own RBAC in the management cluster.
- The agent gains exactly one write permission: its own token Secret.
- Clusters added in the UI live as CRs in the management cluster. GitOps shops can export
  them, since the guide shows the CR YAML, or keep declaring them in Git.

## Deferred

- Bulk import of kubeconfig contexts
- Automatic agent upgrades from the UI
- mTLS certificates issued at join time (v1.1, with agent mTLS)
