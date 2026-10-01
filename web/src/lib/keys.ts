// The single registry of keyboard shortcuts. The help overlay, the command
// palette hints and the global dispatcher all read from here; components only
// attach handlers to binding ids with useKeys().
//
// Key strings use tinykeys syntax: "k", "Shift+R", "g f" (a sequence),
// "$mod+k" (Cmd on macOS, Ctrl elsewhere), "[Shift]+?" (Shift optional,
// because "?" needs it on most layouts).

import { useEffect, useRef, useSyncExternalStore } from "react";
import { createKeybindingsHandler, type KeybindingHandler } from "tinykeys";

export type KeyGroup =
  | "Move around"
  | "Go to"
  | "Find and ask"
  | "Clusters"
  | "Act on selection"
  | "In logs"
  | "In the graph";

export interface Binding {
  keys: readonly string[];
  label: string;
  group: KeyGroup;
  /** Fire even while an input or textarea has focus. */
  inInput?: boolean;
  /** Fire while a dialog (palette, confirm, help) is open. */
  inOverlay?: boolean;
}

export const BINDINGS = {
  down: { keys: ["j", "ArrowDown"], label: "Move down", group: "Move around" },
  up: { keys: ["k", "ArrowUp"], label: "Move up", group: "Move around" },
  open: { keys: ["l", "Enter", "ArrowRight"], label: "Open selected", group: "Move around" },
  back: { keys: ["h", "Escape", "ArrowLeft"], label: "Go back up", group: "Move around" },
  top: { keys: ["g g"], label: "Jump to top", group: "Move around" },
  bottom: { keys: ["Shift+G"], label: "Jump to bottom", group: "Move around" },
  owner: { keys: ["u"], label: "Jump to owner", group: "Move around" },
  prevSection: { keys: ["["], label: "Previous page in this nav group", group: "Move around" },
  nextSection: { keys: ["]"], label: "Next page in this nav group", group: "Move around" },

  fleet: { keys: ["g f"], label: "Fleet overview", group: "Go to" },
  threads: { keys: ["g t"], label: "Threads", group: "Go to" },
  tokens: { keys: ["g k"], label: "Access tokens", group: "Go to" },
  audit: { keys: ["g a"], label: "My audit log", group: "Go to" },

  palette: {
    // Cmd+K on macOS and Ctrl+K everywhere (Ctrl+K is what Linux and Windows users expect).
    keys: ["$mod+k", "Control+k"],
    label: "Search every cluster",
    group: "Find and ask",
    inInput: true,
    inOverlay: true,
  },
  commands: { keys: ["[Shift]+:"], label: "Commands only", group: "Find and ask" },
  filter: { keys: ["/"], label: "Filter this list", group: "Find and ask" },
  ask: { keys: ["a"], label: "Ask AI about the selection (again: back to Details)", group: "Find and ask" },
  details: { keys: ["d"], label: "Details panel", group: "Find and ask" },
  help: { keys: ["[Shift]+?"], label: "Keyboard shortcuts", group: "Find and ask", inOverlay: true },

  cluster: {
    keys: ["1", "2", "3", "4", "5", "6", "7", "8", "9"],
    label: "Switch to cluster 1…9",
    group: "Clusters",
  },
  prevCluster: { keys: ["[Shift]+{"], label: "Previous cluster", group: "Clusters" },
  nextCluster: { keys: ["[Shift]+}"], label: "Next cluster", group: "Clusters" },
  addCluster: { keys: ["n"], label: "Add a cluster (fleet page)", group: "Clusters" },

  reconcile: { keys: ["r"], label: "Reconcile", group: "Act on selection" },
  reconcileSource: { keys: ["Shift+R"], label: "Reconcile with source", group: "Act on selection" },
  suspend: { keys: ["s"], label: "Suspend or resume", group: "Act on selection" },
  logs: { keys: ["Shift+L"], label: "Logs (pods and workloads)", group: "Act on selection" },
  tabThreads: { keys: ["t"], label: "Threads tab", group: "Act on selection" },
  compose: { keys: ["c"], label: "New thread (on the Threads tab)", group: "Act on selection" },
  tabOverview: { keys: ["o"], label: "Overview tab", group: "Act on selection" },
  tabEvents: { keys: ["e"], label: "Events tab", group: "Act on selection" },
  tabYaml: { keys: ["y"], label: "YAML tab", group: "Act on selection" },

  follow: { keys: ["f"], label: "Toggle follow", group: "In logs" },

  // The dependency graph. Arrows, h j k l and Enter move along edges while it has focus
  // (they reuse the Move around keys); these act on the view.
  graphFit: { keys: ["0"], label: "Fit the graph to the screen", group: "In the graph" },
  graphZoomIn: { keys: ["[Shift]+=", "[Shift]++"], label: "Zoom in", group: "In the graph" },
  graphZoomOut: { keys: ["-"], label: "Zoom out", group: "In the graph" },
  graphFocus: {
    keys: ["Shift+F"],
    label: "Focus on the selected node and its neighbours (again: whole graph)",
    group: "In the graph",
  },
} as const satisfies Record<string, Binding>;

export type KeyId = keyof typeof BINDINGS;

export const KEY_GROUPS: readonly KeyGroup[] = [
  "Move around",
  "Go to",
  "Find and ask",
  "Clusters",
  "Act on selection",
  "In logs",
  "In the graph",
];

const IS_MAC =
  typeof navigator === "object" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

const KEY_LABELS: Record<string, string> = {
  ArrowDown: "↓",
  ArrowUp: "↑",
  ArrowLeft: "←",
  ArrowRight: "→",
  Enter: "↵",
  Escape: "esc",
};

/**
 * True when a key event is one of a binding's single-key presses (not sequences). For
 * widgets that move focus themselves while focused, like the graph and the tree.
 */
export function pressMatches(
  id: KeyId,
  event: Pick<KeyboardEvent, "key" | "shiftKey" | "ctrlKey" | "metaKey" | "altKey">,
): boolean {
  if (event.ctrlKey || event.metaKey || event.altKey) return false;
  return (BINDINGS[id].keys as readonly string[]).some((k) => {
    if (k.includes(" ")) return false;
    const parts = k.split(/(?<=\w|\])\+/);
    const key = parts.pop() ?? k;
    const shift = parts.includes("Shift");
    const optional = parts.includes("[Shift]");
    return event.key === key && (optional || shift === event.shiftKey || key.length === 1);
  });
}

/** Turns one tinykeys string into the keycaps to show, e.g. "g f" → ["g", "f"]. */
export function displayKeys(key: string, mac = IS_MAC): string[] {
  const modCaps: Record<string, string> = mac
    ? { $mod: "⌘", Meta: "⌘", Control: "⌃", Alt: "⌥" }
    : { $mod: "Ctrl ", Control: "Ctrl ", Meta: "Win ", Alt: "Alt " };
  return key.split(" ").map((press) => {
    const parts = press.split(/(?<=\w|\])\+/);
    const last = parts.pop() ?? press;
    const cap = KEY_LABELS[last] ?? last;
    // Optional modifiers ("[Shift]") and Shift itself are implied by the key shown ("R", "?").
    const mods = parts.filter((p) => !p.startsWith("[") && p !== "Shift").map((p) => modCaps[p] ?? `${p} `);
    return mods.length ? `${mods.join("")}${cap.toUpperCase()}` : cap;
  });
}

/** The keycaps of a binding's first key, for hints next to buttons. */
export const hint = (id: KeyId): string[] => displayKeys(BINDINGS[id].keys[0] ?? "");

/** True when a binding's first key is a two-key sequence such as "g f". */
export const isSequence = (id: KeyId): boolean => (BINDINGS[id].keys[0] ?? "").includes(" ");

// Dispatcher: one tinykeys handler on window for the whole registry, so
// sequences like "g f" and single keys never race. Components push handlers
// for ids; the most recently mounted handler for an id wins. Effects run
// child-first, so do not bind the same id in a component and its ancestor.

export type KeyHandler = (event: KeyboardEvent) => void;

const stacks = new Map<KeyId, Array<{ current: KeyHandler }>>();
let overlays = 0;

/** Dialogs call this while open so list/detail keys stop firing underneath. */
export function pushOverlay(): () => void {
  overlays++;
  return () => {
    overlays = Math.max(0, overlays - 1);
  };
}

const EDITABLE = "input, textarea, select, [contenteditable]:not([contenteditable='false'])";
const CONTROLS = "button, a[href], summary, [role='button'], [role='tab'], [role='option']";

/** Decides whether a key event must not reach a binding. Exported for tests. */
export function shouldIgnore(event: KeyboardEvent, binding: Binding, overlayOpen = overlays > 0): boolean {
  if (event.isComposing) return true;
  if (overlayOpen && !binding.inOverlay) return true;
  const target = event.target instanceof Element ? event.target : null;
  if (!target) return false;
  if (!binding.inInput && target.closest(EDITABLE)) return true;
  // Enter and Space on a focused control activate that control, not the list.
  if ((event.key === "Enter" || event.key === " ") && target.closest(CONTROLS)) return true;
  return false;
}

function dispatch(id: KeyId, event: KeyboardEvent): void {
  const binding: Binding = BINDINGS[id];
  if (shouldIgnore(event, binding)) return;
  const stack = stacks.get(id);
  const top = stack?.[stack.length - 1];
  if (!top) return;
  event.preventDefault();
  top.current(event);
}

/**
 * Builds the tinykeys map for the registry. Sequences go first: tinykeys
 * stops at the first complete match, so "g f" must be checked before "f".
 * (JavaScript still orders integer-like keys such as "1" first; no sequence
 * ends in a digit, so that is harmless.)
 */
export function buildKeymap(
  onKey: (id: KeyId, event: KeyboardEvent, key: string) => void,
): Record<string, KeybindingHandler> {
  const entries = (Object.entries(BINDINGS) as [KeyId, Binding][]).flatMap(([id, b]) =>
    b.keys.map((key) => [key, (e: KeyboardEvent) => onKey(id, e, key)] as const),
  );
  entries.sort((a, b) => Number(b[0].includes(" ")) - Number(a[0].includes(" ")));
  return Object.fromEntries(entries);
}

// The first key of a pending sequence ("g" of "g f"), shown as a small "g …" hint.
const SEQUENCE_TIMEOUT_MS = 1000;
const SEQUENCE_STARTS = new Set(
  Object.values(BINDINGS as Record<string, Binding>).flatMap((b) =>
    b.keys.filter((k) => k.includes(" ")).map((k) => k.split(" ")[0] ?? ""),
  ),
);
let pending: string | null = null;
let pendingTimer: ReturnType<typeof setTimeout> | undefined;
const pendingListeners = new Set<() => void>();

function setPending(key: string | null): void {
  clearTimeout(pendingTimer);
  if (key) pendingTimer = setTimeout(() => setPending(null), SEQUENCE_TIMEOUT_MS);
  if (pending === key) return;
  pending = key;
  for (const l of pendingListeners) l();
}

/** The first key of a sequence in progress, or null. */
export function usePendingSequence(): string | null {
  return useSyncExternalStore(
    (l) => {
      pendingListeners.add(l);
      return () => pendingListeners.delete(l);
    },
    () => pending,
    () => null,
  );
}

const MODIFIERS = new Set(["Shift", "Control", "Alt", "Meta", "CapsLock", "AltGraph"]);

let installed = false;

/** Installs the global listener once; called from the app root. */
export function installKeyboard(target: Window = window): () => void {
  if (installed) return () => {};
  installed = true;
  let sequenceFired = false;
  const keymap = buildKeymap((id, event, key) => {
    if (key.includes(" ")) sequenceFired = true;
    dispatch(id, event);
  });
  // We filter inputs ourselves per binding, so tinykeys must not.
  const create = () =>
    createKeybindingsHandler(keymap, {
      timeout: SEQUENCE_TIMEOUT_MS,
      ignore: (e) => e.repeat && !["j", "k", "ArrowDown", "ArrowUp"].includes(e.key),
    });
  let handler = create();
  const onKeyDown = (e: KeyboardEvent) => {
    sequenceFired = false;
    handler(e);
    if (MODIFIERS.has(e.key)) return;
    if (sequenceFired) {
      // tinykeys stops at the first complete match and keeps the other half-typed
      // sequences pending, so "g g f" would still fire "g f". Start clean instead.
      handler = create();
      setPending(null);
      return;
    }
    const starts =
      !pending &&
      !e.ctrlKey &&
      !e.metaKey &&
      !e.altKey &&
      SEQUENCE_STARTS.has(e.key) &&
      overlays === 0 &&
      !(e.target instanceof Element && e.target.closest(EDITABLE));
    setPending(starts ? e.key : null);
  };
  target.addEventListener("keydown", onKeyDown);
  return () => {
    installed = false;
    setPending(null);
    target.removeEventListener("keydown", onKeyDown);
  };
}

/** Attaches handlers to binding ids for the lifetime of the calling component. */
export function useKeys(handlers: Partial<Record<KeyId, KeyHandler | false | undefined>>): void {
  const latest = useRef(handlers);
  latest.current = handlers;
  const ids = (Object.keys(handlers) as KeyId[]).filter((id) => handlers[id]).join(",");

  useEffect(() => {
    if (!ids) return;
    const refs = ids.split(",").map((id) => {
      const ref = {
        current: (e: KeyboardEvent) => {
          const h = latest.current[id as KeyId];
          if (h) h(e);
        },
      };
      const stack = stacks.get(id as KeyId) ?? [];
      stack.push(ref);
      stacks.set(id as KeyId, stack);
      return [id as KeyId, ref] as const;
    });
    return () => {
      for (const [id, ref] of refs) {
        const stack = stacks.get(id);
        if (stack)
          stacks.set(
            id,
            stack.filter((r) => r !== ref),
          );
      }
    };
  }, [ids]);
}

/** Every key string of the registry, normalised ("[Shift]+?" → "?"), with the bindings using it. */
export function keyOwners(bindings: Record<string, Binding> = BINDINGS): Map<string, string[]> {
  const owners = new Map<string, string[]>();
  for (const [id, b] of Object.entries(bindings)) {
    for (const k of b.keys) {
      const norm = k.replace(/\[Shift\]\+/g, "");
      owners.set(norm, [...(owners.get(norm) ?? []), id]);
    }
  }
  return owners;
}

/**
 * Key hints rendered for more than one action on one screen (KeyHint marks each with
 * data-key-id). Returns "t: tabThreads, compose"-style descriptions; empty when clean.
 */
export function hintCollisions(root: ParentNode): string[] {
  const byText = new Map<string, Set<string>>();
  for (const el of root.querySelectorAll<HTMLElement>("[data-key-id]")) {
    const text = (el.textContent ?? "").trim();
    const ids = byText.get(text) ?? new Set<string>();
    ids.add(el.dataset.keyId ?? "");
    byText.set(text, ids);
  }
  return [...byText]
    .filter(([, ids]) => ids.size > 1)
    .map(([text, ids]) => `${text}: ${[...ids].join(", ")}`);
}
