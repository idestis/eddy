// Log lines handed to Ask AI (POST /ai/ask `attachments`, docs/api.md "Ask AI"). The hub
// accepts at most 3 attachments, 500 lines and 32 KiB in total; the UI trims before it
// sends, keeping the newest lines. Markers (forbidden, ended, dropped…) are never sent,
// and every line keeps its pod/container prefix so the model knows where it came from.
// A YAML excerpt (the whole document, or a selection with its parent keys) travels the same
// way, but keeps the top of the document, where metadata and spec are.

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
  /** Where the lines came from, to open its Logs or YAML tab again. */
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
  const part = p.attachment.kind === "yaml" ? "first" : "last";
  const what = n < p.total ? `${part} ${n} lines attached` : `${n} line${n === 1 ? "" : "s"}`;
  return `${what} · ${p.attachment.source}`;
}

/** Oldest lines first until the line or byte limit is hit: the top of a document. */
export function trimOldest(
  texts: readonly string[],
  maxLines = ATTACH_MAX_LINES,
  maxBytes = ATTACH_MAX_BYTES,
) {
  const kept: string[] = [];
  let size = 0;
  for (let i = 0; i < texts.length && kept.length < maxLines; i++) {
    const t = texts[i] ?? "";
    const n = encoder.encode(t).length + 1;
    if (size + n > maxBytes) break;
    size += n;
    kept.push(t);
  }
  return kept;
}

/** A YAML excerpt, trimmed from the end so the top of the document stays. */
export function buildYamlAttachment(
  lines: readonly string[],
  source: string,
): { attachment: AskAttachment; total: number } {
  return { attachment: { kind: "yaml", source, lines: trimOldest(lines) }, total: lines.length };
}

const indentOf = (line: string) => line.length - line.trimStart().length;
const isDash = (line: string) => /^\s*-(\s|$)/.test(line);
const isBlank = (line: string) => line.trim() === "" || line.trimStart().startsWith("#");

/**
 * The lines of the keys that contain line `at`, outermost first: `spec:`, `  containers:`,
 * `  - name: app`. A list item may sit at the same indent as its key (the Kubernetes style), so
 * a dash line also accepts a parent key at its own indent. Exported for tests.
 */
export function yamlAncestors(lines: readonly string[], at: number): string[] {
  const out: string[] = [];
  const first = lines[at] ?? "";
  let depth = indentOf(first);
  let dash = isDash(first);
  for (let i = at - 1; i >= 0 && depth >= 0; i--) {
    const l = lines[i] ?? "";
    if (isBlank(l)) continue;
    const ind = indentOf(l);
    // Deeper lines and siblings are skipped; a key at a list item's own indent is its parent.
    if (!(ind < depth || (ind === depth && dash && !isDash(l)))) continue;
    out.push(l);
    depth = ind;
    dash = isDash(l);
  }
  return out.reverse();
}

/** The selected lines `from..to`, with the keys that contain the first of them above. */
export function yamlWithPath(lines: readonly string[], from: number, to: number): string[] {
  const lo = Math.min(from, to);
  const hi = Math.max(from, to);
  return [...yamlAncestors(lines, lo), ...lines.slice(lo, hi + 1)];
}
