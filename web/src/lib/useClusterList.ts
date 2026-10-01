// Where the cluster list's rows come from (ADR-0006 "Render order"):
//
// 1. The probe asks for the first page of the paged list (`view=index`, 200 rows). A hub
//    that does not page answers the whole snapshot instead; it seeds the snapshot query
//    and the page works as before ("legacy").
// 2. Up to WINDOW_THRESHOLD rows ("fill"): the first page renders at once, the full
//    snapshot loads behind it, and filtering stays local once it is there.
// 3. Above it ("windowed"): no snapshot. Only the pages on screen load (500 rows, at most 8
//    kept), and the hub filters and sorts.

import { keepPreviousData, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useState } from "react";
import { getIndexPage, type IndexQuery } from "../api/endpoints";
import { keys } from "../api/queries";
import type { IndexPage, Resource, ResourceSnapshot, Status } from "../api/types";
import {
  FIRST_PAGE,
  fromIndexRow,
  isIndexPage,
  keepPages,
  pagesFor,
  WINDOW_PAGE,
  WINDOW_THRESHOLD,
} from "./indexRows";
import type { ListRow, StatusCounts, StatusFilter } from "./resourceRows";

export type ListMode = "probing" | "legacy" | "fill" | "windowed";

export interface ProbeResult {
  /** The first page, or null when the hub answered the whole snapshot. */
  page: IndexPage | null;
}

/** The probe; exported for tests. */
export async function probeList(
  cluster: string,
  seed: (snapshot: ResourceSnapshot) => void,
  signal?: AbortSignal,
): Promise<ProbeResult> {
  const res = await getIndexPage(cluster, { limit: FIRST_PAGE, sort: "kind" }, signal);
  if (isIndexPage(res)) return { page: res };
  seed(res);
  return { page: null };
}

export const modeOf = (probe: ProbeResult | undefined): ListMode =>
  !probe ? "probing" : !probe.page ? "legacy" : probe.page.total > WINDOW_THRESHOLD ? "windowed" : "fill";

export function useListMode(cluster: string, enabled: boolean) {
  const qc = useQueryClient();
  const probe = useQuery({
    queryKey: keys.indexProbe(cluster),
    queryFn: ({ signal }) =>
      probeList(cluster, (snapshot) => qc.setQueryData(keys.resources(cluster), snapshot), signal),
    enabled,
    // Whether the hub pages, and roughly how big the cluster is, rarely changes.
    staleTime: Number.POSITIVE_INFINITY,
    retry: 1,
  });
  const mode = probe.isError ? "legacy" : modeOf(probe.data);
  const firstPage = useMemo(() => probe.data?.page?.items.map(fromIndexRow), [probe.data]);
  return { mode, firstPage, total: probe.data?.page?.total, error: probe.error };
}

const EMPTY_COUNTS: StatusCounts = { attention: 0, failed: 0, reconciling: 0, suspended: 0, completed: 0 };

function countsOfFacet(statuses: Partial<Record<Status, number>> | undefined): StatusCounts | undefined {
  if (!statuses) return undefined;
  const out = { ...EMPTY_COUNTS };
  for (const [s, n = 0] of Object.entries(statuses) as Array<[Status, number | undefined]>) {
    if (s !== "ready" && s !== "completed") out.attention += n;
    if (s !== "ready" && s !== "unknown") out[s] += n;
  }
  return out;
}

export interface WindowFilter {
  kind?: string;
  status?: StatusFilter;
  namespace?: string;
  text?: string;
}

/** The windowed list: rows for [0, total), loaded pages filled in, the rest placeholders. */
export function useWindowedList(cluster: string, filter: WindowFilter, enabled: boolean) {
  const query = useMemo(
    (): IndexQuery => ({
      kind: filter.kind,
      // "attention" is the hub's status filter too.
      status: filter.status,
      namespace: filter.namespace,
      q: filter.text || undefined,
      sort: "kind",
    }),
    [filter.kind, filter.status, filter.namespace, filter.text],
  );
  const queryKey = JSON.stringify(query);
  const [range, setRange] = useState<[number, number]>([0, 60]);
  const [recent, setRecent] = useState<number[]>([0]);
  // A new filter starts at the top with only its first page.
  // biome-ignore lint/correctness/useExhaustiveDependencies: reset on a new filter only
  useEffect(() => {
    setRange([0, 60]);
    setRecent([0]);
  }, [queryKey]);

  const results = useQueries({
    queries: recent.map((p) => ({
      queryKey: keys.indexPage(cluster, { ...query, offset: p * WINDOW_PAGE }),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        getIndexPage(cluster, { ...query, offset: p * WINDOW_PAGE, limit: WINDOW_PAGE }, signal),
      enabled,
      staleTime: 15_000,
      gcTime: 30_000,
      placeholderData: keepPreviousData,
    })),
  });

  // The newest answer decides the total and the status facets.
  let latest: IndexPage | undefined;
  let at = -1;
  for (const r of results) {
    if (isIndexPage(r.data) && r.dataUpdatedAt > at) {
      latest = r.data;
      at = r.dataUpdatedAt;
    }
  }
  const total = latest?.total ?? 0;
  const loading = results.some((r) => r.isPending);

  const wanted = pagesFor(range[0], range[1], Math.max(total, WINDOW_PAGE));
  const wantedKey = wanted.join(",");
  // biome-ignore lint/correctness/useExhaustiveDependencies: wantedKey stands for wanted
  useEffect(() => {
    setRecent((cur) => {
      const next = keepPages(cur, wanted);
      return next.join(",") === cur.join(",") ? cur : next;
    });
  }, [wantedKey]);

  // Rebuilt only when a page's data changes, not on every render.
  const stamp = results.map((r) => (r.isPlaceholderData ? 0 : r.dataUpdatedAt)).join(",");
  const pagesRef = { recent, data: results.map((r) => (r.isPlaceholderData ? undefined : r.data)) };
  // biome-ignore lint/correctness/useExhaustiveDependencies: stamp and total stand for the page data
  const rows = useMemo((): ListRow[] => {
    const loaded = new Map<number, Resource>();
    pagesRef.recent.forEach((p, i) => {
      const page = pagesRef.data[i];
      if (!isIndexPage(page)) return;
      page.items.forEach((row, j) => {
        loaded.set(p * WINDOW_PAGE + j, fromIndexRow(row));
      });
    });
    return Array.from({ length: total }, (_, i): ListRow => {
      const r = loaded.get(i);
      return r ? { type: "resource", key: r.id, resource: r } : { type: "placeholder", key: `p${i}` };
    });
  }, [stamp, total, recent]);

  const onRange = useCallback((start: number, end: number) => {
    setRange((cur) => (cur[0] === start && cur[1] === end ? cur : [start, end]));
  }, []);

  return {
    rows,
    total,
    loading,
    counts: countsOfFacet(latest?.facets?.statuses),
    onRange,
  };
}
