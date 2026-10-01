import type { ReactNode } from "react";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { AskAIPanel } from "./AskAI";
import { KeyHint } from "./Status";
import { TabIndicator, useTabIndicator } from "./TabIndicator";

/**
 * A segmented control (Details / Ask AI, Grouped / Flat). The selected option's dark
 * pill is one shared <TabIndicator variant="pill"> that slides between the options.
 */
export const SEG = "relative flex rounded-tile border border-line bg-surface p-[3px]";
export const SEG_BTN =
  "seg-btn relative inline-flex h-[30px] flex-1 items-center justify-center gap-1.5 whitespace-nowrap rounded-lg px-2.5 text-12-5 font-medium text-ink-2 transition-colors duration-(--duration-slow) ease-standard hover:text-ink aria-selected:text-surface aria-pressed:text-surface [&_kbd]:transition-colors [&[aria-selected=true]_kbd]:border-surface/40 [&[aria-selected=true]_kbd]:bg-transparent [&[aria-selected=true]_kbd]:text-inherit [&[aria-pressed=true]_kbd]:border-surface/40 [&[aria-pressed=true]_kbd]:bg-transparent [&[aria-pressed=true]_kbd]:text-inherit";

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
  const seg = useTabIndicator(pane);
  return (
    <>
      <div className="shrink-0 border-b border-line px-4 py-3">
        <div ref={seg.list} className={SEG} role="tablist" aria-label="Panel">
          <TabIndicator ref={seg.indicator} variant="pill" />
          <button
            type="button"
            role="tab"
            className={SEG_BTN}
            aria-selected={pane === "details"}
            onClick={() => setPane("details")}
          >
            Details <KeyHint id="details" />
          </button>
          <button
            type="button"
            role="tab"
            className={SEG_BTN}
            aria-selected={pane === "ai"}
            onClick={() => (pane === "ai" ? undefined : ask())}
          >
            Ask AI <KeyHint id="ask" />
          </button>
        </div>
      </div>
      {pane === "ai" ? (
        <div key="ai" className="anim-fade-in flex min-h-0 flex-1 flex-col">
          <AskAIPanel cluster={cluster} resource={resource} />
        </div>
      ) : (
        <div key="details" className="anim-fade-in min-h-0 flex-1 overflow-auto px-5 pt-[18px] pb-7">
          {details}
        </div>
      )}
    </>
  );
}
