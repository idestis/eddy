import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ClusterInfo, KindsResponse } from "../api/types";
import { NAV_TREE, navCounts, totalsOfCounts } from "../lib/kinds";
import { resource } from "../test/fixtures";
import { useNavCounts } from "./Sidebar";

const api = vi.hoisted(() => ({ getResources: vi.fn(), getKinds: vi.fn() }));
vi.mock("../api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/endpoints")>()),
  ...api,
}));

const kinds: KindsResponse = {
  items: [
    {
      group: "helm.toolkit.fluxcd.io",
      kind: "HelmRelease",
      namespaced: true,
      project: "flux",
      watched: true,
      count: 1,
    },
    { group: "", kind: "ConfigMap", namespaced: true, project: "kubernetes", watched: false, count: 4 },
  ],
  projects: [],
  presets: [],
};

const prod = (patch: Partial<ClusterInfo> = {}): ClusterInfo => ({
  name: "prod",
  displayName: "prod",
  protected: false,
  order: 0,
  connected: true,
  counts: { ready: 5, failed: 1, reconciling: 1, completed: 2 },
  kinds: {
    HelmRelease: { ready: 2, failed: 1 },
    Kustomization: { ready: 3, reconciling: 1 },
    Job: { completed: 2 },
  },
  ...patch,
});

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  api.getResources.mockReset().mockResolvedValue({
    items: [resource("apps", { status: "failed" }), resource("infra")],
    resourceVersion: "1",
  });
  api.getKinds.mockReset().mockResolvedValue(kinds);
});

describe("sidebar counts", () => {
  it("come from ClusterInfo.kinds without loading the cluster's resources", async () => {
    const { result } = renderHook(() => useNavCounts(prod(), NAV_TREE), { wrapper });
    expect(result.current).toMatchObject({ all: 9, attention: 2, known: true });
    expect(result.current.counts.get("HelmRelease")).toBe(3);
    expect(result.current.counts.get("Kustomization")).toBe(4);
    await waitFor(() => expect(api.getKinds).toHaveBeenCalled());
    expect(api.getResources).not.toHaveBeenCalled();
  });

  it("stay unknown (no zeros) while the hub is still counting", () => {
    const { result } = renderHook(() => useNavCounts(prod({ countsPending: true }), NAV_TREE), { wrapper });
    expect(result.current.known).toBe(false);
  });

  it("fall back to the snapshot on a hub without ClusterInfo.kinds", async () => {
    const { result } = renderHook(() => useNavCounts(prod({ kinds: undefined }), NAV_TREE), { wrapper });
    await waitFor(() => expect(result.current.known).toBe(true));
    expect(api.getResources).toHaveBeenCalledTimes(1);
    expect(result.current).toMatchObject({ all: 2, attention: 1 });
  });

  it("read nothing for a disconnected cluster without a stale view", () => {
    renderHook(() => useNavCounts(prod({ connected: false, kinds: undefined }), NAV_TREE), { wrapper });
    expect(api.getResources).not.toHaveBeenCalled();
    expect(api.getKinds).not.toHaveBeenCalled();
  });
});

describe("totalsOfCounts", () => {
  it("prefers the live per-Kind counts and keeps inventory-only kinds from the kinds endpoint", () => {
    const totals = totalsOfCounts({ HelmRelease: { ready: 2, failed: 1 } }, kinds);
    expect(totals?.byKind.get("HelmRelease")).toBe(3);
    expect(totals?.byKind.get("ConfigMap")).toBe(4);
    expect(totals?.byRef.get("helm.toolkit.fluxcd.io/HelmRelease")).toBe(3);
    const counts = navCounts(NAV_TREE, totals ?? { byKind: new Map(), byRef: new Map() });
    expect(counts.get("HelmRelease")).toBe(3);
  });

  it("is undefined with neither source", () => {
    expect(totalsOfCounts(undefined, undefined)).toBeUndefined();
  });
});
