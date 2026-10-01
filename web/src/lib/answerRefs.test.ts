import { describe, expect, it } from "vitest";
import type { ResourceRef } from "../api/types";
import { matchRef, refsOf, refTitle } from "./answerRefs";

const ref = (cluster: string, group: string, kind: string, namespace: string, name: string): ResourceRef => ({
  cluster,
  group,
  kind,
  namespace,
  name,
});

const ks = ref("prod", "kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps");
const hr = ref("prod", "helm.toolkit.fluxcd.io", "HelmRelease", "apps", "podinfo");
const dep = ref("prod", "apps", "Deployment", "apps", "podinfo");
const pool = ref("prod", "karpenter.sh", "NodePool", "", "apps-amd64");
const nsApps = ref("prod", "", "Namespace", "", "apps");
const nsServices = ref("prod", "", "Namespace", "", "services");
const devHr = ref("dev", "helm.toolkit.fluxcd.io", "HelmRelease", "web", "frontend");
const refs = [ks, hr, dep, pool, nsApps, nsServices, devHr];

describe("matchRef", () => {
  it.each([
    ["HelmRelease/apps/podinfo", hr],
    ["helmrelease/apps/podinfo", hr],
    ["HR/apps/podinfo", hr],
    ["hr/apps/podinfo", hr],
    ["Deployment/apps/podinfo", dep],
    ["DEP/apps/podinfo", dep],
    ["KS/flux-system/apps", ks],
    ["NodePool/apps-amd64", pool],
    ["NPL/apps-amd64", pool],
    ["apps-amd64", pool],
    ["services", nsServices],
    ["Namespace/apps", nsApps],
    ["HelmRelease/podinfo", hr], // unique namespace
    ["flux-system/apps", ks],
    ["Kustomization/apps", ks], // its only namespace
    ["frontend", devHr],
    [" HelmRelease/apps/podinfo ", hr],
  ])("%s links", (code, want) => {
    expect(matchRef(code, refs)).toEqual(want);
  });

  it.each([
    ["podinfo"], // a HelmRelease and a Deployment
    ["apps/podinfo"], // same
    ["apps"], // a Namespace and a Kustomization
    ["HelmRelease/apps/ghost"], // never returned by a tool
    ["Deployment/apps/ghost"],
    ["ghost"],
    ["Kustomization/web/apps"], // wrong namespace
    ["prod/kustomize.toolkit.fluxcd.io/Kustomization/flux-system/apps"],
    ["/c/prod/r/HelmRelease/apps/podinfo"],
    ["https://eddy.example/c/prod/r/HelmRelease/apps/podinfo"],
    ["flux reconcile hr podinfo"],
    ["HelmRelease//podinfo"],
    [""],
  ])("%s stays plain", (code) => {
    expect(matchRef(code, refs)).toBeNull();
  });

  it("links nothing without refs", () => {
    expect(matchRef("HelmRelease/apps/podinfo", [])).toBeNull();
  });
});

describe("refsOf", () => {
  it("keeps only well-formed refs", () => {
    expect(
      refsOf({
        refs: [hr, { cluster: "prod", kind: "Pod" }, { cluster: 1, kind: "Pod", name: "x" }, null, "x", pool],
      }),
    ).toEqual([hr, pool]);
  });

  it.each([[undefined], [null], [{}], [{ refs: "x" }], ["meta"]])("%s has no refs", (meta) => {
    expect(refsOf(meta)).toEqual([]);
  });
});

describe("refTitle", () => {
  it("names the kind and object", () => {
    expect(refTitle(hr)).toBe("Open HelmRelease apps/podinfo");
    expect(refTitle(pool)).toBe("Open NodePool apps-amd64");
    expect(refTitle(devHr, true)).toBe("Open HelmRelease web/frontend in dev");
  });
});
