import { describe, expect, it } from "vitest";
import { detailLink, safeReturnTo } from "./links";

describe("safeReturnTo", () => {
  it.each([
    ["/c/prod-eu?filter=x", "/c/prod-eu?filter=x"],
    [undefined, "/"],
    ["https://evil.com", "/"],
    ["//evil.com", "/"],
    ["/\\evil.com", "/"],
    ["/login?returnTo=/x", "/"],
  ])("%s → %s", (input, expected) => {
    expect(safeReturnTo(input)).toBe(expected);
  });
});

describe("detailLink", () => {
  it("uses _ for cluster-scoped objects and omits the default view", () => {
    expect(detailLink("dev", { kind: "ClusterIssuer", namespace: "", name: "le" })).toEqual({
      to: "/c/$cluster/r/$kind/$ns/$name",
      params: { cluster: "dev", kind: "ClusterIssuer", ns: "_", name: "le" },
      search: {},
    });
    expect(detailLink("dev", { kind: "Pod", namespace: "a", name: "p" }, "logs").search).toEqual({
      view: "logs",
    });
  });
});
