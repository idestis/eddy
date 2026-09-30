import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { EnvironmentPicker } from "./EnvironmentPicker";

function Harness({ onValue = () => {}, initial = "" }: { onValue?: (v: string) => void; initial?: string }) {
  const [v, setV] = useState(initial);
  return (
    <EnvironmentPicker
      value={v}
      onChange={(x) => {
        setV(x);
        onValue(x);
      }}
    />
  );
}

const radio = (name: string) => screen.getByRole("radio", { name });

describe("EnvironmentPicker", () => {
  it("starts with nothing selected and only the first chip in the tab order", () => {
    render(<Harness />);
    expect(screen.getByRole("radiogroup", { name: "Environment" })).toBeInTheDocument();
    const radios = screen.getAllByRole("radio");
    expect(radios.map((r) => r.textContent)).toEqual([
      "Production",
      "Staging",
      "Development",
      "Edge",
      "Sandbox",
      "Other…",
    ]);
    expect(radios.every((r) => r.getAttribute("aria-checked") === "false")).toBe(true);
    expect(radios.map((r) => r.tabIndex)).toEqual([0, -1, -1, -1, -1, -1]);
    expect(screen.queryByRole("textbox")).toBeNull();
  });

  it("selects with a click and clears by clicking the checked chip", async () => {
    const onValue = vi.fn();
    render(<Harness onValue={onValue} />);
    await userEvent.click(radio("Edge"));
    expect(radio("Edge")).toHaveAttribute("aria-checked", "true");
    expect(radio("Edge").tabIndex).toBe(0);
    expect(radio("Production").tabIndex).toBe(-1);
    expect(onValue).toHaveBeenLastCalledWith("Edge");
    await userEvent.click(radio("Edge"));
    expect(radio("Edge")).toHaveAttribute("aria-checked", "false");
    expect(onValue).toHaveBeenLastCalledWith("");
  });

  it("moves and selects with the arrow keys, wrapping around", async () => {
    const onValue = vi.fn();
    render(<Harness onValue={onValue} />);
    await userEvent.tab();
    expect(radio("Production")).toHaveFocus();
    await userEvent.keyboard("{ArrowRight}");
    expect(radio("Staging")).toHaveFocus();
    expect(radio("Staging")).toHaveAttribute("aria-checked", "true");
    await userEvent.keyboard("{ArrowDown}{ArrowLeft}{ArrowLeft}");
    expect(radio("Production")).toHaveFocus();
    expect(onValue).toHaveBeenLastCalledWith("Production");
    await userEvent.keyboard("{ArrowLeft}");
    expect(radio("Other…")).toHaveFocus();
    expect(radio("Other…")).toHaveAttribute("aria-checked", "true");
    await userEvent.keyboard("{Home}");
    expect(radio("Production")).toHaveFocus();
    await userEvent.keyboard("{End}");
    expect(radio("Other…")).toHaveFocus();
  });

  it("reveals a text input for a custom value", async () => {
    const onValue = vi.fn();
    render(<Harness onValue={onValue} />);
    await userEvent.click(radio("Other…"));
    const input = screen.getByRole("textbox", { name: "Custom environment" });
    expect(input).toHaveFocus();
    await userEvent.type(input, "QA");
    expect(onValue).toHaveBeenLastCalledWith("QA");
    await userEvent.click(radio("Staging"));
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(onValue).toHaveBeenLastCalledWith("Staging");
  });

  it("opens on Other… when the value is not a preset", () => {
    render(<Harness initial="Lab" />);
    expect(radio("Other…")).toHaveAttribute("aria-checked", "true");
    expect(screen.getByRole("textbox", { name: "Custom environment" })).toHaveValue("Lab");
  });
});
