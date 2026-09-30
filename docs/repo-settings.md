# Repository Settings

Checklist for applying when making the repository public.

## Branch Protection

On the main branch:

- [ ] Require the `CI` check (job `ci-ok` in ci.yml, which aggregates every area job; skipped areas count as passed)
- [ ] Require `title` from the PR title workflow as a required status check
- [ ] Require at least 1 approval review (or 0 if self-merging)
- [ ] Require branches to be up to date before merging
- [ ] Require status checks to pass before merging
- [ ] Require code owner reviews for changes to CODEOWNERS or critical paths

## Security & Compliance

- [ ] Enable "Private vulnerability reporting" (Settings > Security > Code security and analysis)
- [ ] Enable "Dependabot alerts" (Settings > Security > Code security and analysis)
- [ ] Enable "Secret scanning" with push protection (Settings > Security > Code security and analysis)
  - [ ] Add custom pattern for Eddy PAT tokens: `eddy_pat_[0-9A-Za-z]{50}`
- [ ] Configure GHCR package visibility (Settings > Code security and analysis > GitHub Container Registry > Visibility)

## Deployment & Pages

- [ ] The `pages.yml` and `codeql.yml` jobs skip while the repo is private (`!github.event.repository.private`). They start working as soon as it is public.

- [ ] Set GitHub Pages source to "GitHub Actions" (Settings > Pages > Source)
- [ ] Verify pages URL in Settings > Pages (should auto-deploy from `pages.yml` workflow on landing/* changes)

## Metadata

- [ ] Add repository topics in Settings > About: `fluxcd`, `gitops`, `kubernetes`, `multi-cluster`, `mcp`, `claude-code`
- [ ] Update description and link in Settings > About
- [ ] Ensure Ko-fi link is visible in repository (via .github/FUNDING.yml, auto-displayed by GitHub)

## Release & Changelog

- [ ] First release: tag `v1.0.0` on main; the release workflow publishes git-cliff notes, images and charts
- [ ] Subsequent releases: `git tag vX.Y.Z` locally, push with `git push origin vX.Y.Z`
- [ ] Release workflow generates release notes from `cliff.toml` config automatically
- [ ] Update CHANGELOG.md after each release (normally done via task/Taskfile automation)

## Labels

The `labels.yml` workflow automatically applies area and type labels based on:

- **Area labels**: `area/*` derived from changed file paths (see `.github/labeler.yml`)
- **Type labels**: `feature`, `bug`, `performance`, etc. derived from Conventional Commit type in PR title

Workflows:
- `labels.yml`: runs on every PR (pull_request_target; never checks out PR code)
- `pr-title.yml`: validates PR title is a Conventional Commit (required check)
