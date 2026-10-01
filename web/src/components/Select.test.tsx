import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { Select, type SelectOption, stepIndex, typeaheadIndex } from "./Select";

const OPTIONS: SelectOption<string>[] = [
  { value: "app", label: "app" },
  { value: "proxy", label: "proxy", disabled: true },
  { value: "metrics", label: "metrics", meta: "sidecar" },
  { value: "migrate", label: "migrate" },
];

function Harness() {
  const [v, setV] = useState("app");
  return (
    <>
      <Select label="Container" value={v} onChange={setV} options={OPTIONS} />
      <output>{v}</output>
    </>
  );
}

const trigger = () => screen.getByRole("button", { name: "Container" });

describe("Select", () => {
  it("opens below the button with the selected option active, and picks with Enter", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    trigger().focus();
    await user.keyboard("{ArrowDown}");
    const list = screen.getByRole("listbox", { name: "Container" });
    expect(document.activeElement).toBe(list);
    expect(screen.getByRole("option", { name: /app/ })).toHaveAttribute("aria-selected", "true");
    // Down skips the disabled option.
    await user.keyboard("{ArrowDown}");
    expect(list.getAttribute("aria-activedescendant")).toBe(
      screen.getByRole("option", { name: /metrics/ }).id,
    );
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("metrics");
    expect(document.activeElement).toBe(trigger());
  });

  it("jumps with Home, End and typeahead, and Esc closes without picking", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());
    const list = screen.getByRole("listbox");
    const active = () => list.getAttribute("aria-activedescendant");
    await user.keyboard("{End}");
    expect(active()).toBe(screen.getByRole("option", { name: /migrate/ }).id);
    await user.keyboard("{Home}");
    expect(active()).toBe(screen.getByRole("option", { name: /^app/ }).id);
    await user.keyboard("me");
    expect(active()).toBe(screen.getByRole("option", { name: /metrics/ }).id);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("app");
    expect(document.activeElement).toBe(trigger());
  });

  it("marks the selected option and shows meta text", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());
    expect(screen.getByRole("option", { name: /metrics/ })).toHaveTextContent("sidecar");
    await user.click(screen.getByRole("option", { name: /migrate/ }));
    expect(screen.getByRole("status")).toHaveTextContent("migrate");
  });

  it("steps and types ahead over enabled options only", () => {
    expect(stepIndex(OPTIONS, 0, 1)).toBe(2);
    expect(stepIndex(OPTIONS, 2, -1)).toBe(0);
    expect(stepIndex(OPTIONS, 3, 1)).toBe(3);
    expect(typeaheadIndex(OPTIONS, 0, "m")).toBe(2);
    expect(typeaheadIndex(OPTIONS, 2, "m")).toBe(3);
    expect(typeaheadIndex(OPTIONS, 3, "mm")).toBe(2);
    expect(typeaheadIndex(OPTIONS, 0, "p")).toBe(0);
  });
});
