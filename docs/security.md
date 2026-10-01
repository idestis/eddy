# Security model

This page summarises how Eddy protects your clusters, for people who run it and people who sign in to it. The full reasoning and threat model are in [ADR-0003](adr/0003-mvp-security.md). To report a vulnerability, see [SECURITY.md](../SECURITY.md).

## The short version

- **Agents only dial out.** Workload API servers are never exposed, and the hub holds no credentials for any workload cluster.
- **Kubernetes RBAC is the only permission model.** Every read and write runs as the signed-in person, impersonated in the cluster. Eddy cannot do more than the user could do with `kubectl`.
- **Agents have no write access of their own.**
- **No Secret or ConfigMap data ever leaves a cluster.** Agents send summaries only.
- **Ask AI is read-only,** and everything the AI or an MCP client sees is filtered by the asking user's RBAC and redacted first.

## Trust boundaries

```
  Browser / Claude Code / MCP client
     |  HTTPS (internal)   session cookie + CSRF      or      PAT bearer on /mcp only
     v
 +-------------------------------- management cluster ------------------------------+
 |  [ingress: internal] --> Hub :8080  UI, /api, /auth, /mcp                          |
 |                              |   store: PostgreSQL (sessions, PATs, threads, audit)|
 |                              |   reads: Cluster CRs + agent token Secrets (own ns) |
 |                          Hub :8443  /agent/v1/connect only  <-- separate Service   |
 +--------------------------------------------^---------------------------------------+
                                              |  WebSocket over TLS, agent dials OUT,
                                              |  bearer token bound to one cluster name
 +------------------ workload cluster --------+---------------------------------------+
 |  Agent (SA: read-only + impersonate + SAR)                                          |
 |     informers: Flux kinds, workloads, pods  -->  summaries only  -->  hub           |
 |     user requests: impersonated as  local:alice / eddy:platform  -->  API server    |
 |  The API server's RBAC decides, and its audit log records the real user.            |
 +-------------------------------------------------------------------------------------+
```

Crossing each boundary needs a separate credential: a session or PAT to reach the hub, an agent token to join as a cluster, and Kubernetes RBAC to do anything in a cluster.

## Impersonation

The agent acts on behalf of the user with the Kubernetes impersonation headers. So:

- The user is `local:<username>` (local auth) or the proxy identity. Groups are `eddy:<group>` plus `eddy:authenticated`.
- `system:*` users and groups are never impersonated, and groups must match `allowedGroupPrefixes` (default `eddy:`). The hub enforces `denyUserPrefixes` (`system:`, `eks:`, `kubernetes-admin`), and the agent checks again on its own.
- For the tightest setup, pin the agent's RBAC `impersonate` permission on groups to an exact list (`impersonation.groups` in the agent chart).
- Writes are not pre-checked by the hub. They run impersonated, so the API server is the authority. Reads are filtered with a per-user SubjectAccessReview cache (45 seconds), because all users share one cache per cluster. This covers thread lists, Ask AI and MCP results as well as the UI.
  - The cache asks the cluster-scope question `(group, resource, namespace "")` first. When it is allowed, every namespace of that kind is allowed; this is the check the API server makes for `kubectl get <kind> -A`, so nothing is over-granted. Users without cluster-wide access are checked per namespace.
  - Per namespace, the hub first asks the agent for the user's rules: one `SelfSubjectRulesReview` per (user, namespace), which the agent creates **as the impersonated user**, so it lists that user's permissions and never the agent's. The hub answers `get`, `list` and `watch` checks of namespaced kinds from the rules, with RBAC's exact matching (`*` verbs, groups and resources; `*/sub` and `res/sub` subresources; `resourceNames` grant `get` of those names only, never `list`). It falls back to a SubjectAccessReview for the namespace when the review is `incomplete` (a webhook or any authorizer that cannot list its rules, which includes every authorizer that can deny), has an `evaluationError`, failed or was truncated. Impersonation adds `system:authenticated`, which SubjectAccessReviews for the user do not carry, so an allow is taken from the rules only when the rules of a user with no bindings (`eddy:rules-baseline`, impersonated by the agent with no groups) do not allow the same check; otherwise a SubjectAccessReview decides. A deny from complete rules is exact. Rules reviews are cached for the same 45 seconds, in a cache bounded apart from the SubjectAccessReview answers. Cluster-scoped kinds always use SubjectAccessReviews.
  - A disconnected cluster's last view is served as stale (ADR-0006). No agent can answer access checks then, so a user's cached answer (a SubjectAccessReview or a rules review) stays usable for `auth.staleAccessTTL` after the agent disconnected: 2 minutes by default, at most 10, and `0` fails closed at once. Anything not cached, including a user whose groups changed, fails closed.
  - List ETags include the user and their exact groups and a 45 s time bucket, so a validator never crosses users and a permission change takes effect within 90 s. `GET` responses are gzipped only on routes that carry no secret (never `/auth/*`, `/api/v1/me`, `/api/v1/tokens*`, join tokens or logs), against BREACH.

A compromised hub cannot reach any cluster directly. The worst it can do is ask an agent to perform actions as a user it names, and the agent refuses privileged identities, so the damage is bounded by the RBAC of `eddy:` groups you granted.

## What leaves a cluster

Only a `Resource` summary per object: kind, name, namespace, status and conditions, revisions and inventory. No `data` or `stringData`, no Secrets or ConfigMaps (the agent does not even watch them). YAML and event reads go through a kind allowlist, and logs only when you allow them. A redactor masks credentials (tokens, keys, PEM blocks, URLs with passwords and high-entropy strings) before anything reaches Ask AI or MCP.

## Signing in

| Mode | What it is | Notes |
|---|---|---|
| GitHub | A GitHub App or OAuth App (github.com or Enterprise Server) | Authorization code flow with PKCE. `allowedOrganizations` is required, and only active memberships count. Orgs and teams become `github:<org>` and `github:<org>/<team>` groups |
| OpenID Connect | Google, Okta, Entra ID, Dex or any OIDC provider | PKCE, and the ID token's signature, `iss`, `aud`/`azp`, `exp` and nonce are verified. `allowedDomains` (Google: the `hd` claim), `allowedGroups`, and a verified email by default |
| Local users | Users and argon2id hashes in a Kubernetes Secret (bcrypt cost 12 or more is also accepted) | Rate-limited, generic errors, a dummy hash for unknown users. Disabling a user or changing a password ends their sessions and PATs. `mode: breakglass` hides the form behind `/login?local=1` and logs every use as a WARN |
| Proxy headers | oauth2-proxy (or similar) in front of the hub | Identity headers are trusted only from `trustedCIDRs` **and** with a matching shared secret. The recommended layout is a sidecar with the hub bound to localhost |
| Dev fake login | Only in `-tags dev` builds, with `EDDY_DEV_MODE=1` and a loopback listener | Not in release images. No Helm value exists for it |

For GitHub and OIDC, the `state`, nonce and PKCE verifier travel in a 10-minute `__Host-eddy_oauth` cookie that is HttpOnly, SameSite=Lax, and encrypted and authenticated (AES-GCM with a key derived from `auth.keyFile`). The cookie is single-use, so a callback the browser did not start fails. `returnTo` must be a local path. Failed callbacks are rate-limited per client address and audited with their reason, while the user sees a generic error. Provider access and ID tokens are used once and never stored or logged. Groups are captured at sign-in, so the absolute session timeout bounds their staleness. Identity providers never connect to the hub: the hub only makes outbound HTTPS calls to them ([docs/auth.md](auth.md)). SAML is planned.

Sessions are stored server-side behind a `__Host-` cookie (Secure, HttpOnly, SameSite=Lax), with an 8 hour idle and 24 hour absolute timeout. The session id rotates at every sign-in. Unsafe requests need a CSRF header and an Origin check.

## Personal access tokens (PATs)

PATs exist only for MCP clients such as Claude Code.

- Format `eddy_pat_…`, so secret scanners can find a leaked one. Shown once, stored only as an HMAC, revocable in the UI.
- Expiry is mandatory (30 days by default, at most 90 for local users and 30 for proxy, GitHub and OIDC users).
- Scope `read` (read tools and threads) or `operate` (adds reconcile, suspend, resume). A PAT can never do more than its owner's RBAC, because every call is still impersonated as the owner.
- A PAT works only as `Authorization: Bearer` on `/mcp`. Anywhere else it gets a 401.
- Operators can revoke everything for a person with `eddy-hub admin revoke --user <subject>`, or by disabling the local user.

## The MCP endpoint

`/mcp` is POST-only and bearer-only (no cookies). It rejects a foreign `Origin` or `Host` (DNS rebinding) and sends no CORS headers. It has size and rate limits (60 calls a minute per token, 10 writes a minute), and every call is audited with `via: mcp` and the token id. Results are redacted and marked as untrusted data. Write tools carry `destructiveHint`, so clients ask the human first. On a protected cluster a write also needs `confirm_cluster` equal to the cluster name, or is denied outright with `mcp.protectedClusters: deny`. See [mcp.md](mcp.md).

## Ask AI

- Read-only: the tools are `get_resource`, `get_events`, `search_resources` and (only if enabled) `get_logs`. No write tool exists.
- Every tool call runs as the asking user, through the same RBAC filtering as the UI.
- Cluster data is wrapped as untrusted content with a per-request random delimiter, and the model is told it cannot change anything. Answers render without images or raw HTML, and suggested commands are copy-only.
- Bedrock keeps data inside your AWS account and region, and uses IRSA instead of keys. The Anthropic API sends data to Anthropic, and the UI shows which provider is in use.
- Limits: at most 6 tool rounds, 24 KB per tool result, and 30 asks per user per hour by default.

## Kill switches

Edit the `eddy-runtime` ConfigMap (flags `aiEnabled`, `mcpEnabled`, `mcpWrites`, `mcpAllowLogs`). The hub checks them on every request and picks up changes within about 90 seconds, with no restart. A flag can only turn a feature off. See [install.md](install.md#8-kill-switches).

## Network exposure

- Two listeners and two Services: `:8080` (UI, API, MCP) and `:8443` (agents only). Each returns 404 for the other's routes. Both are `ClusterIP` by default.
- Expose the UI through an **internal** ingress only. The agent endpoint gets its own internal hostname and TLS. If it must be internet-facing, restrict it by source CIDR, because until mTLS arrives (planned for v1.1) the token is the main control.
- Agent tokens are 32 random bytes, bound to one cluster name, compared in constant time, and rotatable without downtime (`previousToken` in the Secret).
- Optional NetworkPolicies ship in both charts (hub ingress and egress, agent egress only).
- The UI is served with a strict CSP (no inline script), `frame-ancestors 'none'`, `nosniff` and `Referrer-Policy: no-referrer`.

## Audit

Every write, MCP call and AI step is logged as a JSON line with user, groups, `via` (`web`, `mcp` or `askai`), cluster, target and result. Ship the hub logs to your log store. The database copy is kept for 90 days by default. Because actions are impersonated, each cluster's own audit log also names the real user.

## Hardening checklist

- Set `publicURL` to the real https origin. Plain http is for local demos.
- Keep the UI and agent ingress internal and restrict source CIDRs.
- Encrypt the PostgreSQL storage and its backups.
- Grant `eddy:` groups only the RBAC people should have. Start from the agent chart's `userRBAC` values (or `deploy/rbac/eddy-user-rbac.yaml` without Helm), and use `operator` sparingly.
- Mark production clusters `protected: true`.
- Pin agent impersonation with `impersonation.groups` and enable agent NetworkPolicies.
- Prefer Bedrock with a geo-scoped inference profile and a guardrail when you use Ask AI.
- Keep `mcp.allowLogs` and `ai.allowLogs` off unless you need them.
