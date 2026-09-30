import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "../api/client";
import { CANCELLED, type ConfirmRequest, withConfirm } from "../lib/confirm";
import { ConfirmDialog } from "./ConfirmDialog";

const req: ConfirmRequest = {
  title: "Suspend apps on prod-eu?",
  body: "…",
  expected: "prod-eu",
  action: "Suspend",
};
const confirmRequired = () => new ApiError(428, "confirm_required", "Type prod-eu to confirm.");

describe("withConfirm", () => {
  it("asks first on protected clusters and sends the typed name", async () => {
    const call = vi.fn().mockResolvedValue("ok");
    const ask = vi.fn().mockResolvedValue("prod-eu");
    await expect(withConfirm(call, ask, req, true)).resolves.toBe("ok");
    expect(ask).toHaveBeenCalledOnce();
    expect(call).toHaveBeenCalledWith("prod-eu");
  });

  it("does not call the hub when the user cancels", async () => {
    const call = vi.fn();
    await expect(withConfirm(call, vi.fn().mockResolvedValue(null), req, true)).resolves.toBe(CANCELLED);
    expect(call).not.toHaveBeenCalled();
  });

  it("retries once with the confirmation after a 428", async () => {
    const call = vi.fn().mockRejectedValueOnce(confirmRequired()).mockResolvedValueOnce("ok");
    const ask = vi.fn().mockResolvedValue("prod-eu");
    await expect(withConfirm(call, ask, req, false)).resolves.toBe("ok");
    expect(call.mock.calls).toEqual([[], ["prod-eu"]]);
  });

  it("does not ask when the hub accepts without confirmation", async () => {
    const ask = vi.fn();
    await withConfirm(vi.fn().mockResolvedValue("ok"), ask, req, false);
    expect(ask).not.toHaveBeenCalled();
  });

  it("passes other errors through", async () => {
    const err = new ApiError(403, "forbidden", "no");
    await expect(withConfirm(vi.fn().mockRejectedValue(err), vi.fn(), req, false)).rejects.toBe(err);
  });
});

describe("ConfirmDialog", () => {
  it("enables the action only when the cluster name is typed exactly", async () => {
    const onDone = vi.fn();
    render(<ConfirmDialog request={req} onDone={onDone} />);
    const button = screen.getByRole("button", { name: "Suspend" });
    const input = screen.getByRole("textbox");
    expect(button).toBeDisabled();
    await userEvent.type(input, "prod");
    expect(button).toBeDisabled();
    await userEvent.type(input, "-eu");
    expect(button).toBeEnabled();
    await userEvent.click(button);
    expect(onDone).toHaveBeenCalledWith("prod-eu");
  });

  it("submits with Enter and cancels with Escape", async () => {
    const onDone = vi.fn();
    render(<ConfirmDialog request={req} onDone={onDone} />);
    const input = screen.getByRole("textbox");
    await userEvent.type(input, "staging{Enter}");
    expect(onDone).not.toHaveBeenCalled();
    await userEvent.clear(input);
    await userEvent.type(input, "prod-eu{Enter}");
    expect(onDone).toHaveBeenLastCalledWith("prod-eu");
    await userEvent.keyboard("{Escape}");
    expect(onDone).toHaveBeenLastCalledWith(null);
  });
});
