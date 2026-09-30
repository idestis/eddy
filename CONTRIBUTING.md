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
- Docker and kind (for end-to-end testing only)

```sh
task check        # everything CI runs: lint, tests, UI build
task dev:hub      # hub with the memory store and fake login
task ui:dev       # UI with hot reload
```

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
