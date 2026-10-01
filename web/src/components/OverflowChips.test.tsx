import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { computeOverflow, OverflowChips, type OverflowItem } from "./OverflowChips";

describe("computeOverflow", () => {
  const none = [false, false, false, false];
  it("shows everything when it fits", () => {
    expect(computeOverflow([50, 50, 50], [false, false, false], 30, 160, 5)).toEqual({
      mode: "all",
      visible: [0, 1, 2],
      hidden: [],
    });
  });

  it("fills the line in order and leaves a slot for +N", () => {
    // plus 30; a chip costs width+gap: 30 + 55 + 55 = 140 fits, a third (195) does not.
    expect(computeOverflow([50, 50, 50, 50], none, 30, 150, 5)).toEqual({
      mode: "some",
      visible: [0, 1],
      hidden: [2, 3],
    });
  });

  it("never hides an active chip: it takes a slot from the later ones", () => {
    const r = computeOverflow([50, 50, 50, 50], [false, false, false, true], 30, 150, 5);
    expect(r.visible).toEqual([0, 3]);
    expect(r.hidden).toEqual([1, 2]);
  });

  it("keeps several active chips even past the width", () => {
    const r = computeOverflow([50, 50, 50, 50], [false, true, false, true], 30, 150, 5);
    expect(r.visible).toEqual([1, 3]);
  });

  it("collapses to one dropdown when not even one chip and +N fit", () => {
    expect(computeOverflow([50, 60], [true, false], 30, 80, 5)).toEqual({
      mode: "collapsed",
      visible: [],
      hidden: [0, 1],
    });
  });

  it("has nothing to hide for an empty row", () => {
    expect(computeOverflow([], [], 30, 0, 5).mode).toBe("all");
  });
});

const KEYS = ["attention", "failed", "reconciling", "suspended"];

function Row({ initial = "" }: { initial?: string }) {
  const [on, setOn] = useState(initial);
  const items: OverflowItem[] = KEYS.map((k) => ({
    key: k,
    text: k,
    active: on === k,
    option: <span>{k}</span>,
    meta: 3,
    chip: (
      <button type="button" aria-pressed={on === k} onClick={() => setOn(on === k ? "" : k)}>
        {k} 3
      </button>
    ),
  }));
  return (
    <>
      <OverflowChips
        label="Status"
        items={items}
        chipClass="chip"
        onToggle={(k) => setOn(on === k ? "" : k)}
        summary={(a) => a[0]?.text ?? "Status"}
      />
      <output>{on}</output>
    </>
  );
}

// jsdom has no layout: give the chips 100px, the +N 40px and the row `rowWidth`.
let rowWidth = 350;
beforeEach(() => {
  rowWidth = 350;
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockImplementation(function (this: HTMLElement) {
    if (this.matches(".chip")) return 40;
    return this.closest("[aria-hidden=true]") ? 100 : 0;
  });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute("data-overflow-root") ? rowWidth : 0;
  });
});
afterEach(() => vi.restoreAllMocks());

describe("OverflowChips", () => {
  it("shows what fits and opens the rest from +N, toggling the same state", async () => {
    const user = userEvent.setup();
    render(<Row />);
    // 3 chips would need 100*3 + 3*6 + 40 > 350, so two chips + "+2".
    expect(screen.getAllByRole("button", { pressed: false }).map((b) => b.textContent)).toEqual([
      "attention 3",
      "failed 3",
    ]);
    const more = screen.getByRole("button", { name: "More status" });
    expect(more).toHaveTextContent("+2");
    await user.click(more);
    const list = screen.getByRole("listbox", { name: "More status" });
    expect(
      within(list)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["reconciling3", "suspended3"]);
    await user.click(within(list).getByRole("option", { name: /suspended/ }));
    expect(screen.getByRole("status")).toHaveTextContent("suspended");
    // The active chip now sits in the row, in place of the last visible one.
    expect(screen.getByRole("button", { name: "suspended 3", pressed: true })).toBeVisible();
    expect(screen.queryByRole("listbox")).toBeNull();
  });

  it("works from the keyboard", async () => {
    const user = userEvent.setup();
    render(<Row />);
    screen.getByRole("button", { name: "More status" }).focus();
    await user.keyboard("{ArrowDown}{ArrowDown}{Enter}");
    expect(screen.getByRole("status")).toHaveTextContent("suspended");
    await user.click(screen.getByRole("button", { name: "More status" }));
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("button", { name: "More status" })).toHaveFocus();
  });

  it("keeps an active chip out of +N and marks +N with a dot only when one is inside", () => {
    render(<Row initial="suspended" />);
    expect(screen.getByRole("button", { name: "suspended 3", pressed: true })).toBeInTheDocument();
    expect(screen.queryByTestId("overflow-dot")).toBeNull();
  });

  it("collapses to a single Status dropdown on a very narrow row", async () => {
    rowWidth = 120;
    const user = userEvent.setup();
    render(<Row />);
    expect(screen.queryByRole("button", { name: "attention 3" })).toBeNull();
    const dd = screen.getByRole("button", { name: "Status" });
    expect(dd).toHaveTextContent("Status");
    await user.click(dd);
    expect(within(screen.getByRole("listbox")).getAllByRole("option")).toHaveLength(4);
    await user.click(screen.getByRole("option", { name: /failed/ }));
    expect(screen.getByRole("button", { name: "Status" })).toHaveTextContent("failed");
  });

  it("shows every chip when nothing can be measured", () => {
    vi.restoreAllMocks();
    render(<Row />);
    expect(screen.getAllByRole("button", { pressed: false })).toHaveLength(4);
  });
});
