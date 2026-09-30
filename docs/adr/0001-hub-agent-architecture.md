# ADR-0001: Hub and agent architecture

- **Status:** Accepted
- **Date:** 2026-09-30

## Context

Eddy shows many Flux clusters in one keyboard-first UI. Existing tools either
need kubeconfigs for every cluster in one place (a large blast radius) or show
one cluster at a time.

## Decision

- One **hub** runs in a management cluster. One **agent** runs in each workload cluster.
- **Agents dial out** to the hub over a WebSocket. Workload API servers are never
  exposed, and the hub holds no workload-cluster credentials.
- **Kubernetes RBAC is the only permission model.** Every read and write in a workload
  cluster runs through a client that impersonates the signed-in user. The agent's own
  ServiceAccount is read-only, apart from `impersonate` and creating SubjectAccessReviews.
- **Agents send summaries** (`internal/model.Resource`), never raw objects. Secret and
  ConfigMap data never leave a cluster.
- **Flux kinds are read through dynamic informers** on unstructured objects, with served
  versions found by discovery. No Flux API Go modules are imported, so Eddy follows Flux
  releases without a rebuild.

## Consequences

- The hub needs one informer-backed cache per cluster, shared by all users. Reads are
  filtered by a per-user SubjectAccessReview cache (45 s).
- Writes are not pre-checked by the hub. The workload API server decides, and its audit log
  records the real user through impersonation.
- Losing the hub loses the UI, never cluster state.
