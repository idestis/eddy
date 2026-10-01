// One EventSource for the whole app (GET /api/v1/stream). Events are applied
// to the TanStack Query cache; components never read the stream directly.
//
// ADR-0006: the stream carries full `change` deltas only for the watched clusters (the
// one on screen, plus the side panel's when it differs, at most 5). Every other cluster
// sends `counts` and `attention` events. Changing the watch set reconnects the stream.

import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";
import { createContext, type ReactNode, useContext, useEffect, useRef, useState } from "react";
import { useAppState } from "../lib/appState";
import { detectChanges, recordDelta, settleRequested } from "../lib/liveMotion";
import {
  applyAttention,
  applyAttentionFindings,
  applyChange,
  applyClusterFindings,
  applyCounts,
  spliceClusterAttention,
} from "./delta";
import { getAttention, streamUrlFor } from "./endpoints";
import { keys } from "./queries";
import type {
  AttentionEvent,
  AttentionResponse,
  ChangeEvent,
  ClusterInfo,
  CountsEvent,
  List,
  ResourceSnapshot,
} from "./types";

export type StreamState = "connecting" | "live" | "reconnecting";

const StreamContext = createContext<StreamState>("connecting");

export const useStreamState = (): StreamState => useContext(StreamContext);

const MIN_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;

/** At most this many clusters stream full deltas (the hub enforces the same cap). */
export const MAX_WATCH = 5;
/** Quiet time before a new watch set reconnects, so quick j/k or route hops reconnect once. */
export const WATCH_DEBOUNCE_MS = 300;

/** Exponential backoff with full jitter, capped at 30 s. */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const ceiling = Math.min(MAX_BACKOFF_MS, MIN_BACKOFF_MS * 2 ** attempt);
  return Math.round(MIN_BACKOFF_MS / 2 + random() * (ceiling - MIN_BACKOFF_MS / 2));
}

/**
 * The clusters to watch next. `required` (the route's cluster, then the side panel's) always
 * makes it in. When they are all already watched the current set is returned unchanged (the
 * same array), so going back to a recent cluster does not reconnect; otherwise they go
 * first and recent ones fill up to `max`. No required cluster (the fleet page) watches
 * nothing: counts and attention events cover every cluster there.
 */
export function computeWatchSet(
  required: ReadonlyArray<string | undefined>,
  current: readonly string[],
  max = MAX_WATCH,
): readonly string[] {
  const want = [...new Set(required.filter((c): c is string => Boolean(c)))].slice(0, max);
  if (!want.length) return current.length ? [] : current;
  if (want.every((c) => current.includes(c))) return current;
  return [...new Set([...want, ...current])].slice(0, max);
}

function parse<T>(ev: Event): T | undefined {
  try {
    return JSON.parse((ev as MessageEvent<string>).data) as T;
  } catch {
    return undefined;
  }
}

/** Patches every cached GET /attention response the event touches (the fleet's and its cluster's). */
function patchAttention(qc: QueryClient, ev: Pick<AttentionEvent, "cluster" | "upserts" | "deletes">) {
  for (const [key, prev] of qc.getQueriesData<AttentionResponse | null>({ queryKey: keys.attentionAll })) {
    const scope = typeof key[1] === "string" ? key[1] : "";
    const next = applyAttention(prev, scope, ev);
    if (next !== prev) qc.setQueryData(key, next);
  }
}

/**
 * A `resync` of a cluster the stream does not watch: refetch only its part of the attention
 * set (GET /attention?cluster=) and splice it into the fleet's cached answer.
 */
async function resyncAttention(qc: QueryClient, cluster: string): Promise<void> {
  void qc.invalidateQueries({ queryKey: keys.attention(cluster), exact: true });
  if (!qc.getQueryData(keys.attention(""))) return;
  try {
    const fresh = await getAttention(cluster);
    const prev = qc.getQueryData<AttentionResponse | null>(keys.attention(""));
    const next = spliceClusterAttention(prev, cluster, fresh);
    if (next !== prev) qc.setQueryData(keys.attention(""), next);
  } catch {
    void qc.invalidateQueries({ queryKey: keys.attention(""), exact: true });
  }
}

/** Windowed list pages refetch at most this often under a stream of changes. */
const PAGE_REFRESH_MS = 2_000;
const pageRefresh = new Map<string, ReturnType<typeof setTimeout>>();

/**
 * A windowed list (lib/useClusterList.ts) holds pages, not the whole list, so a `change`
 * cannot be applied to it: the pages on screen refetch instead, at most every 2 s.
 */
function refreshPages(qc: QueryClient, cluster: string): void {
  if (pageRefresh.has(cluster) || !qc.getQueryCache().find({ queryKey: keys.indexPages(cluster) })) return;
  pageRefresh.set(
    cluster,
    setTimeout(() => {
      pageRefresh.delete(cluster);
      void qc.invalidateQueries({ queryKey: keys.indexPages(cluster) });
    }, PAGE_REFRESH_MS),
  );
}

/**
 * Wires one stream's events into the query cache. `watch` is the stream's watch set; it
 * decides what a `resync` refetches. Exported for tests.
 */
export function attachStreamHandlers(source: EventSource, qc: QueryClient, watch?: readonly string[]): void {
  source.addEventListener("hello", () => {
    // The stream may have opened after GET /attention answered; attention events from
    // before it are not replayed, so refetch what is shown.
    if (watch) void qc.invalidateQueries({ queryKey: keys.attentionAll });
  });
  source.addEventListener("change", (ev) => {
    const change = parse<ChangeEvent>(ev);
    if (!change) return;
    patchAttention(qc, change);
    refreshPages(qc, change.cluster);
    const key = keys.resources(change.cluster);
    const prev = qc.getQueryData<ResourceSnapshot>(key);
    const next = applyChange(prev, change);
    if (!next || next === prev) return;
    qc.setQueryData<ResourceSnapshot>(key, next);
    // After the cache: the list renders the new rows and their motion in one pass.
    recordDelta(change.cluster, detectChanges(prev?.items, next.items, change, Date.now()));
    settleRequested(change.cluster, change.upserts);
  });
  source.addEventListener("counts", (ev) => {
    const data = parse<CountsEvent | { items?: CountsEvent[] }>(ev);
    if (!data) return;
    const prev = qc.getQueryData<ClusterInfo[]>(keys.clusters);
    const next = applyCounts(prev, data);
    if (next && next !== prev) qc.setQueryData(keys.clusters, next);
  });
  source.addEventListener("attention", (ev) => {
    const data = parse<AttentionEvent>(ev);
    if (!data?.cluster) return;
    patchAttention(qc, { ...data, upserts: data.upserts ?? [], deletes: data.deletes ?? [] });
    const findings = data.findings;
    if (!findings) return;
    const clusters = qc.getQueryData<ClusterInfo[]>(keys.clusters);
    const next = applyClusterFindings(clusters, data.cluster, findings);
    if (next !== clusters) qc.setQueryData(keys.clusters, next);
    for (const [key, prev] of qc.getQueriesData<AttentionResponse | null>({ queryKey: keys.attentionAll })) {
      const patched = applyAttentionFindings(
        prev,
        typeof key[1] === "string" ? key[1] : "",
        data.cluster,
        findings,
      );
      if (patched !== prev) qc.setQueryData(key, patched);
    }
  });
  source.addEventListener("resync", (ev) => {
    const data = parse<{ cluster: string }>(ev);
    if (!data) return;
    if (!watch || watch.includes(data.cluster)) {
      // Under the resources key: the snapshot, hidden Jobs, kinds and index pages.
      void qc.invalidateQueries({ queryKey: keys.resources(data.cluster) });
      void qc.invalidateQueries({ queryKey: keys.indexPages(data.cluster) });
      if (!watch) void qc.invalidateQueries({ queryKey: keys.attentionAll });
      return;
    }
    void resyncAttention(qc, data.cluster);
  });
  source.addEventListener("clusters", (ev) => {
    const data = parse<List<ClusterInfo>>(ev);
    if (!data) return;
    const sorted = [...data.items].sort((a, b) => a.order - b.order || a.name.localeCompare(b.name));
    qc.setQueryData(keys.clusters, sorted);
  });
  source.addEventListener("connection", (ev) => {
    const data = parse<{ cluster: string }>(ev);
    if (!data) return;
    void qc.invalidateQueries({ queryKey: keys.connection(data.cluster) });
    void qc.invalidateQueries({ queryKey: keys.clusters });
  });
  source.addEventListener("thread", () => {
    void qc.invalidateQueries({ queryKey: keys.threadsAll });
  });
}

/**
 * After the watch set changed from `before` to `after`: snapshots of clusters that left it
 * stop getting deltas, so they are marked stale and refetch when shown again. Clusters that
 * joined need nothing here: the hub sends `resync` for every watched cluster on connect.
 */
export function rewatch(qc: QueryClient, before: readonly string[], after: readonly string[]): void {
  for (const c of before) {
    if (!after.includes(c)) void qc.invalidateQueries({ queryKey: keys.resources(c), refetchType: "none" });
  }
}

/** The watch set for the current route and selection, debounced (the first one is immediate). */
export function useWatchSet(): readonly string[] {
  const route = useParams({ strict: false }).cluster;
  const { selection } = useAppState();
  const side = selection?.cluster;
  const [watch, setWatch] = useState<readonly string[]>(() => computeWatchSet([route, side], []));
  const latest = useRef(watch);
  useEffect(() => {
    const t = setTimeout(() => {
      const next = computeWatchSet([route, side], latest.current);
      if (next === latest.current) return;
      latest.current = next;
      setWatch(next);
    }, WATCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [route, side]);
  return watch;
}

export function StreamProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [state, setState] = useState<StreamState>("connecting");
  const watch = useWatchSet();
  const watchKey = watch.join(",");
  // What the previous connection watched, for invalidating on a watch change.
  const watched = useRef<readonly string[] | undefined>(undefined);

  useEffect(() => {
    const set = watchKey ? watchKey.split(",") : [];
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let dropped = false;
    let stopped = false;

    const connect = () => {
      source = new EventSource(streamUrlFor(set));
      attachStreamHandlers(source, qc, set);
      source.addEventListener("open", () => {
        attempt = 0;
        setState("live");
        if (dropped) {
          // Deltas sent while we were away are lost; refetch the snapshots.
          dropped = false;
          void qc.invalidateQueries({ queryKey: keys.resourcesAll });
          void qc.invalidateQueries({ queryKey: keys.attentionAll });
          void qc.invalidateQueries({ queryKey: keys.clusters });
        } else if (watched.current) {
          rewatch(qc, watched.current, set);
        }
        watched.current = set;
      });
      source.addEventListener("error", () => {
        // Take over from the browser's fixed retry so we can back off and
        // survive HTTP errors, after which EventSource gives up.
        source?.close();
        if (stopped) return;
        dropped = true;
        setState("reconnecting");
        timer = setTimeout(connect, backoffDelay(attempt++));
      });
    };

    connect();
    return () => {
      stopped = true;
      clearTimeout(timer);
      source?.close();
    };
  }, [qc, watchKey]);

  return <StreamContext.Provider value={state}>{children}</StreamContext.Provider>;
}
