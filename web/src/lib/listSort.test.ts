import { beforeEach, describe, expect, it } from "vitest";
import { resource } from "../test/fixtures";
import {
  comparator,
  decodeSort,
  encodeSort,
  hubSort,
  hubSortable,
  type ListSort,
  nextSort,
  readyRatio,
  resolveSort,
  sortPageKey,
  sortResources,
} from "./listSort";
import { buildRows } from "./resourceRows";
import {
  getViewPrefs,
  parseViewPrefs,
  reloadViewPrefs,
  savedSort,
  setSavedSort,
  VIEW_STORAGE_KEY,
} from "./viewPrefs";

const names = (rs: readonly { name: string }[]) => rs.map((r) => r.name);
const sorted = (items: ReturnType<typeof resource>[], sort: ListSort) => names(sortResources(items, sort));

describe("comparator", () => {
  it("sorts by name, then namespace", () => {
    const items = [
      resource("b", { namespace: "x" }),
      resource("a", { namespace: "z" }),
      resource("a", { namespace: "y" }),
    ];
    const asc = sortResources(items, { key: "name", order: "asc" });
    expect(asc.map((r) => `${r.namespace}/${r.name}`)).toEqual(["y/a", "z/a", "x/b"]);
    const desc = sortResources(items, { key: "name", order: "desc" });
    expect(desc.map((r) => r.name)).toEqual(["b", "a", "a"]);
    // Ties stay namespace-ascending in both directions.
    expect(desc.slice(1).map((r) => r.namespace)).toEqual(["y", "z"]);
  });

  it("sorts by kind", () => {
    const items = [
      resource("p", { kind: "Pod" }),
      resource("d", { kind: "Deployment" }),
      resource("a", { kind: "Deployment", namespace: "a" }),
    ];
    expect(sorted(items, { key: "kind", order: "asc" })).toEqual(["a", "d", "p"]);
    expect(sorted(items, { key: "kind", order: "desc" })).toEqual(["p", "a", "d"]);
  });

  it("ranks status: failed, reconciling, suspended, unknown, ready, completed, inventory last", () => {
    const items = [
      resource("inv", { status: "ready", inventoryOnly: true }),
      resource("completed", { status: "completed" }),
      resource("ready", { status: "ready" }),
      resource("unknown", { status: "unknown" }),
      resource("suspended", { status: "suspended" }),
      resource("reconciling", { status: "reconciling" }),
      resource("failed", { status: "failed" }),
    ];
    const order = ["failed", "reconciling", "suspended", "unknown", "ready", "completed", "inv"];
    expect(sorted(items, { key: "status", order: "asc" })).toEqual(order);
    expect(sorted(items, { key: "status", order: "desc" })).toEqual([...order].reverse());
  });

  it("sorts replicas by ready ratio, then total, with rows lacking a value last", () => {
    const items = [
      resource("none", { kind: "Service" }),
      resource("full-big", { replicas: "10/10" }),
      resource("half", { replicas: "1/2" }),
      resource("full-small", { replicas: "1/1" }),
      resource("zero", { replicas: "0/3" }),
      resource("job", { completions: "1/2" }),
    ];
    expect(sorted(items, { key: "ready", order: "asc" })).toEqual([
      "zero",
      "half",
      "job",
      "full-small",
      "full-big",
      "none",
    ]);
    expect(sorted(items, { key: "ready", order: "desc" })).toEqual([
      "full-big",
      "full-small",
      "half",
      "job",
      "zero",
      "none",
    ]);
    expect(readyRatio(resource("x", { replicas: "2/3" }))).toEqual([2, 3]);
    expect(readyRatio(resource("x"))).toBeUndefined();
  });

  it("sorts messages alphabetically with empty ones last", () => {
    const items = [
      resource("empty"),
      resource("b", { message: "beta" }),
      resource("a", { message: "Alpha" }),
      resource("inv", { message: "ignored", inventoryOnly: true }),
    ];
    expect(sorted(items, { key: "message", order: "asc" })).toEqual(["a", "b", "empty", "inv"]);
    expect(sorted(items, { key: "message", order: "desc" })).toEqual(["b", "a", "empty", "inv"]);
  });

  it("sorts by version (revision, image or chart)", () => {
    const items = [
      resource("none"),
      resource("img", { kind: "Deployment", images: ["ghcr.io/x/app:2.0.0"] }),
      resource("chart", { chart: "podinfo-6.7.1" }),
    ];
    expect(sorted(items, { key: "version", order: "asc" })).toEqual(["img", "chart", "none"]);
    expect(sorted(items, { key: "version", order: "desc" })).toEqual(["chart", "img", "none"]);
  });

  it("sorts age with the youngest first when ascending", () => {
    const items = [
      resource("old", { createdAt: "2026-01-01T00:00:00Z" }),
      resource("none"),
      resource("new", { createdAt: "2026-09-01T00:00:00Z" }),
    ];
    expect(sorted(items, { key: "age", order: "asc" })).toEqual(["new", "old", "none"]);
    expect(sorted(items, { key: "age", order: "desc" })).toEqual(["old", "new", "none"]);
  });

  it("breaks ties by namespace and name", () => {
    const items = [
      resource("b", { status: "failed", namespace: "n" }),
      resource("a", { status: "failed", namespace: "n" }),
      resource("c", { status: "failed", namespace: "m" }),
    ];
    expect(items.sort(comparator({ key: "status", order: "asc" })).map((r) => r.name)).toEqual([
      "c",
      "a",
      "b",
    ]);
  });
});

describe("buildRows with a sort", () => {
  const items = [resource("b", { status: "failed" }), resource("a", { status: "ready" })];

  it("sorts a flat list and ignores the sort when grouped", () => {
    const flat = buildRows(items, false, { key: "name", order: "asc" });
    expect(flat.map((r) => r.key.split("/").pop())).toEqual(["a", "b"]);
    const grouped = buildRows(items, true, { key: "name", order: "asc" });
    expect(grouped.filter((r) => r.type === "resource").map((r) => r.key.split("/").pop())).toEqual([
      "b",
      "a",
    ]);
  });

  it("keeps the attention-first order without a sort", () => {
    expect(buildRows(items, false).map((r) => r.key.split("/").pop())).toEqual(["b", "a"]);
  });
});

describe("click cycle", () => {
  it("goes ascending, descending, then back to the default", () => {
    const a = nextSort(undefined, "name");
    expect(a).toEqual({ key: "name", order: "asc" });
    const d = nextSort(a, "name");
    expect(d).toEqual({ key: "name", order: "desc" });
    expect(nextSort(d, "name")).toBeUndefined();
  });

  it("starts a new column ascending, whatever the last one was", () => {
    expect(nextSort({ key: "name", order: "desc" }, "age")).toEqual({ key: "age", order: "asc" });
  });
});

describe("saved sort and URL precedence", () => {
  beforeEach(() => {
    localStorage.clear();
    reloadViewPrefs();
  });

  it("lets the URL win over the saved sort", () => {
    const saved: ListSort = { key: "age", order: "desc" };
    expect(resolveSort({ sort: "name", order: "desc" }, saved)).toEqual({ key: "name", order: "desc" });
    expect(resolveSort({ sort: "name" }, saved)).toEqual({ key: "name", order: "asc" });
    expect(resolveSort({}, saved)).toEqual(saved);
    expect(resolveSort({}, undefined)).toBeUndefined();
  });

  it("saves per page and forgets on the third click", () => {
    expect(sortPageKey(undefined)).toBe("");
    expect(sortPageKey("Deployment")).toBe("Deployment");
    setSavedSort("Deployment", { key: "ready", order: "asc" });
    setSavedSort("", { key: "name", order: "desc" });
    expect(savedSort(getViewPrefs(), "Deployment")).toEqual({ key: "ready", order: "asc" });
    expect(savedSort(getViewPrefs(), "")).toEqual({ key: "name", order: "desc" });
    expect(JSON.parse(localStorage.getItem(VIEW_STORAGE_KEY) ?? "{}").listSort).toEqual({
      Deployment: "ready:asc",
      "": "name:desc",
    });
    setSavedSort("Deployment", undefined);
    setSavedSort("", undefined);
    expect(getViewPrefs().listSort).toBeUndefined();
  });

  it("drops malformed saved sorts", () => {
    expect(
      parseViewPrefs({ listSort: { a: "name:asc", b: "name:up", c: "nope:asc", d: 3, e: "name:asc:x" } }),
    ).toEqual({ listSort: { a: "name:asc" } });
    expect(parseViewPrefs({ listSort: { b: "bad" } })).toEqual({});
    expect(decodeSort(encodeSort({ key: "version", order: "desc" }))).toEqual({
      key: "version",
      order: "desc",
    });
  });
});

describe("windowed lists", () => {
  it("maps name, kind, status and age to the hub's sort and order", () => {
    expect(hubSort({ key: "name", order: "desc" })).toEqual({ sort: "name", order: "desc" });
    expect(hubSort({ key: "kind", order: "asc" })).toEqual({ sort: "kind", order: "asc" });
    expect(hubSort({ key: "status", order: "asc" })).toEqual({ sort: "status", order: "asc" });
    expect(hubSort({ key: "age", order: "asc" })).toEqual({ sort: "age", order: "asc" });
  });

  it("falls back to the hub's kind order by default and for columns it cannot sort", () => {
    expect(hubSort(undefined)).toEqual({ sort: "kind" });
    expect(hubSort({ key: "message", order: "asc" })).toEqual({ sort: "kind" });
  });

  it("disables replicas, message and version", () => {
    for (const k of ["ready", "message", "version"] as const) expect(hubSortable(k)).toBe(false);
    for (const k of ["name", "kind", "status", "age"] as const) expect(hubSortable(k)).toBe(true);
  });
});
