import { describe, expect, it } from "vitest";
import { claudeCodePrompt, threadBody, threadTitle } from "./answerThread";

describe("answer threads", () => {
  it("titles with the first 80 characters of the question", () => {
    expect(threadTitle("  why is\nit   failing? ")).toBe("why is it failing?");
    expect(threadTitle("x".repeat(200))).toHaveLength(80);
  });

  it("composes question, marked AI answer and no context", () => {
    expect(
      threadBody({ question: "why?\nreally", answer: "Because.", model: "claude-x", provider: "bedrock" }),
    ).toBe("**Question**\n\n> why?\n> really\n\n**AI answer · claude-x via bedrock**\n\nBecause.");
  });

  it("includes a YAML attachment as a fenced block, longer than any fence inside", () => {
    const body = threadBody({
      question: "q",
      answer: "a",
      attachment: {
        attachment: { kind: "yaml", source: "apps/Deployment/ns/web", lines: ["a: |", "  ```", "b: 1"] },
        total: 3,
      },
    });
    expect(body).toContain("Context: YAML of apps/Deployment/ns/web\n\n````yaml\na: |\n  ```\nb: 1\n````");
  });

  it("never copies log lines, only a reference", () => {
    const body = threadBody({
      question: "q",
      answer: "a",
      attachment: {
        attachment: { kind: "logs", source: "prefect/cryptic-mustang-9kws4", lines: ["password=hunter2"] },
        total: 42,
      },
    });
    expect(body).toContain("Context: 42 log lines from prefect/cryptic-mustang-9kws4 (not copied)");
    expect(body).not.toContain("hunter2");
  });

  it("builds the Claude Code prompt", () => {
    const p = claudeCodePrompt("th_7", "staging", {
      kind: "Kustomization",
      namespace: "flux-system",
      name: "apps",
    });
    expect(p).toContain("Read thread th_7 with get_thread");
    expect(p).toContain("for staging Kustomization flux-system/apps");
    expect(p).toContain("(do not change the cluster directly)");
    expect(claudeCodePrompt("t", "c", { kind: "Namespace", namespace: "", name: "x" })).toContain(
      "c Namespace x.",
    );
  });
});
