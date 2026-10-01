import { render } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import type { ResourceRef } from "../api/types";
import { parseInline, parseMarkdown, safeHref } from "../lib/markdown";
import { Markdown } from "./Markdown";

// The router Link, reduced to an anchor that exposes its target for assertions.
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    params,
    search,
    title,
    className,
  }: {
    children: ReactNode;
    to: string;
    params: Record<string, string>;
    search?: Record<string, string>;
    title?: string;
    className?: string;
  }) => (
    <a
      href={to.replace(/\$(\w+)/g, (_, k: string) => params[k] ?? "")}
      data-internal="true"
      data-search={JSON.stringify(search ?? {})}
      title={title}
      className={className}
    >
      {children}
    </a>
  ),
}));

const html = (source: string, refs?: ResourceRef[]) =>
  render(<Markdown source={source} refs={refs} />).container;

describe("Markdown", () => {
  it("renders paragraphs, lists, code and emphasis", () => {
    const c = html("Hello **world** and `code`\n\n- one\n- two\n\n1. first\n\n```\nflux get ks\n```");
    expect(c.querySelector("strong")?.textContent).toBe("world");
    expect(c.querySelector("p code")?.textContent).toBe("code");
    expect(c.querySelectorAll("ul li")).toHaveLength(2);
    expect(c.querySelectorAll("ol li")).toHaveLength(1);
    expect(c.querySelector("pre code")?.textContent).toBe("flux get ks");
  });

  it("gives code blocks a Copy button and keeps long lines scrollable", () => {
    const c = html(
      "```\nkubectl logs deploy/podinfo -n apps --all-containers --since=1h --timestamps --prefix\n```",
    );
    expect(c.querySelector(".md-pre pre")).not.toBeNull();
    expect(c.querySelector(".md-pre button[aria-label='Copy code']")).not.toBeNull();
  });

  it("renders tables and headings, with inline formatting and no HTML", () => {
    const c = html(
      "### Health\n\n| Category | Status |\n|---|:---:|\n| **Flux** | ✅ `v2.7.2` |\n| a \\| b | <b>x</b> |\n\nAfter",
    );
    expect(c.querySelector(".md-h strong")?.textContent).toBe("Health");
    expect([...c.querySelectorAll("th")].map((th) => th.textContent)).toEqual(["Category", "Status"]);
    const rows = c.querySelectorAll("tbody tr");
    expect(rows).toHaveLength(2);
    expect(rows[0]?.querySelector("strong")?.textContent).toBe("Flux");
    expect(rows[0]?.querySelector("code")?.textContent).toBe("v2.7.2");
    expect(rows[1]?.querySelector("td")?.textContent).toBe("a | b");
    expect(c.querySelector("b")).toBeNull();
    expect(c.querySelector("p:last-child")?.textContent).toBe("After");
  });

  it("keeps pipes without a rule line as a paragraph", () => {
    const c = html("| not | a table |\nstill text");
    expect(c.querySelector("table")).toBeNull();
    expect(c.querySelector("p")?.textContent).toContain("| not | a table |");
  });

  it("never renders images", () => {
    const c = html("look ![tracking pixel](https://evil.example/p.png) here");
    expect(c.querySelector("img")).toBeNull();
    expect(c.textContent).toContain("tracking pixel");
    expect(c.innerHTML).not.toContain("evil.example");
  });

  it("shows raw HTML as text", () => {
    const c = html('<script>alert(1)</script><img src=x onerror="alert(2)"><b>bold</b>');
    expect(c.querySelector("script")).toBeNull();
    expect(c.querySelector("img")).toBeNull();
    expect(c.querySelector("b")).toBeNull();
    expect(c.textContent).toContain("<script>alert(1)</script>");
  });

  it("drops javascript:, data: and relative links but keeps their text", () => {
    const c = html("[a](javascript:alert(1)) [b](data:text/html,x) [c](/local) [d](JAVASCRIPT:alert(1))");
    expect(c.querySelector("a")).toBeNull();
    expect(c.textContent).toContain("a");
    expect(c.textContent).toContain("d");
  });

  it("opens http(s) links safely in a new tab", () => {
    const c = html("see [docs](https://fluxcd.io/docs) or https://example.com/x.");
    const links = [...c.querySelectorAll("a")];
    expect(links.map((a) => a.getAttribute("href"))).toEqual([
      "https://fluxcd.io/docs",
      "https://example.com/x",
    ]);
    for (const a of links) {
      expect(a.getAttribute("rel")).toContain("noopener");
      expect(a.getAttribute("rel")).toContain("noreferrer");
      expect(a.getAttribute("target")).toBe("_blank");
    }
  });

  it("renders headings as bold lines, not heading elements", () => {
    const c = html("# Title\ntext");
    expect(c.querySelector("h1")).toBeNull();
    expect(c.querySelector(".md-h")?.textContent).toBe("Title");
    expect(c.querySelector("p:last-child")?.textContent).toBe("text");
  });
});

describe("safeHref", () => {
  it.each([
    ["https://a.b/c", "https://a.b/c"],
    ["http://a.b", "http://a.b/"],
    ["javascript:alert(1)", null],
    [" javascript:alert(1)", null],
    ["vbscript:x", null],
    ["//evil.com", null],
    ["mailto:a@b.c", null],
  ])("%s → %s", (input, expected) => {
    expect(safeHref(input)).toBe(expected);
  });
});

describe("parseInline", () => {
  it("does not parse inside code spans", () => {
    expect(parseInline("`**x**`")).toEqual([{ t: "code", text: "**x**" }]);
  });
});

describe("nested lists", () => {
  const owner = "- **Namespaces**\n  - `apps`\n  - `services`\n- **NodePools**\n  - `apps-amd64`";

  it("parses one level of nesting", () => {
    const [list] = parseMarkdown(owner);
    expect(list?.t).toBe("ul");
    if (list?.t !== "ul") return;
    expect(list.items).toHaveLength(2);
    expect(list.items[0]?.sub?.items.map((i) => i.children)).toEqual([
      [{ t: "code", text: "apps" }],
      [{ t: "code", text: "services" }],
    ]);
    expect(list.items[1]?.sub?.items).toHaveLength(1);
  });

  it("renders nested ul inside li", () => {
    const c = html(owner);
    expect(c.querySelectorAll(".md > ul > li")).toHaveLength(2);
    expect(c.querySelectorAll(".md > ul > li > ul > li")).toHaveLength(3);
    expect(c.querySelector(".md > ul > li > strong")?.textContent).toBe("Namespaces");
  });

  it("flattens deeper levels and accepts tabs and numbered children", () => {
    const [list] = parseMarkdown("1. one\n\t- a\n      - deep\n2. two\n   1. b");
    expect(list?.t).toBe("ol");
    if (list?.t !== "ol") return;
    expect(list.items).toHaveLength(2);
    expect(list.items[0]?.sub?.t).toBe("ul");
    expect(list.items[0]?.sub?.items).toHaveLength(2);
    expect(list.items[0]?.sub?.items[1]?.sub).toBeUndefined();
    expect(list.items[1]?.sub?.t).toBe("ol");
  });

  it("keeps a one-space indent at the top level", () => {
    const [list] = parseMarkdown("- a\n - b");
    expect(list?.t === "ul" && list.items.length).toBe(2);
  });
});

describe("resource links", () => {
  const hr: ResourceRef = {
    cluster: "prod",
    group: "helm.toolkit.fluxcd.io",
    kind: "HelmRelease",
    namespace: "apps",
    name: "podinfo",
  };
  const dep: ResourceRef = { ...hr, group: "apps", kind: "Deployment" };
  const pool: ResourceRef = {
    cluster: "dev",
    group: "karpenter.sh",
    kind: "NodePool",
    namespace: "",
    name: "apps-amd64",
  };
  const refs = [hr, dep, pool];
  const internal = (c: HTMLElement) => [...c.querySelectorAll("a[data-internal]")];

  it("links an exact Kind/namespace/name", () => {
    const [a, ...rest] = internal(html("See `HelmRelease/apps/podinfo`.", refs));
    expect(rest).toHaveLength(0);
    expect(a?.getAttribute("href")).toBe("/c/prod/r/HelmRelease/apps/podinfo");
    expect(a?.getAttribute("title")).toBe("Open HelmRelease apps/podinfo in prod");
    expect(a?.className).toBe("md-ref");
    expect(a?.querySelector("code")?.textContent).toBe("HelmRelease/apps/podinfo");
    expect(a?.getAttribute("target")).toBeNull();
  });

  it("accepts a short kind", () => {
    const [a] = internal(html("`HR/apps/podinfo`", refs));
    expect(a?.getAttribute("href")).toBe("/c/prod/r/HelmRelease/apps/podinfo");
  });

  it("leaves an ambiguous name as plain code", () => {
    const c = html("`podinfo` and `apps/podinfo`", refs);
    expect(internal(c)).toHaveLength(0);
    expect(c.querySelectorAll("code")).toHaveLength(2);
  });

  it("leaves an unknown object as plain code", () => {
    const c = html("`HelmRelease/apps/ghost`", refs);
    expect(internal(c)).toHaveLength(0);
    expect(c.querySelector("code")?.textContent).toBe("HelmRelease/apps/ghost");
  });

  it("links a ref in another cluster to that cluster", () => {
    const [a] = internal(html("- **NodePools**\n  - `apps-amd64`", refs));
    expect(a?.getAttribute("href")).toBe("/c/dev/r/NodePool/_/apps-amd64");
    expect(a?.closest("li li")).not.toBeNull();
  });

  it("never makes a model-written URL internal", () => {
    const c = html(
      "[podinfo](/c/prod/r/HelmRelease/apps/podinfo) [x](https://eddy.example/c/prod/r/HelmRelease/apps/podinfo) [`HelmRelease/apps/podinfo`](https://evil.example) `/c/prod/r/HelmRelease/apps/podinfo`",
      refs,
    );
    expect(internal(c)).toHaveLength(0);
    const external = [...c.querySelectorAll("a")];
    expect(external.map((a) => a.getAttribute("href"))).toEqual([
      "https://eddy.example/c/prod/r/HelmRelease/apps/podinfo",
      "https://evil.example/",
    ]);
    for (const a of external) expect(a.getAttribute("target")).toBe("_blank");
  });

  it("links nothing without refs", () => {
    expect(internal(html("`HelmRelease/apps/podinfo`"))).toHaveLength(0);
  });
});
