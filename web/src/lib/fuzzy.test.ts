import { describe, expect, it } from "vitest";
import { highlightParts, matchField, matchItem, rank, TIER } from "./fuzzy";

const tier = (score: number | undefined) => Math.floor((score ?? 0) / 100) * 100;

describe("matchField", () => {
  it("orders exact > prefix > word boundary > substring > subsequence on the primary field", () => {
    expect(tier(matchField("redis", "redis", true)?.score)).toBe(TIER.exact);
    expect(tier(matchField("red", "redis", true)?.score)).toBe(TIER.prefix);
    expect(tier(matchField("sys", "flux-system", true)?.score)).toBe(TIER.word);
    expect(tier(matchField("ystem", "flux-system", true)?.score)).toBe(TIER.substring);
    expect(tier(matchField("cm", "cert-manager", true)?.score)).toBe(TIER.subsequence);
  });

  it("never matches a secondary field by subsequence", () => {
    expect(matchField("fs", "flux-system", false)).toBeNull();
    expect(tier(matchField("flux-sy", "flux-system", false)?.score)).toBe(TIER.secondaryPrefix);
  });

  it("rejects scattered subsequences below the minimum quality", () => {
    expect(matchField("fdp", "podinfo", true)).toBeNull();
    expect(matchField("xyz", "podinfo", true)).toBeNull();
    expect(matchField("oif", "podinfo", true)).toBeNull();
  });

  it("returns the matched ranges", () => {
    expect(matchField("sys", "flux-system", true)?.ranges).toEqual([[5, 8]]);
    expect(matchField("cm", "cert-manager", true)?.ranges).toEqual([
      [0, 1],
      [5, 6],
    ]);
  });

  it("prefers shorter targets inside a tier", () => {
    const a = matchField("redis", "redis-master", true)?.score ?? 0;
    const b = matchField("redis", "redis-replicas-long-name", true)?.score ?? 0;
    expect(a).toBeGreaterThan(b);
  });
});

describe("matchItem", () => {
  it("ANDs whitespace-separated terms across fields", () => {
    const fields = { primary: "podinfo", secondary: ["apps", "HelmRelease", "staging"] };
    const m = matchItem("pod staging", fields);
    expect(m).not.toBeNull();
    expect(m?.primary).toEqual([[0, 3]]);
    expect(m?.secondary[2]).toEqual([[0, 7]]);
    expect(matchItem("pod prod", fields)).toBeNull();
  });

  it("matches everything for an empty query", () => {
    expect(matchItem("", { primary: "anything" })?.score).toBeGreaterThan(0);
  });
});

interface Item {
  name: string;
  namespace: string;
  kind: string;
  failing?: boolean;
}

const fields = (i: Item) => ({ primary: i.name, secondary: [i.namespace, i.kind] });

describe("rank", () => {
  const items: Item[] = [
    { name: "apps", namespace: "flux-system", kind: "Kustomization", failing: true },
    { name: "infra-configs", namespace: "flux-system", kind: "Kustomization" },
    { name: "flux-system", namespace: "flux-system", kind: "Kustomization" },
    { name: "flux-system", namespace: "flux-system", kind: "GitRepository" },
    { name: "podinfo", namespace: "apps", kind: "HelmRelease", failing: true },
  ];

  it('ranks names starting with "flux-sy" above items that only match by namespace', () => {
    const ranked = rank("flux-sy", items, fields).map((r) => `${r.item.kind}/${r.item.name}`);
    expect(ranked.slice(0, 2).sort()).toEqual(["GitRepository/flux-system", "Kustomization/flux-system"]);
    expect(ranked.indexOf("Kustomization/apps")).toBeGreaterThan(1);
  });

  it("puts failing items first only when match quality is equal", () => {
    const failingFirst = (a: Item, b: Item) => Number(Boolean(b.failing)) - Number(Boolean(a.failing));
    // "apps" is an exact name match for the Kustomization and a namespace match for podinfo.
    const ranked = rank("apps", items, fields, 10, failingFirst).map((r) => r.item.name);
    expect(ranked[0]).toBe("apps");
    // Among the namespace-only matches for "flux", the failing one comes first.
    const byNs = rank("flux-system", items, fields, 10, failingFirst).map((r) => r.item.name);
    expect(byNs.slice(0, 2)).toEqual(["flux-system", "flux-system"]);
    expect(byNs[2]).toBe("apps");
  });

  it("drops non-matches and applies the limit", () => {
    expect(rank("zzz", items, fields)).toHaveLength(0);
    expect(rank("", items, fields, 2)).toHaveLength(2);
  });
});

describe("highlightParts", () => {
  it("splits text around matched ranges", () => {
    expect(highlightParts("flux-system", [[5, 8]])).toEqual([
      { text: "flux-", hit: false },
      { text: "sys", hit: true },
      { text: "tem", hit: false },
    ]);
  });
});
