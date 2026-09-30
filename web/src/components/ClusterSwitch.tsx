import { Link, useNavigate } from "@tanstack/react-router";
import type { ClusterInfo } from "../api/types";
import { useAppState } from "../lib/appState";
import { clusterStyle } from "../lib/clusterColor";
import { Icon } from "./Icon";
import { Modal } from "./Modal";
import { Health } from "./Status";

const initial = (c: ClusterInfo) => (c.displayName || c.name).charAt(0).toUpperCase();

/** The cluster identity button (sidebar and compact top bar). */
export function ClusterSwitch({ cluster }: { cluster: ClusterInfo | undefined }) {
  const { setClusterMenu } = useAppState();
  return (
    <button
      type="button"
      className="switch"
      aria-haspopup="dialog"
      aria-label={cluster ? `Cluster ${cluster.name}. Switch cluster` : "Choose a cluster"}
      onClick={() => setClusterMenu(true)}
    >
      <span className="sw">
        {cluster ? cluster.protected ? "" : initial(cluster) : <Icon name="globe" />}
      </span>
      <span className="sn">
        <span>{cluster ? cluster.displayName || cluster.name : "Fleet"}</span>
        {cluster?.protected && (
          <span className="lockb">
            <Icon name="lock" />
            Protected
          </span>
        )}
      </span>
      <span className="se">
        {cluster ? [cluster.environment, cluster.region].filter(Boolean).join(", ") : "All clusters"}
      </span>
      <span className="sc">
        <Icon name="updown" />
      </span>
    </button>
  );
}

export function ClusterMenu({ clusters, current }: { clusters: ClusterInfo[]; current: string | undefined }) {
  const { setClusterMenu } = useAppState();
  const navigate = useNavigate();
  const close = () => setClusterMenu(false);
  return (
    <Modal label="Switch cluster" onClose={close} className="clusters-menu">
      {clusters.map((c, i) => (
        <button
          type="button"
          key={c.name}
          className={`clrow${c.protected ? " protected" : ""}`}
          style={clusterStyle(c)}
          aria-current={c.name === current}
          onClick={() => {
            close();
            void navigate({ to: "/c/$cluster", params: { cluster: c.name } });
          }}
        >
          <span className="sw">{c.protected ? "" : initial(c)}</span>
          <span className="cn">
            {c.displayName || c.name}
            {c.protected && (
              <span className="lockb">
                <Icon name="lock" />
                Protected
              </span>
            )}
          </span>
          <span className="ch">
            <Health cluster={c} />
            {i < 9 && <kbd>{i + 1}</kbd>}
          </span>
          <span className="cm">
            {[c.environment, c.region].filter(Boolean).join(", ")}
            {!c.connected && " · disconnected"}
          </span>
        </button>
      ))}
      <Link to="/fleet" className="clrow clrow-fleet" onClick={close}>
        <span className="sw">
          <Icon name="globe" />
        </span>
        <span className="cn">Fleet overview</span>
        <span className="ch">
          <kbd>g</kbd>
          <kbd>f</kbd>
        </span>
        <span className="cm">Every cluster at a glance</span>
      </Link>
    </Modal>
  );
}
