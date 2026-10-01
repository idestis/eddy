import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import {
  attentionQuery,
  type FleetResource,
  useCanAddCluster,
  useFleetResources,
  useMe,
} from "../../api/queries";
import type { ClusterInfo, Finding } from "../../api/types";
import { AddClusterDialog } from "../../components/AddClusterDialog";
import { FactList } from "../../components/ClusterCards";
import { ClusterTile, LockBadge, PinButton } from "../../components/ClusterSwitch";
import { ConnectionDialog, useConnection } from "../../components/ConnectionDialog";
import { Empty } from "../../components/Empty";
import { Icon } from "../../components/Icon";
import { PageHead } from "../../components/PageHead";
import { Screen } from "../../components/Screen";
import { SkeletonText } from "../../components/Skeleton";
import { Health, KeyHint, StatusPill } from "../../components/Status";
import { clusterStyle } from "../../lib/clusterColor";
import { warningFindings } from "../../lib/findings";
import { age, ago, STATUS_RANK } from "../../lib/format";
import { useKeys } from "../../lib/keys";
import { isFlux, kindInfo } from "../../lib/kinds";
import { detailLink, jobsLink } from "../../lib/links";
import { useOrderedClusters } from "../../lib/prefs";
import { needsAttention } from "../../lib/resourceRows";

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
      <div className="flex items-center gap-[11px]">
        <Link
          to="/c/$cluster"
          params={{ cluster: c.name }}
          className="flex min-w-0 flex-1 items-center gap-[11px] no-underline after:absolute after:inset-0 after:content-['']"
          aria-label={`Open ${c.name}`}
        >
          <ClusterTile cluster={c} />
          <span className="flex min-w-0 flex-1 flex-col justify-center">
            <span className="flex min-w-0 items-center gap-1.5 text-15 font-semibold">
              <span className="truncate">{c.displayName || c.name}</span>
              {c.protected && <LockBadge />}
            </span>
            <span className="truncate text-12 text-ink-3">
              {[c.environment, c.region].filter(Boolean).join(", ")}
            </span>
          </span>
        </Link>
        <span className="relative z-[1] flex shrink-0 items-center gap-1">
          {index < 9 && <kbd>{index + 1}</kbd>}
          <PinButton cluster={c} />
        </span>
      </div>
      <FactList
        rows={[
          ["Health", <Health key="h" cluster={c} />],
          [
            "Resources",
            c.countsPending ? (
              <SkeletonText key="n" className="w-10" label="Counting" />
            ) : c.connected || c.stale ? (
              total
            ) : (
              "–"
            ),
          ],
          ["Flux", c.fluxVersion ?? "–"],
          ["Kubernetes", c.kubernetesVersion ?? "–"],
        ]}
      />
      <div className="flex min-h-8 items-center gap-2">
        <span
          className={`inline-flex min-w-0 items-center gap-1.5 text-12 ${c.connected ? "text-ink-3" : "text-bad"}`}
        >
          <span className={`size-2 shrink-0 rounded-full ${c.connected ? "bg-ok" : "bg-bad"}`} />
          <span className="truncate">
            {c.connected
              ? `Connected, agent ${c.agentVersion ?? ""}`
              : c.lastSeen
                ? `${c.stale ? "Stale" : "Disconnected"} · last seen ${ago(c.lastSeen)}`
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

interface FleetFinding {
  cluster: string;
  finding: Finding;
  stale?: boolean;
}

type FleetAttention = FleetResource & { stale?: boolean };

function UnhealthyTable({
  items,
  findings,
  clusters,
}: {
  items: FleetAttention[];
  findings: FleetFinding[];
  clusters: Map<string, ClusterInfo>;
}) {
  const navigate = useNavigate();
  if (!items.length && !findings.length) {
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
          {items.map(({ cluster, resource: r, stale }) => (
            <tr
              key={`${cluster}/${r.id}`}
              className={`group cursor-pointer hover:bg-surface-sunken ${stale ? "opacity-60" : ""}`}
              title={stale ? `${cluster} is disconnected; this is its last known state` : undefined}
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
          {findings.map(({ cluster, finding: f }) => (
            <tr
              key={`${cluster}/${f.id}`}
              className="group cursor-pointer hover:bg-surface-sunken"
              onClick={() => void navigate(jobsLink(cluster, f.namespace))}
            >
              <td className={TD}>
                <span className="ctag" style={clusterStyle(clusters.get(cluster))}>
                  {cluster}
                </span>
              </td>
              <td className={TD}>
                <Link
                  {...jobsLink(cluster, f.namespace)}
                  className="font-mono text-12-5 no-underline hover:underline"
                >
                  <span className="text-ink-3">{kindInfo("Job").abbr} </span>
                  <span className="text-ink-3">{f.namespace} / </span>
                  finished Jobs
                </Link>
              </td>
              <td className={TD}>
                <span className="inline-flex items-center gap-1.5 text-12-5 font-medium whitespace-nowrap text-warn">
                  <Icon name="alert" className="size-4 shrink-0" />
                  Warning
                </span>
              </td>
              <td className={`${TD} max-w-0 truncate text-ink-2`} title={f.recommendation ?? f.message}>
                {f.message}
              </td>
              <td className={`${TD} text-right text-ink-3 tabular-nums`}>{age(f.jobs?.newest)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * Fleet-wide "needs attention": GET /attention, kept live by the stream's `attention` events
 * (ADR-0006), so no cluster's snapshot is loaded. A hub without the endpoint gets the old
 * way: every cluster's snapshot, filtered here.
 */
function useFleetAttention(clusters: readonly ClusterInfo[]) {
  const { data, isPending } = useQuery(attentionQuery(""));
  const fallback = data === null;
  const { items: all, loading } = useFleetResources(fallback);
  const rank = useMemo(() => new Map(clusters.map((c, i) => [c.name, i])), [clusters]);
  const items = useMemo((): FleetAttention[] => {
    if (data) return data.items;
    return all
      .filter(({ resource: r }) => isFlux(r.kind) && needsAttention(r))
      .sort(
        (a, b) =>
          STATUS_RANK[a.resource.status] - STATUS_RANK[b.resource.status] ||
          (rank.get(a.cluster) ?? 0) - (rank.get(b.cluster) ?? 0),
      );
  }, [data, all, rank]);
  const findings = useMemo(
    (): FleetFinding[] =>
      data
        ? data.findings
        : clusters.flatMap((c) => warningFindings(c).map((finding) => ({ cluster: c.name, finding }))),
    [data, clusters],
  );
  return {
    items,
    findings,
    total: (data ? Math.max(data.total, data.items.length) : items.length) + findings.length,
    loading: isPending || (fallback && loading),
  };
}

function FleetPage() {
  const clusters = useOrderedClusters();
  const byName = useMemo(() => new Map(clusters.map((c) => [c.name, c])), [clusters]);
  const { items: unhealthy, findings, total, loading } = useFleetAttention(clusters);
  const disconnected = clusters.filter((c) => !c.connected).length;
  const { data: me } = useMe();
  const onboarding = Boolean(me?.features.onboarding);
  const canAdd = useCanAddCluster();
  const [adding, setAdding] = useState(false);
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
      {adding && <AddClusterDialog onClose={() => setAdding(false)} />}
      <h3 className="mt-2 flex items-center gap-2 text-14 font-semibold">
        Needs attention across the fleet{" "}
        <span className="font-normal text-ink-3">{loading ? "loading…" : total}</span>
      </h3>
      <UnhealthyTable items={unhealthy} findings={findings} clusters={byName} />
    </Screen>
  );
}
