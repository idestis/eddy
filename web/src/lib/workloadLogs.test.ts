import { describe, expect, it } from "vitest";
import type { WorkloadLogEntry } from "../api/types";
import {
  appendEntries,
  containersOf,
  filterLines,
  type LogLine,
  levelCounts,
  podHue,
  podLabel,
  toText,
  truncationNotice,
} from "./workloadLogs";

const at = (s: number) => new Date(Date.UTC(2026, 8, 30, 12, 0, s)).toISOString();
const e = (
  pod: string,
  line: string,
  s?: number,
  extra: Partial<WorkloadLogEntry> = {},
): WorkloadLogEntry => ({
  pod,
  container: "app",
  line,
  ts: s === undefined ? undefined : at(s),
  ...extra,
});

function fill(entries: WorkloadLogEntry[], cap?: number): LogLine[] {
  return appendEntries([], entries, 0, cap).lines;
}

describe("appendEntries", () => {
  it("merges pods by timestamp, across batches", () => {
    let res = appendEntries([], [e("a", "a1", 1), e("a", "a3", 3)], 0);
    res = appendEntries(res.lines, [e("b", "b2", 2), e("b", "b4", 4)], res.nextId);
    expect(res.lines.map((l) => l.line)).toEqual(["a1", "b2", "a3", "b4"]);
    expect(new Set(res.lines.map((l) => l.id)).size).toBe(4);
  });

  it("appends untimed lines and markers in arrival order, and never moves them", () => {
    let res = appendEntries(
      [],
      [e("a", "a1", 1), e("", "37 lines dropped", undefined, { marker: "dropped" })],
      0,
    );
    res = appendEntries(res.lines, [e("b", "b0", 0)], res.nextId);
    expect(res.lines.map((l) => l.line)).toEqual(["a1", "37 lines dropped", "b0"]);
  });

  it("caps the buffer, dropping the oldest", () => {
    const res = appendEntries(
      [],
      Array.from({ length: 12 }, (_, i) => e("a", `l${i}`, i)),
      0,
      10,
    );
    expect(res.lines).toHaveLength(10);
    expect(res.trimmed).toBe(2);
    expect(res.lines[0]?.line).toBe("l2");
  });

  it("sets levels from structured fields or the text", () => {
    const lines = fill([
      e("a", '{"level":"warn","msg":"slow"}', 1),
      e("a", "panic: boom", 2),
      e("a", "level=debug msg=tick", 3),
      e("a", "all good", 4),
    ]);
    expect(lines.map((l) => l.level)).toEqual(["warn", "error", "debug", "info"]);
    expect(levelCounts(lines)).toEqual({ error: 1, warn: 1, info: 1, debug: 1 });
  });
});

describe("filterLines", () => {
  const lines = fill([
    e("web-1", "GET /healthz", 1),
    e("web-2", "error: timeout", 2, { container: "proxy" }),
    e("web-1", "forbidden", undefined, { marker: "forbidden" }),
    e("", "5 lines dropped", undefined, { marker: "dropped" }),
  ]);

  it("filters by pod, container, level and text; stream-wide markers always show", () => {
    expect(filterLines(lines, { pods: new Set(["web-2"]) }).map((l) => l.line)).toEqual([
      "error: timeout",
      "5 lines dropped",
    ]);
    expect(filterLines(lines, { container: "app" }).map((l) => l.line)).toEqual([
      "GET /healthz",
      "forbidden",
      "5 lines dropped",
    ]);
    expect(filterLines(lines, { levels: new Set(["error"] as const) }).map((l) => l.line)).toEqual([
      "error: timeout",
      "forbidden",
      "5 lines dropped",
    ]);
    expect(filterLines(lines, { query: "HEALTH" }).map((l) => l.line)).toEqual([
      "GET /healthz",
      "5 lines dropped",
    ]);
    expect(filterLines(lines, {})).toBe(lines);
  });
});

describe("helpers", () => {
  it("gives each pod a stable hue", () => {
    expect(podHue("web-1")).toBe(podHue("web-1"));
    expect(podHue("web-1")).toBeGreaterThanOrEqual(0);
    expect(podHue("web-1")).toBeLessThan(360);
    expect(podHue("web-1")).not.toBe(podHue("web-2"));
  });

  it("shortens pod names against the workload", () => {
    expect(podLabel("podinfo-7d9f-x2k", "podinfo")).toBe("7d9f-x2k");
    expect(podLabel("other", "podinfo")).toBe("other");
  });

  it("lists containers and explains truncation", () => {
    expect(
      containersOf([
        { name: "a", containers: ["app", "proxy"], status: "ready" },
        { name: "b", containers: ["app"], status: "ready" },
      ]),
    ).toEqual(["app", "proxy"]);
    expect(truncationNotice(20, 20)).toBeUndefined();
    expect(truncationNotice(34, 20)).toMatch(/newest 20 of 34 pods/);
  });

  it("copies as text with prefixes and marker labels", () => {
    const lines = fill([e("web-1", "hello", 1), e("web-1", "pod deleted", undefined, { marker: "ended" })]);
    expect(toText(lines)).toBe("web-1/app hello\nweb-1/app [ended] pod deleted");
  });
});
