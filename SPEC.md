# Eddy: spec v1.0

> **v1.0 changes (2026-09-30).** Where this spec and the ADRs disagree, the ADRs win:
> - Sign-in is local users plus trusted-proxy headers ([ADR-0003](docs/adr/0003-mvp-security.md)). GitHub OAuth, OIDC and SAML move to v1.1.
> - Hub-owned data (sessions, PATs, threads, audit) lives in SQLite on a PVC ([ADR-0002](docs/adr/0002-hub-storage.md)). Cluster state stays in Kubernetes.
> - New in v1.0: `/mcp` for Claude Code and other MCP clients, review **threads** on resources, AWS Bedrock as an Ask AI provider, and runtime kill switches.
> - The API contract is [docs/api.md](docs/api.md).

A keyboard-first, multi-cluster web UI for FluxCD. Open source (Apache-2.0), Go backend, TanStack frontend. It is installed as a **hub** in one cluster and an **agent** in each workload cluster.

## Architecture

```
Browser (TanStack SPA)
   │  session cookie + CSRF header, SSE for live updates
   ▼
Hub (management cluster)          ── reads Cluster CRs, token Secrets
   ▲  WebSocket, agent dials OUT, token auth (mTLS later)
   │
Agent (every workload cluster)    ── informers on Flux + workloads, impersonated actions
```

- **Agents dial out.** Workload API servers are never exposed, and the hub holds no cluster credentials.
- **Kubernetes RBAC is the only permission model.** Eddy never invents roles. A signed-in person becomes a user and groups, and each cluster decides what they can do.
- **The hub never sees raw objects.** Agents send summaries only, so Secrets and ConfigMap data never leave a cluster.

## Go services

Single module `github.com/idestis/eddy` (placeholder org), two binaries, Go 1.26+ (client-go v0.37 requires it).

| Package | Purpose |
|---|---|
| `cmd/hub`, `cmd/agent` | Entrypoints |
| `internal/model` | `Resource` summary shared by agent, hub and UI |
| `internal/protocol` | Hub↔agent JSON frames |
| `internal/flux` | Flux kinds, version discovery, status summarizers, inventory parsing |
| `internal/agent` | Informers, store, WebSocket session, impersonated actions |
| `internal/hub` | HTTP API, SSE, agent registry, SAR-based filtering, audit, Ask AI |
| `internal/auth` | Local users, trusted-proxy headers, sessions, CSRF, PATs, group mapping (OIDC/SAML in v1.1) |
| `internal/store` | Hub storage: SQLite (default) and memory backends (ADR-0002) |
| `internal/threads` | RBAC-aware review threads on resources |
| `internal/mcp` | MCP server at `/mcp` (streamable HTTP, PAT bearer) |
| `internal/redact` | Secret-looking value redaction before AI and MCP output |
| `internal/fleet` | User-scoped fleet `Service` used by HTTP, MCP and AI |
| `internal/ai` | Provider interface (Anthropic Messages API, AWS Bedrock Converse) and a read-only tool loop |
| `internal/ui` | `go:embed` of the built SPA |
| `api/v1alpha1` | CRD types (`controller-gen` for deepcopy and CRD YAML) |

Main dependencies: `client-go`, `coder/websocket`, `modelcontextprotocol/go-sdk`, `aws-sdk-go-v2/service/bedrockruntime`, `anthropic-sdk-go`, `modernc.org/sqlite`, `x/crypto`, `sigs.k8s.io/yaml`. Flux kinds are read as unstructured objects through dynamic informers, so no Flux API modules are imported.

### Agent

- Discovers served Flux versions and watches them: Kustomization v1, HelmRelease v2, and the Git, OCI, Helm, HelmChart and Bucket sources.
- Also watches Deployments, StatefulSets, DaemonSets, ReplicaSets (for Pod→Deployment ownership only) and Pods.
- Strips `managedFields` from cached objects. Namespace scoping is optional.
- Status rules:
  - Flux objects use the `Ready` and `Reconciling` conditions, with `spec.suspend` taking priority.
  - Workloads use replica counts and the `ProgressDeadlineExceeded` condition.
  - Pods use their waiting reasons (CrashLoopBackOff, ImagePullBackOff and similar mean failed).
- Ownership comes from `ownerReferences`, the Flux labels `kustomize.toolkit.fluxcd.io/*` and `helm.toolkit.fluxcd.io/*`, and the Kustomization's `status.inventory` (entry ids look like `ns_name_group_Kind`).
- Sends a snapshot on connect, then deltas batched every 250 ms. Reconnects with jittered backoff capped at 30 s.
- Every user request runs through a client impersonating that user. The agent refuses `system:` users and groups, and any group outside the allowed prefixes.

### Protocol (WebSocket, JSON frames)

`{type, id, payload}` where type is one of `hello | snapshot | delta | request | response | stream | streamEnd | cancel`.

| Op | Effect |
|---|---|
| `reconcile {withSource}` | Merge-patches `reconcile.fluxcd.io/requestedAt`. With source, patches the source first; for a HelmRepository source that means the `HelmChart <ns>-<name>`. |
| `suspend`, `resume` | Patches `spec.suspend`. Resume also sets `requestedAt`. |
| `yaml`, `events` | Impersonated reads |
| `logs {container, follow, tail}` | Streamed; the hub sends `cancel` to stop |
| `access {checks[]}` | SubjectAccessReviews for the user, created by the agent's own ServiceAccount |

### Hub HTTP API

| Route | |
|---|---|
| `GET /api/v1/me` | Identity, CSRF token, feature flags |
| `GET /api/v1/clusters` | Clusters with connection state and health counts |
| `GET /api/v1/clusters/{c}/resources` | RBAC-filtered snapshot |
| `GET /api/v1/stream` | SSE: `change` (filtered deltas), `resync` |
| `GET …/objects/{kind}/{ns}/{name}/yaml` and `…/events` | Detail reads |
| `POST …/objects/{kind}/{ns}/{name}/reconcile`, `/suspend`, `/resume` | Actions. Suspend on a protected cluster requires `{"confirm":"<cluster>"}` server-side. |
| `GET /api/v1/clusters/{c}/pods/{ns}/{name}/logs` | Logs over SSE |
| `POST /api/v1/ai/ask` | `{cluster, resourceId, messages}` → `{text, steps}` |
| `GET /agent/v1/connect` | Agent WebSocket |
| `/auth/{provider}/login\|callback`, `POST /auth/logout` | Sign-in and sign-out |

- **Read filtering:** a `list` SAR per (group, resource, namespace), cached for 45 s per user. This is required because every user shares one informer cache per cluster.
- **Writes are not pre-checked.** They run impersonated, and the API server decides.
- **Audit:** JSON log lines with user, groups, cluster, action, target and result. Cluster audit logs also record the real user through impersonation.
- **Security headers:** strict CSP, `frame-ancestors 'none'`, `nosniff`.

## Auth (v1.1 target; v1.0 uses ADR-0003)

| Provider | Notes |
|---|---|
| GitHub (OAuth2 app) | Scopes `read:user user:email read:org`. Restrict with `allowedOrganizations`. Orgs become `github:<org>` and teams become `github:<org>/<team>`. Supports GHES URLs. |
| Google | OIDC preset (`accounts.google.com`, `allowedDomains` checked against the `hd` claim, verified email required). ID tokens carry no groups, so use `groups.static` or put Dex in front. |
| Generic OIDC | Okta, Entra, Keycloak, Dex, GitLab. Supports `groupsClaim`, `allowedDomains` and `allowedGroups`. |
| SAML 2.0 | `crewjam/saml`, with IdP metadata URL, SP key pair and email/groups attributes. Serves `/auth/saml/metadata` and `/acs`. |

- **Login flow:** state, nonce and PKCE are kept in a signed 10-minute cookie. `returnTo` accepts local paths only.
- **Session:** an HttpOnly, SameSite=Lax cookie holding a random id, stored server-side in memory for v0.1. That means a single replica; Redis comes later. Unsafe methods require the `X-Eddy-CSRF` header.
- **Group mapping:** every group gets the prefix `eddy:`, `system:*` groups are dropped, and optional `allUsers` and `static` groups are added. The Kubernetes user is the email, or `<provider>:<login>` when there is none.
- **Dev mode:** `dev.fakeLogin` enables `/auth/dev/login` for local UI work. It must never be enabled in production.

## CRD: `clusters.gitops.eddy.dev/v1alpha1` (cluster-scoped, short name `ecl`)

```yaml
apiVersion: gitops.eddy.dev/v1alpha1
kind: Cluster
metadata: {name: prod-eu}
spec:
  displayName: prod-eu
  environment: Production
  region: eu-central-1
  color: "#C2410C"        # ^#[0-9A-Fa-f]{6}$
  protected: true          # typed confirmation, striped ribbon
  order: 1
  agentTokenSecretRef: {name: eddy-agent-prod-eu, key: token}   # Secret in hub namespace
status:                    # written by hub (status subresource)
  phase: Connected         # Connected | Disconnected
  lastSeen: "2026-09-30T10:00:00Z"
  agentVersion: v1.0.0
  kubernetesVersion: v1.33.1
  fluxVersion: v2.7.0
  resources: 412
```

The hub compares the agent's token with the Secret using sha256 and a constant-time comparison. `gitops.eddy.dev` is a placeholder API group: pick a domain you own before v1.

## Helm charts

**`eddy-hub`** (management cluster)

| Value | Purpose |
|---|---|
| `publicURL` | Used for OAuth and SAML callbacks and printed agent URLs |
| `config.*` | Rendered to `hub.yaml`, with `${ENV}` expansion |
| `credentialsSecret` | Supplies env vars such as `GITHUB_CLIENT_SECRET` and `ANTHROPIC_API_KEY` |
| `samlCertSecret` | SAML service provider key pair |
| `sessionKeySecret` | Otherwise the chart generates a key and keeps it across upgrades via `lookup` |
| `clusters[]` | The chart creates each Cluster CR plus a random token Secret; NOTES prints the agent install command |
| `ingress.*` | Needs long proxy timeouts for WebSockets |
| `networkPolicy.*`, `replicaCount: 1` | |

- **RBAC:** `get/list/watch` on `clusters`, `patch` on `clusters/status`, and `get` on Secrets in its own namespace only.
- **Pod security:** runs as non-root with a read-only root filesystem, all capabilities dropped and the RuntimeDefault seccomp profile.

**`eddy-agent`** (each workload cluster)

| Value | Purpose |
|---|---|
| `cluster.name` | Must match the Cluster CR name in the hub |
| `hub.url` | The hub's agent endpoint |
| `hub.caBundle` | Only if the hub's certificate isn't publicly trusted |
| `token.existingSecret` or `token.value` | Agent token |
| `watch.namespaces` | Empty watches the whole cluster |
| `impersonation.allowedGroupPrefixes` (`["eddy:"]`) | Groups the agent may impersonate |
| `impersonation.groups` | Optional RBAC `resourceNames` pin, the strictest option |
| `networkPolicy.*` | Egress-only policy |

- **RBAC:** read-only on the Flux groups, apps workloads, ReplicaSets and Pods. It can `create` SubjectAccessReviews and `impersonate` users and groups. It has **no write access of its own**.

**User RBAC example**, applied in each workload cluster:

- `eddy-viewer` (get/list/watch on Flux, workloads, pods, `pods/log`, events) is bound to `eddy:authenticated`.
- `eddy-operator` (adds `patch` on Flux kinds) is bound to `eddy:github:acme/platform`.

## Ask AI

- **Runs server-side** in the hub through a provider interface: the Anthropic Messages API (default model `claude-haiku-4-5-20251001`) or AWS Bedrock Converse (IRSA/Pod Identity credentials, an inference profile id, optional Bedrock Guardrails passthrough).
- **Read-only.** No write tool exists. Tool results are redacted, wrapped as untrusted data, and stored in a private `ask` thread.
- **Tools:** `get_resource`, `get_events`, `search_resources`, and `get_logs` (off unless `ai.allowLogs` is set).
- **Scope:** every tool reads only the asking user's RBAC-filtered view.
- **Limits:** at most 6 rounds and 24 KB per tool result.
- **Prompt:** lead with the answer in under 130 words, use exact versions, suggest `flux` commands, and never invent data.

## Frontend (TanStack)

- **Libraries:** Vite, React, TanStack Router and Query, TanStack Virtual, `cmdk` for the palette, `tinykeys` for keybindings.
- **Live data:** SSE deltas are applied through `queryClient.setQueryData`. A `resync` event invalidates the cached query.
- **Deep links:** `/c/$cluster`, `/c/$cluster/r/$kind/$ns/$name?filter=&view=`.
- **Design reference:** the prototype in `docs/prototype/` covers keymaps, layout, the cluster identity (gradient frame, prod stripes), the detail panel and Ask AI.

## README.md outline

1. Pitch and screenshot
2. Features
3. Architecture diagram
4. Quickstart on kind: install Flux, then `helm install eddy-hub` with `clusters[]`, then install the agent with the printed token
5. Auth recipes: GitHub, Google, Okta/Entra through OIDC, SAML
6. RBAC recipe
7. Security model (see the principles above)
8. Development: `make dev-hub` with `hub.dev.yaml` and fake login, `make dev-agent` against kind, `npm run dev` proxying to `:8080`
9. Roadmap
10. Contributing and license

## Contributor notes

Conventions and security invariants live in [CLAUDE.md](CLAUDE.md).

## Roadmap

1. **v1.0:** the scope in this spec as amended by ADR-0003, ADR-0004 (PostgreSQL, active/active hubs, agent replicas) and ADR-0005 (the Add cluster wizard): local and proxy auth, SQLite store, threads, MCP, Anthropic and Bedrock
2. **v1.1:** GitHub OAuth2, OIDC and SAML sign-in; MCP OAuth; mTLS for agents
3. **v1.2:** diff view (`flux diff`), image automation kinds, notification-controller alerts in the UI
4. **Later:** streaming Ask AI, audit webhook, Backstage plugin
