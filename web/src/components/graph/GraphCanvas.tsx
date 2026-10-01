// The dependency graph's drawing surface: positioned node cards over one SVG of edges,
// inside a world that pans and zooms. The world's transform and the node positions are
// set through element.style (CSSOM), which the strict CSP allows; every animation is a
// class in styles/app.css, so reduced motion turns it off.
//
// Positions come from a cached layout (lib/graphLayout.ts) and change only when nodes or
// edges come and go; then the nodes glide to their new places. Status changes re-render
// cards in place: a pulse in the new status colour, a ring while reconciling.

import {
  type CSSProperties,
  type KeyboardEvent,
  memo,
  type PointerEvent,
  type Ref,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { GraphNode } from "../../api/types";
import { age, shortRevision } from "../../lib/format";
import { type Blockage, isGroupId, nodeLabel, reach } from "../../lib/graph";
import { edgePath, type Layout, type LayoutEdge, NODE_H, NODE_W } from "../../lib/graphLayout";
import { pressMatches } from "../../lib/keys";
import { kindInfo } from "../../lib/kinds";
import type { ClusterMotion, Requested } from "../../lib/liveMotion";
import { DURATION, prefersReducedMotion } from "../../lib/motion";
import { Icon } from "../Icon";
import { AttentionIcon, StatusIcon } from "../Status";

const PAD = 40;
const MIN_ZOOM = 0.12;
const MAX_ZOOM = 2;
/** Above this many nodes, only nodes near the viewport are in the DOM. */
const CULL_OVER = 120;
/** The first view is never smaller than this: names stay legible. */
const READABLE_ZOOM = 0.72;

export interface GraphHandle {
  /** Focuses the selected node (or the first one). */
  focus: () => void;
  move: (dir: "up" | "down" | "left" | "right") => void;
  /** Pans so the node is in the middle. */
  centre: (id: string) => void;
  fit: () => void;
  zoom: (factor: number) => void;
}

export interface GraphCanvasProps {
  layout: Layout;
  label: string;
  selectedId?: string;
  /** The detail page's object, drawn with a ring. */
  rootId?: string;
  onSelect: (id: string) => void;
  onOpen: (id: string) => void;
  onMenu?: (id: string, x: number, y: number) => void;
  /** Escape with nothing left to clear here. Return true when the caller handled it. */
  onEscape?: () => boolean;
  /** Ids the filters dim. */
  dimmed?: ReadonlySet<string>;
  motion: ClusterMotion;
  requested: ReadonlyMap<string, Requested>;
  /** Flow edge keys of a reconcile wave in progress. */
  marching: ReadonlySet<string>;
  blockage: Blockage;
  className?: string;
  ref?: Ref<GraphHandle>;
}

interface View {
  x: number;
  y: number;
  k: number;
}

const clamp = (v: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, v));
export const nodeDomId = (id: string) => `gn-${id.replace(/[^\w-]/g, "_")}`;

const CARD_TONE: Record<string, string> = {
  failed: "border-bad/55 bg-[color-mix(in_oklab,var(--color-bad)_6%,var(--color-surface))]",
  blocked: "border-attn/55 bg-[color-mix(in_oklab,var(--color-attn)_7%,var(--color-surface))]",
  reconciling: "border-run/45",
  suspended: "border-off/45",
  missing: "border-dashed border-line-strong bg-transparent text-ink-3",
};

interface CardProps {
  n: GraphNode;
  x: number;
  y: number;
  label: string;
  selected: boolean;
  root: boolean;
  tabStop: boolean;
  hl: boolean;
  dim: boolean;
  flash: string | undefined;
  flashAt: number | undefined;
  requested: Requested | undefined;
  waiting: string | undefined;
  onSelect: (id: string) => void;
  onOpen: (id: string) => void;
  onHover: (id: string | undefined) => void;
  onMenu?: (id: string, x: number, y: number) => void;
}

const NodeCard = memo(function NodeCard({
  n,
  x,
  y,
  label,
  selected,
  root,
  tabStop,
  hl,
  dim,
  flash,
  flashAt,
  requested,
  waiting,
  onSelect,
  onOpen,
  onHover,
  onMenu,
}: CardProps) {
  const info = kindInfo(n.kind);
  const group = isGroupId(n.id);
  const status = n.status ?? "unknown";
  const tone = n.missing
    ? "missing"
    : n.blocked
      ? "blocked"
      : status === "failed" || status === "reconciling" || status === "suspended"
        ? status
        : "";
  const style: Record<string, string> = { transform: `translate(${x}px,${y}px)` };
  if (flashAt !== undefined) style["--flash-delay"] = `${flashAt - Date.now()}ms`;
  const second = n.missing
    ? "not found or not visible"
    : group
      ? Object.entries(n.statuses ?? {})
          .filter(([s, c]) => (c ?? 0) > 0 && s !== "ready")
          .map(([s, c]) => `${c} ${s}`)
          .join(" · ") || "all ready"
      : waiting
        ? `waiting on ${waiting}`
        : [n.namespace, shortRevision(n.revision)].filter(Boolean).join(" · ");
  return (
    <button
      type="button"
      id={nodeDomId(n.id)}
      tabIndex={tabStop ? 0 : -1}
      aria-label={label}
      aria-pressed={selected}
      data-status={n.missing ? undefined : status}
      data-blocked={n.blocked || undefined}
      data-flash={flash}
      data-hl={hl || undefined}
      data-dim={dim || undefined}
      data-root={root || undefined}
      title={n.message || undefined}
      className={`gnode flex flex-col items-stretch text-left justify-center gap-1 rounded-[12px] border border-line bg-surface px-2.5 shadow-control select-none outline-none hover:border-line-strong aria-pressed:border-c aria-pressed:shadow-[0_0_0_3px_color-mix(in_oklab,var(--c)_22%,transparent)] focus-visible:shadow-[0_0_0_3px_color-mix(in_oklab,var(--c)_40%,transparent)] ${CARD_TONE[tone] ?? ""} ${n.inventoryOnly ? "opacity-70" : ""}`}
      style={{ width: NODE_W, height: NODE_H, ...style } as CSSProperties}
      onClick={() => onSelect(n.id)}
      onDoubleClick={() => onOpen(n.id)}
      onPointerEnter={() => onHover(n.id)}
      onPointerLeave={() => onHover(undefined)}
      onContextMenu={(e) => {
        if (!onMenu) return;
        e.preventDefault();
        onSelect(n.id);
        onMenu(n.id, e.clientX, e.clientY);
      }}
    >
      <span className="flex min-w-0 items-center gap-1.5">
        <span key={`${status}:${n.blocked ? 1 : 0}`} className={`inline-flex ${flash ? "swap-in" : ""}`}>
          {n.missing ? (
            <Icon name="alert" className="size-4 shrink-0 text-ink-3" />
          ) : n.blocked ? (
            <AttentionIcon />
          ) : group || n.inventoryOnly ? (
            <Icon name={info.icon} className="size-4 shrink-0 text-ink-3" />
          ) : (
            <StatusIcon status={status} />
          )}
        </span>
        <span className="rounded-[5px] border border-line px-1 py-px font-mono text-10 font-semibold text-ink-2">
          {info.abbr}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-12-5 font-medium text-ink">
          {group ? info.plural : (n.name ?? n.id)}
        </span>
        {group ? (
          <span className="rounded-full bg-surface-sunken px-1.5 text-11-5 font-semibold text-ink-2 tabular-nums">
            {n.count}
          </span>
        ) : (
          <span className="shrink-0 text-11 text-ink-3 tabular-nums">{age(n.lastChanged)}</span>
        )}
      </span>
      <span className="flex min-w-0 items-center gap-1.5 pl-[22px] text-11-5 text-ink-3">
        <span
          key={second}
          className={`min-w-0 flex-1 truncate ${waiting ? "text-attn" : status === "failed" && !group ? "text-bad" : ""} ${flash ? "swap-fade" : ""}`}
        >
          {second}
        </span>
        {requested && (
          <span
            className="anim-fade-in inline-flex shrink-0 items-center gap-1 rounded-md bg-c-soft px-1 text-10-5 font-semibold text-c"
            title="Sent. Waiting for the cluster to report a new status."
          >
            <span className="requested-dot size-1.5 rounded-full bg-c" />
            requested
          </span>
        )}
      </span>
    </button>
  );
});

const EdgePath = memo(function EdgePath({
  e,
  hl,
  dim,
  blocked,
  march,
}: {
  e: LayoutEdge;
  hl: boolean;
  dim: boolean;
  blocked: boolean;
  march: boolean;
}) {
  return (
    <path
      className="gedge"
      d={edgePath(e.points)}
      data-type={e.type}
      data-hl={hl || undefined}
      data-dim={dim || undefined}
      data-blocked={blocked || undefined}
      data-march={march || undefined}
    />
  );
});

/** Arrow heads, one per edge state (markers do not inherit the path's colour). */
function Markers() {
  const arrow = (id: string) => (
    <marker
      id={id}
      viewBox="0 0 10 10"
      refX="9"
      refY="5"
      markerWidth="7"
      markerHeight="7"
      orient="auto-start-reverse"
    >
      <path d="M0,1 L9,5 L0,9 z" />
    </marker>
  );
  return (
    <defs>
      {arrow("gm-dep")}
      {arrow("gm-hl")}
      {arrow("gm-blk")}
    </defs>
  );
}

export function GraphCanvas({
  layout,
  label,
  selectedId,
  rootId,
  onSelect,
  onOpen,
  onMenu,
  onEscape,
  dimmed,
  motion,
  requested,
  marching,
  blockage,
  className = "",
  ref,
}: GraphCanvasProps) {
  const viewport = useRef<HTMLElement>(null);
  const world = useRef<HTMLDivElement>(null);
  const view = useRef<View>({ x: PAD, y: PAD, k: 1 });
  const userMoved = useRef(false);
  // The last fit: "readable" (first view) or "full" (the 0 key); resizes repeat it.
  const fitMode = useRef<"readable" | "full">("readable");
  const [hovered, setHovered] = useState<string | undefined>();
  const [box, setBox] = useState<{ x0: number; y0: number; x1: number; y1: number } | null>(null);
  const cull = layout.nodes.size > CULL_OVER;
  const frame = useRef(0);

  const measure = useCallback(() => {
    const el = viewport.current;
    if (!el || !cull) return;
    const { x, y, k } = view.current;
    const margin = 260 / k;
    setBox({
      x0: -x / k - margin,
      y0: -y / k - margin,
      x1: (el.clientWidth - x) / k + margin,
      y1: (el.clientHeight - y) / k + margin,
    });
  }, [cull]);

  const apply = useCallback(
    (animate = false) => {
      const w = world.current;
      if (!w) return;
      const { x, y, k } = view.current;
      w.dataset.animate = animate && !prefersReducedMotion() ? "true" : "false";
      w.style.transform = `translate(${x}px,${y}px) scale(${k})`;
      cancelAnimationFrame(frame.current);
      frame.current = requestAnimationFrame(measure);
    },
    [measure],
  );

  /**
   * Fits the whole graph. `readable` (the first view, and resizes) never goes below
   * READABLE_ZOOM: a graph too wide for that starts at its left, where the sources are,
   * and pans from there; the 0 key and the button fit everything.
   */
  const fitView = useCallback(
    (animate = true, readable = false) => {
      const el = viewport.current;
      if (!el) return;
      const w = Math.max(1, el.clientWidth);
      const h = Math.max(1, el.clientHeight);
      const fitK = clamp(
        Math.min((w - PAD * 2) / layout.width, (h - PAD * 2) / Math.max(layout.height, 1)),
        MIN_ZOOM,
        1.1,
      );
      const k = readable ? Math.max(fitK, Math.min(READABLE_ZOOM, 1.1)) : fitK;
      const wide = layout.width * k + PAD * 2 > w + 1;
      let x = wide ? PAD : (w - layout.width * k) / 2;
      // The detail page's object stays in the middle of a graph wider than the view.
      const root = readable && wide && rootId ? layout.nodes.get(rootId) : undefined;
      if (root) x = clamp(w / 2 - (root.x + NODE_W / 2) * k, w - PAD - layout.width * k, PAD);
      const y = layout.height * k + PAD * 2 > h + 1 ? PAD : (h - layout.height * k) / 2;
      view.current = { k, x, y };
      fitMode.current = readable ? "readable" : "full";
      userMoved.current = false;
      apply(animate);
    },
    [layout.width, layout.height, layout.nodes, rootId, apply],
  );

  const zoomAt = useCallback(
    (factor: number, cx?: number, cy?: number, animate = false) => {
      const el = viewport.current;
      if (!el) return;
      const { x, y, k } = view.current;
      const nk = clamp(k * factor, MIN_ZOOM, MAX_ZOOM);
      const px = cx ?? el.clientWidth / 2;
      const py = cy ?? el.clientHeight / 2;
      view.current = { k: nk, x: px - ((px - x) * nk) / k, y: py - ((py - y) * nk) / k };
      userMoved.current = true;
      apply(animate);
    },
    [apply],
  );

  const centre = useCallback(
    (id: string, onlyIfHidden = false, animate = true) => {
      const el = viewport.current;
      const n = layout.nodes.get(id);
      if (!el || !n) return;
      const { x, y, k } = view.current;
      const sx = x + n.x * k;
      const sy = y + n.y * k;
      const inside =
        sx >= 8 && sy >= 8 && sx + NODE_W * k <= el.clientWidth - 8 && sy + NODE_H * k <= el.clientHeight - 8;
      if (onlyIfHidden && inside) return;
      const kk = Math.max(k, 0.6);
      view.current = {
        k: kk,
        x: el.clientWidth / 2 - (n.x + NODE_W / 2) * kk,
        y: el.clientHeight / 2 - (n.y + NODE_H / 2) * kk,
      };
      userMoved.current = true;
      apply(animate);
    },
    [layout.nodes, apply],
  );

  // First layout: fit. A new topology: glide nodes, refit unless the user moved the view.
  const lastKey = useRef<string | undefined>(undefined);
  const [relayout, setRelayout] = useState(false);
  useLayoutEffect(() => {
    const first = lastKey.current === undefined;
    if (lastKey.current === layout.key) return;
    lastKey.current = layout.key;
    if (first) {
      fitView(false, true);
      return;
    }
    if (prefersReducedMotion()) {
      if (!userMoved.current) fitView(false, fitMode.current === "readable");
      return;
    }
    setRelayout(true);
    if (!userMoved.current) fitView(true, fitMode.current === "readable");
    const t = setTimeout(() => setRelayout(false), DURATION.slow + 80);
    return () => clearTimeout(t);
  }, [layout.key, fitView]);

  // Refit when the viewport changes size (side panel drag, window resize) and the user has not moved.
  useEffect(() => {
    const el = viewport.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() =>
      userMoved.current ? measure() : fitView(false, fitMode.current === "readable"),
    );
    ro.observe(el);
    return () => ro.disconnect();
  }, [fitView, measure]);

  // Wheel: pinch (ctrl) and mouse wheels zoom at the pointer; trackpad scrolling pans.
  useEffect(() => {
    const el = viewport.current;
    if (!el) return;
    const onWheel = (e: WheelEvent) => {
      e.preventDefault();
      const r = el.getBoundingClientRect();
      const mouseWheel =
        e.deltaMode !== 0 || (Math.abs(e.deltaY) >= 50 && e.deltaX === 0 && Number.isInteger(e.deltaY));
      if (e.ctrlKey || e.metaKey || mouseWheel) {
        zoomAt(Math.exp(-e.deltaY * (e.ctrlKey ? 0.01 : 0.0018)), e.clientX - r.left, e.clientY - r.top);
      } else {
        view.current = { ...view.current, x: view.current.x - e.deltaX, y: view.current.y - e.deltaY };
        userMoved.current = true;
        apply();
      }
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    return () => el.removeEventListener("wheel", onWheel);
  }, [zoomAt, apply]);

  // Drag to pan; two pointers pinch.
  const pointers = useRef(new Map<number, { x: number; y: number }>());
  const pinch = useRef<{ d: number } | null>(null);
  const onPointerDown = (e: PointerEvent<HTMLElement>) => {
    if ((e.target as Element).closest(".gnode, button") || e.button !== 0) return;
    e.currentTarget.setPointerCapture(e.pointerId);
    pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
    e.currentTarget.dataset.panning = "true";
    if (pointers.current.size === 2) {
      const [a, b] = [...pointers.current.values()] as [{ x: number; y: number }, { x: number; y: number }];
      pinch.current = { d: Math.hypot(a.x - b.x, a.y - b.y) };
    }
  };
  const onPointerMove = (e: PointerEvent<HTMLElement>) => {
    const prev = pointers.current.get(e.pointerId);
    if (!prev) return;
    const next = { x: e.clientX, y: e.clientY };
    pointers.current.set(e.pointerId, next);
    if (pointers.current.size === 2 && pinch.current) {
      const [a, b] = [...pointers.current.values()] as [{ x: number; y: number }, { x: number; y: number }];
      const d = Math.hypot(a.x - b.x, a.y - b.y);
      const r = e.currentTarget.getBoundingClientRect();
      zoomAt(d / pinch.current.d, (a.x + b.x) / 2 - r.left, (a.y + b.y) / 2 - r.top);
      pinch.current = { d };
      return;
    }
    view.current = {
      ...view.current,
      x: view.current.x + next.x - prev.x,
      y: view.current.y + next.y - prev.y,
    };
    userMoved.current = true;
    apply();
  };
  const onPointerUp = (e: PointerEvent<HTMLElement>) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinch.current = null;
    if (!pointers.current.size) delete e.currentTarget.dataset.panning;
  };

  // Keyboard: arrows and h j k l walk the graph, Enter opens, Escape clears.
  const focusNode = useCallback(
    (id: string) => {
      centre(id, true);
      requestAnimationFrame(() => document.getElementById(nodeDomId(id))?.focus({ preventScroll: true }));
    },
    [centre],
  );

  const neighbour = useCallback(
    (from: string | undefined, dir: "up" | "down" | "left" | "right"): string | undefined => {
      const ids = layout.layers.flat();
      const at = from ? layout.nodes.get(from) : undefined;
      if (!at) return layout.layers.find((l) => l.length)?.[0] ?? ids[0];
      if (dir === "up" || dir === "down") {
        const col = layout.layers[at.layer] ?? [];
        return col[at.order + (dir === "down" ? 1 : -1)] ?? from;
      }
      const edges = dir === "right" ? layout.index.down.get(at.id) : layout.index.up.get(at.id);
      const linked = (edges ?? []).map((e) => (dir === "right" ? e.to : e.from));
      const step = dir === "right" ? 1 : -1;
      let pool = linked.filter((id) => layout.nodes.has(id));
      if (!pool.length) {
        for (let l = at.layer + step; l >= 0 && l < layout.layers.length; l += step) {
          if (layout.layers[l]?.length) {
            pool = [...(layout.layers[l] ?? [])];
            break;
          }
        }
      }
      let best: string | undefined;
      let dist = Number.POSITIVE_INFINITY;
      for (const id of pool) {
        const n = layout.nodes.get(id);
        if (!n) continue;
        const d = Math.abs(n.y - at.y) + Math.abs(n.layer - at.layer) * 1000;
        if (d < dist) {
          dist = d;
          best = id;
        }
      }
      return best ?? from;
    },
    [layout],
  );

  const move = useCallback(
    (dir: "up" | "down" | "left" | "right") => {
      const next = neighbour(selectedId, dir);
      if (!next) return;
      onSelect(next);
      focusNode(next);
    },
    [neighbour, selectedId, onSelect, focusNode],
  );

  useImperativeHandle(
    ref,
    () => ({
      focus: () => {
        const id = selectedId && layout.nodes.has(selectedId) ? selectedId : neighbour(undefined, "down");
        if (id) focusNode(id);
        else viewport.current?.focus();
      },
      move,
      centre: (id) => centre(id),
      fit: () => fitView(true),
      zoom: (f) => zoomAt(f, undefined, undefined, true),
    }),
    [selectedId, layout.nodes, neighbour, focusNode, move, centre, fitView, zoomAt],
  );

  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if ((e.target as Element).closest("input, textarea, button:not(.gnode)")) return;
    let handled = true;
    if (pressMatches("down", e)) move("down");
    else if (pressMatches("up", e)) move("up");
    else if (e.key === "Enter") {
      if (selectedId) onOpen(selectedId);
    } else if (pressMatches("open", e)) move("right");
    else if (e.key === "Escape") {
      if (hovered) setHovered(undefined);
      else handled = onEscape?.() ?? false;
    } else if (pressMatches("back", e)) move("left");
    else if (e.key === "Home" || e.key === "End") {
      const ids = layout.layers.flat();
      const id = e.key === "Home" ? ids[0] : ids[ids.length - 1];
      if (id) {
        onSelect(id);
        focusNode(id);
      }
    } else if (e.key === "ContextMenu" || (e.shiftKey && e.key === "F10")) {
      const el = selectedId ? document.getElementById(nodeDomId(selectedId)) : null;
      const r = el?.getBoundingClientRect();
      if (selectedId && r && onMenu) onMenu(selectedId, r.left + 24, r.bottom - 4);
    } else handled = false;
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

  // Highlight: the hovered node's whole upstream and downstream chains.
  const chain = useMemo(() => {
    if (!hovered || !layout.nodes.has(hovered)) return undefined;
    return new Set([hovered, ...reach(layout.index, hovered, "up"), ...reach(layout.index, hovered, "down")]);
  }, [hovered, layout]);

  const visible = (x: number, y: number, w: number, h: number) =>
    !box || (x + w >= box.x0 && x <= box.x1 && y + h >= box.y0 && y <= box.y1);

  const tabStop =
    selectedId && layout.nodes.has(selectedId) ? selectedId : layout.layers.find((l) => l.length)?.[0];
  const now = Date.now();

  const svgW = layout.width + 20;
  const svgH = layout.height + 20;

  return (
    <section
      ref={viewport}
      className={`graph-viewport relative min-h-0 flex-1 overflow-hidden rounded-card border border-line bg-surface-side outline-none focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-c ${className}`}
      aria-roledescription="dependency graph"
      aria-label={label}
      onKeyDown={onKeyDown}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
      onScroll={(e) => {
        // Focus (Tab, find) can scroll the clipped viewport to reveal a node: turn it into a pan.
        const el = e.currentTarget;
        if (!el.scrollLeft && !el.scrollTop) return;
        view.current = {
          ...view.current,
          x: view.current.x - el.scrollLeft,
          y: view.current.y - el.scrollTop,
        };
        el.scrollLeft = 0;
        el.scrollTop = 0;
        userMoved.current = true;
        apply();
      }}
    >
      <div ref={world} className="graph-world" data-relayout={relayout || undefined}>
        <svg
          key={layout.key}
          className="graph-edges absolute top-0 left-0 overflow-visible"
          width={svgW}
          height={svgH}
          aria-hidden="true"
          data-enter={lastKey.current !== undefined && relayout ? "" : undefined}
        >
          <Markers />
          {layout.edges.map((e) => {
            const xs = e.points.map((p) => p[0]);
            const ys = e.points.map((p) => p[1]);
            const minX = Math.min(...xs);
            const minY = Math.min(...ys);
            if (!visible(minX, minY, Math.max(...xs) - minX, Math.max(...ys) - minY)) return null;
            const inChain = chain ? chain.has(e.from) && chain.has(e.to) : false;
            return (
              <EdgePath
                key={e.key}
                e={e}
                hl={inChain}
                dim={Boolean(chain && !inChain) || Boolean(dimmed?.has(e.from) && dimmed?.has(e.to))}
                blocked={blockage.edges.has(e.key)}
                march={marching.has(e.key)}
              />
            );
          })}
        </svg>
        {layout.layers.flat().map((id) => {
          const pos = layout.nodes.get(id);
          const n = layout.index.nodes.get(id);
          if (!pos || !n) return null;
          if (id !== selectedId && !visible(pos.x, pos.y, NODE_W, NODE_H)) return null;
          const change = motion.changed.get(id);
          const flash = change?.kind === "status" ? (n.blocked ? "blocked" : change.to) : undefined;
          const waitingId = blockage.waitingOn.get(id);
          const waiting = waitingId ? (layout.index.nodes.get(waitingId)?.name ?? waitingId) : undefined;
          return (
            <NodeCard
              key={id}
              n={n}
              x={pos.x}
              y={pos.y}
              label={nodeLabel(layout.index, n, waitingId)}
              selected={id === selectedId}
              root={id === rootId}
              tabStop={id === tabStop}
              hl={Boolean(chain?.has(id))}
              dim={Boolean(chain && !chain.has(id)) || Boolean(dimmed?.has(id))}
              flash={flash}
              flashAt={change && change.at > now - DURATION.flash ? change.at : undefined}
              requested={requested.get(id)}
              waiting={waiting}
              onSelect={onSelect}
              onOpen={onOpen}
              onHover={setHovered}
              onMenu={onMenu}
            />
          );
        })}
      </div>
    </section>
  );
}
