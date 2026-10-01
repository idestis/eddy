// A layered (Sugiyama-style) layout for the dependency graph, left → right: sources first,
// then everything in the order Flux applies it. It is small and deterministic, so the
// same topology always lands in the same place, and status changes never move a node:
// callers cache the layout by topologyKey() (createLayoutCache).
//
// Steps: pick the layering edges (cycles broken, owns-into-sources ignored), longest-path
// layers from the roots, dummy nodes for edges that skip layers, barycenter sweeps to cut
// crossings, then vertical placement that pulls each node towards its neighbours.

import type { GraphEdgeType, GraphResponse } from "../api/types";
import {
  type FlowEdge,
  type GraphIndex,
  indexGraph,
  isGroupId,
  isPureSource,
  nodeOrder,
  topologyKey,
} from "./graph";

export const NODE_W = 236;
export const NODE_H = 62;
const GAP_X = 92;
const GAP_Y = 16;
const DUMMY_H = 10;
const DUMMY_GAP = 6;
const SWEEPS = 8;
const PLACEMENT_PASSES = 6;

export interface LayoutNode {
  id: string;
  /** Top-left corner. */
  x: number;
  y: number;
  layer: number;
  /** Position within the layer, top to bottom. */
  order: number;
}

export interface LayoutEdge extends FlowEdge {
  /** From the upstream node's right edge to the downstream node's left edge. */
  points: Array<[number, number]>;
}

export interface Layout {
  key: string;
  index: GraphIndex;
  nodes: ReadonlyMap<string, LayoutNode>;
  /** Drawn edges; every one points right. */
  edges: readonly LayoutEdge[];
  /** Edges not drawn: cycle breakers and an owner's link back to its own source. */
  hidden: readonly FlowEdge[];
  /** Real node ids per layer, top to bottom. */
  layers: readonly (readonly string[])[];
  width: number;
  height: number;
  /**
   * Set when the layout kept the previous one's positions (a group was expanded in place):
   * the canvas glides the new nodes in and keeps the view where it is.
   */
  anchored?: boolean;
}

const PRIORITY: Record<GraphEdgeType, number> = { dependsOn: 0, source: 1, owns: 2 };

/** Picks the edges that order the layers. Exported for tests. */
export function layeringEdges(index: GraphIndex): { dag: FlowEdge[]; hidden: FlowEdge[] } {
  const out = new Map<string, string[]>();
  const reaches = (from: string, target: string): boolean => {
    const stack = [from];
    const seen = new Set([from]);
    while (stack.length) {
      const id = stack.pop() as string;
      if (id === target) return true;
      for (const n of out.get(id) ?? []) {
        if (!seen.has(n)) {
          seen.add(n);
          stack.push(n);
        }
      }
    }
    return false;
  };
  const dag: FlowEdge[] = [];
  const hidden: FlowEdge[] = [];
  const sorted = [...index.edges].sort(
    (a, b) => PRIORITY[a.type] - PRIORITY[b.type] || a.key.localeCompare(b.key),
  );
  for (const e of sorted) {
    const to = index.nodes.get(e.to);
    // Sources stay in the first column even when a Kustomization applies them (Flux bootstrap).
    if (e.type === "owns" && to && isPureSource(to.kind)) {
      hidden.push(e);
      continue;
    }
    // An edge that closes a cycle is dropped; dependsOn and source edges win over owns.
    if (reaches(e.to, e.from)) {
      hidden.push(e);
      continue;
    }
    out.set(e.from, [...(out.get(e.from) ?? []), e.to]);
    dag.push(e);
  }
  return { dag, hidden };
}

/** Longest path from the roots: a node sits one column right of its latest upstream. Exported for tests. */
export function assignLayers(ids: readonly string[], dag: readonly FlowEdge[]): Map<string, number> {
  const indeg = new Map(ids.map((id) => [id, 0]));
  const next = new Map<string, string[]>();
  for (const e of dag) {
    indeg.set(e.to, (indeg.get(e.to) ?? 0) + 1);
    next.set(e.from, [...(next.get(e.from) ?? []), e.to]);
  }
  const layer = new Map(ids.map((id) => [id, 0]));
  const queue = ids.filter((id) => (indeg.get(id) ?? 0) === 0);
  for (let i = 0; i < queue.length; i++) {
    const id = queue[i] as string;
    for (const n of next.get(id) ?? []) {
      layer.set(n, Math.max(layer.get(n) ?? 0, (layer.get(id) ?? 0) + 1));
      const d = (indeg.get(n) ?? 0) - 1;
      indeg.set(n, d);
      if (d === 0) queue.push(n);
    }
  }
  return layer;
}

interface Item {
  id: string;
  dummy: boolean;
  up: Item[];
  down: Item[];
  pos: number;
  y: number;
}

function crossings(upper: readonly Item[], lower: readonly Item[]): number {
  const pairs: Array<[number, number]> = [];
  for (const u of upper) for (const d of u.down) if (lower.includes(d)) pairs.push([u.pos, d.pos]);
  let n = 0;
  for (let i = 0; i < pairs.length; i++) {
    const [a1, b1] = pairs[i] as [number, number];
    for (let j = i + 1; j < pairs.length; j++) {
      const [a2, b2] = pairs[j] as [number, number];
      if ((a1 - a2) * (b1 - b2) < 0) n++;
    }
  }
  return n;
}

const height = (it: Item) => (it.dummy ? DUMMY_H : NODE_H);
const sep = (a: Item, b: Item) => (height(a) + height(b)) / 2 + (a.dummy || b.dummy ? DUMMY_GAP : GAP_Y);

function place(items: Item[], desired: number[]): void {
  const n = items.length;
  if (!n) return;
  const fwd = [...desired];
  for (let i = 1; i < n; i++)
    fwd[i] = Math.max(
      desired[i] as number,
      (fwd[i - 1] as number) + sep(items[i - 1] as Item, items[i] as Item),
    );
  const back = [...desired];
  for (let i = n - 2; i >= 0; i--)
    back[i] = Math.min(
      desired[i] as number,
      (back[i + 1] as number) - sep(items[i] as Item, items[i + 1] as Item),
    );
  for (let i = 0; i < n; i++) (items[i] as Item).y = ((fwd[i] as number) + (back[i] as number)) / 2;
}

/** Lays out a graph. Pure; use createLayoutCache to skip it when only statuses changed. */
export function layoutGraph(g: GraphResponse): Layout {
  const index = indexGraph(g);
  const { dag, hidden } = layeringEdges(index);
  const layerOf = assignLayers(index.ids, dag);
  const depth = Math.max(0, ...layerOf.values()) + 1;

  const items = new Map<string, Item>();
  const columns: Item[][] = Array.from({ length: depth }, () => []);
  const add = (id: string, layer: number, dummy: boolean) => {
    const it: Item = { id, dummy, up: [], down: [], pos: 0, y: 0 };
    items.set(id, it);
    columns[layer]?.push(it);
    return it;
  };
  for (const id of index.ids) add(id, layerOf.get(id) ?? 0, false);

  // Long edges become chains through dummy items, one per skipped layer.
  const chains = new Map<string, Item[]>();
  for (const e of dag) {
    const a = items.get(e.from) as Item;
    const b = items.get(e.to) as Item;
    const la = layerOf.get(e.from) ?? 0;
    const lb = layerOf.get(e.to) ?? 0;
    const chain = [a];
    for (let l = la + 1; l < lb; l++) chain.push(add(`${e.key}#${l}`, l, true));
    chain.push(b);
    for (let i = 0; i + 1 < chain.length; i++) {
      (chain[i] as Item).down.push(chain[i + 1] as Item);
      (chain[i + 1] as Item).up.push(chain[i] as Item);
    }
    chains.set(e.key, chain);
  }

  // Crossing reduction: barycenter sweeps down and up, keeping the best order seen.
  const renumber = (col: Item[]) => {
    col.forEach((it, i) => {
      it.pos = i;
    });
  };
  columns.forEach(renumber);
  const total = () =>
    columns.reduce((n, col, l) => n + (l ? crossings(columns[l - 1] as Item[], col) : 0), 0);
  let best = columns.map((c) => [...c]);
  let bestCount = total();
  for (let s = 0; s < SWEEPS && bestCount > 0; s++) {
    const down = s % 2 === 0;
    const range = down
      ? columns.map((_, i) => i).slice(1)
      : columns
          .map((_, i) => i)
          .slice(0, -1)
          .reverse();
    for (const l of range) {
      const col = columns[l] as Item[];
      const bary = new Map<Item, number>();
      for (const it of col) {
        const ns = down ? it.up : it.down;
        bary.set(it, ns.length ? ns.reduce((sum, n) => sum + n.pos, 0) / ns.length : it.pos);
      }
      col.sort((a, b) => (bary.get(a) ?? 0) - (bary.get(b) ?? 0) || a.pos - b.pos);
      renumber(col);
    }
    const count = total();
    if (count < bestCount) {
      bestCount = count;
      best = columns.map((c) => [...c]);
    }
  }
  best.forEach((col, l) => {
    columns[l] = col;
    renumber(col);
  });

  // Vertical placement: stack each column, then pull items towards their neighbours.
  for (const col of columns) {
    let y = 0;
    col.forEach((it, i) => {
      if (i) y += sep(col[i - 1] as Item, it);
      it.y = y;
    });
  }
  const tallest = Math.max(...columns.map((c) => (c.length ? (c[c.length - 1] as Item).y : 0)));
  for (const col of columns) {
    const h = col.length ? (col[col.length - 1] as Item).y : 0;
    for (const it of col) it.y += (tallest - h) / 2;
  }
  for (let p = 0; p < PLACEMENT_PASSES; p++) {
    const down = p % 2 === 0;
    const order = down ? columns : [...columns].reverse();
    for (const col of order) {
      const desired = col.map((it) => {
        const ns = [...(down ? it.up : it.down)];
        if (!ns.length) {
          const other = down ? it.down : it.up;
          return other.length ? other.reduce((s, n) => s + n.y, 0) / other.length : it.y;
        }
        return ns.reduce((s, n) => s + n.y, 0) / ns.length;
      });
      place(col, desired);
    }
  }
  let minY = Number.POSITIVE_INFINITY;
  let maxY = Number.NEGATIVE_INFINITY;
  for (const it of items.values()) {
    minY = Math.min(minY, it.y - height(it) / 2);
    maxY = Math.max(maxY, it.y + height(it) / 2);
  }
  if (!Number.isFinite(minY)) minY = maxY = 0;
  const colX = (l: number) => l * (NODE_W + GAP_X);

  const nodes = new Map<string, LayoutNode>();
  const layers: string[][] = [];
  columns.forEach((col, l) => {
    const real = col.filter((it) => !it.dummy);
    layers.push(real.map((it) => it.id));
    real.forEach((it, order) => {
      nodes.set(it.id, { id: it.id, x: colX(l), y: Math.round(it.y - minY - NODE_H / 2), layer: l, order });
    });
  });
  const edges: LayoutEdge[] = dag.map((e) => {
    const chain = chains.get(e.key) ?? [];
    const pts: Array<[number, number]> = [];
    chain.forEach((it, i) => {
      const y = Math.round(it.y - minY);
      if (i === 0) pts.push([colX(layerOf.get(e.from) ?? 0) + NODE_W, y]);
      else if (i === chain.length - 1) pts.push([colX(layerOf.get(it.id) ?? 0), y]);
      else {
        const dl = (layerOf.get(e.from) ?? 0) + i;
        pts.push([colX(dl), y], [colX(dl) + NODE_W, y]);
      }
    });
    return { ...e, points: pts };
  });
  return {
    key: topologyKey(g),
    index,
    nodes,
    edges,
    hidden,
    layers,
    width: colX(depth - 1) + NODE_W,
    height: Math.round(maxY - minY),
  };
}

const STEP_X = NODE_W + GAP_X;
const STEP_Y = NODE_H + GAP_Y;

/**
 * Lays out `g` keeping `prev`'s positions, when `g` is `prev` with group nodes expanded:
 * every node that left is a group, and every new node is a member of one of them or sits
 * downstream of a new node. Members stack where their group was; what they own goes one
 * column right of them; nodes below them in a column move down to make room. Anything
 * else (a collapse, a topology change) returns undefined, and the caller lays out afresh.
 * Exported for tests.
 */
export function anchoredLayout(prev: Layout, g: GraphResponse): Layout | undefined {
  const fresh = layoutGraph(g);
  const { index } = fresh;
  const removed = [...prev.nodes.keys()].filter((id) => !index.nodes.has(id));
  const added = index.ids.filter((id) => !prev.nodes.has(id));
  if (!added.length || !removed.length || !removed.every(isGroupId)) return undefined;

  const pos = new Map<string, { x: number; y: number }>();
  const isNew = new Set(added);
  for (const [id, n] of prev.nodes) if (index.nodes.has(id)) pos.set(id, { x: n.x, y: n.y });
  // Members of each expanded group, in the group's place.
  for (const gid of removed) {
    const group = prev.index.nodes.get(gid);
    const at = prev.nodes.get(gid);
    if (!group?.owner || !at) continue;
    const members = added
      .filter(
        (id) =>
          !pos.has(id) &&
          index.nodes.get(id)?.kind === group.kind &&
          (index.up.get(id) ?? []).some((e) => e.type === "owns" && e.from === group.owner),
      )
      .map((id) => index.nodes.get(id))
      .filter((n) => n !== undefined)
      .sort(nodeOrder);
    members.forEach((n, i) => {
      pos.set(n.id, { x: at.x, y: at.y + i * STEP_Y });
    });
  }
  // What the new nodes lead to: one column right of the first placed upstream.
  const below = new Map<string, number>();
  for (let progress = true; progress; ) {
    progress = false;
    for (const id of added) {
      if (pos.has(id)) continue;
      const parent = (index.up.get(id) ?? []).find((e) => isNew.has(e.from) && pos.has(e.from))?.from;
      const p = parent ? pos.get(parent) : undefined;
      if (!parent || !p) continue;
      const k = below.get(parent) ?? 0;
      below.set(parent, k + 1);
      pos.set(id, { x: p.x + STEP_X, y: p.y + k * STEP_Y });
      progress = true;
    }
  }
  if (added.some((id) => !pos.has(id))) return undefined;

  // Columns: nodes only move down, and old ones keep their place over new ones.
  const columns = new Map<number, string[]>();
  for (const [id, p] of pos) {
    const l = Math.max(0, Math.round(p.x / STEP_X));
    columns.set(l, [...(columns.get(l) ?? []), id]);
  }
  const nodes = new Map<string, LayoutNode>();
  const depth = Math.max(0, ...columns.keys()) + 1;
  const layers: string[][] = Array.from({ length: depth }, () => []);
  let height = 0;
  for (const [l, ids] of columns) {
    ids.sort((a, b) => {
      const pa = pos.get(a) as { y: number };
      const pb = pos.get(b) as { y: number };
      return pa.y - pb.y || Number(isNew.has(a)) - Number(isNew.has(b)) || a.localeCompare(b);
    });
    let floor = Number.NEGATIVE_INFINITY;
    ids.forEach((id, order) => {
      const p = pos.get(id) as { x: number; y: number };
      const y = Math.max(p.y, floor);
      floor = y + STEP_Y;
      height = Math.max(height, y + NODE_H);
      nodes.set(id, { id, x: l * STEP_X, y, layer: l, order });
    });
    layers[l] = ids;
  }

  const old = new Map(prev.edges.map((e) => [e.key, e]));
  const edges: LayoutEdge[] = fresh.edges.map((e) => {
    const a = nodes.get(e.from) as LayoutNode;
    const b = nodes.get(e.to) as LayoutNode;
    const was = old.get(e.key);
    const pa = prev.nodes.get(e.from);
    const pb = prev.nodes.get(e.to);
    if (was && pa && pb && pa.x === a.x && pa.y === a.y && pb.x === b.x && pb.y === b.y) return was;
    return {
      ...e,
      points: [
        [a.x + NODE_W, a.y + NODE_H / 2],
        [b.x, b.y + NODE_H / 2],
      ],
    };
  });
  return {
    key: fresh.key,
    index,
    nodes,
    edges,
    hidden: fresh.hidden,
    layers,
    width: (depth - 1) * STEP_X + NODE_W,
    height,
    anchored: true,
  };
}

/**
 * Remembers the last layout and reuses it while the topology is the same, so a status
 * change re-renders nodes in place. The index is refreshed so nodes carry live data. When
 * groups are expanded, the existing nodes keep their positions (anchoredLayout).
 */
export function createLayoutCache(): (g: GraphResponse) => Layout {
  let last: Layout | undefined;
  return (g) => {
    const key = topologyKey(g);
    if (last && last.key === key) {
      last = { ...last, index: indexGraph(g) };
      return last;
    }
    last = (last && anchoredLayout(last, g)) || layoutGraph(g);
    return last;
  };
}

/** A smooth path through the points: horizontal tangents at each bend. */
export function edgePath(points: ReadonlyArray<readonly [number, number]>): string {
  const [first, ...rest] = points;
  if (!first) return "";
  let d = `M${first[0]},${first[1]}`;
  let prev = first;
  for (const p of rest) {
    if (p[1] === prev[1]) d += `L${p[0]},${p[1]}`;
    else {
      const mx = (prev[0] + p[0]) / 2;
      d += `C${mx},${prev[1]} ${mx},${p[1]} ${p[0]},${p[1]}`;
    }
    prev = p;
  }
  return d;
}
