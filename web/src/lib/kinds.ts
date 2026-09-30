// The kinds Eddy knows about. Keep in sync with internal/flux/kinds.go
// (see "Adding a Flux kind" in CLAUDE.md).

import type { IconName } from "../components/Icon";

export type KindGroup = "kustomizations" | "helmreleases" | "sources" | "workloads" | "pods";

export interface KindInfo {
  kind: string;
  group: string;
  /** Short label shown in rows and the palette. */
  abbr: string;
  plural: string;
  icon: IconName;
  nav: KindGroup;
  /** Flux objects can be reconciled and suspended. */
  flux: boolean;
  /** Reconcile "with source" is meaningful (the object has a sourceRef). */
  hasSource: boolean;
}

const FLUX_KUSTOMIZE = "kustomize.toolkit.fluxcd.io";
const FLUX_HELM = "helm.toolkit.fluxcd.io";
const FLUX_SOURCE = "source.toolkit.fluxcd.io";

export const KINDS: readonly KindInfo[] = [
  {
    kind: "Kustomization",
    group: FLUX_KUSTOMIZE,
    abbr: "KS",
    plural: "Kustomizations",
    icon: "layers",
    nav: "kustomizations",
    flux: true,
    hasSource: true,
  },
  {
    kind: "HelmRelease",
    group: FLUX_HELM,
    abbr: "HR",
    plural: "HelmReleases",
    icon: "helm",
    nav: "helmreleases",
    flux: true,
    hasSource: true,
  },
  {
    kind: "GitRepository",
    group: FLUX_SOURCE,
    abbr: "GIT",
    plural: "GitRepositories",
    icon: "git",
    nav: "sources",
    flux: true,
    hasSource: false,
  },
  {
    kind: "OCIRepository",
    group: FLUX_SOURCE,
    abbr: "OCI",
    plural: "OCIRepositories",
    icon: "box",
    nav: "sources",
    flux: true,
    hasSource: false,
  },
  {
    kind: "HelmRepository",
    group: FLUX_SOURCE,
    abbr: "HELM",
    plural: "HelmRepositories",
    icon: "helm",
    nav: "sources",
    flux: true,
    hasSource: false,
  },
  {
    kind: "HelmChart",
    group: FLUX_SOURCE,
    abbr: "CHRT",
    plural: "HelmCharts",
    icon: "box",
    nav: "sources",
    flux: true,
    hasSource: true,
  },
  {
    kind: "Bucket",
    group: FLUX_SOURCE,
    abbr: "BKT",
    plural: "Buckets",
    icon: "bucket",
    nav: "sources",
    flux: true,
    hasSource: false,
  },
  {
    kind: "Deployment",
    group: "apps",
    abbr: "DEP",
    plural: "Deployments",
    icon: "grid",
    nav: "workloads",
    flux: false,
    hasSource: false,
  },
  {
    kind: "StatefulSet",
    group: "apps",
    abbr: "STS",
    plural: "StatefulSets",
    icon: "grid",
    nav: "workloads",
    flux: false,
    hasSource: false,
  },
  {
    kind: "DaemonSet",
    group: "apps",
    abbr: "DS",
    plural: "DaemonSets",
    icon: "grid",
    nav: "workloads",
    flux: false,
    hasSource: false,
  },
  {
    kind: "Pod",
    group: "",
    abbr: "POD",
    plural: "Pods",
    icon: "cube",
    nav: "pods",
    flux: false,
    hasSource: false,
  },
];

const BY_KIND = new Map(KINDS.map((k, i) => [k.kind, { ...k, order: i }]));

const UNKNOWN: KindInfo & { order: number } = {
  kind: "",
  group: "",
  abbr: "?",
  plural: "Other",
  icon: "cube",
  nav: "workloads",
  flux: false,
  hasSource: false,
  order: KINDS.length,
};

export function kindInfo(kind: string): KindInfo & { order: number } {
  return BY_KIND.get(kind) ?? { ...UNKNOWN, kind, abbr: kind.slice(0, 4).toUpperCase(), plural: `${kind}s` };
}

export const isFlux = (kind: string): boolean => kindInfo(kind).flux;

export interface NavGroup {
  id: KindGroup;
  label: string;
  icon: IconName;
}

export const NAV_GROUPS: readonly NavGroup[] = [
  { id: "kustomizations", label: "Kustomizations", icon: "layers" },
  { id: "helmreleases", label: "HelmReleases", icon: "helm" },
  { id: "sources", label: "Sources", icon: "git" },
  { id: "workloads", label: "Workloads", icon: "grid" },
  { id: "pods", label: "Pods", icon: "cube" },
];

/** True when `kind` matches a list filter that is either a nav group id or a Kind name. */
export function matchesKindFilter(kind: string, filter: string | undefined): boolean {
  if (!filter) return true;
  if (NAV_GROUPS.some((g) => g.id === filter)) return kindInfo(kind).nav === filter;
  return kind === filter;
}
