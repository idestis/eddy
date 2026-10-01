# Performance baseline (before ADR-0006 P1)

This is the measurement that [ADR-0006](../adr/0006-progressive-loading.md) asks for before
any change: the hub as it was at commit `0fb7380`, driven by the synthetic load generator at
the target scale of 100 clusters × 15,000 resources (1.5M rows). P1 results are in
[p1.md](p1.md).

## Machine and method

- Apple M2 Max, 12 cores, 64 GiB, macOS 27.0.1, Go 1.27.1 (module `go 1.26`), `GOMAXPROCS` 12.
- Everything runs in one process: the hub (memory store, proxy auth, one replica), the fake
  agents and the simulated browsers. Fake agents generate their rows on demand, so the heap
  measured is the hub's. CPU figures are the whole process.
- **Load generator:** `cmd/eddy-loadgen` (build tag `dev`). Each fake agent is the real
  `internal/agent` session code with a synthetic source:
  - The kind mix is 50 % Jobs (mostly `completed`), 20 % Pods, then Deployments, Services,
    HelmReleases, Kustomizations, sources, CronJobs and inventory-only ConfigMaps, over 200
    namespaces with owners, labels, conditions and messages. Rows average 845 B of JSON,
    close to the owner's stage cluster (768 B).
  - Churn is 5 changes/s per cluster; a changed Job is replaced by a new one.
  - Fake SAR answers follow `internal/loadgen/synth` RBAC: admins list everything
    cluster-wide; team users list everything in their 10 of 200 namespaces only; mixed users
    list Flux kinds cluster-wide and everything else in their team's namespaces.
  - Users are 20 % admin, 50 % team and 30 % mixed. Each page view loads `/clusters`, one
    cluster's `/kinds` and `/resources`, with a 2 s think time and an SSE stream open.
  - "Old UI path" is what the current SPA does for the sidebar, the fleet palette and its
    "needs attention" group: fetch every connected cluster's `/resources`.
- **Benchmarks:** `go test ./internal/hub -run '^$' -bench . -benchmem -benchtime 10x
  -loadgen.full` on in-memory sessions (no network). They report hot-path costs without the
  agent round trips.

Commands:

```sh
go run -tags dev ./cmd/eddy-loadgen -clusters 100 -resources 15000 -users 50 -duration 60s
go run -tags dev ./cmd/eddy-loadgen -clusters 20  -resources 15000 -users 50 -duration 60s
go run -tags dev ./cmd/eddy-loadgen -clusters 100 -resources 15000 -users 3  -duration 30s
go test ./internal/hub -run '^$' -bench . -benchmem -benchtime 10x -loadgen.full
```

## Summary

**At the target scale the baseline does not work.** One user's fleet page asks 220,846
SubjectAccessReview checks: one per (kind, namespace) in 100 clusters. The shared SAR cache
holds 100,000 answers, so it thrashes, and every page load sends them again.

- The agents answer about 2,200 access requests per user and page. The hub's 200 frames/s
  limit per agent then stalls the read loop, pongs go unread, agents time out their pings
  and reconnect with a full 11 MB snapshot.
- With 3 users, 100 × 15k: 4,057 reconnects in a few minutes. With 50 users at 20 × 15k or
  100 × 15k: most agents are disconnected within 15 s of the users opening SSE, and almost
  every list returns 503.
- Even when it answers, the fleet page takes 17–22 s per user (budget 300 ms), and the old
  sidebar and palette path downloads up to 1.1 GB per user.

| Budget (ADR-0006) | Baseline at 100 × 15k |
|---|---|
| Fleet, first counts < 300 ms (cold SAR < 1 s) | 17.5–22 s warm, 20 s cold with 3 users; 7.2 s p50 / 42 s p95 with 50 users. Benchmark without network: 2.0–3.8 s ("warm" is still cold because the cache thrashes) |
| List, first page < 200 ms (hub < 30 ms) | Hub benchmark: 25 ms admin, 11.5 ms team for all 15k rows, plus 30 ms to encode 12.7 MB. Under load: 15–18 s, or 503 because the agent dropped |
| Palette, fleet results < 100 ms | No server search. The SPA needs every snapshot: 290 MiB for an admin in 186 s, 50 MiB (mixed) and 6.7 MiB (team) in 130–155 s |
| Hub memory per replica | 1.98 GiB live heap for the views (1,407 B per row); 2.5–9.2 GiB at the end of the runs, inflated by reconnect snapshots and SAR cache churn |

## Hub memory and ingest (100 × 15k)

| Metric | Value |
|---|---|
| Live heap, empty hub | 9.9 MiB |
| Live heap, every view loaded | 1.98 GiB |
| Heap per row | 1,407 B (benchmark: 1,297 B per decoded row) |
| Snapshot ingest, 100 clusters, 16 at a time | 14.3 s (2.3 s per cluster, wall) |
| Ingest CPU | 50.9 s |
| Agent→hub bytes during ingest | 1.18 GiB (845 B per row, no compression) |
| Steady churn, agent→hub | 448 KiB/s |
| Steady churn, hub CPU | 15 % of one core |
| Benchmark: decode and apply one 15k snapshot | 77 ms (164 MB/s) |

## Browser requests

With 3 users at 100 × 15k, one user at a time (cold, then 5 warm requests):

| Role | Request | Cold | Warm p50 | Size (no compression) |
|---|---|---|---|---|
| admin | `GET /clusters` | 20.0 s | 17.5 s | 198 KiB |
| admin | `GET …/kinds` | 15.2 s | 14.7 s | 2.5 KiB |
| admin | every cluster's `/resources` | 186 s | | 290 MiB (agents dropping meanwhile) |
| mixed | `GET /clusters` | 21.3 s | 20.4 s | 22 KiB |
| mixed | `GET …/resources` | 18.3 s | 15.1 s | 976 KiB |
| mixed | every cluster's `/resources` | 155 s | | 50.5 MiB |
| team | `GET /clusters` | 20.1 s | 22.0 s | 37 KiB |
| team | every cluster's `/resources` | 132 s | | 6.7 MiB |

The 50-user runs are worse: almost every `…/resources` and `…/kinds` returned 503
(`disconnected`), because the agents had already dropped.

| Run | `GET /clusters` p50 / p95 | `…/resources` statuses | Agent connections (expected) |
|---|---|---|---|
| 100 × 15k, 50 users | 7.2 s / 42.2 s | 139 × 503 | 928 (100) |
| 20 × 15k, 50 users | 3.3 s / 20.3 s | 1 × 200, 235 × 503 | 140 (20) |
| 100 × 15k, 3 users | 19.2 s / 19.2 s | 1 × 503 | 4,181 (100) |

## SAR checks

| Measure | Value |
|---|---|
| Checks for one fleet page, any role (benchmark, empty cache) | 220,846 |
| Checks answered per user per minute, 3 users | admin 514k, team 417k, mixed 346k |
| Checks answered per user per minute, 50 users | 32k–40k (agents mostly disconnected) |
| SAR cache capacity | 100,000 answers, shared by all users |

## SSE fan-out

- 50 users at 100 × 15k: no stream got its first `clusters` event within 15 s, because each
  open runs a cold fleet page (above). Hub CPU went from 15 % to 247 % of one core.
- 3 users: 12 KiB/s and 22 events/s per user. The cost was dominated by the `clusters`
  event recomputing every user's counts, not by `change` events (0.1/s per user, since most
  churn is in namespaces team users cannot see).
- Benchmark: filtering one 20-row delta costs 21–25 µs per user.

## Raw benchmark output (100 × 15k)

```
BenchmarkSnapshotIngest-12        10    77235850 ns/op  163.82 MB/s  15000 rows/op
BenchmarkViewBytesPerRow-12       10    60284104 ns/op  1297 B/row
BenchmarkClusters/admin-12        10  2003405533 ns/op
BenchmarkClusters/team-12         10  3212368075 ns/op
BenchmarkClusters/mixed-12        10  3772463962 ns/op
BenchmarkClustersColdSAR/admin-12 10  2204474946 ns/op  220846 checks/op
BenchmarkClustersColdSAR/team-12  10  2114837150 ns/op  220866 checks/op
BenchmarkClustersColdSAR/mixed-12 10  2049495867 ns/op  220842 checks/op
BenchmarkList/admin-12            10    25242550 ns/op  15000 rows/op
BenchmarkList/team-12             10    11460438 ns/op    751 rows/op
BenchmarkList/mixed-12            10    12272621 ns/op   1792 rows/op
BenchmarkListEncode-12            10    29740829 ns/op  844.6 B/row  12668506 bytes/op
BenchmarkKinds/admin-12           10    10891817 ns/op
BenchmarkKinds/team-12            10     7405533 ns/op
BenchmarkKinds/mixed-12           10     8307192 ns/op
BenchmarkFilterChange/admin-12    10       25046 ns/op
BenchmarkFilterChange/team-12     10       20775 ns/op
BenchmarkFilterChange/mixed-12    10       21208 ns/op
```
