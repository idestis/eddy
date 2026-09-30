import type { ReactNode } from "react";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { hint } from "../lib/keys";
import { AskAIPanel } from "./AskAI";
import { Keys } from "./Status";

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
      <div className="dtop">
        <div className="seg" role="tablist" aria-label="Panel">
          <button
            type="button"
            role="tab"
            aria-selected={pane === "details"}
            onClick={() => setPane("details")}
          >
            Details
          </button>
          <button type="button" role="tab" aria-selected={pane === "ai"} onClick={() => ask()}>
            Ask AI <Keys keys={hint("ask")} />
          </button>
        </div>
      </div>
      {pane === "ai" ? (
        <AskAIPanel cluster={cluster} resource={resource} />
      ) : (
        <div className="dbody">{details}</div>
      )}
    </>
  );
}
