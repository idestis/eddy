import { useCallback, useState } from "react";
import { isApiError } from "../api/client";
import { type Action, postAction } from "../api/endpoints";
import type { ClusterInfo, Resource } from "../api/types";
import { useConfirm } from "../components/ConfirmDialog";
import { useToast } from "../components/Toasts";
import { CANCELLED, withConfirm } from "./confirm";
import { kindInfo } from "./kinds";

const VERB: Record<Action, string> = { reconcile: "reconcile", suspend: "suspend", resume: "resume" };
const DONE: Record<Action, string> = {
  reconcile: "Reconcile requested",
  suspend: "Suspended",
  resume: "Resumed",
};
const TITLE: Record<Action, string> = { reconcile: "Reconcile", suspend: "Suspend", resume: "Resume" };

/**
 * Reconcile, suspend and resume for Flux objects. Results arrive through the
 * SSE stream; this hook only sends the request and reports the outcome.
 */
export function useResourceActions(cluster: ClusterInfo | undefined) {
  const askConfirm = useConfirm();
  const toast = useToast();
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());

  const run = useCallback(
    async (r: Resource, action: Action, withSource = false) => {
      if (!cluster) return;
      const setDone = (on: boolean) =>
        setBusy((prev) => {
          const next = new Set(prev);
          if (on) next.add(r.id);
          else next.delete(r.id);
          return next;
        });
      const target = `${r.kind}/${r.name}`;
      setDone(true);
      try {
        const result = await withConfirm(
          (confirm) => postAction(cluster.name, r, action, { withSource: withSource || undefined, confirm }),
          askConfirm,
          {
            title: `${TITLE[action]} ${r.name} on ${cluster.name}?`,
            body:
              action === "suspend"
                ? `Flux stops applying changes to this ${r.kind} until it is resumed. ${cluster.name} is a protected cluster.`
                : `${cluster.name} is a protected cluster, so this action needs a typed confirmation.`,
            expected: cluster.name,
            action: TITLE[action],
          },
          action === "suspend" && cluster.protected,
        );
        if (result === CANCELLED) return;
        toast(`${DONE[action]}${withSource ? " with source" : ""}: ${target}`, "ok");
      } catch (err) {
        if (isApiError(err, "forbidden")) {
          toast(`You can't ${VERB[action]} ${target} on ${cluster.name}: ${err.message}`, "bad");
        } else if (isApiError(err, "disconnected")) {
          toast(`${cluster.name} is disconnected. Try again when its agent is back.`, "bad");
        } else if (isApiError(err)) {
          toast(`Couldn't ${VERB[action]} ${target}: ${err.message}`, "bad");
        } else {
          throw err;
        }
      } finally {
        setDone(false);
      }
    },
    [cluster, askConfirm, toast],
  );

  const readOnly = cluster?.readOnly === true;
  const refuseReadOnly = useCallback(() => {
    toast(
      `${cluster?.name ?? "This cluster"} is in read-only local mode. Restart the agent with ALLOW_WRITES=1 to allow writes.`,
    );
  }, [cluster, toast]);

  const reconcile = useCallback(
    (r: Resource | undefined, withSource = false) => {
      if (!r) return;
      if (readOnly) {
        refuseReadOnly();
        return;
      }
      const info = kindInfo(r.kind);
      if (!info.flux) {
        toast(`${info.plural} aren't reconciled by Flux directly. Press u to jump to the owner.`);
        return;
      }
      if (r.suspended) {
        toast(`${r.name} is suspended. Resume it first with s.`);
        return;
      }
      void run(r, "reconcile", withSource && info.hasSource);
    },
    [run, toast, readOnly, refuseReadOnly],
  );

  const toggleSuspend = useCallback(
    (r: Resource | undefined) => {
      if (!r) return;
      if (readOnly) {
        refuseReadOnly();
        return;
      }
      if (!kindInfo(r.kind).flux) {
        toast("Only Flux objects can be suspended.");
        return;
      }
      void run(r, r.suspended ? "resume" : "suspend");
    },
    [run, toast, readOnly, refuseReadOnly],
  );

  return { reconcile, toggleSuspend, busy, readOnly };
}
