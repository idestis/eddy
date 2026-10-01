import { act, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ClusterInfo } from "../api/types";
import { ConfirmProvider } from "../components/ConfirmDialog";
import { ToastProvider } from "../components/Toasts";
import { resource } from "../test/fixtures";
import { resetLiveMotion, useRequested } from "./liveMotion";
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

describe("useResourceActions feedback", () => {
  const writable: ClusterInfo = { ...local, readOnly: false, mode: undefined };

  it("marks the row requested while the request is out, and confirms with a toast", async () => {
    resetLiveMotion();
    let release: () => void = () => {};
    postAction.mockReset().mockImplementation(
      () =>
        new Promise<void>((r) => {
          release = r;
        }),
    );
    let requested: ReturnType<typeof useRequested> = new Map();
    function Watch() {
      requested = useRequested("kind-eddy");
      return null;
    }
    render(
      <ToastProvider>
        <ConfirmProvider>
          <Harness cluster={writable} />
          <Watch />
        </ConfirmProvider>
      </ToastProvider>,
    );
    const apps = resource("apps");
    act(() => actions?.reconcile(apps));
    expect(actions?.busy.get(apps.id)).toBe("reconcile");
    expect(requested.get(apps.id)?.action).toBe("reconcile");
    await act(async () => release());
    expect(actions?.busy.has(apps.id)).toBe(false);
    expect(screen.getByText("Reconcile requested: Kustomization/apps")).toBeInTheDocument();
    // Still waiting for the cluster to report a new status.
    expect(requested.get(apps.id)).toBeDefined();
  });

  it("offers Undo after a suspend, which resumes", async () => {
    postAction.mockReset().mockResolvedValue(undefined);
    const a = renderActions(writable);
    await act(async () => a.toggleSuspend(resource("apps")));
    await act(async () => screen.getByRole("button", { name: "Undo" }).click());
    expect(postAction.mock.calls.map((c) => c[2])).toEqual(["suspend", "resume"]);
  });

  it("refuses on a disconnected cluster without calling the hub", async () => {
    postAction.mockReset();
    const a = renderActions({ ...writable, connected: false });
    await act(async () => a.reconcile(resource("apps")));
    expect(postAction).not.toHaveBeenCalled();
    expect(screen.getByText(/is disconnected/)).toBeInTheDocument();
  });
});
