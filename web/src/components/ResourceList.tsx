import { useVirtualizer } from "@tanstack/react-virtual";
import {
  type CSSProperties,
  memo,
  type Ref,
  type RefObject,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { Resource } from "../api/types";
import { age, listMessage, revisionOf, revisionTitle } from "../lib/format";
import { kindHeading, kindInfo } from "../lib/kinds";
import { type ClusterMotion, NO_MOTION, type Requested } from "../lib/liveMotion";
import type { ListRow } from "../lib/resourceRows";
import { Icon } from "./Icon";
import { RequestedBadge, StatusPill } from "./Status";

const ROW_HEIGHT = 40;
const GROUP_HEIGHT = 34;

export const INVENTORY_ONLY_HINT = "Managed by Flux · not watched by Eddy";

export const rowDomId = (id: string): string => `row-${id.replace(/[^\w-]/g, "_")}`;

type ColumnId = "name" | "kind" | "status" | "replicas" | "message" | "version" | "age";

/** Columns in display order. Name takes the flexible space; the message is capped. */
const TRACKS: Record<ColumnId, string> = {
  name: "minmax(200px,1.6fr)",
  kind: "132px",
  status: "112px",
  replicas: "64px",
  message: "minmax(120px,1fr)",
  version: "minmax(110px,200px)",
  age: "44px",
};

export interface Columns {
  ids: ColumnId[];
  template: string;
  versionLabel: string;
  /** "Done" when the column holds only Job completions, not live replicas. */
  replicasLabel: string;
}

/**
 * Picks the columns for the rows on screen and the list's width. Kind is dropped when
 * rows are grouped by kind (the group header says it). The ready column appears for
 * workloads, Pods and Jobs and is labelled for what it counts: "Replicas" for workloads,
 * "Containers" for Pods (ready containers, as kubectl's READY), "Done" for Jobs, and
 * "Ready" when kinds are mixed. The version column is "Image" for workloads and
 * "Revision" for Flux objects.
 */
export function pickColumns(rows: readonly ListRow[], grouped: boolean, width: number): Columns {
  let replicas = false;
  let completions = false;
  let podReady = false;
  let workloadReady = false;
  let workloads = 0;
  let flux = 0;
  for (const row of rows) {
    if (row.type !== "resource") continue;
    const r = row.resource;
    if (r.replicas) {
      replicas = true;
      if (r.kind === "Pod") podReady = true;
      else workloadReady = true;
    } else if (r.completions) completions = true;
    const info = kindInfo(r.kind);
    if (info.workload) workloads++;
    else if (info.flux) flux++;
  }
  const ids: ColumnId[] = ["name"];
  if (!grouped && width >= 900) ids.push("kind");
  ids.push("status");
  if (replicas || completions) ids.push("replicas");
  if (width >= 540) ids.push("message");
  if (width >= 820) ids.push("version");
  if (width >= 460) ids.push("age");
  const versionLabel = workloads && !flux ? "Image" : flux && !workloads ? "Revision" : "Version";
  const replicasLabel = !replicas
    ? "Done"
    : completions || (podReady && workloadReady)
      ? "Ready"
      : podReady
        ? "Containers"
        : "Replicas";
  return { ids, template: ids.map((id) => TRACKS[id]).join(" "), versionLabel, replicasLabel };
}

const HEADER: Record<ColumnId, string> = {
  name: "Name",
  kind: "Kind",
  status: "Status",
  replicas: "Replicas",
  message: "Message",
  version: "",
  age: "Age",
};

interface ResourceListProps {
  rows: ListRow[];
  grouped: boolean;
  selectedId: string | undefined;
  onSelect: (r: Resource) => void;
  onOpen: (r: Resource) => void;
  label: string;
  ref?: Ref<ResourceListHandle>;
  /** Live changes to show: status flashes, rows entering and leaving. */
  motion?: ClusterMotion;
  /** Rows waiting for the result of an action the user requested. */
  requested?: ReadonlyMap<string, Requested>;
  /** The cluster is disconnected: the rows are its last known state. */
  stale?: boolean;
  /** The rows on screen (first and last index) changed: a windowed list loads their pages. */
  onRange?: (start: number, end: number) => void;
}

export interface ResourceListHandle {
  focus: () => void;
}

const CELL = "min-w-0 truncate";

const ResourceRow = memo(function ResourceRow({
  r,
  columns,
  selected,
  onSelect,
  onOpen,
  motion,
  requested,
}: {
  r: Resource;
  columns: Columns;
  selected: boolean;
  onSelect: (r: Resource) => void;
  onOpen: (r: Resource) => void;
  motion: ClusterMotion;
  requested: Requested | undefined;
}) {
  const info = kindInfo(r.kind);
  const message = r.inventoryOnly ? INVENTORY_ONLY_HINT : listMessage(r);
  const now = Date.now();
  const change = motion.changed.get(r.id);
  const flash = change?.kind === "status" ? change.to : undefined;
  const entered = motion.entered.get(r.id);
  const leaving = motion.leaving.has(r.id);
  // A row that scrolls back into view mid-animation resumes it (negative delay).
  const live: Record<string, string> = {};
  if (change) live["--flash-delay"] = `${change.at - now}ms`;
  if (entered !== undefined) live["--enter-delay"] = `${entered - now}ms`;
  const cell = (id: ColumnId) => {
    switch (id) {
      case "name":
        return (
          <span
            key={id}
            className={`${CELL} font-mono text-13`}
            title={`${r.namespace ? `${r.namespace}/` : ""}${r.name}`}
          >
            {r.namespace && <span className="text-ink-3">{r.namespace} / </span>}
            <span className="font-medium">{r.name}</span>
          </span>
        );
      case "kind":
        return (
          <span key={id} className={`${CELL} text-12-5 text-ink-2`}>
            {info.kind}
          </span>
        );
      case "status":
        return r.inventoryOnly ? (
          <span key={id} className="inline-flex items-center gap-1.5 text-12-5 text-ink-3">
            <Icon name={info.icon} className="size-3.5" />
            Inventory
          </span>
        ) : (
          // Keyed by status: a new status swaps in instead of jumping.
          <span key={`${id}:${r.status}`} className={`inline-flex min-w-0 ${flash ? "swap-in" : ""}`}>
            <StatusPill status={r.status} />
          </span>
        );
      case "replicas":
        // A finished Job has no replicas; its completions ("1/1") are shown muted, as history.
        return r.replicas || !r.completions ? (
          <span
            key={id}
            className={`font-mono text-12-5 tabular-nums ${r.replicas && r.status !== "ready" ? "text-attn" : "text-ink-2"}`}
            title={r.replicas ? `${r.replicas} ready` : undefined}
          >
            {r.replicas}
          </span>
        ) : (
          <span
            key={id}
            className="font-mono text-12-5 text-ink-3 tabular-nums"
            title={`${r.completions} completions succeeded`}
          >
            {r.completions}
          </span>
        );
      case "message":
        return (
          <span key={id} className="flex min-w-0 items-center gap-2">
            {requested && <RequestedBadge req={requested} />}
            <span
              key={message}
              className={`${CELL} text-12-5 ${r.status === "failed" && !r.inventoryOnly ? "text-bad" : "text-ink-2"} ${change ? "swap-fade" : ""}`}
              title={r.message}
            >
              {message}
            </span>
          </span>
        );
      case "version":
        return (
          <span key={id} className={`${CELL} font-mono text-12 text-ink-2`} title={revisionTitle(r)}>
            {revisionOf(r)}
          </span>
        );
      case "age":
        return (
          <span key={id} className="text-right text-12 text-ink-3 tabular-nums" title={r.createdAt}>
            {age(r.createdAt)}
          </span>
        );
    }
  };
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard selection is handled by the listbox (j/k/Enter)
    <div
      id={rowDomId(r.id)}
      role="option"
      tabIndex={-1}
      aria-selected={selected}
      aria-disabled={leaving || undefined}
      title={r.inventoryOnly ? INVENTORY_ONLY_HINT : undefined}
      data-flash={flash}
      data-enter={entered !== undefined ? "" : undefined}
      data-leaving={leaving ? "" : undefined}
      className={`live-row grid h-full cursor-default items-center gap-3.5 rounded-[10px] px-3 hover:bg-surface-sunken aria-selected:bg-c-soft aria-selected:shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--c)_30%,transparent)] ${r.inventoryOnly ? "opacity-55" : ""}`}
      style={{ gridTemplateColumns: columns.template, ...live } as CSSProperties}
      onClick={() => (selected ? onOpen(r) : onSelect(r))}
      onDoubleClick={() => onOpen(r)}
    >
      {columns.ids.map(cell)}
    </div>
  );
});

/** Tracks an element's width. */
function useWidth(ref: RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(1200);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.clientWidth);
    const ro = new ResizeObserver(([entry]) => {
      if (entry) setWidth(Math.round(entry.contentRect.width));
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}

/**
 * The virtualised resource table. Only visible rows are in the DOM, so it
 * stays smooth with thousands of rows. The container is a listbox that keeps
 * focus; the selected row is announced with aria-activedescendant.
 */
export function ResourceList({
  rows,
  grouped,
  selectedId,
  onSelect,
  onOpen,
  label,
  ref,
  motion = NO_MOTION,
  requested,
  stale = false,
  onRange,
}: ResourceListProps) {
  const scroller = useRef<HTMLDivElement>(null);
  const box = useRef<HTMLDivElement>(null);
  const width = useWidth(box);
  useImperativeHandle(ref, () => ({ focus: () => scroller.current?.focus() }), []);
  const columns = useMemo(() => pickColumns(rows, grouped, width), [rows, grouped, width]);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scroller.current,
    estimateSize: (i) => (rows[i]?.type === "group" ? GROUP_HEIGHT : ROW_HEIGHT),
    getItemKey: (i) => rows[i]?.key ?? i,
    overscan: 12,
  });

  // Rows glide to new places only around live inserts, removals and re-sorts, never
  // when a filter or a scroll moves them.
  const settling = motion.settleUntil > Date.now();

  const items = virtualizer.getVirtualItems();
  const first = items[0]?.index ?? 0;
  const last = items[items.length - 1]?.index ?? 0;
  useEffect(() => {
    onRange?.(first, last);
  }, [onRange, first, last]);

  const selectedIndex = selectedId ? rows.findIndex((r) => r.key === selectedId) : -1;
  useEffect(() => {
    if (selectedIndex >= 0) virtualizer.scrollToIndex(selectedIndex, { align: "auto" });
  }, [selectedIndex, virtualizer]);

  return (
    <div
      ref={box}
      className="stale-able flex min-h-0 flex-1 flex-col overflow-hidden rounded-card border border-line bg-surface"
      data-stale={stale}
    >
      <div
        className="grid shrink-0 items-center gap-3.5 border-b border-line bg-surface-side py-[9px] pr-4 pl-4 text-12 text-ink-3"
        style={{ gridTemplateColumns: columns.template }}
        aria-hidden="true"
      >
        {columns.ids.map((id) => (
          <span key={id} className={id === "age" ? "text-right" : "truncate"}>
            {id === "version" ? columns.versionLabel : id === "replicas" ? columns.replicasLabel : HEADER[id]}
          </span>
        ))}
      </div>
      <div
        ref={scroller}
        className="min-h-0 flex-1 overflow-auto outline-none [contain:strict] focus-visible:rounded-b-card focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-c"
        role="listbox"
        aria-label={label}
        tabIndex={0}
        aria-activedescendant={selectedId && selectedIndex >= 0 ? rowDomId(selectedId) : undefined}
      >
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {items.map((v) => {
            const row = rows[v.index];
            if (!row) return null;
            return (
              <div
                key={v.key}
                className={`absolute top-0 left-0 w-full px-1 ${settling ? "glide" : ""}`}
                style={{ height: v.size, transform: `translateY(${v.start}px)` }}
              >
                {row.type === "placeholder" ? (
                  <div className="flex h-full items-center gap-3 px-3" aria-hidden="true">
                    <span className="skeleton size-4 shrink-0 rounded-full" />
                    <span className="skeleton h-3 w-[40%] rounded" />
                    <span className="skeleton ml-auto h-3 w-14 rounded" />
                  </div>
                ) : row.type === "group" ? (
                  <div
                    className="flex h-full items-center gap-2 px-3 pt-2.5 pb-1 text-12 font-semibold text-ink-3"
                    role="presentation"
                  >
                    <Icon name={kindInfo(row.kind).icon} className="size-3.5" />
                    {kindHeading(row.kind, row.project)}
                    <span className="font-medium tabular-nums">{row.count}</span>
                    {row.failing > 0 && <span className="text-bad">{row.failing} failing</span>}
                  </div>
                ) : (
                  <ResourceRow
                    r={row.resource}
                    columns={columns}
                    selected={row.key === selectedId}
                    onSelect={onSelect}
                    onOpen={onOpen}
                    motion={motion}
                    requested={requested?.get(row.resource.id)}
                  />
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
