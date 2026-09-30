import { Fragment, type ReactNode } from "react";
import { useCluster, useMe } from "../api/queries";
import { useAppState } from "../lib/appState";
import { ago } from "../lib/format";
import { displayKeys, hint } from "../lib/keys";
import { ClusterSwitch } from "./ClusterSwitch";
import { Icon } from "./Icon";
import { Keys } from "./Status";

interface ScreenProps {
  /** Breadcrumb items: links, or strings for the current page. Separators are added. */
  crumbs: ReactNode[];
  /** The cluster this screen is about, for the compact switcher and the disconnected banner. */
  cluster?: string;
  aside?: ReactNode;
  children: ReactNode;
  /** The page body does not scroll itself (the resource list scrolls inside). */
  fill?: boolean;
}

/**
 * One screen of the shell: top bar, page body and an optional right-hand
 * panel. It renders grid children of `.shell`, so the shell's columns
 * adapt to whether an aside is present.
 */
export function Screen({ crumbs, cluster: clusterName, aside, children, fill }: ScreenProps) {
  const cluster = useCluster(clusterName);
  const { data: me } = useMe();
  const { openPalette, pane, setPane, ask } = useAppState();
  const aiOn = Boolean(me?.features.ai);

  return (
    <>
      <main className="content">
        <header className="top">
          <ClusterSwitch cluster={cluster} />
          <nav className="crumbs" aria-label="Breadcrumb">
            {crumbs.map((c, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: breadcrumbs are positional
              <Fragment key={i}>
                {i > 0 && <Icon name="chev" className="i sep" />}
                {typeof c === "string" ? (
                  <span className="cur" aria-current={i === crumbs.length - 1 ? "page" : undefined}>
                    {c}
                  </span>
                ) : (
                  c
                )}
              </Fragment>
            ))}
          </nav>
          <button
            type="button"
            className="search"
            aria-label="Search every cluster"
            onClick={() => openPalette()}
          >
            <Icon name="search" />
            <span className="t">Search every cluster</span>
            <Keys keys={displayKeys("$mod+k")} />
          </button>
          {aside !== undefined && (
            <button
              type="button"
              className="aibtn"
              aria-pressed={pane === "ai"}
              disabled={!aiOn}
              title={aiOn ? "Ask AI" : "Ask AI is turned off on this hub"}
              onClick={() => (pane === "ai" ? setPane("details") : ask())}
            >
              <Icon name="spark" />
              Ask AI <Keys keys={hint("ask")} />
            </button>
          )}
        </header>
        {cluster && !cluster.connected && (
          <div className="banner warn" role="status">
            <Icon name="alert" />
            {cluster.name} is disconnected
            <span>
              Its agent was last seen {ago(cluster.lastSeen)}. Data and actions are unavailable until it
              reconnects.
            </span>
          </div>
        )}
        <section className={`page${fill ? " fill" : ""}`}>{children}</section>
      </main>
      {aside !== undefined && (
        <aside className="aside" aria-label={pane === "ai" ? "Ask AI" : "Details"}>
          {aside}
        </aside>
      )}
    </>
  );
}
