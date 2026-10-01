import { describe, expect, it } from "vitest";
import { indicatorBox } from "./motion";

describe("indicatorBox", () => {
  it("sits under the active tab, relative to the list", () => {
    expect(indicatorBox({ offsetLeft: 96.4, offsetWidth: 71.6 })).toEqual({
      x: 96,
      width: 72,
      visible: true,
    });
  });

  it("shortens an underline by the inset at both ends", () => {
    expect(indicatorBox({ offsetLeft: 100, offsetWidth: 80 }, 10)).toEqual({
      x: 110,
      width: 60,
      visible: true,
    });
  });

  it("hides without an active tab or when the tab has no width yet", () => {
    expect(indicatorBox(null).visible).toBe(false);
    expect(indicatorBox({ offsetLeft: 10, offsetWidth: 0 }).visible).toBe(false);
    expect(indicatorBox({ offsetLeft: 10, offsetWidth: 8 }, 5).visible).toBe(false);
  });
});
