import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { eventsQuery, useMe, yamlQuery } from "../api/queries";
import type { ClusterInfo, KubeEvent, Ref, Resource } from "../api/types";
import { age, ago, dateTime, STATUS_LABEL } from "../lib/format";
import type { KeyId } from "../lib/keys";
import { kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { useClusterMotion, useRequested } from "../lib/liveMotion";
import type { useResourceActions } from "../lib/useResourceActions";
import { WORKLOAD_LOG_KINDS } from "../lib/workloadLogs";
import { Icon, type IconName } from "./Icon";
import { KeyHint, RequestedBadge, Spinner, StatusIcon } from "./Status";

export function KindChip({ kind }: { kind: string }) {
  return (
    <span className="rounded-md border border-line bg-surface px-1.5 py-1 font-mono text-10 font-semibold text-ink-2">
      {kindInfo(kind).abbr}
    </span>
  );
}

const STATUS_BOX: Partial<Record<Resource["status"], string>> = {
  failed: "border-bad/45 bg-bad/6",
  reconciling: "border-run/40 bg-surface",
  suspended: "border-off/40 bg-surface",
  // A finished run: calm, a quiet green outline, no fill.
  completed: "border-ok/25 bg-surface",
};

/**
 * Kind, name and the status box. With `cluster`, a live status change flashes the box in
 * the new status colour and swaps the label in, and a pending action shows "requested".
 */
export function ResourceHeader({ r, large, cluster }: { r: Resource; large?: boolean; cluster?: string }) {
  const change = useClusterMotion(cluster).changed.get(r.id);
  const requested = useRequested(cluster).get(r.id);
  const flash = change?.kind === "status" ? change.to : undefined;
  return (
    <>
      <div className="flex flex-wrap items-center gap-2 text-12-5 text-ink-3">
        <KindChip kind={r.kind} />
        {r.kind}
        {r.namespace ? ` in ${r.namespace}` : " (cluster-scoped)"}
      </div>
      <h2
        className={`mt-2 mb-0.5 font-mono font-semibold break-all leading-tight ${large ? "text-22" : "text-18"}`}
      >
        {r.name}
      </h2>
      {r.inventoryOnly ? (
        <div className="my-3 flex items-start gap-2.5 rounded-[14px] border border-dashed border-line-strong bg-surface px-3.5 py-3 text-13 text-ink-2">
          <Icon name="info" className="mt-px size-4 shrink-0 text-ink-3" />
          <div>
            <b className="block font-semibold text-ink">Managed by Flux · not watched by Eddy</b>
            Eddy knows this object from its owner's inventory. It shows kind, namespace and name only: no
            status, and never any data.
          </div>
        </div>
      ) : (
        <div
          // One border and one background class each: two colour utilities for the same property
          // resolve by CSS order, not by class order, so the status box replaces the default.
          className={`live-row my-3 flex items-start gap-2.5 rounded-[14px] border px-3.5 py-3 text-13 transition-colors duration-(--duration-slow) [overflow-wrap:anywhere] ${STATUS_BOX[r.status] ?? "border-line bg-surface"}`}
          data-flash={flash}
        >
          <StatusIcon key={r.status} status={r.status} className={`mt-px ${flash ? "swap-in" : ""}`} />
          <div className="min-w-0 flex-1">
            <span className="flex flex-wrap items-center gap-2">
              <b key={r.status} className={`block font-semibold ${flash ? "swap-in" : ""}`}>
                {STATUS_LABEL[r.status]}
              </b>
              {requested && <RequestedBadge req={requested} />}
            </span>
            <span key={r.message} className={`block ${change ? "swap-fade" : ""}`}>
              {r.message}
            </span>
          </div>
        </div>
      )}
    </>
  );
}

function ActionButton({
  icon,
  label,
  keyId,
  onClick,
  primary,
  soft,
  disabled,
  pending,
  title,
}: {
  icon: IconName;
  label: string;
  keyId: KeyId;
  onClick: () => void;
  primary?: boolean;
  soft?: boolean;
  disabled?: boolean;
  /** The request is in flight: a spinner replaces the icon until the hub answers. */
  pending?: boolean;
  title?: string;
}) {
  return (
    <button
      type="button"
      className={`btn${primary ? " btn-primary" : ""}${soft ? " btn-soft" : ""}`}
      onClick={onClick}
      disabled={disabled || pending}
      aria-busy={pending || undefined}
      title={title}
    >
      {pending ? <Spinner /> : <Icon name={icon} />}
      {label}
      <KeyHint id={keyId} />
    </button>
  );
}

interface ActionsProps {
  cluster: ClusterInfo;
  r: Resource;
  actions: ReturnType<typeof useResourceActions>;
  onLogs?: () => void;
  onOpen?: () => void;
  onAsk?: () => void;
}

export function ResourceActions({ cluster, r, actions, onLogs, onOpen, onAsk }: ActionsProps) {
  const { data: me } = useMe();
  const info = kindInfo(r.kind);
  const pending = actions.busy.get(r.id);
  const busy = pending !== undefined;
  const offline = !cluster.connected;
  const note = "mt-0.5 mb-4 flex items-center gap-1.5 text-12 text-ink-3 [&_svg]:size-[13px]";
  if (r.inventoryOnly) return null;
  return (
    <>
      <div className="mb-2 flex flex-wrap gap-2">
        {info.flux && !actions.readOnly && (
          <>
            <ActionButton
              icon="sync"
              label={pending === "reconcile" ? "Requesting…" : "Reconcile"}
              keyId="reconcile"
              primary
              title={`flux reconcile ${r.kind.toLowerCase()} ${r.name}`}
              pending={pending === "reconcile"}
              disabled={busy || r.suspended || offline}
              onClick={() => actions.reconcile(r)}
            />
            {info.hasSource && (
              <ActionButton
                icon="sync"
                label="With source"
                keyId="reconcileSource"
                title="Fetch the source first, then apply"
                disabled={busy || r.suspended || offline}
                onClick={() => actions.reconcile(r, true)}
              />
            )}
            <ActionButton
              icon={r.suspended ? "play" : "pause"}
              label={
                pending === "suspend"
                  ? "Suspending…"
                  : pending === "resume"
                    ? "Resuming…"
                    : r.suspended
                      ? "Resume"
                      : "Suspend"
              }
              keyId="suspend"
              pending={pending === "suspend" || pending === "resume"}
              disabled={busy || offline}
              onClick={() => actions.toggleSuspend(r)}
            />
          </>
        )}
        {onLogs &&
          ((r.kind === "Pod" && me?.features.logs) ||
            (WORKLOAD_LOG_KINDS.has(r.kind) && me?.features.workloadLogs)) && (
            <ActionButton icon="term" label="Logs" keyId="logs" primary onClick={onLogs} />
          )}
        {onOpen && <ActionButton icon="open" label="Open" keyId="open" onClick={onOpen} />}
        {onAsk && me?.features.ai && (
          <ActionButton icon="spark" label="Ask" keyId="ask" soft onClick={onAsk} />
        )}
      </div>
      {info.flux && actions.readOnly ? (
        <div className={note}>
          <Icon name="lock" />
          {`${cluster.name} is in read-only local mode. Reconcile, suspend and resume are off.`}
        </div>
      ) : info.flux ? (
        <div className={note}>
          <Icon name={cluster.protected ? "lock" : "user"} />
          {cluster.protected
            ? `${cluster.name} is protected. Suspending asks you to type its name.`
            : cluster.mode === "local"
              ? `Actions run as your kubeconfig identity (${cluster.context || cluster.name}), not as ${me?.user ?? "you"}.`
              : `Actions run as ${me?.user ?? "you"} through Kubernetes RBAC.`}
        </div>
      ) : (
        <div className={note} />
      )}
    </>
  );
}

function RefLink({ cluster, target }: { cluster: string; target: Ref }) {
  return (
    <Link {...detailLink(cluster, target)} className="linkbtn">
      {target.kind}/{target.namespace ? `${target.namespace}/` : ""}
      {target.name}
    </Link>
  );
}

/** Kind-aware key facts. */
export function factRows(cluster: string, r: Resource): Array<[string, ReactNode]> {
  const rows: Array<[string, ReactNode]> = [];
  if (r.source) rows.push(["Source", <RefLink key="s" cluster={cluster} target={r.source} />]);
  if (r.chart) rows.push(["Chart", r.chart]);
  if (r.revision) rows.push(["Revision", r.revision]);
  if (r.url) rows.push(["URL", r.url]);
  if (r.interval) rows.push(["Interval", r.interval]);
  if (r.schedule) rows.push(["Schedule", r.schedule]);
  if (r.inventory) rows.push(["Inventory", `${r.inventory} objects`]);
  if (r.replicas) rows.push(["Replicas", `${r.replicas} ready`]);
  if (r.completions) rows.push(["Completions", `${r.completions} succeeded`]);
  if (r.images?.length) rows.push([r.images.length > 1 ? "Images" : "Image", r.images.join("\n")]);
  if (r.containers?.length) rows.push(["Containers", r.containers.join(", ")]);
  if (r.hosts?.length) rows.push(["Hosts", r.hosts.join("\n")]);
  if (r.ports?.length) rows.push(["Ports", r.ports.join("\n")]);
  if (r.owner) rows.push(["Managed by", <RefLink key="o" cluster={cluster} target={r.owner} />]);
  rows.push(["API version", r.group ? `${r.group}/${r.version}` : r.version]);
  if (r.createdAt) rows.push(["Created", `${dateTime(r.createdAt)} (${age(r.createdAt)})`]);
  if (r.lastChanged) rows.push(["Last change", ago(r.lastChanged)]);
  return rows;
}

/**
 * The facts as a label/value list (preview panel), or as a grid of tiles that uses the
 * width of the detail page.
 */
export function ResourceFacts({ cluster, r, grid }: { cluster: string; r: Resource; grid?: boolean }) {
  const rows = factRows(cluster, r);
  if (grid) {
    return (
      <dl className="m-0 grid grid-cols-[repeat(auto-fill,minmax(240px,1fr))] gap-2">
        {rows.map(([k, v]) => (
          <div key={k} className="min-w-0 rounded-xl border border-line bg-surface px-3 py-2.5">
            <dt className="mb-0.5 text-12 text-ink-3">{k}</dt>
            <dd className="m-0 font-mono text-12-5 break-all whitespace-pre-line">{v}</dd>
          </div>
        ))}
      </dl>
    );
  }
  return (
    <dl className="m-0 grid grid-cols-[max-content_1fr] gap-x-4 gap-y-[9px] text-13">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-ink-3">{k}</dt>
          <dd className="m-0 font-mono text-12-5 break-all whitespace-pre-line">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function SectionTitle({ children }: { children: ReactNode }) {
  return <h3 className="mt-[22px] mb-2.5 flex items-center gap-2 text-13 font-semibold">{children}</h3>;
}

const EVENT = "rounded-xl border border-line bg-surface px-3 py-2.5 text-12-5";

export function Conditions({ r }: { r: Resource }) {
  if (!r.conditions?.length) return <p className="text-ink-3">No conditions reported.</p>;
  return (
    <ul className="flex flex-col gap-2">
      {r.conditions.map((c) => (
        <li key={c.type} className={EVENT}>
          <div className="mb-0.5 flex justify-between gap-2">
            <span className={`font-semibold ${c.status === "False" ? "text-bad" : ""}`}>
              {c.type}={c.status}
              {c.reason ? ` · ${c.reason}` : ""}
            </span>
            <span className="text-ink-3 tabular-nums">{age(c.lastTransitionTime)}</span>
          </div>
          {c.message && <p className="break-words text-ink-2">{c.message}</p>}
        </li>
      ))}
    </ul>
  );
}

export function EventsList({ cluster, r, limit }: { cluster: string; r: Resource; limit?: number }) {
  const { data, isPending, error } = useQuery(eventsQuery(cluster, r));
  if (r.inventoryOnly) return <p className="text-ink-3">Eddy does not watch events for this kind.</p>;
  if (isPending) return <p className="text-ink-3">Loading events…</p>;
  if (error) return <p className="text-12-5 text-bad">Couldn't load events: {error.message}</p>;
  const items: KubeEvent[] = limit ? data.slice(0, limit) : data;
  if (!items.length) return <p className="text-ink-3">No recent events.</p>;
  return (
    <ul className="flex flex-col gap-2">
      {items.map((e, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: events have no stable id in the summary
        <li key={i} className={EVENT}>
          <div className="mb-0.5 flex justify-between gap-2">
            <span className={`font-semibold ${e.type === "Warning" ? "text-bad" : ""}`}>
              {e.reason}
              {e.count > 1 ? ` ×${e.count}` : ""}
            </span>
            <span className="text-ink-3 tabular-nums">{age(e.last ?? e.first)}</span>
          </div>
          <p className="break-words text-ink-2">{e.message}</p>
        </li>
      ))}
    </ul>
  );
}

const YAML_KEY = /^(\s*-?\s*)([A-Za-z][\w./-]*)(:)(.*)$/;

export function YamlView({ cluster, r }: { cluster: string; r: Resource }) {
  const { data, isPending, error } = useQuery(yamlQuery(cluster, r));
  if (isPending) return <p className="text-ink-3">Loading YAML…</p>;
  if (error) return <p className="text-12-5 text-bad">Couldn't load YAML: {error.message}</p>;
  return (
    <pre className="m-0 overflow-auto rounded-xl border border-line bg-surface px-3.5 py-3 font-mono text-12 leading-[1.65]">
      {data.yaml.split("\n").map((line, i) => {
        const m = line.match(YAML_KEY);
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
          <span key={i}>
            {m ? (
              <>
                {m[1]}
                <span className="text-c">{m[2]}</span>
                {m[3]}
                <span className="text-ink-2">{m[4]}</span>
              </>
            ) : (
              line
            )}
            {"\n"}
          </span>
        );
      })}
    </pre>
  );
}
