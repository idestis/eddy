import { describe, expect, it } from "vitest";
import type { GraphResponse, Ref, Resource, Status } from "../api/types";
import {
  blockage,
  buildGraph,
  COLLAPSE_OVER,
  collapseOwned,
  focusGraph,
  groupId,
  indexGraph,
  isBlocked,
  lineage,
  nodeLabel,
  reach,
  resourcesTopologyKey,
  topologyKey,
  withLive,
} from "./graph";
import { cascadeState, QUIET_MS } from "./graphCascade";
import { assignLayers, createLayoutCache, edgePath, layeringEdges, layoutGraph } from "./graphLayout";
import { pressMatches } from "./keys";
import { isFluxFilter, kindInfo } from "./kinds";
import { parseViewPrefs } from "./viewPrefs";

let rv = 0;
function res(kind: string, namespace: string, name: string, extra: Partial<Resource> = {}): Resource {
  const group = extra.group ?? kindInfo(kind).group;
  return {
    group,
    kind,
    namespace,
    name,
    id: `${group}/${kind}/${namespace}/${name}`,
    version: "v1",
    status: "ready",
    resourceVersion: String(++rv),
    ...extra,
  };
}
const ref = (r: Resource): Ref => ({ group: r.group, kind: r.kind, namespace: r.namespace, name: r.name });

/** The Flux bootstrap layout of the mock fixtures, small. */
function topology(opts: { failing?: boolean } = {}) {
  const git = res("GitRepository", "flux-system", "flux-system");
  const root = res("Kustomization", "flux-system", "flux-system", { source: ref(git) });
  git.owner = ref(root); // Flux bootstrap: the root Kustomization applies its own source.
  const helm = res("HelmRepository", "flux-system", "jetstack", { owner: ref(root) });
  const infra = res("Kustomization", "flux-system", "infra-controllers", {
    source: ref(git),
    owner: ref(root),
  });
  const configs = res("Kustomization", "flux-system", "infra-configs", {
    source: ref(git),
    owner: ref(root),
    dependsOn: [ref(infra)],
  });
  const apps = res("Kustomization", "flux-system", "apps", {
    source: ref(git),
    owner: ref(root),
    dependsOn: [ref(configs)],
  });
  const cert = res("HelmRelease", "cert-manager", "cert-manager", { source: ref(helm), owner: ref(infra) });
  const nginx = res("HelmRelease", "ingress-nginx", "ingress-nginx", {
    source: ref(helm),
    owner: ref(infra),
    dependsOn: [ref(cert)],
  });
  const alchemic = res("HelmRelease", "apps", "alchemic", {
    source: ref(helm),
    owner: ref(apps),
    status: opts.failing ? "failed" : "ready",
  });
  const worker = res("HelmRelease", "apps", "alchemic-worker", {
    source: ref(helm),
    owner: ref(apps),
    dependsOn: [ref(alchemic)],
    ...(opts.failing
      ? { status: "reconciling" as Status, blocked: true, message: "Waiting for apps/alchemic" }
      : {}),
  });
  const deploys = ["api", "web", "scheduler"].map((n) =>
    res("Deployment", "apps", `alchemic-${n}`, { owner: ref(alchemic) }),
  );
  const svc = res("Service", "apps", "alchemic-api", { owner: ref(alchemic) });
  const pod = res("Pod", "apps", "alchemic-api-1", { owner: ref(deploys[0] as Resource) });
  const items = [git, root, helm, infra, configs, apps, cert, nginx, alchemic, worker, ...deploys, svc, pod];
  return { git, root, helm, infra, configs, apps, cert, nginx, alchemic, worker, deploys, svc, pod, items };
}

describe("buildGraph (browser fallback)", () => {
  it("keeps Flux objects only for kinds=flux, with the hub's edge directions", () => {
    const t = topology();
    const g = buildGraph(t.items, { kinds: "flux" });
    expect(g.nodes.map((n) => n.kind)).not.toContain("Deployment");
    expect(g.edges).toContainEqual({ from: t.configs.id, to: t.infra.id, type: "dependsOn" });
    expect(g.edges).toContainEqual({ from: t.apps.id, to: t.git.id, type: "source" });
    expect(g.edges).toContainEqual({ from: t.apps.id, to: t.alchemic.id, type: "owns" });
    expect(g.truncated).toBe(false);
  });

  it("adds a missing node for a dependsOn target that is not in the list", () => {
    const a = res("Kustomization", "flux-system", "a", {
      dependsOn: [
        {
          group: "kustomize.toolkit.fluxcd.io",
          kind: "Kustomization",
          namespace: "flux-system",
          name: "gone",
        },
      ],
    });
    const g = buildGraph([a], { kinds: "flux" });
    const missing = g.nodes.find((n) => n.name === "gone");
    expect(missing?.missing).toBe(true);
    expect(g.edges).toEqual([{ from: a.id, to: missing?.id, type: "dependsOn" }]);
  });

  it("collapses more than COLLAPSE_OVER siblings of one kind into a group, unless the focus is among them", () => {
    const d = res("Deployment", "load", "gen");
    const pods = Array.from({ length: COLLAPSE_OVER + 1 }, (_, i) =>
      res("Pod", "load", `gen-${i}`, { owner: ref(d), status: i === 0 ? "failed" : "ready" }),
    );
    const g = buildGraph([d, ...pods], { kinds: "all" });
    const group = g.nodes.find((n) => n.id === groupId(d.id, "Pod"));
    expect(group).toMatchObject({ count: COLLAPSE_OVER + 1, owner: d.id, status: "failed" });
    expect(group?.statuses).toEqual({ failed: 1, ready: COLLAPSE_OVER });
    expect(g.nodes).toHaveLength(2);
    const focused = buildGraph([d, ...pods], { kinds: "all", focus: pods[3]?.id, hops: 1 });
    expect(focused.nodes.some((n) => n.id === pods[3]?.id)).toBe(true);
    expect(focused.nodes.some((n) => n.count)).toBe(false);
  });

  it("focus keeps the nodes within N hops in either direction", () => {
    const t = topology();
    const one = buildGraph(t.items, { kinds: "flux", focus: t.configs.id, hops: 1 });
    expect(new Set(one.nodes.map((n) => n.name))).toEqual(
      new Set(["infra-configs", "infra-controllers", "apps", "flux-system"]),
    );
    const two = buildGraph(t.items, { kinds: "flux", focus: t.configs.id, hops: 2 });
    expect(two.nodes.length).toBeGreaterThan(one.nodes.length);
    expect(two.nodes[0]?.id).toBe(t.configs.id);
    expect(buildGraph(t.items, { kinds: "flux", focus: "nope", hops: 2 }).nodes).toEqual([]);
    // focusGraph does the same on a graph already in hand.
    const all = buildGraph(t.items, { kinds: "flux" });
    expect(new Set(focusGraph(all, t.configs.id, 1).nodes.map((n) => n.id))).toEqual(
      new Set(one.nodes.map((n) => n.id)),
    );
  });
});

describe("layout", () => {
  it("puts sources first and orders Kustomizations and HelmReleases by dependsOn depth", () => {
    const t = topology();
    const layout = layoutGraph(buildGraph(t.items, { kinds: "flux" }));
    const layer = (r: Resource) => layout.nodes.get(r.id)?.layer ?? -1;
    expect(layer(t.git)).toBe(0);
    expect(layer(t.helm)).toBe(0);
    expect(layer(t.root)).toBe(1);
    expect(layer(t.infra)).toBeLessThan(layer(t.configs));
    expect(layer(t.configs)).toBeLessThan(layer(t.apps));
    expect(layer(t.cert)).toBeLessThan(layer(t.nginx));
    expect(layer(t.apps)).toBeLessThan(layer(t.alchemic));
    expect(layer(t.alchemic)).toBeLessThan(layer(t.worker));
    // Every drawn edge points right; x follows the layer.
    for (const e of layout.edges) {
      expect((e.points.at(-1) as [number, number])[0]).toBeGreaterThan((e.points[0] as [number, number])[0]);
    }
    expect(layout.nodes.get(t.apps.id)?.x).toBeGreaterThan(layout.nodes.get(t.configs.id)?.x ?? 0);
    // No two nodes of a column overlap.
    for (const col of layout.layers) {
      const ys = col.map((id) => layout.nodes.get(id)?.y ?? 0);
      for (let i = 1; i < ys.length; i++)
        expect((ys[i] as number) - (ys[i - 1] as number)).toBeGreaterThanOrEqual(62);
    }
  });

  it("breaks cycles: the bootstrap owns-loop and a dependsOn loop", () => {
    const t = topology();
    const a = res("Kustomization", "x", "a");
    const b = res("Kustomization", "x", "b", { dependsOn: [ref(a)] });
    a.dependsOn = [ref(b)];
    const g = buildGraph([...t.items, a, b], { kinds: "flux" });
    const index = indexGraph(g);
    const { dag, hidden } = layeringEdges(index);
    expect(hidden.length).toBeGreaterThanOrEqual(2);
    expect(hidden.some((e) => e.type === "owns" && e.to === t.git.id)).toBe(true);
    const layers = assignLayers(index.ids, dag);
    expect(layers.size).toBe(index.ids.length);
    expect(layers.get(a.id)).not.toBe(layers.get(b.id));
    const layout = layoutGraph(g);
    expect(layout.nodes.size).toBe(g.nodes.length);
    // Reachability walks terminate on cycles too.
    expect(reach(index, a.id, "down").has(b.id)).toBe(true);
    expect(reach(index, a.id, "down").has(a.id)).toBe(false);
  });

  it("keeps positions on status-only updates and re-lays out when the topology changes", () => {
    const t = topology();
    const cache = createLayoutCache();
    const first = cache(buildGraph(t.items, { kinds: "flux" }));
    const changed = t.items.map((r) =>
      r.id === t.apps.id ? { ...r, status: "failed" as Status, message: "boom", resourceVersion: "999" } : r,
    );
    const g2 = buildGraph(changed, { kinds: "flux" });
    expect(topologyKey(g2)).toBe(first.key);
    expect(resourcesTopologyKey(changed, "flux")).toBe(resourcesTopologyKey(t.items, "flux"));
    const second = cache(g2);
    expect(second.nodes).toBe(first.nodes);
    expect(second.edges).toBe(first.edges);
    expect(second.index.nodes.get(t.apps.id)?.status).toBe("failed");
    const added = [...t.items, res("Kustomization", "flux-system", "extra", { dependsOn: [ref(t.apps)] })];
    expect(resourcesTopologyKey(added, "flux")).not.toBe(resourcesTopologyKey(t.items, "flux"));
    const third = cache(buildGraph(added, { kinds: "flux" }));
    expect(third.nodes).not.toBe(first.nodes);
    expect(third.nodes.size).toBe(first.nodes.size + 1);
  });

  it("draws smooth paths through bends", () => {
    expect(
      edgePath([
        [0, 0],
        [10, 0],
      ]),
    ).toBe("M0,0L10,0");
    expect(
      edgePath([
        [0, 0],
        [10, 20],
      ]),
    ).toBe("M0,0C5,0 5,20 10,20");
  });

  it("lays out 500 nodes and 800 edges quickly", () => {
    const items: Resource[] = [];
    const git = res("GitRepository", "flux-system", "repo");
    items.push(git);
    for (let i = 0; i < 499; i++) {
      const deps: Ref[] = [];
      if (i > 0) deps.push(ref(items[1 + Math.floor((i * 7919) % i)] as Resource));
      if (i > 3 && i % 2) deps.push(ref(items[1 + Math.floor((i * 104729) % (i - 1))] as Resource));
      items.push(res("Kustomization", "flux-system", `ks-${i}`, { source: ref(git), dependsOn: deps }));
    }
    const g = buildGraph(items, { kinds: "flux" });
    expect(g.nodes).toHaveLength(500);
    expect(g.edges.length).toBeGreaterThanOrEqual(800);
    const t0 = performance.now();
    const layout = layoutGraph(g);
    const ms = performance.now() - t0;
    expect(layout.nodes.size).toBe(500);
    expect(ms).toBeLessThan(3000);
  });
});

describe("lineage and collapsing (detail page)", () => {
  it("shows upstream, the object, its dependents and its own inventory grouped per kind", () => {
    const t = topology();
    const all = buildGraph(t.items, { kinds: "all" });
    const g = lineage(all, t.alchemic.id);
    const names = new Set(g.nodes.map((n) => n.name));
    expect(names.has("apps")).toBe(true); // owner, upstream
    expect(names.has("jetstack")).toBe(true); // source
    expect(names.has("alchemic-worker")).toBe(true); // dependent
    expect(names.has("alchemic-api-1")).toBe(false); // a pod of an owned Deployment
    expect(names.has("cert-manager")).toBe(false); // a sibling via the shared source
    const collapsed = collapseOwned(g, t.alchemic.id, new Set(), t.items);
    const deployments = collapsed.nodes.find((n) => n.id === groupId(t.alchemic.id, "Deployment"));
    expect(deployments).toMatchObject({ count: 3, status: "ready", namespace: "apps" });
    expect(collapsed.edges).toContainEqual({ from: t.alchemic.id, to: deployments?.id, type: "owns" });
    // A single Service stays a node of its own.
    expect(collapsed.nodes.some((n) => n.id === t.svc.id)).toBe(true);
    const open = collapseOwned(g, t.alchemic.id, new Set([groupId(t.alchemic.id, "Deployment")]), t.items);
    expect(open.nodes.filter((n) => n.kind === "Deployment")).toHaveLength(3);
  });

  it("expands a group the hub collapsed from the resources list", () => {
    const t = topology();
    const gid = groupId(t.alchemic.id, "Deployment");
    const server: GraphResponse = {
      nodes: [
        {
          id: t.alchemic.id,
          kind: "HelmRelease",
          group: t.alchemic.group,
          namespace: "apps",
          name: "alchemic",
          status: "ready",
        },
        {
          id: gid,
          kind: "Deployment",
          group: "apps",
          namespace: "apps",
          count: 3,
          owner: t.alchemic.id,
          statuses: { ready: 3 },
        },
      ],
      edges: [{ from: t.alchemic.id, to: gid, type: "owns" }],
    };
    const open = collapseOwned(server, t.alchemic.id, new Set([gid]), t.items);
    expect(
      open.nodes
        .filter((n) => n.kind === "Deployment")
        .map((n) => n.name)
        .sort(),
    ).toEqual(["alchemic-api", "alchemic-scheduler", "alchemic-web"]);
    expect(open.edges).toHaveLength(3);
  });
});

describe("blocked objects", () => {
  it("recognises DependencyNotReady from the flag or the Ready condition", () => {
    expect(isBlocked({ blocked: true, status: "reconciling" })).toBe(true);
    expect(
      isBlocked({
        status: "reconciling",
        conditions: [{ type: "Ready", status: "False", reason: "DependencyNotReady" }],
      }),
    ).toBe(true);
    expect(isBlocked({ status: "reconciling" })).toBe(false);
  });

  it("tints the path from a failing release to the one waiting on it, and names the cause", () => {
    const t = topology({ failing: true });
    const index = indexGraph(buildGraph(t.items, { kinds: "flux" }));
    const b = blockage(index);
    expect(b.waitingOn.get(t.worker.id)).toBe(t.alchemic.id);
    expect([...b.edges]).toEqual([`${t.alchemic.id}→${t.worker.id}:dependsOn`]);
    const worker = index.nodes.get(t.worker.id);
    expect(worker && nodeLabel(index, worker, t.alchemic.id)).toBe(
      "HelmRelease alchemic-worker, waiting, depends on alchemic, source jetstack, waiting on alchemic",
    );
    // Healthy: nothing is tinted.
    expect(blockage(indexGraph(buildGraph(topology().items, { kinds: "flux" }))).edges.size).toBe(0);
  });

  it("follows a chain of waiting objects back to the first one that is not waiting", () => {
    const a = res("Kustomization", "x", "a", { status: "failed" });
    const b = res("Kustomization", "x", "b", { status: "reconciling", blocked: true, dependsOn: [ref(a)] });
    const c = res("Kustomization", "x", "c", { status: "reconciling", blocked: true, dependsOn: [ref(b)] });
    const b2 = blockage(indexGraph(buildGraph([a, b, c], { kinds: "flux" })));
    expect(b2.waitingOn.get(c.id)).toBe(a.id);
    expect(b2.edges.size).toBe(2);
  });
});

describe("live updates", () => {
  it("takes statuses from the list and keeps unchanged nodes", () => {
    const t = topology();
    const g = buildGraph(t.items, { kinds: "flux" });
    const same = withLive(g, new Map(t.items.map((r) => [r.id, r])), t.items);
    expect(same).toBe(g);
    const failing = { ...t.apps, status: "failed" as Status };
    const items = t.items.map((r) => (r.id === t.apps.id ? failing : r));
    const live = withLive(g, new Map(items.map((r) => [r.id, r])), items);
    expect(live).not.toBe(g);
    expect(live.nodes.find((n) => n.id === t.apps.id)?.status).toBe("failed");
    const other = g.nodes.find((n) => n.id === t.git.id);
    expect(live.nodes.find((n) => n.id === t.git.id)).toBe(other);
  });

  it("recounts collapsed groups from their members", () => {
    const d = res("Deployment", "load", "gen");
    const pods = Array.from({ length: COLLAPSE_OVER + 2 }, (_, i) =>
      res("Pod", "load", `p-${i}`, { owner: ref(d) }),
    );
    const g = buildGraph([d, ...pods], { kinds: "all" });
    const items = [d, ...pods.map((p, i) => (i < 2 ? { ...p, status: "failed" as Status } : p))];
    const live = withLive(g, new Map(items.map((r) => [r.id, r])), items);
    expect(live.nodes.find((n) => n.count)?.statuses).toEqual({ failed: 2, ready: COLLAPSE_OVER });
    expect(live.nodes.find((n) => n.count)?.status).toBe("failed");
  });

  it("a reconcile wave waits for the downstream objects to report a change", () => {
    const t = topology();
    const at = Date.parse("2026-10-01T10:00:00Z");
    const index = indexGraph(buildGraph(t.items, { kinds: "flux" }));
    const c = { root: t.alchemic.id, at, downstream: new Set([t.worker.id]) };
    const nodes = new Map(index.nodes);
    expect(cascadeState(c, nodes, true, at + 100)).toEqual({ pending: new Set([t.worker.id]), over: false });
    const worker = nodes.get(t.worker.id);
    nodes.set(t.worker.id, {
      ...(worker as NonNullable<typeof worker>),
      lastChanged: new Date(at + 2000).toISOString(),
      status: "reconciling",
    });
    expect(cascadeState(c, nodes, false, at + 2100).pending.has(t.worker.id)).toBe(true);
    nodes.set(t.worker.id, {
      ...(worker as NonNullable<typeof worker>),
      lastChanged: new Date(at + 3000).toISOString(),
      status: "ready",
    });
    expect(cascadeState(c, nodes, false, at + 3100)).toEqual({ pending: new Set(), over: true });
    // Nothing downstream ever changes: the wave ends after a quiet period.
    const quiet = cascadeState(c, new Map(index.nodes), false, at + QUIET_MS + 1);
    expect(quiet.over).toBe(true);
  });
});

describe("graph view plumbing", () => {
  it("offers the graph on Flux pages only", () => {
    expect(isFluxFilter("flux")).toBe(true);
    expect(isFluxFilter("sources")).toBe(true);
    expect(isFluxFilter("Kustomization")).toBe(true);
    expect(isFluxFilter("workloads")).toBe(false);
    expect(isFluxFilter("Deployment")).toBe(false);
    expect(isFluxFilter(undefined)).toBe(false);
  });

  it("keeps the graph prefs", () => {
    expect(parseViewPrefs({ listView: "graph", managesView: "graph", graphHops: 3 })).toEqual({
      listView: "graph",
      managesView: "graph",
      graphHops: 3,
    });
    expect(parseViewPrefs({ managesView: "x", graphHops: 9 })).toEqual({});
  });

  it("matches widget keys from the registry", () => {
    const k = (key: string, shiftKey = false) => ({
      key,
      shiftKey,
      ctrlKey: false,
      metaKey: false,
      altKey: false,
    });
    expect(pressMatches("down", k("j"))).toBe(true);
    expect(pressMatches("down", k("ArrowDown"))).toBe(true);
    expect(pressMatches("open", k("l"))).toBe(true);
    expect(pressMatches("graphZoomIn", k("+", true))).toBe(true);
    expect(pressMatches("graphZoomIn", k("="))).toBe(true);
    expect(pressMatches("graphFocus", k("F", true))).toBe(true);
    expect(pressMatches("down", { ...k("j"), ctrlKey: true })).toBe(false);
    expect(pressMatches("top", k("g"))).toBe(false);
  });
});
