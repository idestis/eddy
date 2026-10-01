import { describe, expect, it } from "vitest";
import type { ClusterInfo } from "../api/types";
import { resource } from "../test/fixtures";
import { buildRows, filterResources, needsAttention, statusCounts, summaryCounts } from "./resourceRows";

const items = [
  resource("apps", { status: "failed", message: "Health check failed" }),
  resource("infra"),
  resource("podinfo", {
    kind: "HelmRelease",
    group: "helm.toolkit.fluxcd.io",
    namespace: "apps",
    status: "suspended",
  }),
  resource("web-1", { kind: "Pod", group: "", namespace: "apps" }),
];

describe("filterResources", () => {
  it("filters by text terms, kind group and status", () => {
    expect(filterResources(items, { text: "health" }).map((r) => r.name)).toEqual(["apps"]);
    expect(filterResources(items, { text: "apps pod" }).map((r) => r.name)).toEqual(["podinfo", "web-1"]);
    expect(filterResources(items, { kind: "helmreleases" }).map((r) => r.name)).toEqual(["podinfo"]);
    expect(filterResources(items, { kind: "Pod" }).map((r) => r.name)).toEqual(["web-1"]);
    expect(filterResources(items, { status: "attention" }).map((r) => r.name)).toEqual(["apps", "podinfo"]);
  });
});

describe("buildRows", () => {
  it("groups by kind in registry order with failing first", () => {
    const rows = buildRows(items, true);
    expect(rows.map((r) => (r.type === "group" ? `#${r.kind}` : r.resource.name))).toEqual([
      "#Kustomization",
      "apps",
      "infra",
      "#HelmRelease",
      "podinfo",
      "#Pod",
      "web-1",
    ]);
    const first = rows[0];
    expect(first?.type === "group" && first.failing).toBe(1);
  });

  it("puts what needs attention first in the flat view", () => {
    expect(buildRows(items, false).map((r) => r.key)).toEqual([
      items[0]?.id,
      items[2]?.id,
      items[1]?.id,
      items[3]?.id,
    ]);
  });
});

describe("inventory-only rows", () => {
  const inv = resource("checkout-config", {
    kind: "ConfigMap",
    group: "",
    namespace: "apps",
    status: "unknown",
    inventoryOnly: true,
  });

  it("never count as needing attention", () => {
    expect(filterResources([...items, inv], { status: "attention" }).map((r) => r.name)).not.toContain(
      "checkout-config",
    );
    expect(statusCounts([...items, inv]).attention).toBe(2);
  });

  it("sort after rows that have a status in the flat view", () => {
    const rows = buildRows([inv, ...items], false);
    expect(rows[rows.length - 1]?.key).toBe(inv.id);
  });
});

describe("completed rows", () => {
  const done = resource("backup-1", {
    kind: "Job",
    group: "batch",
    namespace: "apps",
    status: "completed",
    completions: "1/1",
  });
  const pod = resource("backup-1-x7k2p", { kind: "Pod", group: "", namespace: "apps", status: "completed" });

  it("are healthy: they never need attention", () => {
    expect(needsAttention(done)).toBe(false);
    expect(needsAttention(pod)).toBe(false);
    expect(filterResources([...items, done, pod], { status: "attention" }).map((r) => r.name)).toEqual([
      "apps",
      "podinfo",
    ]);
  });

  it("have their own count and filter", () => {
    const counts = statusCounts([...items, done, pod]);
    expect(counts).toEqual({ ready: 2, attention: 2, failed: 1, reconciling: 0, suspended: 1, completed: 2 });
    expect(filterResources([...items, done, pod], { status: "completed" }).map((r) => r.name)).toEqual([
      "backup-1",
      "backup-1-x7k2p",
    ]);
  });

  it("sort after ready rows in the flat view", () => {
    const rows = buildRows([done, ...items], false);
    expect(rows[rows.length - 1]?.key).toBe(done.id);
  });
});

describe("namespace filter", () => {
  it("keeps one namespace", () => {
    expect(filterResources(items, { namespace: "apps" }).map((r) => r.name)).toEqual(["podinfo", "web-1"]);
  });
});

describe("summaryCounts", () => {
  const cluster = {
    name: "prod",
    counts: { ready: 4, failed: 1, reconciling: 2, completed: 3 },
    kinds: { HelmRelease: { ready: 1, failed: 1 }, Job: { completed: 3, reconciling: 2 }, Pod: { ready: 3 } },
  } as unknown as ClusterInfo;

  it("gives the total and chips from the cluster's counts before any row loads", () => {
    expect(summaryCounts(cluster, {})).toEqual({
      total: 10,
      counts: { ready: 4, attention: 3, failed: 1, reconciling: 2, suspended: 0, completed: 3 },
    });
  });

  it("narrows by a nav filter through the per-kind counts", () => {
    expect(summaryCounts(cluster, { kind: "Job" })?.total).toBe(5);
    expect(summaryCounts(cluster, { kind: "HelmRelease" })?.counts.failed).toBe(1);
  });

  it("is unknown with a namespace filter, pending counts, or no per-kind counts", () => {
    expect(summaryCounts(cluster, { namespace: "apps" })).toBeUndefined();
    expect(summaryCounts({ ...cluster, countsPending: true }, {})).toBeUndefined();
    expect(summaryCounts({ ...cluster, kinds: undefined }, { kind: "Job" })).toBeUndefined();
  });
});
