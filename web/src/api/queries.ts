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
import type {
  AttentionResponse,
  ClusterInfo,
  ConnectionInfo,
  KindsResponse,
  Ref,
  Resource,
  ResourceSnapshot,
  SearchResponse,
} from "./types";

export const keys = {
  me: ["me"] as const,
  providers: ["providers"] as const,
  prefs: ["prefs"] as const,
  clusters: ["clusters"] as const,
  clusterPermissions: ["clusterPermissions"] as const,
  connection: (cluster: string) => ["connection", cluster] as const,
  resourcesAll: ["resources"] as const,
  resources: (cluster: string) => ["resources", cluster] as const,
  // Under the cluster's resources key, so a `resync` refetches it too.
  hiddenJobs: (cluster: string, namespace: string | undefined) =>
    ["resources", cluster, "hiddenJobs", namespace ?? ""] as const,
  // Under the cluster's resources key, so a `resync` refetches it too.
  kinds: (cluster: string) => ["resources", cluster, "kinds"] as const,
  /** The paged list (`view=index`): the probe that tells a paging hub, and windowed pages. */
  index: (cluster: string) => ["index", cluster] as const,
  indexProbe: (cluster: string) => ["index", cluster, "probe"] as const,
  indexPages: (cluster: string) => ["index", cluster, "page"] as const,
  indexPage: (cluster: string, q: api.IndexQuery) => ["index", cluster, "page", q] as const,
  attentionAll: ["attention"] as const,
  /** "" is the whole fleet. */
  attention: (cluster: string) => ["attention", cluster] as const,
  search: (q: api.SearchQuery) => ["search", q.scope, q.cluster ?? "", q.kind ?? "", q.q] as const,
  yaml: (cluster: string, id: string) => ["yaml", cluster, id] as const,
  events: (cluster: string, id: string) => ["events", cluster, id] as const,
  threadsAll: ["threads"] as const,
  threads: (q: api.ThreadQuery) => ["threads", "list", q] as const,
  thread: (id: string) => ["threads", "detail", id] as const,
  /** Ask AI chats (ADR-0007): the owner's list and each chat with its messages. */
  chatsAll: ["chats"] as const,
  chatList: ["chats", "list"] as const,
  chat: (id: string) => ["chats", "detail", id] as const,
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

/** The user's stored UI preferences; prefs.tsx merges and writes them back. */
export const prefsQuery = queryOptions({
  queryKey: keys.prefs,
  queryFn: async () => (await api.getPrefs()).data,
  staleTime: Number.POSITIVE_INFINITY,
  retry: 1,
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

/**
 * The cluster's kinds for navigation. Resolves to `null` when the hub does not serve the
 * endpoint (an older hub) or answers something else, so the sidebar falls back to its
 * static tree.
 */
export const kindsQuery = (cluster: string) =>
  queryOptions({
    queryKey: keys.kinds(cluster),
    queryFn: async ({ signal }): Promise<KindsResponse | null> => {
      try {
        const res = await api.getKinds(cluster, signal);
        return Array.isArray(res?.items) ? res : null;
      } catch (err) {
        if (isApiError(err, "disconnected") || isApiError(err, "network")) throw err;
        return null;
      }
    },
    staleTime: 30_000,
    refetchInterval: 60_000,
    retry: 1,
  });

/** True when the hub does not serve an endpoint (an older hub): the caller falls back. */
const missing = (err: unknown) => isApiError(err, "not_found");

/**
 * Rows that need attention (GET /attention), fleet-wide for "". Kept fresh by the stream's
 * `attention` and `change` events. Resolves to `null` on a hub without the endpoint.
 */
export const attentionQuery = (cluster = "") =>
  queryOptions({
    queryKey: keys.attention(cluster),
    queryFn: async ({ signal }): Promise<AttentionResponse | null> => {
      try {
        return await api.getAttention(cluster, signal);
      } catch (err) {
        if (missing(err)) return null;
        throw err;
      }
    },
    staleTime: 60_000,
    retry: 1,
  });

/** GET …/objects/…: one full summary, for a row known only from the paged list. */
export const objectQuery = (cluster: string, r: Ref) =>
  queryOptions({
    queryKey: ["object", cluster, refId(r)] as const,
    queryFn: () => api.getObject(cluster, r),
    staleTime: 15_000,
  });

/**
 * Server-side palette search (GET /search). Resolves to `null` on a hub without the
 * endpoint, so the palette falls back to ranking snapshots itself. A newer key aborts the
 * request in flight: TanStack Query aborts a query's signal once nothing observes it.
 */
export const searchQuery = (q: api.SearchQuery) =>
  queryOptions({
    queryKey: keys.search(q),
    queryFn: async ({ signal }): Promise<SearchResponse | null> => {
      try {
        return await api.search(q, signal);
      } catch (err) {
        if (isApiError(err, "not_found")) return null;
        throw err;
      }
    },
    staleTime: 10_000,
    gcTime: 30_000,
    retry: (count, err) => isApiError(err, "rate_limited") && count < 2,
    retryDelay: 150,
  });

/**
 * Jobs with the finished ones the agent hides, fetched on demand a page at a time. SSE deltas
 * keep patching the normal list (resourcesQuery); this only adds the hidden rows.
 */
export const hiddenJobsQuery = (cluster: string, namespace: string | undefined) =>
  infiniteQueryOptions({
    queryKey: keys.hiddenJobs(cluster, namespace),
    queryFn: ({ pageParam, signal }) =>
      api.getJobsWithHidden(cluster, { namespace, cursor: pageParam || undefined }, signal),
    initialPageParam: "",
    getNextPageParam: (last) => last.hidden.next || undefined,
    staleTime: 60_000,
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

/**
 * Resources of every connected cluster, flattened: every cluster's full snapshot. Only a
 * fallback for hubs without GET /search or GET /attention (ADR-0006); pass `enabled` false
 * otherwise so nothing is fetched.
 */
export function useFleetResources(enabled = true): { items: FleetResource[]; loading: boolean } {
  const { data } = useClusters();
  // Key on the names only: counts change every second, the set of clusters rarely.
  const joined = (enabled ? (data ?? []) : [])
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
