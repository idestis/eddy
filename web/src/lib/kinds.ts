// The kinds Eddy knows about, and the Browse navigation built from them. Keep in sync
// with internal/flux/kinds.go (see "Adding a Flux kind" in CLAUDE.md).

import type { IconName } from "../components/Icon";

/** Top-level Browse sections. */
export type Section = "flux" | "workloads" | "networking" | "storage" | "other";

export interface KindInfo {
  kind: string;
  group: string;
  /** Short label shown in rows and the palette. */
  abbr: string;
  plural: string;
  icon: IconName;
  section: Section;
  /** Flux objects can be reconciled and suspended. */
  flux: boolean;
  /** Reconcile "with source" is meaningful (the object has a sourceRef). */
  hasSource: boolean;
  /** Workloads show replicas and an image instead of a revision. */
  workload?: boolean;
}

const FLUX_KUSTOMIZE = "kustomize.toolkit.fluxcd.io";
const FLUX_HELM = "helm.toolkit.fluxcd.io";
const FLUX_SOURCE = "source.toolkit.fluxcd.io";

const k = (
  kind: string,
  group: string,
  abbr: string,
  plural: string,
  icon: IconName,
  section: Section,
  extra: Partial<Pick<KindInfo, "flux" | "hasSource" | "workload">> = {},
): KindInfo => ({ kind, group, abbr, plural, icon, section, flux: false, hasSource: false, ...extra });

export const KINDS: readonly KindInfo[] = [
  k("Kustomization", FLUX_KUSTOMIZE, "KS", "Kustomizations", "layers", "flux", {
    flux: true,
    hasSource: true,
  }),
  k("HelmRelease", FLUX_HELM, "HR", "HelmReleases", "helm", "flux", { flux: true, hasSource: true }),
  k("GitRepository", FLUX_SOURCE, "GIT", "GitRepositories", "git", "flux", { flux: true }),
  k("OCIRepository", FLUX_SOURCE, "OCI", "OCIRepositories", "box", "flux", { flux: true }),
  k("HelmRepository", FLUX_SOURCE, "HELM", "HelmRepositories", "helm", "flux", { flux: true }),
  k("HelmChart", FLUX_SOURCE, "CHRT", "HelmCharts", "box", "flux", { flux: true, hasSource: true }),
  k("Bucket", FLUX_SOURCE, "BKT", "Buckets", "bucket", "flux", { flux: true }),
  k("Deployment", "apps", "DEP", "Deployments", "grid", "workloads", { workload: true }),
  k("StatefulSet", "apps", "STS", "StatefulSets", "db", "workloads", { workload: true }),
  k("DaemonSet", "apps", "DS", "DaemonSets", "nodes", "workloads", { workload: true }),
  k("Job", "batch", "JOB", "Jobs", "play", "workloads", { workload: true }),
  k("CronJob", "batch", "CJ", "CronJobs", "clock", "workloads", { workload: true }),
  k("Pod", "", "POD", "Pods", "cube", "workloads", { workload: true }),
  k("HorizontalPodAutoscaler", "autoscaling", "HPA", "HPAs", "scale", "workloads"),
  k("Service", "", "SVC", "Services", "plug", "networking"),
  k("Ingress", "networking.k8s.io", "ING", "Ingresses", "route", "networking"),
  k("PersistentVolumeClaim", "", "PVC", "PersistentVolumeClaims", "disk", "storage"),
];

const BY_KIND = new Map(KINDS.map((info, i) => [info.kind, { ...info, order: i }]));

const UNKNOWN: KindInfo & { order: number } = {
  ...k("", "", "?", "Other", "file", "other"),
  order: KINDS.length,
};

/** Registry entry for a kind. Kinds Eddy does not watch (inventory-only) land in "other". */
export function kindInfo(kind: string): KindInfo & { order: number } {
  return (
    BY_KIND.get(kind) ?? {
      ...UNKNOWN,
      kind,
      abbr: kind.replace(/[a-z]/g, "").slice(0, 4) || kind.slice(0, 3).toUpperCase(),
      plural: kind.endsWith("s") ? `${kind}es` : `${kind}s`,
    }
  );
}

export const isFlux = (kind: string): boolean => kindInfo(kind).flux;

/** One Browse entry. `id` is the list filter (`?kind=`): a section id, a group id or a Kind. */
export interface NavNode {
  id: string;
  label: string;
  icon: IconName;
  /** The kinds this entry shows; empty for "other" (every kind outside the registry). */
  kinds: readonly string[];
  children?: readonly NavNode[];
}

const leaf = (kind: string, label?: string): NavNode => {
  const info = kindInfo(kind);
  return { id: kind, label: label ?? info.plural, icon: info.icon, kinds: [kind] };
};
const parent = (id: string, label: string, icon: IconName, children: NavNode[]): NavNode => ({
  id,
  label,
  icon,
  kinds: children.flatMap((c) => c.kinds),
  children,
});

/** The Browse tree: every parent is also a page that shows all of its children, grouped by kind. */
export const NAV_TREE: readonly NavNode[] = [
  parent("flux", "Flux", "flux", [
    leaf("Kustomization"),
    leaf("HelmRelease"),
    parent("sources", "Sources", "git", [
      leaf("GitRepository", "Git repositories"),
      leaf("OCIRepository", "OCI repositories"),
      leaf("HelmRepository", "Helm repositories"),
      leaf("HelmChart", "Helm charts"),
      leaf("Bucket", "Buckets"),
    ]),
  ]),
  parent("workloads", "Workloads", "grid", [
    leaf("Deployment"),
    leaf("StatefulSet"),
    leaf("DaemonSet"),
    leaf("Job"),
    leaf("CronJob"),
    leaf("Pod"),
    leaf("HorizontalPodAutoscaler", "Autoscalers"),
  ]),
  parent("networking", "Networking", "route", [leaf("Service"), leaf("Ingress")]),
  parent("storage", "Storage", "disk", [leaf("PersistentVolumeClaim", "Volume claims")]),
  { id: "other", label: "Inventory only", icon: "file", kinds: [] },
];

// Filters from earlier versions of the URL scheme keep working.
const LEGACY: Record<string, string> = {
  kustomizations: "Kustomization",
  helmreleases: "HelmRelease",
  pods: "Pod",
};

interface Located {
  node: NavNode;
  parent?: NavNode;
  siblings: readonly NavNode[];
}

function locate(id: string, nodes: readonly NavNode[] = NAV_TREE, up?: NavNode): Located | undefined {
  for (const node of nodes) {
    if (node.id === id) return { node, parent: up, siblings: nodes };
    const found = node.children && locate(id, node.children, node);
    if (found) return found;
  }
  return undefined;
}

/** The nav entry for a `?kind=` filter, with its parent and siblings. */
export function navNode(filter: string | undefined): Located | undefined {
  if (!filter) return undefined;
  return locate(LEGACY[filter] ?? filter);
}

/** The ids from the root down to `filter`, for opening the tree around the active entry. */
export function navPath(filter: string | undefined): string[] {
  const out: string[] = [];
  let at = navNode(filter);
  while (at) {
    out.unshift(at.node.id);
    at = at.parent ? navNode(at.parent.id) : undefined;
  }
  return out;
}

/** Every nav entry in display order (depth first), for the palette's "Go to …" commands. */
export function flatNav(nodes: readonly NavNode[] = NAV_TREE): NavNode[] {
  return nodes.flatMap((n) => [n, ...(n.children ? flatNav(n.children) : [])]);
}

const REGISTERED = new Set(KINDS.map((info) => info.kind));

/** True when `kind` matches a list filter: a nav id (section, group or Kind) or any Kind name. */
export function matchesKindFilter(kind: string, filter: string | undefined): boolean {
  if (!filter) return true;
  const at = navNode(filter);
  if (!at) return kind === filter;
  if (at.node.id === "other") return !REGISTERED.has(kind);
  return at.node.kinds.includes(kind);
}

/** The label of a list filter, for titles and breadcrumbs. */
export function filterLabel(filter: string | undefined): string {
  if (!filter) return "All resources";
  return navNode(filter)?.node.label ?? kindInfo(filter).plural;
}
