#!/usr/bin/env bash
# Local quickstart: a kind cluster "eddy" with Flux, a demo app, CloudNativePG with a
# one-instance PostgreSQL, two Eddy hub replicas and two agent replicas (ADR-0004).
# Safe to re-run: every step checks before it changes anything.
#
# The agent connects to the hub's in-cluster agents Service over TLS with a self-signed
# certificate (generated here, trusted by the agent via hub.caBundle). No plain ws:// is used.
set -euo pipefail

CLUSTER=eddy
CONTEXT="kind-${CLUSTER}"
HUB_NS=eddy
AGENT_NS=eddy-system
IMAGE_TAG=kind
# CloudNativePG operator, pinned (https://github.com/cloudnative-pg/cloudnative-pg/releases).
CNPG_VERSION=1.30.1
CNPG_MANIFEST="https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v${CNPG_VERSION}/cnpg-${CNPG_VERSION}.yaml"
DB_CLUSTER=eddy-db
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

log() { printf '\n==> %s\n' "$*"; }
need() { command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is required but not installed" >&2; exit 1; }; }

for tool in docker kind kubectl helm openssl; do need "$tool"; done

kctl() { kubectl --context "$CONTEXT" "$@"; }
hlm() { helm --kube-context "$CONTEXT" "$@"; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

log "kind cluster"
if kind get clusters 2>/dev/null | grep -qx "$CLUSTER"; then
  echo "cluster $CLUSTER already exists"
else
  kind create cluster --config "$ROOT/deploy/kind/kind-config.yaml"
fi

log "Flux"
if ! kctl get namespace flux-system >/dev/null 2>&1 || ! kctl -n flux-system get deploy kustomize-controller >/dev/null 2>&1; then
  if command -v flux >/dev/null 2>&1; then
    flux install --context "$CONTEXT"
  else
    kctl apply -f https://github.com/fluxcd/flux2/releases/latest/download/install.yaml
  fi
fi
kctl -n flux-system wait deploy --all --for=condition=Available --timeout=5m

log "Demo workload (podinfo) so there is something to look at"
kctl apply -f - <<'YAML'
apiVersion: v1
kind: Namespace
metadata:
  name: podinfo
---
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: podinfo
  namespace: flux-system
spec:
  interval: 1m
  url: https://github.com/stefanprodan/podinfo
  ref:
    branch: master
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: podinfo
  namespace: flux-system
spec:
  interval: 5m
  path: ./kustomize
  prune: true
  targetNamespace: podinfo
  sourceRef:
    kind: GitRepository
    name: podinfo
YAML

log "Container images"
docker build --target hub -t "ghcr.io/idestis/eddy-hub:${IMAGE_TAG}" "$ROOT"
docker build --target agent -t "ghcr.io/idestis/eddy-agent:${IMAGE_TAG}" "$ROOT"
kind load docker-image --name "$CLUSTER" \
  "ghcr.io/idestis/eddy-hub:${IMAGE_TAG}" "ghcr.io/idestis/eddy-agent:${IMAGE_TAG}"

log "CloudNativePG operator ${CNPG_VERSION}"
kctl apply --server-side -f "$CNPG_MANIFEST"
kctl -n cnpg-system rollout status deploy/cnpg-controller-manager --timeout=5m

log "PostgreSQL for the hub (CloudNativePG Cluster ${DB_CLUSTER}, one instance)"
kctl create namespace "$HUB_NS" --dry-run=client -o yaml | kctl apply -f -
# The operator's webhook can take a few seconds after the rollout to accept requests.
for attempt in $(seq 1 30); do
  if kctl apply -f - <<YAML; then
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: ${DB_CLUSTER}
  namespace: ${HUB_NS}
spec:
  instances: 1
  imageName: ghcr.io/cloudnative-pg/postgresql:17
  storage: {size: 1Gi}
  bootstrap:
    initdb: {database: eddy, owner: eddy}
YAML
    break
  fi
  [[ $attempt == 30 ]] && { echo "error: could not create the CloudNativePG Cluster" >&2; exit 1; }
  sleep 2
done
kctl -n "$HUB_NS" wait "cluster.postgresql.cnpg.io/${DB_CLUSTER}" --for=condition=Ready --timeout=10m
# CNPG writes the app user's connection string to the Secret <cluster>-app, key "uri".
kctl -n "$HUB_NS" get secret "${DB_CLUSTER}-app" -o jsonpath='{.data.uri}' >/dev/null

log "Hub dev users and agent TLS certificate"
kctl -n "$HUB_NS" create secret generic eddy-users-dev \
  --from-file=users.yaml="$ROOT/hack/users.dev.yaml" --dry-run=client -o yaml | kctl apply -f -

if ! kctl -n "$HUB_NS" get secret eddy-agents-tls >/dev/null 2>&1; then
  svc="eddy-hub-agents"
  openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
    -keyout "$tmp/tls.key" -out "$tmp/tls.crt" -subj "/CN=${svc}" \
    -addext "subjectAltName=DNS:${svc},DNS:${svc}.${HUB_NS}.svc,DNS:${svc}.${HUB_NS}.svc.cluster.local" \
    -addext "basicConstraints=critical,CA:TRUE" 2>/dev/null
  kctl -n "$HUB_NS" create secret tls eddy-agents-tls --cert="$tmp/tls.crt" --key="$tmp/tls.key"
fi
kctl -n "$HUB_NS" get secret eddy-agents-tls -o jsonpath='{.data.tls\.crt}' | base64 -d >"$tmp/ca.crt"

log "Eddy hub"
cat >"$tmp/hub-values.yaml" <<YAML
publicURL: http://localhost:8080
image: {tag: ${IMAGE_TAG}, pullPolicy: Never}
replicaCount: 2
store:
  driver: postgres
  postgres:
    dsnSecret: {name: ${DB_CLUSTER}-app, key: uri}
users: {existingSecret: eddy-users-dev}
agentTLS: {secretName: eddy-agents-tls}
clusters:
  - name: kind
    displayName: kind
    environment: Development
    color: "#0F766E"
    order: 1
YAML
hlm upgrade --install eddy-hub "$ROOT/deploy/charts/eddy-hub" \
  --namespace "$HUB_NS" --values "$tmp/hub-values.yaml" --wait --timeout 5m

log "Eddy agent"
token="$(kctl -n "$HUB_NS" get secret eddy-agent-kind -o jsonpath='{.data.token}' | base64 -d)"
hlm upgrade --install eddy-agent "$ROOT/deploy/charts/eddy-agent" \
  --namespace "$AGENT_NS" --create-namespace \
  --set image.tag="$IMAGE_TAG" --set image.pullPolicy=Never \
  --set cluster.name=kind --set replicaCount=2 \
  --set "hub.url=wss://eddy-hub-agents.${HUB_NS}.svc:443/agent/v1/connect" \
  --set-file hub.caBundle="$tmp/ca.crt" \
  --set-string token.value="$token" \
  --wait --timeout 5m

log "User RBAC (dev user is in group eddy:platform, so it gets eddy-operator)"
kctl apply -f "$ROOT/deploy/rbac/eddy-user-rbac.yaml"

cat <<EOF

Eddy is running: 2 hub replicas on CloudNativePG (${DB_CLUSTER}), 2 agent replicas.

  kubectl --context ${CONTEXT} -n ${HUB_NS} port-forward svc/eddy-hub 8080:80

Then open http://localhost:8080 and sign in:
  username: dev
  password: eddy-dev-password    (dev only, see hack/users.dev.yaml)

Tear down with: task kind:down
EOF
