// What both graph screens share: the cached layout, the blocked paths, the reconcile wave
// (lib/graphCascade.ts), the context menu, the zoom controls and legend, and the Outline
// list (the same graph as text, for screen readers and for reading top to bottom).

import { type ReactNode, type Ref, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import type { ClusterInfo, GraphResponse, Resource } from "../../api/types";
import { blockage, cascadeFollow, isGroupId, nodeLabel, reach } from "../../lib/graph";
import { cascadeState, endCascade, startCascade, useCascades } from "../../lib/graphCascade";
import { createLayoutCache, type Layout } from "../../lib/graphLayout";
import { useKeys } from "../../lib/keys";
import { kindInfo } from "../../lib/kinds";
import { useClusterMotion, useRequested } from "../../lib/liveMotion";
import type { useResourceActions } from "../../lib/useResourceActions";
import { Icon } from "../Icon";
import { KeyHint } from "../Status";
import { GraphCanvas, type GraphHandle, nodeDomId } from "./GraphCanvas";

type Actions = ReturnType<typeof useResourceActions>;

export interface GraphViewProps {
  cluster: ClusterInfo;
  graph: GraphResponse;
  label: string;
  resources: ReadonlyMap<string, Resource>;
  actions: Actions;
  selectedId?: string;
  onSelect: (id: string) => void;
  /** Double click or Enter: a resource opens, a group expands. */
  onOpen: (id: string) => void;
  rootId?: string;
  dimmed?: ReadonlySet<string>;
  /** Focus mode on a node; undefined hides the Focus actions. */
  onFocusMode?: (id: string) => void;
  onEscape?: () => boolean;
  /** The one-line bar above the canvas, given the graph's size (callers own the view switch). */
  header?: (meta: GraphMeta) => ReactNode;
  /** Show the graph as an outline (steps in order) instead of the canvas. */
  outline?: boolean;
  /** Shown above the canvas, under the bar (callouts). */
  notice?: ReactNode;
  className?: string;
  ref?: Ref<GraphHandle>;
}

export interface GraphMeta {
  nodes: number;
  edges: number;
  truncated: boolean;
}

/** "18 nodes · 32 edges", for headers and tooltips. */
export const metaText = (m: GraphMeta): string =>
  `${m.nodes} nodes · ${m.edges} edges${m.truncated ? " · truncated" : ""}`;

const CTRL =
  "inline-flex size-8 items-center justify-center rounded-lg text-ink-2 hover:bg-surface-sunken hover:text-ink disabled:opacity-40";

/** The reconcile waves under way: edges that march, and waves to drop. */
function useWaves(cluster: string, layout: Layout) {
  const requested = useRequested(cluster);
  const cascades = useCascades(cluster);
  const index = layout.index;

  // A request on a node in the graph starts its wave (key, button or menu alike).
  useEffect(() => {
    for (const [id, req] of requested) {
      if (!index.nodes.has(id) || req.action !== "reconcile") continue;
      const c = cascades.get(id);
      if (c && c.at >= req.at) continue;
      startCascade(cluster, id, reach(index, id, "down", cascadeFollow(index)), req.at);
    }
  }, [cluster, requested, cascades, index]);

  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!cascades.size) return;
    const t = setInterval(() => setNow(Date.now()), 500);
    return () => clearInterval(t);
  }, [cascades.size]);

  const { marching, over } = useMemo(() => {
    const marching = new Set<string>();
    const over: string[] = [];
    for (const c of cascades.values()) {
      const st = cascadeState(c, index.nodes, requested.has(c.root), now);
      if (st.over) {
        over.push(c.root);
        continue;
      }
      const wave = new Set([c.root, ...c.downstream]);
      for (const e of layout.edges) if (wave.has(e.from) && st.pending.has(e.to)) marching.add(e.key);
    }
    return { marching, over };
  }, [cascades, index, requested, layout.edges, now]);

  useEffect(() => {
    for (const root of over) endCascade(cluster, root);
  }, [cluster, over]);

  return { marching, requested };
}

interface Menu {
  id: string;
  x: number;
  y: number;
}

function NodeMenu({
  menu,
  r,
  group,
  actions,
  onFocusMode,
  onOpen,
  onClose,
}: {
  menu: Menu;
  r: Resource | undefined;
  group: boolean;
  actions: Actions;
  onFocusMode?: (id: string) => void;
  onOpen: (id: string) => void;
  onClose: () => void;
}) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    box.current?.querySelector<HTMLElement>("[role='menuitem']")?.focus();
    const away = (e: PointerEvent) => {
      if (!box.current?.contains(e.target as Node)) onClose();
    };
    window.addEventListener("pointerdown", away, true);
    return () => window.removeEventListener("pointerdown", away, true);
  }, [onClose]);
  const flux = r && kindInfo(r.kind).flux && !r.inventoryOnly;
  const items: Array<{
    label: string;
    icon: Parameters<typeof Icon>[0]["name"];
    hint?: ReactNode;
    run: () => void;
  }> = [];
  if (flux && r) {
    items.push({
      label: "Reconcile",
      icon: "sync",
      hint: <KeyHint id="reconcile" />,
      run: () => actions.reconcile(r),
    });
    if (kindInfo(r.kind).hasSource)
      items.push({
        label: "Reconcile with source",
        icon: "sync",
        hint: <KeyHint id="reconcileSource" />,
        run: () => actions.reconcile(r, true),
      });
  }
  if (onFocusMode)
    items.push({
      label: "Focus here",
      icon: "focus",
      hint: <KeyHint id="graphFocus" />,
      run: () => onFocusMode(menu.id),
    });
  if (r || group)
    items.push({ label: group ? "Expand" : "Open details", icon: "open", run: () => onOpen(menu.id) });
  const step = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const all = [...(box.current?.querySelectorAll<HTMLElement>("[role='menuitem']") ?? [])];
    const i = all.indexOf(document.activeElement as HTMLElement);
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      all[(i + (e.key === "ArrowDown" ? 1 : -1) + all.length) % all.length]?.focus();
    } else if (e.key === "Escape" || e.key === "Tab") {
      onClose();
      document.getElementById(nodeDomId(menu.id))?.focus();
    } else return;
    e.preventDefault();
    e.stopPropagation();
  };
  if (!items.length) return null;
  return (
    <div
      ref={box}
      role="menu"
      aria-label="Node actions"
      tabIndex={-1}
      onKeyDown={step}
      className="anim-pop-in fixed z-50 min-w-52 rounded-tile border border-line bg-surface p-1 shadow-pop"
      style={{
        left: Math.min(menu.x, window.innerWidth - 230),
        top: Math.min(menu.y, window.innerHeight - 180),
      }}
    >
      {items.map((it) => (
        <button
          key={it.label}
          type="button"
          role="menuitem"
          className="flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left text-13 text-ink hover:bg-surface-sunken focus-visible:bg-surface-sunken focus-visible:outline-none"
          onClick={() => {
            onClose();
            it.run();
          }}
        >
          <Icon name={it.icon} className="size-3.5 text-ink-3" />
          <span className="flex-1">{it.label}</span>
          {it.hint}
        </button>
      ))}
    </div>
  );
}

function Legend() {
  const line = (cls: string) => (
    <svg width="26" height="8" aria-hidden="true" className="shrink-0">
      <path d="M1,4 L25,4" className={`gedge ${cls}`} />
    </svg>
  );
  return (
    <div className="pointer-events-none absolute bottom-2.5 left-2.5 flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-line bg-surface/90 px-2.5 py-1.5 text-11-5 text-ink-3 max-sm:hidden">
      <span className="inline-flex items-center gap-1.5">{line("gedge-legend-dep")}first → then</span>
      <span className="inline-flex items-center gap-1.5">{line("gedge-legend-src")}source</span>
      <span className="inline-flex items-center gap-1.5">{line("gedge-legend-own")}applies</span>
      <span className="inline-flex items-center gap-1.5">{line("gedge-legend-blk")}waiting</span>
    </div>
  );
}

/** The graph as nested lists: one step per column, first to last. */
function Outline({
  layout,
  selectedId,
  onSelect,
  onOpen,
}: {
  layout: Layout;
  selectedId?: string;
  onSelect: (id: string) => void;
  onOpen: (id: string) => void;
}) {
  const block = useMemo(() => blockage(layout.index), [layout.index]);
  return (
    <div className="min-h-0 flex-1 overflow-auto rounded-card border border-line bg-surface px-4 py-3">
      <ol className="m-0 list-none p-0">
        {layout.layers.map((ids, l) =>
          ids.length ? (
            // biome-ignore lint/suspicious/noArrayIndexKey: layers are positional
            <li key={l} className="mb-3">
              <h4 className="mb-1 text-12 font-semibold text-ink-3">
                Step {l + 1}
                {l === 0 ? " · goes first" : ""}
              </h4>
              <ul className="m-0 list-none p-0">
                {ids.map((id) => {
                  const n = layout.index.nodes.get(id);
                  if (!n) return null;
                  return (
                    <li key={id}>
                      <button
                        type="button"
                        aria-pressed={id === selectedId}
                        className="w-full rounded-lg px-2 py-1 text-left text-12-5 hover:bg-surface-sunken aria-pressed:bg-c-soft"
                        onClick={() => onSelect(id)}
                        onDoubleClick={() => onOpen(id)}
                        onKeyDown={(e) => {
                          if (e.key === "Enter") {
                            e.preventDefault();
                            e.stopPropagation();
                            onOpen(id);
                          }
                        }}
                      >
                        {nodeLabel(layout.index, n, block.waitingOn.get(id))}
                      </button>
                    </li>
                  );
                })}
              </ul>
            </li>
          ) : null,
        )}
      </ol>
    </div>
  );
}

export function GraphView({
  cluster,
  graph,
  label,
  resources,
  actions,
  selectedId,
  onSelect,
  onOpen,
  rootId,
  dimmed,
  onFocusMode,
  onEscape,
  header,
  outline = false,
  notice,
  className = "",
  ref,
}: GraphViewProps) {
  const cache = useRef(createLayoutCache());
  const layout = useMemo(() => cache.current(graph), [graph]);
  const block = useMemo(() => blockage(layout.index), [layout.index]);
  const motion = useClusterMotion(cluster.name);
  const { marching, requested } = useWaves(cluster.name, layout);
  const canvas = useRef<GraphHandle>(null);
  const [menu, setMenu] = useState<Menu | undefined>();
  useImperativeHandle(
    ref,
    () => ({
      focus: () => canvas.current?.focus(),
      move: (d) => canvas.current?.move(d),
      centre: (id) => canvas.current?.centre(id),
      fit: () => canvas.current?.fit(),
      zoom: (f) => canvas.current?.zoom(f),
    }),
    [],
  );

  useKeys({
    graphFit: !outline && (() => canvas.current?.fit()),
    graphZoomIn: !outline && (() => canvas.current?.zoom(1.25)),
    graphZoomOut: !outline && (() => canvas.current?.zoom(0.8)),
    graphFocus: onFocusMode && (() => selectedId && onFocusMode(selectedId)),
  });

  return (
    <div className={`flex min-h-0 flex-1 flex-col gap-2 ${className}`}>
      {header?.({
        nodes: layout.index.nodes.size,
        edges: layout.edges.length + layout.hidden.length,
        truncated: Boolean(graph.truncated),
      })}
      {notice}
      {outline ? (
        <Outline layout={layout} selectedId={selectedId} onSelect={onSelect} onOpen={onOpen} />
      ) : (
        <div className="relative flex min-h-0 flex-1 flex-col">
          <GraphCanvas
            ref={canvas}
            layout={layout}
            label={label}
            selectedId={selectedId}
            rootId={rootId}
            onSelect={onSelect}
            onOpen={onOpen}
            onMenu={(id, x, y) => setMenu({ id, x, y })}
            onEscape={onEscape}
            dimmed={dimmed}
            motion={motion}
            requested={requested}
            marching={marching}
            blockage={block}
          />
          <Legend />
          <div className="absolute right-2.5 bottom-2.5 flex flex-col rounded-lg border border-line bg-surface shadow-control">
            <button
              type="button"
              className={CTRL}
              title="Zoom in (+)"
              aria-label="Zoom in"
              onClick={() => canvas.current?.zoom(1.25)}
            >
              <Icon name="plus" className="size-3.5" />
            </button>
            <button
              type="button"
              className={CTRL}
              title="Zoom out (−)"
              aria-label="Zoom out"
              onClick={() => canvas.current?.zoom(0.8)}
            >
              <Icon name="minus" className="size-3.5" />
            </button>
            <button
              type="button"
              className={CTRL}
              title="Fit to screen (0)"
              aria-label="Fit to screen"
              onClick={() => canvas.current?.fit()}
            >
              <Icon name="fit" className="size-3.5" />
            </button>
          </div>
        </div>
      )}
      {menu && (
        <NodeMenu
          menu={menu}
          r={resources.get(menu.id)}
          group={isGroupId(menu.id)}
          actions={actions}
          onFocusMode={onFocusMode}
          onOpen={onOpen}
          onClose={() => setMenu(undefined)}
        />
      )}
    </div>
  );
}
