// Which group nodes of one graph the user expanded. Kept per graph (a scope such as the
// cluster and the detail page's object) for the browser session, so going back to a graph
// shows it as it was left. Groups the hub collapsed are sent back as `expand=` (at most
// MAX_EXPAND, the most recent); groups the browser made (the detail page's owned rows)
// stay local and never refetch.

import { useCallback, useMemo, useState } from "react";
import { MAX_EXPAND } from "./graph";

const PREFIX = "eddy.graph.expand:";

interface Entry {
  id: string;
  /** A group the hub collapsed: expanding it refetches the graph. */
  hub: boolean;
}

// The session's sets, also when sessionStorage is not available.
const memory = new Map<string, Entry[]>();

function load(scope: string): Entry[] {
  const cached = memory.get(scope);
  if (cached) return cached;
  try {
    const raw = sessionStorage.getItem(PREFIX + scope);
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (e): e is Entry => typeof e?.id === "string" && e.id.startsWith("group:") && typeof e.hub === "boolean",
    );
  } catch {
    return [];
  }
}

function save(scope: string, entries: Entry[]): void {
  memory.set(scope, entries);
  try {
    if (entries.length) sessionStorage.setItem(PREFIX + scope, JSON.stringify(entries));
    else sessionStorage.removeItem(PREFIX + scope);
  } catch {
    // Private windows and blocked storage: the in-memory copy is enough.
  }
}

export interface GraphExpansion {
  /** Every expanded group id. */
  expanded: ReadonlySet<string>;
  /** The ids for the hub's expand parameter, oldest first. */
  hub: readonly string[];
  /** Expands a group; `hub` when the hub collapsed it (refetch with expand=). */
  expand: (id: string, hub: boolean) => void;
  /** "Collapse groups": forgets every expanded group of this graph. */
  clear: () => void;
}

export function useGraphExpansion(scope: string): GraphExpansion {
  const [state, setState] = useState(() => ({ scope, entries: load(scope) }));
  // A new scope (another object, another cluster) reads its own set.
  const entries = state.scope === scope ? state.entries : load(scope);
  if (state.scope !== scope) setState({ scope, entries });

  const expand = useCallback(
    (id: string, hub: boolean) =>
      setState((cur) => {
        if (cur.entries.some((e) => e.id === id)) return cur;
        let next = [...cur.entries, { id, hub }];
        // The hub takes MAX_EXPAND ids: the oldest hub group collapses again.
        const hubIds = next.filter((e) => e.hub);
        if (hubIds.length > MAX_EXPAND) {
          const drop = new Set(hubIds.slice(0, hubIds.length - MAX_EXPAND).map((e) => e.id));
          next = next.filter((e) => !drop.has(e.id));
        }
        save(cur.scope, next);
        return { scope: cur.scope, entries: next };
      }),
    [],
  );
  const clear = useCallback(
    () =>
      setState((cur) => {
        save(cur.scope, []);
        return { scope: cur.scope, entries: [] };
      }),
    [],
  );
  const expanded = useMemo(() => new Set(entries.map((e) => e.id)), [entries]);
  const hub = useMemo(() => entries.filter((e) => e.hub).map((e) => e.id), [entries]);
  return { expanded, hub, expand, clear };
}

/** Forgets every expanded set (tests). */
export function resetGraphExpansion(): void {
  memory.clear();
  try {
    for (let i = sessionStorage.length - 1; i >= 0; i--) {
      const k = sessionStorage.key(i);
      if (k?.startsWith(PREFIX)) sessionStorage.removeItem(k);
    }
  } catch {
    // Nothing to clear.
  }
}
