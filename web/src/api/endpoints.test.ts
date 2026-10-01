import { afterEach, describe, expect, it, vi } from "vitest";
import { getEvents, getKinds, getObject, getYaml } from "./endpoints";

afterEach(() => vi.unstubAllGlobals());

function stub() {
  const fn = vi
    .fn<typeof fetch>()
    .mockImplementation(async () => new Response("{}", { headers: { "Content-Type": "application/json" } }));
  vi.stubGlobal("fetch", fn);
  return () => String(fn.mock.calls[fn.mock.calls.length - 1]?.[0]);
}

const nodePool = { group: "karpenter.sh", kind: "NodePool", namespace: "", name: "general" };
const secret = { group: "", kind: "Secret", namespace: "apps", name: "checkout-db" };

describe("object requests", () => {
  it("send the API group on every read, and `core` for the core group", async () => {
    const lastUrl = stub();
    await getObject("prod-eu", nodePool);
    expect(lastUrl()).toBe("/api/v1/clusters/prod-eu/objects/NodePool/_/general?group=karpenter.sh");
    await getYaml("prod-eu", nodePool);
    expect(lastUrl()).toBe("/api/v1/clusters/prod-eu/objects/NodePool/_/general/yaml?group=karpenter.sh");
    await getEvents("prod-eu", secret);
    expect(lastUrl()).toBe("/api/v1/clusters/prod-eu/objects/Secret/apps/checkout-db/events?group=core");
  });

  it("reads the kinds of a cluster", async () => {
    const lastUrl = stub();
    await getKinds("prod-eu");
    expect(lastUrl()).toBe("/api/v1/clusters/prod-eu/kinds");
  });
});
