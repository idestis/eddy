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
  completed: "Completed",
};

/** Sort order: what needs attention first. */
export const STATUS_RANK: Record<Status, number> = {
  failed: 0,
  reconciling: 1,
  suspended: 2,
  unknown: 3,
  ready: 4,
  completed: 5,
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

/** "ghcr.io/stefanprodan/podinfo:6.7.2" → "podinfo:6.7.2"; digests are shortened. */
export function shortImage(image: string | undefined): string {
  if (!image) return "";
  const at = image.indexOf("@sha256:");
  const ref = at >= 0 ? `${image.slice(0, at)}@${image.slice(at + 8, at + 15)}` : image;
  return ref.slice(ref.lastIndexOf("/") + 1);
}

/** The version column: chart for releases, revision for sources, image for workloads. */
export function revisionOf(r: Resource): string {
  if (r.chart) return r.chart;
  if (r.revision) return shortRevision(r.revision);
  const image = r.images?.[0];
  if (image) return shortImage(image) + (r.images && r.images.length > 1 ? ` +${r.images.length - 1}` : "");
  return r.schedule ?? r.hosts?.[0] ?? r.ports?.[0] ?? "";
}

/** The full value behind revisionOf, for its tooltip. */
export function revisionTitle(r: Resource): string | undefined {
  if (r.chart || r.revision) return r.revision ?? r.chart;
  if (r.images?.length) return r.images.join("\n");
  return r.schedule ?? (r.hosts ?? r.ports)?.join(", ");
}

/** Workload messages that only repeat the replicas column ("2/2 replicas ready"). */
export function listMessage(r: Resource): string | undefined {
  if (r.replicas && r.message && /^\d+\/\d+ replicas ready$/.test(r.message)) return undefined;
  if (r.status === "ready" && r.message === "Running") return undefined;
  return r.message;
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  return `${(n / 1024).toFixed(1)} KB`;
}

/** "15168" → "15,168". */
export const thousands = (n: number): string => n.toLocaleString("en-US");

export const plural = (n: number, word: string): string => `${n} ${word}${n === 1 ? "" : "s"}`;
