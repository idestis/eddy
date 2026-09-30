// Mock mode (`VITE_MOCK=1 pnpm dev`): replaces fetch and EventSource for
// same-origin /api and /auth URLs with the in-memory hub in server.ts, so the
// UI runs without the Go backend. main.tsx imports this module only when
// VITE_MOCK is set, so production builds never include it.

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
  console.info(`[eddy] mock mode: ${hub.clusters.length} clusters, in-memory hub`);
}

function startStream(hub: MockHub, url: URL, source: MockEventSource): () => void {
  if (url.pathname === "/api/v1/stream") {
    source.send("hello", {});
    return hub.subscribe((event, data) => source.send(event, data));
  }
  const logs = url.pathname.match(/^\/api\/v1\/clusters\/([^/]+)\/pods\/([^/]+)\/([^/]+)\/logs$/);
  if (logs) {
    const [, cluster = "", ns = "", pod = ""] = logs.map(decodeURIComponent);
    const r = hub.logsFor(cluster, ns, pod);
    const container = url.searchParams.get("container") ?? r?.containers?.[0];
    const sidecar = container && r?.containers && container !== r.containers[0];
    const app = sidecar ? undefined : r?.labels?.["app.kubernetes.io/name"];
    if (r?.status !== "ready") {
      const reason = r?.message ?? "not found";
      source.send("log", { lines: [`Container has not started (${reason}), so there are no logs yet.`] });
      source.send("end", {});
      return () => {};
    }
    const tail = Number.parseInt(url.searchParams.get("tail") ?? "200", 10);
    source.send("log", { lines: Array.from({ length: Math.min(tail, 120) }, () => logLine(app)) });
    if (url.searchParams.get("follow") !== "true") {
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
