#!/usr/bin/env bash
# The checks CI runs, one function per CI job. GitHub Actions calls these, and so does
# `task ci` locally, so the two cannot drift apart.
#
#   hack/ci.sh all             every job below, in order
#   hack/ci.sh go|vuln|web|landing|charts|docker
#
# Local extras: the go job starts a throwaway postgres:17 container for the store tests when
# EDDY_TEST_POSTGRES_DSN is unset and Docker is available (PG=0 skips Postgres). DOCKER=0
# skips the image builds in `all`.
set -euo pipefail

cd "$(dirname "$0")/.."

STATICCHECK="${STATICCHECK:-honnef.co/go/tools/cmd/staticcheck@v0.8.1}"
GOVULNCHECK="${GOVULNCHECK:-golang.org/x/vuln/cmd/govulncheck@v1.1.4}"
PG_CONTAINER=eddy-ci-pg

step() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

start_pg() {
  if [ -n "${EDDY_TEST_POSTGRES_DSN:-}" ] || [ "${PG:-1}" = "0" ]; then
    return
  fi
  if ! command -v docker >/dev/null || ! docker info >/dev/null 2>&1; then
    echo "note: Docker not available, Postgres store tests will be skipped"
    return
  fi
  docker rm -f "$PG_CONTAINER" >/dev/null 2>&1 || true
  docker run -d --rm --name "$PG_CONTAINER" -e POSTGRES_PASSWORD=eddy -p 127.0.0.1:55499:5432 postgres:17 >/dev/null
  trap 'docker stop "$PG_CONTAINER" >/dev/null 2>&1 || true' EXIT
  for _ in $(seq 1 30); do
    docker exec "$PG_CONTAINER" pg_isready -U postgres >/dev/null 2>&1 && break
    sleep 1
  done
  export EDDY_TEST_POSTGRES_DSN="postgres://postgres:eddy@127.0.0.1:55499/postgres?sslmode=disable"
}

job_go() {
  step "go: gofmt"
  if [ -n "$(gofmt -l cmd internal)" ]; then gofmt -l cmd internal; exit 1; fi
  step "go: vet"
  go vet ./...
  go vet -tags dev ./...
  step "go: staticcheck"
  go run "$STATICCHECK" ./...
  go run "$STATICCHECK" -tags dev ./...
  start_pg
  step "go: test -race${EDDY_TEST_POSTGRES_DSN:+ (with Postgres)}"
  go test -race -count=1 ./...
  go test -race -count=1 -tags dev ./internal/agent/... ./internal/devlocal/... ./cmd/...
}

job_vuln() {
  step "govulncheck"
  go run "$GOVULNCHECK" ./...
}

pnpm_job() {
  (
    cd "$1"
    step "$1: install"
    CI=true pnpm install --frozen-lockfile
    step "$1: lint, typecheck, test, build"
    pnpm run lint
    pnpm run typecheck
    if [ "$1" = web ]; then pnpm run test; fi
    pnpm run build
    if [ "$1" = landing ]; then pnpm run check:links; fi
  )
}

job_web() { pnpm_job web; }
job_landing() { pnpm_job landing; }

job_charts() {
  local agent=(--set cluster.name=test --set hub.url=wss://hub.example.com/agent/v1/connect)
  local join=eddy_join_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
  step "charts: lint"
  helm lint deploy/charts/eddy-hub deploy/charts/eddy-agent --set store.postgres.dsnSecret.name=eddy-db
  step "charts: template"
  helm template eddy deploy/charts/eddy-hub --set store.postgres.dsnSecret.name=eddy-db >/dev/null
  helm template eddy deploy/charts/eddy-hub --set store.driver=memory >/dev/null
  if helm template eddy deploy/charts/eddy-hub >/dev/null 2>&1; then
    echo "hub chart without a DSN Secret must fail"; exit 1
  fi
  helm template eddy deploy/charts/eddy-agent "${agent[@]}" --set token.value=x >/dev/null
  helm template eddy deploy/charts/eddy-agent "${agent[@]}" --set token.existingSecret=agent-token \
    --set replicaCount=3 --set limits.qps=30 --set 'impersonation.groups={eddy:authenticated,eddy:platform}' >/dev/null
  helm template eddy deploy/charts/eddy-agent "${agent[@]}" --set joinToken="$join" >/dev/null
  helm template eddy deploy/charts/eddy-agent "${agent[@]}" --set 'watch.presets={karpenter,externalSecrets}' --set token.value=x >/dev/null
  if helm template eddy deploy/charts/eddy-agent --set cluster.name=test --set joinToken="$join" --set token.value=x >/dev/null 2>&1; then
    echo "agent chart with both a join token and a token must fail"; exit 1
  fi
  helm template eddy deploy/charts/eddy-hub --set store.postgres.dsnSecret.name=eddy-db --set onboarding.enabled=false >/dev/null
  helm template eddy deploy/charts/eddy-hub --set store.postgres.dsnSecret.name=eddy-db-app --set store.postgres.dsnSecret.key=uri \
    --set replicaCount=3 --set 'networkPolicy.egress.postgresTo[0].podSelector.matchLabels.cnpg\.io/cluster=eddy-db' \
    --set credentialsSecret=creds --set oauth2Proxy.enabled=true --set oauth2Proxy.existingSecret=o2p \
    --set ingress.ui.enabled=true --set 'ingress.ui.hosts={eddy.example.com}' \
    --set ingress.agents.enabled=true --set 'ingress.agents.hosts={agents.example.com}' \
    --set networkPolicy.enabled=true --set 'clusters[0].name=prod' >/dev/null
  helm template eddy deploy/charts/eddy-hub --set store.driver=memory --set 'onboarding.admins.groups={eddy:platform}' |
    grep -q 'name: eddy-cluster-admin' || { echo "onboarding.admins must create eddy-cluster-admin"; exit 1; }
  if helm template eddy deploy/charts/eddy-hub --set store.driver=memory | grep -q 'name: eddy-cluster-admin'; then
    echo "eddy-cluster-admin must not exist without onboarding.admins"; exit 1
  fi
  for bad in platform system:masters; do
    if helm template eddy deploy/charts/eddy-hub --set store.driver=memory --set "onboarding.admins.groups={$bad}" >/dev/null 2>&1; then
      echo "onboarding.admins.groups=$bad must fail"; exit 1
    fi
  done
  step "charts: CRD copy matches config/crd"
  diff -u config/crd/gitops.eddy.dev_clusters.yaml deploy/charts/eddy-hub/crds/gitops.eddy.dev_clusters.yaml
}

job_docker() {
  for target in hub agent; do
    step "docker: build $target"
    docker build --target "$target" -t "eddy-$target:ci" .
  done
}

case "${1:-all}" in
  go) job_go ;;
  vuln) job_vuln ;;
  web) job_web ;;
  landing) job_landing ;;
  charts) job_charts ;;
  docker) job_docker ;;
  all)
    job_go
    job_vuln
    job_web
    job_landing
    job_charts
    if [ "${DOCKER:-1}" != "0" ]; then job_docker; fi
    step "ci: all checks passed"
    ;;
  *) echo "usage: $0 [all|go|vuln|web|landing|charts|docker]" >&2; exit 2 ;;
esac
