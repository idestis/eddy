// The signed-in user's UI preferences. Cluster pins and visit history live in
// GET/PUT /api/v1/prefs (docs/api.md); localStorage is an instant cache so the order is
// right on first paint. Writes are debounced, and the hub's copy is merged in on load.

import { useQuery } from "@tanstack/react-query";
import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { putPrefs } from "../api/endpoints";
import { prefsQuery, useClusters } from "../api/queries";
import type { ClusterInfo } from "../api/types";
import {
  type ClusterPrefs,
  EMPTY_PREFS,
  fitPrefs,
  mergeClusterPrefs,
  orderClusters,
  parseClusterPrefs,
  recordVisit,
  togglePin,
} from "./frecency";

export const PREFS_STORAGE_KEY = "eddy.prefs.clusters";
export const SAVE_DELAY_MS = 5_000;

function loadLocal(): ClusterPrefs {
  try {
    return parseClusterPrefs(JSON.parse(localStorage.getItem(PREFS_STORAGE_KEY) ?? "null"));
  } catch {
    return EMPTY_PREFS;
  }
}

function saveLocal(prefs: ClusterPrefs): void {
  try {
    localStorage.setItem(PREFS_STORAGE_KEY, JSON.stringify(prefs));
  } catch {
    // Storage blocked: the hub copy still works.
  }
}

interface PrefsApi {
  clusters: ClusterPrefs;
  /** Counts a visit to a cluster (at most one per 30 minutes). */
  visit: (cluster: string) => void;
  togglePin: (cluster: string) => void;
}

const PrefsContext = createContext<PrefsApi>({
  clusters: EMPTY_PREFS,
  visit: () => {},
  togglePin: () => {},
});

export const usePrefs = (): PrefsApi => useContext(PrefsContext);

export function PrefsProvider({ children }: { children: ReactNode }) {
  const { data: remote } = useQuery(prefsQuery);
  const [clusters, setClusters] = useState<ClusterPrefs>(loadLocal);
  const latest = useRef(clusters);
  // The hub's whole object, so keys this UI does not know about survive a PUT.
  const stored = useRef<Record<string, unknown>>({});
  const loaded = useRef(false);
  const dirty = useRef(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  const flush = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = undefined;
    if (!dirty.current || !loaded.current) return;
    dirty.current = false;
    const body = { ...stored.current, clusters: fitPrefs(latest.current) };
    putPrefs(body).then(
      () => {
        stored.current = body;
      },
      () => {
        // Keep the local copy; the next change (or visit) tries again.
        dirty.current = true;
      },
    );
  }, []);

  const schedule = useCallback(() => {
    dirty.current = true;
    if (!loaded.current) return;
    clearTimeout(timer.current);
    timer.current = setTimeout(flush, SAVE_DELAY_MS);
  }, [flush]);

  const change = useCallback(
    (fn: (p: ClusterPrefs) => ClusterPrefs) => {
      const next = fn(latest.current);
      if (next === latest.current) return;
      latest.current = next;
      setClusters(next);
      saveLocal(next);
      schedule();
    },
    [schedule],
  );

  // Merge the hub's copy in once it arrives.
  useEffect(() => {
    if (!remote || loaded.current) return;
    loaded.current = true;
    stored.current = remote;
    const merged = mergeClusterPrefs(latest.current, parseClusterPrefs(remote.clusters));
    if (JSON.stringify(merged) !== JSON.stringify(latest.current)) {
      latest.current = merged;
      setClusters(merged);
      saveLocal(merged);
    }
    if (JSON.stringify(merged) !== JSON.stringify(parseClusterPrefs(remote.clusters))) schedule();
  }, [remote, schedule]);

  useEffect(() => {
    const onHide = () => flush();
    window.addEventListener("pagehide", onHide);
    return () => {
      window.removeEventListener("pagehide", onHide);
      flush();
    };
  }, [flush]);

  const visit = useCallback((name: string) => change((p) => recordVisit(p, name, Date.now())), [change]);
  const pin = useCallback((name: string) => change((p) => togglePin(p, name, Date.now())), [change]);
  const value = useMemo<PrefsApi>(() => ({ clusters, visit, togglePin: pin }), [clusters, visit, pin]);
  return <PrefsContext.Provider value={value}>{children}</PrefsContext.Provider>;
}

const NONE: ClusterInfo[] = [];

/** Clusters in the order shown everywhere: pinned first, then by frecency, then by CRD order. */
export function useOrderedClusters(): ClusterInfo[] {
  const { data = NONE } = useClusters();
  const { clusters: prefs } = usePrefs();
  return useMemo(() => orderClusters(data, prefs, Date.now()), [data, prefs]);
}

/** Counts a visit whenever the route's cluster changes. */
export function useRecordVisits(cluster: string | undefined): void {
  const { data } = useClusters();
  const { visit } = usePrefs();
  const known = Boolean(cluster && data?.some((c) => c.name === cluster));
  useEffect(() => {
    if (cluster && known) visit(cluster);
  }, [cluster, known, visit]);
}
