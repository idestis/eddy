// The paged list of ADR-0006 (`…/resources?view=index`): converting its compact rows for the
// list, and the windowing arithmetic. Up to WINDOW_THRESHOLD rows the page shows the first
// page at once and loads the whole list behind it (then filters locally, as before); above
// it the list stays windowed: only the pages on screen are fetched, filtered and sorted by
// the hub.

import type { IndexPage, IndexRow, Resource, ResourceSnapshot } from "../api/types";

/** Rows per cluster above which the list stays windowed (owner decision, ADR-0006). */
export const WINDOW_THRESHOLD = 25_000;
/** The first page: enough to fill the screen. */
export const FIRST_PAGE = 200;
/** Windowed pages. */
export const WINDOW_PAGE = 500;
/** Windowed pages kept in memory. */
export const MAX_PAGES = 8;

export const isIndexPage = (res: IndexPage | ResourceSnapshot | undefined | null): res is IndexPage =>
  Boolean(res) && typeof (res as IndexPage).total === "number";

/** An index row as a (partial) Resource for the list. Details load on demand. */
export function fromIndexRow(row: IndexRow): Resource {
  return {
    group: row.group,
    kind: row.kind,
    namespace: row.namespace,
    name: row.name,
    id: row.id,
    version: "",
    status: row.status,
    blocked: row.blocked,
    message: row.message,
    revision: row.revision,
    replicas: row.replicas,
    completions: row.completions,
    owner: row.owner,
    project: row.project,
    inventoryOnly: row.inventoryOnly,
    lastChanged: row.lastChanged,
    resourceVersion: "",
  };
}

/** The windowed pages that cover rows [start, end]. */
export function pagesFor(start: number, end: number, total: number, size = WINDOW_PAGE): number[] {
  if (total <= 0 || end < start) return [];
  const last = Math.min(Math.floor(Math.min(end, total - 1) / size), Math.floor((total - 1) / size));
  const out: number[] = [];
  for (let p = Math.max(0, Math.floor(start / size)); p <= last; p++) out.push(p);
  return out;
}

/** Pages to keep: the wanted ones, then the most recently used, at most `max`. */
export function keepPages(recent: readonly number[], wanted: readonly number[], max = MAX_PAGES): number[] {
  return [...new Set([...wanted, ...recent])].slice(0, max);
}
