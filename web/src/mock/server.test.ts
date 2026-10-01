import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  ConnectionInfo,
  CreatedCluster,
  Finding,
  GraphResponse,
  JobsSnapshot,
  KindsResponse,
  List,
  Resource,
  ResourceSnapshot,
} from "../api/types";
import { MockHub } from "./server";

const headers = new Headers({ "X-Eddy-CSRF": "mock-csrf-token" });
const call = async <T>(hub: MockHub, method: string, path: string, body?: unknown) => {
  const res = hub.handle(method, new URL(path, "http://localhost"), body, headers);
  const text = await res.text();
  return { status: res.status, data: (text ? JSON.parse(text) : undefined) as T };
};

describe("mock onboarding", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("creates a pending cluster whose simulated agent joins", async () => {
    const hub = new MockHub();
    const events: string[] = [];
    hub.subscribe((e) => events.push(e));
    const created = await call<CreatedCluster>(hub, "POST", "/api/v1/clusters", {
      name: "prod-us",
      ttl: "1h",
    });
    expect(created.status).toBe(201);
    expect(created.data.joinToken.token).toMatch(/^eddy_join_[0-9A-Za-z]{50}$/);
    expect(created.data.guide.helm).toContain(`--set-string joinToken=${created.data.joinToken.token}`);
    expect(created.data.cluster.phase).toBe("Pending");
    expect((await call(hub, "POST", "/api/v1/clusters", { name: "prod-us" })).status).toBe(409);
    expect((await call(hub, "POST", "/api/v1/clusters", { name: "Bad_Name" })).status).toBe(400);

    const conn = () => call<ConnectionInfo>(hub, "GET", "/api/v1/clusters/prod-us/connection");
    expect((await conn()).data.checks[0]?.state).toBe("pending");
    vi.advanceTimersByTime(2_100);
    expect((await conn()).data.attempts[0]?.reason).toBe("bad_token");
    vi.advanceTimersByTime(3_000);
    let info = (await conn()).data;
    expect(info.checks.find((c) => c.id === "connected")?.state).toBe("ok");
    expect(info.checks.find((c) => c.id === "informers")?.state).toBe("pending");
    expect(info.joinToken?.state).toBe("used");
    vi.advanceTimersByTime(2_600);
    info = (await conn()).data;
    expect(info.checks.find((c) => c.id === "informers")?.state).toBe("ok");
    expect(hub.clusterInfos().find((c) => c.name === "prod-us")?.connected).toBe(true);
    expect(events).toContain("connection");
  });

  it("guards deletes of protected and Helm-managed clusters", async () => {
    const hub = new MockHub();
    await call(hub, "POST", "/api/v1/clusters", { name: "locked", protected: true });
    expect((await call(hub, "DELETE", "/api/v1/clusters/locked", {})).status).toBe(428);
    expect((await call(hub, "DELETE", "/api/v1/clusters/locked", { confirm: "locked" })).status).toBe(204);
    expect((await call(hub, "DELETE", "/api/v1/clusters/prod-eu", { confirm: "prod-eu" })).status).toBe(409);
    expect((await call(hub, "PATCH", "/api/v1/clusters/staging", { region: "us-east-1" })).status).toBe(200);
  });
});

describe("mock prefs", () => {
  it("stores one object per session and guards the body", async () => {
    const hub = new MockHub();
    expect((await call<{ data: object }>(hub, "GET", "/api/v1/prefs")).data).toEqual({ data: {} });
    const put = await call<{ data: object }>(hub, "PUT", "/api/v1/prefs", {
      data: { clusters: { pins: ["a"] } },
    });
    expect(put.status).toBe(200);
    expect((await call<{ data: object }>(hub, "GET", "/api/v1/prefs")).data).toEqual({
      data: { clusters: { pins: ["a"] } },
    });
    expect((await call(hub, "PUT", "/api/v1/prefs", { data: [1] })).status).toBe(400);
    expect((await call(hub, "PUT", "/api/v1/prefs", {})).status).toBe(400);
    const noCsrf = hub.handle(
      "PUT",
      new URL("/api/v1/prefs", "http://localhost"),
      { data: {} },
      new Headers(),
    );
    expect(noCsrf.status).toBe(403);
  });
});

describe("mock hidden finished Jobs", () => {
  const base = "/api/v1/clusters/prod-eu/resources";

  it("reports a warning job-buildup finding and completed Jobs", async () => {
    const hub = new MockHub();
    const info = hub.clusterInfos().find((c) => c.name === "prod-eu");
    const warning = info?.findings?.find((f) => f.severity === "warning");
    expect(warning?.namespace).toBe("prefect");
    expect(warning?.jobs?.hidden).toBe(1240);
    expect(info?.counts?.completed).toBeGreaterThan(0);
    const findings = await call<List<Finding>>(hub, "GET", "/api/v1/clusters/prod-eu/findings");
    expect(findings.data.items.map((f) => f.id)).toEqual(["job-buildup/prefect", "job-buildup/apps"]);
  });

  it("hides them from the normal list and pages through them with includeHidden", async () => {
    const hub = new MockHub();
    const normal = await call<ResourceSnapshot>(hub, "GET", base);
    const listedJobs = normal.data.items.filter((r) => r.kind === "Job" && r.namespace === "prefect");
    expect(listedJobs).toHaveLength(16);

    const first = await call<JobsSnapshot>(hub, "GET", `${base}?kind=Job&includeHidden=1&namespace=prefect`);
    expect(first.status).toBe(200);
    expect(first.data.items).toHaveLength(16 + 500);
    expect(first.data.items.every((r) => r.kind === "Job" && r.namespace === "prefect")).toBe(true);
    expect(first.data.hidden.total).toBe(1240);
    expect(first.data.resourceVersion).not.toBe("");

    const ids = new Set(first.data.items.map((r) => r.id));
    let next = first.data.hidden.next;
    let pages = 0;
    while (next) {
      const page = await call<JobsSnapshot>(
        hub,
        "GET",
        `${base}?kind=Job&includeHidden=1&namespace=prefect&cursor=${next}`,
      );
      expect(page.data.resourceVersion).toBe("");
      for (const r of page.data.items) ids.add(r.id);
      next = page.data.hidden.next;
      pages++;
    }
    expect(pages).toBe(2);
    expect(ids.size).toBe(16 + 1240);

    const small = await call<JobsSnapshot>(hub, "GET", `${base}?kind=Job&includeHidden=1&limit=10`);
    expect(small.data.hidden).toEqual({ total: 1252, next: "h10" });
  });

  it("rejects includeHidden without kind=Job and bad limits", async () => {
    const hub = new MockHub();
    expect((await call(hub, "GET", `${base}?includeHidden=1`)).status).toBe(400);
    expect((await call(hub, "GET", `${base}?kind=Pod&includeHidden=1`)).status).toBe(400);
    expect((await call(hub, "GET", `${base}?kind=Job&includeHidden=1&limit=5000`)).status).toBe(400);
  });
});

describe("mock kinds and inventory reads", () => {
  it("serves /kinds per cluster, with presets deciding which kinds are watched", async () => {
    const hub = new MockHub();
    const prod = (await call<KindsResponse>(hub, "GET", "/api/v1/clusters/prod-eu/kinds")).data;
    const find = (k: KindsResponse, kind: string) => k.items.find((i) => i.kind === kind);
    expect(find(prod, "NodePool")).toMatchObject({
      watched: true,
      preset: "karpenter",
      project: "karpenter",
    });
    expect(find(prod, "NodePool")?.count).toBeGreaterThan(0);
    expect(find(prod, "DatadogAgent")).toMatchObject({ watched: false, project: "datadoghq.com" });
    expect(prod.projects.map((p) => p.id).slice(0, 4)).toEqual([
      "kubernetes",
      "flux",
      "karpenter",
      "external-secrets",
    ]);
    const dev = (await call<KindsResponse>(hub, "GET", "/api/v1/clusters/dev/kinds")).data;
    expect(find(dev, "ExternalSecret")).toMatchObject({ watched: false });
    expect(find(dev, "NodePool")?.watched).toBe(false);
  });

  it("has a failing ExternalSecret and a NodePool near its CPU limit", async () => {
    const hub = new MockHub();
    const { data } = await call<ResourceSnapshot>(hub, "GET", "/api/v1/clusters/prod-eu/resources");
    const es = data.items.find((r) => r.kind === "ExternalSecret" && r.status === "failed");
    expect(es?.message).toMatch(/^SecretSyncedError/);
    expect(es?.details?.find((d) => d.label === "Target secret")?.value).toBe("stripe-api-key");
    const pool = data.items.find((r) => r.kind === "NodePool" && r.name === "general");
    expect(pool?.details?.find((d) => d.label === "CPU")?.value).toBe("94 / 100");
    expect(data.items.every((r) => r.project)).toBe(true);
  });

  it("reads YAML and events of inventory-only rows by group, and refuses Secret YAML", async () => {
    const hub = new MockHub();
    const base = "/api/v1/clusters/prod-eu/objects";
    const yaml = await call<{ yaml: string }>(
      hub,
      "GET",
      `${base}/DatadogAgent/monitoring/datadog/yaml?group=datadoghq.com`,
    );
    expect(yaml.status).toBe(200);
    expect(yaml.data.yaml).toContain("kind: DatadogAgent");
    expect(
      (await call(hub, "GET", `${base}/DatadogAgent/monitoring/datadog/events?group=datadoghq.com`)).status,
    ).toBe(200);
    expect(
      (await call(hub, "GET", `${base}/DatadogAgent/monitoring/datadog/yaml?group=other.io`)).status,
    ).toBe(404);
    expect((await call(hub, "GET", `${base}/Secret/apps/checkout-db/yaml?group=core`)).status).toBe(403);
    expect((await call(hub, "GET", `${base}/Secret/apps/checkout-db/events?group=core`)).status).toBe(200);
  });
});

describe("mock dependency graph", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const KS = "kustomize.toolkit.fluxcd.io/Kustomization/flux-system";
  const HR = "helm.toolkit.fluxcd.io/HelmRelease/apps";

  it("serves GET …/graph like the hub: flux kinds, focus, hops and errors", async () => {
    const hub = new MockHub();
    const g = await call<GraphResponse>(hub, "GET", "/api/v1/clusters/prod-eu/graph?kinds=flux");
    expect(g.status).toBe(200);
    expect(g.data.truncated).toBe(false);
    expect(g.data.nodes.every((n) => n.kind !== "Deployment")).toBe(true);
    expect(g.data.edges).toContainEqual({ from: `${KS}/apps`, to: `${KS}/infra-configs`, type: "dependsOn" });
    expect(g.data.nodes.find((n) => n.id === `${HR}/alchemic-worker`)).toMatchObject({ blocked: true });
    const focused = await call<GraphResponse>(
      hub,
      "GET",
      `/api/v1/clusters/prod-eu/graph?kinds=all&focus=${encodeURIComponent(`${HR}/alchemic`)}&hops=1`,
    );
    expect(focused.data.nodes[0]?.id).toBe(`${HR}/alchemic`);
    expect(focused.data.nodes.some((n) => n.kind === "Deployment")).toBe(true);
    expect((await call(hub, "GET", "/api/v1/clusters/prod-eu/graph?kinds=nope")).status).toBe(400);
    expect((await call(hub, "GET", "/api/v1/clusters/prod-eu/graph?hops=0")).status).toBe(400);
    expect((await call(hub, "GET", "/api/v1/clusters/prod-eu/graph?focus=a/B/c/d")).status).toBe(404);
    hub.noGraph = true;
    expect((await call(hub, "GET", "/api/v1/clusters/prod-eu/graph")).status).toBe(404);
  });

  it("a reconcile of the failing release travels downstream and unblocks its dependent", async () => {
    const hub = new MockHub();
    const seen: Array<{ id: string; status: string; blocked?: boolean }> = [];
    hub.subscribe((event, data) => {
      if (event !== "change") return;
      for (const r of (data as { upserts: Resource[] }).upserts)
        seen.push({ id: r.id, status: r.status, blocked: r.blocked });
    });
    const res = await call(
      hub,
      "POST",
      "/api/v1/clusters/prod-eu/objects/HelmRelease/apps/alchemic/reconcile",
      {},
    );
    expect(res.status).toBe(202);
    vi.advanceTimersByTime(6_000);
    const worker = seen.filter((s) => s.id === `${HR}/alchemic-worker`);
    expect(seen.find((s) => s.id === `${HR}/alchemic`)?.status).toBe("reconciling");
    expect(seen.filter((s) => s.id === `${HR}/alchemic`).at(-1)?.status).toBe("ready");
    expect(worker.map((s) => s.status)).toEqual(["reconciling", "ready"]);
    expect(worker.at(-1)?.blocked).toBeUndefined();
  });
});
