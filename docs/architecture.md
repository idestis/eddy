# Architecture

This page shows Eddy's architecture, access model and main flows as diagrams. The reasons
behind each choice are in [ADR-0001](adr/0001-hub-agent-architecture.md),
[ADR-0002](adr/0002-hub-storage.md) and [ADR-0003](adr/0003-mvp-security.md). The exact
endpoints are in [api.md](api.md).

## Components

```mermaid
flowchart LR
  subgraph Clients
    B[Browser<br/>TanStack SPA]
    CC[Claude Code<br/>MCP client]
  end

  subgraph MGMT[Management cluster]
    direction TB
    ING[Internal ingress<br/>optional oauth2-proxy]
    subgraph HUB[eddy-hub]
      UI[":8080 UI · /api · /auth · /mcp"]
      AG[":8443 /agent/v1/connect"]
      SVC[fleet.Service<br/>SAR filter · audit · kill switches]
      AI[Ask AI<br/>read-only tools]
      MCP[MCP server]
      TH[threads.Service]
    end
    DB[(SQLite on PVC<br/>sessions · PATs · threads · audit)]
    K8S[(Kubernetes API<br/>Cluster CRs · token Secrets<br/>users Secret · eddy-runtime CM)]
  end

  subgraph W1[Workload cluster: prod-eu]
    A1[eddy-agent<br/>informers · summaries]
    API1[(kube-apiserver<br/>Flux CRs · workloads)]
  end
  subgraph W2[Workload cluster: staging]
    A2[eddy-agent]
    API2[(kube-apiserver)]
  end

  LLM[Anthropic API<br/>or AWS Bedrock]

  B -- cookie + CSRF, SSE --> ING --> UI
  CC -- Bearer eddy_pat_ --> ING
  UI --> SVC
  UI --> MCP --> SVC
  UI --> AI --> SVC
  MCP --> TH
  AI --> TH
  TH --> DB
  SVC --> DB
  HUB -. watch .-> K8S
  AI -- redacted, wrapped data --> LLM
  A1 == WebSocket, dials OUT ==> AG
  A2 == WebSocket, dials OUT ==> AG
  AG --> SVC
  A1 -- impersonated user --> API1
  A2 -- impersonated user --> API2
```

**Rules**
- Agents dial out, so workload API servers are never exposed.
- The hub holds no workload-cluster credentials.
- Only summaries leave a cluster.

## Where data lives

```mermaid
flowchart TB
  subgraph WK[Workload clusters: source of truth, never copied]
    F[Flux objects, status, conditions, inventory, events]
    WL[Deployments, Pods, logs]
    SEC[Secrets and ConfigMaps<br/>never read by Eddy]
  end
  subgraph MEM[Agent memory]
    INF[Informer cache<br/>managedFields stripped]
  end
  subgraph HUBMEM[Hub memory]
    SUM[model.Resource summaries per cluster]
    SAR[SAR cache per user, 45 s]
  end
  subgraph K[Management cluster Kubernetes API]
    CR[Cluster CRs + status]
    TS[Agent token Secrets]
    US[Users Secret: argon2id hashes]
    KS[Hub key Secret: CSRF, PAT pepper]
    RT[eddy-runtime ConfigMap: kill switches]
  end
  subgraph S[SQLite /var/lib/eddy/eddy.db]
    SE[sessions: sha256 of id, 8h idle / 24h max]
    PT[api_tokens: HMAC, scopes, expiry ≤ 90d]
    THR[threads + messages: ResourceRef only]
    AU[audit_events: 90 days, also stdout]
    PR[user_prefs]
  end
  F --> INF
  WL --> INF
  INF -- summaries --> SUM
```

```mermaid
erDiagram
  threads ||--o{ messages : contains
  threads {
    text id PK "uuidv7"
    text cluster
    text kind "empty = cluster-level"
    text namespace
    text name
    text type "discussion | ask"
    text visibility "resource | private"
    text status "open | resolved"
    text created_by
  }
  messages {
    text id PK
    text thread_id FK
    text author "accountable human"
    text author_type "human | ai | system"
    text via "web | mcp | askai"
    text client "claude-code, model id"
    text body "≤ 64 KiB"
  }
  api_tokens {
    text id PK "public 12 chars"
    blob hash "HMAC-SHA256"
    text subject
    text scopes "read | operate"
    int expires_at
  }
  sessions {
    blob id_hash PK "sha256"
    text subject
    text groups
    int expires_at
  }
  audit_events {
    int id PK
    text subject
    text via
    text action
    text result "ok | denied | error"
  }
```

## Identity and permissions

```mermaid
flowchart LR
  subgraph IN[Sign-in, v1.0]
    L[Local user<br/>users.yaml]
    P[Proxy headers<br/>trusted CIDR + shared secret]
    T[PAT<br/>MCP only]
  end
  subgraph MAP[Group mapping]
    M["user → local:alice / alice@corp.com<br/>groups → eddy:platform, eddy:authenticated<br/>drop system:* · deny system:/eks: users"]
  end
  subgraph PR[Principal]
    PP["User + Groups + Via + Scopes"]
  end
  subgraph AGT[Agent re-checks]
    C["no system:* · groups match eddy: prefix<br/>optional exact group allowlist"]
  end
  subgraph RBAC[Workload cluster RBAC decides]
    V[eddy-viewer<br/>→ eddy:authenticated]
    O[eddy-operator<br/>→ eddy:platform]
  end
  L --> M
  P --> M
  T --> M
  M --> PP --> C -- Impersonate-User / Impersonate-Group --> RBAC
```

| Who | Channel | Can read | Can act (reconcile / suspend / resume) | Threads |
|---|---|---|---|---|
| Browser user | cookie session | whatever their RBAC allows | if RBAC allows `patch`; protected clusters need the cluster name typed in | read, write, resolve (author or `patch`) |
| PAT with `read` | `/mcp` | whatever their RBAC allows | no | read and write |
| PAT with `operate` | `/mcp` | whatever their RBAC allows | if RBAC allows, `mcp.writes` is on, and `confirm_cluster` is given on protected clusters | read and write |
| Ask AI | server-side | the asking user's RBAC view, redacted | **never** (no write tools) | writes only its own private `ask` thread |
| Agent ServiceAccount | in cluster | get/list/watch on Flux kinds, workloads and pods | **none**: only `impersonate` and SubjectAccessReviews | n/a |
| Hub ServiceAccount | management cluster | Cluster CRs, Secrets in its own namespace | patch `clusters/status` only | n/a |

## Flows

### Agent connects

```mermaid
sequenceDiagram
  autonumber
  participant A as Agent (prod-eu)
  participant H as Hub :8443
  participant K as Mgmt K8s API
  A->>H: WSS /agent/v1/connect?cluster=prod-eu<br/>Authorization: Bearer <agent token>
  H->>K: get Cluster prod-eu → token Secret
  H->>H: sha256 + constant-time compare
  H-->>A: 101 Switching Protocols
  A->>H: hello {versions, namespaces}
  A->>H: snapshot {resources[]}
  H->>K: patch clusters/prod-eu/status (Connected)
  loop every 250 ms while changes occur
    A->>H: delta {upserts, deletes}
    H-->>H: fan out per-user filtered SSE "change"
  end
  Note over A,H: reconnects with jittered backoff (≤ 30 s)
```

### Browser reads (RBAC-filtered, shared cache)

```mermaid
sequenceDiagram
  autonumber
  participant U as Browser
  participant H as Hub
  participant C as SAR cache
  participant A as Agent
  participant K as Workload API
  U->>H: GET /api/v1/clusters/prod-eu/resources (cookie)
  H->>C: can alice list helmreleases in ns team-a?
  alt cache miss (45 s TTL)
    C->>A: request access {checks[]}
    A->>K: SubjectAccessReview (agent SA, user=alice)
    K-->>A: allowed / denied
    A-->>C: allowed[]
  end
  H-->>U: only the rows alice may list
  U->>H: GET /api/v1/stream (SSE)
  H-->>U: change / resync / clusters / thread events
```

### Action on a protected cluster

```mermaid
sequenceDiagram
  autonumber
  participant U as Browser
  participant H as Hub
  participant A as Agent
  participant K as Workload API
  U->>H: POST …/HelmRelease/apps/podinfo/suspend {}<br/>X-Eddy-CSRF
  H-->>U: 428 confirm_required
  U->>U: typed-confirm dialog: "prod-eu"
  U->>H: POST …/suspend {"confirm":"prod-eu"}
  H->>A: request suspend, identity {alice, [eddy:platform]}
  A->>A: reject system:* / foreign group prefixes
  A->>K: PATCH spec.suspend=true<br/>Impersonate-User: alice
  alt RBAC allows patch
    K-->>A: 200
    A-->>H: ok
    H->>H: audit {alice, via web, suspend, ok}
    H-->>U: 202
  else forbidden
    K-->>A: 403
    H->>H: audit {…, denied}
    H-->>U: 403 forbidden
  end
```

### Claude Code over MCP, with review threads

```mermaid
sequenceDiagram
  autonumber
  participant CC as Claude Code
  participant M as Hub /mcp
  participant F as fleet.Service
  participant T as threads.Service
  participant DB as SQLite
  CC->>M: POST tools/call list_unhealthy<br/>Bearer eddy_pat_…
  M->>M: POST only · Origin/Host check · rate limit · kill switch
  M->>DB: token by id → HMAC compare, expiry, scopes
  M->>F: List(principal=alice via mcp) across all clusters
  F-->>M: failed HelmReleases (RBAC-filtered, redacted)
  M-->>CC: untrusted_data {items}
  CC->>M: tools/call create_thread {cluster, HelmRelease, podinfo, body}
  M->>T: Create (needs CanGet on target)
  T->>DB: insert thread + message (author alice, via mcp, client claude-code)
  M->>M: audit mcp.create_thread
  M-->>CC: thread id
  Note over CC,M: reconcile/suspend need scope operate and<br/>confirm_cluster on protected clusters
```

### Ask AI (Anthropic or Bedrock)

```mermaid
sequenceDiagram
  autonumber
  participant U as Browser
  participant H as Hub (ai.Service)
  participant F as fleet.Service
  participant P as Provider (Anthropic / Bedrock)
  U->>H: POST /api/v1/ai/ask {cluster, resourceId, question}
  H->>H: flags.aiEnabled? rate limit per user
  H->>H: private "ask" thread: append question
  loop ≤ 6 rounds
    H->>P: system prompt + <eddy_data nonce> context + tools (read-only)
    P-->>H: tool_use get_events
    H->>F: Events(as alice) → redact → cap 24 KiB → wrap
  end
  P-->>H: final answer
  H->>H: store AI message (author_type ai), audit ai.ask
  H-->>U: {threadId, message, steps}
```

### Sign-in modes (v1.0)

```mermaid
sequenceDiagram
  autonumber
  participant U as Browser
  participant O as oauth2-proxy (optional)
  participant H as Hub
  alt Local users
    U->>H: GET /auth/csrf → pre-session cookie
    U->>H: POST /auth/local/login {user, pass} + CSRF
    H->>H: rate limit · argon2id verify (dummy hash if unknown)
    H-->>U: Set-Cookie __Host-eddy_session (rotated id)
  else Trusted proxy
    U->>O: request (IdP SSO: Google / Okta / GitHub)
    O->>H: X-Forwarded-Email/Groups + X-Eddy-Proxy-Secret
    H->>H: peer in trustedCIDRs AND secret matches? else strip
    H-->>U: session minted; dropped if header identity changes
  end
  Note over U,H: Native OIDC / SAML / GitHub OAuth are planned for v1.1
```
