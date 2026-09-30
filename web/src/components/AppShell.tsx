import { useNavigate, useParams } from "@tanstack/react-router";
import { type ReactNode, useEffect } from "react";
import { useCluster, useClusters, useMe } from "../api/queries";
import { StreamProvider } from "../api/stream";
import type { ClusterInfo } from "../api/types";
import { AppStateProvider, useAppState } from "../lib/appState";
import { applyClusterIdentity } from "../lib/clusterColor";
import { installKeyboard, useKeys } from "../lib/keys";
import { AskAIProvider } from "./AskAI";
import { ClusterMenu } from "./ClusterSwitch";
import { CommandPalette } from "./CommandPalette";
import { ConfirmProvider } from "./ConfirmDialog";
import { HelpOverlay } from "./HelpOverlay";
import { Icon } from "./Icon";
import { Sidebar } from "./Sidebar";
import { ToastProvider, useToast } from "./Toasts";

/** The cluster in the current URL, if any. */
export function useRouteCluster(): string | undefined {
  return useParams({ strict: false }).cluster;
}

function Banners({ cluster }: { cluster: ClusterInfo | undefined }) {
  const { data: me } = useMe();
  if (!me) return null;
  return (
    <>
      {cluster?.mode === "local" && (
        <div className="banner warn" role="status">
          <Icon name="user" />
          Local mode
          <span>
            {`Acting as your kubeconfig identity (${cluster.context || cluster.name})`}
            {cluster.readOnly ? " · read-only" : " · writes enabled"}
          </span>
        </div>
      )}
      {me.features.devMode && (
        <div className="banner danger" role="alert">
          <Icon name="alert" />
          Dev mode
          <span>Fake login is enabled on this hub. Never run it like this in production.</span>
        </div>
      )}
      {me.features.ephemeralStore && (
        <div className="banner warn" role="status">
          <Icon name="alert" />
          Ephemeral store
          <span>Sessions, tokens and threads live in memory and are lost when the hub restarts.</span>
        </div>
      )}
    </>
  );
}

function GlobalKeys() {
  const navigate = useNavigate();
  const toast = useToast();
  const { data: clusters = [] } = useClusters();
  const current = useRouteCluster();
  const { palette, openPalette, closePalette, help, setHelp, ask } = useAppState();

  const goCluster = (i: number) => {
    const c = clusters[(i + clusters.length) % clusters.length];
    if (c) void navigate({ to: "/c/$cluster", params: { cluster: c.name } });
  };
  const index = clusters.findIndex((c) => c.name === current);

  useKeys({
    palette: () => (palette === null ? openPalette() : closePalette()),
    commands: () => openPalette(":"),
    help: () => setHelp(!help),
    cluster: (e) => {
      const n = Number(e.key) - 1;
      if (clusters[n]) goCluster(n);
    },
    prevCluster: () => goCluster(index < 0 ? 0 : index - 1),
    nextCluster: () => goCluster(index + 1),
    fleet: () => void navigate({ to: "/fleet" }),
    threads: () => void navigate({ to: "/threads" }),
    tokens: () => void navigate({ to: "/settings/tokens" }),
    audit: () => void navigate({ to: "/audit" }),
    ask: () => (current ? ask() : toast("Open a cluster to ask AI about it.")),
  });
  return null;
}

function Frame({ children }: { children: ReactNode }) {
  const current = useRouteCluster();
  const cluster = useCluster(current);
  const { data: clusters = [] } = useClusters();
  const { help, clusterMenu, palette } = useAppState();

  useEffect(() => {
    applyClusterIdentity(cluster);
    document.title = cluster ? `${cluster.name}: Eddy` : "Eddy";
  }, [cluster]);

  return (
    <div className="frame">
      <div className="win">
        <div className="ribbon" />
        <Banners cluster={cluster} />
        <div className="shell">
          <Sidebar cluster={cluster} />
          {children}
        </div>
      </div>
      <GlobalKeys />
      {palette !== null && <CommandPalette initialQuery={palette} />}
      {help && <HelpOverlay />}
      {clusterMenu && <ClusterMenu clusters={clusters} current={current} />}
    </div>
  );
}

export function AppShell({ children }: { children: ReactNode }) {
  useEffect(() => installKeyboard(), []);
  return (
    <AppStateProvider>
      <ToastProvider>
        <ConfirmProvider>
          <StreamProvider>
            <AskAIProvider>
              <Frame>{children}</Frame>
            </AskAIProvider>
          </StreamProvider>
        </ConfirmProvider>
      </ToastProvider>
    </AppStateProvider>
  );
}
