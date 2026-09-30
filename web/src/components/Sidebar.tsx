import { useQuery } from "@tanstack/react-query";
import { Link, useLocation, useSearch } from "@tanstack/react-router";
import { useMemo } from "react";
import { logout } from "../api/endpoints";
import { resourcesQuery, useMe } from "../api/queries";
import { useStreamState } from "../api/stream";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { kindInfo, NAV_GROUPS } from "../lib/kinds";
import type { StatusFilter } from "../lib/resourceRows";
import { toggleTheme } from "../lib/theme";
import { ClusterSwitch } from "./ClusterSwitch";
import { Icon, type IconName } from "./Icon";

const LIVE_LABEL = { live: "Live", connecting: "Connecting…", reconnecting: "Reconnecting…" } as const;

function countBy(items: Resource[]) {
  const nav = new Map<string, number>();
  let attention = 0;
  for (const r of items) {
    const g = kindInfo(r.kind).nav;
    nav.set(g, (nav.get(g) ?? 0) + 1);
    if (r.status !== "ready") attention++;
  }
  return { nav, attention };
}

function ClusterNav({ cluster }: { cluster: ClusterInfo }) {
  const { data } = useQuery({ ...resourcesQuery(cluster.name), enabled: cluster.connected });
  const search = useSearch({ strict: false }) as { kind?: string; status?: StatusFilter };
  const onList = useLocation({ select: (l) => l.pathname === `/c/${encodeURIComponent(cluster.name)}` });
  const counts = useMemo(() => countBy(data?.items ?? []), [data]);
  const params = { cluster: cluster.name };

  const item = (
    key: string,
    label: string,
    icon: IconName,
    s: { kind?: string; status?: StatusFilter },
    n?: number,
    bad = false,
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
      >
        <Icon name={icon} />
        {label}
        {n !== undefined && <span className={`n${bad && n > 0 ? " bad" : ""}`}>{n}</span>}
      </Link>
    );
  };

  return (
    <nav className="nav" aria-label={`Browse ${cluster.name}`}>
      <h3>Browse</h3>
      {item("all", "All resources", "grid", {}, data?.items.length)}
      {item("attention", "Needs attention", "alert", { status: "attention" }, counts.attention, true)}
      {NAV_GROUPS.map((g) => item(g.id, g.label, g.icon, { kind: g.id }, counts.nav.get(g.id) ?? 0))}
    </nav>
  );
}

export function Sidebar({ cluster }: { cluster: ClusterInfo | undefined }) {
  const { data: me } = useMe();
  const stream = useStreamState();
  const { setHelp } = useAppState();

  return (
    <aside className="side" aria-label="Navigation">
      <Link to="/" className="logo" aria-label="Eddy home">
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.2"
          strokeLinecap="round"
          aria-hidden="true"
        >
          <path d="M12 3a9 9 0 1 0 9 9 6 6 0 0 0-6-6 4 4 0 0 0-4 4 2 2 0 0 0 2 2" />
        </svg>
        eddy
      </Link>
      <ClusterSwitch cluster={cluster} />
      {cluster && <ClusterNav cluster={cluster} />}
      <nav className="nav" aria-label="Fleet">
        <h3>Fleet</h3>
        <Link to="/fleet" activeOptions={{ exact: true }}>
          <Icon name="globe" />
          Overview
        </Link>
        <Link to="/threads">
          <Icon name="chat" />
          Threads
        </Link>
        <Link to="/audit">
          <Icon name="clock" />
          Audit log
        </Link>
        <Link to="/settings/tokens">
          <Icon name="key" />
          Access tokens
        </Link>
      </nav>
      <div className="side-foot">
        <div className="me">
          <span className="av" aria-hidden="true">
            {(me?.display ?? "?").charAt(0)}
          </span>
          <span className="mt">
            <b>{me?.display}</b>
            <span title={me?.groups.join(", ")}>{me?.user}</span>
          </span>
        </div>
        <div className="foot-row">
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
          <span
            className={`live live-${stream === "live" ? "on" : stream}`}
            role="status"
            title="Live updates"
          >
            <span className="dot" />
            {LIVE_LABEL[stream]}
          </span>
        </div>
      </div>
    </aside>
  );
}
