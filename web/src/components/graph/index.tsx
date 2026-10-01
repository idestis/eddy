// The graph screens, loaded on demand (React.lazy) so the main bundle does not grow:
// - ClusterGraph: the Graph view of the Flux list pages, with Focus mode for big clusters;
// - LineageGraph: the detail page's "Manages" graph: upstream on the left, the object in
//   the middle, dependents and owned inventory (collapsed per kind) on the right.

import { type Ref, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from "react";
import type { ClusterInfo, GraphResponse, Resource } from "../../api/types";
import {
  collapseOwned,
  DEFAULT_HOPS,
  focusGraph,
  isGroupId,
  lineage,
  MAX_HOPS,
  SUGGEST_FOCUS_OVER,
} from "../../lib/graph";
import { kindInfo } from "../../lib/kinds";
import { filterResources, type StatusFilter } from "../../lib/resourceRows";
import { useGraph } from "../../lib/useGraph";
import type { useResourceActions } from "../../lib/useResourceActions";
import { setViewPrefs, useViewPrefs } from "../../lib/viewPrefs";
import { Empty } from "../Empty";
import { Icon } from "../Icon";
import { SEG, SEG_BTN } from "../SidePanel";
import { KeyHint } from "../Status";
import type { GraphHandle } from "./GraphCanvas";
import { GraphView } from "./GraphView";

type Actions = ReturnType<typeof useResourceActions>;

export interface ClusterGraphHandle extends GraphHandle {
  /** Selects and focuses the first node the find text matches. */
  findFirst: () => void;
}

const CHIP =
  "inline-flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-[11px] text-12-5 whitespace-nowrap text-ink-2 hover:border-line-strong disabled:opacity-50";

function Hops({ hops, onChange }: { hops: number; onChange: (n: number) => void }) {
  return (
    <fieldset className={`${SEG} m-0 shrink-0 p-[2px]`}>
      <legend className="sr-only">Hops around the focus</legend>
      {[1, 2, 3].map((n) => (
        <button
          key={n}
          type="button"
          aria-pressed={hops === n}
          className={`${SEG_BTN} h-[26px] flex-none px-2 aria-pressed:bg-ink`}
          title={`${n} hop${n > 1 ? "s" : ""} around the focus`}
          onClick={() => onChange(n)}
        >
          {n}
        </button>
      ))}
    </fieldset>
  );
}

export function ClusterGraph({
  cluster,
  items,
  namespace,
  status,
  text,
  selectedId,
  onSelect,
  onOpen,
  onEscape,
  actions,
  ref,
}: {
  cluster: ClusterInfo;
  items: readonly Resource[];
  namespace?: string;
  status?: StatusFilter;
  text: string;
  selectedId?: string;
  onSelect: (id: string) => void;
  onOpen: (r: Resource) => void;
  onEscape?: () => boolean;
  actions: Actions;
  ref?: Ref<ClusterGraphHandle>;
}) {
  const saved = useViewPrefs().graphHops;
  const [focus, setFocus] = useState<string | undefined>();
  const hops = Math.min(saved ?? DEFAULT_HOPS, MAX_HOPS);
  const {
    graph: full,
    loading,
    error,
  } = useGraph(cluster.name, items, {
    kinds: "flux",
    focus,
    hops: focus ? hops : undefined,
  });
  const byId = useMemo(() => new Map(items.map((r) => [r.id, r])), [items]);

  // The namespace filter narrows the graph; status and text dim what does not match.
  const graph = useMemo((): GraphResponse | undefined => {
    if (!full) return undefined;
    let g = full;
    if (focus && g.nodes.some((n) => n.id === focus)) g = focusGraph(g, focus, hops);
    if (!namespace) return g;
    const keep = new Set(g.nodes.filter((n) => n.namespace === namespace).map((n) => n.id));
    return {
      ...g,
      nodes: g.nodes.filter((n) => keep.has(n.id)),
      edges: g.edges.filter((e) => keep.has(e.from) && keep.has(e.to)),
    };
  }, [full, focus, hops, namespace]);

  const matches = useMemo(() => {
    if (!graph || (!text && !status)) return undefined;
    const ids = new Set(graph.nodes.map((n) => n.id));
    const hit = filterResources(
      items.filter((r) => ids.has(r.id)),
      { text, status },
    );
    return new Set(hit.map((r) => r.id));
  }, [graph, items, text, status]);
  const dimmed = useMemo(
    () =>
      matches && graph ? new Set(graph.nodes.filter((n) => !matches.has(n.id)).map((n) => n.id)) : undefined,
    [matches, graph],
  );

  const view = useRef<GraphHandle>(null);
  const firstMatch = useMemo(() => {
    if (!text || !matches?.size || !graph) return undefined;
    return graph.nodes.find((n) => matches.has(n.id))?.id;
  }, [text, matches, graph]);
  // Typing in the find box (/) centres the first match.
  useEffect(() => {
    if (firstMatch) view.current?.centre(firstMatch);
  }, [firstMatch]);

  useImperativeHandle(
    ref,
    () => ({
      focus: () => view.current?.focus(),
      move: (d) => view.current?.move(d),
      centre: (id) => view.current?.centre(id),
      fit: () => view.current?.fit(),
      zoom: (f) => view.current?.zoom(f),
      findFirst: () => {
        if (firstMatch) onSelect(firstMatch);
        requestAnimationFrame(() => view.current?.focus());
      },
    }),
    [firstMatch, onSelect],
  );

  const toggleFocus = useCallback(
    (id: string) => {
      setFocus((cur) => (cur === id ? undefined : id));
      onSelect(id);
    },
    [onSelect],
  );

  if (error && !graph) {
    return (
      <Empty title="Couldn't load the graph" alert>
        {error.message}
      </Empty>
    );
  }
  if (!graph) return <Empty>{loading ? "Loading the graph…" : "No graph"}</Empty>;
  if (!graph.nodes.length) {
    return (
      <Empty title="No Flux objects here">
        {namespace
          ? `Nothing in ${namespace} has Flux dependencies to draw.`
          : `${cluster.name} has no Flux objects you can see.`}
      </Empty>
    );
  }

  const focusName = focus ? (byId.get(focus)?.name ?? focus) : undefined;
  const selected = selectedId ? byId.get(selectedId) : undefined;
  const big = !focus && graph.nodes.length > SUGGEST_FOCUS_OVER;

  return (
    <GraphView
      ref={view}
      cluster={cluster}
      graph={graph}
      label={`Flux dependency graph of ${cluster.name}${focusName ? `, focused on ${focusName}` : ""}`}
      resources={byId}
      actions={actions}
      selectedId={selectedId}
      onSelect={onSelect}
      onOpen={(id) => {
        const r = byId.get(id);
        if (r) onOpen(r);
        else if (isGroupId(id)) toggleFocus(id);
      }}
      onFocusMode={toggleFocus}
      onEscape={() => {
        if (focus) {
          setFocus(undefined);
          return true;
        }
        return onEscape?.() ?? false;
      }}
      dimmed={dimmed}
      toolbar={
        focus ? (
          <>
            <span className="inline-flex h-8 items-center gap-1.5 rounded-full border border-c/50 bg-c-soft px-[11px] text-12-5 text-ink">
              <Icon name="focus" className="size-3.5 text-c" />
              Focus <span className="font-mono font-medium">{focusName}</span>
              <button
                type="button"
                className="-mr-1 inline-flex size-5 items-center justify-center rounded-full text-ink-3 hover:bg-surface-sunken hover:text-ink"
                aria-label="Show the whole graph"
                title="Show the whole graph (Esc)"
                onClick={() => setFocus(undefined)}
              >
                <Icon name="x" className="size-3" />
              </button>
            </span>
            <Hops hops={hops} onChange={(n) => setViewPrefs({ graphHops: n })} />
          </>
        ) : (
          <button
            type="button"
            className={CHIP}
            disabled={!selected}
            onClick={() => selected && toggleFocus(selected.id)}
            title="Show the selected object and its neighbours only"
          >
            <Icon name="focus" className="size-3.5" />
            Focus
            <KeyHint id="graphFocus" />
          </button>
        )
      }
      notice={
        big ? (
          <div className="flex shrink-0 items-center gap-2.5 rounded-tile border border-attn/40 bg-attn/8 px-3 py-2 text-12-5 text-ink-2">
            <Icon name="info" className="size-4 text-attn" />
            <span className="flex-1">
              {graph.nodes.length} objects is a lot to read at once. Focus on one to see it and its {hops}-hop
              neighbourhood.
            </span>
            <button
              type="button"
              className={CHIP}
              disabled={!selected}
              onClick={() => selected && toggleFocus(selected.id)}
            >
              {selected ? `Focus on ${selected.name}` : "Select a node to focus"}
            </button>
          </div>
        ) : undefined
      }
    />
  );
}

export function LineageGraph({
  cluster,
  root,
  items,
  selectedId,
  onSelect,
  onOpen,
  actions,
}: {
  cluster: ClusterInfo;
  root: Resource;
  items: readonly Resource[];
  selectedId?: string;
  onSelect: (id: string) => void;
  onOpen: (r: Pick<Resource, "kind" | "namespace" | "name" | "group">) => void;
  actions: Actions;
}) {
  const {
    graph: full,
    loading,
    error,
  } = useGraph(cluster.name, items, {
    kinds: "all",
    focus: root.id,
    hops: MAX_HOPS,
  });
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());
  const byId = useMemo(() => new Map(items.map((r) => [r.id, r])), [items]);
  const graph = useMemo(
    () => (full ? collapseOwned(lineage(full, root.id), root.id, expanded, items) : undefined),
    [full, root.id, expanded, items],
  );
  const toggle = (id: string) =>
    setExpanded((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  if (error && !graph) {
    return (
      <Empty title="Couldn't load the graph" alert>
        {error.message}
      </Empty>
    );
  }
  if (!graph) return <Empty>{loading ? "Loading the graph…" : "No graph"}</Empty>;
  if (graph.nodes.length <= 1) {
    return (
      <p className="text-ink-3">
        {root.name} has no dependencies, sources or managed objects that Eddy knows about.
      </p>
    );
  }
  const flux = kindInfo(root.kind).flux;
  return (
    <GraphView
      className="h-[480px] flex-none"
      cluster={cluster}
      graph={graph}
      rootId={root.id}
      label={`Dependencies of ${root.kind} ${root.name}: upstream on the left, what it manages on the right`}
      resources={byId}
      actions={actions}
      selectedId={selectedId ?? root.id}
      onSelect={onSelect}
      onOpen={(id) => {
        if (isGroupId(id)) return toggle(id);
        if (id === root.id) return;
        const n = graph.nodes.find((x) => x.id === id);
        if (n?.name && !n.missing)
          onOpen({ kind: n.kind, namespace: n.namespace, name: n.name, group: n.group });
      }}
      toolbar={
        <>
          <span>
            {flux ? "Upstream ← this object → downstream" : "Owners ← this object → what it manages"}
          </span>
          {expanded.size > 0 && (
            <button type="button" className={CHIP} onClick={() => setExpanded(new Set())}>
              <Icon name="layers" className="size-3.5" />
              Collapse groups
            </button>
          )}
        </>
      }
    />
  );
}
