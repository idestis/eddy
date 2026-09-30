// An in-memory hub that answers the routes of docs/api.md for mock mode.
// It enforces the same CSRF header and protected-cluster confirmation as the
// real hub so the UI's handling of both is exercised.

import type {
  AskStep,
  AuditEvent,
  Author,
  ChangeEvent,
  ClusterInfo,
  ClusterInput,
  ClusterPhase,
  ConnectionAttempt,
  ConnectionCheck,
  ConnectionInfo,
  JoinTokenInfo,
  Me,
  Message,
  OnboardedCluster,
  ResourceRef,
  Thread,
  TokenItem,
  TokenScope,
} from "../api/types";
import { kindInfo } from "../lib/kinds";
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
    workloadLogs: false,
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

  constructor(extraPods = 0) {
    this.clusters = buildFleet(extraPods);
    this.seedOnboarding();
    this.seedThreads();
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
    return this.clusters.map((c) => ({
      ...c.info,
      counts: c.info.connected ? countsOf(c.resources) : c.info.counts,
    }));
  }

  /** Applies a change to a resource and broadcasts it like the hub would. */
  private update(c: MockCluster, r: MockResource, patch: Partial<MockResource>): void {
    Object.assign(r, patch, { resourceVersion: nextResourceVersion(), lastChanged: iso() });
    const change: ChangeEvent = { cluster: c.info.name, upserts: [strip(r)], deletes: [] };
    this.emit("change", change);
    this.emit("clusters", { items: this.clusterInfos() });
  }

  /** Background churn so the live indicator and SSE deltas have something to show. */
  tick(): void {
    const prod = this.cluster("prod-eu");
    if (!prod) return;
    const pods = [...prod.resources.values()].filter(
      (r) => r.kind === "Pod" && r.name.startsWith("checkout"),
    );
    const p = pods[Math.floor(Math.random() * pods.length)];
    if (!p || p.status === "reconciling") return;
    this.update(prod, p, { message: `Running (restarts: ${Math.floor(Math.random() * 3)})` });
  }

  /**
   * GET …/resources, with the hub's optional kind and namespace filters. includeHidden (Jobs
   * only) adds the hidden finished Jobs a page at a time; a cursor page holds hidden Jobs only.
   */
  private resourcesRoute(cl: MockCluster, q: URLSearchParams): Response {
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
    if (path === "/auth/providers") return json({ local: true, proxy: false, dev: true });
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
    if (root === "clusters" && p.length === 1 && method === "GET")
      return json({ items: this.clusterInfos() });
    if (root === "clusters") {
      const res = this.onboardingRoute(method, a, b, body);
      if (res) return res;
    }

    if (root === "clusters" && a) {
      const cl = this.cluster(a);
      if (!cl) return error(404, "not_found", `Cluster ${a} not found.`);
      if (!cl.info.connected) return error(503, "disconnected", `${a} is disconnected.`);
      if (b === "resources") return this.resourcesRoute(cl, url.searchParams);
      if (b === "findings" && !c) return json({ items: cl.info.findings ?? [] });
      if (b === "objects" && c && d && e) {
        const r = findResource(cl, c, d === "_" ? "" : d, e);
        if (!r) return error(404, "not_found", `${c}/${e} not found.`);
        const target: ResourceRef = {
          cluster: a,
          group: r.group,
          kind: r.kind,
          namespace: r.namespace,
          name: r.name,
        };
        if (!f && method === "GET") return json(strip(r));
        if (f === "yaml" && r.inventoryOnly)
          return error(403, "forbidden", `Eddy does not read ${r.kind} objects; it knows only the name.`);
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
    if (root === "ai" && a === "ask" && method === "POST") return this.ask(body);
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

  private act(cl: MockCluster, r: MockResource, action: "reconcile" | "suspend" | "resume"): void {
    if (action === "suspend") {
      this.update(cl, r, { status: "suspended", suspended: true, message: "Reconciliation is suspended" });
      r.events.unshift(event("Normal", "Suspended", `Suspended by ${me.display}`));
      return;
    }
    const before = { status: r.status, message: r.message };
    this.update(cl, r, {
      status: "reconciling",
      suspended: false,
      message: action === "resume" ? "Resuming reconciliation" : "Reconciliation in progress",
    });
    r.events.unshift(event("Normal", "ReconcileRequested", `Reconcile requested by ${me.display}`));
    setTimeout(() => {
      if (before.status === "failed") {
        this.update(cl, r, { status: "failed", message: before.message });
        r.events.unshift(event("Warning", "ReconciliationFailed", before.message ?? ""));
      } else {
        const message =
          r.kind === "Kustomization"
            ? `Applied revision: ${r.revision ?? ""}`
            : r.kind === "HelmRelease"
              ? `Helm upgrade succeeded for release ${r.namespace}/${r.name} with chart ${r.chart ?? ""}`
              : "stored artifact for the latest revision";
        this.update(cl, r, { status: "ready", message });
        r.events.unshift(event("Normal", "ReconciliationSucceeded", message));
      }
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
      visibility: type === "ask" ? "private" : "resource",
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

  private ask(body: unknown): Response {
    if (!me.features.ai) return error(503, "disabled", "Ask AI is turned off.");
    const { cluster, resourceId, threadId, question } = body as {
      cluster: string;
      resourceId?: string;
      threadId?: string;
      question: string;
    };
    const cl = this.cluster(cluster);
    if (!cl) return error(404, "not_found", "Cluster not found.");
    const r = resourceId ? cl.resources.get(resourceId) : undefined;
    const ref: ResourceRef = r
      ? { cluster, group: r.group, kind: r.kind, namespace: r.namespace, name: r.name }
      : { cluster, group: "", kind: "", namespace: "", name: "" };
    const thread =
      this.threads.find((t) => t.id === threadId) ??
      this.newThread(ref, question.slice(0, 80), human(me.display), "ask");
    this.addMessage(thread, human(me.display, "askai"), question);

    const steps: AskStep[] = [];
    let text: string;
    if (r) {
      steps.push({ tool: "get_resource", args: { cluster, id: r.id }, bytes: 1830 });
      steps.push({ tool: "get_events", args: { cluster, id: r.id }, bytes: 942 });
      if (r.status === "failed") {
        text = `\`${r.name}\` is failing: ${r.message ?? "no message"}\n\n- Latest event: \`${r.events[0]?.reason ?? "none"}\`, seen ${r.events[0]?.count ?? 0} times.\n- Check what changed in the source, then run \`flux reconcile ${r.kind.toLowerCase()} ${r.name} -n ${r.namespace} --with-source\` (press R).\n- If it was working before, pin the previous version in Git.`;
      } else if (r.status === "suspended") {
        text = `\`${r.name}\` is suspended, so Flux is not applying changes.\n\n- ${r.events[0]?.message ?? "No suspend event is recorded."}\n- Resume it with \`flux resume ${r.kind.toLowerCase()} ${r.name} -n ${r.namespace}\` (press s).`;
      } else {
        text = `\`${r.name}\` is **${r.status}**. ${r.message ?? ""}\n\n- ${r.chart ? `Chart \`${r.chart}\`` : r.revision ? `Revision \`${r.revision.slice(0, 20)}\`` : `Images: \`${r.images?.join(", ") ?? "n/a"}\``}\n- No warning events in the last hour.`;
      }
    } else {
      steps.push({ tool: "search_resources", args: { cluster, status: "failed" }, bytes: 2210 });
      const bad = [...cl.resources.values()].filter((x) => x.status === "failed" || x.status === "suspended");
      text = bad.length
        ? `${bad.length} object${bad.length === 1 ? " needs" : "s need"} attention on \`${cluster}\`:\n\n${bad
            .slice(0, 4)
            .map((x) => `- \`${x.kind}/${x.name}\` is ${x.status}: ${x.message ?? ""}`)
            .join("\n")}`
        : `Everything on \`${cluster}\` is ready. Flux ${cl.info.fluxVersion ?? ""} is running on Kubernetes ${cl.info.kubernetesVersion ?? ""}.`;
    }
    const message = this.addMessage(thread, aiAuthor, text, 0, {
      steps,
      usage: { inputTokens: 2310, outputTokens: 164 },
    });
    this.record("ai.ask", ref, "ok");
    return json({ threadId: thread.id, message, steps });
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

function event(type: "Normal" | "Warning", reason: string, message: string) {
  return { type, reason, message, count: 1, source: "flux", first: iso(), last: iso() };
}

function findResource(
  cl: MockCluster,
  kind: string,
  namespace: string,
  name: string,
): MockResource | undefined {
  for (const r of cl.resources.values()) {
    if (r.kind === kind && r.namespace === namespace && r.name === name) return r;
  }
  return undefined;
}

const refKey = (r: { kind: string; namespace: string; name: string }) => `${r.kind}/${r.namespace}/${r.name}`;

/** Drops mock-only fields so responses match the wire shape. */
function strip(r: MockResource) {
  const { events: _events, spec: _spec, ...wire } = r;
  return wire;
}

function json(data: unknown, status = 200): Response {
  return new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
}

function error(status: number, code: string, message: string): Response {
  return json({ error: { code, message } }, status);
}
