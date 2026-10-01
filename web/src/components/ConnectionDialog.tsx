// The "Connection" panel of a fleet card (ADR-0005): the same checklist and rejected
// attempts as the connect screen, plus reissuing the join token and deleting the cluster.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { isApiError } from "../api/client";
import { createJoinToken, deleteCluster, updateCluster } from "../api/endpoints";
import { connectionQuery, keys } from "../api/queries";
import type { ClusterInfo, ConnectionInfo, IssuedJoinToken } from "../api/types";
import { CANCELLED, withConfirm } from "../lib/confirm";
import { coreReady } from "../lib/onboarding";
import { ClusterColorPicker } from "./ClusterColorPicker";
import { ClusterTile } from "./ClusterSwitch";
import { useConfirm } from "./ConfirmDialog";
import { ConnectionChecklist, JoinTokenSummary, RejectedAttempts } from "./ConnectionStatus";
import { Icon } from "./Icon";
import { InstallGuide } from "./InstallGuide";
import { Modal } from "./Modal";
import { useToast } from "./Toasts";

const POLL_MS = 3_000;

/**
 * The card's connection query. It resolves to undefined when the user may not read
 * the Cluster in the management cluster (403): the panel then stays hidden.
 */
export function useConnection(cluster: string, enabled: boolean) {
  const q = useQuery({ ...connectionQuery(cluster), enabled });
  return q.error && isApiError(q.error, "forbidden") ? undefined : q.data;
}

const H3 = "text-14 font-semibold";

export function ConnectionDialog({ cluster, onClose }: { cluster: ClusterInfo; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const ask = useConfirm();
  const name = cluster.name;
  const connection = useQuery({
    ...connectionQuery(name),
    refetchInterval: (q) => (q.state.data && !coreReady(q.state.data) ? POLL_MS : false),
  });
  const info: ConnectionInfo | undefined = connection.data;
  const [issued, setIssued] = useState<IssuedJoinToken | null>(null);
  const [armed, setArmed] = useState(false);
  const helm = info?.cluster.managedBy === "helm";

  const regenerate = useMutation({
    mutationFn: () => createJoinToken(name, "1h"),
    onSuccess: (res) => {
      setIssued(res);
      toast("New join token issued. The previous unused one no longer works.", "ok");
      void qc.invalidateQueries({ queryKey: keys.connection(name) });
    },
    onError: (err) => toast(`Couldn't issue a join token: ${err.message}`, "bad"),
  });

  const remove = useMutation({
    mutationFn: () =>
      withConfirm(
        (confirm) => deleteCluster(name, confirm),
        ask,
        {
          title: `Delete ${name} from Eddy?`,
          body: "This deletes the Cluster resource and its agent token. The agent disconnects and nothing in the cluster itself is touched.",
          expected: name,
          action: "Delete cluster",
        },
        Boolean(info?.cluster.protected ?? cluster.protected),
      ),
    onSuccess: (res) => {
      if (res === CANCELLED) return;
      toast(`${name} deleted`, "ok");
      void qc.invalidateQueries({ queryKey: keys.clusters });
      qc.removeQueries({ queryKey: keys.connection(name) });
      onClose();
    },
    onError: (err) => {
      setArmed(false);
      toast(`Couldn't delete ${name}: ${err.message}`, "bad");
    },
  });

  // Colour: Auto (undefined, sent as "") or a hex. Saved explicitly, not on every click.
  const [color, setColor] = useState<string | undefined>(cluster.color?.toLowerCase());
  const colorChanged = (color ?? "") !== (cluster.color?.toLowerCase() ?? "");
  const saveColor = useMutation({
    mutationFn: () => updateCluster(name, { color: color ?? "" }),
    onSuccess: () => {
      toast(`Colour of ${name} saved`, "ok");
      void qc.invalidateQueries({ queryKey: keys.clusters });
    },
    onError: (err) => toast(`Couldn't save the colour: ${err.message}`, "bad"),
  });

  const onDelete = () => {
    // Protected clusters confirm by typing the name; others with a second click.
    if (!(info?.cluster.protected ?? cluster.protected) && !armed) {
      setArmed(true);
      return;
    }
    remove.mutate();
  };

  return (
    <Modal label={`Connection of ${name}`} onClose={onClose} className="max-h-[90vh]! w-[min(820px,100%)]!">
      <div className="flex shrink-0 items-start gap-3 border-b border-line px-[22px] pt-[18px] pb-3.5">
        <ClusterTile cluster={cluster} size="sm" />
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h2 className="text-18 font-semibold tracking-tight">Connection</h2>
          <p className="text-13 text-ink-3">
            {cluster.displayName || name}
            {info && (
              <>
                {" "}
                · {info.cluster.phase} · {info.agents} agent{info.agents === 1 ? "" : "s"}
              </>
            )}
          </p>
        </div>
        <button type="button" className="ib" aria-label="Close" onClick={onClose}>
          <Icon name="x" />
        </button>
      </div>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto px-[22px] py-[18px]">
        {connection.isPending && <p className="text-ink-3">Loading…</p>}
        {connection.error && <p className="text-12-5 text-bad">{connection.error.message}</p>}
        {info && (
          <>
            {helm && (
              <div
                className="flex items-start gap-2 rounded-xl border border-line bg-surface-sunken px-3 py-2.5 text-12-5 text-ink-2"
                role="note"
              >
                <Icon name="helm" className="mt-px size-4 shrink-0 text-ink-3" />
                <span>
                  <b className="font-semibold text-ink">Managed by Helm.</b> This cluster is declared in the
                  eddy-hub chart's <code className="text-12">clusters[]</code> values. Edit or remove it
                  there; the UI can still issue a join token to reinstall its agent.
                </span>
              </div>
            )}
            <div className="grid grid-cols-2 gap-5 max-[859px]:grid-cols-1">
              <section className="flex min-w-0 flex-col gap-2" aria-live="polite">
                <h3 className={H3}>Checks</h3>
                <ConnectionChecklist checks={info.checks} />
              </section>
              <section className="flex min-w-0 flex-col gap-2">
                <h3 className={H3}>Join token</h3>
                <JoinTokenSummary token={info.joinToken} />
                <h3 className={`${H3} mt-2`}>Rejected connections</h3>
                <RejectedAttempts attempts={info.attempts} />
              </section>
            </div>
            {info.permissions.update && !helm && (
              <section className="flex flex-col gap-2 rounded-card border border-line px-4 py-3">
                <ClusterColorPicker
                  value={color}
                  onChange={setColor}
                  name={name}
                  preview={{ ...cluster, color }}
                />
                <div className="flex justify-end">
                  <button
                    type="button"
                    className="btn btn-sm btn-primary"
                    disabled={!colorChanged || saveColor.isPending}
                    aria-busy={saveColor.isPending || undefined}
                    onClick={() => saveColor.mutate()}
                  >
                    {saveColor.isPending ? "Saving…" : "Save colour"}
                  </button>
                </div>
              </section>
            )}
            {issued ? (
              <section className="flex min-w-0 flex-col gap-2 rounded-card border border-ok/40 bg-ok/6 p-4">
                <h3 className={H3}>Reinstall the agent with the new token</h3>
                <InstallGuide guide={issued.guide} joinToken={issued.joinToken} />
              </section>
            ) : (
              <details className="rounded-card border border-line px-4 py-3 [&[open]>summary]:mb-3">
                <summary className="cursor-pointer text-13 font-medium">Install guide</summary>
                <p className="mb-3 text-12-5 text-ink-3">
                  The token is shown as <code className="text-12">&lt;join token&gt;</code>. Issue a new join
                  token to get a ready-to-run command.
                </p>
                <InstallGuide guide={info.guide} />
              </details>
            )}
          </>
        )}
      </div>
      {info && (info.permissions.update || info.permissions.delete) && (
        <div className="flex shrink-0 flex-wrap items-center gap-2 border-t border-line px-[22px] py-3.5">
          {info.permissions.update && (
            <button
              type="button"
              className="btn btn-sm"
              disabled={regenerate.isPending}
              onClick={() => regenerate.mutate()}
            >
              <Icon name="key" />
              {regenerate.isPending
                ? "Issuing…"
                : issued
                  ? "Issue another join token"
                  : "Regenerate join token"}
            </button>
          )}
          {info.permissions.delete && !helm && (
            <>
              {armed && <span className="ml-auto text-12-5 text-bad">Click again to delete {name}.</span>}
              <button
                type="button"
                className={`btn btn-sm ${armed ? "btn-danger" : ""} ${armed ? "" : "ml-auto"}`}
                disabled={remove.isPending}
                onClick={onDelete}
                onBlur={() => setArmed(false)}
              >
                <Icon name="x" />
                {armed ? "Confirm delete" : "Delete cluster"}
              </button>
            </>
          )}
        </div>
      )}
    </Modal>
  );
}
