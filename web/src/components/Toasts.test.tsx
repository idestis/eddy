import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider, useToast } from "./Toasts";

let show: ReturnType<typeof useToast> = () => {};
function Grab() {
  show = useToast();
  return null;
}

describe("toasts", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("slides out before it is removed", () => {
    render(
      <ToastProvider>
        <Grab />
      </ToastProvider>,
    );
    act(() => show("Reconcile requested: Kustomization/apps", "ok"));
    const toast = screen.getByText(/Reconcile requested/).parentElement;
    expect(toast).toHaveClass("anim-toast-in");
    act(() => vi.advanceTimersByTime(3400));
    expect(toast).toHaveClass("anim-toast-out");
    act(() => vi.advanceTimersByTime(200));
    expect(screen.queryByText(/Reconcile requested/)).not.toBeInTheDocument();
  });

  it("offers an action such as Undo, and runs it once", () => {
    render(
      <ToastProvider>
        <Grab />
      </ToastProvider>,
    );
    const undo = vi.fn();
    act(() => show("Suspended: Kustomization/apps", "ok", { action: { label: "Undo", run: undo } }));
    act(() => screen.getByRole("button", { name: "Undo" }).click());
    expect(undo).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument();
  });
});
