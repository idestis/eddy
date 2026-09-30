import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useMemo } from "react";
import { type FleetResource, useClusters, useFleetResources } from "../../api/queries";
import type { ClusterInfo } from "../../api/types";
import { Icon } from "../../components/Icon";
import { Screen } from "../../components/Screen";
import { Health, StatusPill } from "../../components/Status";
import { clusterStyle } from "../../lib/clusterColor";
import { age, ago, STATUS_RANK } from "../../lib/format";
import { isFlux, kindInfo } from "../../lib/kinds";
import { detailLink } from "../../lib/links";

export const Route = createFileRoute("/_app/fleet")({
  component: FleetPage,
});

function ClusterCard({ c, index }: { c: ClusterInfo; index: number }) {
  const total = Object.values(c.counts ?? {}).reduce((a, b) => a + (b ?? 0), 0);
  return (
    <Link
      to="/c/$cluster"
      params={{ cluster: c.name }}
      className={`fcard${c.protected ? " protected" : ""}`}
      style={clusterStyle(c)}
      aria-label={`Open ${c.name}`}
    >
      <div className="fh">
        <span className="sw">{c.protected ? "" : (c.displayName || c.name).charAt(0).toUpperCase()}</span>
        <span className="fn">
          {c.displayName || c.name}
          {c.protected && (
            <span className="lockb">
              <Icon name="lock" />
              Protected
            </span>
          )}
        </span>
        <span className="fe">{[c.environment, c.region].filter(Boolean).join(", ")}</span>
        <span className="fk">{index < 9 && <kbd>{index + 1}</kbd>}</span>
      </div>
      <dl className="kv2">
        <dt>Health</dt>
        <dd>
          <Health cluster={c} />
        </dd>
        <dt>Resources</dt>
        <dd>{c.connected ? total : "–"}</dd>
        <dt>Flux</dt>
        <dd>{c.fluxVersion ?? "–"}</dd>
        <dt>Kubernetes</dt>
        <dd>{c.kubernetesVersion ?? "–"}</dd>
      </dl>
      <span className={`conn${c.connected ? "" : " off"}`}>
        <span className="dot" />
        {c.connected
          ? `Connected, agent ${c.agentVersion ?? ""}`
          : `Disconnected, last seen ${ago(c.lastSeen)}`}
      </span>
    </Link>
  );
}

function UnhealthyTable({ items, clusters }: { items: FleetResource[]; clusters: Map<string, ClusterInfo> }) {
  const navigate = useNavigate();
  if (!items.length) {
    return (
      <div className="empty">
        <strong>Nothing needs attention</strong>
        Every Flux object you can see is ready.
      </div>
    );
  }
  return (
    <table className="dtable clickable">
      <thead>
        <tr>
          <th>Cluster</th>
          <th>Resource</th>
          <th>Status</th>
          <th>Message</th>
          <th className="num">Changed</th>
        </tr>
      </thead>
      <tbody>
        {items.map(({ cluster, resource: r }) => (
          <tr key={`${cluster}/${r.id}`} onClick={() => void navigate(detailLink(cluster, r))}>
            <td>
              <span className="ctag" style={clusterStyle(clusters.get(cluster))}>
                {cluster}
              </span>
            </td>
            <td>
              <Link {...detailLink(cluster, r)} className="mono">
                <span className="muted">{kindInfo(r.kind).abbr} </span>
                {r.namespace ? `${r.namespace}/` : ""}
                {r.name}
              </Link>
            </td>
            <td>
              <StatusPill status={r.status} />
            </td>
            <td className="msg">{r.message}</td>
            <td className="num muted">{age(r.lastChanged ?? r.createdAt)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function FleetPage() {
  const { data: clusters = [] } = useClusters();
  const { items, loading } = useFleetResources();
  const byName = useMemo(() => new Map(clusters.map((c) => [c.name, c])), [clusters]);
  const unhealthy = useMemo(
    () =>
      items
        .filter(({ resource: r }) => isFlux(r.kind) && r.status !== "ready")
        .sort(
          (a, b) =>
            STATUS_RANK[a.resource.status] - STATUS_RANK[b.resource.status] ||
            (byName.get(a.cluster)?.order ?? 0) - (byName.get(b.cluster)?.order ?? 0),
        ),
    [items, byName],
  );
  const disconnected = clusters.filter((c) => !c.connected).length;

  return (
    <Screen crumbs={["Fleet"]}>
      <h1>Fleet</h1>
      <p className="page-sub">
        {clusters.length} clusters{disconnected ? `, ${disconnected} disconnected` : ""}. Press a number to
        jump into one.
      </p>
      <div className="fleet-grid">
        {clusters.map((c, i) => (
          <ClusterCard key={c.name} c={c} index={i} />
        ))}
      </div>
      <h3 className="section-title">
        Needs attention across the fleet{" "}
        <span className="muted">{loading ? "loading…" : unhealthy.length}</span>
      </h3>
      <UnhealthyTable items={unhealthy} clusters={byName} />
    </Screen>
  );
}
