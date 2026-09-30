import { useNavigate } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { ClusterInfo } from "../api/types";
import { useAppState } from "../lib/appState";
import { clusterStyle } from "../lib/clusterColor";
import { Icon } from "./Icon";
import { Modal } from "./Modal";
import { Health, KeyHint } from "./Status";

const initial = (c: ClusterInfo) => (c.displayName || c.name).charAt(0).toUpperCase();

/**
 * A cluster's identity tile. Protected clusters get a solid colour (no gradient, never
 * stripes); the "Protected" lock badge next to the name carries the meaning.
 */
export function ClusterTile({
  cluster,
  size = "md",
  children,
}: {
  cluster?: ClusterInfo;
  size?: "sm" | "md";
  children?: ReactNode;
}) {
  const box = size === "sm" ? "size-[30px] rounded-[9px] text-13" : "size-[38px] rounded-tile text-15";
  const fill = !cluster
    ? "bg-surface-sunken text-ink-2 border border-line"
    : cluster.protected
      ? "bg-cc text-c-ink"
      : "bg-linear-135 from-cc to-cc2 text-c-ink";
  return (
    <span
      className={`flex shrink-0 items-center justify-center font-bold ${box} ${fill}`}
      style={cluster ? clusterStyle(cluster) : undefined}
      aria-hidden="true"
    >
      {children ?? (cluster ? initial(cluster) : <Icon name="globe" />)}
    </span>
  );
}

export function LockBadge() {
  return (
    <span className="inline-flex items-center gap-[3px] whitespace-nowrap rounded-md bg-cc/12 px-1.5 py-0.5 text-10-5 font-semibold text-cc [&_svg]:size-[11px]">
      <Icon name="lock" />
      Protected
    </span>
  );
}

/** The cluster identity button (sidebar, and the compact top bar on narrow screens). */
export function ClusterSwitch({
  cluster,
  compact = false,
  className = "",
}: {
  cluster: ClusterInfo | undefined;
  compact?: boolean;
  className?: string;
}) {
  const { openClusterMenu, clusterMenu } = useAppState();
  return (
    <button
      type="button"
      className={`grid w-full grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2.5 rounded-[14px] border border-line bg-surface text-left shadow-control hover:border-c/45 ${compact ? "max-w-[230px] py-[5px] pr-[9px] pl-[5px]" : "py-[9px] pr-2.5 pl-[9px]"} ${className}`}
      style={cluster ? clusterStyle(cluster) : undefined}
      aria-haspopup="dialog"
      aria-expanded={Boolean(clusterMenu)}
      aria-label={cluster ? `Cluster ${cluster.name}. Switch cluster` : "Choose a cluster"}
      onClick={(e) => openClusterMenu(e.currentTarget)}
    >
      <span className="row-span-2">
        <ClusterTile cluster={cluster} size={compact ? "sm" : "md"} />
      </span>
      <span className={`flex min-w-0 items-center gap-1.5 font-semibold ${compact ? "text-14" : "text-15"}`}>
        <span className="truncate">{cluster ? cluster.displayName || cluster.name : "Fleet"}</span>
        {cluster?.protected && <LockBadge />}
      </span>
      {!compact && (
        <span className="col-start-2 truncate text-12 text-ink-3">
          {cluster ? [cluster.environment, cluster.region].filter(Boolean).join(", ") : "All clusters"}
        </span>
      )}
      <span className="col-start-3 row-span-2 row-start-1 flex text-ink-3">
        <Icon name="updown" />
      </span>
    </button>
  );
}

const ROW =
  "grid w-full min-h-[58px] grid-cols-[38px_minmax(0,1fr)_auto] content-center items-center gap-x-[11px] rounded-xl px-2.5 py-[9px] text-left no-underline hover:bg-surface-sunken aria-[current=true]:bg-surface-sunken";

/** The cluster menu: a popover anchored to the switcher that opened it. */
export function ClusterMenu({ clusters, current }: { clusters: ClusterInfo[]; current: string | undefined }) {
  const { clusterMenu, closeClusterMenu } = useAppState();
  const navigate = useNavigate();
  const go = (to: () => void) => {
    closeClusterMenu();
    to();
  };
  return (
    <Modal
      label="Switch cluster"
      onClose={closeClusterMenu}
      placement="anchor"
      anchor={clusterMenu}
      className="w-[min(380px,calc(100vw-16px))]! p-2"
    >
      <div className="overflow-y-auto">
        <button
          type="button"
          className={ROW}
          aria-current={current === undefined}
          onClick={() => go(() => void navigate({ to: "/fleet" }))}
        >
          <ClusterTile>
            <Icon name="globe" className="size-[18px]" />
          </ClusterTile>
          <span className="font-semibold">Fleet overview</span>
          <span className="col-start-3 row-span-2 row-start-1">
            <KeyHint id="fleet" />
          </span>
          <span className="col-start-2 text-12 text-ink-3">Every cluster at a glance</span>
        </button>
        <div className="mx-2 my-1.5 h-px bg-line" role="presentation" />
        {clusters.map((c, i) => (
          <button
            type="button"
            key={c.name}
            className={ROW}
            style={clusterStyle(c)}
            aria-current={c.name === current}
            onClick={() => go(() => void navigate({ to: "/c/$cluster", params: { cluster: c.name } }))}
          >
            <ClusterTile cluster={c} />
            <span className="flex min-w-0 items-center gap-1.5 font-semibold">
              <span className="truncate">{c.displayName || c.name}</span>
              {c.protected && <LockBadge />}
            </span>
            <span className="col-start-3 row-span-2 row-start-1 flex items-center gap-1.5">
              <Health cluster={c} />
              {i < 9 && <kbd>{i + 1}</kbd>}
            </span>
            <span className="col-start-2 truncate text-12 text-ink-3">
              {[c.environment, c.region].filter(Boolean).join(", ")}
              {!c.connected && <span className="text-bad"> · disconnected</span>}
            </span>
          </button>
        ))}
      </div>
    </Modal>
  );
}
