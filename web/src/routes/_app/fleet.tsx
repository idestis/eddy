import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import {
  type FleetResource,
  useCanAddCluster,
  useClusters,
  useFleetResources,
  useMe,
} from "../../api/queries";
import type { ClusterInfo } from "../../api/types";
import { AddClusterDialog } from "../../components/AddClusterDialog";
import { FactList } from "../../components/ClusterCards";
import { ClusterTile, LockBadge } from "../../components/ClusterSwitch";
import { ConnectionDialog, useConnection } from "../../components/ConnectionDialog";
import { Empty } from "../../components/Empty";
import { Icon } from "../../components/Icon";
import { PageHead } from "../../components/PageHead";
import { Screen } from "../../components/Screen";
import { Health, KeyHint, StatusPill } from "../../components/Status";
import { clusterStyle } from "../../lib/clusterColor";
import { age, ago, STATUS_RANK } from "../../lib/format";
import { useKeys } from "../../lib/keys";
import { isFlux, kindInfo } from "../../lib/kinds";
import { detailLink } from "../../lib/links";

export const Route = createFileRoute("/_app/fleet")({
  component: FleetPage,
});

function ClusterCard({ c, index, onboarding }: { c: ClusterInfo; index: number; onboarding: boolean }) {
  const total = Object.values(c.counts ?? {}).reduce((a, b) => a + (b ?? 0), 0);
  const connection = useConnection(c.name, onboarding);
  const [open, setOpen] = useState(false);
  return (
    <div
      // Protected clusters: a thicker solid top border in the cluster colour, plus the lock badge.
      className={`relative flex min-w-0 flex-col gap-3 overflow-hidden rounded-card border border-line bg-surface p-4 has-[a:hover]:border-x-cc/45 has-[a:hover]:border-b-cc/45 ${c.protected ? "border-t-[5px] border-t-cc pt-3.5" : "border-t-[3px] border-t-cc pt-[15px]"}`}
      style={clusterStyle(c)}
    >
      <Link
        to="/c/$cluster"
        params={{ cluster: c.name }}
        className="flex min-w-0 flex-col gap-3 no-underline after:absolute after:inset-0 after:content-['']"
        aria-label={`Open ${c.name}`}
      >
        <div className="grid grid-cols-[38px_minmax(0,1fr)_auto] items-center gap-x-[11px]">
          <span className="row-span-2">
            <ClusterTile cluster={c} />
          </span>
          <span className="flex min-w-0 items-center gap-1.5 text-15 font-semibold">
            <span className="truncate">{c.displayName || c.name}</span>
            {c.protected && <LockBadge />}
          </span>
          <span className="col-start-3 row-span-2 row-start-1">{index < 9 && <kbd>{index + 1}</kbd>}</span>
          <span className="col-start-2 truncate text-12 text-ink-3">
            {[c.environment, c.region].filter(Boolean).join(", ")}
          </span>
        </div>
        <FactList
          rows={[
            ["Health", <Health key="h" cluster={c} />],
            ["Resources", c.connected ? total : "–"],
            ["Flux", c.fluxVersion ?? "–"],
            ["Kubernetes", c.kubernetesVersion ?? "–"],
          ]}
        />
      </Link>
      <div className="flex min-h-8 items-center gap-2">
        <span
          className={`inline-flex min-w-0 items-center gap-1.5 text-12 ${c.connected ? "text-ink-3" : "text-bad"}`}
        >
          <span className={`size-2 shrink-0 rounded-full ${c.connected ? "bg-ok" : "bg-bad"}`} />
          <span className="truncate">
            {c.connected
              ? `Connected, agent ${c.agentVersion ?? ""}`
              : c.lastSeen
                ? `Disconnected, last seen ${ago(c.lastSeen)}`
                : "Waiting for the agent to connect"}
          </span>
        </span>
        {connection && (
          <button
            type="button"
            // Above the card-wide link overlay.
            className="btn btn-sm btn-ghost relative z-[1] ml-auto"
            aria-label={`Connection of ${c.name}`}
            onClick={() => setOpen(true)}
          >
            <Icon name="plug" />
            Connection
            {connection.attempts.length > 0 && !c.connected && (
              <span className="badge text-bad">{connection.attempts.length}</span>
            )}
          </button>
        )}
      </div>
      {open && <ConnectionDialog cluster={c} onClose={() => setOpen(false)} />}
    </div>
  );
}

const TH =
  "border-b border-line bg-surface-side px-3.5 py-[9px] text-left text-12 font-medium whitespace-nowrap text-ink-3";
const TD = "border-b border-line px-3.5 py-2.5 align-middle group-last:border-b-0";

function UnhealthyTable({ items, clusters }: { items: FleetResource[]; clusters: Map<string, ClusterInfo> }) {
  const navigate = useNavigate();
  if (!items.length) {
    return <Empty title="Nothing needs attention">Every Flux object you can see is ready.</Empty>;
  }
  return (
    <div className="overflow-hidden rounded-card border border-line bg-surface">
      <table className="w-full border-separate border-spacing-0 text-13">
        <thead>
          <tr>
            <th className={`${TH} w-[140px]`}>Cluster</th>
            <th className={`${TH} w-[32%]`}>Resource</th>
            <th className={`${TH} w-[130px]`}>Status</th>
            <th className={TH}>Message</th>
            <th className={`${TH} w-[90px] text-right`}>Changed</th>
          </tr>
        </thead>
        <tbody>
          {items.map(({ cluster, resource: r }) => (
            <tr
              key={`${cluster}/${r.id}`}
              className="group cursor-pointer hover:bg-surface-sunken"
              onClick={() => void navigate(detailLink(cluster, r))}
            >
              <td className={TD}>
                <span className="ctag" style={clusterStyle(clusters.get(cluster))}>
                  {cluster}
                </span>
              </td>
              <td className={TD}>
                <Link
                  {...detailLink(cluster, r)}
                  className="font-mono text-12-5 no-underline hover:underline"
                >
                  <span className="text-ink-3">{kindInfo(r.kind).abbr} </span>
                  {r.namespace ? <span className="text-ink-3">{r.namespace} / </span> : ""}
                  {r.name}
                </Link>
              </td>
              <td className={TD}>
                <StatusPill status={r.status} />
              </td>
              <td className={`${TD} max-w-0 truncate text-ink-2`} title={r.message}>
                {r.message}
              </td>
              <td className={`${TD} text-right text-ink-3 tabular-nums`}>
                {age(r.lastChanged ?? r.createdAt)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function FleetPage() {
  const { data: clusters = [] } = useClusters();
  const { items, loading } = useFleetResources();
  const byName = useMemo(() => new Map(clusters.map((c) => [c.name, c])), [clusters]);
  const unhealthy = useMemo(
    () =>
      items
        .filter(({ resource: r }) => isFlux(r.kind) && r.status !== "ready" && !r.inventoryOnly)
        .sort(
          (a, b) =>
            STATUS_RANK[a.resource.status] - STATUS_RANK[b.resource.status] ||
            (byName.get(a.cluster)?.order ?? 0) - (byName.get(b.cluster)?.order ?? 0),
        ),
    [items, byName],
  );
  const disconnected = clusters.filter((c) => !c.connected).length;
  const { data: me } = useMe();
  const onboarding = Boolean(me?.features.onboarding);
  const canAdd = useCanAddCluster();
  const [adding, setAdding] = useState(false);
  const nextOrder = clusters.reduce((max, c) => Math.max(max, c.order), 0) + 1;
  useKeys({ addCluster: canAdd && (() => setAdding(true)) });

  return (
    <Screen crumbs={["Fleet"]} title="fleet">
      <div className="flex items-start gap-3">
        <PageHead title="Fleet">
          {clusters.length} clusters{disconnected ? `, ${disconnected} disconnected` : ""}. Press a number to
          jump into one, or <kbd>⌘K</kbd> to search every cluster.
        </PageHead>
        {canAdd && (
          <button type="button" className="btn btn-primary ml-auto shrink-0" onClick={() => setAdding(true)}>
            <Icon name="plug" />
            Add cluster
            <KeyHint id="addCluster" />
          </button>
        )}
      </div>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(300px,1fr))] gap-3.5">
        {clusters.map((c, i) => (
          <ClusterCard key={c.name} c={c} index={i} onboarding={onboarding} />
        ))}
      </div>
      {adding && <AddClusterDialog defaultOrder={nextOrder} onClose={() => setAdding(false)} />}
      <h3 className="mt-2 flex items-center gap-2 text-14 font-semibold">
        Needs attention across the fleet{" "}
        <span className="font-normal text-ink-3">{loading ? "loading…" : unhealthy.length}</span>
      </h3>
      <UnhealthyTable items={unhealthy} clusters={byName} />
    </Screen>
  );
}
