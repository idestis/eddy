import { useCallback, useState } from "react";
import { isApiError } from "../api/client";
import { type Action, postAction } from "../api/endpoints";
import type { ClusterInfo, Resource } from "../api/types";
import { useConfirm } from "../components/ConfirmDialog";
import { useToast } from "../components/Toasts";
import { CANCELLED, withConfirm } from "./confirm";
import { kindInfo } from "./kinds";
import { clearRequested, markRequested } from "./liveMotion";

const VERB: Record<Action, string> = { reconcile: "reconcile", suspend: "suspend", resume: "resume" };
const DONE: Record<Action, string> = {
  reconcile: "Reconcile requested",
  suspend: "Suspended",
  resume: "Resumed",
};
const TITLE: Record<Action, string> = { reconcile: "Reconcile", suspend: "Suspend", resume: "Resume" };

const UNDO: Record<Action, Action> = { reconcile: "reconcile", suspend: "resume", resume: "suspend" };

/**
 * Reconcile, suspend and resume for Flux objects. Results arrive through the SSE
 * stream; this hook sends the request and gives feedback at each step:
 * - while the request is in flight, `busy` names the action (the button shows a spinner);
 * - the row is marked "requested" (lib/liveMotion.ts) until its next status arrives,
 *   with a note if none arrives within 30 s;
 * - the 202 is confirmed with a toast; suspend and resume offer Undo.
 */
export function useResourceActions(cluster: ClusterInfo | undefined) {
  const askConfirm = useConfirm();
  const toast = useToast();
  const [busy, setBusy] = useState<ReadonlyMap<string, Action>>(new Map());

  const run = useCallback(
    async (r: Resource, action: Action, withSource = false): Promise<void> => {
      if (!cluster) return;
      const setDone = (on: boolean) =>
        setBusy((prev) => {
          const next = new Map(prev);
          if (on) next.set(r.id, action);
          else next.delete(r.id);
          return next;
        });
      const target = `${r.kind}/${r.name}`;
      const name = cluster.name;
      const noNews = () =>
        toast(
          `No change reported for ${target} yet. It may already be up to date; its Events tab shows what Flux did.`,
        );
      setDone(true);
      try {
        const result = await withConfirm(
          (confirm) => {
            markRequested(name, r, action, action === "reconcile" ? noNews : undefined);
            return postAction(name, r, action, { withSource: withSource || undefined, confirm });
          },
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
        if (result === CANCELLED) {
          clearRequested(name, r.id);
          return;
        }
        const undo = action === "reconcile" ? undefined : UNDO[action];
        toast(
          `${DONE[action]}${withSource ? " with source" : ""}: ${target}`,
          "ok",
          undo ? { action: { label: "Undo", run: () => void run(r, undo) } } : undefined,
        );
      } catch (err) {
        clearRequested(name, r.id);
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
  const offline = cluster ? !cluster.connected : false;
  /** True (after explaining why) when the cluster cannot take writes right now. */
  const refuse = useCallback((): boolean => {
    if (readOnly) {
      toast(
        `${cluster?.name ?? "This cluster"} is in read-only local mode. Restart the agent with ALLOW_WRITES=1 to allow writes.`,
      );
      return true;
    }
    if (offline) {
      toast(
        `${cluster?.name ?? "This cluster"} is disconnected. Actions come back when its agent reconnects.`,
      );
      return true;
    }
    return false;
  }, [cluster, toast, readOnly, offline]);

  const reconcile = useCallback(
    (r: Resource | undefined, withSource = false) => {
      if (!r || refuse()) return;
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
    [run, toast, refuse],
  );

  const toggleSuspend = useCallback(
    (r: Resource | undefined) => {
      if (!r || refuse()) return;
      if (!kindInfo(r.kind).flux) {
        toast("Only Flux objects can be suspended.");
        return;
      }
      void run(r, r.suspended ? "resume" : "suspend");
    },
    [run, toast, refuse],
  );

  return { reconcile, toggleSuspend, busy, readOnly, offline };
}
