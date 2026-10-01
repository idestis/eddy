import { describe, expect, it } from "vitest";
import { resource } from "../test/fixtures";
import { applyAttention, applyChange, applyCounts } from "./delta";
import type { AttentionResponse, ClusterInfo, ResourceSnapshot } from "./types";

const snapshot = (...items: ReturnType<typeof resource>[]): ResourceSnapshot => ({
  items,
  resourceVersion: "10",
});

describe("applyChange", () => {
  const a = resource("a", { resourceVersion: "5" });
  const b = resource("b", { resourceVersion: "6" });

  it("returns undefined when there is no snapshot yet", () => {
    expect(applyChange(undefined, { upserts: [a], deletes: [] })).toBeUndefined();
  });

  it("replaces an upserted item in place, keeping order", () => {
    const next = applyChange(snapshot(a, b), {
      upserts: [{ ...a, status: "failed", resourceVersion: "7" }],
      deletes: [],
    });
    expect(next?.items.map((r) => [r.name, r.status])).toEqual([
      ["a", "failed"],
      ["b", "ready"],
    ]);
  });

  it("appends new items and removes deleted ones", () => {
    const c = resource("c");
    const next = applyChange(snapshot(a, b), { upserts: [c], deletes: [a.id] });
    expect(next?.items.map((r) => r.name)).toEqual(["b", "c"]);
  });

  it("ignores an upsert that is older than the cached item", () => {
    const prev = snapshot(a, b);
    const next = applyChange(prev, {
      upserts: [{ ...b, status: "failed", resourceVersion: "3" }],
      deletes: [],
    });
    expect(next).toBe(prev);
  });

  it("applies upserts with non-numeric resource versions", () => {
    const next = applyChange(snapshot(a), {
      upserts: [{ ...a, message: "x", resourceVersion: "abc" }],
      deletes: [],
    });
    expect(next?.items[0]?.message).toBe("x");
  });

  it("drops an item that is both upserted and deleted in one event", () => {
    const c = resource("c");
    const next = applyChange(snapshot(a), { upserts: [c], deletes: [c.id] });
    expect(next?.items.map((r) => r.name)).toEqual(["a"]);
  });

  it("returns the same object when nothing changes", () => {
    const prev = snapshot(a);
    expect(applyChange(prev, { upserts: [], deletes: ["missing"] })).toBe(prev);
    expect(applyChange(prev, { upserts: [], deletes: [] })).toBe(prev);
  });

  it("handles thousands of rows quickly", () => {
    const many = Array.from({ length: 5000 }, (_, i) => resource(`r${i}`, { resourceVersion: "1" }));
    const prev = snapshot(...many);
    const target = resource("r2500", { status: "failed", resourceVersion: "2" });
    const start = performance.now();
    const next = applyChange(prev, { upserts: [target], deletes: [] });
    expect(performance.now() - start).toBeLessThan(50);
    expect(next?.items[2500]?.status).toBe("failed");
  });
});

describe("applyCounts", () => {
  const c = (name: string, patch: Partial<ClusterInfo> = {}) =>
    ({ name, displayName: name, order: 0, protected: false, connected: true, ...patch }) as ClusterInfo;

  it("replaces counts and kinds of the named cluster and clears countsPending", () => {
    const prev = [c("prod", { countsPending: true }), c("dev")];
    const next = applyCounts(prev, { cluster: "prod", counts: { ready: 2 }, kinds: { Pod: { ready: 2 } } });
    expect(next?.[0]).toMatchObject({
      counts: { ready: 2 },
      kinds: { Pod: { ready: 2 } },
      countsPending: false,
    });
    expect(next?.[1]).toBe(prev[1]);
  });

  it("accepts the list form `{items: [{name, …}]}`", () => {
    const next = applyCounts([c("prod")], { items: [{ name: "prod", cluster: "", counts: { failed: 1 } }] });
    expect(next?.[0]?.counts).toEqual({ failed: 1 });
  });

  it("returns the same array for an unknown cluster, and undefined before the first load", () => {
    const prev = [c("prod")];
    expect(applyCounts(prev, { cluster: "gone", counts: {} })).toBe(prev);
    expect(applyCounts(undefined, { cluster: "prod", counts: {} })).toBeUndefined();
  });

  it("keeps fields the event does not carry", () => {
    const prev = [c("prod", { counts: { ready: 1 }, kinds: { Pod: { ready: 1 } } })];
    expect(applyCounts(prev, { cluster: "prod", connected: false })?.[0]).toMatchObject({
      counts: { ready: 1 },
      kinds: { Pod: { ready: 1 } },
      connected: false,
    });
  });
});

describe("applyAttention", () => {
  const at = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString();
  const failed = resource("a", { status: "failed", lastChanged: at(5) });
  const recon = resource("b", { status: "reconciling", lastChanged: at(1) });
  const base = (): AttentionResponse => ({
    items: [{ cluster: "prod", resource: failed }],
    total: 1,
    findings: [],
  });

  it("adds rows that need attention, sorted failed, reconciling, suspended, then newest", () => {
    const newer = resource("c", { status: "failed", lastChanged: at(0) });
    const next = applyAttention(base(), "", { cluster: "dev", upserts: [recon, newer], deletes: [] });
    expect(next?.items.map((i) => `${i.cluster}/${i.resource.name}`)).toEqual(["dev/c", "prod/a", "dev/b"]);
    expect(next?.total).toBe(3);
  });

  it("removes deletes and upserts that are healthy again, by cluster", () => {
    const prev = base();
    expect(applyAttention(prev, "", { cluster: "prod", upserts: [], deletes: [failed.id] })?.items).toEqual(
      [],
    );
    expect(
      applyAttention(prev, "", { cluster: "prod", upserts: [{ ...failed, status: "ready" }], deletes: [] })
        ?.total,
    ).toBe(0);
    // The same id in another cluster is another object.
    expect(applyAttention(prev, "", { cluster: "dev", upserts: [], deletes: [failed.id] })).toBe(prev);
  });

  it("follows the hub's rule: non-Flux reconciling and inventory-only rows do not count", () => {
    const pod = resource("p", { kind: "Pod", group: "", status: "reconciling" });
    const inv = resource("i", { status: "failed", inventoryOnly: true });
    const prev = base();
    expect(applyAttention(prev, "", { cluster: "dev", upserts: [pod, inv], deletes: [] })).toBe(prev);
  });

  it("leaves a cluster-scoped response alone for other clusters' events", () => {
    const prev = base();
    expect(applyAttention(prev, "prod", { cluster: "dev", upserts: [recon], deletes: [] })).toBe(prev);
    expect(applyAttention(prev, "prod", { cluster: "prod", upserts: [recon], deletes: [] })?.total).toBe(2);
  });

  it("does nothing before the first load or for a hub without the endpoint", () => {
    expect(applyAttention(undefined, "", { cluster: "x", upserts: [recon], deletes: [] })).toBeUndefined();
    expect(applyAttention(null, "", { cluster: "x", upserts: [recon], deletes: [] })).toBeNull();
  });
});
