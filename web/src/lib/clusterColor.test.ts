import { describe, expect, it } from "vitest";
import {
  autoColor,
  checkColor,
  clusterColor,
  contrastRatio,
  darkModeShade,
  hashName,
  initials,
  oklchToHex,
} from "./clusterColor";
import { CLUSTER_PALETTE } from "./onboarding";

describe("autoColor", () => {
  it("is stable for a name and differs between names", () => {
    expect(autoColor("prod-eu")).toBe(autoColor("prod-eu"));
    expect(autoColor("prod-eu")).not.toBe(autoColor("prod-us"));
    expect(autoColor("prod-eu")).toMatch(/^#[0-9a-f]{6}$/);
    expect(hashName("staging")).toBe(hashName("staging"));
  });

  it("keeps white initials readable and the dark-mode shade usable, whatever the hue", () => {
    for (let i = 0; i < 200; i++) {
      const c = checkColor(autoColor(`cluster-${i}`));
      expect(c.tileLight, `cluster-${i}`).toBeGreaterThanOrEqual(4.5);
      expect(c.surfaceDark, `cluster-${i}`).toBeGreaterThanOrEqual(3);
      expect(c.warnings, `cluster-${i}`).toEqual([]);
    }
  });

  it("is what clusters without a stored colour use", () => {
    expect(clusterColor({ name: "dev", order: 3 })).toBe(autoColor("dev"));
    expect(clusterColor({ name: "dev", order: 3, color: "#0f766e" })).toBe("#0f766e");
  });
});

describe("oklchToHex", () => {
  it("brings out-of-gamut colours into sRGB by lowering chroma", () => {
    expect(oklchToHex(0.53, 0.4, 145)).toMatch(/^#[0-9a-f]{6}$/);
    expect(oklchToHex(1, 0, 0)).toBe("#ffffff");
    expect(oklchToHex(0, 0, 0)).toBe("#000000");
  });
});

describe("checkColor", () => {
  it("computes WCAG contrast", () => {
    expect(contrastRatio("#000000", "#ffffff")).toBeCloseTo(21, 0);
    expect(contrastRatio("#777777", "#777777")).toBeCloseTo(1, 5);
  });

  it("passes the curated swatches", () => {
    for (const p of CLUSTER_PALETTE) expect(checkColor(p.hex).tileLight, p.name).toBeGreaterThanOrEqual(4.5);
  });

  it("warns about colours too light for white initials, or too dark for dark mode", () => {
    expect(checkColor("#fde68a").warnings.join(" ")).toMatch(/light mode/);
    expect(checkColor("#101010").warnings.join(" ")).toMatch(/dark/);
    expect(checkColor("#6d28d9").warnings).toEqual([]);
  });

  it("lifts colours towards white for dark mode", () => {
    expect(contrastRatio(darkModeShade("#6d28d9"), "#ffffff")).toBeLessThan(
      contrastRatio("#6d28d9", "#ffffff"),
    );
  });
});

describe("initials", () => {
  it.each([
    ["prod-eu", "PE"],
    ["prod-us", "PU"],
    ["staging", "ST"],
    ["dev", "DE"],
    ["Production EU", "PE"],
    ["edge_ap.1", "EA"],
    ["x", "X"],
    ["", "?"],
  ])("%s → %s", (name, want) => {
    expect(initials(name)).toBe(want);
  });
});
