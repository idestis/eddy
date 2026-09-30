import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  ConnectionInfo,
  CreatedCluster,
  Finding,
  JobsSnapshot,
  List,
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
