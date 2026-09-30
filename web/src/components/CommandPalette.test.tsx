import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, it, vi } from "vitest";
import type { FleetResource } from "../api/queries";
import type { ClusterInfo } from "../api/types";
import { AppStateProvider } from "../lib/appState";
import { resource } from "../test/fixtures";
import { CommandPalette } from "./CommandPalette";

const clusters: ClusterInfo[] = ["staging", "prod-eu"].map((name, i) => ({
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

vi.mock("@tanstack/react-router", () => ({ useNavigate: () => vi.fn() }));
vi.mock("../api/queries", () => ({
  useMe: () => ({ data: { features: { ai: false, logs: true } } }),
  useClusters: () => ({ data: clusters }),
  useCluster: (name?: string) => clusters.find((c) => c.name === name),
  useFleetResources: () => ({ items: fleet, loading: false }),
}));
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
