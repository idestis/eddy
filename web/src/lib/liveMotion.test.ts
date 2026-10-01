import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resource } from "../test/fixtures";
import {
  BULK_LIMIT,
  clearRequested,
  detectChanges,
  getMotion,
  isSettled,
  markRequested,
  NO_MOTION,
  pruneMotion,
  REQUEST_TIMEOUT_MS,
  recordDelta,
  resetLiveMotion,
  settleRequested,
  useRequested,
  withLeaving,
} from "./liveMotion";
import { DURATION } from "./motion";

const job = (
  name: string,
  status: "reconciling" | "completed" | "ready" = "reconciling",
  message = "Running",
) => resource(name, { kind: "Job", group: "batch", namespace: "prefect", status, message });

describe("detectChanges", () => {
  it("reports nothing on the first render (no snapshot yet)", () => {
    const a = job("a");
    const d = detectChanges(undefined, [a], { upserts: [a], deletes: [] }, 1);
    expect(d.changed.size + d.entered.size + d.left.length).toBe(0);
    expect(d.bulk).toBe(false);
  });

  it("sees a status transition, a message change, a new row and a deleted row", () => {
    const a = job("a");
    const b = job("b");
    const c = job("c");
    const prev = [a, b, c];
    const a2 = { ...a, status: "completed" as const, message: "Completed in 2m" };
    const b2 = { ...b, message: "Running, 2 active" };
    const d = job("d");
    const next = [a2, b2, d];
    const out = detectChanges(prev, next, { upserts: [a2, b2, d], deletes: [c.id] }, 5);
    expect(out.changed.get(a.id)).toEqual({ kind: "status", from: "reconciling", to: "completed", at: 5 });
    expect(out.changed.get(b.id)).toEqual({ kind: "message", at: 5 });
    expect(out.entered.get(d.id)).toBe(5);
    expect(out.left).toEqual([c]);
  });

  it("ignores upserts that changed nothing visible", () => {
    const a = job("a");
    const a2 = { ...a, resourceVersion: "2" };
    const out = detectChanges([a], [a2], { upserts: [a2], deletes: [] }, 1);
    expect(out.changed.size).toBe(0);
  });

  it("suppresses per-row motion for a bulk delta, like a resync", () => {
    const prev = Array.from({ length: BULK_LIMIT + 1 }, (_, i) => job(`j${i}`));
    const next = prev.map((r) => ({ ...r, status: "completed" as const }));
    const out = detectChanges(prev, next, { upserts: next, deletes: [] }, 1);
    expect(out.bulk).toBe(true);
    expect(out.changed.size).toBe(0);
  });
});

describe("withLeaving", () => {
  it("keeps deleted rows for their exit, unless they came back", () => {
    const a = job("a");
    const gone = job("gone");
    const back = job("back");
    const leaving = new Map([
      [gone.id, { resource: gone, at: 0 }],
      [back.id, { resource: back, at: 0 }],
    ]);
    expect(withLeaving([a, back], leaving).map((r) => r.name)).toEqual(["a", "back", "gone"]);
    const items = [a];
    expect(withLeaving(items, new Map())).toBe(items);
  });
});

describe("pruneMotion", () => {
  it("drops entries once their animation is over", () => {
    const m = {
      changed: new Map([
        ["s", { kind: "status" as const, from: "reconciling" as const, to: "ready" as const, at: 0 }],
        ["m", { kind: "message" as const, at: 0 }],
      ]),
      entered: new Map([["e", 0]]),
      leaving: new Map([["l", { resource: job("l"), at: 0 }]]),
      settleUntil: 0,
    };
    const mid = pruneMotion(m, DURATION.base + 1);
    expect([...mid.changed.keys()]).toEqual(["s", "m"]);
    expect(mid.leaving.size).toBe(0);
    const late = pruneMotion(m, DURATION.flash + 1);
    expect(late.changed.size + late.entered.size).toBe(0);
    expect(pruneMotion(NO_MOTION, 10)).toBe(NO_MOTION);
  });
});

describe("the store", () => {
  beforeEach(() => {
    resetLiveMotion();
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
    resetLiveMotion();
    vi.unstubAllGlobals();
  });

  it("records a delta and forgets it after the flash", () => {
    const a = job("a");
    const a2 = { ...a, status: "completed" as const };
    recordDelta("prod", detectChanges([a], [a2], { upserts: [a2], deletes: [] }, Date.now()));
    expect(getMotion("prod").changed.get(a.id)?.kind).toBe("status");
    expect(getMotion("prod").settleUntil).toBeGreaterThan(Date.now());
    vi.advanceTimersByTime(DURATION.flash + 50);
    expect(getMotion("prod").changed.size).toBe(0);
  });

  it("records nothing under reduced motion", () => {
    vi.stubGlobal("matchMedia", (q: string) => ({
      matches: q.includes("reduce"),
      addEventListener() {},
      removeEventListener() {},
    }));
    const a = job("a");
    const a2 = { ...a, status: "completed" as const };
    recordDelta("prod", detectChanges([a], [a2], { upserts: [a2], deletes: [] }, Date.now()));
    expect(getMotion("prod")).toBe(NO_MOTION);
  });

  it("marks a request until the row shows a result", () => {
    const a = job("a", "ready", "Applied revision main@1");
    markRequested("prod", a, "reconcile");
    expect(requestedNow("prod").has(a.id)).toBe(true);
    // The request annotation alone (a new resourceVersion) is not a result.
    settleRequested("prod", [{ ...a, resourceVersion: "9" }]);
    expect(requestedNow("prod").has(a.id)).toBe(true);
    settleRequested("prod", [{ ...a, status: "reconciling" }]);
    expect(requestedNow("prod").has(a.id)).toBe(false);
  });

  it("gives up after 30 s and says so", () => {
    const a = job("a", "ready");
    const onTimeout = vi.fn();
    markRequested("prod", a, "reconcile", onTimeout);
    vi.advanceTimersByTime(REQUEST_TIMEOUT_MS - 1);
    expect(onTimeout).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(onTimeout).toHaveBeenCalledOnce();
    expect(requestedNow("prod").has(a.id)).toBe(false);
  });

  it("does not time out a request that was cleared", () => {
    const a = job("a", "ready");
    const onTimeout = vi.fn();
    markRequested("prod", a, "suspend", onTimeout);
    clearRequested("prod", a.id);
    vi.advanceTimersByTime(REQUEST_TIMEOUT_MS);
    expect(onTimeout).not.toHaveBeenCalled();
  });

  it("isSettled looks at status, message and last change", () => {
    const req = {
      action: "reconcile" as const,
      at: 0,
      status: "ready" as const,
      message: "m",
      lastChanged: "t1",
    };
    expect(isSettled(req, job("a", "ready", "m"))).toBe(true); // lastChanged "" differs from "t1"
    expect(isSettled(req, { ...job("a", "ready", "m"), lastChanged: "t1" })).toBe(false);
    expect(isSettled(req, { ...job("a", "ready", "m2"), lastChanged: "t1" })).toBe(true);
  });
});

/** The requested rows of a cluster right now, read through the hook. */
function requestedNow(cluster: string) {
  return renderHook(() => useRequested(cluster)).result.current;
}
