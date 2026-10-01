import { describe, expect, it, vi } from "vitest";
import type { IndexPage, ResourceSnapshot } from "../api/types";
import { fromIndexRow, isIndexPage, keepPages, pagesFor, WINDOW_THRESHOLD } from "./indexRows";
import { modeOf, probeList } from "./useClusterList";

const api = vi.hoisted(() => ({ getIndexPage: vi.fn() }));
vi.mock("../api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/endpoints")>()),
  ...api,
}));

describe("fromIndexRow", () => {
  it("maps the hub's index row onto a list Resource", () => {
    const owner = {
      group: "kustomize.toolkit.fluxcd.io",
      kind: "Kustomization",
      namespace: "flux-system",
      name: "apps",
    };
    const r = fromIndexRow({
      id: "helm.toolkit.fluxcd.io/HelmRelease/apps/podinfo",
      group: "helm.toolkit.fluxcd.io",
      kind: "HelmRelease",
      namespace: "apps",
      name: "podinfo",
      status: "failed",
      message: "install retries exhausted",
      revision: "6.7.0",
      lastChanged: "2026-09-30T10:00:00Z",
      owner,
    });
    expect(r).toMatchObject({
      group: "helm.toolkit.fluxcd.io",
      id: "helm.toolkit.fluxcd.io/HelmRelease/apps/podinfo",
      name: "podinfo",
      status: "failed",
      revision: "6.7.0",
      owner,
    });
    expect(r.inventoryOnly).toBeUndefined();
    expect(
      fromIndexRow({
        id: "/ConfigMap//x",
        group: "",
        kind: "ConfigMap",
        namespace: "",
        name: "x",
        status: "unknown",
        inventoryOnly: true,
      }),
    ).toMatchObject({ id: "/ConfigMap//x", inventoryOnly: true, namespace: "" });
  });
});

describe("windowing", () => {
  it("maps a visible range to 500-row pages", () => {
    expect(pagesFor(0, 40, 30_000)).toEqual([0]);
    expect(pagesFor(480, 520, 30_000)).toEqual([0, 1]);
    expect(pagesFor(29_990, 30_100, 30_000)).toEqual([59]);
    expect(pagesFor(0, 10, 0)).toEqual([]);
  });

  it("keeps the wanted pages first and at most 8", () => {
    expect(keepPages([0, 1, 2, 3, 4, 5, 6, 7], [9, 10])).toEqual([9, 10, 0, 1, 2, 3, 4, 5]);
    expect(keepPages([3, 4], [4])).toEqual([4, 3]);
  });
});

describe("list mode", () => {
  const page = (total: number): IndexPage => ({ items: [], total });

  it("picks legacy, fill or windowed from the probe", () => {
    expect(modeOf(undefined)).toBe("probing");
    expect(modeOf({ page: null })).toBe("legacy");
    expect(modeOf({ page: page(WINDOW_THRESHOLD) })).toBe("fill");
    expect(modeOf({ page: page(WINDOW_THRESHOLD + 1) })).toBe("windowed");
  });

  it("feature-detects paging by `total` and reuses an old hub's full answer as the snapshot", async () => {
    const snapshot: ResourceSnapshot = { items: [], resourceVersion: "7" };
    api.getIndexPage.mockResolvedValueOnce(snapshot);
    const seed = vi.fn();
    expect(await probeList("prod", seed)).toEqual({ page: null });
    expect(seed).toHaveBeenCalledWith(snapshot);
    expect(api.getIndexPage).toHaveBeenCalledWith("prod", { limit: 200, sort: "kind" }, undefined);

    api.getIndexPage.mockResolvedValueOnce(page(12));
    seed.mockClear();
    expect((await probeList("prod", seed)).page?.total).toBe(12);
    expect(seed).not.toHaveBeenCalled();
    expect(isIndexPage(snapshot)).toBe(false);
  });
});
