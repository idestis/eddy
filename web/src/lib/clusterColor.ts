// Cluster identity colours. The API gives one colour per cluster
// (ClusterInfo.color); the second gradient stop is derived by rotating the hue,
// which reproduces the prototype pairs (orange→amber, violet→pink, teal→blue).
// Clusters without a colour get "Auto": a stable hue from the name (autoColor below),
// computed in OKLCH so every hue has the same safe lightness in both themes.

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

/**
 * A CSS colour for the cluster: its hex from the API, or "Auto", a stable colour derived
 * from its name (autoColor). Without a name it falls back to the palette token by order.
 */
export function clusterColor(c: Pick<ClusterInfo, "color" | "order" | "name"> | undefined): string {
  if (!c) return NEUTRAL;
  if (c.color && HEX.test(c.color)) return c.color;
  if (c.name) return autoColor(c.name);
  return fallbackVar(c.order);
}

function pair(c: Pick<ClusterInfo, "color" | "order" | "name"> | undefined): [string, string] {
  const color = clusterColor(c);
  return [color, HEX.test(color) ? secondaryColor(color) : cssSecondary(color)];
}

/** Inline custom properties for chips and swatches of another cluster. */
export function clusterStyle(c: Pick<ClusterInfo, "color" | "order" | "name"> | undefined): CSSProperties {
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

// ---------------------------------------------------------------------------------------
// "Auto" colours, contrast checks and tile initials (cluster colour picker).

/** FNV-1a, 32 bit: a stable number for a cluster name. */
export function hashName(name: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

type Rgb = [number, number, number];

const toLinear = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const fromLinear = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);

function hexToRgb(hex: string): Rgb {
  const n = Number.parseInt(hex.slice(1), 16);
  return [((n >> 16) & 255) / 255, ((n >> 8) & 255) / 255, (n & 255) / 255];
}

function rgbToHex([r, g, b]: Rgb): string {
  return `#${[r, g, b]
    .map((x) =>
      Math.round(Math.max(0, Math.min(1, x)) * 255)
        .toString(16)
        .padStart(2, "0"),
    )
    .join("")}`;
}

/** OKLab (Björn Ottosson) to linear sRGB. */
function oklabToLinear(L: number, a: number, b: number): Rgb {
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
}

function linearToOklab([r, g, b]: Rgb): Rgb {
  const l = Math.cbrt(0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b);
  const m = Math.cbrt(0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b);
  const s = Math.cbrt(0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b);
  return [
    0.2104542553 * l + 0.793617785 * m - 0.0040720468 * s,
    1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s,
    0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s,
  ];
}

/** An OKLCH colour as hex, lowering chroma until it fits in sRGB. */
export function oklchToHex(L: number, C: number, hDeg: number): string {
  const h = (hDeg * Math.PI) / 180;
  for (let c = C; c >= 0; c -= 0.005) {
    const lin = oklabToLinear(L, c * Math.cos(h), c * Math.sin(h));
    if (lin.every((x) => x >= -0.0005 && x <= 1.0005)) return rgbToHex(lin.map(fromLinear) as Rgb);
  }
  return rgbToHex(oklabToLinear(L, 0, 0).map(fromLinear) as Rgb);
}

/**
 * The "Auto" colour: a hue from the name, spread over the whole wheel, at a lightness
 * and chroma that keep white initials readable in light mode and survive the dark-mode
 * lift towards white (app.css mixes 32% white into cluster colours in dark mode).
 */
export function autoColor(name: string): string {
  const hue = (hashName(name) % 3600) / 10;
  return oklchToHex(0.53, 0.15, hue);
}

/** WCAG relative luminance. */
export function luminance(hex: string): number {
  const [r, g, b] = hexToRgb(hex).map(toLinear) as Rgb;
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function contrastRatio(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x) as [number, number];
  return (hi + 0.05) / (lo + 0.05);
}

/** The cluster colour as dark mode shows it: mixed with 32% white in OKLab, like app.css. */
export function darkModeShade(hex: string): string {
  const lab = linearToOklab(hexToRgb(hex).map(toLinear) as Rgb);
  const white = linearToOklab([1, 1, 1]);
  const mix = lab.map((v, i) => v * 0.68 + (white[i] ?? 0) * 0.32) as Rgb;
  return rgbToHex(oklabToLinear(...mix).map(fromLinear) as Rgb);
}

// The surfaces and tile text colours of design/tokens.css (--color-surface, --c-ink).
const SURFACE_LIGHT = "#ffffff";
const SURFACE_DARK = "#14171c";
const INK_ON_LIGHT = "#ffffff";
const INK_ON_DARK = "#14161b";

export interface ColorCheck {
  /** Initials on the tile, light theme (white on the colour). */
  tileLight: number;
  /** Initials on the tile, dark theme (dark ink on the lifted colour). */
  tileDark: number;
  /** The colour as an accent (ribbon, rings) on the light surface. */
  surfaceLight: number;
  surfaceDark: number;
  /** Plain-language problems; empty when the colour works in both themes. */
  warnings: string[];
}

/** Tile text needs 4.5:1; accents on a surface need 3:1 (WCAG 1.4.11). */
export function checkColor(hex: string): ColorCheck {
  const dark = darkModeShade(hex);
  const tileLight = contrastRatio(hex, INK_ON_LIGHT);
  const tileDark = contrastRatio(dark, INK_ON_DARK);
  const surfaceLight = contrastRatio(hex, SURFACE_LIGHT);
  const surfaceDark = contrastRatio(dark, SURFACE_DARK);
  const warnings: string[] = [];
  const r = (n: number) => `${n.toFixed(1)}:1`;
  if (tileLight < 4.5)
    warnings.push(`Initials are hard to read on it in light mode (${r(tileLight)}). Pick a darker colour.`);
  if (tileDark < 4.5)
    warnings.push(
      `Initials are hard to read on it in dark mode (${r(tileDark)}). Pick a lighter or stronger colour.`,
    );
  if (surfaceLight < 3) warnings.push(`It is faint on the light background (${r(surfaceLight)}).`);
  if (surfaceDark < 3) warnings.push(`It is faint on the dark background (${r(surfaceDark)}).`);
  return { tileLight, tileDark, surfaceLight, surfaceDark, warnings };
}

/**
 * Two characters for a tile, so identity does not rest on colour alone: the first letters
 * of the first two words ("prod-eu" → "PE"), or the first two letters of a single word.
 */
export function initials(name: string): string {
  const words = name
    .trim()
    .split(/[\s._/-]+/)
    .filter(Boolean);
  const [a = "", b = ""] = words;
  const out = b ? `${[...a][0] ?? ""}${[...b][0] ?? ""}` : [...a].slice(0, 2).join("");
  return (out || "?").toUpperCase();
}
