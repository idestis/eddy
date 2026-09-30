// An in-memory hub that answers the routes of docs/api.md for mock mode.
// It enforces the same CSRF header and protected-cluster confirmation as the
// real hub so the UI's handling of both is exercised.

import type {
  AskStep,
  AuditEvent,
  Author,
  ChangeEvent,
  ClusterInfo,
  Me,
  Message,
  ResourceRef,
  Thread,
  TokenItem,
  TokenScope,
} from "../api/types";
import { kindInfo } from "../lib/kinds";
import {
  buildFleet,
  countsOf,
  type MockCluster,
  type MockResource,
  nextResourceVersion,
  yamlOf,
} from "./fixtures";

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
  },
  version: "v0.1.0-mock",
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

  constructor(extraPods = 0) {
    this.clusters = buildFleet(extraPods);
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
    if (root === "clusters" && p.length === 1) return json({ items: this.clusterInfos() });

    if (root === "clusters" && a) {
      const cl = this.cluster(a);
      if (!cl) return error(404, "not_found", `Cluster ${a} not found.`);
      if (!cl.info.connected) return error(503, "disconnected", `${a} is disconnected.`);
      if (b === "resources") {
        return json({ items: [...cl.resources.values()].map(strip), resourceVersion: nextResourceVersion() });
      }
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

    if (root === "threads") return this.threadsRoute(method, a, b, url, body);
    if (root === "ai" && a === "ask" && method === "POST") return this.ask(body);
    if (root === "tokens") return this.tokensRoute(method, a, body);
    if (root === "audit")
      return json({ items: this.audit.filter((x) => x.subject === me.user).slice(0, 50) });

    return error(404, "not_found", "No such route.");
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
