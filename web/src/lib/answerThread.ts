// Saving an Ask AI answer as a discussion thread, and the prompt that hands that thread to
// Claude Code over MCP. Log lines are never copied into a thread: its readers may not be
// allowed to read the pods' logs. A YAML excerpt may be, because it is what the UI already
// showed after the hub redacted it, and readers can read the same object.

import type { AskAttachment, Ref } from "../api/types";

export const THREAD_TITLE_MAX = 80;

export function threadTitle(question: string): string {
  return question.trim().replace(/\s+/g, " ").slice(0, THREAD_TITLE_MAX);
}

/** A code fence longer than any run of backticks inside `text`. */
function fenceFor(text: string): string {
  const longest = Math.max(0, ...(text.match(/`+/g) ?? []).map((r) => r.length));
  return "`".repeat(Math.max(3, longest + 1));
}

function contextBlock(a: AskAttachment, total: number): string {
  if (a.kind === "logs") {
    const n = total || a.lines.length;
    return `Context: ${n} log line${n === 1 ? "" : "s"} from ${a.source} (not copied)`;
  }
  const text = a.lines.join("\n");
  const fence = fenceFor(text);
  return `Context: YAML of ${a.source}\n\n${fence}yaml\n${text}\n${fence}`;
}

export interface AnswerParts {
  question: string;
  answer: string;
  model?: string;
  provider?: string;
  attachment?: { attachment: AskAttachment; total: number };
}

/** The first message of the thread: question, answer (marked as AI), and the context. */
export function threadBody({ question, answer, model, provider, attachment }: AnswerParts): string {
  const who = [model, provider && `via ${provider}`].filter(Boolean).join(" ");
  const quoted = question
    .trim()
    .split("\n")
    .map((l) => `> ${l}`)
    .join("\n");
  return [
    `**Question**\n\n${quoted}`,
    `**AI answer${who ? ` · ${who}` : ""}**\n\n${answer.trim()}`,
    ...(attachment ? [contextBlock(attachment.attachment, attachment.total)] : []),
  ].join("\n\n");
}

/** What to paste into Claude Code: read the thread, check live state, propose a PR. */
export function claudeCodePrompt(
  threadId: string,
  cluster: string,
  r: Pick<Ref, "kind" | "namespace" | "name">,
) {
  const where = r.namespace ? `${r.namespace}/${r.name}` : r.name;
  const live = r.kind
    ? `with get_resource and get_events for ${cluster} ${r.kind} ${where}`
    : `with list_unhealthy and list_resources on ${cluster}`;
  return (
    `Use the eddy MCP server. Read thread ${threadId} with get_thread, then check the live state ` +
    `${live}. Propose the change in our ` +
    "Flux GitOps repository as a pull request (do not change the cluster directly). Then reply on " +
    "the thread with reply_thread including the PR link."
  );
}
