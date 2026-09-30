import { describe, expect, it } from "vitest";
import { resource } from "../test/fixtures";
import { applyChange } from "./delta";
import type { ResourceSnapshot } from "./types";

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
