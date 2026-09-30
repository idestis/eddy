// One EventSource for the whole app (GET /api/v1/stream). Events are applied
// to the TanStack Query cache; components never read the stream directly.

import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { createContext, type ReactNode, useContext, useEffect, useState } from "react";
import { applyChange } from "./delta";
import { streamUrl } from "./endpoints";
import { keys } from "./queries";
import type { ChangeEvent, ClusterInfo, List, ResourceSnapshot } from "./types";

export type StreamState = "connecting" | "live" | "reconnecting";

const StreamContext = createContext<StreamState>("connecting");

export const useStreamState = (): StreamState => useContext(StreamContext);

const MIN_BACKOFF_MS = 1_000;
const MAX_BACKOFF_MS = 30_000;

/** Exponential backoff with full jitter, capped at 30 s. */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const ceiling = Math.min(MAX_BACKOFF_MS, MIN_BACKOFF_MS * 2 ** attempt);
  return Math.round(MIN_BACKOFF_MS / 2 + random() * (ceiling - MIN_BACKOFF_MS / 2));
}

function parse<T>(ev: Event): T | undefined {
  try {
    return JSON.parse((ev as MessageEvent<string>).data) as T;
  } catch {
    return undefined;
  }
}

/** Wires one stream's events into the query cache. Exported for tests. */
export function attachStreamHandlers(source: EventSource, qc: QueryClient): void {
  source.addEventListener("change", (ev) => {
    const change = parse<ChangeEvent>(ev);
    if (!change) return;
    qc.setQueryData<ResourceSnapshot>(keys.resources(change.cluster), (prev) => applyChange(prev, change));
  });
  source.addEventListener("resync", (ev) => {
    const data = parse<{ cluster: string }>(ev);
    if (data) void qc.invalidateQueries({ queryKey: keys.resources(data.cluster) });
  });
  source.addEventListener("clusters", (ev) => {
    const data = parse<List<ClusterInfo>>(ev);
    if (!data) return;
    const sorted = [...data.items].sort((a, b) => a.order - b.order || a.name.localeCompare(b.name));
    qc.setQueryData(keys.clusters, sorted);
  });
  source.addEventListener("thread", () => {
    void qc.invalidateQueries({ queryKey: keys.threadsAll });
  });
}

export function StreamProvider({ children }: { children: ReactNode }) {
  const qc = useQueryClient();
  const [state, setState] = useState<StreamState>("connecting");

  useEffect(() => {
    let source: EventSource | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let dropped = false;
    let stopped = false;

    const connect = () => {
      source = new EventSource(streamUrl);
      attachStreamHandlers(source, qc);
      source.addEventListener("open", () => {
        attempt = 0;
        setState("live");
        // Deltas sent while we were away are lost; refetch the snapshots.
        if (dropped) {
          dropped = false;
          void qc.invalidateQueries({ queryKey: keys.resourcesAll });
          void qc.invalidateQueries({ queryKey: keys.clusters });
        }
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
  }, [qc]);

  return <StreamContext.Provider value={state}>{children}</StreamContext.Provider>;
}
