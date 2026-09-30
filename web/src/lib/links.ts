// Typed link targets shared by the list, palette, fleet and threads views.

import type { Ref } from "../api/types";

export type DetailView = "overview" | "yaml" | "events" | "logs" | "threads";

export const DETAIL_VIEWS: readonly DetailView[] = ["overview", "yaml", "events", "logs", "threads"];

/** Route params use "_" for the namespace of cluster-scoped objects. */
export const nsParam = (namespace: string): string => namespace || "_";
export const nsFromParam = (ns: string): string => (ns === "_" ? "" : ns);

export function detailLink(cluster: string, r: Pick<Ref, "kind" | "namespace" | "name">, view?: DetailView) {
  return {
    to: "/c/$cluster/r/$kind/$ns/$name",
    params: { cluster, kind: r.kind, ns: nsParam(r.namespace), name: r.name },
    search: view && view !== "overview" ? { view } : {},
  } as const;
}

/** Only local paths are accepted, never "//host" or "/\host", to avoid open redirects. */
export function safeReturnTo(value: string | undefined): string {
  if (!value?.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) return "/";
  if (value.startsWith("/login")) return "/";
  return value;
}
