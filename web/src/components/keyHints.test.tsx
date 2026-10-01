import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { BINDINGS, hintCollisions, keyOwners } from "../lib/keys";
import { KeyHint } from "./Status";
import { ResourceThreads } from "./Threads";
import { ToastProvider } from "./Toasts";

vi.mock("@tanstack/react-router", () => ({ Link: () => null }));
vi.mock("../api/endpoints", async (orig) => ({
  ...(await orig<typeof import("../api/endpoints")>()),
  listThreads: vi.fn().mockResolvedValue({ items: [] }),
}));

const target = {
  cluster: "prod-eu",
  group: "apps",
  kind: "Deployment",
  namespace: "apps",
  name: "podinfo",
};

describe("key hints", () => {
  it("no key is bound twice, counting optional Shift", () => {
    for (const [key, ids] of keyOwners()) expect(ids, `"${key}"`).toHaveLength(1);
  });

  it("t is the Threads tab and nothing else; c composes", () => {
    expect(BINDINGS.tabThreads.keys).toEqual(["t"]);
    expect(BINDINGS.compose.keys).toEqual(["c"]);
    expect(keyOwners().get("t")).toEqual(["tabThreads"]);
  });

  it("the Threads tab hints t once, and the composer is inline when there are no threads", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <div role="tablist">
            <button type="button" role="tab">
              Threads <KeyHint id="tabThreads" />
            </button>
          </div>
          <ResourceThreads target={target} compose={false} onCompose={() => {}} />
        </ToastProvider>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(screen.getByText(/No threads yet/)).toBeInTheDocument());
    // The empty state is the composer itself: no separate "New thread" step, and no focus grab.
    expect(screen.getByRole("textbox", { name: "Title" })).toBeInTheDocument();
    expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Title" }));
    expect(hintCollisions(container)).toEqual([]);
    expect(container.querySelectorAll('[data-key-id="tabThreads"]')).toHaveLength(1);
  });

  it("reports one key hinted for two actions on one screen", () => {
    const root = document.createElement("div");
    root.innerHTML =
      '<kbd data-key-id="tabThreads">t</kbd><kbd data-key-id="compose">t</kbd><kbd data-key-id="tabThreads">t</kbd>';
    expect(hintCollisions(root)).toEqual(["t: tabThreads, compose"]);
  });
});
