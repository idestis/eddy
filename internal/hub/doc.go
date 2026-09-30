// Package hub is the Eddy hub: the process that runs in the management
// cluster, accepts agent connections and serves the web UI, the HTTP API,
// server-sent events and the MCP endpoint.
//
// The main pieces are:
//
//   - Registry (registry.go, registry_kube.go): the clusters that may connect,
//     from Cluster CRs and their token Secrets, or from hub.yaml's
//     staticClusters for local development.
//   - The agent endpoint (agentconn.go, session.go): one WebSocket session per
//     cluster, holding that cluster's resource summaries and correlating
//     requests with responses and log streams.
//   - The authorizer (authorizer.go): a per-user SubjectAccessReview cache.
//     Every read the hub serves is filtered through it.
//   - The fleet service (fleet.go): the fleet.Service implementation that the
//     HTTP API, MCP and Ask AI share. It impersonates the caller on every
//     agent request, enforces protected-cluster confirmation and audits writes.
//   - The HTTP API (api*.go, sse.go) with its middleware (middleware.go,
//     headers.go), metrics (metrics.go) and background jobs (janitor.go).
//
// See docs/api.md for the contract and docs/adr for the decisions behind it.
package hub
