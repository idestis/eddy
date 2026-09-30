import { describe, expect, it } from "vitest";
import type { ClusterInfo } from "../api/types";
import {
  type ClusterPrefs,
  EMPTY_PREFS,
  fitPrefs,
  HALF_LIFE_MS,
  MAX_VISITS,
  mergeClusterPrefs,
  orderClusters,
  parseClusterPrefs,
  recordVisit,
  score,
  togglePin,
  VISIT_DEBOUNCE_MS,
} from "./frecency";

const DAY = 24 * 60 * 60 * 1000;
const NOW = 1_800_000_000_000;
const cluster = (name: string, order: number): ClusterInfo => ({
  name,
  displayName: name,
  protected: false,
  order,
  connected: true,
});
const fleet = [cluster("prod", 1), cluster("staging", 2), cluster("dev", 3), cluster("edge", 4)];
const names = (l: ClusterInfo[]) => l.map((c) => c.name);

describe("score", () => {
  it("halves every 7 days", () => {
    expect(score([NOW], NOW)).toBeCloseTo(1);
    expect(score([NOW - HALF_LIFE_MS], NOW)).toBeCloseTo(0.5);
    expect(score([NOW - 2 * HALF_LIFE_MS], NOW)).toBeCloseTo(0.25);
  });
  it("sums visits and treats none as zero", () => {
    expect(score([NOW, NOW], NOW)).toBeCloseTo(2);
    expect(score(undefined, NOW)).toBe(0);
  });
});

describe("recordVisit", () => {
  it("counts one visit per cluster per 30 minutes", () => {
    let p = recordVisit(EMPTY_PREFS, "prod", NOW);
    expect(p.visits.prod).toEqual([NOW]);
    expect(recordVisit(p, "prod", NOW + VISIT_DEBOUNCE_MS - 1)).toBe(p);
    p = recordVisit(p, "prod", NOW + VISIT_DEBOUNCE_MS);
    expect(p.visits.prod).toHaveLength(2);
    expect(recordVisit(p, "dev", NOW + 1).visits.dev).toEqual([NOW + 1]);
  });
  it("keeps the newest 50 timestamps", () => {
    let p: ClusterPrefs = EMPTY_PREFS;
    for (let i = 0; i < 60; i++) p = recordVisit(p, "prod", NOW + i * VISIT_DEBOUNCE_MS);
    const v = p.visits.prod ?? [];
    expect(v).toHaveLength(MAX_VISITS);
    expect(v[v.length - 1]).toBe(NOW + 59 * VISIT_DEBOUNCE_MS);
    expect(v[0]).toBe(NOW + 10 * VISIT_DEBOUNCE_MS);
  });
});

describe("orderClusters", () => {
  it("falls back to the CRD order, then name, for a new user", () => {
    expect(names(orderClusters(fleet, EMPTY_PREFS, NOW))).toEqual(["prod", "staging", "dev", "edge"]);
    const tied = [cluster("b", 1), cluster("a", 1)];
    expect(names(orderClusters(tied, EMPTY_PREFS, NOW))).toEqual(["a", "b"]);
  });
  it("ranks frequent and recent visits first", () => {
    const p: ClusterPrefs = {
      pins: [],
      visits: {
        dev: [NOW - 1000, NOW - 2000, NOW - 3000],
        edge: [NOW - 30 * DAY, NOW - 31 * DAY],
        staging: [NOW - 2 * DAY],
      },
    };
    expect(names(orderClusters(fleet, p, NOW))).toEqual(["dev", "staging", "edge", "prod"]);
  });
  it("puts a recent visit above an older pile of visits", () => {
    const old = Array.from({ length: 5 }, (_, i) => NOW - 60 * DAY - i);
    const p: ClusterPrefs = { pins: [], visits: { prod: old, dev: [NOW - DAY] } };
    expect(names(orderClusters(fleet, p, NOW))[0]).toBe("dev");
  });
  it("puts pins first in pin order regardless of score, ignoring unknown pins", () => {
    const p: ClusterPrefs = { pins: ["edge", "gone", "staging"], visits: { dev: [NOW], prod: [NOW] } };
    expect(names(orderClusters(fleet, p, NOW))).toEqual(["edge", "staging", "prod", "dev"]);
  });
  it("does not mutate its input", () => {
    const copy = [...fleet];
    orderClusters(fleet, { pins: ["edge"], visits: {} }, NOW);
    expect(fleet).toEqual(copy);
  });
});

describe("togglePin", () => {
  it("appends and removes pins and stamps the change", () => {
    let p = togglePin(EMPTY_PREFS, "dev", NOW);
    p = togglePin(p, "prod", NOW + 1);
    expect(p.pins).toEqual(["dev", "prod"]);
    p = togglePin(p, "dev", NOW + 2);
    expect(p).toMatchObject({ pins: ["prod"], pinsAt: NOW + 2 });
  });
});

describe("mergeClusterPrefs", () => {
  it("unions and dedupes visits, keeping the newest 50", () => {
    const local: ClusterPrefs = { pins: [], visits: { prod: [1, 2, 3] } };
    const remote: ClusterPrefs = { pins: [], visits: { prod: [3, 4], dev: [9] } };
    expect(mergeClusterPrefs(local, remote).visits).toEqual({ prod: [1, 2, 3, 4], dev: [9] });
    const many = (from: number) => Array.from({ length: 40 }, (_, i) => from + i);
    const big = mergeClusterPrefs(
      { pins: [], visits: { a: many(0) } },
      { pins: [], visits: { a: many(100) } },
    );
    expect(big.visits.a).toHaveLength(MAX_VISITS);
    expect(big.visits.a?.at(-1)).toBe(139);
  });
  it("takes the pins that changed last, and the hub's when neither is stamped", () => {
    const a: ClusterPrefs = { pins: ["a"], visits: {}, pinsAt: 5 };
    const b: ClusterPrefs = { pins: ["b"], visits: {}, pinsAt: 9 };
    expect(mergeClusterPrefs(a, b).pins).toEqual(["b"]);
    expect(mergeClusterPrefs(b, a).pins).toEqual(["b"]);
    expect(mergeClusterPrefs({ pins: ["x"], visits: {} }, { pins: ["y"], visits: {} }).pins).toEqual(["y"]);
  });
  it("lets an unpin on this device beat older remote pins", () => {
    const local: ClusterPrefs = { pins: [], visits: {}, pinsAt: 10 };
    const remote: ClusterPrefs = { pins: ["dev"], visits: {}, pinsAt: 4 };
    expect(mergeClusterPrefs(local, remote).pins).toEqual([]);
  });
});

describe("parseClusterPrefs", () => {
  it("survives garbage", () => {
    expect(parseClusterPrefs(null)).toEqual(EMPTY_PREFS);
    expect(parseClusterPrefs("x")).toEqual(EMPTY_PREFS);
    expect(parseClusterPrefs({ pins: [1, "a", "a"], visits: { x: "no", y: [3, "z", 1] } })).toEqual({
      pins: ["a"],
      visits: { y: [1, 3] },
      pinsAt: undefined,
    });
  });
});

describe("fitPrefs", () => {
  it("trims old visits until the prefs fit", () => {
    const visits = Object.fromEntries(
      Array.from({ length: 40 }, (_, c) => [`cluster-${c}`, Array.from({ length: 50 }, (_, i) => NOW + i)]),
    );
    const fitted = fitPrefs({ pins: ["cluster-1"], visits });
    expect(JSON.stringify(fitted).length).toBeLessThanOrEqual(12 * 1024);
    expect(fitted.pins).toEqual(["cluster-1"]);
    expect(fitted.visits["cluster-0"]?.at(-1)).toBe(NOW + 49);
  });
});
