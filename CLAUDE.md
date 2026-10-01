# Eddy: notes for Claude Code

Eddy is a keyboard-first, multi-cluster web UI for FluxCD. It is Apache-2.0 and public, so
every file is written for outside contributors.

## Layout

- Go module `github.com/idestis/eddy` (Go 1.26). There are two binaries:
  - `cmd/hub`: the hub, which runs in the management cluster.
  - `cmd/agent`: the agent, which runs in every workload cluster and dials out to the hub.
- Shared contracts. Change them only deliberately, and update `docs/api.md` in the same change:
  - `internal/model`: the `Resource` summary. It is the only cluster data that leaves a cluster.
  - `internal/protocol`: hub↔agent WebSocket frames.
  - `internal/identity`: the `Principal` (user, groups, via, scopes).
  - `internal/fleet`: the user-scoped `Service` that HTTP, MCP and Ask AI all go through.
  - `internal/store`: persistence interfaces for sessions, tokens, threads, audit and prefs.
  - `internal/config`: `hub.yaml` and the agent environment variables.
- Implementations:
  - `internal/flux`: kinds, summarizers and inventory.
  - `internal/agent`: informers, impersonated actions and the WebSocket client.
  - `internal/hub`: the HTTP API, SSE, the agent registry (1..N agent sessions per cluster), the peer relay between hub replicas, the SAR authorizer and the threads service.
  - `internal/auth`: GitHub and OIDC sign-in, local users, proxy auth, sessions, CSRF and PATs.
  - `internal/store/{postgres,memory,storetest}`: the store backends and their shared tests. PostgreSQL is the production store (ADR-0004); `memory` is for tests and `task dev`.
  - `internal/devlocal`: local-mode helpers (dev builds only).
  - `internal/ai`: the Anthropic and Bedrock providers, the redactor and the tool loop.
  - `internal/mcp`: the MCP server.
  - `internal/audit`: the audit recorder.
  - `internal/ui`: the embedded SPA.
- `web/`: the Vite, React and TanStack Router/Query/Virtual SPA, with `cmdk` and `tinykeys`.
  `pnpm` is the package manager and Biome handles lint and format.
- `deploy/charts/{eddy-hub,eddy-agent}`: the Helm charts. `deploy/kind/` holds the local
  quickstart. `config/crd/` holds the Cluster CRD.
- `docs/`:
  - `docs/adr/`: decisions. Read them before changing an area.
  - `docs/api.md`: the HTTP, SSE and MCP contract.
  - `docs/install.md`, `docs/security.md`.
  - `docs/prototype/`: the UI design reference.

## Commands (Taskfile)

- `task ci`: exactly what GitHub CI runs (`hack/ci.sh`): Go with Postgres, govulncheck, web,
  landing, charts and images (`DOCKER=0` skips images). Run it before you say you are done.
- `task check`: the quicker lint, test and UI build.
- `task test` / `task lint`: Go tests with `-race` / `go vet` and staticcheck.
- `task ui` / `task ui:dev`: build the SPA / run Vite on :5173, proxying to the hub on :8080.
- `task dev [CONTEXTS=a,b]`: hub, agent and web with air hot reload, using local mode
  against your kube contexts. Read-only unless `ALLOW_WRITES=1`. Settings come from `.env`
  (see `docs/development.md`).
- `task dev:hub` / `task dev:agent` / `task dev:web` / `task dev:mock`: the pieces on
  their own. `dev:mock` is the UI only, with mock data. `LOCAL=0` runs the real
  impersonating agent, for example against kind.
- `task kind:up`: a kind cluster with Flux, the hub and an agent (see `docs/install.md`).
- `task images` / `task charts`: build the container images / lint and package the charts.

## Security invariants (never break)

- Agents only dial out. The hub holds no workload-cluster credentials. The agent's
  ServiceAccount has no write RBAC.
- Every direct cluster read (YAML, events, logs, Get) and every write runs impersonated as the
  requesting user, whether the request comes from the browser, a PAT over MCP, or Ask AI.
  Summaries served from the agent's shared cache (lists, counts, hidden Jobs, findings) are
  not impersonated. Instead, the hub SAR-filters them per user before they leave the hub.
- Never impersonate `system:*` users or groups. Groups get the `eddy:` prefix and must match
  `allowedGroupPrefixes`. `denyUserPrefixes` is enforced on the hub, and the agent checks again.
- Never cache or send Secret or ConfigMap data. Send summaries only. YAML reads use a kind
  allowlist, and the redactor runs before any output reaches AI or MCP.
- The hub filters every read through the SAR authorizer. That covers thread lists and
  AI and MCP tool results, not just the UI.
- Writes to protected clusters need a typed confirmation checked server-side: the UI's
  `confirm` field, or MCP's `confirm_cluster`.
- Honour proxy identity headers only when the TCP peer is in `trustedCIDRs` and the shared
  secret matches. Otherwise strip them.
- Passwords are hashed with argon2id (bcrypt at cost 12 or more is accepted). Logins are
  rate-limited and return generic errors, and unknown users are checked against a dummy hash.
- The session cookie is `__Host-`, Secure, HttpOnly and SameSite=Lax. Its id rotates on
  login. Unsafe methods need the CSRF header and an Origin check. `returnTo` must be a
  local path.
- PATs have the `eddy_pat_` format and are stored as an HMAC. They are shown once, must
  expire, are accepted only as a bearer header on `/mcp`, and are never logged.
- `/mcp` is POST-only and bearer-only (no cookies). Reject a foreign Origin or Host, send no
  CORS headers, apply size and rate caps, and audit every call with `via: mcp`.
- Ask AI is read-only: no write tool exists. Tool data is wrapped as untrusted content with
  nonce delimiters, and the rendered output contains no images and no raw HTML.
- Check the kill switches in the `eddy-runtime` flags file on every request: `ai.enabled`,
  `mcp.enabled`, `mcp.writes`.
- Dev fake login needs all of: the `-tags dev` build, `EDDY_DEV_MODE=1` and a loopback
  listener. It never appears in release images or in Helm.
- Agent local mode (`--local` / `EDDY_AGENT_LOCAL=1`) needs all of: `-tags dev`,
  `EDDY_DEV_MODE=1` and a loopback hub URL.
  - It acts as the kubeconfig identity: no impersonation; access checks are
    SelfSubjectAccessReviews.
  - It is read-only unless `--allow-writes`.
  - `--protect` matches stay read-only without `--allow-writes-protected`, which exists
    only as a flag, never as an env var.
  - It never ships in release images or in Helm.
- Compare tokens (agent tokens, PATs, API keys, the proxy secret) in constant time, and never
  log them.
- The UI uses a strict CSP with no inline script, `frame-ancestors 'none'` and `nosniff`.
  Never use `dangerouslySetInnerHTML`.

## Adding a Flux kind

These places change together:
1. The entry in `internal/flux/kinds.go` and its summarizer. Preset kinds (Karpenter,
   External Secrets) also set `Kind.Preset`.
2. The agent ClusterRole in `deploy/charts/eddy-agent`, inside the matching preset block for
   preset kinds. `internal/flux/chart_test.go` checks the verbs.
3. The viewer and operator rules in the eddy-agent chart's `userRBAC` (`eddy-agent.userRBAC.rules` in
   `templates/_helpers.tpl`) and `deploy/rbac/eddy-user-rbac.yaml`. A test checks they match.
4. The kind label and icon in `web/src/lib/kinds.ts`, and the project in `internal/flux/catalog.go` for a new API group.

## Style

- Go:
  - Log with `log/slog` in JSON.
  - Wrap errors with context (`fmt.Errorf("agent: watch %s: %w", gvr, err)`).
  - No panics on request paths.
  - Write table-driven tests. The store tests go through `storetest.Run`.
- Every Go file starts with a package doc or comment only where it adds meaning. Leave no
  commented-out code and no TODOs without an issue link.
- Web:
  - Use strict TypeScript.
  - Use TanStack Query for server state. Apply SSE deltas with `queryClient.setQueryData`.
  - Put every keybinding in `web/src/lib/keys.ts`.
  - Style with Tailwind v4 utilities. Every colour, radius and shadow comes from the shared tokens in `design/tokens.css` (also used by `landing/`); base layers live in `web/src/styles/app.css`. No inline styles for colour.
- Do not add a dependency without a reason in the PR. Prefer the standard library.
- Commits follow Conventional Commits (`type(scope): subject`). Do not add trailers.

## Subagents and models

- Set `model` on every subagent explicitly. Do not rely on inheriting it.
- **Opus:** auth, sessions and tokens; impersonation and RBAC filtering; the store schema and
  migrations; concurrency (the agent session, the SSE hub, the registry); MCP and AI
  guardrails; prompt-injection and security review; any work that makes a decision.
- **Sonnet:**
  - Read-only discovery and summaries (this repo, library docs, the Flux API).
  - Boilerplate that follows an agreed pattern (tests, handlers after the service is done,
    chart templates, UI components that follow an existing one).
  - Doc drafts.
- **Haiku:** quick lookups, such as finding a file or symbol, or checking whether something exists.
- Parallel agents work in separate directories: `internal/<pkg>`, `web/`, `deploy/`, `docs/<topic>`.
- Only the main session edits shared files:
  - `CLAUDE.md`, `README.md`, `SPEC.md`
  - `docs/api.md`, `docs/adr/`
  - `go.mod`
  - `Taskfile.yml`
  - `internal/{model,protocol,identity,fleet,store/store.go,config}`
- Agents never run `go mod tidy`. If a dependency is missing, they run a single `go get pkg@version` and report it.
- Agents do not commit; the main session commits.
- Agents report decisions as "Proposed SPEC/API/ADR updates", and the main session applies them.
