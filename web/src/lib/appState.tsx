// UI state shared across the shell: what is selected, which side pane is
// showing, and which overlays are open. Server state lives in TanStack Query.

import { createContext, type ReactNode, useCallback, useContext, useMemo, useRef, useState } from "react";
import type { Resource } from "../api/types";
import type { PendingAttachment } from "./logAttachments";
import { getViewPrefs, setViewPrefs } from "./viewPrefs";

export type Pane = "details" | "ai";

export interface Selection {
  cluster: string;
  resource?: Resource;
}

interface AppState {
  selection: Selection | null;
  setSelection: (s: Selection | null) => void;
  pane: Pane;
  /** Switches the side pane. Leaving Ask AI returns focus to where it was before. */
  setPane: (p: Pane) => void;
  /** The palette's initial query while it is open, otherwise null. */
  palette: string | null;
  openPalette: (query?: string) => void;
  closePalette: () => void;
  help: boolean;
  setHelp: (open: boolean) => void;
  /** The trigger the cluster menu opened from while it is open, otherwise null. */
  clusterMenu: HTMLElement | null;
  openClusterMenu: (anchor: HTMLElement) => void;
  closeClusterMenu: () => void;
  /** Bumped to ask the Ask AI composer to take focus. */
  askFocus: number;
  /** True once per askFocus bump: the composer calls it before focusing itself. */
  takeAskFocus: () => boolean;
  /** Question queued for the Ask AI panel (from the palette or a chip). */
  pendingQuestion: string | null;
  ask: (question?: string) => void;
  clearPendingQuestion: () => void;
  /** Log lines waiting for the Ask AI composer (from a logs toolbar or selection). */
  pendingAttachment: PendingAttachment | null;
  /** Opens Ask AI on the open chat with log lines (or YAML) attached and a suggested question. */
  askWithLogs: (p: PendingAttachment) => void;
  clearPendingAttachment: () => void;
}

const AppStateContext = createContext<AppState | null>(null);

export function useAppState(): AppState {
  const s = useContext(AppStateContext);
  if (!s) throw new Error("useAppState must be used inside <AppStateProvider>");
  return s;
}

/** Elements inside the Ask AI panel carry this attribute; focus there is not "where it was". */
export const ASK_PANEL_ATTR = "data-ask-panel";

export function AppStateProvider({ children }: { children: ReactNode }) {
  const [selection, setSelection] = useState<Selection | null>(null);
  const [pane, setPaneState] = useState<Pane>(() => getViewPrefs().pane ?? "details");
  const [palette, setPalette] = useState<string | null>(null);
  const [help, setHelp] = useState(false);
  const [clusterMenu, setClusterMenu] = useState<HTMLElement | null>(null);
  const [askFocus, setAskFocus] = useState(0);
  const [pendingQuestion, setPendingQuestion] = useState<string | null>(null);
  const [pendingAttachment, setPendingAttachment] = useState<PendingAttachment | null>(null);
  const consumed = useRef(0);
  const requested = useRef(0);
  const returnFocus = useRef<HTMLElement | null>(null);

  const openPalette = useCallback((query = "") => setPalette(query), []);
  const closePalette = useCallback(() => setPalette(null), []);
  const openClusterMenu = useCallback((anchor: HTMLElement) => setClusterMenu(anchor), []);
  const closeClusterMenu = useCallback(() => setClusterMenu(null), []);

  const ask = useCallback((question?: string) => {
    const active = document.activeElement;
    if (active instanceof HTMLElement && active !== document.body && !active.closest(`[${ASK_PANEL_ATTR}]`)) {
      returnFocus.current = active;
    }
    setPaneState("ai");
    setViewPrefs({ pane: "ai" });
    if (question) setPendingQuestion(question);
    requested.current += 1;
    setAskFocus(requested.current);
  }, []);

  const paneRef = useRef<Pane>("details");
  paneRef.current = pane;
  const setPane = useCallback((p: Pane) => {
    if (paneRef.current === "ai" && p === "details") {
      const el = returnFocus.current;
      returnFocus.current = null;
      // After the pane has re-rendered.
      requestAnimationFrame(() => {
        if (el?.isConnected) el.focus({ preventScroll: true });
      });
    }
    setPaneState(p);
    setViewPrefs({ pane: p });
  }, []);

  const takeAskFocus = useCallback(() => {
    if (requested.current <= consumed.current) return false;
    consumed.current = requested.current;
    return true;
  }, []);

  const clearPendingQuestion = useCallback(() => setPendingQuestion(null), []);

  const askWithLogs = useCallback(
    (p: PendingAttachment) => {
      setPendingAttachment(p);
      ask();
    },
    [ask],
  );
  const clearPendingAttachment = useCallback(() => setPendingAttachment(null), []);

  const value = useMemo<AppState>(
    () => ({
      selection,
      setSelection,
      pane,
      setPane,
      palette,
      openPalette,
      closePalette,
      help,
      setHelp,
      clusterMenu,
      openClusterMenu,
      closeClusterMenu,
      askFocus,
      takeAskFocus,
      pendingQuestion,
      ask,
      clearPendingQuestion,
      pendingAttachment,
      askWithLogs,
      clearPendingAttachment,
    }),
    [
      pendingAttachment,
      askWithLogs,
      clearPendingAttachment,
      selection,
      pane,
      setPane,
      palette,
      openPalette,
      closePalette,
      help,
      clusterMenu,
      openClusterMenu,
      closeClusterMenu,
      askFocus,
      takeAskFocus,
      pendingQuestion,
      ask,
      clearPendingQuestion,
    ],
  );

  return <AppStateContext.Provider value={value}>{children}</AppStateContext.Provider>;
}
