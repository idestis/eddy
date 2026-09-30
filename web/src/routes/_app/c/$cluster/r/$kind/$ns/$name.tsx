import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useEffect } from "react";
import { z } from "zod";
import { resourcesQuery, threadsQuery, useCluster, useMe } from "../../../../../../../api/queries";
import type { ResourceRef } from "../../../../../../../api/types";
import { AskAIPanel } from "../../../../../../../components/AskAI";
import { ChildrenTree } from "../../../../../../../components/ChildrenTree";
import { Empty } from "../../../../../../../components/Empty";
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
import { ResourceThreads } from "../../../../../../../components/Threads";
import { useToast } from "../../../../../../../components/Toasts";
import { useAppState } from "../../../../../../../lib/appState";
import { useKeys } from "../../../../../../../lib/keys";
import { kindInfo } from "../../../../../../../lib/kinds";
import { DETAIL_VIEWS, type DetailView, detailLink, nsFromParam } from "../../../../../../../lib/links";
import { recallList } from "../../../../../../../lib/listMemory";
import { useResourceActions } from "../../../../../../../lib/useResourceActions";

export const Route = createFileRoute("/_app/c/$cluster/r/$kind/$ns/$name")({
  validateSearch: z.object({
    view: z.enum(DETAIL_VIEWS).optional().catch(undefined),
    compose: z.boolean().optional().catch(undefined),
  }),
  component: DetailPage,
});

const TAB_LABEL: Record<DetailView, string> = {
  overview: "Overview",
  yaml: "YAML",
  events: "Events",
  logs: "Logs",
  threads: "Threads",
};

const TAB_KEY: Partial<Record<DetailView, string>> = {
  overview: "o",
  yaml: "y",
  events: "e",
  logs: "L",
  threads: "t",
};

function DetailPage() {
  const params = Route.useParams();
  const { view = "overview", compose = false } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const cluster = useCluster(params.cluster);
  const { data: me } = useMe();
  const toast = useToast();
  const { setSelection } = useAppState();
  const actions = useResourceActions(cluster);
  const namespace = nsFromParam(params.ns);

  const { data, isPending } = useQuery({
    ...resourcesQuery(params.cluster),
    enabled: cluster?.connected ?? false,
  });
  const items = data?.items ?? [];
  const r = items.find((x) => x.kind === params.kind && x.namespace === namespace && x.name === params.name);
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
  const tabs = DETAIL_VIEWS.filter(
    (v) =>
      (v !== "logs" || (isPod && me?.features.logs)) &&
      // Inventory-only objects have a name and nothing else to show.
      !(r?.inventoryOnly && (v === "yaml" || v === "events" || v === "logs")),
  );
  const setView = (v: DetailView, extra: { compose?: boolean } = {}) =>
    void navigate({ search: { view: v === "overview" ? undefined : v, ...extra }, replace: true });

  const back = () => {
    const place = recallList(params.cluster);
    void navigate({ to: "/c/$cluster", params: { cluster: params.cluster }, search: place.search });
  };

  useKeys({
    back,
    reconcile: () => actions.reconcile(r),
    reconcileSource: () => actions.reconcile(r, true),
    suspend: () => actions.toggleSuspend(r),
    logs: () => (isPod ? setView("logs") : toast("Logs are available on pods. Open one from the tree.")),
    thread: () => setView("threads", { compose: true }),
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
  if (!cluster.connected) {
    body = <Empty title={`${cluster.name} is disconnected`}>Details appear when its agent reconnects.</Empty>;
  } else if (isPending) {
    body = <Empty>Loading…</Empty>;
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
      <div className="@container flex flex-col">
        <ResourceHeader r={r} large />
        <ResourceActions cluster={cluster} r={r} actions={actions} onLogs={() => setView("logs")} />
        <div
          className="no-scrollbar mb-3.5 flex gap-0.5 overflow-x-auto border-b border-line"
          role="tablist"
          aria-label="Details"
        >
          {tabs.map((t) => (
            <button
              type="button"
              role="tab"
              key={t}
              aria-selected={view === t}
              onClick={() => setView(t)}
              className="-mb-px flex items-center gap-1.5 border-b-2 border-transparent px-2.5 py-[9px] text-13 whitespace-nowrap text-ink-3 aria-selected:border-c aria-selected:font-semibold aria-selected:text-ink"
            >
              {TAB_LABEL[t]}
              {t === "threads" && openThreads > 0 && (
                <span className="text-12 font-semibold text-run tabular-nums">{openThreads}</span>
              )}
              {TAB_KEY[t] && <kbd>{TAB_KEY[t]}</kbd>}
            </button>
          ))}
        </div>
        <div role="tabpanel" aria-label={TAB_LABEL[view]}>
          {view === "overview" && (
            <>
              <ResourceFacts cluster={params.cluster} r={r} grid />
              <div className="grid grid-cols-1 gap-x-6 @4xl:grid-cols-2">
                <section>
                  <SectionTitle>Conditions</SectionTitle>
                  <Conditions r={r} />
                </section>
                <section>
                  <SectionTitle>Recent events</SectionTitle>
                  <EventsList cluster={params.cluster} r={r} limit={5} />
                </section>
              </div>
              <SectionTitle>Manages</SectionTitle>
              <ChildrenTree cluster={params.cluster} root={r} items={items} />
            </>
          )}
          {view === "yaml" && <YamlView cluster={params.cluster} r={r} />}
          {view === "events" && <EventsList cluster={params.cluster} r={r} />}
          {view === "logs" && isPod && <LogsView cluster={params.cluster} pod={r} />}
          {view === "threads" && (
            <ResourceThreads
              target={target}
              compose={compose}
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
