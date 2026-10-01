import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { getMotion, resetLiveMotion } from "../lib/liveMotion";
import { resource } from "../test/fixtures";
import { keys } from "./queries";
import { attachStreamHandlers, backoffDelay } from "./stream";
import type { ClusterInfo, ResourceSnapshot } from "./types";

class FakeSource extends EventTarget {
  emit(type: string, data: unknown) {
    this.dispatchEvent(new MessageEvent(type, { data: JSON.stringify(data) }));
  }
}

function setup() {
  const qc = new QueryClient();
  const source = new FakeSource();
  attachStreamHandlers(source as unknown as EventSource, qc);
  return { qc, source };
}

describe("stream handlers", () => {
  it("patches `change` events into the cluster's resources query", () => {
    const { qc, source } = setup();
    const a = resource("a");
    qc.setQueryData<ResourceSnapshot>(keys.resources("prod"), { items: [a], resourceVersion: "1" });
    source.emit("change", {
      cluster: "prod",
      upserts: [{ ...a, status: "failed", resourceVersion: "2" }],
      deletes: [],
    });
    expect(qc.getQueryData<ResourceSnapshot>(keys.resources("prod"))?.items[0]?.status).toBe("failed");
  });

  it("records live motion for a delta: status flash, new row, leaving row", () => {
    resetLiveMotion();
    const { qc, source } = setup();
    const a = resource("a", { status: "reconciling" });
    const b = resource("b");
    qc.setQueryData<ResourceSnapshot>(keys.resources("prod"), { items: [a, b], resourceVersion: "1" });
    const c = resource("c");
    source.emit("change", {
      cluster: "prod",
      upserts: [{ ...a, status: "completed", resourceVersion: "2" }, c],
      deletes: [b.id],
    });
    const m = getMotion("prod");
    expect(m.changed.get(a.id)).toMatchObject({ kind: "status", from: "reconciling", to: "completed" });
    expect(m.entered.has(c.id)).toBe(true);
    expect(m.leaving.get(b.id)?.resource.name).toBe("b");
    resetLiveMotion();
  });

  it("does not create a cache entry for clusters that were never loaded", () => {
    const { qc, source } = setup();
    source.emit("change", { cluster: "other", upserts: [resource("x")], deletes: [] });
    expect(qc.getQueryData(keys.resources("other"))).toBeUndefined();
  });

  it("invalidates the cluster on `resync`", () => {
    const { qc, source } = setup();
    const spy = vi.spyOn(qc, "invalidateQueries");
    source.emit("resync", { cluster: "prod" });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.resources("prod") });
  });

  it("replaces the clusters list on `clusters`, sorted by order", () => {
    const { qc, source } = setup();
    const c = (name: string, order: number) => ({ name, order }) as ClusterInfo;
    source.emit("clusters", { items: [c("b", 2), c("a", 1)] });
    expect(qc.getQueryData<ClusterInfo[]>(keys.clusters)?.map((x) => x.name)).toEqual(["a", "b"]);
  });

  it("refetches the connection checklist and the clusters on `connection`", () => {
    const { qc, source } = setup();
    const spy = vi.spyOn(qc, "invalidateQueries");
    source.emit("connection", { cluster: "prod" });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.connection("prod") });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.clusters });
  });

  it("invalidates thread queries on `thread`", () => {
    const { qc, source } = setup();
    const spy = vi.spyOn(qc, "invalidateQueries");
    source.emit("thread", { threadId: "t1" });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.threadsAll });
  });

  it("ignores malformed frames", () => {
    const { qc, source } = setup();
    source.dispatchEvent(new MessageEvent("change", { data: "{not json" }));
    expect(qc.getQueryCache().getAll()).toHaveLength(0);
  });
});

describe("backoffDelay", () => {
  it("grows exponentially and caps at 30s", () => {
    expect(backoffDelay(0, () => 1)).toBe(1000);
    expect(backoffDelay(3, () => 1)).toBe(8000);
    expect(backoffDelay(20, () => 1)).toBe(30000);
    expect(backoffDelay(5, () => 0)).toBe(500);
  });
});
