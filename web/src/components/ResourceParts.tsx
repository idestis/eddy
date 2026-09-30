import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { eventsQuery, useMe, yamlQuery } from "../api/queries";
import type { ClusterInfo, KubeEvent, Ref, Resource } from "../api/types";
import { age, ago, dateTime, STATUS_LABEL } from "../lib/format";
import { hint, type KeyId } from "../lib/keys";
import { kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import type { useResourceActions } from "../lib/useResourceActions";
import { Icon, type IconName } from "./Icon";
import { Keys, StatusIcon } from "./Status";

export function ResourceHeader({ r }: { r: Resource }) {
  const info = kindInfo(r.kind);
  return (
    <>
      <div className="dk">
        <span className="kind">{info.abbr}</span>
        {r.kind}
        {r.namespace ? ` in ${r.namespace}` : " (cluster-scoped)"}
      </div>
      <h2 className="dname">{r.name}</h2>
      <div className={`dstatus ${r.status}`}>
        <StatusIcon status={r.status} />
        <div>
          <b>{STATUS_LABEL[r.status]}</b>
          {r.message}
        </div>
      </div>
    </>
  );
}

function ActionButton({
  icon,
  label,
  keyId,
  onClick,
  primary,
  soft,
  disabled,
  title,
}: {
  icon: IconName;
  label: string;
  keyId: KeyId;
  onClick: () => void;
  primary?: boolean;
  soft?: boolean;
  disabled?: boolean;
  title?: string;
}) {
  return (
    <button
      type="button"
      className={`btn${primary ? " primary" : ""}${soft ? " soft" : ""}`}
      onClick={onClick}
      disabled={disabled}
      title={title}
    >
      <Icon name={icon} />
      {label}
      <Keys keys={hint(keyId)} />
    </button>
  );
}

interface ActionsProps {
  cluster: ClusterInfo;
  r: Resource;
  actions: ReturnType<typeof useResourceActions>;
  onLogs?: () => void;
  onOpen?: () => void;
  onAsk?: () => void;
}

export function ResourceActions({ cluster, r, actions, onLogs, onOpen, onAsk }: ActionsProps) {
  const { data: me } = useMe();
  const info = kindInfo(r.kind);
  const busy = actions.busy.has(r.id);
  const offline = !cluster.connected;
  return (
    <>
      <div className="actions">
        {info.flux && !actions.readOnly && (
          <>
            <ActionButton
              icon="sync"
              label="Reconcile"
              keyId="reconcile"
              primary
              title={`flux reconcile ${r.kind.toLowerCase()} ${r.name}`}
              disabled={busy || r.suspended || offline}
              onClick={() => actions.reconcile(r)}
            />
            {info.hasSource && (
              <ActionButton
                icon="sync"
                label="With source"
                keyId="reconcileSource"
                title="Fetch the source first, then apply"
                disabled={busy || r.suspended || offline}
                onClick={() => actions.reconcile(r, true)}
              />
            )}
            <ActionButton
              icon={r.suspended ? "play" : "pause"}
              label={r.suspended ? "Resume" : "Suspend"}
              keyId="suspend"
              disabled={busy || offline}
              onClick={() => actions.toggleSuspend(r)}
            />
          </>
        )}
        {r.kind === "Pod" && onLogs && me?.features.logs && (
          <ActionButton icon="term" label="Logs" keyId="logs" primary onClick={onLogs} />
        )}
        {onOpen && <ActionButton icon="open" label="Open" keyId="open" onClick={onOpen} />}
        {onAsk && me?.features.ai && (
          <ActionButton icon="spark" label="Ask" keyId="ask" soft onClick={onAsk} />
        )}
      </div>
      {info.flux && actions.readOnly ? (
        <div className="note">
          <Icon name="lock" />
          {`${cluster.name} is in read-only local mode. Reconcile, suspend and resume are off.`}
        </div>
      ) : info.flux ? (
        <div className="note">
          <Icon name={cluster.protected ? "lock" : "user"} />
          {cluster.protected
            ? `${cluster.name} is protected. Suspending asks you to type its name.`
            : cluster.mode === "local"
              ? `Actions run as your kubeconfig identity (${cluster.context || cluster.name}), not as ${me?.user ?? "you"}.`
              : `Actions run as ${me?.user ?? "you"} through Kubernetes RBAC.`}
        </div>
      ) : (
        <div className="note" />
      )}
    </>
  );
}

function RefLink({ cluster, target }: { cluster: string; target: Ref }) {
  return (
    <Link {...detailLink(cluster, target)} className="linkbtn">
      {target.kind}/{target.namespace ? `${target.namespace}/` : ""}
      {target.name}
    </Link>
  );
}

/** Kind-aware key facts (overview tab and preview panel). */
export function ResourceFacts({ cluster, r }: { cluster: string; r: Resource }) {
  const rows: Array<[string, ReactNode]> = [];
  if (r.source) rows.push(["Source", <RefLink key="s" cluster={cluster} target={r.source} />]);
  if (r.chart) rows.push(["Chart", r.chart]);
  if (r.revision) rows.push(["Revision", r.revision]);
  if (r.url) rows.push(["URL", r.url]);
  if (r.interval) rows.push(["Interval", r.interval]);
  if (r.inventory) rows.push(["Inventory", `${r.inventory} objects`]);
  if (r.replicas) rows.push(["Replicas", `${r.replicas} ready`]);
  if (r.images?.length) rows.push(["Images", r.images.join("\n")]);
  if (r.owner) rows.push(["Managed by", <RefLink key="o" cluster={cluster} target={r.owner} />]);
  rows.push(["API version", r.group ? `${r.group}/${r.version}` : r.version]);
  if (r.createdAt) rows.push(["Created", `${dateTime(r.createdAt)} (${age(r.createdAt)})`]);
  if (r.lastChanged) rows.push(["Last change", ago(r.lastChanged)]);
  return (
    <dl className="kv">
      {rows.map(([k, v]) => (
        <div key={k} className="kv-row">
          <dt>{k}</dt>
          <dd>{v}</dd>
        </div>
      ))}
    </dl>
  );
}

export function Conditions({ r }: { r: Resource }) {
  if (!r.conditions?.length) return null;
  return (
    <>
      <h3 className="section-title">Conditions</h3>
      <ul className="evs">
        {r.conditions.map((c) => (
          <li key={c.type} className={`ev${c.status === "False" ? " warn" : ""}`}>
            <div className="eh">
              <span className="r">
                {c.type}={c.status}
                {c.reason ? ` · ${c.reason}` : ""}
              </span>
              <span className="a">{age(c.lastTransitionTime)}</span>
            </div>
            {c.message && <p>{c.message}</p>}
          </li>
        ))}
      </ul>
    </>
  );
}

export function EventsList({ cluster, r, limit }: { cluster: string; r: Resource; limit?: number }) {
  const { data, isPending, error } = useQuery(eventsQuery(cluster, r));
  if (isPending) return <p className="muted">Loading events…</p>;
  if (error) return <p className="error-text">Couldn't load events: {error.message}</p>;
  const items: KubeEvent[] = limit ? data.slice(0, limit) : data;
  if (!items.length) return <p className="muted">No recent events.</p>;
  return (
    <ul className="evs">
      {items.map((e, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: events have no stable id in the summary
        <li key={i} className={`ev${e.type === "Warning" ? " warn" : ""}`}>
          <div className="eh">
            <span className="r">
              {e.reason}
              {e.count > 1 ? ` ×${e.count}` : ""}
            </span>
            <span className="a">{age(e.last ?? e.first)}</span>
          </div>
          <p>{e.message}</p>
        </li>
      ))}
    </ul>
  );
}

const YAML_KEY = /^(\s*-?\s*)([A-Za-z][\w./-]*)(:)(.*)$/;

export function YamlView({ cluster, r }: { cluster: string; r: Resource }) {
  const { data, isPending, error } = useQuery(yamlQuery(cluster, r));
  if (isPending) return <p className="muted">Loading YAML…</p>;
  if (error) return <p className="error-text">Couldn't load YAML: {error.message}</p>;
  return (
    <pre className="yaml">
      {data.yaml.split("\n").map((line, i) => {
        const m = line.match(YAML_KEY);
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: lines are positional
          <span key={i}>
            {m ? (
              <>
                {m[1]}
                <span className="yk">{m[2]}</span>
                {m[3]}
                <span className="ys">{m[4]}</span>
              </>
            ) : (
              line
            )}
            {"\n"}
          </span>
        );
      })}
    </pre>
  );
}
