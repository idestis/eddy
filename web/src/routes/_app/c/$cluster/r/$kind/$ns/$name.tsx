import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { lazy, type ReactNode, Suspense, useEffect, useRef, useState } from "react";
import { z } from "zod";
import { resourcesQuery, threadsQuery, useCluster, useMe } from "../../../../../../../api/queries";
import type { ResourceRef } from "../../../../../../../api/types";
import { AskAIPanel } from "../../../../../../../components/AskAI";
import { ChildrenTree } from "../../../../../../../components/ChildrenTree";
import { Empty } from "../../../../../../../components/Empty";
import type { GraphMeta } from "../../../../../../../components/graph/GraphView";
import { LogsView } from "../../../../../../../components/LogsView";
import {
  Conditions,
  EventsList,
  ResourceActions,
  ResourceFacts,
  ResourceHeader,
  SectionTitle,
  YamlView,
} from "../../../../../../../components/ResourceParts";
import { Screen } from "../../../../../../../components/Screen";
import { KeyHint } from "../../../../../../../components/Status";
import { TabIndicator, useTabIndicator } from "../../../../../../../components/TabIndicator";
import { ResourceThreads } from "../../../../../../../components/Threads";
import { useToast } from "../../../../../../../components/Toasts";
import { SectionToolbar, type ViewOption, ViewSwitch } from "../../../../../../../components/ViewSwitch";
import { WorkloadLogsView } from "../../../../../../../components/WorkloadLogsView";
import { useAppState } from "../../../../../../../lib/appState";
import { type KeyId, useKeys } from "../../../../../../../lib/keys";
import { kindInfo } from "../../../../../../../lib/kinds";
import { DETAIL_VIEWS, type DetailView, detailLink, nsFromParam } from "../../../../../../../lib/links";
import { recallList } from "../../../../../../../lib/listMemory";
import { useResourceActions } from "../../../../../../../lib/useResourceActions";
import { type ManagesView, setViewPrefs, useViewPrefs } from "../../../../../../../lib/viewPrefs";
import { WORKLOAD_LOG_KINDS } from "../../../../../../../lib/workloadLogs";

// The graph and its layout code load only when the Graph toggle is on.
const LineageGraph = lazy(() =>
  import("../../../../../../../components/graph").then((m) => ({ default: m.LineageGraph })),
);

export const Route = createFileRoute("/_app/c/$cluster/r/$kind/$ns/$name")({
  validateSearch: z.object({
    view: z.enum(DETAIL_VIEWS).optional().catch(undefined),
    compose: z.boolean().optional().catch(undefined),
    // A thread to open in the Threads tab, such as one just saved from an Ask AI answer.
    thread: z.string().optional().catch(undefined),
    // The API group, for a kind outside the registry that two groups may share ("core" is the core group).
    group: z.string().optional().catch(undefined),
  }),
  component: DetailPage,
});

const MANAGES_VIEWS: ReadonlyArray<ViewOption<ManagesView>> = [
  { value: "tree", label: "Tree", icon: "list", title: "Ownership tree" },
  {
    value: "graph",
    label: "Graph",
    icon: "graph",
    title: "Dependencies, sources and managed objects as a graph",
  },
  { value: "outline", label: "Outline", icon: "outline", title: "The graph as a list, step by step" },
];

const TAB_LABEL: Record<DetailView, string> = {
  overview: "Overview",
  yaml: "YAML",
  events: "Events",
  logs: "Logs",
  threads: "Threads",
};

const TAB_KEY: Record<DetailView, KeyId> = {
  overview: "tabOverview",
  yaml: "tabYaml",
  events: "tabEvents",
  logs: "logs",
  threads: "tabThreads",
};

function DetailPage() {
  const params = Route.useParams();
  const { view = "overview", compose = false, group, thread } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const cluster = useCluster(params.cluster);
  const { data: me } = useMe();
  const toast = useToast();
  const { setSelection } = useAppState();
  const actions = useResourceActions(cluster);
  const namespace = nsFromParam(params.ns);
  const tabList = useTabIndicator(view);
  const managesView = useViewPrefs().managesView ?? "tree";
  // The node picked in the Manages graph; r and R act on it there.
  const [graphPick, setGraphPick] = useState<string | undefined>();

  const { data, isPending } = useQuery({
    ...resourcesQuery(params.cluster),
    enabled: Boolean(cluster?.connected || cluster?.stale),
  });
  const items = data?.items ?? [];
  const found = items.find(
    (x) =>
      x.kind === params.kind &&
      x.namespace === namespace &&
      x.name === params.name &&
      (group === undefined || (x.group || "core") === group),
  );
  const r = found;
  // Remember that this page showed the object, so its disappearance reads as a deletion
  // (the list deletes it live) rather than as a bad link.
  const pageKey = `${params.cluster}/${params.kind}/${namespace}/${params.name}`;
  const [seen, setSeen] = useState<string | undefined>();
  useEffect(() => {
    if (found) setSeen(pageKey);
  }, [found, pageKey]);
  const deleted = !found && seen === pageKey && Boolean(data);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new page starts with nothing picked
  useEffect(() => setGraphPick(undefined), [pageKey]);
  const target: ResourceRef | undefined = r && {
    cluster: params.cluster,
    group: r.group,
    kind: r.kind,
    namespace: r.namespace,
    name: r.name,
  };
  const threads = useQuery({
    ...threadsQuery({
      cluster: params.cluster,
      kind: params.kind,
      namespace,
      name: params.name,
      type: "discussion",
    }),
    enabled: Boolean(r),
  });
  const openThreads = threads.data?.items.filter((t) => t.status === "open").length ?? 0;

  useEffect(() => {
    setSelection({ cluster: params.cluster, resource: r });
  }, [params.cluster, r, setSelection]);
  useEffect(() => () => setSelection(null), [setSelection]);

  const isPod = params.kind === "Pod";
  const isWorkload = WORKLOAD_LOG_KINDS.has(params.kind) && Boolean(me?.features.workloadLogs);
  const hasLogs = (isPod && Boolean(me?.features.logs)) || isWorkload;
  // Inventory-only objects have YAML and Events (read as the user) but no logs.
  const tabs = DETAIL_VIEWS.filter((v) => v !== "logs" || hasLogs);
  // The panel slides in from the side of the tab that was picked.
  const lastView = useRef(view);
  const panelDir = useRef<"next" | "prev" | undefined>(undefined);
  if (lastView.current !== view) {
    panelDir.current = tabs.indexOf(view) >= tabs.indexOf(lastView.current) ? "next" : "prev";
    lastView.current = view;
  }
  const setView = (v: DetailView, extra: { compose?: boolean } = {}) =>
    void navigate({ search: { view: v === "overview" ? undefined : v, ...extra }, replace: true });

  const back = () => {
    const place = recallList(params.cluster);
    void navigate({ to: "/c/$cluster", params: { cluster: params.cluster }, search: place.search });
  };

  const fluxRoot = Boolean(r && kindInfo(r.kind).flux);
  const graphOn = view === "overview" && managesView !== "tree" && fluxRoot;
  // One line: "Manages" and its caption, then [Tree | Graph | Outline] and the graph's size.
  const managesHeader = (meta?: GraphMeta, extra?: ReactNode) => {
    const size = meta
      ? `${meta.nodes} nodes · ${meta.edges} edges${meta.truncated ? " · truncated" : ""}`
      : undefined;
    return (
      <SectionToolbar
        title="Manages"
        caption={graphOn ? "Upstream ← this object → downstream" : undefined}
        extra={extra}
        meta={size}
        views={
          fluxRoot ? (
            <ViewSwitch
              legend="Show as"
              value={graphOn ? managesView : "tree"}
              options={MANAGES_VIEWS}
              title={size}
              onChange={(v) => setViewPrefs({ managesView: v })}
            />
          ) : undefined
        }
      />
    );
  };
  const actTarget = (graphOn && graphPick && items.find((x) => x.id === graphPick)) || r;
  useKeys({
    back,
    reconcile: () => actions.reconcile(actTarget),
    reconcileSource: () => actions.reconcile(actTarget, true),
    suspend: () => actions.toggleSuspend(r),
    logs: () =>
      hasLogs ? setView("logs") : toast("Logs are available on pods and workloads. Open one from the tree."),
    tabThreads: () => setView("threads"),
    // `c` composes only where the composer is: on the Threads tab.
    compose: view === "threads" && Boolean(r) && (() => setView("threads", { compose: true })),
    owner: () =>
      r?.owner ? void navigate(detailLink(params.cluster, r.owner)) : toast("Nothing owns this object."),
    tabOverview: () => setView("overview"),
    tabEvents: () => setView("events"),
    tabYaml: () => setView("yaml"),
  });

  if (!cluster) return null;

  const crumbs = [
    <Link
      key="c"
      to="/c/$cluster"
      params={{ cluster: params.cluster }}
      search={recallList(params.cluster).search}
    >
      {params.cluster}
    </Link>,
    <Link key="k" to="/c/$cluster" params={{ cluster: params.cluster }} search={{ kind: params.kind }}>
      {kindInfo(params.kind).plural}
    </Link>,
    <span key="n" className="px-[7px] py-[5px] font-mono text-14 font-semibold text-ink" aria-current="page">
      {params.name}
    </span>,
  ];

  let body: ReactNode;
  if (!cluster.connected && !r) {
    body = <Empty title={`${cluster.name} is disconnected`}>Details appear when its agent reconnects.</Empty>;
  } else if (isPending && !r) {
    body = <Empty>Loading…</Empty>;
  } else if (deleted) {
    body = (
      <div className="anim-fade-in">
        <Empty title={`${params.kind}/${params.name} was deleted`}>
          It was removed from {params.cluster} while you were looking at it.{" "}
          <button type="button" className="linkbtn" onClick={back}>
            Back to the list
          </button>
        </Empty>
      </div>
    );
  } else if (!r || !target) {
    body = (
      <Empty title={`${params.kind}/${params.name} was not found`}>
        It may have been deleted, or you may not be allowed to see it.{" "}
        <button type="button" className="linkbtn" onClick={back}>
          Back to the list
        </button>
      </Empty>
    );
  } else {
    body = (
      <div className="@container stale-able flex flex-col" data-stale={!cluster.connected}>
        <ResourceHeader r={r} large cluster={params.cluster} />
        <ResourceActions cluster={cluster} r={r} actions={actions} />
        <div
          ref={tabList.list}
          className="no-scrollbar relative mb-3.5 flex gap-0.5 overflow-x-auto border-b border-line"
          role="tablist"
          aria-label="Details"
        >
          <TabIndicator ref={tabList.indicator} variant="underline" />
          {tabs.map((t) => (
            <button
              type="button"
              role="tab"
              key={t}
              aria-selected={view === t}
              onClick={() => setView(t)}
              className="flex items-center gap-1.5 px-2.5 py-[9px] text-13 whitespace-nowrap text-ink-3 transition-colors duration-(--duration-base) hover:text-ink-2 aria-selected:text-ink"
            >
              {TAB_LABEL[t]}
              {t === "threads" && openThreads > 0 && (
                <span className="text-12 font-semibold text-run tabular-nums">{openThreads}</span>
              )}
              <KeyHint id={TAB_KEY[t]} />
            </button>
          ))}
        </div>
        <div
          key={view}
          role="tabpanel"
          aria-label={TAB_LABEL[view]}
          className="tab-panel"
          data-dir={panelDir.current}
        >
          {view === "overview" && (
            <>
              <ResourceFacts cluster={params.cluster} r={r} grid />
              <div className="grid grid-cols-1 gap-x-6 @4xl:grid-cols-2">
                {!r.inventoryOnly && (
                  <section>
                    <SectionTitle>Conditions</SectionTitle>
                    <Conditions r={r} />
                  </section>
                )}
                <section>
                  <SectionTitle>Recent events</SectionTitle>
                  <EventsList cluster={params.cluster} r={r} limit={5} />
                </section>
              </div>
              {!r.inventoryOnly &&
                (graphOn ? (
                  <Suspense fallback={managesHeader()}>
                    <LineageGraph
                      cluster={cluster}
                      root={r}
                      items={items}
                      selectedId={graphPick}
                      onSelect={setGraphPick}
                      onOpen={(ref) => void navigate(detailLink(params.cluster, ref))}
                      actions={actions}
                      outline={managesView === "outline"}
                      header={managesHeader}
                    />
                  </Suspense>
                ) : (
                  <>
                    {managesHeader()}
                    <ChildrenTree cluster={params.cluster} root={r} items={items} />
                  </>
                ))}
            </>
          )}
          {view === "yaml" && <YamlView cluster={params.cluster} r={r} />}
          {view === "events" && <EventsList cluster={params.cluster} r={r} />}
          {view === "logs" && isPod && <LogsView cluster={params.cluster} pod={r} />}
          {view === "logs" && !isPod && isWorkload && (
            <WorkloadLogsView cluster={params.cluster} workload={r} />
          )}
          {view === "threads" && (
            <ResourceThreads
              target={target}
              compose={compose}
              openId={thread}
              onCompose={(open) => setView("threads", { compose: open || undefined })}
            />
          )}
        </div>
      </div>
    );
  }

  return (
    <Screen
      cluster={params.cluster}
      title={params.name}
      crumbs={crumbs}
      aside={<AskAIPanel cluster={cluster} resource={r} />}
    >
      {body}
    </Screen>
  );
}
