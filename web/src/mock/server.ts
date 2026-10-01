// An in-memory hub that answers the routes of docs/api.md for mock mode.
// It enforces the same CSRF header and protected-cluster confirmation as the
// real hub so the UI's handling of both is exercised.

import { compareAttention, isAttentionRow } from "../api/delta";
import type {
  AskAttachment,
  AskStep,
  AuditEvent,
  Author,
  ChangeEvent,
  Chat,
  ClusterInfo,
  ClusterInput,
  ClusterPhase,
  ConnectionAttempt,
  ConnectionCheck,
  ConnectionInfo,
  ContextStatus,
  IndexRow,
  JoinTokenInfo,
  KindCounts,
  KindSummary,
  KindsResponse,
  Me,
  Message,
  OnboardedCluster,
  ResourceRef,
  Status,
  Thread,
  TokenItem,
  TokenScope,
} from "../api/types";
import { STATUS_RANK } from "../lib/format";
import { rank } from "../lib/fuzzy";
import { buildGraph, isFluxResource, MAX_HOPS } from "../lib/graph";
import { KINDS, kindInfo, matchesKindFilter, projectName, projectOf } from "../lib/kinds";
import {
  buildCluster,
  buildFleet,
  countsOf,
  type MockCluster,
  type MockResource,
  nextResourceVersion,
  yamlOf,
} from "./fixtures";
import { greenChecks, installGuide, joinToken, pendingChecks, TOKEN_PLACEHOLDER } from "./onboarding";

export type StreamListener = (event: string, data: unknown) => void;

const CSRF = "mock-csrf-token";
const PRE_CSRF = "mock-pre-csrf-token";
const AI_MODEL = "claude-haiku-4-5-20251001";

const me: Me = {
  user: "local:dana",
  display: "dana",
  groups: ["eddy:platform", "eddy:authenticated"],
  provider: "local",
  csrf: CSRF,
  features: {
    ai: true,
    aiProvider: "anthropic",
    mcp: true,
    mcpWrites: true,
    logs: true,
    ephemeralStore: true,
    devMode: false,
    onboarding: true,
    workloadLogs: true,
    aiLogs: true,
  },
  version: "v1.0.0",
};

const human = (display: string, via = "web", client?: string): Author => ({
  type: "human",
  subject: `local:${display}`,
  display,
  via,
  client,
});
const aiAuthor: Author = {
  type: "ai",
  subject: "local:dana",
  display: "Ask AI",
  via: "askai",
  client: AI_MODEL,
};

/** Mock-only onboarding state of one cluster (the Cluster CR status and the hub's memory). */
interface Onboarding {
  phase: ClusterPhase;
  managedBy?: "helm";
  checks: ConnectionCheck[];
  attempts: ConnectionAttempt[];
  token?: JoinTokenInfo;
  timers: Array<ReturnType<typeof setTimeout>>;
}

// Timings of the simulated agent after a join token is issued.
const STALE_AGENT_MS = 2_000;
const AGENT_JOIN_MS = 5_000;
const INFORMERS_SYNC_MS = 7_500;

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/;
// biome-ignore lint/suspicious/noControlCharactersInRegex: the hub rejects control characters
const PRINTABLE = /^[^\u0000-\u001f\u007f]{0,63}$/u;
const HEX_COLOR = /^#[0-9a-fA-F]{6}$/;

/** "1h", "30m", "24h" → milliseconds; undefined when it is not a duration the hub accepts. */
function parseTtl(ttl: string | undefined): number | undefined {
  if (!ttl) return 3_600_000;
  const m = ttl.match(/^(\d+)(m|h)$/);
  if (!m) return undefined;
  const ms = Number(m[1]) * (m[2] === "h" ? 3_600_000 : 60_000);
  return ms > 0 && ms <= 24 * 3_600_000 ? ms : undefined;
}

/** The hub's default page of hidden finished Jobs. */
const HIDDEN_PAGE = 500;

const iso = (minutesAgo = 0) => new Date(Date.now() - minutesAgo * 60_000).toISOString();

export class MockHub {
  readonly clusters: MockCluster[];
  private signedIn = true;
  private listeners = new Set<StreamListener>();
  private threads: Thread[] = [];
  private messages = new Map<string, Message[]>();
  private tokens: TokenItem[] = [];
  private audit: AuditEvent[] = [];
  private ids = 100;
  private onboarding = new Map<string, Onboarding>();

  /** Answer GET …/graph with 404, like a hub from before the endpoint (VITE_MOCK_NO_GRAPH=1). */
  noGraph = false;
  /**
   * Behave like a hub from before ADR-0006 P1/P2 (VITE_MOCK_OLD_HUB=1): no /search, no
   * /attention, no `ClusterInfo.kinds`, no stale views. The UI falls back to snapshots.
   */
  oldHub = false;
  /** Disconnected clusters whose last view the hub keeps (docs/api.md "Stale views"). */
  private staleViews = new Set<string>();

  constructor(extraPods = 0) {
    this.clusters = buildFleet(extraPods);
    for (const c of this.clusters)
      if (!c.info.connected && c.resources.size) this.staleViews.add(c.info.name);
    this.seedOnboarding();
    this.seedThreads();
    this.seedChats();
    this.seedAudit();
    this.tokens.push({
      id: "k3j9x0a2bq7d",
      name: "laptop claude code",
      scopes: ["read"],
      createdAt: iso(60 * 24 * 9),
      expiresAt: new Date(Date.now() + 21 * 86_400_000).toISOString(),
      lastUsedAt: iso(35),
    });
  }

  subscribe(fn: StreamListener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private emit(event: string, data: unknown): void {
    for (const fn of this.listeners) fn(event, data);
  }

  private cluster(name: string): MockCluster | undefined {
    return this.clusters.find((c) => c.info.name === name);
  }

  clusterInfos(): ClusterInfo[] {
    return this.clusters.map((c) => {
      const readable = this.readable(c);
      if (this.oldHub) return { ...c.info, counts: c.info.connected ? countsOf(c.resources) : c.info.counts };
      return {
        ...c.info,
        counts: readable ? countsOf(c.resources) : c.info.counts,
        kinds: readable ? kindCountsOf(c.resources) : undefined,
        ...(!c.info.connected && readable && { stale: true }),
      };
    });
  }

  /** Connected, or disconnected with a kept view. */
  private readable(c: MockCluster): boolean {
    return c.info.connected || (!this.oldHub && this.staleViews.has(c.info.name));
  }

  /** Applies a change to a resource and broadcasts it like the hub would. */
  private update(c: MockCluster, r: MockResource, patch: Partial<MockResource>): void {
    Object.assign(r, patch, { resourceVersion: nextResourceVersion(), lastChanged: iso() });
    const change: ChangeEvent = { cluster: c.info.name, upserts: [strip(r)], deletes: [] };
    this.emit("change", change);
    this.emitCounts(c);
  }

  /** Broadcasts several upserts and deletes as one delta, like the hub batches them. */
  private emitChange(c: MockCluster, upserts: MockResource[], deletes: string[]): void {
    for (const r of upserts) r.resourceVersion = nextResourceVersion();
    const change: ChangeEvent = { cluster: c.info.name, upserts: upserts.map(strip), deletes };
    this.emit("change", change);
    this.emitCounts(c);
  }

  /**
   * A cluster's counts changed. The stream turns this into a `counts` event (ADR-0006) or,
   * for a client that sent no `watch=` (an older SPA), into the full `clusters` event.
   */
  private emitCounts(c: MockCluster): void {
    const info = this.clusterInfos().find((i) => i.name === c.info.name);
    this.emit("counts", { cluster: c.info.name, counts: info?.counts, kinds: info?.kinds });
  }

  /** Background churn so the live indicator and SSE deltas have something to show. */
  tick(): void {
    const prod = this.cluster("prod-eu");
    if (!prod?.info.connected) return;
    this.jobCycle(prod);
    const pods = [...prod.resources.values()].filter(
      (r) => r.kind === "Pod" && r.name.startsWith("checkout"),
    );
    const p = pods[Math.floor(Math.random() * pods.length)];
    if (!p || p.status === "reconciling") return;
    this.update(prod, p, { message: `Running (restarts: ${Math.floor(Math.random() * 3)})` });
  }

  private jobRuns = 0;

  /**
   * A Prefect flow run every few ticks: a running Job (and its pod) appears, completes a
   * little later, and the oldest listed run of the flow leaves the list, as the agent's
   * history limit would hide it. This drives the list's enter, flash and exit motion.
   */
  private jobCycle(c: MockCluster): void {
    const now = Date.now();
    const jobs = [...c.resources.values()].filter(
      (r) => r.kind === "Job" && r.labels?.["prefect.io/deployment-name"] === "etl-hourly",
    );
    const running = jobs.find((j) => j.status === "reconciling");
    const podsOf = (j: MockResource) =>
      [...c.resources.values()].filter(
        (p) => p.kind === "Pod" && p.owner?.kind === "Job" && p.owner.name === j.name,
      );
    if (running) {
      const started = Date.parse(running.lastChanged ?? "") || now;
      if (now - started < 10_000) return;
      const secs = Math.max(1, Math.round((now - (Date.parse(running.createdAt ?? "") || started)) / 1000));
      Object.assign(running, {
        status: "completed",
        message: `Completed in ${Math.floor(secs / 60)}m${secs % 60}s`,
        replicas: undefined,
        completions: "1/1",
        lastChanged: iso(),
      });
      const pods = podsOf(running);
      for (const p of pods)
        Object.assign(p, { status: "completed", message: "Succeeded", lastChanged: iso() });
      this.emitChange(c, [running, ...pods], []);
      return;
    }
    const name = `etl-hourly-run-${(0x9a17 + this.jobRuns++ * 131).toString(36)}`;
    const images = ["prefecthq/prefect:3.4.1-python3.12"];
    const job: MockResource = {
      group: "batch",
      kind: "Job",
      namespace: "prefect",
      name,
      id: `batch/Job/prefect/${name}`,
      version: "v1",
      status: "reconciling",
      message: "Running, 1 active",
      replicas: "1/1",
      completions: "0/1",
      images,
      labels: { "prefect.io/deployment-name": "etl-hourly" },
      createdAt: iso(),
      lastChanged: iso(),
      resourceVersion: nextResourceVersion(),
      events: [],
      spec: {},
    };
    const podName = `${name}-${Math.random().toString(36).slice(2, 7)}`;
    const pod: MockResource = {
      group: "",
      kind: "Pod",
      namespace: "prefect",
      name: podName,
      id: `/Pod/prefect/${podName}`,
      version: "v1",
      status: "ready",
      message: "Running",
      owner: { group: "batch", kind: "Job", namespace: "prefect", name },
      containers: ["prefect-job"],
      images,
      createdAt: iso(),
      lastChanged: iso(),
      resourceVersion: nextResourceVersion(),
      events: [],
      spec: { container: "prefect-job" },
    };
    c.resources.set(job.id, job);
    c.resources.set(pod.id, pod);
    // The oldest finished run of the flow leaves the list.
    const oldest = jobs
      .filter((j) => j.status === "completed")
      .sort((a, b) => (a.createdAt ?? "").localeCompare(b.createdAt ?? ""))[0];
    const deletes: string[] = [];
    if (oldest && jobs.length >= 6) {
      for (const r of [oldest, ...podsOf(oldest)]) {
        c.resources.delete(r.id);
        deletes.push(r.id);
      }
    }
    this.emitChange(c, [job, pod], deletes);
  }

  /** Mock-only: drops or restores a cluster's agent, as the hub reports it. */
  setConnected(name: string, connected: boolean): void {
    const c = this.cluster(name);
    if (!c || c.info.connected === connected) return;
    c.info.connected = connected;
    c.info.lastSeen = iso();
    if (connected) this.staleViews.delete(name);
    else this.staleViews.add(name);
    this.emit("clusters", { items: this.clusterInfos() });
    this.emit("resync", { cluster: name });
  }

  /** The current pods of a workload, newest first, for its log stream. */
  workloadPods(cluster: string, kind: string, namespace: string, name: string): MockResource[] {
    const cl = this.cluster(cluster);
    if (!cl) return [];
    return [...cl.resources.values()]
      .filter(
        (r) =>
          r.kind === "Pod" &&
          r.owner?.kind === kind &&
          r.owner.namespace === namespace &&
          r.owner.name === name,
      )
      .sort((a, b) => (b.createdAt ?? "").localeCompare(a.createdAt ?? ""));
  }

  /**
   * GET …/resources, with the hub's optional kind and namespace filters. includeHidden (Jobs
   * only) adds the hidden finished Jobs a page at a time; a cursor page holds hidden Jobs only.
   */
  /** GET …/resources?view=index: one page of IndexRow, filtered and sorted by kind (ADR-0006). */
  private indexRoute(cl: MockCluster, q: URLSearchParams): Response {
    const kind = q.get("kind") ?? "";
    const status = q.get("status") ?? "";
    const namespace = q.get("namespace") ?? "";
    const text = (q.get("q") ?? "").toLowerCase();
    const limit = Math.min(Number(q.get("limit") ?? 200) || 200, 1000);
    const offset = Math.max(0, Number(q.get("offset") ?? 0) || 0);
    const all = [...cl.resources.values()];
    const inScope = all.filter(
      (r) =>
        matchesKindFilter(r.kind, kind || undefined, r.group) && (!namespace || r.namespace === namespace),
    );
    const statuses: Partial<Record<Status, number>> = {};
    for (const r of inScope) if (!r.inventoryOnly) statuses[r.status] = (statuses[r.status] ?? 0) + 1;
    const rows = inScope
      .filter(
        (r) =>
          (!status ||
            (status === "attention"
              ? !r.inventoryOnly && r.status !== "ready" && r.status !== "completed"
              : r.status === status)) &&
          (!text || `${r.name} ${r.namespace} ${r.kind} ${r.message ?? ""}`.toLowerCase().includes(text)),
      )
      .sort(
        (a, b) =>
          kindInfo(a.kind).order - kindInfo(b.kind).order ||
          a.kind.localeCompare(b.kind) ||
          a.namespace.localeCompare(b.namespace) ||
          a.name.localeCompare(b.name),
      );
    const page = rows.slice(offset, offset + limit).map(
      (r): IndexRow => ({
        id: r.id,
        group: r.group,
        kind: r.kind,
        namespace: r.namespace,
        name: r.name,
        status: r.status,
        ...(r.blocked && { blocked: true }),
        ...(r.message && { message: r.message.slice(0, 160) }),
        ...(r.revision && { revision: r.revision }),
        ...(r.replicas && { replicas: r.replicas }),
        ...(r.completions && { completions: r.completions }),
        ...(r.owner && { owner: r.owner }),
        ...(r.project && { project: r.project }),
        ...(r.inventoryOnly && { inventoryOnly: true }),
        ...((r.lastChanged ?? r.createdAt) && { lastChanged: r.lastChanged ?? r.createdAt }),
      }),
    );
    const end = offset + page.length;
    return json({
      items: page,
      total: rows.length,
      ...(end < rows.length && { next: `o${end}` }),
      facets: { statuses },
    });
  }

  /** GET /search: fuzzy.ts over every readable cluster, the way internal/hub/fuzzy.go ranks. */
  private searchRoute(q: URLSearchParams): Response {
    const text = (q.get("q") ?? "").trim();
    const scope = q.get("scope") || "fleet";
    const current = q.get("cluster") ?? "";
    const kind = q.get("kind") ?? "";
    const limit = Math.min(Number(q.get("limit") ?? 30) || 30, 100);
    if (scope === "cluster" && !current) return error(400, "bad_request", "scope=cluster needs cluster.");
    if (text.length > 128) return error(400, "bad_request", "q is at most 128 characters.");
    if (!text) return json({ items: [], partial: [] });
    const pool = this.clusters
      .filter((c) => this.readable(c) && (scope !== "cluster" || c.info.name === current))
      .flatMap((c) =>
        [...c.resources.values()]
          .filter((r) => !kind || r.kind === kind)
          .map((r) => ({ cluster: c.info.name, r, stale: !c.info.connected })),
      );
    const order = (x: { cluster: string; r: MockResource }) =>
      (x.r.inventoryOnly ? 9 : STATUS_RANK[x.r.status]) * 2 + (x.cluster === current ? 0 : 1);
    const hits = rank(
      text,
      pool,
      (x) => ({
        primary: x.r.name,
        secondary: [x.r.namespace, x.r.kind, kindInfo(x.r.kind).abbr, x.cluster],
      }),
      limit,
      (a, b) => order(a) - order(b),
    );
    return json({
      items: hits.map(({ item, match }) => ({
        cluster: item.cluster,
        resource: strip(item.r),
        match,
        ...(item.stale && { stale: true }),
      })),
      partial: [],
    });
  }

  /** GET /attention: failed rows and Flux rows reconciling or suspended, plus warning findings. */
  private attentionRoute(q: URLSearchParams): Response {
    const only = q.get("cluster") ?? "";
    const limit = Math.min(Number(q.get("limit") ?? 200) || 200, 1000);
    const clusters = this.clusters.filter((c) => this.readable(c) && (!only || c.info.name === only));
    if (only && !clusters.length && !this.cluster(only))
      return error(404, "not_found", `Cluster ${only} not found.`);
    const items = clusters
      .flatMap((c) =>
        [...c.resources.values()].filter(isAttentionRow).map((r) => ({
          cluster: c.info.name,
          resource: strip(r),
          ...(!c.info.connected && { stale: true }),
        })),
      )
      .sort(compareAttention);
    const findings = clusters.flatMap((c) =>
      (c.info.findings ?? [])
        .filter((f) => f.severity === "warning")
        .map((finding) => ({ cluster: c.info.name, finding, ...(!c.info.connected && { stale: true }) })),
    );
    return json({ items: items.slice(0, limit), total: items.length, findings, partial: [] });
  }

  private resourcesRoute(cl: MockCluster, q: URLSearchParams): Response {
    if (q.get("view") === "index" && !this.oldHub) return this.indexRoute(cl, q);
    const kind = q.get("kind") ?? "";
    const namespace = q.get("namespace") ?? "";
    const match = (r: MockResource) =>
      (!kind || r.kind === kind) && (!namespace || r.namespace === namespace);
    const listed = [...cl.resources.values()].filter(match).map(strip);
    if (!q.has("includeHidden")) return json({ items: listed, resourceVersion: nextResourceVersion() });
    if (kind !== "Job") return error(400, "bad_request", "includeHidden needs kind=Job.");
    const limit = Number(q.get("limit") ?? HIDDEN_PAGE);
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000)
      return error(400, "bad_request", "limit must be between 1 and 1000.");
    const cursor = q.get("cursor");
    const offset = cursor ? Number(cursor.replace(/^h/, "")) : 0;
    if (cursor && (!/^h\d+$/.test(cursor) || !Number.isInteger(offset)))
      return error(400, "bad_request", "Invalid cursor.");
    const hidden = (cl.hidden ?? []).filter(match);
    const page = hidden.slice(offset, offset + limit).map(strip);
    const end = offset + page.length;
    return json({
      items: cursor ? page : [...listed, ...page],
      resourceVersion: cursor ? "" : nextResourceVersion(),
      hidden: { total: hidden.length, next: end < hidden.length ? `h${end}` : undefined },
    });
  }

  private record(
    action: string,
    target: ResourceRef | undefined,
    result: AuditEvent["result"],
    detail?: unknown,
  ) {
    this.audit.unshift({
      id: this.ids++,
      ts: iso(),
      subject: me.user,
      groups: me.groups,
      via: "web",
      action,
      target,
      result,
      detail,
    });
  }

  handle(method: string, url: URL, body: unknown, headers: Headers): Response {
    const path = url.pathname;
    const unsafe = method !== "GET" && method !== "HEAD";
    if (unsafe && path.startsWith("/api/") && headers.get("X-Eddy-CSRF") !== CSRF) {
      return error(403, "forbidden", "Missing or invalid CSRF token.");
    }

    if (path === "/auth/csrf") return json({ csrf: PRE_CSRF });
    if (path === "/auth/providers")
      return json({
        local: { enabled: true, mode: "normal" },
        proxy: false,
        dev: true,
        // The mock intercepts fetch only, so it cannot play an OAuth redirect.
        providers: [],
      });
    if (path === "/auth/local/login" && method === "POST") {
      if (headers.get("X-Eddy-CSRF") !== PRE_CSRF) return error(403, "forbidden", "Missing CSRF token.");
      const { username, password } = (body ?? {}) as { username?: string; password?: string };
      if (!username || !password) return error(401, "unauthorized", "Invalid username or password.");
      this.signedIn = true;
      me.user = `local:${username}`;
      me.display = username;
      return new Response(null, { status: 204 });
    }
    if (path === "/auth/logout" && method === "POST") {
      this.signedIn = false;
      return new Response(null, { status: 204 });
    }
    if (path === "/auth/dev/login") {
      this.signedIn = true;
      return new Response(null, { status: 302, headers: { Location: "/" } });
    }

    if (!path.startsWith("/api/v1/")) return error(404, "not_found", "No such route.");
    if (!this.signedIn) return error(401, "unauthorized", "Sign in to continue.");

    const p = path.slice("/api/v1/".length).split("/").map(decodeURIComponent);
    const [root, a, b, c, d, e, f] = p;

    if (root === "me") return json(me);
    if (root === "search" && !a && method === "GET" && !this.oldHub)
      return this.searchRoute(url.searchParams);
    if (root === "attention" && !a && method === "GET" && !this.oldHub)
      return this.attentionRoute(url.searchParams);
    if (root === "clusters" && p.length === 1 && method === "GET")
      return json({ items: this.clusterInfos() });
    if (root === "clusters") {
      const res = this.onboardingRoute(method, a, b, body);
      if (res) return res;
    }

    if (root === "clusters" && a) {
      const cl = this.cluster(a);
      if (!cl) return error(404, "not_found", `Cluster ${a} not found.`);
      // A stale view answers reads of the last state; writes, YAML, events and logs need the agent.
      const stale = !cl.info.connected;
      const staleRead =
        stale &&
        this.readable(cl) &&
        method === "GET" &&
        (b === "resources" ||
          b === "kinds" ||
          b === "graph" ||
          b === "findings" ||
          (b === "objects" && !f) ||
          f === "children");
      if (stale && !staleRead) return error(503, "disconnected", `${a} is disconnected.`);
      const mark = (res: Response) => (staleRead ? markStale(res) : res);
      if (b === "resources") return mark(this.resourcesRoute(cl, url.searchParams));
      if (b === "kinds" && !c) return mark(json(kindsOf(cl)));
      if (b === "graph" && !c) return mark(this.graphRoute(cl, url.searchParams));
      if (b === "findings" && !c) return mark(json({ items: cl.info.findings ?? [] }));
      if (b === "objects" && c && d && e) {
        const group = url.searchParams.get("group") ?? undefined;
        const r = findResource(cl, c, d === "_" ? "" : d, e, group);
        if (!r) return error(404, "not_found", `${c}/${e} not found.`);
        const target: ResourceRef = {
          cluster: a,
          group: r.group,
          kind: r.kind,
          namespace: r.namespace,
          name: r.name,
        };
        if (!f && method === "GET") return json(strip(r));
        if (f === "yaml" && r.kind === "Secret")
          return error(403, "forbidden", "Secret contents are never read.");
        if (f === "yaml") return json({ yaml: yamlOf(r) });
        if (f === "events") return json({ items: r.events });
        if (f === "children") {
          return json({
            items: [...cl.resources.values()]
              .filter((x) => x.owner && refKey(x.owner) === refKey(r))
              .map(strip),
          });
        }
        if (method === "POST" && (f === "reconcile" || f === "suspend" || f === "resume")) {
          const { confirm, withSource } = (body ?? {}) as { confirm?: string; withSource?: boolean };
          if (!kindInfo(r.kind).flux) return error(400, "bad_request", `${r.kind} is not a Flux object.`);
          if (cl.info.protected && f === "suspend" && confirm !== a) {
            this.record(f, target, "denied", { reason: "confirm_required" });
            return error(428, "confirm_required", `Type ${a} to confirm.`);
          }
          this.record(f, target, "ok", withSource ? { withSource } : undefined);
          this.act(cl, r, f);
          return new Response(null, { status: 202 });
        }
      }
      if (b === "pods") return error(400, "bad_request", "Logs are streamed over SSE.");
    }

    if (root === "prefs") return this.prefsRoute(method, body);
    if (root === "threads") return this.threadsRoute(method, a, b, url, body);
    if (root === "ai" && a === "ask" && !b && method === "POST") return this.ask(body);
    if (root === "ai" && a === "chats" && !c) return this.chatsRoute(method, b, url, body);
    if (root === "tokens") return this.tokensRoute(method, a, body);
    if (root === "audit")
      return json({ items: this.audit.filter((x) => x.subject === me.user).slice(0, 50) });

    return error(404, "not_found", "No such route.");
  }

  // Per-user UI preferences (GET/PUT /prefs): one JSON object up to 16 KiB.
  private prefs: Record<string, unknown> = {};

  private prefsRoute(method: string, body: unknown): Response {
    if (method === "GET") return json({ data: this.prefs });
    if (method !== "PUT") return error(404, "not_found", "No such route.");
    const data = (body as { data?: unknown } | undefined)?.data;
    if (!data || typeof data !== "object" || Array.isArray(data))
      return error(400, "bad_request", "data must be a JSON object");
    if (JSON.stringify(data).length > 16 * 1024) return error(400, "bad_request", "request body too large");
    this.prefs = data as Record<string, unknown>;
    return json({ data: this.prefs });
  }

  // Cluster onboarding (ADR-0005)

  private seedOnboarding(): void {
    for (const c of this.clusters) {
      const connected = c.info.connected;
      const created = 60 * 24 * 40;
      this.onboarding.set(c.info.name, {
        phase: connected ? "Connected" : "Disconnected",
        managedBy: c.info.name === "prod-eu" ? "helm" : undefined,
        checks: connected
          ? greenChecks({ flux: c.info.fluxVersion, resources: c.resources.size })
          : pendingChecks(`47m ago`),
        attempts: connected
          ? []
          : [
              {
                at: iso(12),
                reason: "join_used",
                detail: "The join token was already used. Issue a new one to reinstall the agent.",
                peer: "198.51.100.7:40522",
                hubPod: "eddy-hub-6f7d9c7b5-x2k4p",
              },
            ],
        token: {
          id: joinToken().id,
          state: "used",
          createdBy: "local:sam",
          createdAt: iso(created),
          expiresAt: iso(created - 60),
          usedAt: iso(created - 4),
        },
        timers: [],
      });
    }
  }

  private onboarded(c: MockCluster): OnboardedCluster {
    const o = this.onboarding.get(c.info.name);
    const { name, displayName, environment, region, color, order } = c.info;
    return {
      name,
      displayName,
      environment,
      region,
      color,
      protected: c.info.protected,
      order,
      phase: o?.phase ?? (c.info.connected ? "Connected" : "Disconnected"),
      managedBy: o?.managedBy,
    };
  }

  private connectionOf(c: MockCluster): ConnectionInfo {
    const o = this.onboarding.get(c.info.name);
    return {
      cluster: this.onboarded(c),
      checks: o?.checks ?? [],
      agents: c.info.connected ? 1 : 0,
      joinToken: o?.token,
      attempts: o?.attempts ?? [],
      permissions: { update: true, edit: true, delete: true },
      guide: installGuide(this.onboarded(c), TOKEN_PLACEHOLDER),
    };
  }

  private validateInput(input: Partial<ClusterInput>, creating: boolean): string | undefined {
    if (creating && (!input.name || input.name.length > 63 || !DNS_LABEL.test(input.name)))
      return "name must be a DNS-1123 label (lower-case letters, digits and hyphens, at most 63).";
    for (const f of ["displayName", "environment", "region"] as const) {
      const v = input[f];
      if (v !== undefined && (typeof v !== "string" || !PRINTABLE.test(v)))
        return `${f} must be at most 63 printable characters.`;
    }
    if (input.color !== undefined && input.color !== "" && !HEX_COLOR.test(input.color))
      return 'color must look like "#RRGGBB".';
    if (input.order !== undefined && !Number.isInteger(input.order)) return "order must be an integer.";
    if (parseTtl(input.ttl) === undefined) return "ttl must be a duration between 1m and 24h, such as 1h.";
    return undefined;
  }

  /** Issues a join token (revoking an unused predecessor) and, for a cluster not yet connected, simulates the agent. */
  private issueToken(c: MockCluster, ttl: string | undefined) {
    const o = this.onboarding.get(c.info.name);
    if (!o) throw new Error(`mock: no onboarding state for ${c.info.name}`);
    if (o.token?.state === "active") o.token.state = "revoked";
    const { id, token } = joinToken();
    const expiresAt = new Date(Date.now() + (parseTtl(ttl) ?? 3_600_000)).toISOString();
    o.token = { id, state: "active", createdBy: me.user, createdAt: iso(), expiresAt };
    if (!c.info.connected) this.simulateJoin(c, id);
    return { token, expiresAt };
  }

  /**
   * A fake agent install: a stale agent is rejected after 2 s, the new one joins at 5 s
   * with informers still syncing, and they finish at 7.5 s.
   */
  private simulateJoin(c: MockCluster, tokenId: string): void {
    const name = c.info.name;
    const o = this.onboarding.get(name);
    if (!o) return;
    for (const t of o.timers) clearTimeout(t);
    const current = () => this.cluster(name) === c && o.token?.id === tokenId;
    const emit = () => this.emit("connection", { cluster: name });
    o.timers = [
      setTimeout(() => {
        if (!current()) return;
        o.attempts.unshift({
          at: iso(),
          reason: "bad_token",
          detail:
            "The agent sent a token this hub does not know. Is an agent from an earlier install still running?",
          peer: "203.0.113.24:51734",
          hubPod: "eddy-hub-6f7d9c7b5-x2k4p",
        });
        o.attempts.splice(20);
        emit();
      }, STALE_AGENT_MS),
      setTimeout(() => {
        if (!current() || !o.token) return;
        o.token.state = "used";
        o.token.usedAt = iso();
        o.phase = "Connected";
        const built = buildCluster(
          {
            info: {
              ...c.info,
              connected: true,
              lastSeen: iso(),
              agentVersion: "v1.0.0",
              kubernetesVersion: "v1.33.2",
              fluxVersion: "v2.7.0",
            },
            branch: "main",
          },
          0,
        );
        c.info = { ...built.info, counts: undefined };
        c.resources = built.resources;
        o.checks = greenChecks({ flux: "v2.7.0", resources: c.resources.size, impersonation: "warn" }).map(
          (check) =>
            check.id === "informers"
              ? { ...check, state: "pending", detail: "Syncing: 7 of 12 kinds" }
              : check,
        );
        this.record("cluster.joined", { cluster: name, group: "", kind: "", namespace: "", name: "" }, "ok");
        emit();
        this.emit("clusters", { items: this.clusterInfos() });
      }, AGENT_JOIN_MS),
      setTimeout(() => {
        if (!current()) return;
        o.checks = o.checks.map((check) =>
          check.id === "informers"
            ? { ...check, state: "ok", detail: `${c.resources.size} resources` }
            : check,
        );
        emit();
      }, INFORMERS_SYNC_MS),
    ];
  }

  private onboardingRoute(
    method: string,
    a: string | undefined,
    b: string | undefined,
    body: unknown,
  ): Response | undefined {
    const target = (name: string): ResourceRef => ({
      cluster: name,
      group: "",
      kind: "",
      namespace: "",
      name: "",
    });
    if (a === "permissions" && !b && method === "GET") return json({ onboarding: true, create: true });

    if (!a && method === "POST") {
      const input = (body ?? {}) as ClusterInput;
      const invalid = this.validateInput(input, true);
      if (invalid) return error(400, "bad_request", invalid);
      if (this.cluster(input.name)) return error(409, "conflict", `Cluster ${input.name} already exists.`);
      const c: MockCluster = {
        info: {
          name: input.name,
          displayName: input.displayName?.trim() || input.name,
          environment: input.environment || undefined,
          region: input.region || undefined,
          color: input.color || undefined,
          protected: Boolean(input.protected),
          order: input.order ?? Math.max(0, ...this.clusters.map((x) => x.info.order)) + 1,
          connected: false,
        },
        resources: new Map(),
      };
      this.clusters.push(c);
      this.onboarding.set(c.info.name, {
        phase: "Pending",
        checks: pendingChecks(),
        attempts: [],
        timers: [],
      });
      const token = this.issueToken(c, input.ttl);
      this.record("cluster.create", target(c.info.name), "ok");
      this.emit("clusters", { items: this.clusterInfos() });
      return json(
        { cluster: this.onboarded(c), joinToken: token, guide: installGuide(this.onboarded(c), token.token) },
        201,
      );
    }

    if (!a) return undefined;
    const c = this.cluster(a);
    const o = this.onboarding.get(a);
    const isRoute =
      (!b && (method === "PATCH" || method === "DELETE")) ||
      (b === "join-token" && method === "POST") ||
      (b === "connection" && method === "GET");
    if (!isRoute) return undefined;
    if (!c || !o) return error(404, "not_found", `Cluster ${a} not found.`);

    if (b === "connection") return json(this.connectionOf(c));

    if (b === "join-token") {
      const { ttl } = (body ?? {}) as { ttl?: string };
      if (parseTtl(ttl) === undefined) return error(400, "bad_request", "ttl must be between 1m and 24h.");
      const token = this.issueToken(c, ttl);
      this.record("cluster.join_token", target(a), "ok");
      this.emit("connection", { cluster: a });
      const warnings = c.info.connected
        ? [
            "An agent is connected. Installing with this token gives the new agent fresh credentials; the running one keeps its session until it restarts.",
          ]
        : undefined;
      return json({ joinToken: token, guide: installGuide(this.onboarded(c), token.token, warnings) }, 201);
    }

    if (o.managedBy === "helm") {
      return error(409, "conflict", `${a} is declared in the eddy-hub chart values. Change it there.`);
    }
    const { confirm, ...patch } = (body ?? {}) as Partial<ClusterInput> & { confirm?: string };

    if (method === "DELETE") {
      if (c.info.protected && confirm !== a) {
        this.record("cluster.delete", target(a), "denied", { reason: "confirm_required" });
        return error(428, "confirm_required", `Type ${a} to confirm.`);
      }
      for (const t of o.timers) clearTimeout(t);
      this.clusters.splice(this.clusters.indexOf(c), 1);
      this.onboarding.delete(a);
      this.record("cluster.delete", target(a), "ok");
      this.emit("clusters", { items: this.clusterInfos() });
      return new Response(null, { status: 204 });
    }

    // PATCH
    const invalid = this.validateInput({ ...patch, ttl: undefined }, false);
    if (invalid) return error(400, "bad_request", invalid);
    if (c.info.protected && patch.protected === false && confirm !== a) {
      return error(428, "confirm_required", `Type ${a} to turn protection off.`);
    }
    if (patch.displayName !== undefined) c.info.displayName = patch.displayName.trim() || a;
    if (patch.environment !== undefined) c.info.environment = patch.environment || undefined;
    if (patch.region !== undefined) c.info.region = patch.region || undefined;
    if (patch.color !== undefined) c.info.color = patch.color || undefined;
    if (patch.protected !== undefined) c.info.protected = patch.protected;
    if (patch.order !== undefined) c.info.order = patch.order;
    this.record("cluster.update", target(a), "ok");
    this.emit("clusters", { items: this.clusterInfos() });
    this.emit("connection", { cluster: a });
    return json({ cluster: this.onboarded(c) });
  }

  // GET …/graph (docs/api.md "Dependency graph"), built with the browser fallback's code.
  private graphRoute(cl: MockCluster, q: URLSearchParams): Response {
    if (this.noGraph) return error(404, "not_found", "No such route.");
    const kinds = q.get("kinds") || "flux";
    if (kinds !== "flux" && kinds !== "all") return error(400, "bad_request", "kinds must be flux or all");
    const hopsRaw = q.get("hops");
    const hops = hopsRaw ? Number(hopsRaw) : 2;
    if (!Number.isInteger(hops) || hops < 1)
      return error(400, "bad_request", "hops must be a positive integer");
    const focus = q.get("focus") || undefined;
    const rows = [...cl.resources.values()].map(strip);
    if (focus && !rows.some((r) => r.id === focus && (kinds === "all" || isFluxResource(r))))
      return error(404, "not_found", `${focus} not found`);
    const g = buildGraph(rows, { kinds, focus, hops: Math.min(hops, MAX_HOPS) });
    return json({ ...g, truncated: g.truncated ?? false });
  }

  private act(cl: MockCluster, r: MockResource, action: "reconcile" | "suspend" | "resume"): void {
    if (action === "suspend") {
      this.update(cl, r, { status: "suspended", suspended: true, message: "Reconciliation is suspended" });
      r.events.unshift(event("Normal", "Suspended", `Suspended by ${me.display}`));
      return;
    }
    r.events.unshift(event("Normal", "ReconcileRequested", `Reconcile requested by ${me.display}`));
    this.reconcileWave(
      cl,
      r,
      action === "resume" ? "Resuming reconciliation" : "Reconciliation in progress",
      new Set(),
    );
  }

  /** What a reconcile of `r` sets off afterwards: its dependents, and the Flux objects a Kustomization applies. */
  private downstreamOf(cl: MockCluster, r: MockResource): MockResource[] {
    const key = refKey(r);
    return [...cl.resources.values()].filter(
      (x) =>
        !x.suspended &&
        isFluxResource(x) &&
        (x.dependsOn?.some((d) => refKey(d) === key) ||
          (r.kind === "Kustomization" && x.owner && refKey(x.owner) === key && x.kind !== "GitRepository")),
    );
  }

  /**
   * One object reconciles (Reconciling, then its result about 1.4 s later). On success the
   * objects downstream of it reconcile in turn, a little staggered, so the graph shows the
   * wave travelling. An object whose dependency is still not ready ends up waiting
   * (DependencyNotReady); a failing release fails again unless its fix has landed.
   */
  private reconcileWave(cl: MockCluster, r: MockResource, message: string, seen: Set<string>): void {
    seen.add(r.id);
    const before = { status: r.status, message: r.message };
    this.update(cl, r, { status: "reconciling", suspended: false, blocked: undefined, message });
    setTimeout(() => {
      const notReady = (r.dependsOn ?? [])
        .map((d) => [...cl.resources.values()].find((x) => refKey(x) === refKey(d)))
        .find((d) => d?.status !== "ready");
      if (notReady) {
        const msg = `Waiting for ${notReady.namespace}/${notReady.name}`;
        r.conditions = [
          {
            type: "Ready",
            status: "False",
            reason: "DependencyNotReady",
            message: `dependency '${notReady.namespace}/${notReady.name}' is not ready`,
          },
        ];
        this.update(cl, r, { status: "reconciling", blocked: true, message: msg });
        r.events.unshift(event("Normal", "DependencyNotReady", msg));
        return;
      }
      if (before.status === "failed" && !r.recovers) {
        this.update(cl, r, { status: "failed", message: before.message });
        r.events.unshift(event("Warning", "ReconciliationFailed", before.message ?? ""));
        return;
      }
      r.recovers = undefined;
      if (r.chart && r.kind === "HelmRelease") r.revision = r.chart.split("@")[1];
      const done =
        r.kind === "Kustomization"
          ? `Applied revision: ${r.revision ?? ""}`
          : r.kind === "HelmRelease"
            ? `Helm upgrade succeeded for release ${r.namespace}/${r.name} with chart ${r.chart ?? ""}`
            : "stored artifact for the latest revision";
      r.conditions = [{ type: "Ready", status: "True", reason: "ReconciliationSucceeded", message: done }];
      this.update(cl, r, { status: "ready", blocked: undefined, message: done });
      r.events.unshift(event("Normal", "ReconciliationSucceeded", done));
      this.downstreamOf(cl, r)
        .filter((x) => !seen.has(x.id))
        .forEach((x, i) => {
          seen.add(x.id);
          setTimeout(() => this.reconcileWave(cl, x, "Reconciliation in progress", seen), 500 + i * 350);
        });
    }, 1400);
  }

  private threadsRoute(
    method: string,
    id: string | undefined,
    sub: string | undefined,
    url: URL,
    body: unknown,
  ) {
    if (!id && method === "GET") {
      const q = url.searchParams;
      const items = this.threads
        .filter(
          (t) =>
            (t.visibility === "resource" || t.createdBy.subject === me.user) &&
            t.type === (q.get("type") ?? t.type),
        )
        .filter((t) => !q.get("cluster") || t.ref.cluster === q.get("cluster"))
        .filter((t) => !q.get("kind") || t.ref.kind === q.get("kind"))
        .filter((t) => !q.get("namespace") || t.ref.namespace === q.get("namespace"))
        .filter((t) => !q.get("name") || t.ref.name === q.get("name"))
        .filter((t) => !q.get("status") || t.status === q.get("status"))
        .sort((x, y) => y.updatedAt.localeCompare(x.updatedAt));
      return json({ items });
    }
    if (!id && method === "POST") {
      const { ref, title, body: text } = body as { ref: ResourceRef; title: string; body: string };
      if (!title?.trim() || !text?.trim()) return error(400, "bad_request", "Title and body are required.");
      const thread = this.newThread(ref, title, human(me.display), "discussion");
      const message = this.addMessage(thread, human(me.display), text);
      this.record("thread.create", ref, "ok");
      return json({ thread, message }, 201);
    }
    const thread = this.threads.find((t) => t.id === id);
    if (!thread) return error(404, "not_found", "Thread not found.");
    if (!sub && method === "GET") return json({ thread, messages: this.messages.get(thread.id) ?? [] });
    if (sub === "messages" && method === "POST") {
      const text = (body as { body?: string })?.body ?? "";
      if (!text.trim()) return error(400, "bad_request", "Message body is required.");
      return json(this.addMessage(thread, human(me.display), text), 201);
    }
    if ((sub === "resolve" || sub === "reopen") && method === "POST") {
      thread.status = sub === "resolve" ? "resolved" : "open";
      thread.updatedAt = iso();
      thread.resolvedBy = sub === "resolve" ? me.user : undefined;
      this.emit("thread", { threadId: thread.id, ref: thread.ref });
      return json(thread);
    }
    return error(404, "not_found", "No such route.");
  }

  private newThread(
    ref: ResourceRef,
    title: string,
    by: Author,
    type: Thread["type"],
    minutesAgo = 0,
  ): Thread {
    const t: Thread = {
      id: `th_${this.ids++}`,
      ref,
      type,
      visibility: "resource",
      title,
      status: "open",
      createdBy: by,
      createdAt: iso(minutesAgo),
      updatedAt: iso(minutesAgo),
      messageCount: 0,
    };
    this.threads.push(t);
    this.messages.set(t.id, []);
    return t;
  }

  private addMessage(t: Thread, author: Author, body: string, minutesAgo = 0, meta?: unknown): Message {
    const m: Message = {
      id: `m_${this.ids++}`,
      threadId: t.id,
      author,
      body,
      createdAt: iso(minutesAgo),
      meta,
    };
    this.messages.get(t.id)?.push(m);
    t.messageCount++;
    t.updatedAt = m.createdAt;
    this.emit("thread", { threadId: t.id, ref: t.ref });
    return m;
  }

  // Ask AI chats (ADR-0007): owner-only, newest first, at most 10 context references.

  private chats: Chat[] = [];
  private chatMessages = new Map<string, Message[]>();

  private ownChat(id: string | undefined): Chat | undefined {
    return this.chats.find((c) => c.id === id && c.owner === me.user);
  }

  /** Whether the signed-in user can still see a context reference (the hub asks the SAR authorizer). */
  private contextVisible(r: ResourceRef): boolean {
    const cl = this.cluster(r.cluster);
    if (!cl) return false;
    if (r.kind === "") return true;
    return Boolean(findResource(cl, r.kind, r.namespace, r.name, r.group || undefined));
  }

  private validContext(context: unknown): ResourceRef[] | string {
    if (context === undefined) return [];
    if (!Array.isArray(context)) return "context must be a list.";
    if (context.length > 10) return "At most 10 context references.";
    for (const r of context as ResourceRef[])
      if (!r || typeof r.cluster !== "string" || !r.cluster || typeof r.kind !== "string")
        return "Each context reference needs a cluster and a kind.";
    return context as ResourceRef[];
  }

  private newChat(context: ResourceRef[], title = "", minutesAgo = 0): Chat {
    const c: Chat = {
      id: `ch_${this.ids++}`,
      owner: me.user,
      title,
      context,
      createdAt: iso(minutesAgo),
      updatedAt: iso(minutesAgo),
      messageCount: 0,
    };
    this.chats.push(c);
    this.chatMessages.set(c.id, []);
    // 200 chats per user: one more drops the least recently used.
    const own = this.chats.filter((x) => x.owner === me.user);
    if (own.length > 200) {
      const oldest = own.reduce((a, b) => (a.updatedAt <= b.updatedAt ? a : b));
      this.chats = this.chats.filter((x) => x !== oldest);
      this.chatMessages.delete(oldest.id);
    }
    return c;
  }

  private chatMessage(c: Chat, author: Author, body: string, minutesAgo = 0, meta?: unknown): Message {
    const m: Message = {
      id: `m_${this.ids++}`,
      threadId: c.id,
      author,
      body,
      createdAt: iso(minutesAgo),
      meta,
    };
    this.chatMessages.get(c.id)?.push(m);
    c.messageCount++;
    c.updatedAt = m.createdAt;
    return m;
  }

  private chatsRoute(method: string, id: string | undefined, url: URL, body: unknown): Response {
    const q = url.searchParams;
    const page = <T>(items: T[], fallback: number, max: number) => {
      const limit = Math.min(Number(q.get("limit") ?? fallback) || fallback, max);
      const offset = Number((q.get("cursor") ?? "o0").slice(1)) || 0;
      const end = offset + limit;
      return { items: items.slice(offset, end), ...(end < items.length && { next: `o${end}` }) };
    };
    if (!id && method === "GET") {
      const own = this.chats
        .filter((c) => c.owner === me.user)
        .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt) || b.id.localeCompare(a.id));
      return json(page(own, 50, 200));
    }
    if (!id && method === "POST") {
      const context = this.validContext((body as { context?: unknown } | undefined)?.context);
      if (typeof context === "string") return error(400, "bad_request", context);
      return json(this.newChat(context), 201);
    }
    const chat = this.ownChat(id);
    if (!chat) return error(404, "not_found", "Chat not found.");
    if (method === "GET") {
      const { items, next } = page(this.chatMessages.get(chat.id) ?? [], 100, 500);
      return json({ chat, messages: items, ...(next && { next }) });
    }
    if (method === "PATCH") {
      const { title, context } = (body ?? {}) as { title?: unknown; context?: unknown };
      if (title !== undefined) {
        if (typeof title !== "string" || !title.trim() || title.length > 200)
          return error(400, "bad_request", "title must be 1 to 200 characters.");
        chat.title = title.trim();
      }
      if (context !== undefined) {
        const refs = this.validContext(context);
        if (typeof refs === "string") return error(400, "bad_request", refs);
        chat.context = refs;
      }
      return json(chat);
    }
    if (method === "DELETE") {
      this.chats = this.chats.filter((c) => c !== chat);
      this.chatMessages.delete(chat.id);
      return new Response(null, { status: 204 });
    }
    return error(404, "not_found", "No such route.");
  }

  private ask(body: unknown): Response {
    if (!me.features.ai) return error(503, "disabled", "Ask AI is turned off.");
    const { chatId, context, question, attachments } = (body ?? {}) as {
      chatId?: string;
      context?: unknown;
      question?: string;
      attachments?: AskAttachment[];
    };
    if (typeof question !== "string" || !question.trim())
      return error(400, "bad_request", "question is required.");
    if (new TextEncoder().encode(question).length > 8 * 1024)
      return error(400, "bad_request", "question is at most 8 KiB.");
    const refs = context === undefined ? undefined : this.validContext(context);
    if (typeof refs === "string") return error(400, "bad_request", refs);
    if (attachments?.length) {
      if (!me.features.aiLogs && attachments.some((a) => a.kind === "logs"))
        return error(400, "bad_request", "Log attachments are turned off (ai.allowLogs).");
      const lines = attachments.reduce((n, a) => n + a.lines.length, 0);
      const size = attachments.reduce(
        (n, a) => n + a.lines.reduce((m, l) => m + new TextEncoder().encode(l).length + 1, 0),
        0,
      );
      if (attachments.length > 3 || lines > 500 || size > 32 * 1024)
        return error(400, "bad_request", "At most 3 attachments, 500 lines and 32 KiB in total.");
    }
    let chat: Chat;
    if (chatId) {
      const found = this.ownChat(chatId);
      if (!found) return error(404, "not_found", "Chat not found.");
      chat = found;
      if (refs) chat.context = refs;
    } else {
      chat = this.newChat(refs ?? [], question.trim().replace(/\s+/g, " ").slice(0, 80));
    }
    if ((this.chatMessages.get(chat.id)?.length ?? 0) >= 1000)
      return error(409, "conflict", "This chat is full. Start a new one.");
    this.chatMessage(chat, human(me.display, "askai"), question);

    const contextStatus: ContextStatus[] = chat.context.map((r) =>
      this.contextVisible(r) ? "ok" : "hidden",
    );
    const seen = chat.context.filter((_, i) => contextStatus[i] === "ok");
    const { text, steps, refs: answerRefs } = this.answer(seen, attachments);
    const message = this.chatMessage(chat, aiAuthor, text, 0, {
      steps,
      refs: answerRefs,
      usage: { inputTokens: 2310, outputTokens: 164 },
    });
    this.record("ai.ask", seen[0], "ok", { chatId: chat.id, context: chat.context });
    return json({ chat, message, steps, contextStatus });
  }

  /** A canned answer that names the context it read, like the real tool loop would. */
  private answer(
    refs: ResourceRef[],
    attachments: AskAttachment[] | undefined,
  ): { text: string; steps: AskStep[]; refs: ResourceRef[] } {
    const steps: AskStep[] = [];
    // meta.refs: like the hub, only what the "tools" returned (the visible context and the
    // objects read below), so `Kind/ns/name` spans in the text become links.
    const out: ResourceRef[] = [];
    const cite = (cluster: string, x: Pick<ResourceRef, "group" | "kind" | "namespace" | "name">) => {
      if (!out.some((o) => o.cluster === cluster && refKey(o) === refKey(x)))
        out.push({ cluster, group: x.group, kind: x.kind, namespace: x.namespace, name: x.name });
      return `\`${x.kind}/${x.namespace ? `${x.namespace}/` : ""}${x.name}\``;
    };
    const named = (r: ResourceRef) => (r.kind ? `${cite(r.cluster, r)} on ${r.cluster}` : `\`${r.cluster}\``);
    const intro = refs.length
      ? `Looking at ${refs.map(named).join(", ")}.\n\n`
      : "No resource is in this chat's context, so I looked across the fleet.\n\n";
    if (attachments?.length) {
      const all = attachments.flatMap((a) => a.lines);
      const errors = all.filter((l) => /\b(error|fatal|panic|failed)\b/i.test(l));
      const warns = all.filter((l) => /\bwarn(ing)?\b/i.test(l));
      const sources = attachments.map((a) => `\`${a.source}\``).join(", ");
      return {
        steps,
        refs: out,
        text: `${intro}I read ${all.length} line${all.length === 1 ? "" : "s"} from ${sources}.\n\n- ${errors.length} error${errors.length === 1 ? "" : "s"} and ${warns.length} warning${warns.length === 1 ? "" : "s"}.${errors[0] ? `\n- The first error:\n\n\`\`\`\n${errors[0]}\n\`\`\`` : "\n- Nothing in these lines looks like a failure."}\n- If this repeats, check the events and recent rollouts.`,
      };
    }
    const parts: string[] = [];
    for (const ref of refs.slice(0, 4)) {
      const cl = this.cluster(ref.cluster);
      if (!cl) continue;
      if (!ref.kind) {
        steps.push({ tool: "list_unhealthy", args: { cluster: ref.cluster }, bytes: 2210 });
        const bad = [...cl.resources.values()].filter(
          (x) => x.status === "failed" || x.status === "suspended",
        );
        parts.push(
          bad.length
            ? `**${ref.cluster}**: ${bad.length} object${bad.length === 1 ? " needs" : "s need"} attention.\n${bad
                .slice(0, 4)
                .map((x) => `- ${cite(ref.cluster, x)} is ${x.status}: ${x.message ?? ""}`)
                .join("\n")}`
            : `**${ref.cluster}**: everything is ready. Flux ${cl.info.fluxVersion ?? ""} on Kubernetes ${cl.info.kubernetesVersion ?? ""}.`,
        );
        continue;
      }
      const r = findResource(cl, ref.kind, ref.namespace, ref.name, ref.group || undefined);
      if (!r) continue;
      steps.push({ tool: "get_resource", args: { cluster: ref.cluster, id: r.id }, bytes: 1830 });
      steps.push({ tool: "get_events", args: { cluster: ref.cluster, id: r.id }, bytes: 942 });
      const kids = [...cl.resources.values()].filter((x) => x.owner && refKey(x.owner) === refKey(r));
      const byKind = new Map<string, MockResource[]>();
      for (const k of kids) byKind.set(k.kind, [...(byKind.get(k.kind) ?? []), k]);
      const manages = [...byKind]
        .slice(0, 4)
        .map(
          ([kind, xs]) =>
            `- **${kindInfo(kind).plural}**\n${xs
              .slice(0, 4)
              .map((x) => `  - ${cite(ref.cluster, x)}`)
              .join("\n")}`,
        )
        .join("\n");
      if (manages) parts.push(`${cite(ref.cluster, r)} manages:\n${manages}`);
      if (r.status === "failed")
        parts.push(
          `${cite(ref.cluster, r)} is failing: ${r.message ?? "no message"}\n- Latest event: \`${r.events[0]?.reason ?? "none"}\`, seen ${r.events[0]?.count ?? 0} times.\n- Check what changed in the source, then reconcile with source (press R).`,
        );
      else if (r.status === "suspended")
        parts.push(
          `${cite(ref.cluster, r)} is suspended, so Flux is not applying changes. ${r.events[0]?.message ?? ""}\n- Resume it with \`flux resume ${r.kind.toLowerCase()} ${r.name} -n ${r.namespace}\` (press s).`,
        );
      else
        parts.push(
          `${cite(ref.cluster, r)} is **${r.status}**. ${r.message ?? ""}\n- ${r.chart ? `Chart \`${r.chart}\`` : r.revision ? `Revision \`${r.revision.slice(0, 20)}\`` : `Images: \`${r.images?.join(", ") ?? "n/a"}\``}\n- No warning events in the last hour.`,
        );
    }
    if (!refs.length) {
      steps.push({ tool: "list_unhealthy", args: {}, bytes: 3120 });
      const bad = this.clusters.flatMap((c) =>
        [...c.resources.values()]
          .filter((x) => x.status === "failed")
          .map((x) => `- ${cite(c.info.name, x)} on ${c.info.name}`),
      );
      parts.push(
        bad.length
          ? `Failing across the fleet:\n${bad.slice(0, 5).join("\n")}`
          : "Nothing is failing across the fleet.",
      );
    }
    return { steps, refs: out, text: intro + parts.join("\n\n") };
  }

  private seedChats(): void {
    const podinfo: ResourceRef = {
      cluster: "staging",
      group: "helm.toolkit.fluxcd.io",
      kind: "HelmRelease",
      namespace: "apps",
      name: "podinfo",
    };
    const a = this.newChat(
      [podinfo, { cluster: "staging", group: "", kind: "", namespace: "", name: "" }],
      "Why does the podinfo upgrade time out?",
      60 * 26,
    );
    this.chatMessage(a, human(me.display, "askai"), "Why does the podinfo upgrade time out?", 60 * 26);
    this.chatMessage(
      a,
      aiAuthor,
      "Looking at `HelmRelease/apps/podinfo` on staging.\n\nOne replica is stuck in `ImagePullBackOff`: the values point at the `6.7.2-debug` tag, which was never pushed.",
      60 * 26,
      { steps: [{ tool: "get_events", args: { cluster: "staging" }, bytes: 942 }], refs: [podinfo] },
    );
    const b = this.newChat(
      [{ cluster: "prod-eu", group: "", kind: "", namespace: "", name: "" }],
      "Anything unhealthy on prod-eu?",
      60 * 24 * 3,
    );
    this.chatMessage(b, human(me.display, "askai"), "Anything unhealthy on prod-eu?", 60 * 24 * 3);
    this.chatMessage(b, aiAuthor, "Looking at `prod-eu`.\n\nEverything is ready.", 60 * 24 * 3);
  }

  private tokensRoute(method: string, id: string | undefined, body: unknown): Response {
    if (!id && method === "GET") return json({ items: this.tokens });
    if (!id && method === "POST") {
      const { name, scopes, ttl } = body as { name: string; scopes: TokenScope[]; ttl: string };
      if (!name?.trim()) return error(400, "bad_request", "Name is required.");
      const hours = Number.parseInt(ttl, 10) || 720;
      const tid = Math.random().toString(36).slice(2, 14).padEnd(12, "0");
      const item: TokenItem = {
        id: tid,
        name,
        scopes,
        createdAt: iso(),
        expiresAt: new Date(Date.now() + hours * 3_600_000).toISOString(),
      };
      this.tokens.unshift(item);
      this.record("token.create", undefined, "ok", { name });
      const secret = Array.from(
        { length: 32 },
        () => "abcdefghijklmnopqrstuvwxyz0123456789"[Math.floor(Math.random() * 36)],
      ).join("");
      return json({ token: `eddy_pat_${tid}${secret}a1b2c3`, item }, 201);
    }
    if (id && method === "DELETE") {
      this.tokens = this.tokens.filter((t) => t.id !== id);
      this.record("token.revoke", undefined, "ok", { id });
      return new Response(null, { status: 204 });
    }
    return error(404, "not_found", "No such route.");
  }

  private seedThreads(): void {
    const podinfo: ResourceRef = {
      cluster: "staging",
      group: "helm.toolkit.fluxcd.io",
      kind: "HelmRelease",
      namespace: "apps",
      name: "podinfo",
    };
    const t1 = this.newThread(
      podinfo,
      "podinfo 6.7.2 upgrade times out on staging",
      human("alex"),
      "discussion",
      90,
    );
    this.addMessage(
      t1,
      human("alex"),
      "The upgrade to `6.7.2` has been timing out since this morning. One replica is stuck in `ImagePullBackOff`.",
      90,
    );
    this.addMessage(
      t1,
      human("dana", "mcp", "claude-code"),
      "Found it: the values file points at the `6.7.2-debug` tag, which was never pushed.\n\n- Fix in acme/fleet-infra#412\n- After merge, run `flux reconcile hr podinfo -n apps --with-source`",
      40,
    );
    this.addMessage(
      t1,
      { ...aiAuthor, display: "Ask AI" },
      "Summary: the rollout is blocked by a missing image tag (`6.7.2-debug`). Kustomization `apps` fails its health check because of it.",
      38,
    );
    const freeze = this.newThread(
      { cluster: "prod-eu", group: "", kind: "", namespace: "", name: "" },
      "Change freeze until Thursday 18:00 CET",
      human("sam"),
      "discussion",
      60 * 20,
    );
    this.addMessage(
      freeze,
      human("sam"),
      "No manual reconciles on **prod-eu** during the payment provider migration. Ping #platform if you need an exception.",
      60 * 20,
    );
    const dev: ResourceRef = {
      cluster: "dev",
      group: "kustomize.toolkit.fluxcd.io",
      kind: "Kustomization",
      namespace: "flux-system",
      name: "apps",
    };
    const t3 = this.newThread(dev, "Why is apps suspended on dev?", human("dana"), "discussion", 60 * 30);
    this.addMessage(t3, human("dana"), "Anyone know why `apps` is suspended here?", 60 * 30);
    this.addMessage(t3, human("alex"), "I paused it to test a chart locally. Will resume after.", 60 * 29);
    t3.status = "resolved";
  }

  private seedAudit(): void {
    const target: ResourceRef = {
      cluster: "staging",
      group: "helm.toolkit.fluxcd.io",
      kind: "HelmRelease",
      namespace: "apps",
      name: "podinfo",
    };
    const rows: Array<[number, string, ResourceRef | undefined, AuditEvent["result"], string]> = [
      [5, "reconcile", target, "ok", "web"],
      [42, "mcp.get_resource", target, "ok", "mcp"],
      [44, "mcp.list_unhealthy", undefined, "ok", "mcp"],
      [
        180,
        "suspend",
        {
          ...target,
          cluster: "prod-eu",
          kind: "Kustomization",
          group: "kustomize.toolkit.fluxcd.io",
          namespace: "flux-system",
          name: "apps",
        },
        "denied",
        "web",
      ],
      [60 * 26, "login", undefined, "ok", "web"],
    ];
    for (const [m, action, t, result, via] of rows) {
      this.audit.push({
        id: this.ids++,
        ts: iso(m),
        subject: me.user,
        groups: me.groups,
        via,
        action,
        target: t,
        result,
      });
    }
  }

  /** Looks up the pod a mock log stream is for. */
  logsFor(cluster: string, namespace: string, pod: string): MockResource | undefined {
    const cl = this.cluster(cluster);
    return cl ? findResource(cl, "Pod", namespace, pod) : undefined;
  }
}

const CLUSTER_SCOPED = new Set([
  "Namespace",
  "StorageClass",
  "NodePool",
  "NodeClaim",
  "EC2NodeClass",
  "ClusterExternalSecret",
  "ClusterSecretStore",
]);

const PROJECT_ORDER = ["kubernetes", "flux", "karpenter", "external-secrets"];

/** GET …/kinds: the watched kinds (count 0 included) plus every kind with inventory-only rows. */
export function kindsOf(cl: MockCluster): KindsResponse {
  const presets = cl.info.presets ?? [];
  const byRef = new Map<string, KindSummary>();
  for (const info of KINDS) {
    if (info.preset && !presets.includes(info.preset)) continue;
    byRef.set(`${info.group}/${info.kind}`, {
      group: info.group,
      kind: info.kind,
      plural: info.plural.toLowerCase(),
      namespaced: !CLUSTER_SCOPED.has(info.kind),
      project: info.project,
      watched: true,
      preset: info.preset ? info.preset : undefined,
      count: 0,
    });
  }
  for (const r of cl.resources.values()) {
    const key = `${r.group}/${r.kind}`;
    let item = byRef.get(key);
    if (!item) {
      item = {
        group: r.group,
        kind: r.kind,
        namespaced: r.namespace !== "",
        project: r.project ?? projectOf(r.group),
        watched: false,
        count: 0,
      };
      byRef.set(key, item);
    }
    item.count++;
  }
  const rank = (p: string) => {
    const i = PROJECT_ORDER.indexOf(p);
    return i < 0 ? PROJECT_ORDER.length : i;
  };
  const items = [...byRef.values()].sort(
    (a, b) =>
      rank(a.project) - rank(b.project) ||
      (rank(a.project) === PROJECT_ORDER.length ? a.project.localeCompare(b.project) : 0) ||
      a.kind.localeCompare(b.kind),
  );
  const projects = [...new Set(items.map((i) => i.project))].map((id) => ({ id, name: projectName(id) }));
  return { items, projects, presets };
}

function event(type: "Normal" | "Warning", reason: string, message: string) {
  return { type, reason, message, count: 1, source: "flux", first: iso(), last: iso() };
}

function findResource(
  cl: MockCluster,
  kind: string,
  namespace: string,
  name: string,
  group?: string,
): MockResource | undefined {
  for (const r of cl.resources.values()) {
    if (r.kind !== kind || r.namespace !== namespace || r.name !== name) continue;
    if (group === undefined || (r.group || "core") === group) return r;
  }
  return undefined;
}

const refKey = (r: { kind: string; namespace: string; name: string }) => `${r.kind}/${r.namespace}/${r.name}`;

/** Drops mock-only fields so responses match the wire shape. */
/** Per-Kind status counts, as `ClusterInfo.kinds` (inventory-only rows not counted). */
export function kindCountsOf(resources: Map<string, MockResource>): KindCounts {
  const out: KindCounts = {};
  for (const r of resources.values()) {
    if (r.inventoryOnly) continue;
    const k = out[r.kind] ?? {};
    k[r.status] = (k[r.status] ?? 0) + 1;
    out[r.kind] = k;
  }
  return out;
}

/** The data behind each json() response, so a stale read can be marked without re-parsing. */
const bodies = new WeakMap<Response, unknown>();

/** Marks a read as served from a stale view: the header, and `stale: true` in object bodies. */
function markStale(res: Response): Response {
  if (!res.ok) return res;
  const data = bodies.get(res);
  const marked = data && typeof data === "object" && !Array.isArray(data) ? { ...data, stale: true } : data;
  const out = json(marked, res.status);
  out.headers.set("X-Eddy-Stale", "true");
  return out;
}

function strip(r: MockResource) {
  const { events: _events, spec: _spec, recovers: _recovers, ...wire } = r;
  return wire;
}

function json(data: unknown, status = 200): Response {
  const res = new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
  bodies.set(res, data);
  return res;
}

function error(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status);
}
