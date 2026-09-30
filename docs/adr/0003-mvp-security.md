# ADR-0003: MVP security architecture (auth, PATs, MCP, Ask AI, ingress)

- **Status:** Accepted · **Date:** 2026-09-30 · **Supersedes:** the Auth section of SPEC v0.1 for the MVP
- **Context:** SAML, OAuth2 and OIDC move to the next phase. The MVP targets installs behind an internal ingress. It adds PATs, an MCP endpoint and a multi-provider Ask AI. The SPEC's core invariants stay unchanged.

## 1. Decision summary

1. **MVP sign-in:** (a) **local users** from a Kubernetes Secret (argon2id, with bcrypt accepted), and (b) **trusted reverse-proxy headers** (oauth2-proxy, Pomerium). An operator can enable either one or both. (c) **Dev fake login** exists only in dev builds and runs only on loopback.
2. **Browser sessions** use a server-side session store and a `__Host-` cookie, with a CSRF header plus an Origin check.
3. **PATs** (`eddy_pat_…`) are for MCP clients only. They are HMAC-hashed at rest, stored in the hub store (ADR-0002), and carry the scope `read` or `operate`. Every PAT must expire.
4. **`/mcp`** is served on the hub over stateless Streamable HTTP with bearer PATs. Every call is impersonated, audited with `via: mcp`, rate limited and size capped.
5. **Ask AI** uses the Anthropic Messages API or Bedrock Converse. It is read-only and RBAC-filtered, redacts secrets, wraps all data as untrusted, and has a kill switch.
6. **Network exposure:** the UI, API and MCP are internal only. The agent endpoint gets its **own listener and Service** so it can be exposed separately.

## 2. Threat model

| Asset | Threat | Mitigation |
|---|---|---|
| Workload clusters | Hub compromise leads to cluster takeover | Agents dial out. The hub holds no kube credentials. The agent SA has no write RBAC, and every action is impersonated, so the API server decides. |
| Impersonation | Hub or agent asserts `system:masters` or another privileged user | Agent refuses `system:*` users and groups and any group outside `allowedGroupPrefixes` (`eddy:`). Optional `resourceNames` pin on the `impersonate` verb. The hub also denies `denyUserPrefixes`. |
| Identity headers | Spoofed `X-Forwarded-User` sent straight to the hub | Headers are honoured only if **both** the TCP peer (`RemoteAddr`, never XFF) is in `trustedCIDRs` **and** the shared-secret header matches in constant time. Otherwise they are stripped and logged. NetworkPolicy allows ingress only from the proxy. |
| Local passwords | Brute force, credential stuffing, DoS through hashing | argon2id. Per-username and per-IP limits with lockout backoff. A dummy hash for unknown users gives uniform timing. A hashing semaphore caps concurrent hashes. |
| Sessions | Theft, fixation, CSRF | `__Host-` cookie that is Secure, HttpOnly and SameSite=Lax. 256-bit id stored hashed. Id rotates on login. Idle and absolute timeouts. `X-Eddy-CSRF` header plus an Origin/`Sec-Fetch-Site` check on unsafe methods. |
| PATs | Leak via git, logs or shell history; replay after offboarding | `eddy_pat_` prefix with a checksum so scanners catch it. HMAC at rest, shown once, expiry required, revocable. Groups can only shrink after issue. Proxy users get a short max TTL. PATs are rejected outside `/mcp`. |
| `/mcp` | DNS rebinding or cross-site calls from a browser | No cookie auth on `/mcp`, bearer only. Reject any `Origin` not on the allowlist (403). Host must equal the `publicURL` host. No CORS headers. |
| `/mcp` writes | Prompt-injected agent suspends prod | `operate` scope, impersonation, `confirm_cluster` equal to the cluster name on protected clusters (or `deny`). `destructiveHint` makes clients ask the human. Write rate limit, audit, and a runtime `mcp.writes` kill switch. |
| Resource data | Secret/ConfigMap data leaves the cluster | Summaries only. Kind allowlist on `yaml`/`get_resource`. The agent never informs on Secret/ConfigMap. The redactor runs on YAML and logs before AI or MCP. |
| RBAC view | User A sees B's resources or threads through the shared cache | Every read, including thread lists and AI/MCP tool results, is filtered by the asking user's SAR cache (45 s). Threads are visible only if the resource is. |
| Ask AI | Prompt injection in logs, annotations or threads exfiltrates data | AI tools are read-only. Data is wrapped in nonce delimiters and the system prompt says to ignore instructions inside data. Rendered output shows no images and no raw HTML, and links need a click (blocks beacon exfil). Size and round caps. |
| AI provider | Data residency, key leakage | Bedrock keeps data in the account and region (prefer geo-scoped inference profiles). The Anthropic key is only an env var from a Secret and never logged. A per-provider disclosure appears in the UI. |
| Agent channel | Rogue agent impersonates another cluster; token theft | Token bound to the Cluster CR name. sha256 plus constant-time compare. Dual-token rotation. TLS required. Failed auth is rate limited. Hub re-validates on Secret change and drops stale sessions. |
| Agent data | Compromised cluster sends XSS payloads or huge frames | Frame size limit (1 MiB) and per-agent rate limits. The UI never uses `dangerouslySetInnerHTML`. Strict CSP. Resource data is displayed only, never executed. |
| SPA | XSS, clickjacking, leaky referrers | CSP with no inline script, `frame-ancestors 'none'`, `nosniff`, `Referrer-Policy: no-referrer`, COOP/CORP. |
| Audit trail | Actions without attribution | JSON audit line for every write and every MCP/AI call with user, groups, `via`, token id, cluster, target and result. K8s audit logs record the impersonated user. |
| Availability | SSE, log-stream or AI floods | Per-user concurrency caps on SSE, logs, AI and MCP. Server timeouts. Log tail limits. |

## 3. MVP authentication

### 3.1 Config (`hub.yaml`, rendered from Helm `config.*`)

```yaml
publicURL: https://eddy.internal.example.com   # Origin/Host checks and cookie scope derive from this
auth:
  local:
    enabled: true
    usersFile: /etc/eddy/users/users.yaml      # mounted from Secret eddy-users; hot-reloaded on change
    userPrefix: "local:"                       # k8s user = local:<username>
  proxy:
    enabled: false
    userHeader: X-Forwarded-Email              # single value; must be email or ^[a-zA-Z0-9._@-]{1,128}$
    groupsHeader: X-Forwarded-Groups           # optional
    groupsSeparator: ","
    trustedCIDRs: ["127.0.0.1/32"]             # TCP peer of the proxy; sidecar pattern recommended
    sharedSecret:
      header: X-Eddy-Proxy-Secret
      env: EDDY_PROXY_SECRET                   # from credentialsSecret; >= 32 random bytes
    insecureSkipSharedSecret: false            # if true: startup WARN + UI banner
    userPrefix: ""                             # k8s user = <prefix><header value>
  groups:
    prefix: "eddy:"                            # applied to every group, local and proxy
    allUsers: authenticated                    # -> eddy:authenticated
    static: {"alice@example.com": [oncall]}    # extra groups by identity
    maxGroups: 64                              # extra groups dropped + WARN
    maxGroupLength: 128
  denyUserPrefixes: ["system:", "eks:", "kubernetes-admin"]
  session:
    idleTimeout: 8h
    absoluteTimeout: 24h
    maxPerUser: 10
  loginRateLimit:
    perUsername: {failures: 5, window: 15m, lockout: 15m, backoff: exponential, maxLockout: 4h}
    perIP: {requests: 20, window: 1m}
    maxConcurrentHashes: 4                     # bounds argon2 memory (4 x 64 MiB)
dev:
  fakeLogin: false                             # see 3.4
```

`users.yaml` (Secret `eddy-users`, key `users.yaml`). Hashes come from `eddy hash-password`, which reads stdin.

```yaml
users:
  - username: alice                             # ^[a-z0-9][a-z0-9._-]{0,62}$
    passwordHash: "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>"
    groups: [platform, oncall]                  # -> eddy:platform, eddy:oncall
    disabled: false
```

### 3.2 Rules

- **Algorithms:** new hashes use argon2id with m=64 MiB, t=3, p=4 (RFC 9106 second recommendation). `$2a$`/`$2b$` bcrypt with cost 12 or more is accepted, so htpasswd users can migrate. Anything weaker is rejected when the file loads.
- **Group mapping:** trim each group and validate it against `^[A-Za-z0-9._:/@-]+$`. Drop anything starting with `system:` **before and after** prefixing, then add `groups.prefix`. Add `eddy:<allUsers>`, dedupe and cap. The agent rechecks the prefix rule independently.
- **Username mapping:** local users become `local:<username>`. Proxy users become `userPrefix + value`. Reject any result that matches `denyUserPrefixes` or contains `,` or control characters, and reject requests that carry several user headers. Groups are never derived from the username.
- **Proxy mode:** config validation fails without `trustedCIDRs`, and also without a shared secret unless `insecureSkipSharedSecret` is set. The recommended layout runs oauth2-proxy as a **sidecar**, with the hub UI listener bound to `127.0.0.1` and CIDR `127.0.0.1/32`. The proxy must strip client-supplied identity headers. The hub mints its own session on the first proxied request and **drops it whenever the header identity differs** from the session identity. It re-reads groups on every request.
- **Proxy bypass routes:** `/mcp` must be excluded from the proxy's auth (for example oauth2-proxy `skip_auth_routes=^/mcp$`) because MCP clients send only a PAT. The hub never honours proxy headers on `/mcp`.
- **Login:** `POST /auth/local/login` takes JSON `{username,password}` and requires the CSRF pre-session token from a `__Host-eddy_pre` cookie plus a header. The error message is always the same generic one. Successful and failed logins are both audited, with the username truncated.
- **Session cookie:** `__Host-eddy_session=<base64url(32B)>` with `Path=/; Secure; HttpOnly; SameSite=Lax`, a session-only cookie with no Max-Age. The server stores sha256(id) mapped to `{user, groups, csrf, created, lastSeen}`. The session id rotates on login. Logout deletes the entry server-side. Sessions live in the hub store (ADR-0002), so they survive restarts when persistence is enabled. Changing a local user's password or setting `disabled` kills that user's sessions and PATs.
- **CSRF:** unsafe methods on `/api` and `/auth` need `X-Eddy-CSRF` equal to the session token (`GET /api/v1/me`), compared in constant time. They also need `Origin` equal to the `publicURL` origin, or `Sec-Fetch-Site: same-origin` when Origin is absent. `returnTo` must be a local path: it starts with `/`, not `//` or `/\`.

### 3.3 Why not proxy-only or local-only
Local users work anywhere, including air-gapped clusters and kind. Proxy mode reuses a company IdP today without any OIDC code in Eddy. Both feed one mapping function, and OIDC and SAML will plug into it later.

### 3.4 Dev fake login
The route is compiled only with `-tags dev`, so release images do not contain it. On top of that it must be enabled with `dev.fakeLogin: true` **and** `EDDY_DEV_MODE=1`, and the listener must be loopback. If any of these is missing, the hub refuses to start. When active, it logs a WARN every minute, the UI shows a red banner, and `/api/v1/me` reports `devMode: true`. The Helm chart has no value for it.

## 4. Personal access tokens

- **Format:** `eddy_pat_<id:12><secret:32><crc:6>`, all base62. The secret has 190 bits of entropy. The CRC32 checksum lets scanners and the hub reject typos offline. Scanner regex: `eddy_pat_[0-9A-Za-z]{50}`. Register a GitHub secret-scanning custom pattern, and later join the partner program.
- **Storage:** the `api_tokens` table of the hub store (ADR-0002), keyed by the public `<id12>`. The `hash` column is `HMAC-SHA256(pepper, secret)`, where the pepper is derived from the hub key file (`auth.keyFile`, from `sessionKeySecret`). The row also holds owner, `groups` snapshot, `scopes`, `expires_at`, name, and `last_used_at` (written at most once every 5 minutes). The plaintext token is never stored. Lookup goes by id, then a constant-time compare of the HMAC. No extra Kubernetes RBAC is needed.
- **Scopes:** `read` covers read tools and thread writes. `operate` adds reconcile, suspend and resume. Neither scope can grant more than the owner's RBAC, because everything is still impersonated.
- **Expiry:** always required. Default 30d, `maxTTL` 90d for local users and **30d for proxy users**, since Eddy cannot see IdP deprovisioning. A user can hold at most 10 tokens.
- **Identity at use time:** the user is the owner. Local users' groups are resolved **live** from `users.yaml`, and a missing or disabled user makes the PAT invalid. Proxy users' groups are `groupsSnapshot ∩ lastSeenGroups`, where lastSeen comes from the user's most recent browser session. Groups can shrink after issue but never grow.
- **Lifecycle:** a user creates tokens in the UI (settings) and the plaintext is shown once with a copy button. Users list and revoke their own tokens. Operators revoke all of a user's tokens and sessions with `eddy-hub admin revoke --user <subject>` (runs against the store), or by disabling the local user, which invalidates their tokens on the next request.
- **Where accepted:** only `Authorization: Bearer` on `/mcp`. Query strings and cookies are never accepted. On `/api` a PAT gets 401. Tokens and their HMACs never appear in logs; audit records `tokenId` only.

## 5. MCP endpoint (`/mcp`)

- **Stack:** `github.com/modelcontextprotocol/go-sdk` v1.x, pinned at **≥ v1.7**. That version supports spec **2026-07-28** and 2025-11-25, and v1.4 or later includes the DNS-rebinding fix for CVE-2026-34742. The hub uses `mcp.NewStreamableHTTPHandler(..., &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})` wrapped in `auth.RequireBearerToken(verifier, &auth.RequireBearerTokenOptions{Scopes: []string{"read"}})`. The verifier returns `TokenInfo{UserID, Scopes, Expiration}`. Handlers call `auth.TokenInfoFromContext`.
- **Transport rules (2026-07-28):** POST only, so GET and DELETE get 405. There are no protocol sessions: the hub ignores `Mcp-Session-Id` and `Last-Event-ID`. `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` must match the body, or the response is 400 with `-32020 HeaderMismatch`. Unknown versions get a 400 listing the supported ones. The 2025-11-25 clients that do the `initialize` handshake are also served, because Claude Code and others still use it.
- **Origin and DNS rebinding:** the SDK's localhost protection does not apply to a hub behind an ingress, so Eddy adds its own middleware. It returns 403 if an `Origin` is present and not in `mcp.allowedOrigins` (default empty, since CLI clients send none). It also rejects requests whose `Host` is not the `publicURL` host. `/mcp` sends no CORS headers and answers no preflight.
- **Limits:**
  - Request body at most 256 KiB.
  - Per token: 60 calls/min and 4 concurrent. Write tools: 10/min per user. `get_logs`: 5/min.
  - Per-tool timeout 15 s. Tool result at most 64 KiB, truncated with `truncated: true`.
  - `list_resources` returns at most 200 items with an opaque cursor that is bound to the user.
- **Content:** YAML and logs go through the same redactor as AI (§6). Text results that contain cluster or thread content are wrapped as `{"untrusted_data": …}`. Tool descriptions say the content is data and never instructions.
- **Audit:** one line per `tools/call` with `ts, via:"mcp", user, groups, tokenId, clientInfo`. `clientInfo` is untrusted and is logged only. The line also records `tool, cluster, target, args` (redacted, at most 1 KiB), `result, bytes, durationMs`.

| Tool | Scope | readOnlyHint | destructiveHint | idempotentHint | Notes |
|---|---|---|---|---|---|
| `list_clusters` | read | true | — | true | Connection state and health counts |
| `list_resources` | read | true | — | true | `cluster?, kind?, namespace?, status?, query?, cursor?`. SAR-filtered |
| `get_resource` | read | true | — | true | Summary plus redacted YAML. Kind allowlist; Secret and ConfigMap are never available |
| `get_events` | read | true | — | true | At most 100 events |
| `get_logs` | read | true | — | true | **Off unless `mcp.allowLogs`**. `tail` at most 500 lines, no follow. Needs the `pods/log` RBAC |
| `list_threads`, `get_thread` | read | true | — | true | Only threads on resources the user can currently `get` |
| `create_thread`, `reply_thread` | read* | false | false | false | Author is the owner, badge `via mcp`. At most 8 KiB. 10/min. Plain text only |
| `resolve_thread` | read* | false | false | true | Only the author or anyone with `patch` on the resource |
| `reconcile` | operate | false | false | true | `withSource?` |
| `resume` | operate | false | false | true | |
| `suspend` | operate | false | **true** | true | |

\* Set `mcp.threads.writeScope: operate` to require `operate` for thread writes. All tools set `openWorldHint: false`.

- **Protected clusters:** every write tool that targets a protected cluster needs `confirm_cluster` equal to the cluster name, checked server-side. Otherwise it returns an error that tells the model to ask the human. `mcp.protectedClusters: confirm | deny` defaults to `confirm`, and `deny` blocks MCP writes to protected clusters entirely. `confirm_cluster` is only a speed bump. The real guardrails are the `destructiveHint`-driven client approval prompt, RBAC and the audit log.

## 6. Ask AI

```yaml
ai:
  enabled: true
  provider: bedrock                       # anthropic | bedrock
  anthropic: {model: claude-haiku-4-5-20251001, apiKeyEnv: ANTHROPIC_API_KEY, baseURL: ""}
  bedrock:
    region: eu-central-1
    modelId: eu.anthropic.claude-haiku-4-5-20251001-v1:0   # inference profile id or ARN
    guardrail: {id: "", version: "", trace: disabled}      # passthrough to Converse GuardrailConfig
  allowLogs: false
  limits: {maxRounds: 6, maxToolResultBytes: 24576, maxContextBytes: 131072, maxOutputTokens: 1024,
           timeout: 60s, perUser: {asks: 30, window: 1h, concurrent: 1}, globalDailyAsks: 2000}
```

- **Providers:** the Anthropic provider uses the Messages API, with the key read from `credentialsSecret` env. Bedrock uses `aws-sdk-go-v2/service/bedrockruntime` `Client.Converse` with `ConverseInput{ModelId, System, Messages, ToolConfig, InferenceConfig, GuardrailConfig: &types.GuardrailConfiguration{GuardrailIdentifier, GuardrailVersion, Trace}}`. Credentials come from the default chain: **IRSA** (`eks.amazonaws.com/role-arn` on the SA) or **EKS Pod Identity**, never static keys. The IAM policy allows only `bedrock:InvokeModel` on the named inference-profile and foundation-model ARNs, plus `bedrock:ApplyGuardrail` on the guardrail ARN if one is set.
- **Read-only:** the only tools are `get_resource`, `get_events`, `search_resources` and `get_logs` (gated by `allowLogs`). No write tool exists in the tool list or the dispatcher. The dispatcher rejects unknown tool names, and a test asserts that the list is read-only.
- **RBAC:** every tool call runs as the asking user through the same Authorizer and impersonated agent reads as the UI. The model cannot choose a cluster or resource that the user cannot see.
- **Redaction:** runs before anything reaches the model, and before MCP output too.
  - Drop `data` and `stringData`, `env[].value` (the names stay), `last-applied-configuration`, and `helm.sh/release` payloads.
  - Mask values that match JWTs, `AKIA`/`ASIA` keys, PEM blocks, `eddy_pat_`, `ghp_`/`github_pat_`, `xox[abp]-`, `sk-ant-`, bearer or basic auth, URLs with userinfo, `password|secret|token|apikey=…`, and high-entropy strings of 32 characters or more.
  - Each mask is `[REDACTED:<kind>]` and a count goes to the audit log.
- **Prompt injection:** each tool result is wrapped as `<eddy_data nonce="N" source="…">…</eddy_data nonce="N">`, where N is random per request and any occurrence of the delimiter in the content is escaped. The system prompt says that content inside `eddy_data` is untrusted data from the cluster, that it must never be followed as instructions, and that the model has no ability to change anything. Thread text is treated the same way.
- **Output:** answers render as sanitized Markdown. Raw HTML and images are disabled. Links work only for `http(s)` and must be clicked (no prefetch). Suggested `flux`/`kubectl` commands are shown as copyable text and never run.
- **Kill switch:** a runtime flags ConfigMap `eddy-runtime` is mounted as a volume and hot-reloaded, so a change takes effect within about 90 s with no extra RBAC. It has `ai.enabled`, `mcp.enabled`, `mcp.writes` and `mcp.allowLogs`. Flipping `ai.enabled` returns 503 and hides the UI button.
- **Audit:** `via: ai` with user, provider, model, rounds, tools called, bytes sent, redaction count, token usage and result. Prompt text is not logged by default (`ai.auditPrompts: false`).
- **Data residency:** Bedrock processes data inside the configured AWS account and region, and the provider does not use it for training. Geo inference profiles (`eu.`, `us.`, `apac.`) can route to other regions **inside that geography**, while `global.` profiles can route anywhere, so regulated installs should pin a geo or single-region model. The Anthropic API sends data to Anthropic, so the UI shows which provider handles the data.

## 7. Network exposure

- **Two listeners:** `:8080` serves the UI, `/api`, `/auth` and `/mcp`. `:8443` (Service `eddy-hub-agents`) serves only `/agent/v1/connect` and `/healthz`. Each listener 404s the other's routes. By default both Services are `ClusterIP`.
- **UI ingress is internal only.**
  - nginx: `ingressClassName: nginx-internal`, `nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"`, `proxy-buffering: "off"` for SSE.
  - AWS ALB: `alb.ingress.kubernetes.io/scheme: internal`, `target-type: ip`, `load-balancer-attributes: idle_timeout.timeout_seconds=3600`, `listen-ports: '[{"HTTPS":443}]'`, `ssl-redirect: "443"`.
  - Apply `inbound-cidrs` or security groups for the corporate network.
- **Agent endpoint:** workload clusters in other VPCs or accounts reach it through a separate internal NLB or ALB (VPC peering, Transit Gateway or PrivateLink), a separate hostname, and TLS. This path bypasses the auth proxy. It carries long idle timeouts and a WebSocket upgrade. If it must be internet-facing, restrict it by source CIDR, because the token is then the only control until mTLS arrives in v0.2. The hub refuses agents over plain HTTP unless it is in dev mode.
- **NetworkPolicy:** the UI port accepts traffic only from the ingress or proxy namespace. The agent port accepts traffic only from the agent ingress. Egress goes to the kube API, DNS, the AI provider endpoint and the IdP proxy.

## 8. Agent token auth

The token is 32 random bytes, base64url-encoded, and sent as `Authorization: Bearer` over TLS. The hub resolves the Cluster CR from the requested name and computes `subtle.ConstantTimeCompare(sha256(presented), sha256(secret.token))`. It also accepts `secret.previousToken` if that key exists. **Rotation:** write the new `token` and move the old one to `previousToken`. Roll the agent (`token.existingSecret`), then delete `previousToken`. The hub watches the Secret and closes any session whose token is no longer valid. The token only lets an agent register as that CR's name. Failed agent auth is limited to 10/min per IP. Tokens are never logged.

## 9. HTTP security headers (production Vite build)

```
Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:;
  font-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none';
  form-action 'self'; frame-ancestors 'none'; object-src 'none'; upgrade-insecure-requests
Strict-Transport-Security: max-age=31536000; includeSubDomains
X-Content-Type-Options: nosniff        X-Frame-Options: DENY          Referrer-Policy: no-referrer
Cross-Origin-Opener-Policy: same-origin     Cross-Origin-Resource-Policy: same-origin
Permissions-Policy: camera=(), microphone=(), geolocation=(), usb=(), payment=(), clipboard-read=()
Cache-Control: no-store                 (on /api, /auth, /mcp, index.html; hashed /assets/* are immutable, 1y)
```

- **Vite build and CSP:**
  - Vite emits only external `<script type="module" src>`, so no `unsafe-inline` is needed.
  - Set `build.assetsInlineLimit` to allow only images (fonts are self-hosted).
  - Forbid libraries that inject `<style>` tags. React `style={}` goes through CSSOM and is fine.
  - No Google Fonts or other CDNs.
  - Dev (`npm run dev`) runs without these headers. Only the Go server sets them.
- **Trusted Types:** add `require-trusted-types-for 'script'` as Report-Only in v0.1 and enforce it in v0.2.
- **CORS:** none anywhere. The SPA is same-origin.

## 10. Explicitly deferred

- **Sign-in:** SAML 2.0, GitHub OAuth2, OIDC (Google, Okta, Entra, Dex). MCP OAuth 2.1 authorization (protected-resource metadata), so MCP clients can drop PATs.
- **Agents:** mTLS for the agent channel (v0.2).
- **Scale:** a Postgres store backend and a multi-replica hub (HA). Until then, one replica with SQLite on a PVC.
- **Local users:** WebAuthn/TOTP.
- **PATs:** per-cluster or per-namespace scoping. GitHub secret-scanning partner registration.
- **AI and MCP:** AI write tools of any kind. Streaming AI. MCP `subscriptions/listen` and log follow.
- **Audit and hardening:** audit webhook/SIEM sink. Enforced Trusted Types.

## 11. Guardrails checklist (paste into CLAUDE.md)

```md
Security invariants (never break):
- Agents only dial out; hub holds no workload kube credentials; agent SA has no write RBAC.
- Every cluster read/write runs impersonated as the requesting user (browser, PAT/MCP, AI alike).
- Never impersonate system:* users/groups; groups are prefixed eddy: and must match allowedGroupPrefixes; denyUserPrefixes enforced.
- Never cache/send Secret or ConfigMap data; summaries only; YAML kind allowlist; redactor runs before AI and MCP output.
- Hub filters every read (incl. threads, AI and MCP tool results) through the Authorizer SAR cache.
- Protected clusters: server-side typed confirmation (UI confirm, MCP confirm_cluster) for writes.
- Proxy identity headers honoured only from trustedCIDRs (TCP peer) AND matching shared secret; else stripped.
- Passwords: argon2id (bcrypt>=12 accepted); rate-limit logins; generic errors; dummy hash for unknown users.
- Session cookie __Host-, Secure, HttpOnly, SameSite=Lax; id rotated on login; CSRF header + Origin check on unsafe methods; returnTo local path only.
- PATs: eddy_pat_ format, HMAC at rest, shown once, expiry mandatory, bearer header only, /mcp only, never logged.
- /mcp: POST only, bearer only (no cookies), reject foreign Origin/Host, no CORS; size/rate caps; audit via:mcp.
- AI is read-only: no write tools exist; data wrapped as untrusted with nonce delimiters; no images/raw HTML in output.
- Kill switches (eddy-runtime ConfigMap: ai.enabled, mcp.enabled, mcp.writes) must be checked per request.
- Dev fake login only with -tags dev + EDDY_DEV_MODE=1 + loopback; never in release images or Helm.
- Tokens (agent, PAT, API keys, proxy secret) compared in constant time and never logged.
- Strict CSP (no inline script), frame-ancestors 'none', nosniff; never use dangerouslySetInnerHTML.
```

## Verified facts (2026-09-30)
- MCP spec **2026-07-28** Streamable HTTP: servers MUST validate `Origin` and return 403 if it is invalid, SHOULD authenticate, and SHOULD bind to localhost when local. GET streams and `Mcp-Session-Id` sessions are removed. `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` headers MUST match the body (`-32020`). https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http
- Go SDK is stable v1.x, and v1.7+ supports 2026-07-28 through 2024-11-05. It has `mcp.NewStreamableHTTPHandler`, `StreamableHTTPOptions{Stateless, JSONResponse}`, `ToolAnnotations{ReadOnlyHint, DestructiveHint, IdempotentHint, OpenWorldHint}`, and `auth.RequireBearerToken` / `TokenInfo{Scopes, Expiration, UserID}`. DNS-rebinding protection is on by default for localhost only since v1.4.0 (CVE-2026-34742). https://github.com/modelcontextprotocol/go-sdk · https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/auth
- Bedrock Converse takes `GuardrailConfiguration{guardrailIdentifier, guardrailVersion, trace}`, and ConverseStream uses `GuardrailStreamConfiguration`. https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails-use-converse-api.html

## Reconciliation with ADR-0002 (main session, 2026-09-30)

- PATs, sessions, threads and audit all live in the hub store (SQLite). There is no AccessToken CRD.
- Token scopes are `read` and `operate` only. Thread writes need `read` unless `auth.tokens.threadWriteScope: operate`.
- Session timeouts are 8h idle and 24h absolute (this ADR wins over ADR-0002).
- Token and session hashes: sessions use sha256 of the 256-bit cookie id; PATs use HMAC-SHA256 with the pepper.
- With a plain-http `publicURL` (local dev only) the cookie is `eddy_session` without `Secure`; `__Host-` requires https.
- Runtime kill-switch keys in the `eddy-runtime` flags file are `aiEnabled`, `mcpEnabled`, `mcpWrites` and `mcpAllowLogs` (see `internal/runtimeflags`). They can only turn features off.
- Config keys follow `internal/config/hub.go`; where this ADR shows a nested or different key (for example `mcp.threads.writeScope`), the Go key (`auth.tokens.threadWriteScope`) is authoritative.
