// The graph screens, loaded on demand (React.lazy) so the main bundle does not grow:
// - ClusterGraph: the Graph view of the Flux list pages, with Focus mode for big clusters;
// - LineageGraph: the detail page's "Manages" graph: upstream on the left, the object in
//   the middle, dependents and owned inventory (collapsed per kind) on the right.
// Both expand group nodes in place (double click, Enter, the menu): a group the hub
// collapsed is refetched with expand=, and its members appear where it was.

import {
  type ReactNode,
  type Ref,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
} from "react";
import type { ClusterInfo, GraphResponse, Resource } from "../../api/types";
import {
  collapseOwned,
  DEFAULT_HOPS,
  expandFromItems,
  focusGraph,
  isGroupId,
  lineage,
  MAX_HOPS,
  SUGGEST_FOCUS_OVER,
} from "../../lib/graph";
import { filterResources, type StatusFilter } from "../../lib/resourceRows";
import { useGraph } from "../../lib/useGraph";
import { type GraphExpansion, useGraphExpansion } from "../../lib/useGraphExpansion";
import type { useResourceActions } from "../../lib/useResourceActions";
import { setViewPrefs, useViewPrefs } from "../../lib/viewPrefs";
import { Empty } from "../Empty";
import { Icon } from "../Icon";
import { SEG, SEG_BTN } from "../SidePanel";
import { KeyHint } from "../Status";
import type { GraphHandle } from "./GraphCanvas";
import { type GraphMeta, GraphView, metaText } from "./GraphView";

type Actions = ReturnType<typeof useResourceActions>;

/**
 * The group nodes still waiting for the hub's expanded graph. While that request is in
 * flight the previous graph is shown; afterwards a group the hub did not expand (an older
 * hub) is expanded from the resources list instead.
 */
function useExpandedGraph(
  full: GraphResponse | undefined,
  expanding: boolean,
  exp: GraphExpansion,
  items: readonly Resource[],
): { graph: GraphResponse | undefined; pending: ReadonlySet<string> | undefined } {
  return useMemo(() => {
    if (!full) return { graph: undefined, pending: undefined };
    if (!expanding) return { graph: expandFromItems(full, exp.expanded, items), pending: undefined };
    const groups = new Set(full.nodes.filter((n) => isGroupId(n.id)).map((n) => n.id));
    const pending = new Set(exp.hub.filter((id) => groups.has(id)));
    return { graph: full, pending: pending.size ? pending : undefined };
  }, [full, expanding, exp.expanded, exp.hub, items]);
}

/** Expands a group node: one the hub collapsed (in `full`) refetches with expand=. */
function expandGroup(exp: GraphExpansion, full: GraphResponse | undefined, id: string): void {
  if (exp.expanded.has(id)) return;
  exp.expand(id, Boolean(full?.nodes.some((n) => n.id === id)));
}

function CollapseGroups({ exp }: { exp: GraphExpansion }) {
  if (!exp.expanded.size) return null;
  return (
    <button
      type="button"
      className={CHIP}
      onClick={exp.clear}
      aria-label="Collapse groups"
      title="Collapse groups"
    >
      <Icon name="layers" className="size-3.5" />
      <span className="@max-lg:sr-only" aria-hidden="true">
        Collapse groups
      </span>
    </button>
  );
}

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
  outline = false,
  ref,
}: {
  cluster: ClusterInfo;
  items: readonly Resource[];
  /** The Outline view of the list page: the same graph as steps. */
  outline?: boolean;
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
  // Flux objects are never grouped, so the Flux graph has groups only from an older hub.
  const exp = useGraphExpansion(`${cluster.name}|flux`);
  const {
    graph: fetched,
    loading,
    expanding,
    error,
  } = useGraph(cluster.name, items, {
    kinds: "flux",
    focus,
    hops: focus ? hops : undefined,
    expand: exp.hub,
  });
  const { graph: full, pending } = useExpandedGraph(fetched, expanding, exp, items);
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
      // A group is not an object to focus on.
      if (isGroupId(id)) return;
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

  const collapse = <CollapseGroups exp={exp} />;
  const graphToolbar = focus ? (
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
      {collapse}
    </>
  ) : (
    <>
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
      {collapse}
    </>
  );
  const notice = big ? (
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
  ) : undefined;

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
        else if (isGroupId(id)) expandGroup(exp, fetched, id);
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
      pending={pending}
      outline={outline}
      header={(meta) => (
        // One line: Focus controls on the left, the graph's size on the right (into the
        // Focus chip's tooltip once the bar is narrow). The view switch is in the list toolbar.
        <div className="@container shrink-0">
          <div
            className="flex min-h-8 min-w-0 items-center gap-2 text-12-5 text-ink-3"
            title={metaText(meta)}
          >
            {graphToolbar}
            <span className="ml-auto whitespace-nowrap tabular-nums @max-md:hidden" aria-live="polite">
              {metaText(meta)}
            </span>
          </div>
        </div>
      )}
      notice={notice}
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
  outline = false,
  header,
}: {
  cluster: ClusterInfo;
  root: Resource;
  items: readonly Resource[];
  selectedId?: string;
  onSelect: (id: string) => void;
  onOpen: (r: Pick<Resource, "kind" | "namespace" | "name" | "group">) => void;
  actions: Actions;
  outline?: boolean;
  /**
   * The section's one-line header (the page owns its title and view switch), given the
   * graph's size once known and this graph's extra controls.
   */
  header: (meta: GraphMeta | undefined, extra: ReactNode) => ReactNode;
}) {
  const exp = useGraphExpansion(`${cluster.name}|lineage|${root.id}`);
  const {
    graph: fetched,
    loading,
    expanding,
    error,
  } = useGraph(cluster.name, items, {
    kinds: "all",
    focus: root.id,
    hops: MAX_HOPS,
    expand: exp.hub,
  });
  const { graph: full, pending } = useExpandedGraph(fetched, expanding, exp, items);
  const byId = useMemo(() => new Map(items.map((r) => [r.id, r])), [items]);
  const graph = useMemo(
    () => (full ? collapseOwned(lineage(full, root.id), root.id, exp.expanded) : undefined),
    [full, root.id, exp.expanded],
  );

  if (error && !graph) {
    return (
      <>
        {header(undefined, null)}
        <Empty title="Couldn't load the graph" alert>
          {error.message}
        </Empty>
      </>
    );
  }
  if (!graph) {
    return (
      <>
        {header(undefined, null)}
        <Empty>{loading ? "Loading the graph…" : "No graph"}</Empty>
      </>
    );
  }
  if (graph.nodes.length <= 1) {
    return (
      <>
        {header(undefined, null)}
        <p className="text-ink-3">
          {root.name} has no dependencies, sources or managed objects that Eddy knows about.
        </p>
      </>
    );
  }
  const collapse = exp.expanded.size > 0 ? <CollapseGroups exp={exp} /> : null;
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
        if (isGroupId(id)) return expandGroup(exp, fetched, id);
        if (id === root.id) return;
        const n = graph.nodes.find((x) => x.id === id);
        if (n?.name && !n.missing)
          onOpen({ kind: n.kind, namespace: n.namespace, name: n.name, group: n.group });
      }}
      outline={outline}
      pending={pending}
      header={(meta) => header(meta, collapse)}
    />
  );
}
