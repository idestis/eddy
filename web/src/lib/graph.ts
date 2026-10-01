// The Flux dependency graph: who goes first and who second. The hub serves it from
// GET …/graph (internal/hub/fleet_graph.go); an older hub answers 404, and then
// buildGraph() makes the same shape from the resources list, with the same edge
// directions, collapsing, expanding and focus rules. Flux objects are never collapsed:
// their dependsOn and source edges are the graph. Everything here is pure, so the list page, the
// detail page and the tests share it. Layout lives in graphLayout.ts.
//
// Wire edges follow the spec fields: `dependsOn` and `source` point from the dependent to
// what it needs, `owns` from the owner to the owned object. The layout wants the order of
// events instead ("first → then"), so flowOf() turns every edge into upstream → downstream.

import type { GraphEdge, GraphEdgeType, GraphNode, GraphResponse, Ref, Resource, Status } from "../api/types";
import { STATUS_RANK } from "./format";
import { kindInfo } from "./kinds";

/** Non-Flux siblings of one kind under one owner collapse into a group node above this many (as the hub). */
export const COLLAPSE_OVER = 20;
/** The hub expands at most this many groups per request. */
export const MAX_EXPAND = 20;
/** The hub's node cap; beyond it the response is truncated. */
export const MAX_NODES = 2000;
export const DEFAULT_HOPS = 2;
export const MAX_HOPS = 6;
/** Above this many nodes the list page suggests Focus mode. */
export const SUGGEST_FOCUS_OVER = 300;

const refKey = (r: Ref): string => `${r.group}/${r.kind}/${r.namespace}/${r.name}`;

export const GROUP_PREFIX = "group:";
export const groupId = (ownerId: string, kind: string): string => `${GROUP_PREFIX}${ownerId}/${kind}`;
export const isGroupId = (id: string): boolean => id.startsWith(GROUP_PREFIX);

/** A Flux object Eddy watches (not an inventory-only row of a Flux kind). */
export function isFluxResource(r: Pick<Resource, "kind" | "group" | "inventoryOnly">): boolean {
  const info = kindInfo(r.kind);
  return info.flux && !r.inventoryOnly && info.group === r.group;
}

/** Git, OCI and Helm repositories and Buckets: the first column. HelmCharts have a source of their own. */
export function isPureSource(kind: string): boolean {
  const info = kindInfo(kind);
  return info.flux && info.section === "flux" && !info.hasSource && info.group.startsWith("source.");
}

/** True when the object waits for a dependency (Ready=False, DependencyNotReady). */
export function isBlocked(r: Pick<Resource, "blocked" | "status" | "conditions">): boolean {
  if (r.blocked !== undefined) return r.blocked;
  return (
    r.status === "reconciling" &&
    Boolean(
      r.conditions?.some(
        (c) => c.type === "Ready" && c.status === "False" && c.reason === "DependencyNotReady",
      ),
    )
  );
}

/** The worst status of a breakdown, for a group node's colour. */
export function worstStatus(statuses: Partial<Record<Status, number>> | undefined): Status {
  let worst: Status = "unknown";
  let rank = Number.POSITIVE_INFINITY;
  for (const [s, n] of Object.entries(statuses ?? {}) as [Status, number][]) {
    if (n > 0 && STATUS_RANK[s] < rank) {
      worst = s;
      rank = STATUS_RANK[s];
    }
  }
  return worst;
}

export function nodeOf(r: Resource): GraphNode {
  return {
    id: r.id,
    kind: r.kind,
    group: r.group,
    namespace: r.namespace,
    name: r.name,
    status: r.status,
    message: r.message,
    blocked: isBlocked(r) || undefined,
    revision: r.revision,
    lastChanged: r.lastChanged,
    inventoryOnly: r.inventoryOnly,
  };
}

export interface GraphQueryOptions {
  kinds: "flux" | "all";
  focus?: string;
  hops?: number;
  /** Group ids returned as their members (at most MAX_EXPAND go to the hub). */
  expand?: readonly string[];
}

const TYPE_ORDER: Record<GraphEdgeType, number> = { dependsOn: 0, source: 1, owns: 2 };

/**
 * The client-side twin of the hub's graph builder, for hubs without GET …/graph. Rows are
 * the user's list (already filtered by the hub); `owns` and `source` edges are drawn only
 * between rows in it, and a dependsOn target that is not becomes a `missing` node.
 */
export function buildGraph(items: readonly Resource[], opts: GraphQueryOptions): GraphResponse {
  const rows = new Map<string, Resource>();
  for (const r of items) if (opts.kinds === "all" || isFluxResource(r)) rows.set(r.id, r);

  const raw: GraphEdge[] = [];
  const missing = new Map<string, Ref>();
  const children = new Map<string, Map<string, string[]>>();
  for (const r of rows.values()) {
    for (const d of r.dependsOn ?? []) {
      const id = refKey(d);
      if (!rows.has(id)) missing.set(id, d);
      raw.push({ from: r.id, to: id, type: "dependsOn" });
    }
    if (r.source && rows.has(refKey(r.source)))
      raw.push({ from: r.id, to: refKey(r.source), type: "source" });
    if (r.owner) {
      const o = refKey(r.owner);
      if (o !== r.id && rows.has(o)) {
        raw.push({ from: o, to: r.id, type: "owns" });
        // Flux objects are never collapsed, nor hidden under a collapsed row.
        if (isFluxResource(r)) continue;
        const byKind = children.get(o) ?? new Map<string, string[]>();
        byKind.set(r.kind, [...(byKind.get(r.kind) ?? []), r.id]);
        children.set(o, byKind);
      }
    }
  }

  // The focus and its owners stay expanded.
  const keep = new Set<string>();
  for (let id = opts.focus; id && !keep.has(id); ) {
    keep.add(id);
    const owner = rows.get(id)?.owner;
    id = owner && rows.has(refKey(owner)) ? refKey(owner) : undefined;
  }

  const expand = new Set(opts.expand ?? []);
  const expandedMembers = new Set<string>();
  const nodes = new Map<string, GraphNode>();
  const rep = new Map<string, string>(); // row → group id, or "" when hidden under a collapsed row
  const collapsed: string[] = [];
  for (const [owner, byKind] of children) {
    for (const [kind, ids] of byKind) {
      const gid = groupId(owner, kind);
      if (expand.has(gid) && ids.length > COLLAPSE_OVER) {
        for (const id of ids) expandedMembers.add(id);
        continue;
      }
      if (ids.length <= COLLAPSE_OVER || ids.some((id) => keep.has(id))) continue;
      const first = rows.get(ids[0] ?? "");
      const n: GraphNode = {
        id: gid,
        kind,
        group: first?.group ?? "",
        namespace: first?.namespace ?? "",
        owner,
        count: ids.length,
        statuses: {},
      };
      for (const id of ids) {
        const r = rows.get(id);
        if (!r) continue;
        if (r.group !== n.group) n.group = "";
        if (r.namespace !== n.namespace) n.namespace = "";
        const statuses = n.statuses ?? {};
        statuses[r.status] = (statuses[r.status] ?? 0) + 1;
        rep.set(id, gid);
        collapsed.push(id);
      }
      n.status = worstStatus(n.statuses);
      nodes.set(gid, n);
    }
  }
  while (collapsed.length) {
    const id = collapsed.pop() as string;
    for (const kids of children.get(id)?.values() ?? []) {
      for (const k of kids) {
        if (!rep.has(k)) {
          rep.set(k, "");
          collapsed.push(k);
        }
      }
    }
  }
  const repOf = (id: string) => rep.get(id) ?? id;
  for (const [id, r] of rows) if (!rep.has(id)) nodes.set(id, nodeOf(r));
  for (const [id, ref] of missing) {
    nodes.set(id, { ...ref, id, status: "unknown", missing: true });
  }

  const seen = new Set<string>();
  const edges: GraphEdge[] = [];
  for (const e of raw) {
    const from = repOf(e.from);
    const to = repOf(e.to);
    const key = `${from}\n${to}\n${e.type}`;
    if (!from || !to || from === to || seen.has(key)) continue;
    seen.add(key);
    edges.push({ from, to, type: e.type });
  }

  let order = [...nodes.keys()];
  let truncated = false;
  if (opts.focus) {
    order = bfs(order, edges, opts.focus, Math.min(opts.hops ?? DEFAULT_HOPS, MAX_HOPS));
    if (order.length > MAX_NODES) {
      order = order.slice(0, MAX_NODES);
      truncated = true;
    }
  } else if (order.length > MAX_NODES) {
    const rank = (id: string) => (expandedMembers.has(id) ? 1 : rankForCut(nodes.get(id)));
    order.sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
    order = order.slice(0, MAX_NODES);
    truncated = true;
  }
  const keepIds = new Set(order);
  return {
    nodes: order.flatMap((id) => {
      const n = nodes.get(id);
      return n ? [n] : [];
    }),
    edges: edges
      .filter((e) => keepIds.has(e.from) && keepIds.has(e.to))
      .sort(
        (a, b) =>
          a.from.localeCompare(b.from) || a.to.localeCompare(b.to) || TYPE_ORDER[a.type] - TYPE_ORDER[b.type],
      ),
    truncated,
  };
}

/** Flux kinds first, then (expanded members, ranked by the caller) watched kinds, then inventory-only rows. */
function rankForCut(n: GraphNode | undefined): number {
  if (!n || n.inventoryOnly) return 3;
  return kindInfo(n.kind).flux ? 0 : 2;
}

/** Ids within `hops` edges of `focus`, in either direction, nearest first. */
function bfs(ids: readonly string[], edges: readonly GraphEdge[], focus: string, hops: number): string[] {
  if (!ids.includes(focus)) return [];
  const adj = new Map<string, string[]>();
  for (const e of edges) {
    adj.set(e.from, [...(adj.get(e.from) ?? []), e.to]);
    adj.set(e.to, [...(adj.get(e.to) ?? []), e.from]);
  }
  const dist = new Map([[focus, 0]]);
  const order = [focus];
  for (let i = 0; i < order.length; i++) {
    const id = order[i] as string;
    const d = dist.get(id) ?? 0;
    if (d >= hops) continue;
    for (const n of [...(adj.get(id) ?? [])].sort()) {
      if (dist.has(n)) continue;
      dist.set(n, d + 1);
      order.push(n);
    }
  }
  return order;
}

/** Focus mode on a graph already in hand: the focus and everything within `hops` edges. */
export function focusGraph(g: GraphResponse, focus: string, hops: number): GraphResponse {
  const keep = new Set(
    bfs(
      g.nodes.map((n) => n.id),
      g.edges,
      focus,
      hops,
    ),
  );
  if (!keep.size) return g;
  return {
    ...g,
    nodes: g.nodes.filter((n) => keep.has(n.id)),
    edges: g.edges.filter((e) => keep.has(e.from) && keep.has(e.to)),
  };
}

// Flow: every edge as upstream → downstream.

export interface FlowEdge {
  /** Stable id: "<upstream>→<downstream>:<type>". */
  key: string;
  /** What goes first: the dependency, the source, the owner. */
  from: string;
  /** What goes second. */
  to: string;
  type: GraphEdgeType;
}

export function flowOf(e: GraphEdge): FlowEdge {
  const [from, to] = e.type === "owns" ? [e.from, e.to] : [e.to, e.from];
  return { key: `${from}→${to}:${e.type}`, from, to, type: e.type };
}

export interface GraphIndex {
  nodes: ReadonlyMap<string, GraphNode>;
  /** Node ids in a stable order (kind, namespace, name). */
  ids: readonly string[];
  edges: readonly FlowEdge[];
  up: ReadonlyMap<string, readonly FlowEdge[]>;
  down: ReadonlyMap<string, readonly FlowEdge[]>;
}

export function nodeOrder(a: GraphNode, b: GraphNode): number {
  return (
    kindInfo(a.kind).order - kindInfo(b.kind).order ||
    a.namespace.localeCompare(b.namespace) ||
    (a.name ?? a.id).localeCompare(b.name ?? b.id) ||
    a.id.localeCompare(b.id)
  );
}

export function indexGraph(g: GraphResponse): GraphIndex {
  const nodes = new Map(g.nodes.map((n) => [n.id, n]));
  const ids = [...g.nodes].sort(nodeOrder).map((n) => n.id);
  const seen = new Set<string>();
  const edges: FlowEdge[] = [];
  for (const e of g.edges) {
    const f = flowOf(e);
    if (!nodes.has(f.from) || !nodes.has(f.to) || f.from === f.to || seen.has(f.key)) continue;
    seen.add(f.key);
    edges.push(f);
  }
  edges.sort((a, b) => a.key.localeCompare(b.key));
  const up = new Map<string, FlowEdge[]>();
  const down = new Map<string, FlowEdge[]>();
  for (const e of edges) {
    down.set(e.from, [...(down.get(e.from) ?? []), e]);
    up.set(e.to, [...(up.get(e.to) ?? []), e]);
  }
  return { nodes, ids, edges, up, down };
}

/**
 * Everything upstream (dir "up") or downstream ("down") of `start`, not including it.
 * Cycles are fine: each node is visited once. `follow` limits the edges walked.
 */
export function reach(
  index: GraphIndex,
  start: string,
  dir: "up" | "down",
  follow: (e: FlowEdge) => boolean = () => true,
): Set<string> {
  const out = new Set<string>();
  const stack = [start];
  const adj = dir === "up" ? index.up : index.down;
  while (stack.length) {
    const id = stack.pop() as string;
    for (const e of adj.get(id) ?? []) {
      if (!follow(e)) continue;
      const next = dir === "up" ? e.from : e.to;
      if (next === start || out.has(next)) continue;
      out.add(next);
      stack.push(next);
    }
  }
  return out;
}

/** The edges a reconcile pushes onward: dependents, consumers of a source, Flux objects a Kustomization applies. */
export function cascadeFollow(index: GraphIndex): (e: FlowEdge) => boolean {
  return (e) => {
    if (e.type !== "owns") return true;
    const n = index.nodes.get(e.to);
    return Boolean(n && !isGroupId(n.id) && kindInfo(n.kind).flux && !n.inventoryOnly);
  };
}

/**
 * The detail page's view of one object: its upstream (sources, dependencies, owners) on
 * the left, and on the right its dependents, consumers and the Flux objects it applies,
 * plus its own owned inventory (collapsed per kind by collapseOwned). Owned non-Flux rows
 * of other objects are left out, so the picture stays about this object.
 */
export function lineage(g: GraphResponse, root: string): GraphResponse {
  const index = indexGraph(g);
  if (!index.nodes.has(root)) return { ...g, nodes: [], edges: [] };
  const up = reach(index, root, "up");
  const flux = cascadeFollow(index);
  const down = reach(index, root, "down", (e) => e.from === root || flux(e));
  const keep = new Set([root, ...up, ...down]);
  return {
    ...g,
    nodes: g.nodes.filter((n) => keep.has(n.id)),
    edges: g.edges.filter((e) => keep.has(e.from) && keep.has(e.to)),
  };
}

/**
 * The fallback for a hub that ignored `expand` (older hubs): group nodes whose id is in
 * `expanded` are replaced by their members from `items` (the resources list), with an
 * `owns` edge from the group's owner. Other edges of the group are dropped. Returns `g`
 * itself when nothing is expanded this way.
 */
export function expandFromItems(
  g: GraphResponse,
  expanded: ReadonlySet<string>,
  items: readonly Resource[],
): GraphResponse {
  const groups = g.nodes.filter((n) => isGroupId(n.id) && n.owner && expanded.has(n.id));
  if (!groups.length) return g;
  const gone = new Set(groups.map((n) => n.id));
  const nodes = new Map(g.nodes.filter((n) => !gone.has(n.id)).map((n) => [n.id, n]));
  const edges = g.edges.filter((e) => !gone.has(e.from) && !gone.has(e.to));
  const want = new Map(groups.map((n) => [groupId(n.owner as string, n.kind), n.owner as string]));
  for (const r of items) {
    if (!r.owner) continue;
    const owner = want.get(groupId(refKey(r.owner), r.kind));
    if (!owner || nodes.has(r.id)) continue;
    nodes.set(r.id, nodeOf(r));
    edges.push({ from: owner, to: r.id, type: "owns" });
  }
  return { ...g, nodes: [...nodes.values()], edges };
}

/**
 * Collapses `owner`'s owned non-Flux rows into one group node per kind ("Deployments 3")
 * when there are at least two, unless the group's id is in `expanded`. Flux objects stay
 * nodes of their own. Status breakdowns come from the rows, so they stay live. Groups the
 * hub collapsed are expanded by the hub (expand=), or by expandFromItems beforehand.
 */
export function collapseOwned(g: GraphResponse, owner: string, expanded: ReadonlySet<string>): GraphResponse {
  const byId = new Map(g.nodes.map((n) => [n.id, n]));
  const nodes = new Map(byId);
  const kids = new Map<string, string[]>();
  for (const e of g.edges) {
    const n = byId.get(e.to);
    if (e.type === "owns" && e.from === owner && n && !isGroupId(n.id) && !kindInfo(n.kind).flux) {
      kids.set(n.kind, [...(kids.get(n.kind) ?? []), n.id]);
    }
  }
  const replaced = new Map<string, string>();
  for (const [kind, ids] of kids) {
    const gid = groupId(owner, kind);
    if (ids.length < 2 || expanded.has(gid)) continue;
    const statuses: Partial<Record<Status, number>> = {};
    let ns: string | undefined;
    let group: string | undefined;
    for (const id of ids) {
      const n = byId.get(id);
      if (!n) continue;
      const s = n.status ?? "unknown";
      statuses[s] = (statuses[s] ?? 0) + 1;
      ns = ns === undefined || ns === n.namespace ? n.namespace : "";
      group = group === undefined || group === n.group ? n.group : "";
      nodes.delete(id);
      replaced.set(id, gid);
    }
    nodes.set(gid, {
      id: gid,
      kind,
      group: group ?? "",
      namespace: ns ?? "",
      owner,
      count: ids.length,
      statuses,
      status: worstStatus(statuses),
    });
  }
  const seen = new Set<string>();
  const edges: GraphEdge[] = [];
  for (const e of g.edges) {
    const from = replaced.get(e.from) ?? e.from;
    const to = replaced.get(e.to) ?? e.to;
    const key = `${from}\n${to}\n${e.type}`;
    if (from === to || seen.has(key) || !nodes.has(from) || !nodes.has(to)) continue;
    seen.add(key);
    edges.push({ from, to, type: e.type });
  }
  return { ...g, nodes: [...nodes.values()], edges };
}

/**
 * Takes status, message, revision and the blocked flag from the live resources list (SSE
 * keeps it fresh), so the graph changes without refetching. Group nodes recount their
 * members. Nodes whose data did not change keep their identity, for memoised rendering.
 */
export function withLive(g: GraphResponse, byId: ReadonlyMap<string, Resource>, items: readonly Resource[]) {
  let groups: Map<string, Partial<Record<Status, number>>> | undefined;
  const groupStatuses = (n: GraphNode) => {
    if (!groups) {
      groups = new Map();
      for (const r of items) {
        if (!r.owner) continue;
        const gid = groupId(refKey(r.owner), r.kind);
        const s = groups.get(gid) ?? {};
        s[r.status] = (s[r.status] ?? 0) + 1;
        groups.set(gid, s);
      }
    }
    return groups.get(n.id) ?? n.statuses;
  };
  let changed = false;
  const nodes = g.nodes.map((n) => {
    if (isGroupId(n.id)) {
      const statuses = groupStatuses(n);
      if (JSON.stringify(statuses) === JSON.stringify(n.statuses)) return n;
      changed = true;
      return { ...n, statuses, status: worstStatus(statuses) };
    }
    const r = byId.get(n.id);
    if (!r) return n;
    const blocked = isBlocked(r) || undefined;
    if (
      r.status === n.status &&
      r.message === n.message &&
      r.revision === n.revision &&
      r.lastChanged === n.lastChanged &&
      blocked === n.blocked
    )
      return n;
    changed = true;
    return {
      ...n,
      status: r.status,
      message: r.message,
      revision: r.revision,
      lastChanged: r.lastChanged,
      blocked,
    };
  });
  return changed ? { ...g, nodes } : g;
}

/** What a graph's layout depends on: its node ids and edges, never statuses. */
export function topologyKey(g: GraphResponse): string {
  const ids = g.nodes.map((n) => n.id).sort();
  const edges = g.edges.map((e) => `${e.from}>${e.to}>${e.type}`).sort();
  return `${ids.join("|")}#${edges.join("|")}`;
}

/** What the graph is built from in a resources list, so a status-only delta does not refetch it. */
export function resourcesTopologyKey(items: readonly Resource[], kinds: "flux" | "all"): string {
  const parts: string[] = [];
  for (const r of items) {
    if (kinds === "flux" && !isFluxResource(r)) continue;
    parts.push(
      `${r.id}<${r.owner ? refKey(r.owner) : ""}<${r.source ? refKey(r.source) : ""}<${(r.dependsOn ?? []).map(refKey).join(",")}`,
    );
  }
  parts.sort();
  // A short hash keeps the query key small.
  let h = 0;
  for (const p of parts) for (let i = 0; i < p.length; i++) h = (Math.imul(h, 31) + p.charCodeAt(i)) | 0;
  return `${parts.length}:${(h >>> 0).toString(36)}`;
}

// What is waiting on what.

export interface Blockage {
  /** dependsOn edges (flow keys) on a path from a failing or blocked object to one waiting on it. */
  edges: Set<string>;
  /** Each blocked object → the object it ultimately waits on. */
  waitingOn: Map<string, string>;
}

const stuck = (n: GraphNode | undefined): boolean =>
  Boolean(n && (n.missing || n.status === "failed" || n.blocked));

/**
 * For every blocked object, the first upstream dependency that is not ready and not
 * itself waiting (a failure, a missing object, one still reconciling); and the dependsOn
 * edges from failing or blocked objects to the blocked objects downstream of them, which
 * the graph tints faintly.
 */
export function blockage(index: GraphIndex): Blockage {
  const edges = new Set<string>();
  const waitingOn = new Map<string, string>();
  for (const id of index.ids) {
    const n = index.nodes.get(id);
    if (!stuck(n)) continue;
    const stack = [id];
    const seen = new Set([id]);
    while (stack.length) {
      const at = stack.pop() as string;
      for (const e of index.down.get(at) ?? []) {
        if (e.type !== "dependsOn" || !index.nodes.get(e.to)?.blocked) continue;
        edges.add(e.key);
        if (!seen.has(e.to)) {
          seen.add(e.to);
          stack.push(e.to);
        }
      }
    }
  }
  for (const id of index.ids) {
    if (!index.nodes.get(id)?.blocked) continue;
    const cause = rootCause(index, id);
    if (cause) waitingOn.set(id, cause);
  }
  return { edges, waitingOn };
}

function rootCause(index: GraphIndex, id: string): string | undefined {
  const seen = new Set([id]);
  let frontier = [id];
  let fallback: string | undefined;
  while (frontier.length) {
    const next: string[] = [];
    for (const at of frontier) {
      for (const e of index.up.get(at) ?? []) {
        if (e.type !== "dependsOn" || seen.has(e.from)) continue;
        seen.add(e.from);
        const n = index.nodes.get(e.from);
        if (!n) continue;
        if (n.blocked) {
          fallback ??= n.id;
          next.push(n.id);
        } else if (n.missing || n.status !== "ready") {
          return n.id;
        }
      }
    }
    frontier = next;
  }
  return fallback;
}

/** "Kustomization apps, failed, depends on infra-configs" for screen readers. */
export function nodeLabel(index: GraphIndex, n: GraphNode, waitingOn?: string, expanding = false): string {
  const name = n.name ?? n.id;
  const parts: string[] = [];
  if (isGroupId(n.id)) {
    parts.push(`${n.count ?? 0} ${kindInfo(n.kind).plural}`);
    const s = Object.entries(n.statuses ?? {})
      .filter(([, c]) => (c ?? 0) > 0)
      .map(([st, c]) => `${c} ${st}`)
      .join(", ");
    if (s) parts.push(s);
    parts.push(expanding ? "expanding" : "group, press Enter or double-click to expand");
  } else {
    parts.push(`${n.kind} ${name}`);
    parts.push(
      n.missing
        ? "not found or not visible"
        : n.inventoryOnly
          ? "managed by Flux, not watched by Eddy"
          : n.blocked
            ? "waiting"
            : (n.status ?? "unknown"),
    );
  }
  const names = (ids: string[]) => ids.map((i) => index.nodes.get(i)?.name ?? index.nodes.get(i)?.kind ?? i);
  const deps = (index.up.get(n.id) ?? []).filter((e) => e.type === "dependsOn").map((e) => e.from);
  if (deps.length) parts.push(`depends on ${names(deps).join(", ")}`);
  const src = (index.up.get(n.id) ?? []).filter((e) => e.type === "source").map((e) => e.from);
  if (src.length) parts.push(`source ${names(src).join(", ")}`);
  if (waitingOn) parts.push(`waiting on ${names([waitingOn]).join("")}`);
  return parts.join(", ");
}
