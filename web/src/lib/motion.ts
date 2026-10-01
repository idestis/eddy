// Motion helpers shared by the components. The durations mirror the tokens in
// design/tokens.css; JavaScript needs them only to keep an element around for its
// exit animation. Under prefers-reduced-motion every wait is 0, so state changes land
// at once (the CSS side drops its durations to 0 as well).

import { useSyncExternalStore } from "react";

export const DURATION = { fast: 120, base: 180, slow: 260, flash: 1200 } as const;

const QUERY = "(prefers-reduced-motion: reduce)";

function media(): MediaQueryList | undefined {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia(QUERY)
    : undefined;
}

/** True when the user asked the system for less motion. */
export function prefersReducedMotion(): boolean {
  return media()?.matches ?? false;
}

/** A duration from DURATION, or 0 under reduced motion. */
export function motionMs(name: keyof typeof DURATION): number {
  return prefersReducedMotion() ? 0 : DURATION[name];
}

/** The reduced-motion preference, kept live. */
export function useReducedMotion(): boolean {
  return useSyncExternalStore(
    (cb) => {
      const m = media();
      m?.addEventListener("change", cb);
      return () => m?.removeEventListener("change", cb);
    },
    prefersReducedMotion,
    () => false,
  );
}

/** Where a tab indicator goes: an x offset and a width, in px, inside the tab list. */
export interface IndicatorBox {
  x: number;
  width: number;
  visible: boolean;
}

/**
 * Places the indicator under (or behind) the active tab. `offsetLeft` and `offsetWidth`
 * are the tab's, relative to the positioned tab list, so scrolling the list needs no
 * correction. `inset` shortens an underline at both ends. No active tab hides it.
 */
export function indicatorBox(
  active: { offsetLeft: number; offsetWidth: number } | null | undefined,
  inset = 0,
): IndicatorBox {
  if (!active || active.offsetWidth <= 0) return { x: 0, width: 0, visible: false };
  const width = Math.max(0, active.offsetWidth - inset * 2);
  return { x: Math.round(active.offsetLeft + inset), width: Math.round(width), visible: width > 0 };
}
