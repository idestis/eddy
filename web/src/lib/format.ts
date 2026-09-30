import type { Resource, Status } from "../api/types";

/** Compact relative age like kubectl: "41s", "7m", "3h", "4d". */
export function age(iso: string | undefined, now: number = Date.now()): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

export function ago(iso: string | undefined, now: number = Date.now()): string {
  const a = age(iso, now);
  return a ? `${a} ago` : "never";
}

export function dateTime(iso: string | undefined): string {
  if (!iso) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime())
    ? ""
    : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export const STATUS_LABEL: Record<Status, string> = {
  ready: "Ready",
  failed: "Failed",
  reconciling: "Reconciling",
  suspended: "Suspended",
  unknown: "Unknown",
};

/** Sort order: what needs attention first. */
export const STATUS_RANK: Record<Status, number> = {
  failed: 0,
  reconciling: 1,
  suspended: 2,
  unknown: 3,
  ready: 4,
};

/** "main@sha1:0a1b2c3d4e…" → "main@0a1b2c3"; "6.7.1@sha256:…" → "6.7.1". */
export function shortRevision(rev: string | undefined): string {
  if (!rev) return "";
  const m = rev.match(/^(.*?)@?(sha1|sha256):([0-9a-f]+)$/);
  if (!m) return rev;
  const [, ref = "", algo, hash = ""] = m;
  if (algo === "sha256" && ref) return ref;
  return ref ? `${ref}@${hash.slice(0, 7)}` : hash.slice(0, 7);
}

/** The "revision" column: chart for releases, revision for sources, image tag for workloads. */
export function revisionOf(r: Resource): string {
  if (r.chart) return r.chart;
  if (r.revision) return shortRevision(r.revision);
  const image = r.images?.[0];
  if (image) return image.slice(image.lastIndexOf(":") + 1);
  return "";
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  return `${(n / 1024).toFixed(1)} KB`;
}

export const plural = (n: number, word: string): string => `${n} ${word}${n === 1 ? "" : "s"}`;
