// Log lines handed to Ask AI (POST /ai/ask `attachments`, docs/api.md "Ask AI"). The hub
// accepts at most 3 attachments, 500 lines and 32 KiB in total; the UI trims before it
// sends, keeping the newest lines. Markers (forbidden, ended, dropped…) are never sent,
// and every line keeps its pod/container prefix so the model knows where it came from.

import type { AskAttachment, Ref } from "../api/types";
import type { LogLine } from "./workloadLogs";

export const ATTACH_MAX_LINES = 500;
export const ATTACH_MAX_BYTES = 32 * 1024;
export const ATTACH_MAX = 3;
/** Lines added on each side for "Ask AI with context". */
export const CONTEXT_LINES = 20;

/** An attachment waiting in the Ask AI composer. */
export interface PendingAttachment {
  attachment: AskAttachment;
  /** Suggested question, pre-filled and selected so typing replaces it. */
  question: string;
  /** Lines the user picked before trimming. */
  total: number;
  /** Where the lines came from, to open its Logs tab again. */
  link?: { cluster: string; ref: Pick<Ref, "kind" | "namespace" | "name"> };
}

/** "[pod/container] line": the raw line with where it came from. */
export function lineText(l: Pick<LogLine, "pod" | "container" | "line">): string {
  const who = l.pod ? `${l.pod}${l.container ? `/${l.container}` : ""}` : "";
  return who ? `[${who}] ${l.line}` : l.line;
}

const encoder = new TextEncoder();

/** Newest lines first until the line or byte limit is hit. Exported for tests. */
export function trimNewest(
  texts: readonly string[],
  maxLines = ATTACH_MAX_LINES,
  maxBytes = ATTACH_MAX_BYTES,
) {
  const kept: string[] = [];
  let size = 0;
  for (let i = texts.length - 1; i >= 0 && kept.length < maxLines; i--) {
    const t = texts[i] ?? "";
    const n = encoder.encode(t).length + 1;
    if (size + n > maxBytes) break;
    size += n;
    kept.push(t);
  }
  return kept.reverse();
}

/** Builds an attachment from buffered lines: markers dropped, prefixes kept, trimmed to the limits. */
export function buildAttachment(
  lines: readonly LogLine[],
  source: string,
): { attachment: AskAttachment; total: number } {
  const texts = lines.filter((l) => !l.marker).map(lineText);
  return { attachment: { kind: "logs", source, lines: trimNewest(texts) }, total: texts.length };
}

/** The lines from `from` to `to` (inclusive, any order) with `ctx` more on each side. */
export function sliceWithContext<T>(all: readonly T[], from: number, to: number, ctx = 0): T[] {
  const lo = Math.max(0, Math.min(from, to) - ctx);
  const hi = Math.min(all.length - 1, Math.max(from, to) + ctx);
  return lo <= hi ? all.slice(lo, hi + 1) : [];
}

/** The composer chip: "42 lines · apps/podinfo/podinfo", or "last 500 lines attached". */
export function attachmentLabel(p: Pick<PendingAttachment, "attachment" | "total">): string {
  const n = p.attachment.lines.length;
  const what = n < p.total ? `last ${n} lines attached` : `${n} line${n === 1 ? "" : "s"}`;
  return `${what} · ${p.attachment.source}`;
}
