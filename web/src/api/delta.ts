import type { ChangeEvent, ResourceSnapshot } from "./types";

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
