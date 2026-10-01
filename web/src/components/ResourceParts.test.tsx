import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { resource } from "../test/fixtures";
import { EventsList, nearLimit, ResourceFacts, ResourceHeader, YamlView } from "./ResourceParts";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="/">{children}</a>,
}));

afterEach(() => vi.unstubAllGlobals());

const wrap = (node: ReactNode) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
    {node}
  </QueryClientProvider>
);

function stubFetch(body: unknown) {
  const fn = vi
    .fn<typeof fetch>()
    .mockImplementation(
      async () => new Response(JSON.stringify(body), { headers: { "Content-Type": "application/json" } }),
    );
  vi.stubGlobal("fetch", fn);
  return fn;
}

const urls = (fn: ReturnType<typeof stubFetch>) => fn.mock.calls.map((c) => String(c[0]));

const secret = resource("checkout-db", {
  group: "",
  kind: "Secret",
  namespace: "apps",
  status: "unknown",
  inventoryOnly: true,
  project: "kubernetes",
});

describe("Secret objects", () => {
  it("replace the YAML with a calm note and never request it", () => {
    const fn = stubFetch({});
    render(wrap(<YamlView cluster="dev" r={secret} />));
    expect(screen.getByText("Secret contents are never shown")).toBeInTheDocument();
    expect(fn).not.toHaveBeenCalled();
  });

  it("still read their events, with the core group", async () => {
    const fn = stubFetch({ items: [] });
    render(wrap(<EventsList cluster="dev" r={secret} />));
    await waitFor(() => expect(screen.getByText("No recent events.")).toBeInTheDocument());
    expect(urls(fn)).toEqual(["/api/v1/clusters/dev/objects/Secret/apps/checkout-db/events?group=core"]);
  });
});

describe("inventory-only objects", () => {
  const agent = resource("datadog", {
    group: "datadoghq.com",
    kind: "DatadogAgent",
    namespace: "monitoring",
    status: "unknown",
    inventoryOnly: true,
    project: "datadoghq.com",
  });

  it("read YAML with their API group", async () => {
    const fn = stubFetch({ yaml: "apiVersion: datadoghq.com/v2alpha1\nkind: DatadogAgent" });
    render(wrap(<YamlView cluster="dev" r={agent} />));
    await waitFor(() => expect(screen.getByText("DatadogAgent")).toBeInTheDocument());
    expect(urls(fn)).toEqual([
      "/api/v1/clusters/dev/objects/DatadogAgent/monitoring/datadog/yaml?group=datadoghq.com",
    ]);
  });

  it("explain what the page shows", () => {
    render(wrap(<ResourceHeader r={agent} />));
    expect(screen.getByText("Managed by Flux · not watched by Eddy")).toBeInTheDocument();
    expect(screen.getByText(/YAML and Events are read from the cluster as you/)).toBeInTheDocument();
  });
});

describe("details", () => {
  const pool = resource("general", {
    group: "karpenter.sh",
    kind: "NodePool",
    namespace: "",
    project: "karpenter",
    details: [
      { label: "Nodes", value: "12" },
      { label: "CPU", value: "94 / 100" },
      { label: "Memory", value: "120Gi / 400Gi" },
      { label: "Ephemeral", value: "390Gi / 400Gi" },
      { label: "Node class", value: "default" },
    ],
  });

  it("show as facts in the grid, in order, after the standard ones", () => {
    render(wrap(<ResourceFacts cluster="prod-eu" r={pool} grid />));
    const labels = screen.getAllByRole("term").map((t) => t.textContent);
    expect(labels.slice(0, 5)).toEqual(["Nodes", "CPU", "Memory", "Ephemeral", "Node class"]);
    expect(screen.getByText("default")).toBeInTheDocument();
  });

  it("mark usage close to the limit and leave the rest calm", () => {
    render(wrap(<ResourceFacts cluster="prod-eu" r={pool} />));
    expect(screen.getByText("94 / 100")).toHaveClass("text-attn");
    expect(screen.getByText("120Gi / 400Gi")).not.toHaveClass("text-attn");
    expect(screen.getByText("390Gi / 400Gi")).toHaveClass("text-attn");
    expect(nearLimit("85 / 100")).toBe(true);
    expect(nearLimit("84 / 100")).toBe(false);
    expect(nearLimit("0 / 0")).toBe(false);
    expect(nearLimit("default")).toBe(false);
  });

  it("show the project next to a notable kind", () => {
    render(wrap(<ResourceHeader r={pool} />));
    expect(screen.getByText(/Karpenter/)).toBeInTheDocument();
  });
});
