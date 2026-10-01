// Filtering, sorting and grouping for the resource list. Pure functions so
// they can be tested and memoised; the list renders whatever rows they return.

import type { ClusterInfo, Resource, Status } from "../api/types";
import { STATUS_RANK } from "./format";
import { kindInfo, matchesKindFilter } from "./kinds";
import { type ListSort, sortResources } from "./listSort";

/** "attention" means anything that is not ready (or completed). */
export type StatusFilter = Status | "attention";

export interface ListFilter {
  text?: string;
  kind?: string;
  status?: StatusFilter;
  namespace?: string;
}

export type Row =
  | { type: "group"; key: string; kind: string; project?: string; count: number; failing: number }
  | { type: "resource"; key: string; resource: Resource };

/** A list row, or a row of a windowed list whose page has not loaded yet. */
export type ListRow = Row | { type: "placeholder"; key: string };

/** Healthy statuses: ready, or a finished Job or Pod that completed. */
export const isHealthy = (status: Status): boolean => status === "ready" || status === "completed";

/** Needs attention: anything watched that is not healthy. Inventory-only rows have no status. */
export const needsAttention = (r: Resource): boolean => !isHealthy(r.status) && !r.inventoryOnly;

export function matchesStatus(r: Resource, status: StatusFilter | undefined): boolean {
  if (!status) return true;
  if (status === "attention") return needsAttention(r);
  return r.status === status && !r.inventoryOnly;
}

export interface StatusCounts {
  attention: number;
  failed: number;
  reconciling: number;
  suspended: number;
  completed: number;
}

/**
 * The list's total and status chips from `ClusterInfo` alone (ADR-0006: counts render before
 * any row). `kind` narrows to a nav filter through `ClusterInfo.kinds`. Undefined when the
 * hub sends no counts yet, or with a namespace filter (counts are not per namespace).
 */
export function summaryCounts(
  cluster: ClusterInfo | undefined,
  filter: { kind?: string; namespace?: string },
): { total: number; counts: StatusCounts } | undefined {
  if (!cluster || filter.namespace || cluster.countsPending) return undefined;
  let byStatus: Partial<Record<Status, number>> | undefined;
  if (!filter.kind) byStatus = cluster.counts;
  else if (cluster.kinds) {
    byStatus = {};
    for (const [kind, statuses] of Object.entries(cluster.kinds)) {
      if (!matchesKindFilter(kind, filter.kind)) continue;
      for (const [st, n] of Object.entries(statuses) as Array<[Status, number | undefined]>)
        byStatus[st] = (byStatus[st] ?? 0) + (n ?? 0);
    }
  }
  if (!byStatus) return undefined;
  const counts: StatusCounts = { attention: 0, failed: 0, reconciling: 0, suspended: 0, completed: 0 };
  let total = 0;
  for (const [st, n = 0] of Object.entries(byStatus) as Array<[Status, number | undefined]>) {
    total += n;
    if (!isHealthy(st)) counts.attention += n;
    if (st !== "ready" && st !== "unknown") counts[st] += n;
  }
  return { total, counts };
}

export function statusCounts(items: readonly Resource[]): StatusCounts {
  const counts: StatusCounts = { attention: 0, failed: 0, reconciling: 0, suspended: 0, completed: 0 };
  for (const r of items) {
    if (r.inventoryOnly) continue;
    if (needsAttention(r)) counts.attention++;
    if (r.status !== "ready" && r.status !== "unknown") counts[r.status]++;
  }
  return counts;
}

/** Case-insensitive AND of whitespace-separated terms over name, kind, namespace and message. */
export function matchesText(r: Resource, text: string | undefined): boolean {
  if (!text) return true;
  const haystack =
    `${r.name} ${r.kind} ${r.namespace} ${r.message ?? ""} ${r.revision ?? ""} ${r.chart ?? ""} ${r.images?.join(" ") ?? ""} ${r.hosts?.join(" ") ?? ""}`.toLowerCase();
  return text
    .toLowerCase()
    .split(/\s+/)
    .every((term) => !term || haystack.includes(term));
}

export function filterResources(items: readonly Resource[], f: ListFilter): Resource[] {
  return items.filter(
    (r) =>
      matchesKindFilter(r.kind, f.kind, r.group) &&
      (!f.namespace || r.namespace === f.namespace) &&
      matchesStatus(r, f.status) &&
      matchesText(r, f.text),
  );
}

function compare(a: Resource, b: Resource): number {
  return (
    kindInfo(a.kind).order - kindInfo(b.kind).order ||
    a.kind.localeCompare(b.kind) ||
    a.group.localeCompare(b.group) ||
    Number(Boolean(a.inventoryOnly)) - Number(Boolean(b.inventoryOnly)) ||
    STATUS_RANK[a.status] - STATUS_RANK[b.status] ||
    a.namespace.localeCompare(b.namespace) ||
    a.name.localeCompare(b.name)
  );
}

// Inventory-only rows sort after everything with a status.
const rankOf = (r: Resource) => (r.inventoryOnly ? 9 : STATUS_RANK[r.status]);

/**
 * Sorts by kind, then what needs attention, then namespace/name; optionally adds kind headers.
 * A flat list takes the user's column sort instead; a grouped list ignores it.
 */
export function buildRows(items: readonly Resource[], grouped: boolean, sort?: ListSort): Row[] {
  const sorted =
    !grouped && sort
      ? sortResources(items, sort)
      : [...items].sort(grouped ? compare : (a, b) => rankOf(a) - rankOf(b) || compare(a, b));
  if (!grouped) return sorted.map((r) => ({ type: "resource", key: r.id, resource: r }));
  const rows: Row[] = [];
  let header: Extract<Row, { type: "group" }> | undefined;
  for (const r of sorted) {
    const key = `group:${r.group}/${r.kind}`;
    if (header?.key !== key) {
      header = { type: "group", key, kind: r.kind, project: r.project, count: 0, failing: 0 };
      rows.push(header);
    }
    header.count++;
    if (r.status === "failed" && !r.inventoryOnly) header.failing++;
    rows.push({ type: "resource", key: r.id, resource: r });
  }
  return rows;
}
