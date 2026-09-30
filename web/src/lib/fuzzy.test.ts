import { describe, expect, it } from "vitest";
import { fuzzyScore, rank } from "./fuzzy";

describe("fuzzyScore", () => {
  it("requires every character in order", () => {
    expect(fuzzyScore("pdf", "podinfo")).toBeGreaterThan(0);
    expect(fuzzyScore("fdp", "podinfo")).toBe(0);
    expect(fuzzyScore("xyz", "podinfo")).toBe(0);
  });

  it("matches everything for an empty query", () => {
    expect(fuzzyScore("", "anything")).toBeGreaterThan(0);
  });

  it("prefers contiguous and word-start matches", () => {
    expect(fuzzyScore("pod", "podinfo")).toBeGreaterThan(fuzzyScore("pod", "prod-deploy"));
    expect(fuzzyScore("cm", "cert-manager")).toBeGreaterThan(fuzzyScore("cm", "ciamo"));
  });

  it("prefers shorter targets on equal matches", () => {
    expect(fuzzyScore("redis", "redis")).toBeGreaterThan(fuzzyScore("redis", "redis-replicas-long-name"));
  });

  it("is case-insensitive and ignores spaces in the query", () => {
    expect(fuzzyScore("POD info", "podinfo")).toBeGreaterThan(0);
  });
});

describe("rank", () => {
  const names = ["ingress-nginx", "podinfo", "podinfo-redis", "cert-manager", "flux-system"];

  it("orders by score and applies the limit", () => {
    expect(rank("pod", names, (s) => s)).toEqual(["podinfo", "podinfo-redis"]);
    expect(rank("", names, (s) => s, 2)).toHaveLength(2);
  });

  it("applies the boost", () => {
    const items = [
      { name: "podinfo", cluster: "staging" },
      { name: "podinfo", cluster: "prod-eu" },
    ];
    const ranked = rank(
      "podinfo",
      items,
      (i) => i.name,
      10,
      (i) => (i.cluster === "prod-eu" ? 0.5 : 0),
    );
    expect(ranked[0]?.cluster).toBe("prod-eu");
  });
});
