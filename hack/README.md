# hack/

Local development assets. Nothing here is used by the Helm charts or the release images.

- `hub.dev.yaml`: hub config for `task dev:hub` (memory store, fake login, loopback listeners).
- `users.dev.yaml`: local users for dev and kind. Login `dev` / `eddy-dev-password`.
- `dev.key`: **DEV ONLY** random key (base64 text) for `auth.keyFile`. It is committed on
  purpose so `task dev:hub` works out of the box. It is public, so it protects nothing: never
  reuse it. The charts generate a fresh key per install.
