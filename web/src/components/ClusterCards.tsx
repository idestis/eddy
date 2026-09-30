import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import type { ClusterInfo, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { ago, shortRevision } from "../lib/format";
import { isFlux } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { QuestionChip, suggestions } from "./AskAI";
import { Icon } from "./Icon";
import { StatusIcon } from "./Status";

export const CARD = "flex min-w-0 flex-col gap-2.5 rounded-card border border-line bg-surface p-4";
const CARD_TITLE = "flex items-center gap-2 text-14 font-semibold tracking-tight";

/** A two-column list of facts: label on the left, monospace value on the right. */
export function FactList({ rows }: { rows: Array<[string, ReactNode, string?]> }) {
  return (
    <dl className="m-0 grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-12-5">
      {rows.map(([k, v, title]) => (
        <div key={k} className="contents">
          <dt className="text-ink-3">{k}</dt>
          <dd className="truncate font-mono" title={title}>
            {v}
          </dd>
        </div>
      ))}
    </dl>
  );
}

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
    .filter((r) => isFlux(r.kind) && r.status !== "ready" && !r.inventoryOnly)
    .sort((a, b) => Number(b.status === "failed") - Number(a.status === "failed"));
  const git =
    items.find((r) => r.kind === "GitRepository" && r.name === "flux-system") ??
    items.find((r) => r.kind === "GitRepository");
  const applied = items.filter(
    (r) => r.kind === "Kustomization" && r.source && git && r.source.name === git.name,
  ).length;
  const failing = attention.some((r) => r.status === "failed");

  return (
    <div className="grid shrink-0 grid-cols-[1fr_1fr_1.15fr] gap-3.5 max-[1180px]:grid-cols-2 max-[859px]:hidden">
      <div className={CARD}>
        <h4 className={CARD_TITLE}>
          Needs attention
          <span
            className={`ml-auto font-mono text-22 font-medium ${failing ? "text-bad" : attention.length ? "text-attn" : "text-ok"}`}
          >
            {attention.length}
          </span>
        </h4>
        {attention.length === 0 && (
          <div className="text-12-5 text-ink-3">Every Flux object on {cluster.name} is ready.</div>
        )}
        {attention.slice(0, 3).map((r) => (
          <Link
            key={r.id}
            {...detailLink(cluster.name, r)}
            className="flex min-w-0 items-center gap-[9px] rounded-[9px] px-2 py-[7px] text-12-5 no-underline hover:bg-surface-sunken"
          >
            <StatusIcon status={r.status} label />
            <span className="font-mono font-medium whitespace-nowrap">{r.name}</span>
            <span className="min-w-0 truncate text-ink-3">{r.message}</span>
          </Link>
        ))}
        {attention.length > 3 && (
          <div className="mt-auto flex gap-2">
            <Link
              to="/c/$cluster"
              params={{ cluster: cluster.name }}
              search={{ status: "attention" }}
              className="btn btn-sm"
            >
              Show all {attention.length}
            </Link>
          </div>
        )}
      </div>
      <div className={CARD}>
        <h4 className={CARD_TITLE}>
          <Icon name="git" />
          Git source
        </h4>
        {git ? (
          <>
            <FactList
              rows={[
                ["Repository", git.url?.replace(/^.*[:/]([^/]+\/[^/]+?)(\.git)?$/, "$1"), git.url],
                ["Revision", shortRevision(git.revision)],
                ["Fetched", `${ago(git.lastChanged)}${git.interval ? `, every ${git.interval}` : ""}`],
                ["Applied by", `${applied} Kustomizations`],
              ]}
            />
            <div className="mt-auto flex gap-2">
              <Link {...detailLink(cluster.name, git)} className="btn btn-sm">
                Details
              </Link>
            </div>
          </>
        ) : (
          <div className="text-12-5 text-ink-3">No GitRepository on this cluster.</div>
        )}
      </div>
      <div
        className={`${CARD} border-c/30 bg-[radial-gradient(120%_140%_at_0%_0%,color-mix(in_oklab,var(--c)_20%,transparent),transparent_60%),radial-gradient(120%_140%_at_100%_100%,color-mix(in_oklab,var(--c2)_20%,transparent),transparent_60%)] max-[1180px]:col-span-full`}
      >
        <h4 className={`${CARD_TITLE} [&_svg]:text-c`}>
          <Icon name="spark" />
          Ask AI about {cluster.name}
        </h4>
        <p className="-mt-1 text-12-5 text-ink-2">
          Quick answers from what Eddy can see here: statuses, events and versions.
        </p>
        <div className="flex flex-wrap gap-1.5">
          {suggestions(cluster, undefined).map((q) => (
            <QuestionChip
              key={q}
              question={q}
              onAsk={(x) => ask(x)}
              disabled={!aiEnabled || !cluster.connected}
            />
          ))}
        </div>
      </div>
    </div>
  );
}
