// UI state shared across the shell: what is selected, which side pane is
// showing, and which overlays are open. Server state lives in TanStack Query.

import { createContext, type ReactNode, useCallback, useContext, useMemo, useState } from "react";
import type { Resource } from "../api/types";

export type Pane = "details" | "ai";

export interface Selection {
  cluster: string;
  resource?: Resource;
}

interface AppState {
  selection: Selection | null;
  setSelection: (s: Selection | null) => void;
  pane: Pane;
  setPane: (p: Pane) => void;
  /** The palette's initial query while it is open, otherwise null. */
  palette: string | null;
  openPalette: (query?: string) => void;
  closePalette: () => void;
  help: boolean;
  setHelp: (open: boolean) => void;
  clusterMenu: boolean;
  setClusterMenu: (open: boolean) => void;
  /** Bumped to ask the Ask AI composer to take focus. */
  askFocus: number;
  /** Question queued for the Ask AI panel (from the palette or a chip). */
  pendingQuestion: string | null;
  ask: (question?: string) => void;
  clearPendingQuestion: () => void;
}

const AppStateContext = createContext<AppState | null>(null);

export function useAppState(): AppState {
  const s = useContext(AppStateContext);
  if (!s) throw new Error("useAppState must be used inside <AppStateProvider>");
  return s;
}

export function AppStateProvider({ children }: { children: ReactNode }) {
  const [selection, setSelection] = useState<Selection | null>(null);
  const [pane, setPane] = useState<Pane>("details");
  const [palette, setPalette] = useState<string | null>(null);
  const [help, setHelp] = useState(false);
  const [clusterMenu, setClusterMenu] = useState(false);
  const [askFocus, setAskFocus] = useState(0);
  const [pendingQuestion, setPendingQuestion] = useState<string | null>(null);

  const openPalette = useCallback((query = "") => setPalette(query), []);
  const closePalette = useCallback(() => setPalette(null), []);
  const ask = useCallback((question?: string) => {
    setPane("ai");
    if (question) setPendingQuestion(question);
    setAskFocus((n) => n + 1);
  }, []);
  const clearPendingQuestion = useCallback(() => setPendingQuestion(null), []);

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
      setClusterMenu,
      askFocus,
      pendingQuestion,
      ask,
      clearPendingQuestion,
    }),
    [
      selection,
      pane,
      palette,
      openPalette,
      closePalette,
      help,
      clusterMenu,
      askFocus,
      pendingQuestion,
      ask,
      clearPendingQuestion,
    ],
  );

  return <AppStateContext.Provider value={value}>{children}</AppStateContext.Provider>;
}
