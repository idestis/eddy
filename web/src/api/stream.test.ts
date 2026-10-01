import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ getAttention: vi.fn() }));
vi.mock("./endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("./endpoints")>()),
  ...api,
}));

import { getMotion, resetLiveMotion } from "../lib/liveMotion";
import { resource } from "../test/fixtures";
import { streamUrlFor } from "./endpoints";
import { keys } from "./queries";
import { attachStreamHandlers, backoffDelay, computeWatchSet, MAX_WATCH, rewatch } from "./stream";
import type { AttentionResponse, ClusterInfo, ResourceSnapshot } from "./types";

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

describe("computeWatchSet", () => {
  it("watches the route's cluster and the side panel's when it differs", () => {
    expect(computeWatchSet(["prod", "staging"], [])).toEqual(["prod", "staging"]);
    expect(computeWatchSet(["prod", "prod"], [])).toEqual(["prod"]);
    expect(computeWatchSet(["prod", undefined], [])).toEqual(["prod"]);
  });

  it("keeps the current set (same array) when everything required is watched", () => {
    const current = ["prod", "staging", "dev"];
    expect(computeWatchSet(["staging"], current)).toBe(current);
  });

  it("puts new clusters first and keeps recent ones, at most 5", () => {
    expect(computeWatchSet(["e"], ["a", "b", "c", "d"])).toEqual(["e", "a", "b", "c", "d"]);
    expect(computeWatchSet(["f"], ["e", "a", "b", "c", "d"])).toEqual(["f", "e", "a", "b", "c"]);
    expect(computeWatchSet(["a", "b", "c", "d", "e", "f"], [], MAX_WATCH)).toHaveLength(5);
  });

  it("watches nothing on the fleet page", () => {
    expect(computeWatchSet([undefined], ["prod"])).toEqual([]);
    const none: string[] = [];
    expect(computeWatchSet([], none)).toBe(none);
  });

  it("builds the stream URL with the watch parameter, empty included", () => {
    expect(streamUrlFor(["prod-eu", "a b"])).toBe("/api/v1/stream?watch=prod-eu,a%20b");
    expect(streamUrlFor([])).toBe("/api/v1/stream?watch=");
  });
});

describe("rewatch", () => {
  it("marks clusters that left stale without refetching; the hub resyncs ones that joined", () => {
    const qc = new QueryClient();
    const spy = vi.spyOn(qc, "invalidateQueries");
    rewatch(qc, ["a", "b"], ["b", "c"]);
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.resources("a"), refetchType: "none" });
    expect(spy).toHaveBeenCalledTimes(1);
  });
});

describe("scoped stream resync", () => {
  const scoped = (watch: string[]) => {
    const qc = new QueryClient();
    const source = new FakeSource();
    attachStreamHandlers(source as unknown as EventSource, qc, watch);
    return { qc, source, spy: vi.spyOn(qc, "invalidateQueries") };
  };

  it("refetches a watched cluster's resources", () => {
    const { source, spy } = scoped(["prod"]);
    source.emit("resync", { cluster: "prod" });
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.resources("prod") });
    expect(spy).not.toHaveBeenCalledWith({ queryKey: keys.attentionAll });
  });

  it("refetches only the attention of an unwatched cluster, spliced into the fleet's", async () => {
    const { qc, source, spy } = scoped(["prod"]);
    const old = resource("old", { status: "failed" });
    const fresh = resource("fresh", { status: "failed" });
    const other = resource("x", { status: "failed" });
    qc.setQueryData<AttentionResponse>(keys.attention(""), {
      items: [
        { cluster: "dev", resource: old },
        { cluster: "prod", resource: other },
      ],
      total: 2,
      findings: [],
    });
    api.getAttention.mockResolvedValueOnce({
      items: [{ cluster: "dev", resource: fresh }],
      total: 1,
      findings: [],
    });
    source.emit("resync", { cluster: "dev" });
    expect(spy).not.toHaveBeenCalledWith({ queryKey: keys.resources("dev") });
    await vi.waitFor(() =>
      expect(
        qc.getQueryData<AttentionResponse>(keys.attention(""))?.items.map((i) => i.resource.name),
      ).toEqual(["x", "fresh"]),
    );
    expect(api.getAttention).toHaveBeenCalledWith("dev");
  });

  it("refetches attention on hello, since the stream may open after it loaded", () => {
    const { source, spy } = scoped([]);
    source.emit("hello", {});
    expect(spy).toHaveBeenCalledWith({ queryKey: keys.attentionAll });
  });

  it("replaces a cluster's findings from an `attention` event", () => {
    const { qc, source } = scoped([]);
    const finding = {
      id: "job-buildup/x",
      kind: "job-buildup",
      severity: "warning",
      namespace: "x",
      message: "m",
    };
    const info = { id: "job-buildup/y", kind: "job-buildup", severity: "info", namespace: "y", message: "i" };
    qc.setQueryData(keys.clusters, [{ name: "prod", findings: [] } as unknown as ClusterInfo]);
    qc.setQueryData<AttentionResponse>(keys.attention(""), { items: [], total: 0, findings: [] });
    source.emit("attention", { cluster: "prod", upserts: [], deletes: [], findings: [finding, info] });
    expect(qc.getQueryData<ClusterInfo[]>(keys.clusters)?.[0]?.findings).toHaveLength(2);
    expect(qc.getQueryData<AttentionResponse>(keys.attention(""))?.findings).toEqual([
      { cluster: "prod", finding },
    ]);
  });
});

describe("counts and attention events", () => {
  const cluster = (name: string, patch: Partial<ClusterInfo> = {}) =>
    ({ name, displayName: name, order: 0, protected: false, connected: true, ...patch }) as ClusterInfo;

  it("applies `counts` to the cluster list without touching other clusters", () => {
    const { qc, source } = setup();
    const other = cluster("dev", { counts: { ready: 1 } });
    qc.setQueryData(keys.clusters, [cluster("prod", { counts: { ready: 1 } }), other]);
    source.emit("counts", {
      cluster: "prod",
      counts: { ready: 3, failed: 1 },
      kinds: { HelmRelease: { ready: 3, failed: 1 } },
    });
    const [prod, dev] = qc.getQueryData<ClusterInfo[]>(keys.clusters) ?? [];
    expect(prod?.counts).toEqual({ ready: 3, failed: 1 });
    expect(prod?.kinds).toEqual({ HelmRelease: { ready: 3, failed: 1 } });
    expect(dev).toBe(other);
  });

  it("patches `attention` into the fleet's and the cluster's attention queries only", () => {
    const { qc, source } = setup();
    const bad = resource("apps", { status: "failed" });
    const empty: AttentionResponse = { items: [], total: 0, findings: [] };
    qc.setQueryData(keys.attention(""), empty);
    qc.setQueryData(keys.attention("prod"), empty);
    qc.setQueryData(keys.attention("dev"), empty);
    source.emit("attention", { cluster: "prod", upserts: [bad], deletes: [] });
    expect(qc.getQueryData<AttentionResponse>(keys.attention(""))?.items).toEqual([
      { cluster: "prod", resource: bad },
    ]);
    expect(qc.getQueryData<AttentionResponse>(keys.attention("prod"))?.total).toBe(1);
    expect(qc.getQueryData(keys.attention("dev"))).toBe(empty);
  });

  it("derives attention from a watched cluster's `change` too", () => {
    const { qc, source } = setup();
    const a = resource("apps", { status: "failed" });
    qc.setQueryData<AttentionResponse>(keys.attention(""), {
      items: [{ cluster: "prod", resource: a }],
      total: 1,
      findings: [],
    });
    source.emit("change", { cluster: "prod", upserts: [{ ...a, status: "ready" }], deletes: [] });
    expect(qc.getQueryData<AttentionResponse>(keys.attention(""))).toMatchObject({ items: [], total: 0 });
  });
});
