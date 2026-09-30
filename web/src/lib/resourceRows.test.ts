import { describe, expect, it } from "vitest";
import { resource } from "../test/fixtures";
import { buildRows, filterResources } from "./resourceRows";

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
