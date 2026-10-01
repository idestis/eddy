import { fireEvent, render, screen } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { appendEntries } from "../lib/workloadLogs";
import { LogLines } from "./LogLines";

const { lines } = appendEntries(
  [],
  Array.from({ length: 60 }, (_, i) => ({
    pod: `web-${i % 2}`,
    container: "app",
    line:
      i === 5
        ? '{"level":"error","msg":"cache write failed","error":"refused","stacktrace":"a.go:1\\n\\tb.go:2"}'
        : `line ${i}`,
    ts: new Date(Date.UTC(2026, 8, 30, 12, 0, i)).toISOString(),
  })),
  0,
);

// jsdom has no layout: the log viewport is 600px tall and each line 20px.
const realRect = HTMLElement.prototype.getBoundingClientRect;
const realHeight = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
const realWidth = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetWidth");
const height = (el: HTMLElement) => (el.dataset.index !== undefined ? 20 : 600);
beforeAll(() => {
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    return new DOMRect(0, 0, 800, height(this));
  };
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return height(this);
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", { configurable: true, get: () => 800 });
});
afterAll(() => {
  HTMLElement.prototype.getBoundingClientRect = realRect;
  if (realHeight) Object.defineProperty(HTMLElement.prototype, "offsetHeight", realHeight);
  if (realWidth) Object.defineProperty(HTMLElement.prototype, "offsetWidth", realWidth);
});

function setup(onAsk = vi.fn()) {
  render(
    <LogLines
      lines={lines}
      format="structured"
      query=""
      workload="web"
      showContainer={false}
      follow={false}
      setFollow={() => {}}
      unseen={0}
      onResume={() => {}}
      onAsk={onAsk}
      emptyText="none"
    />,
  );
  return onAsk;
}

const gutter = (n: number) => screen.getByRole("button", { name: `Select line ${n}` });

describe("LogLines", () => {
  it("selects a range from the gutter and hands it to Ask AI", () => {
    const onAsk = setup();
    fireEvent.click(gutter(2));
    fireEvent.click(gutter(4), { shiftKey: true });
    const bar = screen.getByRole("toolbar", { name: "3 selected lines" });
    expect(bar).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Ask AI about selection" }));
    expect(onAsk).toHaveBeenCalledWith(lines.slice(1, 4), "selection");
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
  });

  it("adds 20 lines of context on each side", () => {
    const onAsk = setup();
    fireEvent.click(gutter(30));
    fireEvent.click(screen.getByRole("button", { name: "Ask AI with context" }));
    expect(onAsk).toHaveBeenCalledWith(lines.slice(9, 50), "context");
  });

  it("extends the selection with Shift+arrows and hides it on Esc", () => {
    setup();
    fireEvent.click(gutter(10));
    const log = screen.getByRole("log");
    fireEvent.keyDown(log, { key: "ArrowDown", shiftKey: true });
    fireEvent.keyDown(log, { key: "ArrowDown", shiftKey: true });
    expect(screen.getByRole("toolbar", { name: "3 selected lines" })).toBeInTheDocument();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
  });

  it("renders structured lines with a level badge and expands every field", () => {
    setup();
    const msg = screen.getByText("cache write failed");
    expect(screen.getByText("ERR")).toBeInTheDocument();
    fireEvent.click(msg);
    expect(screen.getByText("stacktrace")).toBeInTheDocument();
    const trace = screen.getByText("stacktrace").nextElementSibling;
    expect(trace?.textContent).toBe("a.go:1\n\tb.go:2");
  });

  it("has no selection toolbar when Ask AI cannot take logs", () => {
    render(
      <LogLines
        lines={lines}
        format="raw"
        query=""
        showContainer={false}
        follow={false}
        setFollow={() => {}}
        unseen={0}
        onResume={() => {}}
        emptyText="none"
      />,
    );
    fireEvent.click(gutter(2));
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
  });
});
