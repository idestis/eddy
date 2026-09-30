import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ClusterInfo } from "../api/types";
import { PREFS_STORAGE_KEY, PrefsProvider, SAVE_DELAY_MS, useOrderedClusters, usePrefs } from "./prefs";

const api = vi.hoisted(() => ({ getPrefs: vi.fn(), putPrefs: vi.fn() }));
vi.mock("../api/endpoints", async (orig) => ({
  ...(await orig<typeof import("../api/endpoints")>()),
  ...api,
}));

const clusters: ClusterInfo[] = ["prod", "staging", "dev"].map((name, i) => ({
  name,
  displayName: name,
  protected: false,
  order: i,
  connected: true,
}));
vi.mock("../api/queries", async (orig) => {
  const real = await orig<typeof import("../api/queries")>();
  return { ...real, useClusters: () => ({ data: clusters }) };
});

let current: ReturnType<typeof usePrefs>;
let ordered: string[];
function Probe() {
  current = usePrefs();
  ordered = useOrderedClusters().map((c) => c.name);
  return null;
}

function mount(children: ReactNode = <Probe />) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PrefsProvider>{children}</PrefsProvider>
    </QueryClientProvider>,
  );
}

describe("PrefsProvider", () => {
  beforeEach(() => {
    localStorage.clear();
    api.getPrefs.mockReset();
    api.putPrefs.mockReset().mockResolvedValue({ data: {} });
  });
  afterEach(() => vi.useRealTimers());

  it("orders from the cached prefs before the hub answers", () => {
    localStorage.setItem(PREFS_STORAGE_KEY, JSON.stringify({ pins: ["dev"], visits: {} }));
    api.getPrefs.mockReturnValue(new Promise(() => {}));
    mount();
    expect(ordered).toEqual(["dev", "prod", "staging"]);
  });

  it("merges the hub's prefs, keeps unknown keys and debounces the PUT", async () => {
    api.getPrefs.mockResolvedValue({
      data: { theme: "dark", clusters: { pins: ["staging"], visits: { dev: [1] } } },
    });
    mount();
    await waitFor(() => expect(ordered[0]).toBe("staging"));
    vi.useFakeTimers();
    act(() => current.visit("prod"));
    act(() => current.visit("prod")); // same cluster within 30 minutes: counted once
    act(() => current.togglePin("dev"));
    expect(api.putPrefs).not.toHaveBeenCalled();
    expect(ordered.slice(0, 2)).toEqual(["staging", "dev"]);
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS);
    });
    expect(api.putPrefs).toHaveBeenCalledTimes(1);
    const body = api.putPrefs.mock.calls[0]?.[0] as {
      theme: string;
      clusters: { pins: string[]; visits: Record<string, number[]> };
    };
    expect(body.theme).toBe("dark");
    expect(body.clusters.pins).toEqual(["staging", "dev"]);
    expect(body.clusters.visits.prod).toHaveLength(1);
    expect(body.clusters.visits.dev).toEqual([1]);
    expect(JSON.parse(localStorage.getItem(PREFS_STORAGE_KEY) ?? "{}").pins).toEqual(["staging", "dev"]);
  });

  it("never writes before the hub's copy has been read", async () => {
    api.getPrefs.mockReturnValue(new Promise(() => {}));
    mount();
    vi.useFakeTimers();
    act(() => current.togglePin("dev"));
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS * 2);
    });
    expect(api.putPrefs).not.toHaveBeenCalled();
    expect(ordered[0]).toBe("dev");
  });

  it("still works when localStorage is blocked", async () => {
    api.getPrefs.mockResolvedValue({ data: {} });
    const get = vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    const set = vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("blocked");
    });
    mount();
    act(() => current.togglePin("staging"));
    expect(ordered[0]).toBe("staging");
    get.mockRestore();
    set.mockRestore();
  });

  it("falls back to the CRD order without a provider", () => {
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <Probe />
        <span>ok</span>
      </QueryClientProvider>,
    );
    expect(screen.getByText("ok")).toBeInTheDocument();
    expect(ordered).toEqual(["prod", "staging", "dev"]);
  });
});
