import { isFlux } from "../lib/kinds";
import type {
  AttentionEvent,
  AttentionItem,
  AttentionResponse,
  ChangeEvent,
  ClusterInfo,
  CountsEvent,
  Finding,
  Resource,
  ResourceSnapshot,
  Status,
} from "./types";

/**
 * Applies an SSE `change` event to a cached snapshot. Upserts replace an
 * item with the same id in place (keeping list order stable) or are
 * appended; deletes remove by id. An upsert older than the cached item
 * (by numeric resourceVersion) is ignored, so a late delta cannot undo a
 * newer one. Returns the same object when nothing changed, and undefined
 * when there is no snapshot yet: the initial fetch will include the change.
 */
export function applyChange(
  prev: ResourceSnapshot | undefined,
  change: Pick<ChangeEvent, "upserts" | "deletes">,
): ResourceSnapshot | undefined {
  if (!prev) return prev;
  if (change.upserts.length === 0 && change.deletes.length === 0) return prev;

  const upserts = new Map(change.upserts.map((r) => [r.id, r]));
  const deletes = new Set(change.deletes);
  let changed = false;
  const items = [];

  for (const item of prev.items) {
    if (deletes.has(item.id)) {
      changed = true;
      continue;
    }
    const next = upserts.get(item.id);
    if (next) {
      upserts.delete(item.id);
      if (!isOlder(next.resourceVersion, item.resourceVersion)) {
        items.push(next);
        changed = true;
        continue;
      }
    }
    items.push(item);
  }
  for (const r of upserts.values()) {
    if (deletes.has(r.id)) continue;
    items.push(r);
    changed = true;
  }
  return changed ? { ...prev, items } : prev;
}

/** Kubernetes resourceVersions are opaque; compare only when both are integers. */
function isOlder(a: string, b: string): boolean {
  if (!/^\d+$/.test(a) || !/^\d+$/.test(b)) return false;
  return BigInt(a) < BigInt(b);
}

/**
 * Applies an SSE `counts` event (a cluster outside the watch set) to the cluster list.
 * Accepts `{cluster, counts, kinds}` and, as ADR-0006 first sketched it, `{items: [{name, …}]}`.
 * Returns the same array when nothing changed.
 */
export function applyCounts(
  prev: ClusterInfo[] | undefined,
  ev: CountsEvent | { items?: Array<CountsEvent & { name?: string }> },
): ClusterInfo[] | undefined {
  if (!prev) return prev;
  const updates: CountsEvent[] =
    "items" in ev && Array.isArray(ev.items)
      ? ev.items.map((i) => ({ ...i, cluster: i.cluster || i.name || "" }))
      : "cluster" in ev
        ? [ev]
        : [];
  if (!updates.length) return prev;
  const byName = new Map(updates.map((u) => [u.cluster, u]));
  let changed = false;
  const next = prev.map((c) => {
    const u = byName.get(c.name);
    if (!u) return c;
    changed = true;
    return {
      ...c,
      ...(u.counts !== undefined && { counts: u.counts, countsPending: false }),
      ...(u.kinds !== undefined && { kinds: u.kinds }),
      ...(u.connected !== undefined && { connected: u.connected }),
    };
  });
  return changed ? next : prev;
}

const ATTENTION_ORDER: Partial<Record<Status, number>> = { failed: 0, reconciling: 1, suspended: 2 };

/** The hub's attention rule (docs/api.md): failed, or a Flux object that is reconciling or suspended. */
export const isAttentionRow = (r: Resource): boolean =>
  !r.inventoryOnly &&
  (r.status === "failed" || (isFlux(r.kind) && (r.status === "reconciling" || r.status === "suspended")));

const changedAt = (r: Resource) => Date.parse(r.lastChanged ?? r.createdAt ?? "") || 0;

/** Failed, reconciling, suspended, then most recently changed, as the hub sorts them. */
export const compareAttention = (a: AttentionItem, b: AttentionItem): number =>
  (ATTENTION_ORDER[a.resource.status] ?? 9) - (ATTENTION_ORDER[b.resource.status] ?? 9) ||
  changedAt(b.resource) - changedAt(a.resource);

/**
 * Applies an SSE `attention` event, or the attention part of a `change` event, to a cached
 * GET /attention response. `scope` is the query's cluster ("" for the fleet); events of other
 * clusters leave it alone. An upsert that no longer needs attention removes the row.
 */
export function applyAttention(
  prev: AttentionResponse | null | undefined,
  scope: string,
  ev: Pick<AttentionEvent, "cluster" | "upserts" | "deletes">,
): AttentionResponse | null | undefined {
  if (!prev) return prev;
  if (scope && scope !== ev.cluster) return prev;
  if (!ev.upserts.length && !ev.deletes.length) return prev;
  const gone = new Set([...ev.deletes, ...ev.upserts.map((r) => r.id)]);
  const kept = prev.items.filter((i) => i.cluster !== ev.cluster || !gone.has(i.resource.id));
  const added = ev.upserts.filter(isAttentionRow).map((resource) => ({ cluster: ev.cluster, resource }));
  const removed = prev.items.length - kept.length;
  if (!removed && !added.length) return prev;
  const items = [...kept, ...added].sort(compareAttention);
  return { ...prev, items, total: Math.max(0, prev.total - removed + added.length) };
}

/**
 * An `attention` event with `findings` replaces that cluster's findings: all severities in
 * `ClusterInfo.findings`, the warnings in cached GET /attention responses.
 */
export function applyClusterFindings(
  prev: ClusterInfo[] | undefined,
  cluster: string,
  findings: Finding[],
): ClusterInfo[] | undefined {
  if (!prev?.some((c) => c.name === cluster)) return prev;
  return prev.map((c) => (c.name === cluster ? { ...c, findings } : c));
}

export function applyAttentionFindings(
  prev: AttentionResponse | null | undefined,
  scope: string,
  cluster: string,
  findings: Finding[],
): AttentionResponse | null | undefined {
  if (!prev || (scope && scope !== cluster)) return prev;
  const warnings = findings.filter((f) => f.severity === "warning").map((finding) => ({ cluster, finding }));
  const others = prev.findings.filter((f) => f.cluster !== cluster);
  if (!warnings.length && others.length === prev.findings.length) return prev;
  return { ...prev, findings: [...others, ...warnings] };
}

/**
 * Puts a fresh GET /attention?cluster= answer for one cluster into the fleet's cached answer,
 * for a `resync` of an unwatched cluster: the rest of the fleet is not refetched.
 */
export function spliceClusterAttention(
  prev: AttentionResponse | null | undefined,
  cluster: string,
  fresh: AttentionResponse,
): AttentionResponse | null | undefined {
  if (!prev) return prev;
  const kept = prev.items.filter((i) => i.cluster !== cluster);
  const removed = prev.items.length - kept.length;
  return {
    ...prev,
    items: [...kept, ...fresh.items].sort(compareAttention),
    total: Math.max(0, prev.total - removed + fresh.total),
    findings: [...prev.findings.filter((f) => f.cluster !== cluster), ...fresh.findings],
  };
}
