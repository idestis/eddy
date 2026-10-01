// Typed link targets shared by the list, palette, fleet and threads views.

import type { Ref } from "../api/types";
import { isRegistered } from "./kinds";

export type DetailView = "overview" | "yaml" | "events" | "logs" | "threads";

export const DETAIL_VIEWS: readonly DetailView[] = ["overview", "yaml", "events", "logs", "threads"];

/** Route params use "_" for the namespace of cluster-scoped objects. */
export const nsParam = (namespace: string): string => namespace || "_";
export const nsFromParam = (ns: string): string => (ns === "_" ? "" : ns);

/**
 * The detail page of an object. A kind outside the registry (inventory-only) carries its API
 * group in `?group=` (`core` for the core group), because two groups may share a Kind.
 */
export function detailLink(
  cluster: string,
  r: Pick<Ref, "kind" | "namespace" | "name"> & { group?: string },
  view?: DetailView,
) {
  const search: { view?: DetailView; group?: string } = {};
  if (view && view !== "overview") search.view = view;
  if (r.group !== undefined && !isRegistered(r.kind)) search.group = r.group || "core";
  return {
    to: "/c/$cluster/r/$kind/$ns/$name",
    params: { cluster, kind: r.kind, ns: nsParam(r.namespace), name: r.name },
    search,
  } as const;
}

/** The cluster's Jobs list, optionally in one namespace (where job-buildup findings point). */
export function jobsLink(cluster: string, namespace?: string) {
  return {
    to: "/c/$cluster",
    params: { cluster },
    search: namespace ? { kind: "Job", namespace } : { kind: "Job" },
  } as const;
}

/** Only local paths are accepted, never "//host" or "/\host", to avoid open redirects. */
export function safeReturnTo(value: string | undefined): string {
  if (!value?.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) return "/";
  if (value.startsWith("/login")) return "/";
  return value;
}
