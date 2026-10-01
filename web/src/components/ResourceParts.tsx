import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { eventsQuery, useMe, yamlQuery } from "../api/queries";
import type { ClusterInfo, KubeEvent, Ref, Resource } from "../api/types";
import { useAppState } from "../lib/appState";
import { age, ago, dateTime, STATUS_LABEL } from "../lib/format";
import type { KeyId } from "../lib/keys";
import { isNotable, kindInfo, projectName } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { useClusterMotion, useRequested } from "../lib/liveMotion";
import { buildYamlAttachment, yamlWithPath } from "../lib/logAttachments";
import type { useResourceActions } from "../lib/useResourceActions";
import { WORKLOAD_LOG_KINDS } from "../lib/workloadLogs";
import { Icon, type IconName } from "./Icon";
import { SelectionToolbar } from "./SelectionToolbar";
import { RevisionValue, SourceUrl } from "./SourceLinks";
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
        {isNotable(r.project) && r.project && <span>· {projectName(r.project)}</span>}
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
            Eddy knows this object from its owner's inventory, so it has no status here. YAML and Events are
            read from the cluster as you, and ConfigMap and Secret data is never shown.
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

const RATIO = /^(\d+(?:\.\d+)?)([A-Za-z]*)\s*\/\s*(\d+(?:\.\d+)?)([A-Za-z]*)$/;

/** A usage-against-limit value ("92 / 100") turns amber from 85% of the limit. */
export const NEAR_LIMIT = 0.85;

export function nearLimit(value: string): boolean {
  const m = RATIO.exec(value.trim());
  const used = Number(m?.[1]);
  const limit = Number(m?.[3]);
  return Boolean(m) && m?.[2] === m?.[4] && limit > 0 && used / limit >= NEAR_LIMIT;
}

function DetailValue({ value }: { value: string }) {
  return nearLimit(value) ? (
    <span className="font-semibold text-attn" title="Close to the limit">
      {value}
    </span>
  ) : (
    value
  );
}

/** Kind-aware key facts. */
export function factRows(cluster: string, r: Resource): Array<[string, ReactNode]> {
  const rows: Array<[string, ReactNode]> = [];
  if (r.source) rows.push(["Source", <RefLink key="s" cluster={cluster} target={r.source} />]);
  if (r.chart) rows.push(["Chart", r.chart]);
  if (r.revision) rows.push(["Revision", <RevisionValue key="rev" cluster={cluster} r={r} />]);
  if (r.url) rows.push(["URL", <SourceUrl key="url" url={r.url} />]);
  if (r.interval) rows.push(["Interval", r.interval]);
  if (r.schedule) rows.push(["Schedule", r.schedule]);
  if (r.inventory) rows.push(["Inventory", `${r.inventory} objects`]);
  if (r.replicas) rows.push(["Replicas", `${r.replicas} ready`]);
  if (r.completions) rows.push(["Completions", `${r.completions} succeeded`]);
  if (r.images?.length) rows.push([r.images.length > 1 ? "Images" : "Image", r.images.join("\n")]);
  if (r.containers?.length) rows.push(["Containers", r.containers.join(", ")]);
  if (r.hosts?.length) rows.push(["Hosts", r.hosts.join("\n")]);
  if (r.ports?.length) rows.push(["Ports", r.ports.join("\n")]);
  // Kind-specific facts from the agent (a NodePool's usage, an ExternalSecret's target…).
  for (const d of r.details ?? []) {
    if (!rows.some(([label]) => label === d.label))
      rows.push([d.label, <DetailValue key={d.label} value={d.value} />]);
  }
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

/** Secrets are never read: the hub answers 403 for any Secret, so the tab explains it instead. */
export function SecretNote() {
  return (
    <div className="flex items-start gap-2.5 rounded-[14px] border border-dashed border-line-strong bg-surface px-3.5 py-3 text-13 text-ink-2">
      <Icon name="lock" className="mt-px size-4 shrink-0 text-ink-3" />
      <div>
        <b className="block font-semibold text-ink">Secret contents are never shown</b>
        Eddy does not read Secret objects, not even their metadata. Events for this Secret are still
        available.
      </div>
    </div>
  );
}

const YAML_QUESTION = "Explain this configuration and flag anything risky.";

export function YamlView({ cluster, r }: { cluster: string; r: Resource }) {
  const secret = r.kind === "Secret";
  const { data, isPending, error } = useQuery({ ...yamlQuery(cluster, r), enabled: !secret });
  const { data: me } = useMe();
  const { askWithLogs } = useAppState();
  const lines = useMemo(() => (data ? data.yaml.replace(/\n$/, "").split("\n") : []), [data]);
  const wrap = useRef<HTMLDivElement>(null);
  const toolbar = useRef<HTMLDivElement>(null);
  const [range, setRange] = useState<{ from: number; to: number } | null>(null);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);
  const aiOn = Boolean(me?.features.ai) && lines.length > 0;

  const send = useCallback(
    (picked: readonly string[]) => {
      const { attachment, total } = buildYamlAttachment(picked, r.id);
      askWithLogs({ attachment, total, question: YAML_QUESTION, link: { cluster, ref: r } });
    },
    [askWithLogs, cluster, r],
  );

  // Native selection: map its ends to lines whenever it changes.
  useEffect(() => {
    if (!aiOn) return;
    let frame = 0;
    const indexOf = (node: Node | null) => {
      const el = node instanceof Element ? node : node?.parentElement;
      const row = el?.closest<HTMLElement>("[data-line-index]");
      if (!row || !wrap.current?.contains(row)) return undefined;
      return Number(row.dataset.lineIndex);
    };
    const onChange = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const sel = window.getSelection();
        const a = sel && !sel.isCollapsed ? indexOf(sel.anchorNode) : undefined;
        const f = sel && !sel.isCollapsed ? indexOf(sel.focusNode) : undefined;
        if (a === undefined || f === undefined) {
          setRange(null);
          return;
        }
        const next = { from: Math.min(a, f), to: Math.max(a, f) };
        setRange((c) => (c && c.from === next.from && c.to === next.to ? c : next));
      });
    };
    document.addEventListener("selectionchange", onChange);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("selectionchange", onChange);
    };
  }, [aiOn]);

  // Above the selection, below it near the top of the box.
  const place = useCallback(() => {
    const box = wrap.current?.getBoundingClientRect();
    const sel = window.getSelection();
    if (!box || !range || !sel || sel.rangeCount === 0) {
      setPos(null);
      return;
    }
    const rect = sel.getRangeAt(0).getBoundingClientRect();
    const tw = toolbar.current?.offsetWidth ?? 320;
    const th = toolbar.current?.offsetHeight ?? 34;
    const above = rect.top - box.top - th - 6;
    const next = {
      top: Math.round(above >= 0 ? above : rect.bottom - box.top + 6),
      left: Math.round(Math.max(0, Math.min(rect.left + rect.width / 2 - box.left - tw / 2, box.width - tw))),
    };
    setPos((p) => (p && p.top === next.top && p.left === next.left ? p : next));
  }, [range]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: placed whenever the selection moves
  useLayoutEffect(() => {
    place();
  }, [place, range]);

  useEffect(() => {
    if (!range) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      window.getSelection()?.removeAllRanges();
      setRange(null);
    };
    window.addEventListener("keydown", onKey, true);
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
    };
  }, [range, place]);

  const ask = (withPath: boolean) => {
    if (!range) return;
    send(withPath ? yamlWithPath(lines, range.from, range.to) : lines.slice(range.from, range.to + 1));
    window.getSelection()?.removeAllRanges();
    setRange(null);
  };

  if (secret) return <SecretNote />;
  if (isPending) return <p className="text-ink-3">Loading YAML…</p>;
  if (error) return <p className="text-12-5 text-bad">Couldn't load YAML: {error.message}</p>;
  return (
    <div ref={wrap} className="relative flex flex-col gap-2">
      {aiOn && (
        <div className="flex justify-end">
          <button
            type="button"
            className="inline-flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-[11px] text-12-5 whitespace-nowrap text-ink-2 hover:border-line-strong"
            title="Attach this YAML (the first 500 lines) to a question"
            onClick={() => send(lines)}
          >
            <Icon name="spark" className="size-3.5 text-c" />
            Ask AI
          </button>
        </div>
      )}
      <pre className="m-0 overflow-auto rounded-xl border border-line bg-surface px-3.5 py-3 font-mono text-12 leading-[1.65]">
        {lines.map((line, i) => {
          const m = line.match(YAML_KEY);
          return (
            // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
            <span key={i} data-line-index={i}>
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
      {aiOn && range && (
        <SelectionToolbar
          toolbarRef={toolbar}
          count={range.to - range.from + 1}
          pos={pos}
          contextTitle="The selection with the keys that contain it, so the excerpt stays valid YAML"
          onSelection={() => ask(false)}
          onContext={() => ask(true)}
        />
      )}
    </div>
  );
}
