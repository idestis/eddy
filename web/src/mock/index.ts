// Mock mode (`VITE_MOCK=1 pnpm dev`): replaces fetch and EventSource for
// same-origin /api and /auth URLs with the in-memory hub in server.ts, so the
// UI runs without the Go backend. main.tsx imports this module only when
// VITE_MOCK is set, so production builds never include it.

import { isAttentionRow } from "../api/delta";
import type { ChangeEvent, WorkloadLogEntry } from "../api/types";
import { logLine } from "./fixtures";
import { MockHub } from "./server";

const LATENCY_MS = 120;
const TICK_MS = 6_000;

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

const isHubPath = (url: URL) =>
  url.origin === window.location.origin &&
  (url.pathname.startsWith("/api/") || url.pathname.startsWith("/auth/"));

/** A minimal EventSource that is fed by the mock hub instead of the network. */
class MockEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readonly url: string;
  readonly withCredentials = false;
  readyState = 0;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private stop: () => void = () => {};

  constructor(url: string, start: (source: MockEventSource) => () => void) {
    super();
    this.url = url;
    setTimeout(() => {
      if (this.readyState === 2) return;
      this.readyState = 1;
      this.dispatchEvent(new Event("open"));
      this.stop = start(this);
    }, LATENCY_MS);
  }

  send(event: string, data: unknown): void {
    if (this.readyState !== 1) return;
    this.dispatchEvent(new MessageEvent(event, { data: JSON.stringify(data) }));
  }

  close(): void {
    this.readyState = 2;
    this.stop();
  }
}

export function installMock(): void {
  const extra = Number.parseInt(import.meta.env.VITE_MOCK_ROWS ?? "0", 10) || 0;
  const hub = new MockHub(extra);
  // An older hub without GET …/graph: the UI builds the graph in the browser.
  hub.noGraph = import.meta.env.VITE_MOCK_NO_GRAPH === "1";
  // A hub from before ADR-0006 P1/P2: no /search, /attention, `kinds` or `watch=`.
  hub.oldHub = import.meta.env.VITE_MOCK_OLD_HUB === "1";
  const realFetch = window.fetch.bind(window);
  const RealEventSource = window.EventSource;

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = new Request(input, init);
    const url = new URL(req.url, window.location.href);
    if (!isHubPath(url)) return realFetch(input, init);
    await sleep(LATENCY_MS);
    const text = await req.text();
    return hub.handle(req.method, url, text ? JSON.parse(text) : undefined, req.headers);
  };

  // A construct trap lets `new EventSource(url)` return our mock for hub URLs
  // and a real EventSource for anything else.
  window.EventSource = new Proxy(RealEventSource, {
    construct(target, [input, init]: [string | URL, EventSourceInit | undefined]) {
      const url = new URL(String(input), window.location.href);
      if (!isHubPath(url)) return new target(input, init);
      return new MockEventSource(url.href, (source) => startStream(hub, url, source));
    },
  });

  setInterval(() => hub.tick(), TICK_MS);
  // Mock-only controls for trying the disconnected state from the console:
  // __eddyMock.disconnect("staging"), __eddyMock.reconnect("staging").
  (window as unknown as { __eddyMock: unknown }).__eddyMock = {
    disconnect: (name: string) => hub.setConnected(name, false),
    reconnect: (name: string) => hub.setConnected(name, true),
    tick: () => hub.tick(),
  };
  console.info(`[eddy] mock mode: ${hub.clusters.length} clusters, in-memory hub`);
}

const MAX_LOG_PODS = 20;
/** Log lines from fixtures start with an ISO timestamp; workload entries carry it as `ts`. */
const splitTs = (line: string) => {
  const m = line.match(/^(\S+) (.*)$/);
  return m ? { ts: m[1], line: m[2] ?? "" } : { line };
};

/** GET …/workloads/{kind}/{ns}/{name}/logs: `pods`, then per-pod tails, then followed lines. */
function workloadLogs(hub: MockHub, url: URL, source: MockEventSource, m: RegExpMatchArray): () => void {
  const [, cluster = "", kind = "", ns = "", name = ""] = m.map(decodeURIComponent);
  const q = url.searchParams;
  const only = q
    .getAll("pods")
    .flatMap((p) => p.split(","))
    .filter(Boolean);
  const container = q.get("container") ?? "";
  const allContainers = q.get("allContainers") !== "false";
  const all = hub.workloadPods(cluster, kind, ns === "_" ? "" : ns, name);
  const pods = all.filter((p) => !only.length || only.includes(p.name)).slice(0, MAX_LOG_PODS);
  source.send("pods", {
    pods: pods.map((p) => ({
      name: p.name,
      containers: p.containers ?? [],
      status: p.status,
      createdAt: p.createdAt,
    })),
    total: all.length,
    limit: MAX_LOG_PODS,
  });
  const streams = pods.flatMap((p) => {
    const cs = p.containers ?? [];
    const picked = container ? cs.filter((c) => c === container) : allContainers ? cs : cs.slice(0, 1);
    return picked.map((c) => ({
      pod: p,
      container: c,
      app: c === cs[0] ? p.labels?.["app.kubernetes.io/name"] : undefined,
    }));
  });
  const tail = Math.min(Number.parseInt(q.get("tail") ?? "100", 10) || 100, 80);
  const now = Date.now();
  // Each stream's tail spans the last minute, so the UI has to merge them by time.
  for (const s of streams) {
    if (s.pod.status !== "ready" && s.pod.status !== "completed") {
      source.send("log", {
        entries: [
          {
            pod: s.pod.name,
            container: s.container,
            line: `container has not started (${s.pod.message ?? "pending"})`,
            marker: "error",
          },
        ],
      });
      continue;
    }
    const entries = Array.from({ length: tail }, (_, i) => {
      const { line } = splitTs(logLine(s.app));
      return {
        pod: s.pod.name,
        container: s.container,
        line,
        ts: new Date(now - (tail - i) * (60_000 / tail) - Math.random() * 400).toISOString(),
      };
    });
    source.send("log", { entries });
  }
  const finished = kind === "Job" && pods.every((p) => p.status === "completed");
  if (q.get("follow") !== "true" || finished || streams.length === 0) {
    for (const p of pods)
      source.send("log", { entries: [{ pod: p.name, line: "pod completed", marker: "ended" }] });
    source.send("end", {});
    return () => {};
  }
  let ticks = 0;
  const timer = setInterval(() => {
    ticks++;
    const s = streams[Math.floor(Math.random() * streams.length)];
    if (!s) return;
    const entries: WorkloadLogEntry[] = Array.from({ length: Math.random() < 0.35 ? 2 : 1 }, () => ({
      pod: s.pod.name,
      container: s.container,
      ...splitTs(logLine(s.app)),
    }));
    // Once, a burst the rate limit cut, as the hub reports it.
    if (ticks === 24)
      entries.push({
        pod: "",
        line: "37 lines dropped by the rate limit (2000 lines/s per stream)",
        marker: "dropped",
      });
    source.send("log", { entries });
  }, 650);
  return () => clearInterval(timer);
}

function startStream(hub: MockHub, url: URL, source: MockEventSource): () => void {
  if (url.pathname === "/api/v1/stream") {
    source.send("hello", {});
    source.send("clusters", { items: hub.clusterInfos() });
    // ADR-0006 P2: full deltas only for the watched clusters; `counts` and `attention` for
    // every cluster. Without `watch=` (or as an older hub) every delta goes out, as before.
    const watchParam = url.searchParams.get("watch");
    const watch =
      watchParam !== null && !hub.oldHub ? new Set(watchParam.split(",").filter(Boolean)) : undefined;
    // Changes between the client's fetch and this stream are never lost.
    for (const c of watch ?? []) source.send("resync", { cluster: c });
    return hub.subscribe((event, data) => {
      if (event === "counts") {
        if (watch) source.send("counts", data);
        else source.send("clusters", { items: hub.clusterInfos() });
        return;
      }
      if (event === "change" && watch) {
        const change = data as ChangeEvent;
        const upserts = change.upserts.filter(isAttentionRow);
        const deletes = [
          ...change.deletes,
          ...change.upserts.filter((r) => !isAttentionRow(r)).map((r) => r.id),
        ];
        if (upserts.length || deletes.length)
          source.send("attention", { cluster: change.cluster, upserts, deletes });
        if (!watch.has(change.cluster)) return;
      }
      source.send(event, data);
    });
  }
  const workload = url.pathname.match(
    /^\/api\/v1\/clusters\/([^/]+)\/workloads\/([^/]+)\/([^/]+)\/([^/]+)\/logs$/,
  );
  if (workload) return workloadLogs(hub, url, source, workload);
  const logs = url.pathname.match(/^\/api\/v1\/clusters\/([^/]+)\/pods\/([^/]+)\/([^/]+)\/logs$/);
  if (logs) {
    const [, cluster = "", ns = "", pod = ""] = logs.map(decodeURIComponent);
    const r = hub.logsFor(cluster, ns, pod);
    const container = url.searchParams.get("container") ?? r?.containers?.[0];
    const sidecar = container && r?.containers && container !== r.containers[0];
    const app = sidecar ? undefined : r?.labels?.["app.kubernetes.io/name"];
    if (r?.status !== "ready" && r?.status !== "completed") {
      const reason = r?.message ?? "not found";
      source.send("log", { lines: [`Container has not started (${reason}), so there are no logs yet.`] });
      source.send("end", {});
      return () => {};
    }
    const tail = Number.parseInt(url.searchParams.get("tail") ?? "200", 10);
    source.send("log", { lines: Array.from({ length: Math.min(tail, 120) }, () => logLine(app)) });
    if (url.searchParams.get("follow") !== "true" || r.status === "completed") {
      source.send("end", {});
      return () => {};
    }
    const timer = setInterval(() => {
      source.send("log", { lines: Array.from({ length: Math.random() < 0.35 ? 2 : 1 }, () => logLine(app)) });
    }, 850);
    return () => clearInterval(timer);
  }
  setTimeout(() => source.dispatchEvent(new Event("error")), 0);
  return () => {};
}
