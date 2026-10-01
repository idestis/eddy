// A reconcile requested on one object travels downstream: its dependents and the Flux
// objects it applies reconcile after it. The graph shows that wave: from the request
// until the downstream objects settle, the edges into them march (animated dashes).
//
// A cascade starts when an object in the graph is marked "requested" (lib/liveMotion.ts,
// so the key, the side panel button and the context menu all start one). A downstream
// object is done once it reports a change after the request and is no longer
// reconciling. The cascade ends when the root has its result and the wave has been quiet
// for QUIET_MS, or after MAX_MS.

import { useSyncExternalStore } from "react";
import type { GraphNode } from "../api/types";

export const QUIET_MS = 4_000;
export const MAX_MS = 45_000;

export interface Cascade {
  root: string;
  /** When the request went out (ms). */
  at: number;
  /** Everything downstream of the root when it started. */
  downstream: ReadonlySet<string>;
}

const stores = new Map<string, ReadonlyMap<string, Cascade>>();
const listeners = new Set<() => void>();
const EMPTY: ReadonlyMap<string, Cascade> = new Map();

function notify(): void {
  for (const l of listeners) l();
}

export function startCascade(
  cluster: string,
  root: string,
  downstream: Iterable<string>,
  at = Date.now(),
): void {
  const next = new Map(stores.get(cluster) ?? EMPTY);
  next.set(root, { root, at, downstream: new Set(downstream) });
  stores.set(cluster, next);
  notify();
}

export function endCascade(cluster: string, root: string): void {
  const cur = stores.get(cluster);
  if (!cur?.has(root)) return;
  const next = new Map(cur);
  next.delete(root);
  stores.set(cluster, next);
  notify();
}

export function useCascades(cluster: string): ReadonlyMap<string, Cascade> {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => stores.get(cluster) ?? EMPTY,
    () => EMPTY,
  );
}

export function resetCascades(): void {
  stores.clear();
  notify();
}

const changedAt = (n: GraphNode | undefined): number => (n?.lastChanged ? Date.parse(n.lastChanged) || 0 : 0);

/** A downstream object has shown its result: it changed after the request and is not reconciling. */
export function isDone(c: Cascade, n: GraphNode | undefined): boolean {
  return !n || (changedAt(n) >= c.at - 500 && n.status !== "reconciling");
}

export interface CascadeState {
  /** Downstream objects still expected to change. */
  pending: Set<string>;
  /** The cascade is over and can be dropped. */
  over: boolean;
}

/**
 * Where a cascade stands. `rootPending` is true while the root's own request has not
 * shown a result (liveMotion's requested marker is still on).
 */
export function cascadeState(
  c: Cascade,
  nodes: ReadonlyMap<string, GraphNode>,
  rootPending: boolean,
  now: number,
): CascadeState {
  const pending = new Set<string>();
  let last = c.at;
  const root = nodes.get(c.root);
  last = Math.max(last, changedAt(root));
  for (const id of c.downstream) {
    const n = nodes.get(id);
    last = Math.max(last, changedAt(n));
    if (!isDone(c, n)) pending.add(id);
  }
  const rootBusy = rootPending || root?.status === "reconciling";
  const anyReconciling = [...pending].some((id) => nodes.get(id)?.status === "reconciling");
  const quiet = now - last >= QUIET_MS;
  const over = now - c.at >= MAX_MS || (!rootBusy && (pending.size === 0 || (quiet && !anyReconciling)));
  return { pending, over };
}
