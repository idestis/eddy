import { describe, expect, it } from "vitest";
import { filterLabel, flatNav, kindInfo, matchesKindFilter, navNode, navPath } from "./kinds";

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
