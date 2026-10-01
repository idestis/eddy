// What just changed, per cluster, so the list and the detail header can show it:
// status transitions (the row flashes in the new status colour), rows that appeared
// (they slide in), rows that were deleted (they stay for a short exit before they go)
// and actions the user requested that have not shown a result yet.
//
// Only SSE `change` deltas feed it (api/stream.tsx). A first load or a resync refetch
// is not a delta, so it never animates; a delta that touches many rows at once is
// treated like a resync and applied without per-row motion. Under reduced motion
// nothing is recorded, so every change lands instantly.

import { useSyncExternalStore } from "react";
import type { Action } from "../api/endpoints";
import type { ChangeEvent, Resource, Status } from "../api/types";
import { DURATION, prefersReducedMotion } from "./motion";

/** More changed rows than this in one delta: no per-row motion, just the update. */
export const BULK_LIMIT = 20;

export type RowChange =
  | { kind: "status"; from: Status; to: Status; at: number }
  | { kind: "message"; at: number };

export interface Leaving {
  resource: Resource;
  at: number;
}

export interface Detected {
  changed: Map<string, RowChange>;
  entered: Map<string, number>;
  left: Resource[];
  /** True when the delta was too large for per-row motion. */
  bulk: boolean;
}

/**
 * Compares the rows a delta touched before and after it was applied. `prev` undefined
 * means there was no snapshot yet (the first render): nothing is reported.
 */
export function detectChanges(
  prev: readonly Resource[] | undefined,
  next: readonly Resource[],
  change: Pick<ChangeEvent, "upserts" | "deletes">,
  now: number,
  bulkLimit = BULK_LIMIT,
): Detected {
  const out: Detected = { changed: new Map(), entered: new Map(), left: [], bulk: false };
  if (!prev) return out;
  const before = new Map(prev.map((r) => [r.id, r]));
  const after = new Map(next.map((r) => [r.id, r]));
  for (const u of change.upserts) {
    const was = before.get(u.id);
    const is = after.get(u.id);
    if (!is) continue;
    if (!was) out.entered.set(u.id, now);
    else if (was.status !== is.status)
      out.changed.set(u.id, { kind: "status", from: was.status, to: is.status, at: now });
    else if ((was.message ?? "") !== (is.message ?? "")) out.changed.set(u.id, { kind: "message", at: now });
  }
  for (const id of change.deletes) {
    const was = before.get(id);
    if (was && !after.has(id)) out.left.push(was);
  }
  if (out.changed.size + out.entered.size + out.left.length > bulkLimit) {
    return { changed: new Map(), entered: new Map(), left: [], bulk: true };
  }
  return out;
}

/** Adds the rows that are on their way out, unless they came back. Exported for tests. */
export function withLeaving(
  items: readonly Resource[],
  leaving: ReadonlyMap<string, Leaving>,
): readonly Resource[] {
  if (leaving.size === 0) return items;
  const present = new Set(items.map((r) => r.id));
  const extra = [...leaving.values()].filter((l) => !present.has(l.resource.id)).map((l) => l.resource);
  return extra.length ? [...items, ...extra] : items;
}

/** Drops entries whose animation is over. Exported for tests. */
export function pruneMotion(m: ClusterMotion, now: number): ClusterMotion {
  const changed = new Map(
    [...m.changed].filter(([, c]) => now - c.at < (c.kind === "status" ? DURATION.flash : DURATION.slow)),
  );
  const entered = new Map([...m.entered].filter(([, at]) => now - at < DURATION.slow));
  const leaving = new Map([...m.leaving].filter(([, l]) => now - l.at < DURATION.base));
  if (changed.size === m.changed.size && entered.size === m.entered.size && leaving.size === m.leaving.size)
    return m;
  return { changed, entered, leaving, settleUntil: m.settleUntil };
}

export interface ClusterMotion {
  changed: ReadonlyMap<string, RowChange>;
  entered: ReadonlyMap<string, number>;
  leaving: ReadonlyMap<string, Leaving>;
  /** Until this time rows glide to new positions (after inserts, removals and re-sorts). */
  settleUntil: number;
}

export const NO_MOTION: ClusterMotion = {
  changed: new Map(),
  entered: new Map(),
  leaving: new Map(),
  settleUntil: 0,
};

export interface Requested {
  action: Action;
  at: number;
  /** What the row looked like when the request went out. */
  status: Status;
  message: string;
  lastChanged: string;
}

const NO_REQUESTS: ReadonlyMap<string, Requested> = new Map();

// The store: plain module state with useSyncExternalStore, one snapshot object per
// cluster so unrelated clusters do not re-render.
const motions = new Map<string, ClusterMotion>();
const requests = new Map<string, ReadonlyMap<string, Requested>>();
const timeouts = new Map<string, ReturnType<typeof setTimeout>>();
const listeners = new Set<() => void>();
let pruneTimer: ReturnType<typeof setTimeout> | undefined;

function notify(): void {
  for (const l of listeners) l();
}

function schedulePrune(): void {
  clearTimeout(pruneTimer);
  let next = Number.POSITIVE_INFINITY;
  for (const m of motions.values()) {
    for (const c of m.changed.values())
      next = Math.min(next, c.at + (c.kind === "status" ? DURATION.flash : DURATION.slow));
    for (const at of m.entered.values()) next = Math.min(next, at + DURATION.slow);
    for (const l of m.leaving.values()) next = Math.min(next, l.at + DURATION.base);
    if (m.settleUntil > Date.now()) next = Math.min(next, m.settleUntil);
  }
  if (!Number.isFinite(next)) return;
  pruneTimer = setTimeout(
    () => {
      const now = Date.now();
      for (const [cluster, m] of motions) {
        const pruned = pruneMotion(m, now);
        const settled =
          pruned.settleUntil && pruned.settleUntil <= now ? { ...pruned, settleUntil: 0 } : pruned;
        if (settled !== m) motions.set(cluster, settled);
      }
      notify();
      schedulePrune();
    },
    Math.max(16, next - Date.now()),
  );
}

/** Records what a delta changed. Called by the stream after it patched the cache. */
export function recordDelta(cluster: string, d: Detected, now = Date.now()): void {
  if (d.bulk || prefersReducedMotion()) return;
  if (d.changed.size === 0 && d.entered.size === 0 && d.left.length === 0) return;
  const m = motions.get(cluster) ?? NO_MOTION;
  const changed = new Map(m.changed);
  for (const [id, c] of d.changed) changed.set(id, c);
  const entered = new Map(m.entered);
  for (const [id, at] of d.entered) entered.set(id, at);
  const leaving = new Map(m.leaving);
  for (const id of d.entered.keys()) leaving.delete(id);
  for (const r of d.left) leaving.set(r.id, { resource: r, at: now });
  // Removals wait for the exit before the rows below move up, so they settle later.
  const settle = now + (d.left.length ? DURATION.base * 2 + 60 : DURATION.slow);
  motions.set(cluster, { changed, entered, leaving, settleUntil: Math.max(m.settleUntil, settle) });
  notify();
  schedulePrune();
}

export function getMotion(cluster: string): ClusterMotion {
  return motions.get(cluster) ?? NO_MOTION;
}

function subscribe(l: () => void): () => void {
  listeners.add(l);
  return () => listeners.delete(l);
}

/** The live motion of one cluster's rows. */
export function useClusterMotion(cluster: string | undefined): ClusterMotion {
  return useSyncExternalStore(
    subscribe,
    () => (cluster ? getMotion(cluster) : NO_MOTION),
    () => NO_MOTION,
  );
}

// Requested actions: a row shows "requested" from the click until its next status (or
// message) arrives, or until REQUEST_TIMEOUT_MS passes without one.

export const REQUEST_TIMEOUT_MS = 30_000;

const reqKey = (cluster: string, id: string) => `${cluster}\n${id}`;

function setRequests(cluster: string, next: ReadonlyMap<string, Requested>): void {
  requests.set(cluster, next);
  notify();
}

/** Marks a row as waiting for the result of an action. `onTimeout` runs if nothing arrives. */
export function markRequested(
  cluster: string,
  r: Resource,
  action: Action,
  onTimeout?: () => void,
  now = Date.now(),
): void {
  const next = new Map(requests.get(cluster) ?? NO_REQUESTS);
  next.set(r.id, {
    action,
    at: now,
    status: r.status,
    message: r.message ?? "",
    lastChanged: r.lastChanged ?? "",
  });
  setRequests(cluster, next);
  const key = reqKey(cluster, r.id);
  clearTimeout(timeouts.get(key));
  timeouts.set(
    key,
    setTimeout(() => {
      timeouts.delete(key);
      if (!requests.get(cluster)?.has(r.id)) return;
      clearRequested(cluster, r.id);
      onTimeout?.();
    }, REQUEST_TIMEOUT_MS),
  );
}

export function clearRequested(cluster: string, id: string): void {
  const cur = requests.get(cluster);
  if (!cur?.has(id)) return;
  const next = new Map(cur);
  next.delete(id);
  clearTimeout(timeouts.get(reqKey(cluster, id)));
  timeouts.delete(reqKey(cluster, id));
  setRequests(cluster, next);
}

/**
 * True once a row shows the result of a request: its status, message or last change
 * moved. A bare resourceVersion bump (the request annotation itself) does not count.
 */
export function isSettled(req: Requested, r: Resource): boolean {
  return (
    r.status !== req.status || (r.message ?? "") !== req.message || (r.lastChanged ?? "") !== req.lastChanged
  );
}

/** Clears requests for rows that now show a result. Called by the stream with the new rows. */
export function settleRequested(cluster: string, rows: readonly Resource[]): void {
  const cur = requests.get(cluster);
  if (!cur?.size) return;
  for (const r of rows) {
    const req = cur.get(r.id);
    if (req && isSettled(req, r)) clearRequested(cluster, r.id);
  }
}

/** The rows of a cluster waiting for an action's result. */
export function useRequested(cluster: string | undefined): ReadonlyMap<string, Requested> {
  return useSyncExternalStore(
    subscribe,
    () => (cluster ? (requests.get(cluster) ?? NO_REQUESTS) : NO_REQUESTS),
    () => NO_REQUESTS,
  );
}

/** Resets the store (tests). */
export function resetLiveMotion(): void {
  motions.clear();
  requests.clear();
  for (const t of timeouts.values()) clearTimeout(t);
  timeouts.clear();
  clearTimeout(pruneTimer);
  notify();
}
