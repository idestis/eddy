import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { pageTitle, useTitle } from "./title";

describe("pageTitle", () => {
  it("joins the resource or page, the cluster and a lower-case eddy", () => {
    expect(pageTitle("apps", "staging")).toBe("apps · staging · eddy");
    expect(pageTitle("staging")).toBe("staging · eddy");
    expect(pageTitle("fleet")).toBe("fleet · eddy");
    expect(pageTitle("sign in")).toBe("sign in · eddy");
  });

  it("skips empty parts", () => {
    expect(pageTitle(undefined, "", "tokens", null, false)).toBe("tokens · eddy");
    expect(pageTitle()).toBe("eddy");
  });
});

describe("useTitle", () => {
  it("sets document.title", () => {
    renderHook(() => useTitle("podinfo", "prod-eu"));
    expect(document.title).toBe("podinfo · prod-eu · eddy");
  });
});
