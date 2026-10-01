// The buffer behind the workload Logs tab: entries from every pod of a workload,
// merged in time order when they carry a kubelet timestamp, capped, and filtered by
// pod, container and a search. Pure functions so they can be tested; the view keeps
// the array in state and renders it virtualised.

import type { LogMarker, WorkloadLogEntry, WorkloadPod } from "../api/types";
import { parseStructured, type Structured } from "./logFormat";

/** Lines kept in memory; older ones are dropped from the top. */
export const LOG_CAP = 20_000;

/** Kinds that have a workload Logs tab (docs/api.md, "Workload logs"). */
export const WORKLOAD_LOG_KINDS: ReadonlySet<string> = new Set([
  "Deployment",
  "StatefulSet",
  "DaemonSet",
  "Job",
]);

export type LogLevel = "error" | "warn" | "info" | "debug";

export const LOG_LEVELS: readonly LogLevel[] = ["error", "warn", "info", "debug"];

export interface LogLine extends WorkloadLogEntry {
  id: number;
  level: LogLevel;
  /** Parsed `ts`, for merging. */
  tsMs?: number;
  /** The parsed JSON or logfmt fields, when the line is structured. */
  structured?: Structured;
}

export function levelOf(text: string): LogLevel {
  if (/\b(error|err|fatal|panic|failed)\b/i.test(text)) return "error";
  if (/\bwarn(ing)?\b/i.test(text)) return "warn";
  if (/\b(debug|trace)\b/i.test(text)) return "debug";
  return "info";
}

/** A buffered line: its time parsed and, for JSON or logfmt, its fields. Exported for the pod view. */
export function toLine(e: WorkloadLogEntry, id: number): LogLine {
  const t = e.ts ? Date.parse(e.ts) : Number.NaN;
  const structured = e.marker ? undefined : parseStructured(e.line);
  return {
    ...e,
    id,
    level: e.marker ? "info" : (structured?.level ?? levelOf(structured?.msg || e.line)),
    tsMs: Number.isNaN(t) ? undefined : t,
    structured,
  };
}

export interface Appended {
  lines: LogLine[];
  nextId: number;
  /** Lines dropped from the top to stay under the cap. */
  trimmed: number;
}

/**
 * Appends a batch. Timestamped lines are merged into the buffer's tail in time order
 * (pods stream independently, so their tails arrive interleaved); lines without a
 * timestamp and markers go at the end, in arrival order.
 */
export function appendEntries(
  buf: readonly LogLine[],
  entries: readonly WorkloadLogEntry[],
  startId: number,
  cap = LOG_CAP,
): Appended {
  if (entries.length === 0) return { lines: buf as LogLine[], nextId: startId, trimmed: 0 };
  let id = startId;
  const incoming = entries.map((e) => toLine(e, id++));
  const timed = incoming.filter((l) => l.tsMs !== undefined).sort((a, b) => (a.tsMs ?? 0) - (b.tsMs ?? 0));
  const untimed = incoming.filter((l) => l.tsMs === undefined);

  let out: LogLine[];
  const first = timed[0];
  if (!first) {
    out = buf.concat(untimed);
  } else {
    // Walk back over buffered lines that are newer than the batch's oldest line; stop at a
    // line without a timestamp, so markers and untimed lines never move.
    let k = buf.length;
    while (k > 0) {
      const t = buf[k - 1]?.tsMs;
      if (t === undefined || t <= (first.tsMs ?? 0)) break;
      k--;
    }
    const tail = buf.slice(k);
    const merged: LogLine[] = [];
    let i = 0;
    let j = 0;
    while (i < tail.length || j < timed.length) {
      const a = tail[i];
      const b = timed[j];
      if (a && (!b || (a.tsMs ?? 0) <= (b.tsMs ?? 0))) {
        merged.push(a);
        i++;
      } else if (b) {
        merged.push(b);
        j++;
      }
    }
    out = buf.slice(0, k).concat(merged, untimed);
  }
  const trimmed = Math.max(0, out.length - cap);
  return { lines: trimmed ? out.slice(trimmed) : out, nextId: id, trimmed };
}

export interface LogFilter {
  /** Only these pods; undefined means every pod. */
  pods?: ReadonlySet<string>;
  /** Only this container; "" or undefined means every container. */
  container?: string;
  /** Case-insensitive text to find in the line. */
  query?: string;
  /** Only these levels; undefined means every level. Markers always pass. */
  levels?: ReadonlySet<LogLevel>;
}

/** Applies the pod, container, level and search filters. Stream-wide markers (no pod) always show. */
export function filterLines(lines: readonly LogLine[], f: LogFilter): readonly LogLine[] {
  const q = f.query?.trim().toLowerCase();
  if (!f.pods && !f.container && !q && !f.levels) return lines;
  return lines.filter((l) => {
    if (l.marker && !l.pod) return true;
    if (f.pods && !f.pods.has(l.pod)) return false;
    if (f.container && l.container && l.container !== f.container) return false;
    if (f.levels && !l.marker && !f.levels.has(l.level)) return false;
    if (q && !l.line.toLowerCase().includes(q)) return false;
    return true;
  });
}

/** Lines per level in the buffer (markers not counted), for the level chips. */
export function levelCounts(lines: readonly LogLine[]): Record<LogLevel, number> {
  const out: Record<LogLevel, number> = { error: 0, warn: 0, info: 0, debug: 0 };
  for (const l of lines) if (!l.marker) out[l.level]++;
  return out;
}

/** A stable hue (0–359) for a pod, so its prefix keeps its colour for the whole session. */
export function podHue(name: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0) % 360;
}

/** The pod name without the workload's prefix: "podinfo-7d9f-x2k" of podinfo → "7d9f-x2k". */
export function podLabel(pod: string, workload: string): string {
  return pod.startsWith(`${workload}-`) && pod.length > workload.length + 1
    ? pod.slice(workload.length + 1)
    : pod;
}

/** Every container name across the pods, in first-seen order. */
export function containersOf(pods: readonly WorkloadPod[]): string[] {
  const seen = new Set<string>();
  for (const p of pods) for (const c of p.containers) seen.add(c);
  return [...seen];
}

/** Said when the hub streams only the newest pods. */
export function truncationNotice(total: number, limit: number): string | undefined {
  if (total <= limit) return undefined;
  return `Streaming the newest ${limit} of ${total} pods. Pick pods to narrow it down, or open a pod for its own logs.`;
}

export const MARKER_LABEL: Record<LogMarker, string> = {
  forbidden: "no access",
  ended: "ended",
  error: "error",
  dropped: "dropped",
};

/** The lines as plain text, for Copy: "pod/container line". */
export function toText(lines: readonly LogLine[]): string {
  return lines
    .map((l) => {
      const who = l.pod ? `${l.pod}${l.container ? `/${l.container}` : ""} ` : "";
      return l.marker ? `${who}[${MARKER_LABEL[l.marker]}] ${l.line}` : `${who}${l.line}`;
    })
    .join("\n");
}
