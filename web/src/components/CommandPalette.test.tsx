import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { FleetResource } from "../api/queries";
import type { ClusterInfo } from "../api/types";
import { AppStateProvider } from "../lib/appState";
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
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("../api/queries", () => ({
  useMe: () => ({ data: { features: { ai: false, logs: true } } }),
  useClusters: () => ({ data: clusters }),
  useCluster: (name?: string) => clusters.find((c) => c.name === name),
  useFleetResources: () => ({ items: fleet, loading: false }),
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
  // cmdk measures and scrolls its list; jsdom has neither API.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  Element.prototype.scrollIntoView ??= () => {};
});

function open(routeCluster?: string, query = "") {
  render(
    <AppStateProvider>
      <CommandPalette initialQuery={query} routeCluster={routeCluster} />
    </AppStateProvider>,
  );
}

const resourceRows = () => {
  const group = screen.queryByText(/^Resources/);
  const list = group?.closest("[cmdk-group]");
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
    expect(screen.getByText("Resources")).toBeInTheDocument();
    expect(resourceRows().some((r) => r.includes("prod-eu"))).toBe(true);
    await user.keyboard("{Tab}");
    expect(screen.getByText("Resources in staging")).toBeInTheDocument();
  });

  it("widens with the scope chip", async () => {
    const user = userEvent.setup();
    open("staging", "payments");
    expect(resourceRows()).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: /All clusters/ }));
    expect(resourceRows()[0]).toContain("payments");
  });

  it("searches every cluster on the fleet page and shows no scope toggle", () => {
    open(undefined, "payments");
    expect(screen.queryByRole("button", { name: /All clusters/ })).toBeNull();
    expect(resourceRows()[0]).toContain("payments");
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
    const group = screen.getByText("Clusters").closest("[cmdk-group]") as HTMLElement;
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
