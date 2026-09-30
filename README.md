# Eddy

**A fast, keyboard-first, multi-cluster UI for [Flux](https://fluxcd.io).**

Eddy shows every Flux cluster you run in one place. You can jump anywhere with a command
palette, reconcile or suspend with a single key, and read logs, events and YAML without
leaving the keyboard. We built it for our own platform work and share it in case it helps
yours.

> **Status: v1.0, early release.** The core is stable. Sign-in providers and high
> availability arrive in v1.1 (see the roadmap below).

![Eddy resource list](docs/images/list.png)

## Why Eddy

- **Multi-cluster.** A hub runs in a management cluster and a
  light agent runs in every workload cluster. The agents dial out, so no workload API
  server is ever exposed and the hub holds no cluster credentials.
- **Fast.**
  - Agents stream summaries through informers and batched deltas.
  - The UI updates over SSE and uses virtualised lists.
  - A command palette (`⌘K`) searches every resource in every cluster.
- **Kubernetes RBAC is the permission model.** Every read and action runs impersonated as
  the signed-in user, and each cluster decides what they may do. Eddy invents no roles.
- **Safe by default.**
  - Secret and ConfigMap data never leave a cluster.
  - Protected clusters need a typed confirmation, and every action is audited.
  - AI features are read-only.
- **Built for AI-assisted operations.**
  - An MCP endpoint lets Claude Code (or any MCP client) query the whole fleet, act with
    your identity and guardrails, and leave review threads on resources.
  - Ask AI works with the Anthropic API or AWS Bedrock.

## What's in v1.0 and what's next

| Area | Ready in v1.0 | Next (v1.1+) |
|---|---|---|
| Clusters | Hub and agents over outbound WebSocket; **Add cluster wizard** with install guide, one-time join tokens and a live connection checklist; 1..N agent replicas per cluster | mTLS for agents |
| Flux | Kustomization, HelmRelease, Git/OCI/Helm repositories, HelmChart, Bucket; workloads and pods; inventory tree | Image automation, notification alerts, `flux diff` view |
| Actions | Reconcile (with source), suspend, resume; typed confirmation on protected clusters | Workload restart, bulk actions |
| UI | Keyboard-first SPA (TanStack Router, Query and Virtual), `⌘K` palette, fleet overview, detail views (YAML, events, logs), dark mode | Saved views, graph view |
| Sign-in | Local users (argon2id); trusted reverse-proxy headers (oauth2-proxy, Pomerium) | **GitHub OAuth2, OIDC (Google, Okta, Entra, Dex), SAML 2.0** |
| Access tokens | Personal access tokens (`read` / `operate`, mandatory expiry) for MCP | MCP OAuth 2.1, per-cluster scoping |
| MCP | `/mcp` with fleet-wide read tools, guarded actions and thread tools | Log follow, subscriptions |
| Threads | Review threads on any resource or cluster, from people, Claude Code and Ask AI | Mentions, notifications, webhooks |
| Ask AI | Anthropic API or AWS Bedrock (IRSA, Guardrails), read-only tools, redaction | Streaming answers |
| Storage and HA | PostgreSQL (bring your own, for example CloudNativePG); **multiple hub replicas**, active/active | Read replicas |
| Ops | Helm charts, internal ingress examples, audit log, runtime kill switches | Prometheus dashboards, audit webhook |

## Architecture

```
 Browser (TanStack SPA) ─┐                       ┌─ Claude Code / MCP client
   session cookie, SSE   │                       │   Bearer eddy_pat_…
                         ▼                       ▼
            ┌──────────────── Hub (management cluster) ────────────────┐
            │  :8080  UI · /api · /auth · /mcp       (internal ingress)│
            │  :8443  /agent/v1/connect              (agent endpoint)  │
            │  PostgreSQL: sessions · tokens · threads · audit         │
            │  K8s:    Cluster CRs · agent token Secrets               │
            └────────────▲────────────────────────────▲────────────────┘
                         │ WebSocket (agent dials out)│
            ┌────────────┴───────────┐    ┌───────────┴────────────┐
            │ Agent · prod-eu        │    │ Agent · staging        │
            │ informers, summaries,  │    │ impersonated reads and │
            │ impersonated actions   │    │ actions, read-only SA  │
            └────────────────────────┘    └────────────────────────┘
```

Design decisions are recorded in [`docs/adr/`](docs/adr), and the API contract is in
[`docs/api.md`](docs/api.md).

## Quickstart (kind, about 5 minutes)

Requirements: Docker, [kind](https://kind.sigs.k8s.io), `kubectl`, `helm`, `flux` and
[Task](https://taskfile.dev).

```sh
task kind:up      # kind cluster + Flux + a demo app + the Eddy hub and agent
kubectl -n eddy port-forward svc/eddy-hub 8080:80
open http://localhost:8080   # sign in as the dev user printed by the script
```

For production installs, including internal ingress, local users, proxy auth, Bedrock and
backups, see **[docs/install.md](docs/install.md)**.

## Connect Claude Code

Create a token under **Settings → Access tokens**. Then run:

```sh
claude mcp add --transport http eddy https://eddy.internal.example.com/mcp \
  --header "Authorization: Bearer eddy_pat_…"
```

Then ask, for example: *"Review every failing HelmRelease across the fleet and leave a thread
on each one with the likely cause."* See [docs/mcp.md](docs/mcp.md) for the tools, scopes and
guardrails.

## Security model

- Agents only dial out. The agent's ServiceAccount is read-only, apart from `impersonate`
  and SubjectAccessReviews.
- Every request is impersonated as the user. `system:*` users and groups are never
  impersonated, and groups are prefixed `eddy:`.
- Only resource summaries reach the hub. YAML views drop Secret data, `managedFields` and
  env values.
- Tokens are hashed at rest, sessions are server-side, CSRF is enforced, and the CSP is strict.
- AI and MCP output is redacted and wrapped as untrusted data. AI has no write tools. MCP
  writes need the `operate` scope and typed confirmation on protected clusters, and can be
  switched off at runtime.

Details: [docs/security.md](docs/security.md). To report a vulnerability, see
[SECURITY.md](SECURITY.md).

## Development

```sh
cp .env.example .env                   # local settings (gitignored)
task dev                               # hub + agent + web with hot reload; nothing installed in a cluster
task dev CONTEXTS=kind-eddy,staging    # serve these kubeconfig contexts (read-only by default)
task dev:mock                          # UI only, with mock data
task check                             # lint + tests + UI build; run before a PR
```

Local mode runs the agent on your machine as your kubeconfig identity. It works only in dev
builds and on loopback, is read-only unless `ALLOW_WRITES=1`, and keeps contexts matching
`(?i)prod` read-only. See [CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/development.md](docs/development.md).

`CLAUDE.md` holds the conventions and security invariants used by contributors and by
Claude Code.

## Contributing

Contributions are welcome. Read [CONTRIBUTING.md](CONTRIBUTING.md) and the
[code of conduct](CODE_OF_CONDUCT.md). Commits use Conventional Commits.

## License

[Apache License 2.0](LICENSE).
