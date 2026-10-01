// Sorting for the flat resource list: one column at a time, chosen by clicking its header.
// Click once for ascending, again for descending, a third time to return to the default
// order (what needs attention first). The grouped view is always ordered by kind.

import type { IndexQuery } from "../api/endpoints";
import type { Resource } from "../api/types";
import { revisionOf, STATUS_RANK } from "./format";
import { kindInfo } from "./kinds";

export type SortKey = "name" | "kind" | "status" | "ready" | "message" | "version" | "age";
export type SortOrder = "asc" | "desc";

export interface ListSort {
  key: SortKey;
  order: SortOrder;
}

export const SORT_KEYS: readonly SortKey[] = ["name", "kind", "status", "ready", "message", "version", "age"];

export const isSortKey = (v: unknown): v is SortKey => SORT_KEYS.includes(v as SortKey);
export const isSortOrder = (v: unknown): v is SortOrder => v === "asc" || v === "desc";

/** The next click on `key`: ascending, then descending, then the default order (undefined). */
export function nextSort(current: ListSort | undefined, key: SortKey): ListSort | undefined {
  if (current?.key !== key) return { key, order: "asc" };
  return current.order === "asc" ? { key, order: "desc" } : undefined;
}

/** The URL (`?sort=&order=`) wins; otherwise the saved sort of this page. */
export function resolveSort(
  url: { sort?: SortKey; order?: SortOrder },
  saved: ListSort | undefined,
): ListSort | undefined {
  return url.sort ? { key: url.sort, order: url.order ?? "asc" } : saved;
}

/** The page whose sort is saved: the kind filter, or "" for the whole cluster. */
export const sortPageKey = (kind: string | undefined): string => kind ?? "";

/** Saved form, "name:asc"; anything else is dropped. */
export const encodeSort = (s: ListSort): string => `${s.key}:${s.order}`;

export function decodeSort(v: unknown): ListSort | undefined {
  if (typeof v !== "string") return undefined;
  const [key, order, ...rest] = v.split(":");
  return rest.length === 0 && isSortKey(key) && isSortOrder(order) ? { key, order } : undefined;
}

// Inventory-only rows have no status of their own and sort after everything that has one.
const statusRank = (r: Resource): number => (r.inventoryOnly ? 9 : STATUS_RANK[r.status]);

const text = (a: string, b: string): number => a.localeCompare(b, undefined, { numeric: true });

/** "2/3" as [ready, total]; undefined when the row has no such value. */
export function readyRatio(r: Resource): [number, number] | undefined {
  const m = (r.replicas ?? r.completions)?.match(/^(\d+)\/(\d+)$/);
  return m ? [Number(m[1]), Number(m[2])] : undefined;
}

/** Ready ratio first (0/0 counts as full), then the total. */
function compareReady(a: [number, number], b: [number, number]): number {
  const ra = a[1] ? a[0] / a[1] : 1;
  const rb = b[1] ? b[0] / b[1] : 1;
  return ra - rb || a[1] - b[1];
}

/**
 * Orders `a` against `b` for one column, ascending. Undefined when either row has no value in
 * the column: those rows go last in both directions, so they never lead the list.
 */
function compareColumn(key: SortKey, a: Resource, b: Resource): number | undefined {
  switch (key) {
    case "name":
      return text(a.name, b.name);
    case "kind":
      return text(a.kind, b.kind) || kindInfo(a.kind).order - kindInfo(b.kind).order;
    case "status":
      return statusRank(a) - statusRank(b);
    case "ready": {
      const ra = readyRatio(a);
      const rb = readyRatio(b);
      return ra && rb ? compareReady(ra, rb) : undefined;
    }
    case "message": {
      const ma = a.inventoryOnly ? "" : (a.message ?? "");
      const mb = b.inventoryOnly ? "" : (b.message ?? "");
      return ma && mb ? text(ma, mb) : undefined;
    }
    case "version": {
      const va = revisionOf(a);
      const vb = revisionOf(b);
      return va && vb ? text(va, vb) : undefined;
    }
    case "age": {
      // Ascending age is the youngest first, as the Age column reads.
      const ta = a.createdAt ? Date.parse(a.createdAt) : Number.NaN;
      const tb = b.createdAt ? Date.parse(b.createdAt) : Number.NaN;
      return Number.isNaN(ta) || Number.isNaN(tb) ? undefined : tb - ta;
    }
  }
}

const hasValue = (key: SortKey, r: Resource): boolean => {
  switch (key) {
    case "ready":
      return readyRatio(r) !== undefined;
    case "message":
      return !r.inventoryOnly && Boolean(r.message);
    case "version":
      return revisionOf(r) !== "";
    case "age":
      return !Number.isNaN(r.createdAt ? Date.parse(r.createdAt) : Number.NaN);
    default:
      return true;
  }
};

/** Ties (and rows without a value) fall back to namespace, then name, ascending. */
const byIdentity = (a: Resource, b: Resource): number =>
  text(a.namespace, b.namespace) || text(a.name, b.name) || text(a.kind, b.kind);

/** The comparator for a sort; rows without a value in the column come last either way. */
export function comparator(sort: ListSort): (a: Resource, b: Resource) => number {
  const sign = sort.order === "desc" ? -1 : 1;
  return (a, b) => {
    const ea = hasValue(sort.key, a);
    const eb = hasValue(sort.key, b);
    if (ea !== eb) return ea ? -1 : 1;
    const c = ea ? (compareColumn(sort.key, a, b) ?? 0) : 0;
    return sign * c || byIdentity(a, b);
  };
}

export const sortResources = (items: readonly Resource[], sort: ListSort): Resource[] =>
  [...items].sort(comparator(sort));

/** What the hub can sort a paged index list by. */
const HUB_SORT: Partial<Record<SortKey, NonNullable<IndexQuery["sort"]>>> = {
  name: "name",
  kind: "kind",
  status: "status",
  age: "age",
};

/** Columns the hub cannot sort: disabled on a windowed list. */
export const hubSortable = (key: SortKey): boolean => key in HUB_SORT;

export const WINDOWED_SORT_HINT = "Available on smaller clusters";

/** The hub's `sort` and `order` for a windowed list; the default is the hub's kind order. */
export function hubSort(sort: ListSort | undefined): Pick<IndexQuery, "sort" | "order"> {
  const by = sort && HUB_SORT[sort.key];
  return by && sort ? { sort: by, order: sort.order } : { sort: "kind" };
}
