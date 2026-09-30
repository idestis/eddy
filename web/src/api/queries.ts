// Query keys and options. Every piece of server state goes through TanStack
// Query; the SSE stream (stream.tsx) patches the same keys.

import {
  infiniteQueryOptions,
  queryOptions,
  type UseQueryResult,
  useQueries,
  useQuery,
} from "@tanstack/react-query";
import { useCallback, useMemo } from "react";
import { isApiError, setCsrfToken } from "./client";
import * as api from "./endpoints";
import type { ClusterInfo, ConnectionInfo, Ref, Resource, ResourceSnapshot } from "./types";

export const keys = {
  me: ["me"] as const,
  providers: ["providers"] as const,
  clusters: ["clusters"] as const,
  clusterPermissions: ["clusterPermissions"] as const,
  connection: (cluster: string) => ["connection", cluster] as const,
  resourcesAll: ["resources"] as const,
  resources: (cluster: string) => ["resources", cluster] as const,
  yaml: (cluster: string, id: string) => ["yaml", cluster, id] as const,
  events: (cluster: string, id: string) => ["events", cluster, id] as const,
  threadsAll: ["threads"] as const,
  threads: (q: api.ThreadQuery) => ["threads", "list", q] as const,
  thread: (id: string) => ["threads", "detail", id] as const,
  tokens: ["tokens"] as const,
  audit: ["audit"] as const,
};

export const meQuery = queryOptions({
  queryKey: keys.me,
  queryFn: async () => {
    const me = await api.getMe();
    setCsrfToken(me.csrf);
    return me;
  },
  staleTime: 5 * 60_000,
});

export const providersQuery = queryOptions({
  queryKey: keys.providers,
  queryFn: api.getProviders,
  staleTime: Number.POSITIVE_INFINITY,
});

const byOrder = (a: ClusterInfo, b: ClusterInfo) => a.order - b.order || a.name.localeCompare(b.name);

export const clustersQuery = queryOptions({
  queryKey: keys.clusters,
  queryFn: async () => (await api.getClusters()).items.sort(byOrder),
  // Kept fresh by the `clusters` SSE event.
  staleTime: Number.POSITIVE_INFINITY,
});

export const clusterPermissionsQuery = queryOptions({
  queryKey: keys.clusterPermissions,
  queryFn: api.getClusterPermissions,
  staleTime: 5 * 60_000,
});

/** The management cluster's RBAC decides; a 403 is an answer, not an error to retry. */
const retryUnlessForbidden = (count: number, err: Error) => !isApiError(err, "forbidden") && count < 2;

/** A cluster's connection checklist. Kept fresh by the `connection` SSE event. */
export const connectionQuery = (cluster: string) =>
  queryOptions<ConnectionInfo>({
    queryKey: keys.connection(cluster),
    queryFn: () => api.getConnection(cluster),
    staleTime: 30_000,
    retry: retryUnlessForbidden,
  });

export const resourcesQuery = (cluster: string) =>
  queryOptions({
    queryKey: keys.resources(cluster),
    queryFn: ({ signal }) => api.getResources(cluster, signal),
    // Kept fresh by `change` and `resync` SSE events.
    staleTime: Number.POSITIVE_INFINITY,
  });

export const yamlQuery = (cluster: string, r: Resource) =>
  queryOptions({
    queryKey: keys.yaml(cluster, r.id),
    queryFn: () => api.getYaml(cluster, r),
    staleTime: 10_000,
  });

export const eventsQuery = (cluster: string, r: Resource) =>
  queryOptions({
    queryKey: keys.events(cluster, r.id),
    queryFn: async () => (await api.getEvents(cluster, r)).items,
    staleTime: 10_000,
    refetchInterval: 30_000,
  });

export const threadsQuery = (q: api.ThreadQuery) =>
  queryOptions({
    queryKey: keys.threads(q),
    queryFn: () => api.listThreads(q),
  });

export const threadsInfiniteQuery = (q: api.ThreadQuery) =>
  infiniteQueryOptions({
    queryKey: [...keys.threads(q), "infinite"],
    queryFn: ({ pageParam }) => api.listThreads({ ...q, cursor: pageParam || undefined }),
    initialPageParam: "",
    getNextPageParam: (last) => last.next || undefined,
  });

export const threadQuery = (id: string) =>
  queryOptions({
    queryKey: keys.thread(id),
    queryFn: () => api.getThread(id),
  });

export const tokensQuery = queryOptions({
  queryKey: keys.tokens,
  queryFn: async () => (await api.listTokens()).items,
});

export const auditQuery = infiniteQueryOptions({
  queryKey: keys.audit,
  queryFn: ({ pageParam }) => api.listAudit(pageParam || undefined),
  initialPageParam: "",
  getNextPageParam: (last) => last.next || undefined,
});

export function useMe() {
  return useQuery(meQuery);
}

/** Whether the signed-in user may add clusters: the hub feature and management-cluster RBAC. */
export function useCanAddCluster(): boolean {
  const { data: me } = useMe();
  const enabled = Boolean(me?.features.onboarding);
  const { data } = useQuery({ ...clusterPermissionsQuery, enabled });
  return enabled && Boolean(data?.onboarding && data.create);
}

export function useClusters() {
  return useQuery(clustersQuery);
}

export function useCluster(name: string | undefined): ClusterInfo | undefined {
  const { data } = useClusters();
  return name ? data?.find((c) => c.name === name) : undefined;
}

/** A resource that lives in a cluster; used by fleet-wide views. */
export interface FleetResource {
  cluster: string;
  resource: Resource;
}

/** Resources of every connected cluster, flattened. Used by the palette and the fleet view. */
export function useFleetResources(): { items: FleetResource[]; loading: boolean } {
  const { data } = useClusters();
  // Key on the names only: counts change every second, the set of clusters rarely.
  const joined = (data ?? [])
    .filter((c) => c.connected)
    .map((c) => c.name)
    .join("\n");
  const names = useMemo(() => (joined ? joined.split("\n") : []), [joined]);
  const combine = useCallback(
    (results: UseQueryResult<ResourceSnapshot>[]) => {
      const items: FleetResource[] = [];
      names.forEach((cluster, i) => {
        for (const resource of results[i]?.data?.items ?? []) items.push({ cluster, resource });
      });
      return { items, loading: results.some((r) => r.isPending) };
    },
    [names],
  );
  return useQueries({ queries: names.map((n) => resourcesQuery(n)), combine });
}

export const refId = (r: Ref): string => `${r.group}/${r.kind}/${r.namespace}/${r.name}`;
