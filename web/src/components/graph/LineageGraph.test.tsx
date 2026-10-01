import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "../../api/client";
import type { GraphQuery } from "../../api/endpoints";
import type { ClusterInfo, GraphNode, GraphResponse, Ref, Resource } from "../../api/types";
import { groupId, nodeOf } from "../../lib/graph";
import { kindInfo } from "../../lib/kinds";
import { resetGraphEndpointCache } from "../../lib/useGraph";
import { resetGraphExpansion } from "../../lib/useGraphExpansion";
import type { useResourceActions } from "../../lib/useResourceActions";
import { LineageGraph } from ".";

const getGraph = vi.hoisted(() => vi.fn());
vi.mock("../../api/endpoints", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/endpoints")>()),
  getGraph,
}));

const cluster: ClusterInfo = {
  name: "dev",
  displayName: "dev",
  protected: false,
  order: 1,
  connected: true,
};

const res = (kind: string, ns: string, name: string, extra: Partial<Resource> = {}): Resource => {
  const group = kindInfo(kind).group;
  return {
    group,
    kind,
    namespace: ns,
    name,
    id: `${group}/${kind}/${ns}/${name}`,
    version: "v1",
    status: "ready",
    resourceVersion: "1",
    ...extra,
  };
};
const ref = (r: Resource): Ref => ({ group: r.group, kind: r.kind, namespace: r.namespace, name: r.name });

// The owner's cluster: the bootstrap Kustomization applies 26 Kustomizations (a dependsOn
// chain) and 25 Deployments, which the hub collapses into one group.
const root = res("Kustomization", "flux-system", "flux-system");
const kss = Array.from({ length: 26 }, (_, i) =>
  res("Kustomization", "flux-system", `ks${i}`, { owner: ref(root) }),
);
kss.forEach((k, i) => {
  if (i > 0) k.dependsOn = [ref(kss[i - 1] as Resource)];
});
const deploys = Array.from({ length: 25 }, (_, i) =>
  res("Deployment", "apps", `d${String(i).padStart(2, "0")}`, { owner: ref(root) }),
);
const items = [root, ...kss, ...deploys];
const gid = groupId(root.id, "Deployment");

/** What the hub answers: the Deployments collapsed unless expand names their group. */
function hubGraph(q: GraphQuery, honourExpand = true): GraphResponse {
  const expanded = honourExpand && (q.expand ?? "").split(",").includes(gid);
  const nodes: GraphNode[] = [root, ...kss].map(nodeOf);
  const edges: GraphResponse["edges"] = kss.map((k) => ({ from: root.id, to: k.id, type: "owns" as const }));
  kss.forEach((k, i) => {
    if (i > 0) edges.push({ from: k.id, to: (kss[i - 1] as Resource).id, type: "dependsOn" });
  });
  if (expanded) {
    for (const d of deploys) {
      nodes.push(nodeOf(d));
      edges.push({ from: root.id, to: d.id, type: "owns" });
    }
  } else {
    nodes.push({
      id: gid,
      kind: "Deployment",
      group: "apps",
      namespace: "apps",
      owner: root.id,
      count: 25,
      statuses: { ready: 25 },
    });
    edges.push({ from: root.id, to: gid, type: "owns" });
  }
  return { nodes, edges, truncated: false };
}

const actions = {
  reconcile: vi.fn(),
} as unknown as ReturnType<typeof useResourceActions>;

function renderGraph() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <LineageGraph
        cluster={cluster}
        root={root}
        items={items}
        onSelect={() => {}}
        onOpen={() => {}}
        actions={actions}
        header={(_, extra) => <div>{extra}</div>}
      />
    </QueryClientProvider>,
  );
}

const groupNode = () => screen.findByRole("button", { name: /^25 Deployments/ });
const nodeEl = (r: Resource) => screen.getByRole("button", { name: new RegExp(`^${r.kind} ${r.name},`) });
const transform = (el: HTMLElement) => el.style.transform;

function deferred<T>() {
  let resolve: (v: T) => void = () => {};
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

beforeEach(() => {
  getGraph.mockReset();
  resetGraphEndpointCache();
  resetGraphExpansion();
});
afterEach(() => vi.useRealTimers());

describe("LineageGraph group expansion", () => {
  it("never groups Kustomizations; expanding a hub group refetches with expand= and keeps positions", async () => {
    const pending = deferred<GraphResponse>();
    getGraph.mockImplementation((_c: string, q: GraphQuery) =>
      q.expand ? pending.promise : Promise.resolve(hubGraph(q)),
    );
    renderGraph();
    const group = await groupNode();
    expect(group).toHaveAccessibleName(/press Enter or double-click to expand/);
    expect(group).toHaveTextContent("Expand");
    for (const k of kss) expect(nodeEl(k)).toBeInTheDocument();
    const before = new Map(kss.map((k) => [k.id, transform(nodeEl(k))]));
    const groupAt = transform(group);

    fireEvent.doubleClick(group);
    await waitFor(() =>
      expect(getGraph).toHaveBeenLastCalledWith(
        "dev",
        expect.objectContaining({ expand: gid }),
        expect.anything(),
      ),
    );
    // While the hub answers, the group shows a spinner.
    const busy = await groupNode();
    expect(busy).toHaveAttribute("aria-busy", "true");
    expect(busy).toHaveTextContent("expanding…");

    await act(async () => pending.resolve(hubGraph({ kinds: "all", expand: gid })));
    await waitFor(() => expect(screen.queryByRole("button", { name: /^25 Deployments/ })).toBeNull());
    for (const d of deploys) expect(nodeEl(d)).toBeInTheDocument();
    // Old nodes did not move, except those below the group in its column, which make
    // room; the first member sits where the group was.
    const xOf = (t: string | undefined) => t?.match(/translate\((-?\d+)px/)?.[1];
    const yOf = (t: string | undefined) => Number(t?.match(/,(-?\d+)px\)/)?.[1]);
    for (const k of kss) {
      const was = before.get(k.id);
      const now = transform(nodeEl(k));
      if (xOf(was) !== xOf(groupAt) || yOf(was) < yOf(groupAt)) expect(now).toBe(was);
      else expect(yOf(now)).toBeGreaterThan(yOf(was));
    }
    expect(transform(nodeEl(deploys[0] as Resource))).toBe(groupAt);

    // Collapse groups: back to the hub's grouped graph, without expand.
    fireEvent.click(screen.getByRole("button", { name: "Collapse groups" }));
    expect(await groupNode()).toBeInTheDocument();
  });

  it("remembers the expanded groups for the session", async () => {
    getGraph.mockImplementation((_c: string, q: GraphQuery) => Promise.resolve(hubGraph(q)));
    const first = renderGraph();
    // The context menu's Expand.
    fireEvent.contextMenu(await groupNode());
    fireEvent.click(screen.getByRole("menuitem", { name: "Expand" }));
    await waitFor(() => expect(nodeEl(deploys[3] as Resource)).toBeInTheDocument());
    first.unmount();
    renderGraph();
    await waitFor(() => expect(nodeEl(deploys[3] as Resource)).toBeInTheDocument());
    expect(getGraph).toHaveBeenLastCalledWith(
      "dev",
      expect.objectContaining({ expand: gid }),
      expect.anything(),
    );
  });

  it("expands from the resources list when the hub has no graph endpoint", async () => {
    getGraph.mockRejectedValue(new ApiError(404, "not_found", "not found"));
    renderGraph();
    const group = await groupNode();
    for (const k of kss) expect(nodeEl(k)).toBeInTheDocument();
    fireEvent.doubleClick(group);
    await waitFor(() => expect(nodeEl(deploys[0] as Resource)).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /^25 Deployments/ })).toBeNull();
  });

  it("expands from the resources list when the hub ignores expand", async () => {
    getGraph.mockImplementation((_c: string, q: GraphQuery) => Promise.resolve(hubGraph(q, false)));
    renderGraph();
    fireEvent.doubleClick(await groupNode());
    await waitFor(() => expect(nodeEl(deploys[24] as Resource)).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /^25 Deployments/ })).toBeNull();
  });
});
