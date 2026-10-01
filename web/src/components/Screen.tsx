import { Fragment, type KeyboardEvent, type PointerEvent, type ReactNode, useEffect, useState } from "react";
import { useCluster, useMe } from "../api/queries";
import { useAppState } from "../lib/appState";
import { ago } from "../lib/format";
import { displayKeys } from "../lib/keys";
import { useTitle } from "../lib/title";
import { setViewPrefs, useViewPrefs } from "../lib/viewPrefs";
import { useRouteCluster } from "./AppShell";
import { ClusterSwitch } from "./ClusterSwitch";
import { Icon } from "./Icon";
import { KeyHint, Keys } from "./Status";

interface ScreenProps {
  /** Breadcrumb items: links, or strings for the current page. Separators are added. */
  crumbs: ReactNode[];
  /** The resource or page for the browser tab title; the cluster and "eddy" are appended. */
  title?: string;
  /** The cluster this screen is about, for the compact switcher and the disconnected banner. */
  cluster?: string;
  aside?: ReactNode;
  children: ReactNode;
  /** The page body does not scroll itself (the resource list scrolls inside). */
  fill?: boolean;
  /** Content width: "full" uses the whole column, "wide" and "narrow" cap long lines. */
  width?: "full" | "wide" | "narrow";
}

const WIDTH = { full: "", wide: "max-w-[1400px]", narrow: "max-w-[960px]" } as const;

const ASIDE_KEY = "eddy.aside.width";
const ASIDE_MIN = 360;
const ASIDE_MAX = 760;
const ASIDE_DEFAULT = 460;

function loadWidth(): number {
  try {
    const n = Number(localStorage.getItem(ASIDE_KEY));
    return n >= ASIDE_MIN && n <= ASIDE_MAX ? n : ASIDE_DEFAULT;
  } catch {
    return ASIDE_DEFAULT;
  }
}

/** The right-hand panel's width, saved with the view preferences (lib/viewPrefs.ts). */
function useAsideWidth() {
  const saved = useViewPrefs().asideWidth;
  const [width, setWidth] = useState(() =>
    saved && saved >= ASIDE_MIN && saved <= ASIDE_MAX ? saved : loadWidth(),
  );
  // Save once a drag settles, not on every pointer move.
  useEffect(() => {
    const t = setTimeout(() => setViewPrefs({ asideWidth: width }), 400);
    return () => clearTimeout(t);
  }, [width]);
  const clamp = (n: number) => Math.round(Math.max(ASIDE_MIN, Math.min(ASIDE_MAX, n)));
  return [width, (n: number) => setWidth(clamp(n))] as const;
}

/** A drag handle on the aside's left edge. Arrow keys resize by 24px; Home/End jump to the limits. */
function ResizeHandle({ width, setWidth }: { width: number; setWidth: (n: number) => void }) {
  const onPointerDown = (e: PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    const startX = e.clientX;
    const start = width;
    const el = e.currentTarget;
    el.setPointerCapture(e.pointerId);
    const move = (ev: globalThis.PointerEvent) => setWidth(start + (startX - ev.clientX));
    const up = () => {
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const step = e.shiftKey ? 96 : 24;
    const next: Record<string, number> = {
      ArrowLeft: width + step,
      ArrowRight: width - step,
      Home: ASIDE_MAX,
      End: ASIDE_MIN,
    };
    const n = next[e.key];
    if (n === undefined) return;
    e.preventDefault();
    e.stopPropagation();
    setWidth(n);
  };
  return (
    // biome-ignore lint/a11y/useSemanticElements: a focusable, resizable separator has no semantic element
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize the side panel"
      aria-valuemin={ASIDE_MIN}
      aria-valuemax={ASIDE_MAX}
      aria-valuenow={width}
      tabIndex={0}
      className="group absolute inset-y-0 -left-[3px] z-10 w-[7px] cursor-col-resize touch-none rounded-none focus-visible:outline-none"
      onPointerDown={onPointerDown}
      onKeyDown={onKeyDown}
      onDoubleClick={() => setWidth(ASIDE_DEFAULT)}
    >
      <span className="mx-auto block h-full w-px bg-transparent transition-colors group-hover:bg-c group-focus-visible:w-[3px] group-focus-visible:bg-c group-active:bg-c" />
    </div>
  );
}

/**
 * One screen of the shell: top bar, page body and an optional right-hand panel.
 * It renders the flex children of the shell row, so the shell adapts to whether an
 * aside is present.
 */
export function Screen({
  crumbs,
  title,
  cluster: clusterName,
  aside,
  children,
  fill,
  width = "full",
}: ScreenProps) {
  const cluster = useCluster(clusterName);
  const routeCluster = useRouteCluster();
  const { data: me } = useMe();
  const { openPalette, pane, setPane, ask } = useAppState();
  const [asideWidth, setAsideWidth] = useAsideWidth();
  const aiOn = Boolean(me?.features.ai);
  useTitle(title, clusterName);

  return (
    <>
      <main className="flex min-h-0 min-w-0 flex-1 flex-col">
        <header className="flex h-[60px] shrink-0 items-center gap-3 border-b border-line px-5 max-[859px]:h-14 max-[859px]:gap-2 max-[859px]:px-2.5">
          <ClusterSwitch
            cluster={cluster}
            compact
            className="hidden w-auto! max-[1180px]:flex max-[859px]:max-w-none max-[859px]:flex-1"
          />
          <nav
            className="no-scrollbar flex min-w-0 flex-auto items-center gap-0.5 overflow-x-auto whitespace-nowrap text-15 text-ink-3 max-[859px]:hidden [&_a]:rounded-lg [&_a]:px-[7px] [&_a]:py-[5px] [&_a:hover]:bg-surface-sunken [&_a:hover]:text-ink"
            aria-label="Breadcrumb"
          >
            {crumbs.map((c, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: breadcrumbs are positional
              <Fragment key={i}>
                {i > 0 && <Icon name="chev" className="size-[13px] shrink-0 opacity-55" />}
                {typeof c === "string" ? (
                  <span
                    className="px-[7px] py-[5px] font-semibold text-ink"
                    aria-current={i === crumbs.length - 1 ? "page" : undefined}
                  >
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
            className="flex h-[38px] min-w-[150px] flex-[0_3_340px] items-center gap-[9px] rounded-tile border border-line bg-surface-sunken pr-2 pl-3 text-left text-ink-3 hover:border-line-strong max-[859px]:w-10 max-[859px]:min-w-0 max-[859px]:flex-none max-[859px]:justify-center max-[859px]:p-0"
            aria-label={routeCluster ? `Search ${routeCluster}` : "Search every cluster"}
            onClick={() => openPalette()}
          >
            <Icon name="search" />
            <span className="flex-1 text-13-5 max-[859px]:hidden">
              {routeCluster ? `Search ${routeCluster}` : "Search every cluster"}
            </span>
            <Keys keys={displayKeys("$mod+k")} className="max-[859px]:hidden" />
          </button>
          {aside !== undefined && (
            <button
              type="button"
              className="inline-flex h-[38px] shrink-0 items-center gap-2 rounded-tile border border-c/35 bg-linear-135 from-c/12 to-c2/12 px-3 text-13-5 font-semibold text-ink disabled:cursor-not-allowed disabled:opacity-50 aria-pressed:border-c min-[860px]:hidden [&_svg]:text-c"
              aria-pressed={pane === "ai"}
              disabled={!aiOn}
              title={aiOn ? "Ask AI" : "Ask AI is turned off on this hub"}
              onClick={() => (pane === "ai" ? setPane("details") : ask())}
            >
              <Icon name="spark" />
              Ask AI <KeyHint id="ask" />
            </button>
          )}
        </header>
        {cluster && !cluster.connected && (
          <div
            className="flex shrink-0 items-center gap-2 border-b border-line bg-warn/12 px-4 py-[7px] text-12-5 font-semibold text-warn"
            role="status"
          >
            <Icon name="alert" />
            {cluster.name} is disconnected
            <span className="font-normal text-ink-2">
              Stale · last seen {ago(cluster.lastSeen)}. You see the last known state; actions are off until
              its agent reconnects.
            </span>
          </div>
        )}
        <section
          className={`flex min-h-0 flex-1 flex-col ${fill ? "overflow-hidden" : "overflow-auto"} px-5 pt-[18px] pb-7 max-[859px]:px-3 max-[859px]:pt-3 max-[859px]:pb-5`}
        >
          <div className={`mx-auto flex w-full min-h-0 flex-1 flex-col gap-4 ${WIDTH[width]}`}>
            {children}
          </div>
        </section>
      </main>
      {aside !== undefined && (
        <aside
          className={`relative flex min-h-0 min-w-0 shrink-0 flex-col border-l border-line bg-surface-side max-[1180px]:w-[380px]! ${
            // Narrow screens have no side column: Ask AI opens as a full-screen sheet instead.
            pane === "ai"
              ? "sheet-narrow max-[859px]:fixed max-[859px]:inset-0 max-[859px]:z-40 max-[859px]:w-full!"
              : "max-[859px]:hidden"
          }`}
          style={{ width: asideWidth }}
          aria-label={pane === "ai" ? "Ask AI" : "Details"}
        >
          <ResizeHandle width={asideWidth} setWidth={setAsideWidth} />
          {aside}
        </aside>
      )}
    </>
  );
}
