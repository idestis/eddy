// The context of an Ask AI chat (ADR-0007): references to resources, or (kind "") to whole
// clusters, from any cluster. Pure helpers shared by the panel, the pickers and the mock hub.

import type { Resource, ResourceRef } from "../api/types";

/** At most this many references per chat (store.MaxChatContext). */
export const MAX_CONTEXT = 10;

export function resourceRef(cluster: string, r: Pick<Resource, "group" | "kind" | "namespace" | "name">) {
  return {
    cluster,
    group: r.group,
    kind: r.kind,
    namespace: r.namespace,
    name: r.name,
  } satisfies ResourceRef;
}

export function clusterRef(cluster: string): ResourceRef {
  return { cluster, group: "", kind: "", namespace: "", name: "" };
}

export const isClusterRef = (r: ResourceRef): boolean => r.kind === "";

/** A stable identity for a reference. */
export function contextKey(r: ResourceRef): string {
  return isClusterRef(r) ? r.cluster : `${r.cluster}/${r.group}/${r.kind}/${r.namespace}/${r.name}`;
}

export const sameRef = (a: ResourceRef, b: ResourceRef): boolean => contextKey(a) === contextKey(b);

export const hasRef = (list: readonly ResourceRef[], r: ResourceRef): boolean =>
  list.some((x) => sameRef(x, r));

export function sameContext(a: readonly ResourceRef[], b: readonly ResourceRef[]): boolean {
  return a.length === b.length && a.every((r, i) => sameRef(r, b[i] as ResourceRef));
}

/** Adds a reference once, keeping the order and the limit. */
export function withRef(list: readonly ResourceRef[], r: ResourceRef): ResourceRef[] {
  if (hasRef(list, r) || list.length >= MAX_CONTEXT) return [...list];
  return [...list, r];
}

export const withoutRef = (list: readonly ResourceRef[], r: ResourceRef): ResourceRef[] =>
  list.filter((x) => !sameRef(x, r));

/** The text an @mention inserts: `@namespace/name`, `@name` when cluster-scoped, `@cluster`. */
export function mentionText(r: ResourceRef): string {
  if (isClusterRef(r)) return `@${r.cluster}`;
  return r.namespace ? `@${r.namespace}/${r.name}` : `@${r.name}`;
}

/** A one-line description, for titles and accessible names: "Kustomization flux-system/apps on staging". */
export function refLabel(r: ResourceRef): string {
  if (isClusterRef(r)) return `the whole cluster ${r.cluster}`;
  const where = r.namespace ? `${r.namespace}/${r.name}` : r.name;
  return `${r.kind} ${where} on ${r.cluster}`;
}

/**
 * The @mention being typed at the caret: the text after an `@` at the start or after a
 * space, up to the caret. Undefined when the caret is not in a mention.
 */
export function mentionAt(text: string, caret: number): { start: number; query: string } | undefined {
  const before = text.slice(0, caret);
  const m = /(^|\s)@([^\s@]{0,80})$/.exec(before);
  if (!m) return undefined;
  const query = m[2] ?? "";
  return { start: caret - query.length - 1, query };
}

/** What a chat starts with: the selected resource, else the current cluster, else nothing. */
export function screenContext(cluster: string | undefined, r: Resource | undefined): ResourceRef[] {
  if (cluster && r) return [resourceRef(cluster, r)];
  if (cluster) return [clusterRef(cluster)];
  return [];
}
