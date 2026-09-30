# Contributing to Eddy

Thanks for your interest. Eddy is small and opinionated, so please open an issue to
discuss larger changes before you write code.

## Ground rules

- **Security invariants are non-negotiable.** They are listed in [CLAUDE.md](CLAUDE.md#security-invariants-never-break)
  and [docs/security.md](docs/security.md). A change that weakens one needs an ADR in
  `docs/adr/` first.
- **Kubernetes RBAC is the only permission model.** Do not add Eddy-specific roles.
- **Keep dependencies few.** Explain any new dependency in the PR description.

## Setup

Requirements:
- Go 1.26+
- Node 22+ with pnpm (`corepack enable`)
- [Task](https://taskfile.dev)
- Optional: [air](https://github.com/air-verse/air) for Go hot reload. The tasks fall back to
  `go run` of a pinned air version when it is not installed.
- Optional: Docker, for the PostgreSQL store tests (`task dev:pg`) and for kind.

```sh
cp .env.example .env   # local settings; .env is gitignored and loaded by Task
task check             # everything CI runs: lint, tests, UI build
```

## Run Eddy locally against your own clusters

You can work on the whole UI without installing anything into a cluster. `task dev` runs the
hub, one agent and the web app on your machine:
- The hub and agent use hot reload (air), and the web app runs on Vite at
  http://localhost:5173.
- The agent uses **your current kubeconfig** in *local mode*.

```sh
task dev                                   # your current kube context
task dev CONTEXTS=staging-eu,prod-eu       # several contexts, one cluster each in the UI
```

Sign in with the dev login on the sign-in page, or with `dev` / `eddy-dev-password`.

**Local mode** (`EDDY_AGENT_LOCAL=1`, which `task dev` sets for you) changes how the agent
acts:
- **Identity:** it uses your kubeconfig identity for every request instead of
  impersonating the signed-in user. The UI shows exactly what *you* can see, and access
  checks become SelfSubjectAccessReviews.
- **Read-only by default:** `ALLOW_WRITES=1` enables reconcile, suspend and resume.
  Contexts matching `EDDY_AGENT_PROTECT` (default `(?i)prod`) stay read-only even then.
- **Dev builds only:** local mode exists only in `-tags dev` builds. It needs
  `EDDY_DEV_MODE=1` and a hub on localhost, so it can never switch on in a release image.
  The UI shows a banner while it is active.

| Variable (in `.env` or the shell) | Default | Meaning |
|---|---|---|
| `EDDY_AGENT_LOCAL` | `1` in `task dev` | Use your kube context as the agent's own identity. Set `LOCAL=0` to run the real impersonating agent, for example against kind. |
| `EDDY_AGENT_CONTEXTS` | current context | Comma-separated contexts. `ctx=name` renames one. |
| `EDDY_AGENT_ALLOW_WRITES` | `0` | Allow actions |
| `EDDY_AGENT_PROTECT` | `(?i)prod` | Contexts that stay read-only |
| `EDDY_LOG_LEVEL` | `info` | `debug` for verbose hub and agent logs |

Other entry points:

```sh
task dev:mock   # UI only, with mocked clusters (no Go, no kube access)
task dev:hub    # hub alone
task dev:agent  # agent alone
task dev:web    # Vite alone
```

Debugging tips, such as attaching `dlv` to the air-built binaries and the Vite proxy
settings, are in [docs/development.md](docs/development.md).

> Local mode runs with **your** credentials. Point it at production only in read-only mode,
> and never share a local hub with other people.

## Pull requests

- Keep each PR focused on one change. Include tests with it: table-driven for Go, vitest for the UI.
- Use [Conventional Commits](https://www.conventionalcommits.org) (`feat(agent): …`,
  `fix(ui): …`).
- Update the docs when behaviour changes. That includes `docs/api.md` for API changes and
  the README roadmap table.

## Adding a Flux kind

See "Adding a Flux kind" in [CLAUDE.md](CLAUDE.md).

## Reporting security issues

Do not open a public issue. Follow [SECURITY.md](SECURITY.md).
