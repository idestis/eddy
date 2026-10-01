// How the user likes to see things: grouped or flat lists, raw or structured logs, the
// side panel's width and tab, open nav groups, the theme. Stored with the other prefs
// (GET/PUT /api/v1/prefs, key `view`) and cached in localStorage, which is read
// synchronously so the first render already uses the saved choice (no flash of the
// default). An explicit URL search parameter still wins, so shared links keep their view.

import { useSyncExternalStore } from "react";

export type ListView = "grouped" | "flat";
export type LogFormat = "structured" | "raw";

export interface ViewPrefs {
  listView?: ListView;
  logFormat?: LogFormat;
  /** Follow new log lines when a Logs tab opens. */
  logFollow?: boolean;
  logTail?: number;
  /** Seconds, as the `since` query; "" for any time. */
  logSince?: string;
  asideWidth?: number;
  pane?: "details" | "ai";
  /** Sidebar groups the user opened (true) or closed (false). */
  nav?: Record<string, boolean>;
  theme?: "light" | "dark";
  /** When this object last changed, so the newer copy wins across devices. */
  at?: number;
}

export const VIEW_STORAGE_KEY = "eddy.prefs.view";

const oneOf = <T extends string>(v: unknown, options: readonly T[]): T | undefined =>
  typeof v === "string" && (options as readonly string[]).includes(v) ? (v as T) : undefined;

/** Keeps only well-formed fields; anything else is dropped. Exported for tests. */
export function parseViewPrefs(raw: unknown): ViewPrefs {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
  const r = raw as Record<string, unknown>;
  const out: ViewPrefs = {};
  const listView = oneOf(r.listView, ["grouped", "flat"] as const);
  if (listView) out.listView = listView;
  const logFormat = oneOf(r.logFormat, ["structured", "raw"] as const);
  if (logFormat) out.logFormat = logFormat;
  if (typeof r.logFollow === "boolean") out.logFollow = r.logFollow;
  if (typeof r.logTail === "number" && r.logTail >= 1 && r.logTail <= 5000) out.logTail = r.logTail;
  if (typeof r.logSince === "string" && /^\d*$/.test(r.logSince)) out.logSince = r.logSince;
  if (typeof r.asideWidth === "number" && r.asideWidth >= 200 && r.asideWidth <= 2000)
    out.asideWidth = r.asideWidth;
  const pane = oneOf(r.pane, ["details", "ai"] as const);
  if (pane) out.pane = pane;
  if (r.nav && typeof r.nav === "object" && !Array.isArray(r.nav)) {
    const nav: Record<string, boolean> = {};
    for (const [k, v] of Object.entries(r.nav as Record<string, unknown>))
      if (typeof v === "boolean") nav[k] = v;
    out.nav = nav;
  }
  const theme = oneOf(r.theme, ["light", "dark"] as const);
  if (theme) out.theme = theme;
  if (typeof r.at === "number") out.at = r.at;
  return out;
}

/** URL > saved > default. */
export function resolvePref<T>(url: T | undefined, saved: T | undefined, fallback: T): T {
  return url ?? saved ?? fallback;
}

function load(): ViewPrefs {
  try {
    return parseViewPrefs(JSON.parse(localStorage.getItem(VIEW_STORAGE_KEY) ?? "null"));
  } catch {
    return {};
  }
}

let state: ViewPrefs = load();
const listeners = new Set<() => void>();

function commit(next: ViewPrefs): void {
  state = next;
  try {
    localStorage.setItem(VIEW_STORAGE_KEY, JSON.stringify(state));
  } catch {
    // Storage blocked: the hub copy still works.
  }
  for (const l of listeners) l();
}

export function getViewPrefs(): ViewPrefs {
  return state;
}

/** Saves a change now (locally) and, through PrefsProvider, to the hub a little later. */
export function setViewPrefs(patch: Partial<ViewPrefs>, now = Date.now()): void {
  const next = { ...state, ...patch, at: now };
  if (JSON.stringify({ ...next, at: 0 }) === JSON.stringify({ ...state, at: 0 })) return;
  commit(next);
}

/**
 * Takes the hub's copy when it is newer than ours. Returns true when ours is newer, so
 * the caller should write it back.
 */
export function mergeRemoteView(remote: unknown): boolean {
  const theirs = parseViewPrefs(remote);
  if ((theirs.at ?? 0) > (state.at ?? 0)) {
    commit(theirs);
    return false;
  }
  return (state.at ?? 0) > (theirs.at ?? 0);
}

export function subscribeViewPrefs(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}

export function useViewPrefs(): ViewPrefs {
  return useSyncExternalStore(subscribeViewPrefs, getViewPrefs, getViewPrefs);
}

/** Reloads from localStorage (tests). */
export function reloadViewPrefs(): void {
  state = load();
  for (const l of listeners) l();
}
