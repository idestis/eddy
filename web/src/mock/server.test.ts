import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ConnectionInfo, CreatedCluster } from "../api/types";
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
