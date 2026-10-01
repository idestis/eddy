// The shared ranking fixture of ADR-0006: testdata/search_cases.json at the repo root.
// internal/hub/fuzzy_test.go runs the same cases against the Go port, so the palette's local
// ranking and GET /api/v1/search cannot drift apart.

import { describe, expect, it } from "vitest";
import { matchField, matchItem, rank } from "./fuzzy";

type Range = [number, number];

interface SearchCases {
  fieldCases: Array<{
    term: string;
    text: string;
    primary: boolean;
    match: { score: number; ranges: Range[] } | null;
  }>;
  itemCases: Array<{
    query: string;
    primary: string;
    secondary: string[];
    match: { score: number; primary: Range[]; secondary: Range[][] } | null;
  }>;
  rankItems: Array<{ id: string; primary: string; secondary: string[]; order: number }>;
  rankCases: Array<{ query: string; limit: number; expected: string[] }>;
}

// Node's fs, typed by hand: the app's tsconfig carries no Node types.
const fs = (await import(/* @vite-ignore */ "node:fs" as string)) as {
  readFileSync: (path: string, encoding: "utf8") => string;
};
const cases = JSON.parse(fs.readFileSync("../testdata/search_cases.json", "utf8")) as SearchCases; // vitest runs in web/

describe("testdata/search_cases.json", () => {
  it("has cases in every section", () => {
    expect(cases.fieldCases.length).toBeGreaterThan(0);
    expect(cases.itemCases.length).toBeGreaterThan(0);
    expect(cases.rankItems.length).toBeGreaterThan(0);
    expect(cases.rankCases.length).toBeGreaterThan(0);
  });

  it.each(
    cases.fieldCases.map((c) => [`${c.term} in ${c.text}${c.primary ? "" : " (secondary)"}`, c] as const),
  )("matchField: %s", (_, c) => {
    const got = matchField(c.term, c.text, c.primary);
    if (!c.match) return expect(got).toBeNull();
    expect(got?.score).toBeCloseTo(c.match.score, 9);
    expect(got?.ranges).toEqual(c.match.ranges);
  });

  it.each(cases.itemCases.map((c) => [JSON.stringify(c.query), c] as const))("matchItem: %s", (_, c) => {
    const got = matchItem(c.query, { primary: c.primary, secondary: c.secondary });
    if (!c.match) return expect(got).toBeNull();
    expect(got?.score).toBeCloseTo(c.match.score, 9);
    expect(got?.primary).toEqual(c.match.primary);
    expect(got?.secondary).toEqual(c.match.secondary);
  });

  // The same order the Go side asserts: tier, then `order` (failing first, then the current
  // cluster), then the finer score.
  it.each(cases.rankCases.map((c) => [`${JSON.stringify(c.query)} limit ${c.limit}`, c] as const))(
    "rank: %s",
    (_, c) => {
      const got = rank(
        c.query,
        cases.rankItems,
        (i) => ({ primary: i.primary, secondary: i.secondary }),
        c.limit,
        (a, b) => a.order - b.order,
      );
      expect(got.map((r) => r.item.id)).toEqual(c.expected);
    },
  );
});
