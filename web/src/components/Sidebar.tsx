import { useQuery } from "@tanstack/react-query";
import { Link, useLocation, useSearch } from "@tanstack/react-router";
import { useEffect, useMemo, useState } from "react";
import { logout } from "../api/endpoints";
import { resourcesQuery, useMe } from "../api/queries";
import { type StreamState, useStreamState } from "../api/stream";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { matchesKindFilter, NAV_TREE, type NavNode, navPath } from "../lib/kinds";
import { needsAttention, type StatusFilter } from "../lib/resourceRows";
import { toggleTheme } from "../lib/theme";
import { getViewPrefs, setViewPrefs } from "../lib/viewPrefs";
import { ClusterSwitch } from "./ClusterSwitch";
import { Icon, type IconName } from "./Icon";
import { type NavLinkProps, NavTree } from "./NavTree";

export function EddyMark({ className = "size-[22px]" }: { className?: string }) {
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.2"
      strokeLinecap="round"
      aria-hidden="true"
    >
      <path d="M12 3a9 9 0 1 0 9 9 6 6 0 0 0-6-6 4 4 0 0 0-4 4 2 2 0 0 0 2 2" />
    </svg>
  );
}

const LIVE: Record<StreamState, { label: string; dot: string; title: string }> = {
  live: {
    label: "Live",
    dot: "bg-ok motion-safe:animate-live",
    title: "Live updates are streaming",
  },
  reconnecting: {
    label: "Reconnecting…",
    dot: "bg-warn motion-safe:animate-blink",
    title: "The live stream dropped; reconnecting",
  },
  connecting: { label: "Offline", dot: "bg-off", title: "Not connected to live updates yet" },
};

/** The live-updates indicator: pulses while live (not under reduced motion), amber while reconnecting. */
export function LiveIndicator() {
  const stream = useStreamState();
  const s = LIVE[stream];
  return (
    <span
      className="ml-auto inline-flex items-center gap-1.5 text-12 text-ink-3"
      role="status"
      title={s.title}
      data-stream={stream}
    >
      <span className={`size-2 rounded-full ${s.dot}`} />
      {s.label}
    </span>
  );
}

const NAV_LINK =
  "nav-link flex h-[34px] min-w-0 shrink-0 items-center gap-2.5 rounded-control px-2.5 text-13-5 text-ink-2 no-underline hover:bg-surface-sunken hover:text-ink aria-[current=page]:bg-c-soft aria-[current=page]:font-semibold aria-[current=page]:text-ink aria-[current=page]:[&_svg]:text-c";

function Count({ n, bad }: { n: number | undefined; bad?: boolean }) {
  if (n === undefined) return null;
  return (
    <span
      className={`ml-auto pl-2 text-12 tabular-nums ${bad && n > 0 ? "font-semibold text-attn" : n === 0 ? "text-ink-3/60" : "text-ink-3"}`}
    >
      {n}
    </span>
  );
}

const OPEN_KEY = "eddy.nav.open";

function loadOpen(): Record<string, boolean> {
  try {
    return JSON.parse(localStorage.getItem(OPEN_KEY) ?? "{}") as Record<string, boolean>;
  } catch {
    return {};
  }
}

function ClusterNav({ cluster }: { cluster: ClusterInfo }) {
  const { data } = useQuery({ ...resourcesQuery(cluster.name), enabled: cluster.connected });
  const search = useSearch({ strict: false }) as { kind?: string; status?: StatusFilter };
  const onList = useLocation({ select: (l) => l.pathname === `/c/${encodeURIComponent(cluster.name)}` });
  const items = data?.items;
  const params = { cluster: cluster.name };
  const activePath = useMemo(() => (onList ? navPath(search.kind) : []), [onList, search.kind]);
  // Collapsed state the user chose; a group is open by default while it holds the active page.
  // Saved with the view preferences, so it follows the user; the old key seeds it once.
  const [open, setOpen] = useState<Record<string, boolean>>(() => getViewPrefs().nav ?? loadOpen());
  useEffect(() => {
    setViewPrefs({ nav: open });
  }, [open]);

  const counts = useMemo(() => {
    const byId = new Map<string, number>();
    const walk = (nodes: readonly NavNode[]) => {
      for (const n of nodes) {
        byId.set(n.id, (items ?? []).filter((r: Resource) => matchesKindFilter(r.kind, n.id)).length);
        if (n.children) walk(n.children);
      }
    };
    walk(NAV_TREE);
    return byId;
  }, [items]);
  const attention = useMemo(() => (items ?? []).filter(needsAttention).length, [items]);

  const link = (
    key: string,
    label: string,
    icon: IconName,
    s: { kind?: string; status?: StatusFilter },
    n: number | undefined,
    bad = false,
    props?: NavLinkProps,
  ) => {
    const active = onList && search.kind === s.kind && search.status === s.status;
    return (
      <Link
        key={key}
        to="/c/$cluster"
        params={params}
        search={s}
        // Exact search matching keeps the router from marking "All" active for every filter.
        activeOptions={{ exact: true }}
        aria-current={active ? "page" : undefined}
        className={NAV_LINK}
        {...props}
      >
        <Icon name={icon} />
        <span className="truncate">{label}</span>
        <Count n={items ? n : undefined} bad={bad} />
      </Link>
    );
  };

  return (
    <nav className="mt-4 flex flex-col gap-px" aria-label={`Browse ${cluster.name}`}>
      <h3 className="mx-2.5 mb-1.5 text-12 font-medium text-ink-3">Browse</h3>
      {link("all", "All resources", "list", {}, items?.length)}
      {link("attention", "Needs attention", "alert", { status: "attention" }, attention, true)}
      <div className="mt-1">
        <NavTree
          nodes={NAV_TREE}
          isOpen={(n) => open[n.id] ?? activePath.includes(n.id)}
          setOpen={(id, on) => setOpen((o) => ({ ...o, [id]: on }))}
          skip={(n) => n.id === "other" && (counts.get(n.id) ?? 0) === 0}
          renderLink={(n, props) =>
            link(n.id, n.label, n.icon, { kind: n.id }, counts.get(n.id) ?? 0, false, props)
          }
        />
      </div>
    </nav>
  );
}

const FLEET_LINK = NAV_LINK;

export function Sidebar({ cluster }: { cluster: ClusterInfo | undefined }) {
  const { data: me } = useMe();
  const { setHelp } = useAppState();

  return (
    <aside
      className="flex min-h-0 w-[260px] shrink-0 flex-col overflow-y-auto border-r border-line bg-surface-side px-3 pt-3.5 pb-3 max-[1180px]:hidden"
      aria-label="Navigation"
    >
      <Link
        to="/"
        className="flex items-center gap-[9px] px-2 pt-1 pb-3.5 text-17 font-semibold tracking-tight no-underline [&_svg]:text-c"
        aria-label="Eddy home"
      >
        <EddyMark />
        eddy
      </Link>
      <ClusterSwitch cluster={cluster} />
      {cluster && <ClusterNav cluster={cluster} />}
      <nav className="mt-4 flex flex-col gap-px" aria-label="Fleet">
        <h3 className="mx-2.5 mb-1.5 text-12 font-medium text-ink-3">Fleet</h3>
        <Link to="/fleet" activeOptions={{ exact: true }} className={FLEET_LINK}>
          <Icon name="globe" />
          Overview
        </Link>
        <Link to="/threads" className={FLEET_LINK}>
          <Icon name="chat" />
          Threads
        </Link>
        <Link to="/audit" className={FLEET_LINK}>
          <Icon name="clock" />
          Audit log
        </Link>
        <Link to="/settings/tokens" className={FLEET_LINK}>
          <Icon name="key" />
          Access tokens
        </Link>
      </nav>
      <div className="mt-auto flex flex-col gap-2.5 border-t border-line px-1 pt-3">
        <div className="flex min-w-0 items-center gap-2.5">
          <span
            className="flex size-8 shrink-0 items-center justify-center rounded-full border border-line bg-surface-sunken text-13 font-semibold uppercase"
            aria-hidden="true"
          >
            {(me?.display ?? "?").charAt(0)}
          </span>
          <span className="flex min-w-0 flex-col text-12-5">
            <b className="truncate font-semibold">{me?.display}</b>
            <span className="truncate text-ink-3" title={me?.groups.join(", ")}>
              {me?.user}
            </span>
          </span>
        </div>
        <div className="flex items-center gap-1">
          <button
            type="button"
            className="ib"
            aria-label="Toggle theme"
            title="Toggle theme"
            onClick={() => toggleTheme()}
          >
            <Icon name="moon" />
          </button>
          <button
            type="button"
            className="ib"
            aria-label="Keyboard shortcuts"
            title="Keyboard shortcuts (?)"
            onClick={() => setHelp(true)}
          >
            <Icon name="keyboard" />
          </button>
          <button
            type="button"
            className="ib"
            aria-label="Sign out"
            title="Sign out"
            onClick={async () => {
              await logout().catch(() => undefined);
              window.location.assign("/login");
            }}
          >
            <Icon name="logout" />
          </button>
          <LiveIndicator />
        </div>
      </div>
    </aside>
  );
}
