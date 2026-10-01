import { beforeEach, describe, expect, it } from "vitest";
import {
  getViewPrefs,
  mergeRemoteView,
  parseViewPrefs,
  reloadViewPrefs,
  resolvePref,
  setViewPrefs,
  VIEW_STORAGE_KEY,
} from "./viewPrefs";

beforeEach(() => {
  localStorage.clear();
  reloadViewPrefs();
});

describe("view preferences", () => {
  it("resolves URL > saved > default", () => {
    expect(resolvePref("flat", "grouped", "grouped")).toBe("flat");
    expect(resolvePref(undefined, "flat", "grouped")).toBe("flat");
    expect(resolvePref(undefined, undefined, "grouped")).toBe("grouped");
  });

  it("keeps only well-formed fields", () => {
    expect(
      parseViewPrefs({
        listView: "flat",
        logFormat: "nope",
        logTail: 9e9,
        pane: "ai",
        nav: { flux: true, x: 1 },
        at: 5,
      }),
    ).toEqual({ listView: "flat", pane: "ai", nav: { flux: true }, at: 5 });
    expect(parseViewPrefs("junk")).toEqual({});
  });

  it("saves to localStorage at once, so the next page load starts with it", () => {
    setViewPrefs({ listView: "flat" }, 10);
    expect(JSON.parse(localStorage.getItem(VIEW_STORAGE_KEY) ?? "{}")).toMatchObject({
      listView: "flat",
      at: 10,
    });
    reloadViewPrefs();
    expect(getViewPrefs().listView).toBe("flat");
  });

  it("takes the hub's copy only when it is newer", () => {
    setViewPrefs({ listView: "flat" }, 10);
    expect(mergeRemoteView({ listView: "grouped", at: 5 })).toBe(true);
    expect(getViewPrefs().listView).toBe("flat");
    expect(mergeRemoteView({ listView: "grouped", at: 20 })).toBe(false);
    expect(getViewPrefs().listView).toBe("grouped");
  });
});
