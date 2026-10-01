// The body of both Logs tabs (a pod's, and a workload's): virtualised lines with an
// optional pod prefix, structured rendering of JSON and logfmt lines, search highlights,
// follow, and line selection that can be handed to Ask AI.
//
// Selecting lines: drag across the text (native selection), click a line's gutter and
// shift+click another, or Shift+↑/↓ in the focused list. A small toolbar appears at the
// selection with "Ask AI about selection" and "Ask AI with context". Esc, a click
// elsewhere or scrolling the selection out of view hides it.

import { useVirtualizer } from "@tanstack/react-virtual";
import {
  type CSSProperties,
  type KeyboardEvent,
  type MouseEvent,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { useKeys } from "../lib/keys";
import { fieldText, type StructuredLevel } from "../lib/logFormat";
import type { LogFormat } from "../lib/viewPrefs";
import { LOG_LEVELS, type LogLevel, type LogLine, MARKER_LABEL, podHue, podLabel } from "../lib/workloadLogs";
import { Icon } from "./Icon";
import { OverflowChips, type OverflowItem } from "./OverflowChips";
import { SelectionToolbar } from "./SelectionToolbar";

export type AskMode = "selection" | "context";

const podStyle = (pod: string) => ({ "--pod-h": podHue(pod) }) as CSSProperties;

const LEVEL_SHORT: Record<StructuredLevel, string> = { error: "ERR", warn: "WRN", info: "INF", debug: "DBG" };

function clock(ts: string | undefined): string {
  if (!ts) return "";
  const d = new Date(ts);
  return Number.isNaN(d.getTime()) ? "" : d.toLocaleTimeString(undefined, { hour12: false });
}

/** Highlights `query` in `text` without HTML: plain spans and <mark>. */
export function Highlight({ text, query }: { text: string; query: string }) {
  const q = query.trim().toLowerCase();
  if (!q) return <>{text}</>;
  const parts: Array<{ t: string; hit: boolean; at: number }> = [];
  const lower = text.toLowerCase();
  let at = 0;
  for (let i = lower.indexOf(q); i >= 0; i = lower.indexOf(q, at)) {
    if (i > at) parts.push({ t: text.slice(at, i), hit: false, at });
    parts.push({ t: text.slice(i, i + q.length), hit: true, at: i });
    at = i + q.length;
  }
  if (at < text.length) parts.push({ t: text.slice(at), hit: false, at });
  return (
    <>
      {parts.map((p) =>
        p.hit ? (
          <mark key={p.at} className="rounded-sm bg-code-warn/30 text-inherit">
            {p.t}
          </mark>
        ) : (
          <span key={p.at}>{p.t}</span>
        ),
      )}
    </>
  );
}

interface Range {
  from: number;
  to: number;
}

interface ToolbarPos {
  top: number;
  left: number;
}

export interface LogLinesProps {
  lines: readonly LogLine[];
  format: LogFormat;
  query: string;
  /** Show the pod prefix, shortened against this workload name. */
  workload?: string;
  showContainer: boolean;
  follow: boolean;
  setFollow: (on: boolean) => void;
  /** Lines that arrived while not following. */
  unseen: number;
  onResume: () => void;
  /** Hands lines to Ask AI; without it there is no selection toolbar. */
  onAsk?: (lines: LogLine[], mode: AskMode) => void;
  emptyText: string;
}

export function LogLines({
  lines,
  format,
  query,
  workload,
  showContainer,
  follow,
  setFollow,
  unseen,
  onResume,
  onAsk,
  emptyText,
}: LogLinesProps) {
  const wrap = useRef<HTMLDivElement>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const toolbar = useRef<HTMLDivElement>(null);
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  // Gutter/keyboard selection (indices into `lines`) and native text selection.
  const [gutter, setGutter] = useState<{ anchor: number; focus: number } | null>(null);
  const [textRange, setTextRange] = useState<Range | null>(null);
  const [pos, setPos] = useState<ToolbarPos | null>(null);
  const autoScrollAt = useRef(0);
  // One fixed width for the pod column, sized to the longest short pod name, so rows line up.
  // With a single pod the column says nothing and is hidden (the header names the pod).
  const podCol = useMemo(() => {
    if (!workload) return null;
    const pods = new Set<string>();
    let width = 0;
    for (const l of lines) {
      if (!l.pod) continue;
      pods.add(l.pod);
      const label = podLabel(l.pod, workload) + (l.container && showContainer ? `/${l.container}` : "");
      width = Math.max(width, label.length);
    }
    return pods.size > 1 ? `${Math.min(Math.max(width, 6), 24)}ch` : null;
  }, [lines, workload, showContainer]);

  const virtualizer = useVirtualizer({
    count: lines.length,
    getScrollElement: () => scroller.current,
    estimateSize: () => 20,
    getItemKey: (i) => lines[i]?.id ?? i,
    overscan: 20,
    // A first frame of rows before the scroller is measured (and in tests).
    initialRect: { width: 800, height: 600 },
  });

  // Follow: stay pinned to the newest line. Our own scrolls must not switch it off.
  const toBottom = useCallback(() => {
    if (!lines.length) return;
    autoScrollAt.current = Date.now();
    virtualizer.scrollToIndex(lines.length - 1, { align: "end" });
  }, [lines.length, virtualizer]);
  useEffect(() => {
    if (follow) toBottom();
  }, [follow, toBottom]);

  const range: Range | null = useMemo(
    () =>
      gutter
        ? { from: Math.min(gutter.anchor, gutter.focus), to: Math.max(gutter.anchor, gutter.focus) }
        : textRange,
    [gutter, textRange],
  );

  const clearSelection = useCallback(() => {
    setGutter(null);
    setTextRange(null);
    setPos(null);
    const sel = window.getSelection();
    if (sel && !sel.isCollapsed && scroller.current?.contains(sel.anchorNode)) sel.removeAllRanges();
  }, []);

  // The line index of a DOM node inside the list, if any.
  const indexOf = useCallback((node: Node | null): number | undefined => {
    const el = node instanceof Element ? node : node?.parentElement;
    const row = el?.closest<HTMLElement>("[data-line-index]");
    if (!row || !scroller.current?.contains(row)) return undefined;
    const n = Number(row.dataset.lineIndex);
    return Number.isNaN(n) ? undefined : n;
  }, []);

  // Native selection: map its ends to lines whenever it changes.
  useEffect(() => {
    if (!onAsk) return;
    let frame = 0;
    const onChange = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(() => {
        const sel = window.getSelection();
        if (!sel || sel.isCollapsed || sel.rangeCount === 0) {
          setTextRange(null);
          return;
        }
        const a = indexOf(sel.anchorNode);
        const f = indexOf(sel.focusNode);
        if (a === undefined || f === undefined) {
          setTextRange(null);
          return;
        }
        setGutter(null);
        const from = Math.min(a, f);
        const to = Math.max(a, f);
        setTextRange((r) => (r && r.from === from && r.to === to ? r : { from, to }));
      });
    };
    document.addEventListener("selectionchange", onChange);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("selectionchange", onChange);
    };
  }, [onAsk, indexOf]);

  // Place the toolbar above the selection (below it near the top), inside the log viewport.
  const placeToolbar = useCallback(() => {
    const w = wrap.current;
    const sc = scroller.current;
    if (!w || !sc || !range) {
      setPos((p) => (p ? null : p));
      return;
    }
    let rect: DOMRect | undefined;
    const sel = window.getSelection();
    if (textRange && sel && sel.rangeCount > 0 && !sel.isCollapsed) {
      rect = sel.getRangeAt(0).getBoundingClientRect();
    } else {
      const rows = [...sc.querySelectorAll<HTMLElement>("[data-line-index]")].filter((el) => {
        const n = Number(el.dataset.lineIndex);
        return n >= range.from && n <= range.to;
      });
      const first = rows[0]?.getBoundingClientRect();
      const last = rows[rows.length - 1]?.getBoundingClientRect();
      if (first && last) rect = new DOMRect(first.left, first.top, first.width, last.bottom - first.top);
    }
    const view = sc.getBoundingClientRect();
    if (!rect || rect.bottom < view.top + 4 || rect.top > view.bottom - 4) {
      setPos((p) => (p ? null : p));
      return;
    }
    const box = w.getBoundingClientRect();
    const tw = toolbar.current?.offsetWidth ?? 320;
    const th = toolbar.current?.offsetHeight ?? 34;
    const top = Math.max(view.top, rect.top) - box.top - th - 6;
    const below = Math.min(view.bottom, rect.bottom) - box.top + 6;
    const minTop = view.top - box.top + 4;
    const center = rect.left + Math.min(rect.width, view.width) / 2 - box.left;
    const next = {
      top: Math.round(top >= minTop ? top : Math.min(below, view.bottom - box.top - th - 4)),
      left: Math.round(Math.max(8, Math.min(center - tw / 2, box.width - tw - 8))),
    };
    setPos((p) => (p && p.top === next.top && p.left === next.left ? p : next));
  }, [range, textRange]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: re-place when the selection or lines move
  useLayoutEffect(() => {
    if (!onAsk) return;
    placeToolbar();
  }, [onAsk, placeToolbar, lines.length]);

  // Esc hides the toolbar before it can reach the page's "back"; a click elsewhere clears
  // a gutter selection (a text selection clears itself).
  useEffect(() => {
    if (!range) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (e.key !== "Escape") return;
      e.preventDefault();
      e.stopPropagation();
      clearSelection();
    };
    const onDown = (e: PointerEvent) => {
      if (wrap.current?.contains(e.target as Node)) return;
      setGutter(null);
    };
    window.addEventListener("keydown", onKey, true);
    document.addEventListener("pointerdown", onDown, true);
    return () => {
      window.removeEventListener("keydown", onKey, true);
      document.removeEventListener("pointerdown", onDown, true);
    };
  }, [range, clearSelection]);

  const toggleExpand = (id: number) =>
    setExpanded((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  const onGutter = (e: MouseEvent, i: number) => {
    window.getSelection()?.removeAllRanges();
    setTextRange(null);
    setGutter((g) => (e.shiftKey && g ? { anchor: g.anchor, focus: i } : { anchor: i, focus: i }));
    scroller.current?.focus({ preventScroll: true });
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.shiftKey && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      e.stopPropagation();
      const items = virtualizer.getVirtualItems();
      const start = gutter?.focus ?? items[items.length - 1]?.index ?? lines.length - 1;
      const focus = Math.max(
        0,
        Math.min(lines.length - 1, start + (gutter ? (e.key === "ArrowDown" ? 1 : -1) : 0)),
      );
      setTextRange(null);
      setGutter((g) => ({ anchor: g?.anchor ?? start, focus }));
      setFollow(false);
      virtualizer.scrollToIndex(focus, { align: "auto" });
      return;
    }
    if (e.key === "Enter" && gutter && gutter.anchor === gutter.focus) {
      const l = lines[gutter.focus];
      if (l?.structured) {
        e.preventDefault();
        e.stopPropagation();
        toggleExpand(l.id);
      }
    }
  };

  const scrollBy = (dy: number) => scroller.current?.scrollBy({ top: dy });
  useKeys({
    down: () => scrollBy(60),
    up: () => {
      setFollow(false);
      scrollBy(-60);
    },
    top: () => {
      setFollow(false);
      scroller.current?.scrollTo({ top: 0 });
    },
    bottom: onResume,
  });

  const ask = (mode: AskMode) => {
    if (!range || !onAsk) return;
    const lo = mode === "context" ? Math.max(0, range.from - 20) : range.from;
    const hi = mode === "context" ? Math.min(lines.length - 1, range.to + 20) : range.to;
    onAsk(lines.slice(lo, hi + 1), mode);
    clearSelection();
  };

  const picked = range ? range.to - range.from + 1 : 0;

  return (
    <div ref={wrap} className="relative flex min-h-0 flex-1 flex-col">
      <div
        ref={scroller}
        className="min-h-0 flex-1 overflow-auto pt-2 pb-6 font-mono text-12-5 leading-[1.6] outline-none [contain:strict] focus-visible:shadow-[inset_0_0_0_2px_color-mix(in_oklab,var(--c)_60%,transparent)]"
        role="log"
        aria-live="off"
        // biome-ignore lint/a11y/noNoninteractiveTabindex: the list is scrolled and selected with the keyboard
        tabIndex={0}
        aria-label="Log lines. Shift and the arrow keys select lines."
        onKeyDown={onKeyDown}
        onScroll={(e) => {
          if (onAsk && range) placeToolbar();
          const el = e.currentTarget;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          if (Date.now() - autoScrollAt.current < 250) return;
          if (follow && !atBottom) setFollow(false);
          else if (!follow && atBottom && unseen > 0) onResume();
        }}
      >
        {lines.length === 0 && <div className="lg-meta px-4 py-px">{emptyText}</div>}
        <div className="relative w-full" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((v) => {
            const l = lines[v.index];
            if (!l) return null;
            const s = format === "structured" ? l.structured : undefined;
            const open = s && expanded.has(l.id);
            const inRange = range && v.index >= range.from && v.index <= range.to;
            return (
              <div
                key={v.key}
                data-index={v.index}
                data-line-index={v.index}
                ref={virtualizer.measureElement}
                className={`absolute top-0 left-0 w-full pr-4 ${inRange ? "bg-white/8 shadow-[inset_2px_0_0_var(--c)]" : "hover:bg-white/4"}`}
                style={{ transform: `translateY(${v.start}px)` }}
              >
                <div
                  className={`flex items-start gap-3 py-px ${l.marker ? `lg-marker lg-marker-${l.marker}` : `lg-${l.level}`}`}
                >
                  <button
                    type="button"
                    tabIndex={-1}
                    className="box-content block w-[8ch] shrink-0 cursor-pointer self-start pl-3 text-left leading-[inherit] text-code-dim tabular-nums select-none hover:text-code-ink"
                    title="Select this line (shift+click to select a range)"
                    aria-label={`Select line ${v.index + 1}`}
                    onMouseDown={(e) => e.shiftKey && e.preventDefault()}
                    onClick={(e) => onGutter(e, v.index)}
                  >
                    {clock(l.ts) || "·"}
                  </button>
                  {podCol && l.pod && (
                    <span
                      className="lg-pod shrink-0 truncate"
                      style={{ ...podStyle(l.pod), width: podCol }}
                      title={`${l.pod}${l.container ? `/${l.container}` : ""}`}
                    >
                      {podLabel(l.pod, workload ?? "")}
                      {l.container && showContainer && <span className="opacity-60">/{l.container}</span>}
                    </span>
                  )}
                  <span className="min-w-0 flex-1 break-words whitespace-pre-wrap">
                    {l.marker && (
                      <span className="mr-1.5 rounded border border-current/40 px-1 text-10-5 font-semibold tracking-wide uppercase not-italic">
                        {MARKER_LABEL[l.marker]}
                      </span>
                    )}
                    {s ? (
                      // biome-ignore lint/a11y/useKeyWithClickEvents lint/a11y/noStaticElementInteractions: Enter on a selected line expands it
                      <span
                        className="cursor-pointer"
                        onClick={() => {
                          if (window.getSelection()?.isCollapsed !== false) toggleExpand(l.id);
                        }}
                      >
                        {s.level && (
                          <span className={`lg-lvl lg-lvl-${s.level}`}>{LEVEL_SHORT[s.level]}</span>
                        )}
                        <Icon
                          name="chev"
                          className={`mr-1 inline size-3 align-[-1px] text-code-dim transition-transform duration-(--duration-fast) ${open ? "rotate-90" : ""}`}
                        />
                        <Highlight text={s.msg || l.line} query={query} />
                        {s.key.map(([k, val]) => (
                          <span
                            key={k}
                            className={`ml-2 ${k === "error" || k === "err" ? "text-code-error/80" : "text-code-dim"}`}
                          >
                            {k}=<Highlight text={val} query={query} />
                          </span>
                        ))}
                      </span>
                    ) : (
                      <Highlight text={l.line} query={query} />
                    )}
                  </span>
                </div>
                {open && s && (
                  <dl className="anim-fade-in my-1 mr-2 ml-[74px] grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3 gap-y-0.5 rounded-lg border border-code-line bg-white/3 px-3 py-2 text-12 [tab-size:4]">
                    {s.fields.map(([k, val]) => (
                      <div key={k} className="contents">
                        <dt className="text-code-dim">{k}</dt>
                        <dd className="m-0 break-words whitespace-pre-wrap text-code-ink">
                          {fieldText(val)}
                        </dd>
                      </div>
                    ))}
                  </dl>
                )}
              </div>
            );
          })}
        </div>
      </div>
      {onAsk && range && (
        <SelectionToolbar
          toolbarRef={toolbar}
          count={picked}
          pos={pos}
          contextTitle="The selection and 20 lines on each side"
          onSelection={() => ask("selection")}
          onContext={() => ask("context")}
        />
      )}
      {!follow && unseen > 0 && (
        <button
          type="button"
          className="anim-pop-in absolute bottom-4 left-1/2 flex -translate-x-1/2 items-center gap-1.5 rounded-full bg-c px-3 py-1.5 text-12-5 font-semibold text-c-ink shadow-toast"
          onClick={onResume}
        >
          <Icon name="chev" className="size-3.5 rotate-90" />
          {unseen.toLocaleString()} new line{unseen === 1 ? "" : "s"}
        </button>
      )}
    </div>
  );
}

const CHIP =
  "inline-flex h-7 items-center gap-1.5 rounded-[9px] border border-white/14 px-2 text-11-5 transition-colors duration-(--duration-fast) hover:bg-white/6 aria-pressed:bg-white/10";

/** Level filter chips with the count of each level in the buffer. None picked = all. */
export function LevelChips({
  counts,
  picked,
  onChange,
  className = "min-w-0 flex-1",
}: {
  counts: Record<LogLevel, number>;
  picked: ReadonlySet<LogLevel> | undefined;
  onChange: (next: ReadonlySet<LogLevel> | undefined) => void;
  className?: string;
}) {
  const toggle = (lv: LogLevel) => {
    const next = new Set(picked ?? []);
    if (next.has(lv)) next.delete(lv);
    else next.add(lv);
    onChange(next.size ? next : undefined);
  };
  const items: OverflowItem[] = LOG_LEVELS.map((lv) => {
    const on = picked?.has(lv) ?? false;
    const name = () => <span className={`lg-lvl lg-lvl-${lv} m-0!`}>{LEVEL_SHORT[lv]}</span>;
    return {
      key: lv,
      text: LEVEL_SHORT[lv],
      active: on,
      option: name(),
      meta: counts[lv].toLocaleString(),
      chip: (
        <button type="button" className={CHIP} aria-pressed={on} onClick={() => toggle(lv)}>
          {name()}
          <span className="tabular-nums text-code-dim">{counts[lv].toLocaleString()}</span>
        </button>
      ),
    };
  });
  return (
    <OverflowChips
      label="Levels"
      tone="code"
      gap={4}
      className={className}
      chipClass={`${CHIP} aria-pressed:bg-white/10`}
      items={items}
      onToggle={(k) => toggle(k as LogLevel)}
      summary={(on) => (on.length ? on.map((i) => i.option) : "Levels")}
    />
  );
}

/** Raw or structured rendering of JSON and logfmt lines. */
export function FormatToggle({ value, onChange }: { value: LogFormat; onChange: (f: LogFormat) => void }) {
  return (
    <fieldset className="m-0 flex rounded-[9px] border border-white/14 p-0.5">
      <legend className="sr-only">Line format</legend>
      {(["structured", "raw"] as const).map((f) => (
        <button
          key={f}
          type="button"
          aria-pressed={value === f}
          className="seg-btn inline-flex h-6 items-center rounded-md px-2 text-11-5 text-code-dim transition-colors duration-(--duration-fast) aria-pressed:bg-white/12 aria-pressed:text-code-ink"
          onClick={() => onChange(f)}
        >
          {f === "structured" ? "Structured" : "Raw"}
        </button>
      ))}
    </fieldset>
  );
}
