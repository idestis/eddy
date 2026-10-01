// The dependency graph of a cluster, kept live. The hub's GET …/graph gives the topology;
// statuses come from the resources list, which SSE deltas already patch, so a status
// change never refetches or re-lays out the graph. The graph is refetched only when the
// topology in the list changes (an object added or removed, a new dependsOn) or the set of
// expanded groups does. A hub without the endpoint (404) gets the same graph built from
// the list in the browser.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { isApiError } from "../api/client";
import { getGraph } from "../api/endpoints";
import type { GraphResponse, Resource } from "../api/types";
import { buildGraph, type GraphQueryOptions, MAX_EXPAND, resourcesTopologyKey, withLive } from "./graph";

export type GraphSource = "hub" | "browser";

export interface UseGraph {
  graph: GraphResponse | undefined;
  source: GraphSource | undefined;
  loading: boolean;
  /**
   * The graph shown is the previous one while a new expand set is fetched: group nodes
   * being expanded show a spinner until it lands.
   */
  expanding: boolean;
  error: Error | null;
}

// Hubs that answered 404 for an unfocused graph: they have no endpoint, skip the request.
const noEndpoint = new Set<string>();

/** The expand parameter: sorted, deduplicated, at most MAX_EXPAND ids (the last ones). */
export function expandParam(expand: readonly string[] | undefined): string {
  return [...new Set(expand ?? [])].slice(-MAX_EXPAND).sort().join(",");
}

export function graphQueryKey(cluster: string, opts: GraphQueryOptions, topology: string) {
  // Under the cluster's resources key, so a `resync` refetches it too.
  return [
    "resources",
    cluster,
    "graph",
    opts.kinds,
    opts.focus ?? "",
    opts.hops ?? 0,
    expandParam(opts.expand),
    topology,
  ] as const;
}

export function useGraph(
  cluster: string,
  items: readonly Resource[] | undefined,
  opts: GraphQueryOptions,
  enabled = true,
): UseGraph {
  const topology = useMemo(() => (items ? resourcesTopologyKey(items, opts.kinds) : ""), [items, opts.kinds]);
  const expand = expandParam(opts.expand);
  const q = useQuery({
    queryKey: graphQueryKey(cluster, opts, topology),
    queryFn: async ({ signal }): Promise<GraphResponse | null> => {
      if (noEndpoint.has(cluster)) return null;
      try {
        const res = await getGraph(
          cluster,
          { kinds: opts.kinds, focus: opts.focus, hops: opts.hops, expand: expand || undefined },
          signal,
        );
        return Array.isArray(res?.nodes) && Array.isArray(res.edges) ? res : null;
      } catch (err) {
        if (isApiError(err, "not_found")) {
          if (!opts.focus) noEndpoint.add(cluster);
          return null;
        }
        throw err;
      }
    },
    enabled: enabled && items !== undefined,
    staleTime: Number.POSITIVE_INFINITY,
    placeholderData: keepPreviousData,
    retry: 1,
  });

  const byId = useMemo(() => new Map((items ?? []).map((r) => [r.id, r])), [items]);
  const fromHub = q.data;
  // The browser build follows the list directly; it changes only when the topology does.
  // biome-ignore lint/correctness/useExhaustiveDependencies: rebuilt per topology, statuses come from withLive
  const built = useMemo(
    () => (fromHub === null && items ? buildGraph(items, { ...opts, expand: expand.split(",") }) : undefined),
    [fromHub === null, topology, opts.kinds, opts.focus, opts.hops, expand],
  );
  const base = fromHub ?? built;
  const graph = useMemo(() => (base ? withLive(base, byId, items ?? []) : undefined), [base, byId, items]);
  return {
    graph,
    source: fromHub ? "hub" : built ? "browser" : undefined,
    loading: q.isPending,
    expanding: q.isPlaceholderData && q.isFetching,
    error: q.error,
  };
}

/** Forgets which hubs lack the endpoint (tests). */
export function resetGraphEndpointCache(): void {
  noEndpoint.clear();
}
