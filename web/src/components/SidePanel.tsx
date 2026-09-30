import type { ReactNode } from "react";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { AskAIPanel } from "./AskAI";
import { KeyHint } from "./Status";

/** A two-option segmented control (Details / Ask AI, Grouped / Flat). */
export const SEG = "flex rounded-tile border border-line bg-surface p-[3px]";
export const SEG_BTN =
  "inline-flex h-[30px] flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg px-2.5 text-12-5 font-medium text-ink-2 aria-selected:bg-ink aria-selected:text-surface aria-pressed:bg-ink aria-pressed:text-surface [&[aria-selected=true]_kbd]:border-surface/40 [&[aria-selected=true]_kbd]:bg-transparent [&[aria-selected=true]_kbd]:text-inherit";

/** The right-hand panel: resource details or Ask AI, switched with the segment or `a`. */
export function SidePanel({
  cluster,
  resource,
  details,
}: {
  cluster: ClusterInfo;
  resource?: Resource;
  details: ReactNode;
}) {
  const { pane, setPane, ask } = useAppState();
  return (
    <>
      <div className="shrink-0 border-b border-line px-4 py-3">
        <div className={SEG} role="tablist" aria-label="Panel">
          <button
            type="button"
            role="tab"
            className={SEG_BTN}
            aria-selected={pane === "details"}
            onClick={() => setPane("details")}
          >
            Details
          </button>
          <button
            type="button"
            role="tab"
            className={SEG_BTN}
            aria-selected={pane === "ai"}
            onClick={() => ask()}
          >
            Ask AI <KeyHint id="ask" />
          </button>
        </div>
      </div>
      {pane === "ai" ? (
        <AskAIPanel cluster={cluster} resource={resource} />
      ) : (
        <div className="min-h-0 flex-1 overflow-auto px-5 pt-[18px] pb-7">{details}</div>
      )}
    </>
  );
}
