import { describe, expect, it } from "vitest";
import { fieldText, normalizeLevel, parseStructured } from "./logFormat";

describe("parseStructured", () => {
  it("reads zap-style JSON: level, message and key fields", () => {
    const s = parseStructured(
      '{"level":"warn","ts":1727700000.1,"caller":"api/server.go:12","msg":"slow request","error":"timeout","path":"/x"}',
    );
    expect(s?.format).toBe("json");
    expect(s?.level).toBe("warn");
    expect(s?.msg).toBe("slow request");
    expect(s?.key).toEqual([
      ["caller", "api/server.go:12"],
      ["error", "timeout"],
    ]);
    expect(s?.fields.map(([k]) => k)).toContain("path");
  });

  it("reads logfmt with quoted values", () => {
    const s = parseStructured('level=error msg="payment failed" error="upstream \\"timeout\\"" order=o1');
    expect(s?.format).toBe("logfmt");
    expect(s?.level).toBe("error");
    expect(s?.msg).toBe("payment failed");
    expect(s?.key).toEqual([["error", 'upstream "timeout"']]);
  });

  it("allows a kubectl --timestamps prefix", () => {
    expect(parseStructured('2026-09-30T12:00:00.1Z {"msg":"hi","level":"info"}')?.msg).toBe("hi");
  });

  it("leaves plain text and non-log JSON alone", () => {
    expect(parseStructured("GET /healthz 200")).toBeUndefined();
    expect(parseStructured('{"a":1}')).toBeUndefined();
    expect(parseStructured("[1,2]")).toBeUndefined();
    expect(parseStructured("{not json")).toBeUndefined();
  });
});

describe("normalizeLevel", () => {
  it.each([
    ["ERROR", "error"],
    ["fatal", "error"],
    ["warning", "warn"],
    ["INFO", "info"],
    ["trace", "debug"],
    [50, "error"],
    [40, "warn"],
    [30, "info"],
    [20, "debug"],
    ["custom", undefined],
  ])("%s → %s", (v, want) => {
    expect(normalizeLevel(v)).toBe(want);
  });
});

describe("fieldText", () => {
  it("keeps a stack trace's newlines and tabs, and pretty-prints objects", () => {
    expect(fieldText("a\n\tb")).toBe("a\n\tb");
    expect(fieldText({ a: 1 })).toBe('{\n  "a": 1\n}');
  });
});
