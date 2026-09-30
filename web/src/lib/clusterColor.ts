// Cluster identity colours. The API gives one colour per cluster
// (ClusterInfo.color); the second gradient stop is derived by rotating the hue,
// which reproduces the prototype pairs (orange→amber, violet→pink, teal→blue).

import type { CSSProperties } from "react";
import type { ClusterInfo } from "../api/types";

const FALLBACK = ["#6D28D9", "#0F766E", "#C2410C", "#2563EB", "#BE185D", "#4D7C0F"];
const NEUTRAL = "#475569";

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

export function clusterColor(c: Pick<ClusterInfo, "color" | "order"> | undefined): string {
  if (!c) return NEUTRAL;
  if (c.color && HEX.test(c.color)) return c.color;
  return FALLBACK[Math.abs(c.order) % FALLBACK.length] ?? NEUTRAL;
}

/** Inline custom properties for chips and swatches of another cluster. */
export function clusterStyle(c: Pick<ClusterInfo, "color" | "order"> | undefined): CSSProperties {
  const color = clusterColor(c);
  return { "--cc": color, "--cc2": secondaryColor(color) };
}

/** Sets the app-wide identity (frame gradient, ribbon, accents) to a cluster. */
export function applyClusterIdentity(c: ClusterInfo | undefined): void {
  const root = document.documentElement;
  const color = c ? clusterColor(c) : NEUTRAL;
  root.style.setProperty("--cluster", color);
  root.style.setProperty("--cluster-2", c ? secondaryColor(color) : "#64748B");
  root.dataset.protected = String(Boolean(c?.protected));
}
