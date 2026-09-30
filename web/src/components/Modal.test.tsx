import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Modal } from "./Modal";

describe("Modal", () => {
  it("renders through a portal into document.body, outside a transformed ancestor", () => {
    const { container } = render(
      <div style={{ transform: "translateZ(0)", overflow: "hidden", height: 130 }}>
        <Modal label="Test dialog" onClose={() => {}}>
          <button type="button">Inside</button>
        </Modal>
      </div>,
    );
    const dialog = screen.getByRole("dialog", { name: "Test dialog" });
    const scrim = dialog.parentElement;
    expect(container.contains(dialog)).toBe(false);
    expect(scrim?.parentElement).toBe(document.body);
    // The scrim is fixed to the viewport, not to the 130px box it was declared in.
    expect(scrim?.className).toContain("fixed");
    expect(scrim?.className).toContain("inset-0");
  });

  it("closes on Escape and on a click on the scrim, and restores focus", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const trigger = document.createElement("button");
    document.body.append(trigger);
    trigger.focus();
    const { unmount } = render(
      <Modal label="Closable" onClose={onClose}>
        <input aria-label="field" />
      </Modal>,
    );
    expect(screen.getByLabelText("field")).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(onClose).toHaveBeenCalledTimes(1);
    const scrim = screen.getByRole("dialog").parentElement;
    if (scrim) await user.pointer({ keys: "[MouseLeft]", target: scrim });
    expect(onClose).toHaveBeenCalledTimes(2);
    unmount();
    expect(trigger).toHaveFocus();
    trigger.remove();
  });

  it("anchors a popover to its trigger", () => {
    const anchor = document.createElement("button");
    document.body.append(anchor);
    render(
      <Modal label="Menu" onClose={() => {}} placement="anchor" anchor={anchor}>
        <span>rows</span>
      </Modal>,
    );
    const dialog = screen.getByRole("dialog", { name: "Menu" });
    expect(dialog.style.position).toBe("absolute");
    anchor.remove();
  });
});
