import { describe, expect, it } from "vitest";
import {
  ATTACH_MAX_BYTES,
  ATTACH_MAX_LINES,
  attachmentLabel,
  buildAttachment,
  buildYamlAttachment,
  lineText,
  sliceWithContext,
  trimNewest,
  trimOldest,
  yamlAncestors,
  yamlWithPath,
} from "./logAttachments";
import { appendEntries } from "./workloadLogs";

describe("log attachments", () => {
  it("keeps the pod/container prefix and drops markers", () => {
    const { lines } = appendEntries(
      [],
      [
        { pod: "web-1", container: "app", line: "boom" },
        { pod: "web-1", line: "forbidden", marker: "forbidden" },
        { pod: "", line: "3 lines dropped", marker: "dropped" },
        { pod: "web-2", line: "raw" },
      ],
      0,
    );
    const { attachment, total } = buildAttachment(lines, "apps/web · 2 pods");
    expect(attachment).toEqual({
      kind: "logs",
      source: "apps/web · 2 pods",
      lines: ["[web-1/app] boom", "[web-2] raw"],
    });
    expect(total).toBe(2);
    expect(lineText({ pod: "", line: "x" })).toBe("x");
  });

  it("trims to 500 lines and 32 KiB, newest first", () => {
    const many = Array.from({ length: 600 }, (_, i) => `line ${i}`);
    const kept = trimNewest(many);
    expect(kept).toHaveLength(ATTACH_MAX_LINES);
    expect(kept[kept.length - 1]).toBe("line 599");
    const big = Array.from({ length: 100 }, (_, i) => `${i}`.padEnd(1000, "x"));
    const bytes = trimNewest(big);
    expect(bytes.reduce((n, l) => n + l.length + 1, 0)).toBeLessThanOrEqual(ATTACH_MAX_BYTES);
    expect(bytes[bytes.length - 1]?.startsWith("99")).toBe(true);
  });

  it("adds context lines around a selection, within bounds", () => {
    const all = Array.from({ length: 50 }, (_, i) => i);
    expect(sliceWithContext(all, 10, 12, 20)).toEqual(all.slice(0, 33));
    expect(sliceWithContext(all, 45, 44, 20)).toEqual(all.slice(24, 50));
  });

  it("labels the composer chip", () => {
    const attachment = { kind: "logs" as const, source: "apps/web-1/app", lines: ["a", "b"] };
    expect(attachmentLabel({ attachment, total: 2 })).toBe("2 lines · apps/web-1/app");
    expect(attachmentLabel({ attachment, total: 900 })).toBe("last 2 lines attached · apps/web-1/app");
  });
});

describe("yaml attachments", () => {
  const doc = [
    "apiVersion: apps/v1",
    "kind: Deployment",
    "spec:",
    "  template:",
    "    spec:",
    "      containers:",
    "      - name: app",
    "        image: nginx",
    "        env:",
    "        - name: A",
    "          value: b",
    "      - name: side",
    "        image: busybox",
  ];

  it("finds the parent keys of a line, outermost first", () => {
    expect(yamlAncestors(doc, 7)).toEqual([
      "spec:",
      "  template:",
      "    spec:",
      "      containers:",
      "      - name: app",
    ]);
    expect(yamlAncestors(doc, 12)).toEqual([
      "spec:",
      "  template:",
      "    spec:",
      "      containers:",
      "      - name: side",
    ]);
    expect(yamlAncestors(doc, 1)).toEqual([]);
  });

  it("treats a list item as a child of the key at its own indent", () => {
    expect(yamlAncestors(doc, 11)).toEqual(["spec:", "  template:", "    spec:", "      containers:"]);
    expect(yamlAncestors(doc, 10).at(-1)).toBe("        - name: A");
  });

  it("puts the path above the selection", () => {
    expect(yamlWithPath(doc, 8, 7)).toEqual([
      ...yamlAncestors(doc, 7),
      "        image: nginx",
      "        env:",
    ]);
  });

  it("keeps the top of a long document", () => {
    const long = Array.from({ length: 700 }, (_, i) => `k${i}: v`);
    const { attachment, total } = buildYamlAttachment(long, "apps/Deployment/ns/web");
    expect(attachment.kind).toBe("yaml");
    expect(attachment.lines).toHaveLength(ATTACH_MAX_LINES);
    expect(attachment.lines[0]).toBe("k0: v");
    expect(total).toBe(700);
    expect(attachmentLabel({ attachment, total })).toBe("first 500 lines attached · apps/Deployment/ns/web");
    expect(trimOldest(["a".repeat(40_000)])).toEqual([]);
  });
});
