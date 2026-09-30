// Cluster identity colours. The API gives one colour per cluster
// (ClusterInfo.color); the second gradient stop is derived by rotating the hue,
// which reproduces the prototype pairs (orange→amber, violet→pink, teal→blue).
// Clusters without a colour use the fallback palette in design/tokens.css
// (--color-cluster-1…6), so there are no colour literals here.

import type { CSSProperties } from "react";
import type { ClusterInfo } from "../api/types";

const FALLBACK_COUNT = 6;
const fallbackVar = (order: number) => `var(--color-cluster-${(Math.abs(order) % FALLBACK_COUNT) + 1})`;
const NEUTRAL = "var(--color-cluster-none)";
// The same hue rotation, in CSS, for colours that are token references rather than hex.
const cssSecondary = (color: string) => `oklch(from ${color} calc(l + 0.06) c calc(h + 40))`;

const HEX = /^#[0-9a-f]{6}$/i;

function hexToHsl(hex: string): [number, number, number] {
  const n = Number.parseInt(hex.slice(1), 16);
  const r = ((n >> 16) & 255) / 255;
  const g = ((n >> 8) & 255) / 255;
  const b = (n & 255) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return [0, 0, l];
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h: number;
  if (max === r) h = (g - b) / d + (g < b ? 6 : 0);
  else if (max === g) h = (b - r) / d + 2;
  else h = (r - g) / d + 4;
  return [h * 60, s, l];
}

function hslToHex(h: number, s: number, l: number): string {
  const k = (n: number) => (n + h / 30) % 12;
  const a = s * Math.min(l, 1 - l);
  const f = (n: number) => l - a * Math.max(-1, Math.min(k(n) - 3, 9 - k(n), 1));
  return `#${[f(0), f(8), f(4)]
    .map((x) =>
      Math.round(x * 255)
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`;
}

export function secondaryColor(hex: string): string {
  const [h, s, l] = hexToHsl(hex);
  return hslToHex((h + 40) % 360, Math.min(1, s * 1.05), Math.min(0.6, l + 0.08));
}

/** A CSS colour for the cluster: its hex from the API, or a palette token. */
export function clusterColor(c: Pick<ClusterInfo, "color" | "order"> | undefined): string {
  if (!c) return NEUTRAL;
  if (c.color && HEX.test(c.color)) return c.color;
  return fallbackVar(c.order);
}

function pair(c: Pick<ClusterInfo, "color" | "order"> | undefined): [string, string] {
  const color = clusterColor(c);
  return [color, HEX.test(color) ? secondaryColor(color) : cssSecondary(color)];
}

/** Inline custom properties for chips and swatches of another cluster. */
export function clusterStyle(c: Pick<ClusterInfo, "color" | "order"> | undefined): CSSProperties {
  const [color, second] = pair(c);
  return { "--cc": color, "--cc2": second };
}

/** Sets the app-wide identity (ribbon, accents, selection) to a cluster. */
export function applyClusterIdentity(c: ClusterInfo | undefined): void {
  const root = document.documentElement;
  const [color, second] = pair(c);
  root.style.setProperty("--cluster", color);
  root.style.setProperty("--cluster-2", second);
  root.dataset.protected = String(Boolean(c?.protected));
}
