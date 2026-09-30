// Automatic cluster ordering: pinned clusters first, then by "frecency" (how often and how
// recently the user visited a cluster). Pure functions; lib/prefs.tsx stores the result.

import type { ClusterInfo } from "../api/types";

export interface ClusterPrefs {
  /** Pinned cluster names, in pin order. */
  pins: string[];
  /** Visit timestamps (ms since the epoch) per cluster, oldest first. */
  visits: Record<string, number[]>;
  /** When `pins` last changed, so the newer side wins when two devices are merged. */
  pinsAt?: number;
}

export const EMPTY_PREFS: ClusterPrefs = { pins: [], visits: {} };

export const HALF_LIFE_MS = 7 * 24 * 60 * 60 * 1000;
/** One visit counts per cluster per window. */
export const VISIT_DEBOUNCE_MS = 30 * 60 * 1000;
export const MAX_VISITS = 50;
/** The hub stores 16 KiB of preferences; stay well under it. */
export const MAX_PREFS_BYTES = 12 * 1024;

/** Sum of exponentially decaying visit weights: a visit now is 1, a week ago 0.5. */
export function score(visits: readonly number[] | undefined, now: number): number {
  let total = 0;
  for (const t of visits ?? []) total += 2 ** (-Math.max(0, now - t) / HALF_LIFE_MS);
  return total;
}

/** Records a visit unless the cluster was already counted in the last 30 minutes. */
export function recordVisit(prefs: ClusterPrefs, cluster: string, now: number): ClusterPrefs {
  const seen = prefs.visits[cluster] ?? [];
  const last = seen[seen.length - 1];
  if (last !== undefined && now - last < VISIT_DEBOUNCE_MS) return prefs;
  return { ...prefs, visits: { ...prefs.visits, [cluster]: [...seen, now].slice(-MAX_VISITS) } };
}

export function togglePin(prefs: ClusterPrefs, cluster: string, now: number): ClusterPrefs {
  const pins = prefs.pins.includes(cluster)
    ? prefs.pins.filter((p) => p !== cluster)
    : [...prefs.pins, cluster];
  return { ...prefs, pins, pinsAt: now };
}

/** Pinned clusters in pin order, then by score; ties and unvisited clusters fall back to `order`, then name. */
export function orderClusters(
  clusters: readonly ClusterInfo[],
  prefs: ClusterPrefs,
  now: number,
): ClusterInfo[] {
  const pinIndex = new Map(prefs.pins.map((p, i) => [p, i]));
  const scores = new Map(clusters.map((c) => [c.name, score(prefs.visits[c.name], now)]));
  return [...clusters].sort((a, b) => {
    const pa = pinIndex.get(a.name);
    const pb = pinIndex.get(b.name);
    if (pa !== undefined || pb !== undefined) {
      if (pa === undefined) return 1;
      if (pb === undefined) return -1;
      return pa - pb;
    }
    return (
      (scores.get(b.name) ?? 0) - (scores.get(a.name) ?? 0) ||
      a.order - b.order ||
      a.name.localeCompare(b.name)
    );
  });
}

/** Reads untrusted JSON (localStorage, the hub) into a valid ClusterPrefs. */
export function parseClusterPrefs(raw: unknown): ClusterPrefs {
  if (!raw || typeof raw !== "object") return EMPTY_PREFS;
  const r = raw as Record<string, unknown>;
  const pins = Array.isArray(r.pins)
    ? [...new Set(r.pins.filter((p): p is string => typeof p === "string"))]
    : [];
  const visits: Record<string, number[]> = {};
  if (r.visits && typeof r.visits === "object") {
    for (const [name, list] of Object.entries(r.visits)) {
      if (!Array.isArray(list)) continue;
      const times = list.filter((t): t is number => typeof t === "number" && Number.isFinite(t));
      if (times.length) visits[name] = times.sort((a, b) => a - b).slice(-MAX_VISITS);
    }
  }
  return { pins, visits, pinsAt: typeof r.pinsAt === "number" ? r.pinsAt : undefined };
}

/**
 * Merges this browser's prefs with the hub's: visits are unioned (deduplicated, newest 50),
 * and the side whose pins changed last wins the pins. Without timestamps the hub wins.
 */
export function mergeClusterPrefs(local: ClusterPrefs, remote: ClusterPrefs): ClusterPrefs {
  const visits: Record<string, number[]> = {};
  for (const name of new Set([...Object.keys(local.visits), ...Object.keys(remote.visits)])) {
    const all = [...new Set([...(remote.visits[name] ?? []), ...(local.visits[name] ?? [])])];
    visits[name] = all.sort((a, b) => a - b).slice(-MAX_VISITS);
  }
  const localWins = (local.pinsAt ?? 0) > (remote.pinsAt ?? 0);
  const winner = localWins ? local : remote;
  return { pins: winner.pins, visits, pinsAt: Math.max(local.pinsAt ?? 0, remote.pinsAt ?? 0) || undefined };
}

/** Drops the oldest visits until the serialised prefs fit, so a big fleet never hits the hub's limit. */
export function fitPrefs(prefs: ClusterPrefs, maxBytes = MAX_PREFS_BYTES): ClusterPrefs {
  let cap = MAX_VISITS;
  let out = prefs;
  while (JSON.stringify(out).length > maxBytes && cap > 1) {
    cap = Math.max(1, Math.floor(cap * 0.75));
    out = {
      ...prefs,
      visits: Object.fromEntries(Object.entries(prefs.visits).map(([k, v]) => [k, v.slice(-cap)])),
    };
  }
  return out;
}
