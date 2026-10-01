import { describe, expect, it } from "vitest";
import type { KindsResponse } from "../api/types";
import {
  buildNav,
  filterLabel,
  flatNav,
  kindHeading,
  kindInfo,
  matchesKindFilter,
  NAV_TREE,
  type NavNode,
  navCounts,
  navNode,
  navPath,
  projectOf,
} from "./kinds";

describe("nav tree", () => {
  it("puts Pods, Jobs and CronJobs under Workloads, and Services and Ingresses under Networking", () => {
    expect(navNode("Pod")?.parent?.id).toBe("workloads");
    expect(navNode("CronJob")?.parent?.id).toBe("workloads");
    expect(navNode("Service")?.parent?.id).toBe("networking");
    expect(navNode("Ingress")?.parent?.id).toBe("networking");
    expect(navPath("GitRepository")).toEqual(["flux", "sources", "GitRepository"]);
  });

  it("matches parents, leaves, legacy ids and inventory-only kinds", () => {
    expect(matchesKindFilter("StatefulSet", "workloads")).toBe(true);
    expect(matchesKindFilter("Service", "workloads")).toBe(false);
    expect(matchesKindFilter("Deployment", "Deployment")).toBe(true);
    expect(matchesKindFilter("HelmRelease", "helmreleases")).toBe(true);
    expect(matchesKindFilter("OCIRepository", "sources")).toBe(true);
    expect(matchesKindFilter("ConfigMap", "other")).toBe(true);
    expect(matchesKindFilter("Deployment", "other")).toBe(false);
  });

  it("labels filters and kinds", () => {
    expect(filterLabel(undefined)).toBe("All resources");
    expect(filterLabel("networking")).toBe("Networking");
    expect(kindInfo("HorizontalPodAutoscaler").abbr).toBe("HPA");
    expect(kindInfo("ServiceAccount").abbr).toBe("SA");
    expect(flatNav().map((n) => n.id)).toContain("PersistentVolumeClaim");
  });
});

const item = (
  kind: string,
  group: string,
  count: number,
  watched = true,
): KindsResponse["items"][number] => ({
  group,
  kind,
  namespaced: true,
  project: projectOf(group),
  watched,
  count,
});

const ids = (nodes: readonly NavNode[]): string[] => nodes.map((n) => n.id);

describe("buildNav", () => {
  const kinds: KindsResponse = {
    items: [
      item("Kustomization", "kustomize.toolkit.fluxcd.io", 4),
      item("Deployment", "apps", 0),
      item("Namespace", "", 3),
      item("NodePool", "karpenter.sh", 2),
      item("NodeClaim", "karpenter.sh", 0),
      item("ExternalSecret", "external-secrets.io", 0, false),
      item("PushSecret", "external-secrets.io", 1, false),
      item("ConfigMap", "", 12, false),
      item("Secret", "", 2, false),
      item("DatadogAgent", "datadoghq.com", 1, false),
      item("DatadogMonitor", "datadoghq.com", 3, false),
    ],
    projects: [],
    presets: ["karpenter"],
  };
  const nav = buildNav(kinds);

  it("groups the registered kinds into their project sections, in order", () => {
    expect(ids(nav)).toEqual(["flux", "workloads", "cluster", "karpenter", "external-secrets", "other"]);
    expect(ids(nav.find((n) => n.id === "karpenter")?.children ?? [])).toEqual(["NodePool", "NodeClaim"]);
  });

  it("shows a watched kind at 0 and hides an unwatched kind at 0", () => {
    const workloads = nav.find((n) => n.id === "workloads");
    expect(ids(workloads?.children ?? [])).toEqual(["Deployment"]);
    expect(workloads?.children?.[0]?.keep).toBe(true);
    // ExternalSecret is inventory-only here with no rows; PushSecret has one.
    expect(ids(nav.find((n) => n.id === "external-secrets")?.children ?? [])).toEqual(["PushSecret"]);
  });

  it("marks kinds that appear only as inventory rows", () => {
    const push = flatNav(nav).find((n) => n.id === "PushSecret");
    expect(push?.inventory).toBe(true);
    expect(push?.keep).toBe(false);
    expect(flatNav(nav).find((n) => n.id === "NodePool")?.inventory).toBe(false);
  });

  it("collects the rest under Other, one entry per API group labelled with the group", () => {
    const other = nav.find((n) => n.id === "other");
    expect(other?.children?.map((g) => [g.id, g.label])).toEqual([
      ["g:core", "core"],
      ["g:datadoghq.com", "datadoghq.com"],
    ]);
    expect(ids(other?.children?.[1]?.children ?? [])).toEqual(["DatadogAgent", "DatadogMonitor"]);
    expect(other?.children?.[1]?.children?.[0]?.label).toBe("DatadogAgents");
    expect(other?.children?.[0]?.children?.every((k) => k.inventory)).toBe(true);
  });

  it("leaves Other out when nothing remains, and falls back to the static tree without kinds", () => {
    expect(ids(buildNav({ items: [item("Pod", "", 1)], projects: [], presets: [] }))).toEqual(["workloads"]);
    expect(buildNav(null)).toBe(NAV_TREE);
    expect(buildNav(undefined)).toBe(NAV_TREE);
    expect(ids(NAV_TREE)).toContain("karpenter");
  });

  it("tells apart a Kind that two groups share", () => {
    const shared = buildNav({
      items: [item("Certificate", "cert-manager.io", 2, false), item("Certificate", "example.dev", 1, false)],
      projects: [],
      presets: [],
    });
    const leaves = flatNav(shared).filter((n) => !n.children && n.kinds[0] === "Certificate");
    expect(leaves.map((n) => n.id)).toEqual(["cert-manager.io/Certificate", "example.dev/Certificate"]);
    expect(matchesKindFilter("Certificate", "example.dev/Certificate", "example.dev")).toBe(true);
    expect(matchesKindFilter("Certificate", "example.dev/Certificate", "cert-manager.io")).toBe(false);
  });
});

describe("kind filters and counts", () => {
  it("matches an inventory group by API group, core included", () => {
    expect(matchesKindFilter("DatadogAgent", "g:datadoghq.com", "datadoghq.com")).toBe(true);
    expect(matchesKindFilter("DatadogAgent", "g:core", "datadoghq.com")).toBe(false);
    expect(matchesKindFilter("ConfigMap", "g:core", "")).toBe(true);
    expect(matchesKindFilter("NodePool", "karpenter")).toBe(true);
    expect(matchesKindFilter("Pod", "karpenter")).toBe(false);
  });

  it("counts every entry in one pass, with Other taking the unregistered kinds", () => {
    const nav = buildNav({
      items: [
        item("NodePool", "karpenter.sh", 2),
        item("ConfigMap", "", 3, false),
        item("DatadogAgent", "datadoghq.com", 1, false),
      ],
      projects: [],
      presets: [],
    });
    const rows = [
      { kind: "NodePool", group: "karpenter.sh" },
      { kind: "NodePool", group: "karpenter.sh" },
      { kind: "ConfigMap", group: "" },
      { kind: "ConfigMap", group: "" },
      { kind: "DatadogAgent", group: "datadoghq.com" },
    ];
    const counts = navCounts(nav, rows);
    expect(counts.get("karpenter")).toBe(2);
    expect(counts.get("NodePool")).toBe(2);
    expect(counts.get("other")).toBe(3);
    expect(counts.get("g:core")).toBe(2);
    expect(counts.get("g:datadoghq.com")).toBe(1);
  });

  it("names projects and headings", () => {
    expect(projectOf("karpenter.k8s.aws")).toBe("karpenter");
    expect(projectOf("storage.k8s.io")).toBe("kubernetes");
    expect(projectOf("notification.toolkit.fluxcd.io")).toBe("flux");
    expect(projectOf("datadoghq.com")).toBe("datadoghq.com");
    expect(kindHeading("NodePool", "karpenter")).toBe("Karpenter · NodePools");
    expect(kindHeading("Deployment", "kubernetes")).toBe("Deployments");
    expect(kindInfo("NetworkPolicy").plural).toBe("NetworkPolicies");
    expect(kindInfo("DatadogAgent").plural).toBe("DatadogAgents");
    expect(filterLabel("g:datadoghq.com")).toBe("datadoghq.com");
  });
});
