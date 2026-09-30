import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { parseInline, safeHref } from "../lib/markdown";
import { Markdown } from "./Markdown";

const html = (source: string) => render(<Markdown source={source} />).container;

describe("Markdown", () => {
  it("renders paragraphs, lists, code and emphasis", () => {
    const c = html("Hello **world** and `code`\n\n- one\n- two\n\n1. first\n\n```\nflux get ks\n```");
    expect(c.querySelector("strong")?.textContent).toBe("world");
    expect(c.querySelector("p code")?.textContent).toBe("code");
    expect(c.querySelectorAll("ul li")).toHaveLength(2);
    expect(c.querySelectorAll("ol li")).toHaveLength(1);
    expect(c.querySelector("pre code")?.textContent).toBe("flux get ks");
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

  it("renders headings as plain paragraphs", () => {
    const c = html("# Title\ntext");
    expect(c.querySelector("h1")).toBeNull();
    expect(c.querySelector("p")?.textContent).toBe("Title text");
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
