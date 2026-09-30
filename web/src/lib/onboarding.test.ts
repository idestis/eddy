import { describe, expect, it } from "vitest";
import type { ConnectionCheck, ConnectionInfo } from "../api/types";
import {
  CLUSTER_PALETTE,
  coreReady,
  expiresIn,
  isConnected,
  labelError,
  nameError,
  paletteEntry,
  reasonLabel,
  suggestName,
} from "./onboarding";

describe("nameError", () => {
  it.each(["a", "prod-eu", "edge-ap-2", "0", "a".repeat(63)])("accepts %s", (name) => {
    expect(nameError(name)).toBeUndefined();
  });

  it.each([
    ["", "required"],
    ["Prod", "lower-case"],
    ["-prod", "Start and end"],
    ["prod-", "Start and end"],
    ["prod_eu", "hyphens"],
    ["prod.eu", "hyphens"],
    ["a".repeat(64), "63"],
  ])("rejects %j", (name, why) => {
    expect(nameError(name)).toContain(why);
  });
});

describe("labelError", () => {
  it("accepts empty and ordinary text", () => {
    expect(labelError("")).toBeUndefined();
    expect(labelError("Production, eu-central-1 ✓")).toBeUndefined();
  });

  it("rejects long and control-character text", () => {
    expect(labelError("x".repeat(64))).toContain("63");
    expect(labelError("a\tb")).toContain("printable");
  });
});

describe("suggestName", () => {
  it("turns a display name into a DNS label", () => {
    expect(suggestName("Prod EU (2)")).toBe("prod-eu-2");
    expect(suggestName("  --Edge__AP-- ")).toBe("edge-ap");
    expect(nameError(suggestName(`${"x".repeat(62)} y`))).toBeUndefined();
  });
});

describe("reasonLabel", () => {
  it("words every known reason and passes unknown ones through", () => {
    expect(reasonLabel("join_used")).toBe("Join token already used");
    expect(reasonLabel("wrong_cluster")).toBe("Token belongs to another cluster");
    expect(reasonLabel("something_new")).toBe("something new");
  });
});

describe("CLUSTER_PALETTE", () => {
  it("mirrors the cluster colour tokens in design/tokens.css", async () => {
    // Node's fs, typed by hand: the app's tsconfig carries no Node types.
    const fs = (await import(/* @vite-ignore */ "node:fs" as string)) as {
      readFileSync: (path: string, encoding: "utf8") => string;
    };
    const css = fs.readFileSync("../design/tokens.css", "utf8"); // vitest runs in web/
    expect(css).toContain("--color-cluster-1");
    for (const p of CLUSTER_PALETTE) {
      const m = css.match(new RegExp(`--color-${p.token}:\\s*(#[0-9a-fA-F]{6});`));
      expect(m?.[1]?.toLowerCase(), p.token).toBe(p.hex);
      expect(p.swatch).toBe(`bg-${p.token}`);
    }
  });

  it("finds entries whatever the hex case", () => {
    expect(paletteEntry("#6D28D9")?.token).toBe("cluster-1");
    expect(paletteEntry("#123456")).toBeUndefined();
    expect(paletteEntry(undefined)).toBeUndefined();
  });
});

const info = (checks: Array<Pick<ConnectionCheck, "id" | "state">>) =>
  ({ checks: checks.map((c) => ({ ...c, label: c.id })) }) as ConnectionInfo;

describe("coreReady and isConnected", () => {
  it("waits for connected, protocol and informers", () => {
    const pending = info([
      { id: "connected", state: "ok" },
      { id: "protocol", state: "ok" },
      { id: "informers", state: "pending" },
      { id: "impersonation", state: "warn" },
    ]);
    expect(isConnected(pending)).toBe(true);
    expect(coreReady(pending)).toBe(false);
    const done = info([
      { id: "connected", state: "ok" },
      { id: "protocol", state: "ok" },
      { id: "informers", state: "ok" },
      { id: "impersonation", state: "warn" },
    ]);
    expect(coreReady(done)).toBe(true);
    expect(coreReady(undefined)).toBe(false);
    expect(isConnected(info([{ id: "connected", state: "pending" }]))).toBe(false);
  });
});

describe("expiresIn", () => {
  const now = Date.parse("2026-09-30T12:00:00Z");
  it("formats the time left", () => {
    expect(expiresIn("2026-09-30T12:59:00Z", now)).toBe("in 59m");
    expect(expiresIn("2026-09-30T13:00:00Z", now)).toBe("in 1h");
    expect(expiresIn("2026-09-30T14:30:00Z", now)).toBe("in 2h 30m");
    expect(expiresIn("2026-09-30T11:00:00Z", now)).toBe("expired");
  });
});
