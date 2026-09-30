import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ClusterInfo } from "../api/types";
import { ConfirmProvider } from "../components/ConfirmDialog";
import { ToastProvider } from "../components/Toasts";
import { resource } from "../test/fixtures";
import { useResourceActions } from "./useResourceActions";

const postAction = vi.hoisted(() => vi.fn());
vi.mock("../api/endpoints", () => ({ postAction }));

const local: ClusterInfo = {
  name: "kind-eddy",
  displayName: "kind-eddy",
  protected: false,
  order: 1,
  connected: true,
  mode: "local",
  readOnly: true,
  context: "kind-eddy",
};

let actions: ReturnType<typeof useResourceActions> | undefined;

function Harness({ cluster }: { cluster: ClusterInfo }) {
  actions = useResourceActions(cluster);
  return null;
}

function renderActions(cluster: ClusterInfo) {
  render(
    <ToastProvider>
      <ConfirmProvider>
        <Harness cluster={cluster} />
      </ConfirmProvider>
    </ToastProvider>,
  );
  if (!actions) throw new Error("hook did not render");
  return actions;
}

describe("useResourceActions in read-only local mode", () => {
  it("never calls the hub and explains why", async () => {
    const a = renderActions(local);
    expect(a.readOnly).toBe(true);
    await act(async () => {
      a.reconcile(resource("apps"));
      a.toggleSuspend(resource("apps"));
    });
    expect(postAction).not.toHaveBeenCalled();
    expect(screen.getAllByText(/read-only local mode/)).toHaveLength(2);
  });

  it("sends actions when the cluster is writable", async () => {
    postAction.mockResolvedValue(undefined);
    const a = renderActions({ ...local, readOnly: false });
    expect(a.readOnly).toBe(false);
    await act(async () => a.reconcile(resource("apps")));
    expect(postAction).toHaveBeenCalledOnce();
  });
});
