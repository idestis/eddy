// A sliding indicator for tab lists and segmented controls. The list measures its
// active child ([aria-selected=true] or [aria-pressed=true]) and moves one shared
// indicator there with a transform and a width transition, instead of each tab
// painting its own border or background. Positions are set through element.style
// (CSSOM), which the strict CSP allows.

import { type Ref, useCallback, useLayoutEffect, useRef, useState } from "react";
import { indicatorBox } from "../lib/motion";

const ACTIVE = ':scope > [aria-selected="true"], :scope > [aria-pressed="true"]';

export type IndicatorVariant = "underline" | "pill" | "raised";

/** Classes of the indicator itself; the list must be `relative`. */
const VARIANT: Record<IndicatorVariant, string> = {
  underline: "bottom-0 h-0.5 rounded-full bg-c",
  pill: "inset-y-[3px] rounded-lg bg-ink",
  raised: "inset-y-0.5 rounded-lg bg-surface shadow-control",
};

/**
 * Returns a ref for the tab list and one for the indicator. `active` is whatever
 * identifies the selected tab; when it changes the indicator slides over. The list ref
 * is a callback, so a list that mounts later (after data loads) is placed too.
 */
export function useTabIndicator<L extends HTMLElement = HTMLDivElement>(active: unknown, inset = 0) {
  const [el, setEl] = useState<L | null>(null);
  const indicator = useRef<HTMLSpanElement | null>(null);
  const list = useCallback((node: L | null) => setEl(node), []);

  const place = useCallback(() => {
    const ind = indicator.current;
    if (!el || !ind) return;
    const box = indicatorBox(el.querySelector<HTMLElement>(ACTIVE), inset);
    ind.style.transform = `translateX(${box.x}px)`;
    ind.style.width = `${box.width}px`;
    ind.style.opacity = box.visible ? "1" : "0";
  }, [el, inset]);

  // Every render: tabs come and go (a Logs tab once features load) and labels change.
  // It is one query and three style writes.
  useLayoutEffect(() => place());

  // biome-ignore lint/correctness/useExhaustiveDependencies: `active` moves the indicator
  useLayoutEffect(() => {
    place();
    const ind = indicator.current;
    // The first placement lands without a transition; later moves slide.
    if (ind && ind.dataset.ready !== "true") {
      const frame = requestAnimationFrame(() => {
        ind.dataset.ready = "true";
      });
      return () => cancelAnimationFrame(frame);
    }
  }, [active, place]);

  // Labels change width (counts, fonts loading, container queries): follow them.
  useLayoutEffect(() => {
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => place());
    ro.observe(el);
    for (const child of el.children) ro.observe(child);
    return () => ro.disconnect();
  }, [el, place]);

  return { list, indicator };
}

/** The indicator element. Put it inside the list, before or after the tabs. */
export function TabIndicator({ ref, variant }: { ref: Ref<HTMLSpanElement>; variant: IndicatorVariant }) {
  return <span ref={ref} aria-hidden="true" className={`tab-ind ${VARIANT[variant]}`} />;
}
