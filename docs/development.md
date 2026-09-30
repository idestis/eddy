# Developing Eddy locally

`task dev` runs the whole of Eddy on your machine: the hub, an agent and the web UI, with hot
reload for all three. Nothing is installed in any cluster. The agent runs in **local mode**:
it reads the kubeconfig contexts you choose and talks to their API servers as **your
kubeconfig identity**, so you can debug the UI against real data, read-only by default.

For a full in-cluster install (the hub and the agent as Helm releases on kind), see the
quickstart in [install.md](install.md).

## Prerequisites

- Go 1.26 or later. The module asks for toolchain 1.27, which `go` downloads if needed.
- Node 22 or later and pnpm (`corepack enable` gives you the version pinned in
  `web/package.json`).
- [Task](https://taskfile.dev) 3.
- Optional: [air](https://github.com/air-verse/air) on your `PATH`
  (`go install github.com/air-verse/air@latest`). Without it, the tasks run a pinned
  version with `go run`, which is slower on the first start. air 1.62 or later stops a
  binary as soon as it exits; older versions always wait the full kill delay (2 s).
- Optional: [Delve](https://github.com/go-delve/delve) (`dlv`) for debugging.
- A kubeconfig whose contexts you can already use with `kubectl`. If a context needs a
  login (for example `aws sso login`), do that first.

## Quick start

```sh
cp .env.example .env               # optional: keep your settings in .env
task dev                           # your current kubeconfig context, read-only
task dev CONTEXTS=kind-eddy,staging=stg   # several contexts; "ctx=name" renames one
```

Open <http://localhost:5173> and sign in, either:

- with the dev fake login: <http://localhost:5173/auth/dev/login?user=me&groups=platform>
  (any user, groups become `eddy:<group>`), or
- with the local user `dev` / `eddy-dev-password` (from `hack/users.dev.yaml`).

Ctrl+C stops the hub, the agent and Vite together. Output is prefixed with `hub  |`,
`agent|` and `web  |`.

`task dev` does four things (`hack/dev.sh` has the details):

1. `task dev:config` writes `.dev/hub.yaml` with `go run -tags dev ./cmd/hub dev-config`:
   loopback listeners, the memory store (or sqlite with `SQLITE=1`), local users, the dev
   fake login, MCP, and one static cluster per context. It also creates the shared agent
   token in `.dev/agent-token` the first time. It reads your kubeconfig file but contacts
   no cluster, and it leaves an unchanged file alone.
2. The hub, built with `-tags dev`, under air (`.air.hub.toml`).
3. The agent, built with `-tags dev`, under air (`.air.agent.toml`), in local mode.
4. Vite on `:5173`, which proxies `/api`, `/auth` and `/mcp` to the hub on `:8080`.

`.dev/` is gitignored. It holds `hub.yaml`, `agent-token`, `bin/` (the air builds),
`tmp/` and, with `SQLITE=1`, `eddy.db`. Delete it to start from scratch.

## Settings

Task variables on the command line win over your shell's environment, which wins over
`.env`. The agent reads the `EDDY_AGENT_*` variables directly, and its flags win over them.

| Task variable | Environment / `.env` | Agent flag | Default | Meaning |
|---|---|---|---|---|
| `CONTEXTS` | `EDDY_AGENT_CONTEXTS` | `--contexts` | current context | Comma-separated contexts. `ctx=name` sets the cluster name. |
| `ALLOW_WRITES=1` | `EDDY_AGENT_ALLOW_WRITES` | `--allow-writes` | off | Allow reconcile, suspend and resume, as your identity. |
| `ALLOW_WRITES_PROTECTED=1` | none, on purpose | `--allow-writes-protected` | off | Also allow writes on protected contexts. Needs `ALLOW_WRITES=1`. |
| `PROTECT` | `EDDY_AGENT_PROTECT` | `--protect` | `(?i)prod` | Regex of contexts or cluster names that are protected. |
| `LOCAL=0` | `EDDY_AGENT_LOCAL` | `--local` | `1` in tasks | `0` runs the real, impersonating agent (see [kind](#testing-the-impersonating-agent-on-kind)). |
| `SQLITE=1` | | | memory | Keep sessions and threads in `.dev/eddy.db` across restarts. |
| | `EDDY_LOG_LEVEL` | | `info` | `debug`, `info`, `warn` or `error`, for the hub and the agent. |
| | `KUBECONFIG` | `--kubeconfig` | `~/.kube/config` | Which kubeconfig to read. |

Cluster names come from the context names, made DNS-label safe: lowercase, with other
characters replaced by `-`. An EKS ARN context uses its last segment, so
`arn:aws:eks:eu-west-2:123456789012:cluster/prod-eu` becomes `prod-eu`. Two contexts that
map to the same name are an error; rename one with `ctx=name`.

## What local mode does, and its guardrails

A normal agent runs in the cluster and impersonates the signed-in Eddy user, so the
cluster's RBAC decides what each person can do. Local mode is different, and exists only
for development:

- **Dev builds only.** The code is compiled only with `-tags dev`. A release binary has no
  `--local` flag and refuses `EDDY_AGENT_LOCAL=1` with "local mode requires a dev build".
- **Explicit activation.** It needs all of: a dev build, `EDDY_DEV_MODE=1`, `--local` (or
  `EDDY_AGENT_LOCAL=1`) and a hub URL on a loopback address (`127.0.0.1`, `localhost` or
  `[::1]`). Anything else refuses to start.
- **Your identity, not the Eddy user's.** There is no impersonation. Every read and action
  runs with your kubeconfig credentials, whoever is signed in to the hub. Access checks are
  SelfSubjectAccessReviews of your identity, so the UI shows what *you* may do. The agent
  logs this at start-up for every context, the hub logs it when the agent connects, and the
  UI shows a "Local mode" banner. Requests for `system:` users or groups are still refused.
- **Read-only by default.** Reconcile, suspend and resume return 403 "read-only local mode
  (start with --allow-writes)" until you pass `ALLOW_WRITES=1`. The hub also refuses them
  for a read-only cluster, and the UI hides the buttons.
- **Protected contexts stay read-only.** Contexts or cluster names matching `PROTECT`
  (default `(?i)prod`) are marked protected on the hub (writes need the typed cluster name)
  and stay read-only even with `ALLOW_WRITES=1`. Only `ALLOW_WRITES_PROTECTED=1` on the
  command line lifts that, with a loud warning. It cannot be set from `.env`.
- **Loopback everywhere.** The generated hub listens on `127.0.0.1` only, and the dev fake
  login works only from a loopback peer.

What you can see is still limited by your kubeconfig identity's own RBAC, and Secrets and
ConfigMaps are never read, as with a normal agent. A context that fails (expired
credentials, an unreachable API server) is logged and does not stop the others.

## Running the parts separately

```sh
task dev:config CONTEXTS=kind-eddy   # regenerate .dev/hub.yaml
task dev:hub                         # hub only (regenerates the config first)
task dev:agent CONTEXTS=kind-eddy    # agent only
task dev:web                         # Vite only
task dev:mock                        # Vite with mock data (VITE_MOCK=1), no hub or agent
```

Pass the same `CONTEXTS` and `PROTECT` to `dev:hub` and `dev:agent`: both regenerate
`.dev/hub.yaml`, and the hub restarts when it changes.

## Hot reload

- The hub rebuilds when anything it imports changes under `cmd/`, `internal/` or `hack/`,
  and when `.dev/hub.yaml` changes. `hack/users.dev.yaml` is also hot-reloaded by the hub
  itself.
- The agent rebuilds only for the packages it imports (`cmd/agent`, `internal/agent`,
  `internal/flux` and the shared contracts).
- On a rebuild air sends SIGINT and waits up to 2 s. The agent drops its connection to
  the hub and reconnects about a second after the new binary starts, with a fresh snapshot.
- Test files (`_test.go`), `web/` and `landing/` do not trigger Go rebuilds. Vite handles
  `web/` with its own hot module replacement.

## Ask AI in local dev

Ask AI is off unless you choose a provider. Set it in `.env`, then run `task dev` again:

- Anthropic: `EDDY_AI_PROVIDER=anthropic`, `ANTHROPIC_API_KEY=...`, and optionally
  `EDDY_AI_MODEL` (default `claude-haiku-4-5-20251001`).
- Amazon Bedrock: `EDDY_AI_PROVIDER=bedrock`, `EDDY_BEDROCK_REGION`,
  `EDDY_BEDROCK_MODEL_ID` (an inference profile id or ARN, for example
  `eu.anthropic.claude-haiku-4-5-20251001-v1:0`), and optionally
  `EDDY_BEDROCK_GUARDRAIL_ID` with `EDDY_BEDROCK_GUARDRAIL_VERSION`. Credentials come from
  the AWS default chain, so use `AWS_PROFILE` (SSO works). Never put AWS access keys in
  `.env`.

`task dev:config` fails with a clear message when the chosen provider is missing a
required setting. The generated `ai:` block names only the variable (`ANTHROPIC_API_KEY`),
never the key itself.

## Debugging

- **Logs:** `EDDY_LOG_LEVEL=debug task dev` logs every agent request and hub decision.
- **Delve:** the air builds use `-gcflags=all=-N -l`, so you can attach to the running
  binaries: `dlv attach $(pgrep -f .dev/bin/eddy-hub)` or
  `dlv attach $(pgrep -f .dev/bin/eddy-agent)`. A rebuild replaces the process, so attach
  again after one. To debug start-up, stop the task and run the binary under Delve
  yourself, for example
  `EDDY_DEV_MODE=1 EDDY_DEV_AGENT_TOKEN=$(cat .dev/agent-token) dlv exec .dev/bin/eddy-hub -- --config .dev/hub.yaml`.
- **The API without the UI:** sign in with the fake login and reuse the cookie:

  ```sh
  curl -c /tmp/eddy.jar -o /dev/null 'http://127.0.0.1:8080/auth/dev/login?user=me&groups=platform'
  curl -b /tmp/eddy.jar http://127.0.0.1:8080/api/v1/clusters
  ```

  Unsafe methods also need the `X-Eddy-CSRF` header (the `csrf` field of `/api/v1/me`) and
  `Origin: http://localhost:5173`.
- **Vite proxy:** the browser only talks to `:5173`. If the UI shows network errors, check
  that the hub listens on `127.0.0.1:8080` (`hub  |` lines) and see `web/vite.config.ts`.
- **Mock mode:** `task dev:mock` needs no hub, agent or cluster. Use it for pure UI work.
- **Agent health:** local mode runs no health server. With `LOCAL=0` it is on
  `127.0.0.1:18081` (`/healthz`, `/readyz`); set `EDDY_HEALTH_ADDR` to move it.

## Testing the impersonating agent on kind

Local mode skips impersonation, so test that path with the real agent against a local kind
cluster:

```sh
kind create cluster --name eddy
flux install                      # or any Flux install you like
kubectl config use-context kind-eddy
task dev LOCAL=0
```

With `LOCAL=0` the agent is the normal one: it impersonates the signed-in user and answers
access checks with SubjectAccessReviews. It serves only the current context, runs with your
kubeconfig credentials (which on kind can impersonate), and refuses to start when that
context matches `PROTECT`. Give your dev user RBAC in the cluster with the examples in
[install.md](install.md), for example by binding `eddy:platform`.

## Troubleshooting

- **"local mode needs EDDY_DEV_MODE=1":** you ran the agent by hand. Use the tasks, or set
  `EDDY_DEV_MODE=1`.
- **A cluster stays disconnected:** look for `agent|` errors for that context, usually
  expired credentials. Log in again; the agent restarts on the next rebuild, or restart
  `task dev`.
- **"address already in use":** another process holds `:8080`, `:8443`, `:9090` or
  `:5173`. Stop it, or stop the other `task dev`.
- **Cluster names changed:** you changed `CONTEXTS`. Keep `CONTEXTS` the same for the hub
  and the agent, or run `task dev` so both use one value.
