import { Link } from "@tanstack/react-router";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { ago, shortRevision } from "../lib/format";
import { isFlux } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { suggestions } from "./AskAI";
import { Icon } from "./Icon";
import { StatusIcon } from "./Status";

/** The three summary cards above the list: attention, Git source, Ask AI. */
export function ClusterCards({
  cluster,
  items,
  aiEnabled,
}: {
  cluster: ClusterInfo;
  items: readonly Resource[];
  aiEnabled: boolean;
}) {
  const { ask } = useAppState();
  const attention = items
    .filter((r) => isFlux(r.kind) && r.status !== "ready")
    .sort((a, b) => Number(b.status === "failed") - Number(a.status === "failed"));
  const git =
    items.find((r) => r.kind === "GitRepository" && r.name === "flux-system") ??
    items.find((r) => r.kind === "GitRepository");
  const applied = items.filter(
    (r) => r.kind === "Kustomization" && r.source && git && r.source.name === git.name,
  ).length;

  return (
    <div className="cards">
      <div className="card">
        <h4>
          Needs attention
          <span className={`big${attention.some((r) => r.status === "failed") ? " bad" : ""}`}>
            {attention.length}
          </span>
        </h4>
        {attention.length === 0 && <div className="sub">Every Flux object on {cluster.name} is ready.</div>}
        {attention.slice(0, 3).map((r) => (
          <Link key={r.id} {...detailLink(cluster.name, r)} className="citem">
            <StatusIcon status={r.status} label />
            <span className="nm">{r.name}</span>
            <span className="ms">{r.message}</span>
          </Link>
        ))}
        {attention.length > 3 && (
          <div className="row-btns">
            <Link
              to="/c/$cluster"
              params={{ cluster: cluster.name }}
              search={{ status: "attention" }}
              className="btn sm"
            >
              Show all {attention.length}
            </Link>
          </div>
        )}
      </div>
      <div className="card">
        <h4>
          <Icon name="git" />
          Git source
        </h4>
        {git ? (
          <>
            <dl className="kv2">
              <dt>Repository</dt>
              <dd title={git.url}>{git.url?.replace(/^.*[:/]([^/]+\/[^/]+?)(\.git)?$/, "$1")}</dd>
              <dt>Revision</dt>
              <dd>{shortRevision(git.revision)}</dd>
              <dt>Fetched</dt>
              <dd>
                {ago(git.lastChanged)}
                {git.interval ? `, every ${git.interval}` : ""}
              </dd>
              <dt>Applied by</dt>
              <dd>{applied} Kustomizations</dd>
            </dl>
            <div className="row-btns">
              <Link {...detailLink(cluster.name, git)} className="btn sm">
                Details
              </Link>
            </div>
          </>
        ) : (
          <div className="sub">No GitRepository on this cluster.</div>
        )}
      </div>
      <div className="card ai">
        <h4>
          <Icon name="spark" />
          Ask AI about {cluster.name}
        </h4>
        <p>Quick answers from what Eddy can see here: statuses, events and versions.</p>
        <div className="qchips">
          {suggestions(cluster, undefined).map((q) => (
            <button type="button" key={q} className="qchip" onClick={() => ask(q)} disabled={!aiEnabled}>
              <Icon name="spark" />
              {q}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
