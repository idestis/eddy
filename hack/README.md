# hack/

Local development assets. Nothing here is used by the Helm charts or the release images.
See [docs/development.md](../docs/development.md) for the workflow.

- `dev.sh`: the local-mode workflow behind `task dev`, `task dev:config`, `task dev:hub`,
  `task dev:agent` and `task dev:web`. It generates `.dev/hub.yaml`, runs the hub and the
  agent under air (`.air.hub.toml`, `.air.agent.toml`) and Vite, prefixes their output and
  stops them together on Ctrl+C.
- `hub.dev.yaml`: a static hub config (one cluster, `dev`) for running the hub by hand:
  `EDDY_DEV_MODE=1 EDDY_DEV_AGENT_TOKEN=... go run -tags dev ./cmd/hub --config hack/hub.dev.yaml`.
  The tasks use the generated `.dev/hub.yaml` instead.
- `users.dev.yaml`: local users for dev and kind. Login `dev` / `eddy-dev-password`.
- `dev.key`: **DEV ONLY** random key (base64 text) for `auth.keyFile`. It is committed on
  purpose so the dev hub works out of the box. It is public, so it protects nothing: never
  reuse it. The charts generate a fresh key per install.
- `check-links.py`: fails when a relative link in a built static site (default `landing/`) points at a missing file.
