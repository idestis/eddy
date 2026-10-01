// The kinds Eddy knows about, and the Browse navigation built from them. Keep in sync
// with internal/flux/kinds.go (see "Adding a Flux kind" in CLAUDE.md).

import type { KindSummary, KindsResponse } from "../api/types";
import type { IconName } from "../components/Icon";

/** Top-level Browse sections. */
export type Section =
  | "flux"
  | "workloads"
  | "networking"
  | "storage"
  | "cluster"
  | "karpenter"
  | "external-secrets"
  | "other";

/** The project (docs/api.md "Kinds and projects") an API group belongs to. */
export function projectOf(group: string): string {
  if (group === "" || !group.includes(".")) return "kubernetes";
  if (group.endsWith(".fluxcd.io")) return "flux";
  if (group === "karpenter.sh" || group === "karpenter.k8s.aws") return "karpenter";
  if (group === "external-secrets.io") return "external-secrets";
  if (group.endsWith(".k8s.io")) return "kubernetes";
  return group;
}

const PROJECT_NAMES: Record<string, string> = {
  kubernetes: "Kubernetes",
  flux: "Flux",
  karpenter: "Karpenter",
  "external-secrets": "External Secrets",
};

/** Display name of a project; an API-group project is shown as the group itself. */
export const projectName = (project: string): string => PROJECT_NAMES[project] ?? project;

/** Projects whose kinds are labelled with their project wherever kinds are mixed (headers, palette). */
export const isNotable = (project: string | undefined): boolean =>
  Boolean(project) && project !== "kubernetes" && project !== "flux";

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
  /** The project the kind belongs to ("kubernetes", "flux", "karpenter", …). */
  project: string;
  /** The agent watch preset that turns the kind on, when it is opt-in. */
  preset?: string;
}

const FLUX_KUSTOMIZE = "kustomize.toolkit.fluxcd.io";
const FLUX_HELM = "helm.toolkit.fluxcd.io";
const FLUX_SOURCE = "source.toolkit.fluxcd.io";
const KARPENTER = "karpenter.sh";
const KARPENTER_AWS = "karpenter.k8s.aws";
const ESO = "external-secrets.io";

const k = (
  kind: string,
  group: string,
  abbr: string,
  plural: string,
  icon: IconName,
  section: Section,
  extra: Partial<Pick<KindInfo, "flux" | "hasSource" | "workload" | "preset">> = {},
): KindInfo => ({
  kind,
  group,
  abbr,
  plural,
  icon,
  section,
  flux: false,
  hasSource: false,
  project: projectOf(group),
  ...extra,
});

export const KINDS: readonly KindInfo[] = [
  k("Kustomization", FLUX_KUSTOMIZE, "KS", "Kustomizations", "layers", "flux", {
    flux: true,
    hasSource: true,
  }),
  k("HelmRelease", FLUX_HELM, "HR", "HelmReleases", "helm", "flux", { flux: true, hasSource: true }),
  k("GitRepository", FLUX_SOURCE, "GIT", "GitRepositories", "git", "flux", { flux: true }),
  k("OCIRepository", FLUX_SOURCE, "OCI", "OCIRepositories", "box", "flux", { flux: true }),
  k("HelmRepository", FLUX_SOURCE, "HELM", "HelmRepositories", "helm", "flux", { flux: true }),
  k("HelmChart", FLUX_SOURCE, "CHRT", "HelmCharts", "helm", "flux", { flux: true, hasSource: true }),
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
  k("NetworkPolicy", "networking.k8s.io", "NP", "NetworkPolicies", "wall", "networking"),
  k("PersistentVolumeClaim", "", "PVC", "PersistentVolumeClaims", "disk", "storage"),
  k("StorageClass", "storage.k8s.io", "SC", "StorageClasses", "stack", "storage"),
  k("Namespace", "", "NS", "Namespaces", "ns", "cluster"),
  k("ServiceAccount", "", "SA", "ServiceAccounts", "badge", "cluster"),
  k("PodDisruptionBudget", "policy", "PDB", "PodDisruptionBudgets", "shieldCheck", "cluster"),
  k("NodePool", KARPENTER, "NPL", "NodePools", "server", "karpenter", { preset: "karpenter" }),
  k("NodeClaim", KARPENTER, "NCL", "NodeClaims", "claim", "karpenter", { preset: "karpenter" }),
  k("EC2NodeClass", KARPENTER_AWS, "ENC", "EC2NodeClasses", "chip", "karpenter", { preset: "karpenter" }),
  k("ExternalSecret", ESO, "ES", "ExternalSecrets", "key", "external-secrets", { preset: "externalSecrets" }),
  k("ClusterExternalSecret", ESO, "CES", "ClusterExternalSecrets", "key", "external-secrets", {
    preset: "externalSecrets",
  }),
  k("SecretStore", ESO, "SS", "SecretStores", "vault", "external-secrets", { preset: "externalSecrets" }),
  k("ClusterSecretStore", ESO, "CSS", "ClusterSecretStores", "vault", "external-secrets", {
    preset: "externalSecrets",
  }),
  k("PushSecret", ESO, "PS", "PushSecrets", "upload", "external-secrets", { preset: "externalSecrets" }),
];

const BY_KIND = new Map(KINDS.map((info, i) => [info.kind, { ...info, order: i }]));
const BY_REF = new Map(KINDS.map((info) => [`${info.group}/${info.kind}`, info]));

/** "Policy" → "Policies", "Class" → "Classes", "Agent" → "Agents". */
const pluralOf = (kind: string): string =>
  /[^aeiou]y$/i.test(kind)
    ? `${kind.slice(0, -1)}ies`
    : /(s|x|ch|sh)$/i.test(kind)
      ? `${kind}es`
      : `${kind}s`;

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
      plural: pluralOf(kind),
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
  /** Shown even with no objects (a kind the agent watches); other entries hide at 0. */
  keep?: boolean;
  /** A kind that appears only as inventory-only rows (Eddy does not watch it). */
  inventory?: boolean;
  /** The API group of a leaf, for an id that has to tell groups apart. */
  group?: string;
  /** Hover text. */
  hint?: string;
}

const leaf = (kind: string, label?: string, keep = true): NavNode => {
  const info = kindInfo(kind);
  return { id: kind, label: label ?? info.plural, icon: info.icon, kinds: [kind], keep, group: info.group };
};
const parent = (id: string, label: string, icon: IconName, children: NavNode[]): NavNode => ({
  id,
  label,
  icon,
  kinds: children.flatMap((c) => c.kinds),
  children,
  keep: children.some((c) => c.keep),
});

/** The "Other" entry: every kind outside the registry. Its children come from the kinds endpoint. */
export const OTHER_HINT = "Objects in Flux inventories that Eddy does not watch";
const OTHER: NavNode = { id: "other", label: "Other", icon: "file", kinds: [], hint: OTHER_HINT };

/**
 * The static Browse tree: the fallback for a hub without the kinds endpoint. Every parent is
 * also a page that shows all of its children, grouped by kind. The sections an older hub does
 * not serve (Cluster, Karpenter, External Secrets) hide while they hold nothing.
 */
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
  parent("networking", "Networking", "route", [
    leaf("Service"),
    leaf("Ingress"),
    leaf("NetworkPolicy", undefined, false),
  ]),
  parent("storage", "Storage", "disk", [
    leaf("PersistentVolumeClaim", "Volume claims"),
    leaf("StorageClass", undefined, false),
  ]),
  parent("cluster", "Cluster", "globe", [
    leaf("Namespace", undefined, false),
    leaf("ServiceAccount", undefined, false),
    leaf("PodDisruptionBudget", undefined, false),
  ]),
  parent("karpenter", "Karpenter", "server", [
    leaf("NodePool", undefined, false),
    leaf("NodeClaim", undefined, false),
    leaf("EC2NodeClass", undefined, false),
  ]),
  parent("external-secrets", "External Secrets", "vault", [
    leaf("ExternalSecret", undefined, false),
    leaf("ClusterExternalSecret", undefined, false),
    leaf("SecretStore", undefined, false),
    leaf("ClusterSecretStore", undefined, false),
    leaf("PushSecret", undefined, false),
  ]),
  OTHER,
];

const groupLabel = (group: string): string => group || "core";

/** The list filter of an "Other" group entry: every kind of one API group. */
export const groupFilter = (group: string): string => `g:${groupLabel(group)}`;

/** Builds the Browse tree for a cluster from its kinds; the static tree when there are none. */
export function buildNav(kinds: KindsResponse | null | undefined): readonly NavNode[] {
  if (!kinds) return NAV_TREE;
  const byRef = new Map(kinds.items.map((i) => [`${i.group}/${i.kind}`, i]));

  // A registered kind appears when the agent watches it or it holds objects the user may see.
  const prune = (nodes: readonly NavNode[]): NavNode[] =>
    nodes.flatMap((n): NavNode[] => {
      if (n.id === "other") return [];
      if (n.children) {
        const children = prune(n.children);
        if (!children.length) return [];
        return [
          { ...n, children, kinds: children.flatMap((c) => c.kinds), keep: children.some((c) => c.keep) },
        ];
      }
      const item = byRef.get(`${n.group ?? ""}/${n.id}`);
      if (!item || (item.count === 0 && !item.watched)) return [];
      return [{ ...n, keep: item.watched, inventory: !item.watched }];
    });
  const sections = prune(NAV_TREE);

  // Everything else comes from inventory: one entry per API group, one leaf per kind.
  const other = kinds.items.filter((i) => !BY_REF.has(`${i.group}/${i.kind}`) && i.count > 0);
  const groups = new Map<string, KindSummary[]>();
  for (const i of other) groups.set(i.group, [...(groups.get(i.group) ?? []), i]);
  const seen = new Map<string, number>();
  for (const i of other) seen.set(i.kind, (seen.get(i.kind) ?? 0) + 1);
  const entries: NavNode[] = [...groups.entries()]
    .sort(([a], [b]) => groupLabel(a).localeCompare(groupLabel(b)))
    .map(([group, items]) => {
      const children = items
        .sort((a, b) => a.kind.localeCompare(b.kind))
        .map(
          (i): NavNode => ({
            // The bare Kind unless two groups share it.
            id: (seen.get(i.kind) ?? 0) > 1 ? `${groupLabel(group)}/${i.kind}` : i.kind,
            label: kindInfo(i.kind).plural,
            icon: "file",
            kinds: [i.kind],
            inventory: true,
            group,
          }),
        );
      return {
        id: groupFilter(group),
        label: groupLabel(group),
        icon: "box",
        kinds: children.flatMap((c) => c.kinds),
        children,
        group,
      };
    });
  if (!entries.length) return sections;
  return [...sections, { ...OTHER, kinds: entries.flatMap((e) => e.kinds), children: entries }];
}

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

function locate(id: string, nodes: readonly NavNode[], up?: NavNode): Located | undefined {
  for (const node of nodes) {
    if (node.id === id) return { node, parent: up, siblings: nodes };
    const found = node.children && locate(id, node.children, node);
    if (found) return found;
  }
  return undefined;
}

/** The nav entry for a `?kind=` filter, with its parent and siblings. */
export function navNode(
  filter: string | undefined,
  nodes: readonly NavNode[] = NAV_TREE,
): Located | undefined {
  if (!filter) return undefined;
  return locate(LEGACY[filter] ?? filter, nodes);
}

/** The ids from the root down to `filter`, for opening the tree around the active entry. */
export function navPath(filter: string | undefined, nodes: readonly NavNode[] = NAV_TREE): string[] {
  const out: string[] = [];
  let at = navNode(filter, nodes);
  while (at) {
    out.unshift(at.node.id);
    at = at.parent ? navNode(at.parent.id, nodes) : undefined;
  }
  return out;
}

/** Every nav entry in display order (depth first), for the palette's "Go to …" commands. */
export function flatNav(nodes: readonly NavNode[] = NAV_TREE): NavNode[] {
  return nodes.flatMap((n) => [n, ...(n.children ? flatNav(n.children) : [])]);
}

const REGISTERED = new Set(KINDS.map((info) => info.kind));

export const isRegistered = (kind: string): boolean => REGISTERED.has(kind);

/**
 * Object counts for every entry of a nav tree, in one pass over `items`. A parent counts its
 * children; "Other" counts every object whose kind is outside the registry.
 */
export function navCounts(
  nodes: readonly NavNode[],
  items: ReadonlyArray<{ kind: string; group: string }>,
): Map<string, number> {
  const byKind = new Map<string, number>();
  const byRef = new Map<string, number>();
  let unregistered = 0;
  for (const r of items) {
    byKind.set(r.kind, (byKind.get(r.kind) ?? 0) + 1);
    const ref = `${groupLabel(r.group)}/${r.kind}`;
    byRef.set(ref, (byRef.get(ref) ?? 0) + 1);
    if (!REGISTERED.has(r.kind)) unregistered++;
  }
  const out = new Map<string, number>();
  const count = (n: NavNode): number => {
    let total: number;
    if (n.id === "other") {
      if (n.children) n.children.forEach(count);
      total = unregistered;
    } else if (n.children) {
      total = n.children.reduce((sum, c) => sum + count(c), 0);
    } else if (n.id.includes("/")) {
      total = byRef.get(n.id) ?? 0;
    } else {
      total = byKind.get(n.id) ?? 0;
    }
    out.set(n.id, total);
    return total;
  };
  nodes.forEach(count);
  return out;
}

/**
 * True when an object matches a list filter: a nav id (section, group or Kind), `g:<group>`
 * for an inventory group, `<group>/<Kind>` for a Kind two groups share, or any Kind name.
 * `group` is the object's API group; without it only the Kind is compared.
 */
export function matchesKindFilter(kind: string, filter: string | undefined, group?: string): boolean {
  if (!filter) return true;
  if (filter.startsWith("g:")) return group !== undefined && groupLabel(group) === filter.slice(2);
  const slash = filter.indexOf("/");
  if (slash > 0) return group !== undefined && `${groupLabel(group)}/${kind}` === filter;
  const at = navNode(filter);
  if (!at) return kind === filter;
  if (at.node.id === "other") return !REGISTERED.has(kind);
  return at.node.kinds.includes(kind);
}

/** The label of a list filter, for titles and breadcrumbs. */
export function filterLabel(filter: string | undefined, nodes: readonly NavNode[] = NAV_TREE): string {
  if (!filter) return "All resources";
  const found = navNode(filter, nodes) ?? navNode(filter);
  if (found) return found.node.label;
  if (filter.startsWith("g:")) return filter.slice(2);
  const slash = filter.indexOf("/");
  return slash > 0 ? kindInfo(filter.slice(slash + 1)).plural : kindInfo(filter).plural;
}

/** The heading of a kind group in a mixed list: "Karpenter · NodePools" for notable projects. */
export function kindHeading(kind: string, project: string | undefined, plural?: string): string {
  const label = plural ?? kindInfo(kind).plural;
  return isNotable(project) && project ? `${projectName(project)} · ${label}` : label;
}
