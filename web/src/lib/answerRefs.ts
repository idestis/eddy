// Links from Ask AI answers to Eddy's own resource pages. The hub stores, on each answer,
// `meta.refs`: the resources the tools returned as the asking user (docs/api.md "Ask AI").
// An inline code span in the answer links to one of them only when it names exactly that
// ref. The model never supplies a link target; its text can only select a ref the hub sent.

import type { ResourceRef } from "../api/types";
import { kindInfo } from "./kinds";

/** The upper bound the hub applies; anything past it is ignored. */
const MAX_REFS = 200;
/** Longer code spans are commands or messages, not object names. */
const MAX_NAME_LEN = 320;

const isStr = (v: unknown): v is string => typeof v === "string";

/** The well-formed refs in an AI message's meta, or [] when there are none. */
export function refsOf(meta: unknown): ResourceRef[] {
  const raw = (meta as { refs?: unknown } | null | undefined)?.refs;
  if (!Array.isArray(raw)) return [];
  const out: ResourceRef[] = [];
  for (const r of raw.slice(0, MAX_REFS)) {
    if (!r || typeof r !== "object") continue;
    const { cluster, group, kind, namespace, name } = r as Record<string, unknown>;
    if (!isStr(cluster) || !isStr(kind) || !isStr(name) || !cluster || !kind || !name) continue;
    out.push({
      cluster,
      group: isStr(group) ? group : "",
      kind,
      namespace: isStr(namespace) ? namespace : "",
      name,
    });
  }
  return out;
}

/** "HelmRelease", "helmrelease" and the short label "HR" all name the HelmRelease kind. */
function kindMatches(token: string, kind: string): boolean {
  const t = token.toLowerCase();
  return t === kind.toLowerCase() || t === kindInfo(kind).abbr.toLowerCase();
}

/**
 * The one ref that an inline code span names, or null when none or several do. Accepted
 * forms: `Kind/namespace/name`, `Kind/name` (cluster-scoped, or a unique namespace),
 * `namespace/name`, and a bare `name` that is unique among the refs.
 */
export function matchRef(code: string, refs: readonly ResourceRef[]): ResourceRef | null {
  const text = code.trim();
  if (!text || text.length > MAX_NAME_LEN || /\s/.test(text) || refs.length === 0) return null;
  const parts = text.split("/");
  if (parts.some((p) => p === "")) return null;
  let hits: ResourceRef[];
  if (parts.length === 3) {
    const [kind = "", ns, name] = parts;
    hits = refs.filter((r) => r.namespace === ns && r.name === name && kindMatches(kind, r.kind));
  } else if (parts.length === 2) {
    const [a = "", b] = parts;
    hits = refs.filter((r) => r.name === b && (kindMatches(a, r.kind) || r.namespace === a));
  } else if (parts.length === 1) {
    hits = refs.filter((r) => r.name === text);
  } else {
    return null;
  }
  return hits.length === 1 ? (hits[0] ?? null) : null;
}

/** The hover text of a ref link: "Open HelmRelease apps/podinfo". */
export function refTitle(r: ResourceRef, withCluster = false): string {
  const name = r.namespace ? `${r.namespace}/${r.name}` : r.name;
  return `Open ${r.kind} ${name}${withCluster ? ` in ${r.cluster}` : ""}`;
}
