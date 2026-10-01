import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import type { SearchQuery } from "../api/endpoints";
import { type FleetResource, keys } from "../api/queries";
import type { ClusterInfo, ResourceSnapshot, SearchResponse } from "../api/types";
import { AppStateProvider } from "../lib/appState";
import { rank } from "../lib/fuzzy";
import { resource } from "../test/fixtures";
import { CommandPalette } from "./CommandPalette";

const clusters: ClusterInfo[] = ["staging", "prod-eu", "dev"].map((name, i) => ({
  name,
  displayName: name,
  protected: false,
  order: i,
  connected: true,
}));

const fleet: FleetResource[] = [
  { cluster: "staging", resource: resource("apps", { status: "failed" }) },
  { cluster: "staging", resource: resource("flux-system") },
  { cluster: "prod-eu", resource: resource("flux-system") },
  { cluster: "prod-eu", resource: resource("payments") },
];

const navigate = vi.hoisted(() => vi.fn());
const api = vi.hoisted(() => ({ search: vi.fn(), getAttention: vi.fn(), getResources: vi.fn() }));
const fleetFallback = vi.hoisted(() => vi.fn());
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/endpoints")>()),
  ...api,
}));
vi.mock("../api/queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../api/queries")>()),
  useMe: () => ({ data: { features: { ai: false, logs: true } } }),
  useClusters: () => ({ data: clusters }),
  useCluster: (name?: string) => clusters.find((c) => c.name === name),
  useFleetResources: (enabled = true) => {
    fleetFallback(enabled);
    return { items: enabled ? fleet : [], loading: false };
  },
}));
vi.mock("../lib/useNav", async () => {
  const { NAV_TREE } = await import("../lib/kinds");
  return { useNav: () => NAV_TREE };
});
vi.mock("../lib/useResourceActions", () => ({
  useResourceActions: () => ({
    readOnly: false,
    busy: new Set(),
    reconcile: vi.fn(),
    toggleSuspend: vi.fn(),
  }),
}));

beforeAll(() => {
  Element.prototype.scrollIntoView ??= () => {};
});

/** GET /search answered from `fleet`, ranked like the hub (same fuzzy.ts order). */
function searchFromFleet(q: SearchQuery): SearchResponse {
  return {
    items: rank(q.q, fleet, (fr) => ({
      primary: fr.resource.name,
      secondary: [fr.resource.namespace, fr.resource.kind, "KS", fr.cluster],
    })).map(({ item, match }) => ({ cluster: item.cluster, resource: item.resource, match })),
    partial: [],
  };
}

beforeEach(() => {
  api.search.mockReset().mockImplementation(async (q: SearchQuery) => searchFromFleet(q));
  api.getAttention.mockReset().mockResolvedValue({ items: [], total: 0, findings: [] });
  api.getResources.mockReset().mockResolvedValue({ items: [], resourceVersion: "1" });
  fleetFallback.mockReset();
});

function open(routeCluster?: string, query = "") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  // The current cluster's list is loaded: the "This cluster" scope ranks it locally.
  if (routeCluster)
    qc.setQueryData<ResourceSnapshot>(keys.resources(routeCluster), {
      items: fleet.filter((f) => f.cluster === routeCluster).map((f) => f.resource),
      resourceVersion: "1",
    });
  render(
    <QueryClientProvider client={qc}>
      <AppStateProvider>
        <CommandPalette initialQuery={query} routeCluster={routeCluster} />
      </AppStateProvider>
    </QueryClientProvider>,
  );
  return qc;
}

const resourceRows = () => {
  const group = screen.queryByText(/^Resources/);
  const list = group?.closest('[role="group"]');
  return list
    ? within(list as HTMLElement)
        .queryAllByRole("option")
        .map((o) => o.textContent ?? "")
    : [];
};

describe("CommandPalette scope", () => {
  it("searches only the current cluster inside a cluster", () => {
    open("staging", "flux-sy");
    expect(screen.getByText("Resources in staging")).toBeInTheDocument();
    const rows = resourceRows();
    expect(rows.every((r) => r.includes("staging"))).toBe(true);
    expect(rows[0]).toContain("flux-system");
    expect(screen.getByRole("button", { name: /staging/, pressed: true })).toBeInTheDocument();
  });

  it("widens to every cluster with Tab, and back", async () => {
    const user = userEvent.setup();
    open("staging", "flux-sy");
    await user.keyboard("{Tab}");
    expect(screen.getByRole("button", { name: /All clusters/, pressed: true })).toBeInTheDocument();
    await waitFor(() => expect(resourceRows().some((r) => r.includes("prod-eu"))).toBe(true));
    expect(screen.getByText("Resources")).toBeInTheDocument();
    await user.keyboard("{Tab}");
    expect(screen.getByText("Resources in staging")).toBeInTheDocument();
  });

  it("widens with the scope chip", async () => {
    const user = userEvent.setup();
    open("staging", "payments");
    expect(resourceRows()).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: /All clusters/ }));
    await waitFor(() => expect(resourceRows()[0]).toContain("payments"));
  });

  it("searches every cluster on the fleet page and shows no scope toggle", async () => {
    open(undefined, "payments");
    expect(screen.queryByRole("button", { name: /All clusters/ })).toBeNull();
    await waitFor(() => expect(resourceRows()[0]).toContain("payments"));
  });

  it("ranks this cluster locally without asking the hub", () => {
    open("staging", "apps");
    expect(resourceRows()[0]).toContain("apps");
    expect(api.search).not.toHaveBeenCalled();
    expect(fleetFallback).not.toHaveBeenCalledWith(true);
  });

  it("highlights the matched characters", () => {
    open("staging", "flux-sy");
    const marks = document.querySelectorAll("mark");
    expect([...marks].some((m) => m.textContent === "flux-sy")).toBe(true);
  });
});

describe("CommandPalette cluster digits", () => {
  const input = () => screen.getByPlaceholderText(/^Search/);
  beforeEach(() => navigate.mockClear());

  const clusterRows = () => {
    const group = screen.getByText("Clusters").closest('[role="group"]') as HTMLElement;
    return within(group).getAllByRole("option");
  };

  it("switches immediately on a digit while the input is empty", async () => {
    const user = userEvent.setup();
    open("staging");
    await user.keyboard("2");
    expect(navigate).toHaveBeenCalledWith({ to: "/c/$cluster", params: { cluster: "prod-eu" } });
    expect(input()).toHaveValue("");
  });

  it("types digits normally once something has been typed", async () => {
    const user = userEvent.setup();
    open("staging");
    await user.keyboard("a2");
    expect(navigate).not.toHaveBeenCalled();
    expect(input()).toHaveValue("a2");
    // The digit hints fade out and the footer stops offering 1–9.
    expect(screen.queryByText("switch")).toBeNull();
  });

  it("ignores a digit with no cluster and types it", async () => {
    const user = userEvent.setup();
    open("staging");
    await user.keyboard("9");
    expect(navigate).not.toHaveBeenCalled();
    expect(input()).toHaveValue("9");
  });

  it("hints 1–9 in the footer while empty and fades the row digits when typing", async () => {
    const user = userEvent.setup();
    open("staging");
    expect(screen.getByText("switch")).toBeInTheDocument();
    const digit = within(clusterRows()[0] as HTMLElement).getByText("2");
    expect(digit).toHaveClass("opacity-100");
    await user.type(input(), "x");
    expect(input()).toHaveValue("x");
    expect(document.querySelector("kbd.opacity-0")).not.toBeNull();
  });

  it("lists the current cluster last with a badge, dimmed, and no 'Switch to'", () => {
    open("staging");
    const rows = clusterRows();
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringContaining("Switch to prod-eu"),
      expect.stringContaining("Switch to dev"),
      expect.stringContaining("staging"),
    ]);
    const current = rows[2] as HTMLElement;
    expect(current).toHaveTextContent("current");
    expect(current.textContent).not.toContain("Switch to");
    expect(current).toHaveClass("opacity-60");
    // Digits keep following the displayed order: staging is 1, even though it is listed last.
    expect(within(rows[0] as HTMLElement).getByText("2")).toBeInTheDocument();
    expect(within(rows[1] as HTMLElement).getByText("3")).toBeInTheDocument();
  });

  it("does nothing for the current cluster's digit except close", async () => {
    const user = userEvent.setup();
    open("staging");
    await user.keyboard("1");
    expect(navigate).not.toHaveBeenCalled();
  });
});

describe("CommandPalette fleet search", () => {
  const input = () => screen.getByRole("combobox", { name: "Search" });

  it("debounces typing into one request for the final query", async () => {
    const user = userEvent.setup();
    open(undefined);
    await user.type(input(), "paym");
    await waitFor(() => expect(resourceRows()[0]).toContain("payments"));
    expect(api.search).toHaveBeenCalledTimes(1);
    expect(api.search.mock.calls[0]?.[0]).toMatchObject({ q: "paym", scope: "fleet" });
  });

  it("shows a spinner while the hub answers and keeps the keyboard flow", async () => {
    let answer: (r: SearchResponse) => void = () => {};
    api.search.mockImplementation(() => new Promise<SearchResponse>((r) => (answer = r)));
    const user = userEvent.setup();
    open(undefined);
    await user.type(input(), "payments");
    await waitFor(() =>
      expect(api.search).toHaveBeenLastCalledWith(
        expect.objectContaining({ q: "payments" }),
        expect.anything(),
      ),
    );
    expect(input()).toHaveAttribute("aria-busy", "true");
    expect(screen.getByText("searching…")).toBeInTheDocument();
    answer(searchFromFleet({ q: "payments", scope: "fleet" }));
    await waitFor(() => expect(resourceRows()[0]).toContain("payments"));
    expect(input()).toHaveAttribute("aria-busy", "false");
    // The first result is active and Enter opens it.
    await user.keyboard("{Enter}");
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ params: expect.objectContaining({ cluster: "prod-eu" }) }),
    );
  });

  it("aborts the request in flight when the query changes", async () => {
    const signals: AbortSignal[] = [];
    api.search.mockImplementation(
      (_q: SearchQuery, signal: AbortSignal) =>
        new Promise<SearchResponse>(() => {
          signals.push(signal);
        }),
    );
    const user = userEvent.setup();
    open(undefined);
    await user.type(input(), "flux");
    await waitFor(() => expect(signals).toHaveLength(1));
    await user.type(input(), "-sys");
    await waitFor(() => expect(signals).toHaveLength(2));
    expect(signals[0]?.aborted).toBe(true);
    expect(signals[1]?.aborted).toBe(false);
  });

  it("falls back to ranking every snapshot on a hub without /search", async () => {
    api.search.mockRejectedValue(new ApiError(404, "not_found", "No such route."));
    const user = userEvent.setup();
    open(undefined);
    await user.type(input(), "payments");
    await waitFor(() => expect(fleetFallback).toHaveBeenCalledWith(true));
    await waitFor(() => expect(resourceRows()[0]).toContain("payments"));
  });

  it("moves with the arrow keys and wraps", async () => {
    const user = userEvent.setup();
    open("staging");
    const options = () => screen.getAllByRole("option");
    expect(options()[0]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowUp}");
    expect(options()[options().length - 1]).toHaveAttribute("aria-selected", "true");
    await user.keyboard("{ArrowDown}");
    expect(options()[0]).toHaveAttribute("aria-selected", "true");
    expect(input()).toHaveAttribute("aria-activedescendant", options()[0]?.id);
  });
});
