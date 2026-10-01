# Installing Eddy

Eddy has two parts:

- The **hub** runs once, in your management cluster. It serves the UI, the API and the MCP endpoint.
- An **agent** runs in every workload cluster. It dials out to the hub, so workload API servers are never exposed and the hub holds no cluster credentials.

Eddy has no permission model of its own. Each person signs in to the hub, and their requests run in each cluster as that user, impersonated by the agent. What they can do is decided by your normal Kubernetes RBAC. See [security.md](security.md).

## Prerequisites

- Kubernetes 1.27 or later in every cluster, with Flux installed in the workload clusters.
- Helm 3.12 or later (Helm 4 works).
- For production: an internal ingress (nginx or an AWS ALB) and a TLS certificate for the UI, and a second internal endpoint for agents.
- For the quickstart: `docker`, `kind`, `kubectl`, `helm`, `openssl` (and the `flux` CLI if you have it).

## Quickstart on kind (5 minutes)

```sh
task kind:up
```

This is `deploy/kind/up.sh`. It is safe to re-run. It:

1. Creates a kind cluster named `eddy` and installs Flux.
2. Adds a demo `GitRepository` and `Kustomization` (podinfo), so there is data to look at.
3. Builds the hub and agent images and loads them into kind.
4. Installs the CloudNativePG operator (pinned release) and a one-instance PostgreSQL `Cluster` named `eddy-db`.
5. Installs two hub replicas with local users, the `eddy-db-app` Secret as the store DSN, and one registered cluster called `kind`.
6. Installs two agent replicas, pointing at the in-cluster agents Service over TLS. The script generates a self-signed certificate and gives it to the agent as `hub.caBundle`, so nothing uses plain `ws://`.
7. Applies the example user RBAC.

Then run the port-forward it prints:

```sh
kubectl --context kind-eddy -n eddy port-forward svc/eddy-hub 8080:80
```

Open <http://localhost:8080> and sign in as `dev` with password `eddy-dev-password`. That user is in group `eddy:platform`, so it can also reconcile and suspend. The password is for local demos only. Clean up with `task kind:down`.

## Production install

### 1. PostgreSQL

The hub keeps its own data in PostgreSQL 14 or later: sessions, personal access tokens, threads, audit events, preferences, plus the state replicas share (rate-limit counters, which replica holds which agent, cross-replica notifications). Cluster state is never stored there. Bring your own database: CloudNativePG, RDS or Aurora, Cloud SQL, or any PostgreSQL. It needs no extensions. The hub runs its migrations at start, under an advisory lock, so replicas starting together are fine.

Give the hub a Secret whose key holds the DSN, a libpq URL such as `postgres://eddy:<password>@db.example.com:5432/eddy?sslmode=verify-full` (`verify-full` is recommended outside the cluster). Each replica opens up to `store.postgres.maxOpenConns` connections (default 10) plus one for `LISTEN`.

**CloudNativePG.** With the [operator](https://cloudnative-pg.io) installed, this is all it takes (two instances give you a standby; one is enough to start):

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: eddy-db
  namespace: eddy            # the hub's namespace
spec:
  instances: 2
  imageName: ghcr.io/cloudnative-pg/postgresql:17
  storage:
    size: 5Gi
  bootstrap:
    initdb:
      database: eddy
      owner: eddy
  # backup:                  # barman to object storage, see the CNPG docs
  #   barmanObjectStore: {...}
```

CNPG creates the Secret `eddy-db-app`. Its `uri` key is a complete DSN, so point the hub at it:

```yaml
store:
  postgres:
    dsnSecret: {name: eddy-db-app, key: uri}
```

With the hub's NetworkPolicy on, also allow egress to the database, for example `networkPolicy.egress.postgresTo: [{podSelector: {matchLabels: {cnpg.io/cluster: eddy-db}}}]`.

**Your own database.** Create a database and a role that owns it, then the Secret:

```sh
kubectl -n eddy create secret generic eddy-db \
  --from-literal=dsn='postgres://eddy:<password>@db.example.com:5432/eddy?sslmode=verify-full'
```

and set `store.postgres.dsnSecret: {name: eddy-db, key: dsn}`.

Sessions, rate-limit counters and the agent session registry live in `UNLOGGED` tables: a PostgreSQL crash, or a failover to a standby (standbys do not receive `UNLOGGED` data), empties them. That signs everyone out and resets the limits; nothing else is lost, and agents re-register within about 10 s.

`store.driver: memory` needs no database but is for evaluation only: it runs one replica, and every restart signs everyone out, invalidates MCP tokens and loses threads. NOTES.txt and the UI warn about it.

### 2. Install the hub (management cluster)

Create the credentials Secret first (only what you use), so no secret goes into Helm values:

```sh
kubectl create namespace eddy
kubectl -n eddy create secret generic eddy-credentials \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...        # only if you use the Anthropic API
```

Hash a password for each local user. `hash-password` reads stdin, so the password stays out of shell history:

```sh
read -rs PW && printf '%s' "$PW" | docker run --rm -i ghcr.io/idestis/eddy-hub:1.0.0 hash-password
```

Then write `hub-values.yaml`:

```yaml
publicURL: https://eddy.internal.example.com
agentsPublicURL: https://eddy-agents.internal.example.com

credentialsSecret: eddy-credentials

users:
  list:
    - username: alice
      passwordHash: "$argon2id$v=19$m=65536,t=3,p=4$...$..."
      groups: [platform]          # becomes Kubernetes group eddy:platform
    - username: bob
      passwordHash: "$argon2id$v=19$m=65536,t=3,p=4$...$..."

store:
  postgres:
    dsnSecret: {name: eddy-db-app, key: uri}   # see step 1

ingress:
  ui:
    enabled: true
    className: nginx-internal
    hosts: [eddy.internal.example.com]
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-buffering: "off"
    tls:
      - secretName: eddy-tls
        hosts: [eddy.internal.example.com]
  agents:
    enabled: true
    className: nginx-internal
    hosts: [eddy-agents.internal.example.com]
    annotations:
      nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
      nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    tls:
      - secretName: eddy-agents-tls
        hosts: [eddy-agents.internal.example.com]
```

Install it:

```sh
helm install eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version 1.0.0 \
  --namespace eddy -f hub-values.yaml
```

Things to know:

- The hub runs **two replicas** by default, active/active (ADR-0004): each serves the UI, API, SSE, MCP and the agent endpoint. Agents connect to whichever replica the load balancer picks; the other replicas relay to it over the peer channel, a WebSocket on port 8444 between hub pods. The chart adds a headless Service `eddy-hub-peers` for discovery, `POD_NAME`/`POD_IP`, a PodDisruptionBudget (`maxUnavailable: 1`), a rolling update that never removes a replica before its replacement is ready (`maxSurge: 1`, `maxUnavailable: 0`), zone and host spreading and a preferred anti-affinity. With `networkPolicy.enabled`, port 8444 accepts only hub pods.
- Replicas authenticate each other with a key derived from the hub key Secret, so every replica must mount the same one (the chart does). Each replica logs a `keyFingerprint` at start; a replica whose peer links fail authentication reports not ready.
- A replica is ready when the store is reachable, the cluster registry has synced, it has a link to every peer that DNS lists (after a 30 s grace) and its relayed clusters have synced.
- The session/pepper key is generated on first install and kept across upgrades. With `helm template` or Argo CD, `lookup` does not work, so create the Secret yourself and set `sessionKeySecret` (key name `key`, at least 32 random bytes). The same goes for agent tokens: use `clusters[].existingTokenSecret`.
- Both Services (`eddy-hub`, `eddy-hub-agents`) are `ClusterIP`. The UI, API and MCP are on port 8080 and the agent endpoint is on 8443, with separate listeners so they can be exposed separately.

#### Ingress: nginx, internal

Use an internal ingress class (one backed by an internal load balancer). The UI uses SSE, and agents use long-lived WebSockets, so raise the proxy timeouts as in the values above and turn off buffering for the UI.

#### Ingress: AWS ALB, internal

```yaml
ingress:
  ui:
    enabled: true
    className: alb
    hosts: [eddy.internal.example.com]
    annotations:
      alb.ingress.kubernetes.io/scheme: internal
      alb.ingress.kubernetes.io/target-type: ip
      alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'
      alb.ingress.kubernetes.io/ssl-redirect: "443"
      alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:eu-central-1:111122223333:certificate/xxxx
      alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
      alb.ingress.kubernetes.io/healthcheck-path: /healthz
      alb.ingress.kubernetes.io/inbound-cidrs: 10.0.0.0/8
  agents:
    enabled: true
    className: alb
    hosts: [eddy-agents.internal.example.com]
    annotations:
      alb.ingress.kubernetes.io/scheme: internal
      alb.ingress.kubernetes.io/target-type: ip
      alb.ingress.kubernetes.io/listen-ports: '[{"HTTPS":443}]'
      alb.ingress.kubernetes.io/certificate-arn: arn:aws:acm:eu-central-1:111122223333:certificate/yyyy
      alb.ingress.kubernetes.io/load-balancer-attributes: idle_timeout.timeout_seconds=3600
      alb.ingress.kubernetes.io/healthcheck-path: /healthz
      alb.ingress.kubernetes.io/inbound-cidrs: 10.20.0.0/16   # only your workload clusters
```

Agents in other VPCs or accounts reach the agents endpoint over VPC peering, Transit Gateway or PrivateLink. If it is ever internet-facing, restrict it by source CIDR: until mTLS arrives in v0.2, the token is the only other control.

If you prefer no ingress for agents, put an internal NLB in front of the `eddy-hub-agents` Service with `service.agents.annotations`, and let the hub terminate TLS itself with `agentTLS.secretName` (a `kubernetes.io/tls` Secret).

### 3. Register clusters

There are two ways to register a workload cluster. Both end with a `Cluster` custom resource
(`kubectl get clusters`, short name `ecl`) in the management cluster and a token Secret
`eddy-agent-<name>` in the hub namespace. Pick per cluster; they mix freely.

#### Option A: from the UI (onboarding)

With `onboarding.enabled: true` (the default), a user who may create `clusters.gitops.eddy.dev`
in the **management** cluster sees **Add cluster** on the Fleet page:

1. Fill in the name (a DNS label), display name, environment, region, colour, protection
   and order. The hub creates the `Cluster` resource (phase `Pending`), its token Secret and a
   **one-time join token** (valid 1 h by default, `onboarding.joinTokenTTL`, at most 24 h).
2. The Connect screen shows the install command for that cluster in three forms: a `helm`
   command, a `values.yaml`, and plain manifests for GitOps-managed clusters. It also shows the
   `Cluster` resource, in case you want to keep clusters in Git.
3. Run the command against the **workload** cluster. The agent connects with the join token,
   the hub writes a permanent agent token into the cluster's token Secret and hands it to the
   agent, which stores it in its own token Secret and reconnects. The join token is then
   worthless.
4. The checklist fills in live: agent connected, protocol compatible, Flux detected, informers
   synced, SubjectAccessReview working, impersonation pinned, watched namespaces. Rejected
   attempts (bad or expired token, used join token, wrong cluster, protocol mismatch) are
   listed with their reason. **Finish** closes the wizard.

Afterwards the same checklist is the **Connection** panel of the cluster card, with
**Regenerate join token** (reinstall an agent or rotate its token) and **Delete**
(typed confirmation on protected clusters). Clusters from the chart's `clusters[]` can get a
join token there too, but are edited and removed in your Helm values.

Set `agentsPublicURL` so the guide shows the address agents really dial; without it the guide
uses the first `ingress.agents` host, then the in-cluster Service.

**Who may do it.** Eddy adds no roles of its own: the hub asks the management cluster, with a
SubjectAccessReview as the signed-in user, for `create` (add), `update` (join tokens and
edits), `delete` (remove) and `get` (see the Connection panel) on `clusters.gitops.eddy.dev`.
Bind your platform group in the **management** cluster:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: eddy-cluster-admin
rules:
  - apiGroups: [gitops.eddy.dev]
    resources: [clusters]
    verbs: [get, list, watch, create, update, patch, delete]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: eddy-cluster-admin
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: eddy-cluster-admin
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: eddy:platform
```

**What it adds to the hub's RBAC.** `onboarding.enabled: true` grants the hub ServiceAccount
`create`, `update`, `patch` and `delete` on `clusters`, `create` on `subjectaccessreviews`, and
`create`, `update` and `delete` on Secrets in the hub namespace only. With
`onboarding.enabled: false` the hub keeps its read-only RBAC and the button is hidden. The
wizard is also off when the hub uses `staticClusters` (local development): the API answers
409 and `features.onboarding` in `/api/v1/me` is `false`.

**GitOps.** A join token in Git is acceptable because it is short-lived and single-use, but a
SOPS or External Secrets reference is better. The agent writes its permanent token into the
same Secret under `token`; if a GitOps controller manages that Secret, tell it to ignore that
key (for example an Argo CD `ignoreDifferences` on `/data/token`), or it will remove it and the
agent falls back to the used join token after a restart.

**HA.** Every hub replica serves every step. The `Cluster` resources and Secrets are the source
of truth, join tokens live in the `join_tokens` table, and rejected attempts (the last 20 per
cluster, only for clusters that exist) in the `UNLOGGED` table `connection_attempts`, so
every replica shows the same checklist.

#### Option B: in Helm values (GitOps)

List every workload cluster in `clusters[]` and upgrade:

```yaml
clusters:
  - name: prod-eu
    environment: Production
    region: eu-central-1
    color: "#C2410C"
    protected: true        # writes need a typed confirmation of the cluster name
    order: 1
  - name: staging
    environment: Staging
    color: "#0F766E"
    order: 2
```

For each entry the chart creates a `Cluster` custom resource and a random token Secret
`eddy-agent-<name>` in the hub namespace. Both are kept across upgrades.

### 4. Install an agent in each workload cluster

With Option A, run the command the Connect screen shows. With Option B, `helm upgrade` (or
`helm install`) on the hub prints the exact command per cluster in NOTES.txt. It looks like this:

```sh
helm install eddy-agent oci://ghcr.io/idestis/charts/eddy-agent --version 1.0.0 \
  --kube-context prod-eu --namespace eddy-system --create-namespace \
  --set cluster.name=prod-eu \
  --set hub.url=wss://eddy-agents.internal.example.com/agent/v1/connect \
  --set-string token.value="$(kubectl --context mgmt -n eddy get secret eddy-agent-prod-eu -o jsonpath='{.data.token}' | base64 -d)"
```

Options:

| Value | Purpose |
|---|---|
| `cluster.name` | Must equal the `Cluster` name in the hub |
| `hub.url` | The agents endpoint, `wss://` only |
| `hub.caBundle` | PEM CA, only if the hub certificate is not publicly trusted (`--set-file hub.caBundle=ca.pem`) |
| `joinToken` | One-time `eddy_join_…` token from the UI. The chart puts it in the token Secret and adds a Role that lets the agent `get` and `update` that one Secret, where it stores its permanent token |
| `token.existingSecret` / `token.value` | A pre-provisioned agent token instead of a join token. Prefer an existing Secret under GitOps |
| `watch.namespaces` | Limit what the agent watches. Empty means the whole cluster |
| `impersonation.allowedGroupPrefixes` | Default `["eddy:"]`. `system:` is always refused |
| `impersonation.groups` | Strictest: pin impersonation to an exact list of groups (RBAC `resourceNames`) |
| `networkPolicy.enabled` | Egress-only policy (DNS, Kubernetes API, hub) |
| `replicaCount` | Default `2`. Every replica connects; the hub uses the oldest and keeps the others as hot standbys, so a lost pod or node does not disconnect the cluster. Each replica runs its own watches |
| `limits.qps`, `limits.burst` | client-go QPS/burst for the whole Deployment (default 20/40). The chart divides them by `replicaCount` |
| `limits.concurrency`, `limits.logStreams`, `limits.sarConcurrency` | Requests, log streams and SubjectAccessReviews in flight for the whole Deployment (default 16/8/8), divided by `replicaCount` |

The agent's ServiceAccount is read-only (Flux kinds, Deployments, StatefulSets, DaemonSets, ReplicaSets, Jobs, CronJobs, Pods, Services, Ingresses, HorizontalPodAutoscalers, PersistentVolumeClaims, events, Namespaces). It cannot read Secrets or ConfigMaps. Its only extra powers are creating SubjectAccessReviews and impersonating users and `eddy:` groups, plus, with `joinToken`, `get` and `update` on its own token Secret.

Within a minute the cluster shows as `Connected`: `kubectl get clusters`.

### 5. Apply user RBAC

Eddy shows what a person's own RBAC allows, so nobody sees anything until you grant it. Apply `deploy/rbac/eddy-user-rbac.yaml` in each workload cluster and adjust the subjects:

- `eddy-viewer` (get, list, watch on Flux kinds, workloads, Jobs, CronJobs, pods, Services, Ingresses, HorizontalPodAutoscalers, PersistentVolumeClaims, `pods/log` and events) is bound to `eddy:authenticated`, which every signed-in user has.

Objects a Kustomization applied whose kinds Eddy does not watch (ConfigMaps, Secrets, ServiceAccounts, RBAC, CRDs and so on) appear as **inventory-only** rows: kind, namespace and name from the Kustomization's `status.inventory`, never the object itself. A user sees such a row only if they can list its Kustomization and, for kinds Eddy knows (for example `secrets`), list that kind in the row's namespace. HelmReleases keep no inventory in their status, so their unwatched objects do not appear.
- `eddy-operator` (adds `patch` on Flux kinds, which is what reconcile, suspend and resume need) is bound to `eddy:platform`.

Local user `alice` with `groups: [platform]` is Kubernetes user `local:alice` in group `eddy:platform` (plus `eddy:authenticated`). A GitHub user in team `acme/platform` is in `eddy:github:acme/platform`, and an OIDC user's groups claim maps the same way ([docs/auth.md](auth.md#from-identity-to-kubernetes-user-and-groups)). Namespace-scoped RoleBindings work too.

### 6. Optional: sign in through your identity provider

Eddy signs people in with **GitHub** (a GitHub App or OAuth App, github.com or Enterprise Server) and any **OpenID Connect** provider (Google, Okta, Entra ID, Dex and others) on its own. [docs/auth.md](auth.md) walks through creating the GitHub App or OIDC client, the callback URL `<publicURL>/auth/<id>/callback`, group mapping and RBAC, and keeping a break-glass admin password. In short:

```sh
kubectl -n eddy create secret generic eddy-credentials \
  --from-literal=GITHUB_CLIENT_SECRET=...        # merge with the keys you already have
```

```yaml
config:
  auth:
    github:
      enabled: true
      clientID: Iv23li...
      allowedOrganizations: [acme]      # groups: eddy:github:acme, eddy:github:acme/<team>
    local:
      enabled: true
      mode: breakglass                  # password form only at /login?local=1, every use logged as WARN
```

The identity provider never connects to the hub (both legs are browser redirects), so this works on a private network or VPN. The hub only needs outbound HTTPS to the provider ([egress list](auth.md#private-networks-vpns-and-egress)).

#### Alternative: a sign-in proxy

If you already run [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) or Pomerium in front of your tools, Eddy can trust its headers instead. The chart can run oauth2-proxy as a sidecar. How it fits together:

- The sidecar listens on 4180 and forwards to `127.0.0.1:8080`. The hub's UI listener binds to `127.0.0.1` and trusts identity headers only from `127.0.0.1/32` **and** only with a matching shared secret, so nothing can reach the hub except through the proxy.
- The UI Service then points at the proxy.
- `/mcp` bypasses the proxy (`--skip-auth-route=^/mcp$`). MCP clients send only a personal access token, so they cannot do a browser login. **Do not add authentication in front of `/mcp`**, and the hub never honours proxy headers on it.

Create two Secrets:

```sh
# Shared secret between proxy and hub (at least 32 random bytes), merged into credentialsSecret.
kubectl -n eddy create secret generic eddy-credentials \
  --from-literal=EDDY_PROXY_SECRET="$(openssl rand -base64 48)"

kubectl -n eddy create secret generic eddy-oauth2-proxy \
  --from-literal=OAUTH2_PROXY_COOKIE_SECRET="$(openssl rand -base64 32 | tr -- '+/' '-_')" \
  --from-literal=OAUTH2_PROXY_CLIENT_ID=... \
  --from-literal=OAUTH2_PROXY_CLIENT_SECRET=...
```

Values (Google shown; use a different `provider` and its options for GitHub, Okta and so on):

```yaml
config:
  auth:
    local:
      enabled: false              # or keep it on as a break-glass login
    groups:
      static:
        alice@example.com: [platform]   # Google sends no groups, so map them here
oauth2Proxy:
  enabled: true
  existingSecret: eddy-oauth2-proxy
  extraArgs: ["--email-domain=example.com"]   # who may sign in
  alphaConfig:
    providers:
      - id: google
        provider: google
        clientID: ${OAUTH2_PROXY_CLIENT_ID}
        clientSecret: ${OAUTH2_PROXY_CLIENT_SECRET}
```

The chart enables hub proxy auth, sets `trustedCIDRs` to `127.0.0.1/32`, and configures the proxy to send `X-Forwarded-Email`, `X-Forwarded-Groups` and `X-Eddy-Proxy-Secret` (read from the same `EDDY_PROXY_SECRET`). The chart's oauth2-proxy settings use its alpha configuration format, which can change between releases. Check a new version with `oauth2-proxy --config-test`. Users from the proxy get the Kubernetes user name from the email header, and PATs for proxy users live at most 30 days.

### 7. Optional: Ask AI

Ask AI answers questions about a resource using only what the asking user can see. It is read-only and off by default.

**Anthropic API:**

```yaml
credentialsSecret: eddy-credentials     # holds ANTHROPIC_API_KEY
config:
  ai:
    enabled: true
    provider: anthropic
    anthropic:
      model: claude-haiku-4-5-20251001
```

Resource summaries and redacted YAML go to Anthropic. The UI says which provider handles the data.

**AWS Bedrock (recommended on AWS):** data stays in your AWS account and region, with no static keys. The hub uses IRSA (or EKS Pod Identity).

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/eddy-hub-bedrock
config:
  ai:
    enabled: true
    provider: bedrock
    bedrock:
      region: eu-central-1
      modelId: eu.anthropic.claude-haiku-4-5-20251001-v1:0     # inference profile id or ARN
      guardrail: {id: "abc123xyz", version: "1", trace: false}  # optional
```

The role trusts the hub ServiceAccount:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {"StringEquals": {
      "oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE:sub": "system:serviceaccount:eddy:eddy-hub",
      "oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE:aud": "sts.amazonaws.com"
    }}
  }]
}
```

and its permissions policy allows `InvokeModel` on only the inference profile and the foundation models behind it (list each region the profile routes to; an `eu.` profile stays inside Europe, while a `global.` profile can route anywhere, so pick a geo or single-region profile if data residency matters):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "InvokeViaInferenceProfile",
      "Effect": "Allow",
      "Action": "bedrock:InvokeModel",
      "Resource": "arn:aws:bedrock:eu-central-1:111122223333:inference-profile/eu.anthropic.claude-haiku-4-5-20251001-v1:0"
    },
    {
      "Sid": "InvokeUnderlyingModelOnlyViaThatProfile",
      "Effect": "Allow",
      "Action": "bedrock:InvokeModel",
      "Resource": [
        "arn:aws:bedrock:eu-central-1::foundation-model/anthropic.claude-haiku-4-5-20251001-v1:0",
        "arn:aws:bedrock:eu-west-1::foundation-model/anthropic.claude-haiku-4-5-20251001-v1:0",
        "arn:aws:bedrock:eu-west-3::foundation-model/anthropic.claude-haiku-4-5-20251001-v1:0"
      ],
      "Condition": {"StringLike": {"bedrock:InferenceProfileArn": "arn:aws:bedrock:eu-central-1:111122223333:inference-profile/eu.anthropic.claude-haiku-4-5-20251001-v1:0"}}
    },
    {
      "Sid": "ApplyGuardrail",
      "Effect": "Allow",
      "Action": "bedrock:ApplyGuardrail",
      "Resource": "arn:aws:bedrock:eu-central-1:111122223333:guardrail/abc123xyz"
    }
  ]
}
```

Drop the last statement if you do not set a guardrail. The condition keeps the foundation-model permission usable only through that profile. If Bedrock denies a call, compare the ARNs in the error message with the policy, because the regions behind a profile can change over time. Enable model access for the model in the Bedrock console first.

### 8. Kill switches

Flip a feature off without a restart. The `eddy-runtime` ConfigMap is mounted into the hub and re-read continuously (Kubernetes propagates ConfigMap edits in about a minute):

```sh
kubectl -n eddy patch configmap eddy-runtime --type merge \
  -p '{"data":{"flags.yaml":"aiEnabled: false\nmcpEnabled: true\nmcpWrites: false\nmcpAllowLogs: false\n"}}'
```

| Flag | Effect when `false` |
|---|---|
| `aiEnabled` | Ask AI returns 503 and its button disappears |
| `mcpEnabled` | `/mcp` refuses all calls |
| `mcpWrites` | MCP reconcile, suspend and resume are refused, reads still work |
| `mcpAllowLogs` | The MCP `get_logs` tool is refused |

A flag can only turn something off. It cannot enable what `config.*` disables. Set the same values under `runtimeFlags:` in your Helm values so an upgrade does not undo your change.

### 9. Backup, upgrade, uninstall

**Backup.** All hub state is in the PostgreSQL database: tokens, threads, audit and preferences (sessions and counters too, but those are throwaway). Cluster definitions and agent tokens live in Kubernetes (your Helm values). The database holds emails and message text but no credentials: PAT secrets are stored only as HMACs. Back it up the way you back up any PostgreSQL:

- CloudNativePG: a `ScheduledBackup` with barman to object storage, or volume snapshots, as its documentation describes.
- RDS, Aurora, Cloud SQL: their automated backups and point-in-time recovery.
- Anywhere: `pg_dump --format=custom "$DSN" > eddy.dump`, restored with `pg_restore`. `UNLOGGED` tables are dumped too; restoring them is harmless.

Backups contain PII (emails, message text), so encrypt and restrict them. `eddy-hub admin backup` was removed together with SQLite.

**Upgrade.**

```sh
helm upgrade eddy-hub oci://ghcr.io/idestis/charts/eddy-hub --version <new> \
  --namespace eddy -f hub-values.yaml
```

Helm does not upgrade CRDs in a chart's `crds/` directory. Apply CRD changes first:

```sh
helm pull oci://ghcr.io/idestis/charts/eddy-hub --version <new> --untar --untardir /tmp/eddy
kubectl apply -f /tmp/eddy/eddy-hub/crds/
```

 Upgrade the hub before the agents, then each agent with the same chart version.

The hub rolls one replica at a time, so the UI stays up: agents of the replica being replaced reconnect to another one within seconds, and with two agent replicas the standby keeps the cluster connected meanwhile. Migrations are forward-only and applied by the first new replica. Sessions and tokens survive, since they are in PostgreSQL.

**Upgrading from a pre-release SQLite install.** There is no automatic migration of the SQLite file. Users sign in again and create new tokens; threads and in-database audit from v0.x are not carried over (stdout audit in your log pipeline is unaffected). Remove the old `persistence.*` values and the `eddy-hub-data` PVC once you no longer need the file.

**Uninstall.**

```sh
helm uninstall eddy-agent -n eddy-system --kube-context prod-eu     # per workload cluster
helm uninstall eddy-hub -n eddy
```

The database, the signing-key Secret and the CRD remain on purpose. To remove everything, drop the database (or delete the CNPG `Cluster`), then:

```sh
kubectl -n eddy delete secret eddy-hub-key
kubectl delete crd clusters.gitops.eddy.dev
```

## Troubleshooting

- **An added cluster stays `Pending`:** open its Connection panel. Rejected attempts say why: an expired or already used join token (regenerate one), a token for another cluster, or a protocol mismatch (upgrade the agent). No attempts at all means the agent never reached the hub: check `hub.url`, DNS, and outbound 443 from the workload cluster.
- **Cluster stays `Disconnected`:** read the agent log (`kubectl -n eddy-system logs deploy/eddy-agent`). Usual causes are a wrong `hub.url`, a certificate the agent does not trust (set `hub.caBundle`), a token that does not match the `eddy-agent-<name>` Secret, or an ingress that closes idle WebSockets.
- **Sign-in loops or fails with an Origin error:** `publicURL` must equal the address in the browser, including the scheme.
- **The UI is empty:** the user has no RBAC in the cluster. Apply the example roles.
- **Everyone got logged out:** the store is ephemeral (`store.driver: memory`), PostgreSQL crashed (its `UNLOGGED` session table is emptied on crash recovery), or the key Secret changed.
- **A hub replica stays not ready:** read its `/readyz` on port 9090 (`kubectl -n eddy port-forward pod/<pod> 9090`, then `curl localhost:9090/readyz`). It names the missing peer link or unsynced cluster. Check that NetworkPolicies allow port 8444 between hub pods and that every replica logs the same `keyFingerprint`.
- **Sign-in answers 503:** the hub cannot reach PostgreSQL. Reads of cluster data keep working; sign-in, token and thread writes and rate-limited actions fail closed until it is back.
