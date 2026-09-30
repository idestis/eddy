import { useNavigate, useParams } from "@tanstack/react-router";
import { type ReactNode, useEffect } from "react";
import { useCluster, useClusters, useMe } from "../api/queries";
import { StreamProvider } from "../api/stream";
import type { ClusterInfo } from "../api/types";
import { AppStateProvider, useAppState } from "../lib/appState";
import { applyClusterIdentity } from "../lib/clusterColor";
import { BINDINGS, type Binding, installKeyboard, useKeys, usePendingSequence } from "../lib/keys";
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

const BANNER = "flex shrink-0 items-center gap-2 border-b border-line px-4 py-[7px] text-12-5 font-semibold";
const BANNER_TEXT = "font-normal text-ink-2";

function Banners({ cluster }: { cluster: ClusterInfo | undefined }) {
  const { data: me } = useMe();
  if (!me) return null;
  return (
    <>
      {cluster?.mode === "local" && (
        <div className={`${BANNER} bg-warn/12 text-warn`} role="status">
          <Icon name="user" />
          Local mode
          <span className={BANNER_TEXT}>
            {`Acting as your kubeconfig identity (${cluster.context || cluster.name})`}
            {cluster.readOnly ? " · read-only" : " · writes enabled"}
          </span>
        </div>
      )}
      {me.features.devMode && (
        <div className={`${BANNER} bg-bad/12 text-bad`} role="alert">
          <Icon name="alert" />
          Dev mode
          <span className={BANNER_TEXT}>
            Fake login is enabled on this hub. Never run it like this in production.
          </span>
        </div>
      )}
      {me.features.ephemeralStore && (
        <div className={`${BANNER} bg-warn/12 text-warn`} role="status">
          <Icon name="alert" />
          Ephemeral store
          <span className={BANNER_TEXT}>
            Sessions, tokens and threads live in memory and are lost when the hub restarts.
          </span>
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

/** A small "g …" hint while the first key of a two-key sequence waits for the second. */
function PendingSequence() {
  const key = usePendingSequence();
  if (!key) return null;
  const next = (Object.values(BINDINGS) as Binding[]).flatMap((b) =>
    b.keys.filter((k) => k.startsWith(`${key} `)).map((k) => [k.slice(key.length + 1), b.label] as const),
  );
  return (
    <div
      className="pointer-events-none fixed bottom-6 left-1/2 z-50 flex -translate-x-1/2 items-center gap-3 rounded-full border border-line-strong bg-surface px-4 py-2 text-12-5 whitespace-nowrap text-ink-2 shadow-toast"
      role="status"
      aria-live="polite"
    >
      <span className="inline-flex items-center gap-1">
        <kbd>{key}</kbd>
        <span className="text-ink-3">…</span>
      </span>
      {next.map(([k, label]) => (
        <span key={k} className="inline-flex items-center gap-1.5">
          <kbd>{k}</kbd>
          <span className="text-ink-3">{label}</span>
        </span>
      ))}
    </div>
  );
}

function Frame({ children }: { children: ReactNode }) {
  const current = useRouteCluster();
  const cluster = useCluster(current);
  const { data: clusters = [] } = useClusters();
  const { help, clusterMenu, palette } = useAppState();

  useEffect(() => {
    applyClusterIdentity(cluster);
  }, [cluster]);

  // The window fills the viewport. The ribbon carries the cluster colour; protected
  // clusters get a thicker solid ribbon (never stripes) next to the lock badge.
  return (
    <div className="flex h-dvh w-full flex-col overflow-hidden bg-surface">
      <div
        className={`shrink-0 bg-c ${cluster?.protected ? "h-[5px]" : "h-[3px]"}`}
        data-protected={cluster?.protected ? "true" : undefined}
      />
      <Banners cluster={cluster} />
      <div className="flex min-h-0 flex-1">
        <Sidebar cluster={cluster} />
        {children}
      </div>
      <GlobalKeys />
      <PendingSequence />
      {palette !== null && <CommandPalette initialQuery={palette} routeCluster={current} />}
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
