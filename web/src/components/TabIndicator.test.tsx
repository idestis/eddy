import { act, render } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { TabIndicator, useTabIndicator } from "./TabIndicator";

// jsdom has no layout: give each tab an offset and a width.
function place(el: HTMLElement | null, left: number, width: number) {
  if (!el) return;
  Object.defineProperty(el, "offsetLeft", { configurable: true, value: left });
  Object.defineProperty(el, "offsetWidth", { configurable: true, value: width });
}

let select: (v: string) => void = () => {};

function Tabs() {
  const [tab, setTab] = useState("overview");
  select = setTab;
  const { list, indicator } = useTabIndicator(tab);
  return (
    <div ref={list} role="tablist">
      <TabIndicator ref={indicator} variant="underline" />
      {["overview", "yaml", "events"].map((t, i) => (
        <button
          key={t}
          type="button"
          role="tab"
          aria-selected={t === tab}
          ref={(el) => place(el, i * 100, 60 + i * 10)}
        >
          {t}
        </button>
      ))}
    </div>
  );
}

describe("useTabIndicator", () => {
  it("moves one indicator to the selected tab with a transform and a width", () => {
    const { container } = render(<Tabs />);
    const ind = container.querySelector<HTMLElement>(".tab-ind");
    expect(ind?.style.transform).toBe("translateX(0px)");
    expect(ind?.style.width).toBe("60px");
    act(() => select("events"));
    expect(ind?.style.transform).toBe("translateX(200px)");
    expect(ind?.style.width).toBe("80px");
    expect(ind?.getAttribute("aria-hidden")).toBe("true");
  });
});
