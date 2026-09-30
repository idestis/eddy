// The "Add cluster" wizard (ADR-0005): a form that creates the Cluster CR and a
// join token, then the connect screen with the install guide and the live checklist.

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type ReactNode, useId, useState } from "react";
import { isApiError } from "../api/client";
import { createCluster } from "../api/endpoints";
import { connectionQuery, keys } from "../api/queries";
import type { ClusterInfo, ClusterInput, CreatedCluster } from "../api/types";
import {
  CLUSTER_PALETTE,
  coreReady,
  isConnected,
  LABEL_MAX,
  labelError,
  NAME_MAX,
  nameError,
} from "../lib/onboarding";
import { ClusterTile } from "./ClusterSwitch";
import { ConnectionChecklist, RejectedAttempts } from "./ConnectionStatus";
import { Icon } from "./Icon";
import { InstallGuide } from "./InstallGuide";
import { Modal } from "./Modal";

const POLL_MS = 3_000;
const ENVIRONMENTS = ["Production", "Staging", "Development", "Edge", "Sandbox"];

function DialogHead({ title, sub, onClose }: { title: ReactNode; sub?: ReactNode; onClose: () => void }) {
  return (
    <div className="flex shrink-0 items-start gap-3 border-b border-line px-[22px] pt-[18px] pb-3.5">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <h2 className="flex items-center gap-2.5 text-18 font-semibold tracking-tight">{title}</h2>
        {sub && <p className="text-13 text-ink-3">{sub}</p>}
      </div>
      <button type="button" className="ib" aria-label="Close" onClick={onClose}>
        <Icon name="x" />
      </button>
    </div>
  );
}

function FieldError({ id, children }: { id: string; children?: string }) {
  if (!children) return null;
  return (
    <span id={id} className="text-12 text-bad">
      {children}
    </span>
  );
}

export interface ClusterFormProps {
  defaultOrder: number;
  pending?: boolean;
  /** The last error from the hub, shown inline. */
  error?: Error | null;
  onSubmit: (input: ClusterInput) => void;
  onCancel: () => void;
}

/** Step 1: the cluster's identity. Validation mirrors the hub; the hub has the last word. */
export function ClusterForm({ defaultOrder, pending, error, onSubmit, onCancel }: ClusterFormProps) {
  const [name, setName] = useState("");
  const [touched, setTouched] = useState(false);
  const [displayName, setDisplayName] = useState("");
  const [environment, setEnvironment] = useState("");
  const [region, setRegion] = useState("");
  const [color, setColor] = useState<string | undefined>(undefined);
  const [isProtected, setProtected] = useState(false);
  const [order, setOrder] = useState(String(defaultOrder));
  const id = useId();

  const errors = {
    name: nameError(name),
    displayName: labelError(displayName),
    environment: labelError(environment),
    region: labelError(region),
    order: /^-?\d+$/.test(order.trim()) ? undefined : "Use a whole number.",
  };
  const valid = !Object.values(errors).some(Boolean);
  const conflict = isApiError(error, "conflict") && /exist/i.test(error.message);
  const nameMessage = (touched || name ? errors.name : undefined) ?? (conflict ? error?.message : undefined);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    setTouched(true);
    if (!valid || pending) return;
    onSubmit({
      name,
      displayName: displayName.trim() || undefined,
      environment: environment.trim() || undefined,
      region: region.trim() || undefined,
      color,
      protected: isProtected,
      order: Number.parseInt(order, 10),
      ttl: "1h",
    });
  };

  const preview: ClusterInfo = {
    name: name || "cluster",
    displayName: displayName || name,
    color,
    protected: isProtected,
    order: Number.parseInt(order, 10) || 0,
    connected: false,
  };

  return (
    <form className="flex min-h-0 flex-1 flex-col" onSubmit={submit} noValidate>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-auto px-[22px] py-[18px]">
        <label className="field">
          <span>
            Name <span className="text-ink-3">(required)</span>
          </span>
          <input
            className="font-mono"
            value={name}
            onChange={(e) => setName(e.target.value)}
            onBlur={() => setTouched(true)}
            placeholder="prod-us"
            maxLength={NAME_MAX + 10}
            autoComplete="off"
            spellCheck={false}
            required
            aria-invalid={Boolean(nameMessage)}
            aria-describedby={`${id}-name-help ${id}-name-err`}
          />
          <span id={`${id}-name-help`} className="text-12 text-ink-3">
            Lower-case letters, digits and hyphens, at most {NAME_MAX}. The agent is installed with this name
            and it cannot be changed later.
          </span>
          <FieldError id={`${id}-name-err`}>{nameMessage}</FieldError>
        </label>
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          <TextField
            label="Display name"
            value={displayName}
            onChange={setDisplayName}
            placeholder={name || "Production US"}
            error={errors.displayName}
          />
          <TextField
            label="Environment"
            value={environment}
            onChange={setEnvironment}
            placeholder="Production"
            error={errors.environment}
            list={`${id}-envs`}
          />
          <datalist id={`${id}-envs`}>
            {ENVIRONMENTS.map((e) => (
              <option key={e} value={e} />
            ))}
          </datalist>
          <TextField
            label="Region"
            value={region}
            onChange={setRegion}
            placeholder="us-east-1"
            error={errors.region}
          />
          <label className="field">
            Order
            <input
              type="number"
              inputMode="numeric"
              value={order}
              onChange={(e) => setOrder(e.target.value)}
              aria-invalid={Boolean(errors.order)}
              aria-describedby={`${id}-order-err`}
            />
            <FieldError id={`${id}-order-err`}>{errors.order}</FieldError>
          </label>
        </div>
        <fieldset className="m-0 flex flex-col gap-2 border-0 p-0">
          <legend className="mb-2 p-0 text-13 text-ink-2">Colour</legend>
          <div className="flex flex-wrap items-center gap-2.5">
            <button
              type="button"
              className="flex size-8 items-center justify-center rounded-full border border-dashed border-line-strong bg-surface-sunken text-ink-3 ring-ink ring-offset-2 ring-offset-surface aria-pressed:ring-2"
              aria-label="Automatic colour"
              aria-pressed={color === undefined}
              title="Automatic: picked from the order"
              onClick={() => setColor(undefined)}
            >
              <Icon name="sync" className="size-3.5" />
            </button>
            {CLUSTER_PALETTE.map((p) => (
              <button
                key={p.token}
                type="button"
                className={`size-8 rounded-full ${p.swatch} ring-ink ring-offset-2 ring-offset-surface aria-pressed:ring-2`}
                aria-label={p.name}
                aria-pressed={color === p.hex}
                title={p.name}
                onClick={() => setColor(p.hex)}
              />
            ))}
            <span className="ml-auto flex items-center gap-2 text-12-5 text-ink-3">
              Preview
              <ClusterTile cluster={preview} />
            </span>
          </div>
        </fieldset>
        <label className="flex items-start gap-3 rounded-xl border border-line-strong px-3.5 py-3 has-checked:border-c has-checked:bg-c-soft">
          <input
            type="checkbox"
            role="switch"
            aria-checked={isProtected}
            className="mt-0.5 size-4 shrink-0 accent-(--c)"
            checked={isProtected}
            onChange={(e) => setProtected(e.target.checked)}
          />
          <span className="flex flex-col gap-0.5">
            <span className="flex items-center gap-1.5 text-13-5 font-semibold">
              <Icon name="lock" className="size-3.5 text-ink-3" />
              Protected
            </span>
            <span className="text-12-5 text-ink-3">
              Reconcile, suspend and resume, and deleting the cluster from Eddy, need the cluster name typed
              to confirm.
            </span>
          </span>
        </label>
        {error && !conflict && (
          <div
            className="flex items-start gap-2 rounded-xl border border-bad/40 bg-bad/6 px-3 py-2.5 text-12-5 text-ink-2"
            role="alert"
          >
            <Icon name="alert" className="mt-px size-4 shrink-0 text-bad" />
            <span>
              <b className="font-semibold text-ink">Couldn't add the cluster.</b> {serverMessage(error)}
            </span>
          </div>
        )}
      </div>
      <div className="flex shrink-0 items-center justify-end gap-2 border-t border-line px-[22px] py-3.5">
        <button type="button" className="btn" onClick={onCancel}>
          Cancel
        </button>
        <button
          type="submit"
          className="btn btn-primary"
          disabled={pending || ((touched || name !== "") && !valid)}
        >
          <Icon name="plug" />
          {pending ? "Adding…" : "Add and get install command"}
        </button>
      </div>
    </form>
  );
}

function TextField({
  label,
  value,
  onChange,
  placeholder,
  error,
  list,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  error?: string;
  list?: string;
}) {
  const id = useId();
  return (
    <label className="field">
      {label}
      <input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        maxLength={LABEL_MAX + 10}
        list={list}
        autoComplete="off"
        aria-invalid={Boolean(error)}
        aria-describedby={`${id}-err`}
      />
      <FieldError id={`${id}-err`}>{error}</FieldError>
    </label>
  );
}

function serverMessage(err: Error): string {
  if (isApiError(err, "forbidden")) return `You may not add clusters here. ${err.message}`;
  if (isApiError(err, "conflict")) return err.message;
  if (isApiError(err, "disconnected")) return `The hub cannot reach the management cluster. ${err.message}`;
  return err.message;
}

/** Step 2: install the agent and watch it connect. */
export function ConnectStep({ created, onFinish }: { created: CreatedCluster; onFinish: () => void }) {
  const name = created.cluster.name;
  const connection = useQuery({
    ...connectionQuery(name),
    // SSE `connection` events drive updates; polling covers a dropped stream.
    refetchInterval: (q) => (coreReady(q.state.data) ? false : POLL_MS),
  });
  const info = connection.data;
  const connected = isConnected(info);
  const tile: ClusterInfo = { ...created.cluster, connected };

  return (
    <>
      <DialogHead
        title={
          <>
            <ClusterTile cluster={tile} size="sm" />
            Connect {created.cluster.displayName || name}
          </>
        }
        sub="Install the agent in the new cluster. This screen updates as it connects."
        onClose={onFinish}
      />
      <div className="grid min-h-0 flex-1 grid-cols-[minmax(0,1.4fr)_minmax(0,1fr)] gap-5 overflow-auto px-[22px] py-[18px] max-[859px]:grid-cols-1">
        <section className="flex min-w-0 flex-col gap-2" aria-labelledby="connect-install">
          <h3 id="connect-install" className="text-14 font-semibold">
            1. Install the agent
          </h3>
          <InstallGuide guide={created.guide} joinToken={created.joinToken} />
        </section>
        <section className="flex min-w-0 flex-col gap-2" aria-labelledby="connect-checks" aria-live="polite">
          <h3 id="connect-checks" className="flex items-center gap-2 text-14 font-semibold">
            2. Watch it connect
            {!coreReady(info) && (
              <span className="text-12 font-normal text-ink-3">checking every few seconds…</span>
            )}
          </h3>
          {connection.error && !isApiError(connection.error, "forbidden") && (
            <p className="text-12-5 text-bad">{connection.error.message}</p>
          )}
          {info && (
            <>
              <ConnectionChecklist checks={info.checks} />
              <h4 className="mt-2 text-13 font-medium text-ink-3">Rejected connections</h4>
              <RejectedAttempts attempts={info.attempts} />
            </>
          )}
          {connection.isPending && <p className="text-12-5 text-ink-3">Loading…</p>}
        </section>
      </div>
      <div className="flex shrink-0 items-center justify-end gap-3 border-t border-line px-[22px] py-3.5">
        <span className="mr-auto text-12-5 text-ink-3">
          {connected
            ? `${name} is connected.`
            : "You can finish now: the checklist stays on the cluster card under Connection."}
        </span>
        <button type="button" className={`btn${connected ? " btn-primary" : ""}`} onClick={onFinish}>
          {connected && <Icon name="check" />}
          Finish
        </button>
      </div>
    </>
  );
}

/** The whole wizard in one dialog. Closing it at any step returns to the fleet page. */
export function AddClusterDialog({ defaultOrder, onClose }: { defaultOrder: number; onClose: () => void }) {
  const qc = useQueryClient();
  const [created, setCreated] = useState<CreatedCluster | null>(null);
  const create = useMutation({
    mutationFn: createCluster,
    onSuccess: (res) => {
      setCreated(res);
      void qc.invalidateQueries({ queryKey: keys.clusters });
    },
  });

  return (
    <Modal
      label={created ? `Connect ${created.cluster.name}` : "Add a cluster"}
      onClose={onClose}
      className={created ? "max-h-[90vh]! w-[min(1040px,100%)]!" : "max-h-[90vh]! w-[min(620px,100%)]!"}
    >
      {created ? (
        <ConnectStep created={created} onFinish={onClose} />
      ) : (
        <>
          <DialogHead
            title="Add a cluster"
            sub="Eddy creates the Cluster resource and a one-time join token, then shows how to install the agent."
            onClose={onClose}
          />
          <ClusterForm
            defaultOrder={defaultOrder}
            pending={create.isPending}
            error={create.error}
            onSubmit={(input) => create.mutate(input)}
            onCancel={onClose}
          />
        </>
      )}
    </Modal>
  );
}
