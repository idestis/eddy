import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { z } from "zod";
import { hiddenJobsQuery, objectQuery, resourcesQuery, useCluster, useMe } from "../../../../api/queries";
import { type Resource, STATUSES } from "../../../../api/types";
import { ClusterCards } from "../../../../components/ClusterCards";
import { Empty } from "../../../../components/Empty";
import { FindingCallout } from "../../../../components/Findings";
import type { ClusterGraphHandle } from "../../../../components/graph";
import { Icon } from "../../../../components/Icon";
import { OverflowChips, type OverflowItem } from "../../../../components/OverflowChips";
import { RemovableChip } from "../../../../components/RemovableChip";
import { ResourceList, type ResourceListHandle } from "../../../../components/ResourceList";
import {
  EventsList,
  ResourceActions,
  ResourceFacts,
  ResourceHeader,
  SectionTitle,
} from "../../../../components/ResourceParts";
import { Screen } from "../../../../components/Screen";
import { SEG, SEG_BTN, SidePanel } from "../../../../components/SidePanel";
import { SkeletonRows, SkeletonText } from "../../../../components/Skeleton";
import { AttentionIcon, StatusIcon } from "../../../../components/Status";
import { TabIndicator, useTabIndicator } from "../../../../components/TabIndicator";
import { useToast } from "../../../../components/Toasts";
import { useAppState } from "../../../../lib/appState";
import { hiddenJobCount, warningFindings } from "../../../../lib/findings";
import { ago, STATUS_LABEL, thousands } from "../../../../lib/format";
import { useKeys } from "../../../../lib/keys";
import { filterLabel, isFluxFilter, type NavNode, navNode } from "../../../../lib/kinds";
import { type DetailView, detailLink } from "../../../../lib/links";
import { recallList, rememberList } from "../../../../lib/listMemory";
import { useClusterMotion, useRequested, withLeaving } from "../../../../lib/liveMotion";
import {
  buildRows,
  filterResources,
  type ListRow,
  type StatusFilter,
  statusCounts,
  summaryCounts,
} from "../../../../lib/resourceRows";
import { useListMode, useWindowedList } from "../../../../lib/useClusterList";
import { useNav } from "../../../../lib/useNav";
import { useResourceActions } from "../../../../lib/useResourceActions";
import { type ListView, resolvePref, setViewPrefs, useViewPrefs } from "../../../../lib/viewPrefs";
import { WORKLOAD_LOG_KINDS } from "../../../../lib/workloadLogs";

const searchSchema = z.object({
  filter: z.string().optional().catch(undefined),
  kind: z.string().optional().catch(undefined),
  namespace: z.string().optional().catch(undefined),
  status: z
    .enum([...STATUSES, "attention"])
    .optional()
    .catch(undefined),
  view: z.enum(["grouped", "flat", "graph", "outline"]).optional().catch(undefined),
});

// The graph and its layout code load only when a graph is shown.
const ClusterGraph = lazy(() =>
  import("../../../../components/graph").then((m) => ({ default: m.ClusterGraph })),
);

const VIEWS: ReadonlyArray<readonly [ListView, string, "layers" | "list" | "graph" | "outline", string]> = [
  ["grouped", "Grouped by kind", "layers", "Grouped"],
  ["flat", "Flat list", "list", "Flat"],
  ["graph", "Dependency graph: who goes first", "graph", "Graph"],
  ["outline", "The dependency graph as a list, step by step", "outline", "Outline"],
];

export const Route = createFileRoute("/_app/c/$cluster/")({
  validateSearch: searchSchema,
  component: ClusterPage,
});

function listLabel(
  nodes: readonly NavNode[],
  kind?: string,
  status?: StatusFilter,
  namespace?: string,
): string {
  const k = filterLabel(kind, nodes);
  const where = namespace ? ` in ${namespace}` : "";
  if (status === "attention") return kind ? `${k} needing attention${where}` : `Needs attention${where}`;
  if (status) return `${STATUS_LABEL[status]} ${k.toLowerCase()}${where}`;
  return `${k}${where}`;
}

const CHIPS = ["attention", "failed", "reconciling", "suspended", "completed"] as const;
type ChipStatus = (typeof CHIPS)[number];

/** Chip colours come from the status tokens; the active chip is tinted with its colour. */
const CHIP_TONE: Record<ChipStatus, string> = {
  attention: "aria-pressed:border-attn/60 aria-pressed:bg-attn/12 [&_.n]:text-attn",
  failed: "aria-pressed:border-bad/60 aria-pressed:bg-bad/12 [&_.n]:text-bad",
  reconciling: "aria-pressed:border-run/60 aria-pressed:bg-run/12 [&_.n]:text-run",
  suspended: "aria-pressed:border-off/60 aria-pressed:bg-off/12 [&_.n]:text-off",
  completed: "aria-pressed:border-ok/60 aria-pressed:bg-ok/12 [&_.n]:text-ok",
};

const TOOL_BTN =
  "inline-flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-[11px] text-12-5 whitespace-nowrap text-ink-2 hover:border-line-strong aria-pressed:border-c/60 aria-pressed:bg-c-soft aria-pressed:text-ink disabled:opacity-60";

const CHIP_BASE =
  "inline-flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-[11px] text-12-5 whitespace-nowrap text-ink-2 hover:border-line-strong aria-pressed:text-ink";

const CHIP_PICKER = `${CHIP_BASE} aria-pressed:border-c/60 aria-pressed:bg-c-soft`;

const statusLabel = (s: ChipStatus) => (s === "attention" ? "Not ready" : STATUS_LABEL[s]);

function StatusChip({
  status,
  count,
  active,
  onClick,
}: {
  status: ChipStatus;
  count: number;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`${CHIP_BASE} ${CHIP_TONE[status]}`}
    >
      {status === "attention" ? <AttentionIcon /> : <StatusIcon status={status} />}
      {statusLabel(status)}
      <span className="n font-semibold tabular-nums">{count}</span>
    </button>
  );
}

function ClusterPage() {
  const { cluster: name } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const cluster = useCluster(name);
  const nav = useNav(name, cluster?.connected);
  const { data: me } = useMe();
  const toast = useToast();
  const { setSelection, ask, pane, setPane } = useAppState();
  const actions = useResourceActions(cluster);
  const list = useRef<ResourceListHandle>(null);
  const graphRef = useRef<ClusterGraphHandle>(null);
  const filterInput = useRef<HTMLInputElement>(null);

  // A disconnected cluster whose last view the hub keeps (`stale`) stays readable.
  const readable = Boolean(cluster?.connected || cluster?.stale);
  // Counts, then the first page, then the rest (lib/useClusterList.ts). Above 25k rows the
  // list stays windowed and the hub filters; otherwise the full list loads behind the first page.
  const { mode, firstPage } = useListMode(name, readable);
  const windowed = mode === "windowed";
  const { data, isPending, error } = useQuery({
    ...resourcesQuery(name),
    enabled: readable && (mode === "legacy" || mode === "fill"),
  });
  // Until the full list is there, the rows are the first page (partial summaries).
  const partialRows = !data && (windowed || (mode === "fill" && Boolean(firstPage)));
  const listed = data?.items ?? (mode === "fill" ? firstPage : undefined);

  // Finished Jobs the agent hides: fetched on demand, a page at a time, only on the Jobs list.
  const namespace = search.namespace;
  const onJobs = search.kind === "Job";
  const hiddenTotal = onJobs ? hiddenJobCount(cluster, namespace) : 0;
  const findings = onJobs ? warningFindings(cluster, namespace) : [];
  const hiddenScope = `${name}/${namespace ?? ""}`;
  const [hiddenFor, setHiddenFor] = useState<string | undefined>();
  const showHidden = onJobs && hiddenFor === hiddenScope;
  const hidden = useInfiniteQuery({
    ...hiddenJobsQuery(name, namespace),
    enabled: showHidden && readable,
  });
  const hiddenPages = showHidden ? hidden.data?.pages : undefined;
  // The normal list stays the source of truth (SSE deltas patch it); pages add only the rows it lacks.
  const items = useMemo(() => {
    const base = listed ?? [];
    if (!hiddenPages) return base;
    const seen = new Set(base.map((r) => r.id));
    const extra: Resource[] = [];
    for (const r of hiddenPages.flatMap((p) => p.items)) {
      if (seen.has(r.id)) continue;
      seen.add(r.id);
      extra.push(r);
    }
    return extra.length ? [...base, ...extra] : base;
  }, [listed, hiddenPages]);
  const lastPage = hiddenPages?.[hiddenPages.length - 1];
  const loadedHidden = items.length - (listed?.length ?? 0);
  const remaining = lastPage?.hidden.next ? Math.max(0, lastPage.hidden.total - loadedHidden) : 0;

  // Typing stays instant; the 5k-row filter runs at a lower priority.
  const text = useDeferredValue(search.filter ?? "");
  // The URL wins when it names a view (shared links); otherwise the saved choice.
  const savedView = useViewPrefs().listView;
  const fluxPage = isFluxFilter(search.kind);
  const wanted = resolvePref(search.view, savedView, "grouped");
  // The graph (and its outline) is a view of Flux pages; elsewhere a saved one reads as grouped.
  const graphView = wanted === "graph" || wanted === "outline";
  // A windowed list is flat: the hub sorts it by kind, without group headers or a graph.
  const listView: ListView = windowed ? "flat" : graphView && !fluxPage ? "grouped" : wanted;
  const graphMode = listView === "graph" || listView === "outline";
  // A windowed list comes sorted by kind from the hub, without group headers.
  const grouped = listView !== "flat" && !windowed;
  const viewSeg = useTabIndicator<HTMLFieldSetElement>(listView);
  // Rows deleted by a live delta stay for their exit animation; counts never include them.
  const motion = useClusterMotion(name);
  const requested = useRequested(name);
  const shown = useMemo(() => withLeaving(items, motion.leaving), [items, motion.leaving]);
  const filtered = useMemo(
    () => filterResources(shown, { text, kind: search.kind, status: search.status, namespace }),
    [shown, text, search.kind, search.status, namespace],
  );
  const win = useWindowedList(
    name,
    { kind: search.kind, status: search.status, namespace, text },
    readable && windowed,
  );
  const built = useMemo(() => buildRows(filtered, grouped), [filtered, grouped]);
  const rows: ListRow[] = windowed ? win.rows : built;
  const filteredCount = windowed
    ? win.total
    : motion.leaving.size
      ? filtered.filter((r) => !motion.leaving.has(r.id)).length
      : filtered.length;
  // Counts follow the kind filter, so "Failed 3" on Workloads means three failing workloads.
  const inKind = useMemo(
    () => filterResources(items, { kind: search.kind, namespace }),
    [items, search.kind, namespace],
  );
  // Counts come first (ADR-0006): until the rows arrive, the chips and the total use the
  // cluster's counts, which `/clusters` and the stream already carry.
  const summary = data ? undefined : summaryCounts(cluster, { kind: search.kind, namespace });
  const loaded = useMemo(() => statusCounts(inKind), [inKind]);
  const counts = (windowed ? win.counts : undefined) ?? summary?.counts ?? loaded;
  const resourceRows = useMemo(
    () => rows.flatMap((r) => (r.type === "resource" && !motion.leaving.has(r.key) ? [r.resource] : [])),
    [rows, motion.leaving],
  );

  const [selectedId, setSelectedId] = useState<string | undefined>(() => recallList(name).selectedId);
  // In the graph, any Flux object (or nothing, for a group node) can be selected.
  const picked = graphMode
    ? items.find((r) => r.id === selectedId)
    : (resourceRows.find((r) => r.id === selectedId) ?? resourceRows[0]);
  // A row from the paged list carries the list columns only; its details load on selection.
  const detail = useQuery({
    ...objectQuery(name, picked ?? { group: "", kind: "", namespace: "", name: "" }),
    enabled: partialRows && Boolean(picked),
  });
  const selected = partialRows && picked && detail.data?.id === picked.id ? detail.data : picked;

  useEffect(() => {
    setSelection({ cluster: name, resource: selected });
    rememberList(name, { search, selectedId: selected?.id });
  }, [name, selected, search, setSelection]);
  useEffect(() => () => setSelection(null), [setSelection]);

  const setSearch = useCallback(
    (patch: Partial<z.infer<typeof searchSchema>>) =>
      void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true }),
    [navigate],
  );

  const statusItems = useMemo<OverflowItem[]>(
    () =>
      CHIPS.map((s) => ({
        key: s,
        text: statusLabel(s),
        active: search.status === s,
        meta: counts[s],
        option: (
          <span className="inline-flex items-center gap-1.5">
            {s === "attention" ? <AttentionIcon /> : <StatusIcon status={s} />}
            {statusLabel(s)}
          </span>
        ),
        chip: (
          <StatusChip
            status={s}
            count={counts[s]}
            active={search.status === s}
            onClick={() => setSearch({ status: search.status === s ? undefined : s })}
          />
        ),
      })),
    [counts, search.status, setSearch],
  );

  const open = useCallback(
    (r: Resource | undefined, view?: DetailView) => {
      if (!r) return;
      setSelectedId(r.id);
      rememberList(name, { search, selectedId: r.id });
      void navigate(detailLink(name, r, view));
    },
    [name, search, navigate],
  );

  // [ and ] step through the sibling pages of the current nav entry (Deployments → StatefulSets…).
  const cycleSection = (delta: number) => {
    const at = navNode(search.kind, nav);
    const siblings = at ? at.siblings : nav;
    const i = at ? siblings.indexOf(at.node) : -1;
    const next = siblings[(i + delta + siblings.length) % siblings.length];
    if (next) void navigate({ search: (prev) => ({ ...prev, kind: next.id }), replace: true });
  };

  const move = (delta: number) => {
    if (!resourceRows.length) return;
    const i = selected ? resourceRows.indexOf(selected) : -1;
    const next = resourceRows[Math.max(0, Math.min(resourceRows.length - 1, i + delta))];
    if (next) setSelectedId(next.id);
  };

  useKeys({
    down: () => (graphMode ? graphRef.current?.move("down") : move(1)),
    up: () => (graphMode ? graphRef.current?.move("up") : move(-1)),
    top: () => !graphMode && setSelectedId(resourceRows[0]?.id),
    bottom: () => !graphMode && setSelectedId(resourceRows[resourceRows.length - 1]?.id),
    open: (e) => (graphMode && e.key !== "Enter" ? graphRef.current?.move("right") : open(selected)),
    back: (e) => {
      if (graphMode && e.key !== "Escape") graphRef.current?.move("left");
      else if (pane === "ai") setPane("details");
      else if (search.filter || search.kind || search.status || search.namespace)
        setSearch({ filter: undefined, kind: undefined, status: undefined, namespace: undefined });
    },
    filter: () => filterInput.current?.focus(),
    prevSection: () => cycleSection(-1),
    nextSection: () => cycleSection(1),
    reconcile: () => actions.reconcile(selected),
    reconcileSource: () => actions.reconcile(selected, true),
    suspend: () => actions.toggleSuspend(selected),
    logs: () =>
      selected?.kind === "Pod" ||
      (selected && WORKLOAD_LOG_KINDS.has(selected.kind) && me?.features.workloadLogs)
        ? open(selected, "logs")
        : toast("Logs are available on pods and workloads. Select one first."),
    tabThreads: () => {
      if (selected) void navigate(detailLink(name, selected, "threads"));
    },
    owner: () => {
      const owner = selected?.owner;
      if (!owner) return toast("This is a top-level object; nothing owns it.");
      const found = items.find(
        (r) => r.kind === owner.kind && r.namespace === owner.namespace && r.name === owner.name,
      );
      if (found && resourceRows.includes(found)) setSelectedId(found.id);
      else void navigate(detailLink(name, owner));
    },
    tabOverview: () => open(selected),
    tabEvents: () => open(selected, "events"),
    tabYaml: () => open(selected, "yaml"),
  });

  if (!cluster) return null;

  const showCards = !search.filter && !search.kind && !search.status && !namespace && Boolean(data);
  const title = listLabel(nav, search.kind, search.status, namespace);

  const preview = selected ? (
    <>
      <ResourceHeader r={selected} cluster={name} />
      <ResourceActions
        cluster={cluster}
        r={selected}
        actions={actions}
        onOpen={() => open(selected)}
        onLogs={() => open(selected, "logs")}
        onAsk={() => ask()}
      />
      <ResourceFacts cluster={name} r={selected} />
      <SectionTitle>Recent events</SectionTitle>
      <EventsList cluster={name} r={selected} limit={4} />
    </>
  ) : (
    <Empty title="Nothing selected">
      {graphMode ? "Click a node, or move with the arrow keys." : "Move with j and k."}
    </Empty>
  );

  return (
    <Screen
      cluster={name}
      title={search.kind || search.status || namespace ? title.toLowerCase() : undefined}
      fill
      crumbs={[
        <Link key="c" to="/c/$cluster" params={{ cluster: name }}>
          {name}
        </Link>,
        title,
      ]}
      aside={<SidePanel cluster={cluster} resource={selected} details={preview} />}
    >
      {showCards && <ClusterCards cluster={cluster} items={items} aiEnabled={Boolean(me?.features.ai)} />}
      {findings.map((f) => (
        <FindingCallout key={f.id} cluster={name} finding={f} link={!namespace} />
      ))}
      <div className="flex shrink-0 items-center gap-2.5">
        <label className="flex h-9 min-w-24 flex-[0_1_300px] sm:min-w-[180px] items-center gap-[7px] rounded-control border border-line bg-surface pr-2 pl-2.5 text-ink-3 focus-within:border-c">
          <Icon name="filter" />
          <input
            ref={filterInput}
            type="text"
            placeholder={graphMode ? "Find in the graph" : "Filter this list"}
            autoComplete="off"
            spellCheck={false}
            aria-label={graphMode ? "Find in the graph" : "Filter this list"}
            className="min-w-0 flex-1 border-0 bg-transparent text-14 text-ink outline-none placeholder:text-ink-3"
            value={search.filter ?? ""}
            onChange={(e) => setSearch({ filter: e.target.value || undefined })}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setSearch({ filter: undefined });
                if (graphMode) graphRef.current?.focus();
                else list.current?.focus();
              } else if (e.key === "Enter" || e.key === "ArrowDown") {
                e.preventDefault();
                if (graphMode) graphRef.current?.findFirst();
                else list.current?.focus();
              }
            }}
          />
          <kbd>/</kbd>
        </label>
        <OverflowChips
          label="Status"
          className="min-w-[5.5rem] flex-[1_1_0]"
          chipClass={CHIP_PICKER}
          items={statusItems}
          onToggle={(k) => setSearch({ status: search.status === k ? undefined : (k as ChipStatus) })}
          summary={(on) =>
            on[0] ? (
              <>
                {on[0].option}
                <span className="font-semibold tabular-nums">{on[0].meta}</span>
              </>
            ) : (
              "Status"
            )
          }
        />
        <span className="ml-auto shrink-0 text-12-5 whitespace-nowrap text-ink-3" aria-live="polite">
          {windowed ? (
            <span>{win.loading && !win.total ? "Counting…" : `${thousands(win.total)} resources`}</span>
          ) : !data && isPending && readable ? (
            summary ? (
              <span>
                <span className="max-sm:sr-only">{thousands(summary.total)} resources</span>
                <span className="sm:hidden">{thousands(summary.total)}</span>
              </span>
            ) : (
              <SkeletonText className="w-20" label="Counting resources" />
            )
          ) : (
            <>
              <span className="max-sm:sr-only">
                {filteredCount === items.length
                  ? `${items.length} resources`
                  : `${filteredCount} of ${items.length}`}
              </span>
              <span className="sm:hidden" aria-hidden="true">
                {filteredCount === items.length ? items.length : `${filteredCount}/${items.length}`}
              </span>
            </>
          )}
        </span>
        <fieldset ref={viewSeg.list} className={`${SEG} m-0 shrink-0`}>
          <legend className="sr-only">View</legend>
          <TabIndicator ref={viewSeg.indicator} variant="pill" />
          {VIEWS.filter(([v]) =>
            windowed ? v === "flat" : (v !== "graph" && v !== "outline") || fluxPage,
          ).map(([v, label, icon, short]) => (
            <button
              type="button"
              key={v}
              aria-pressed={v === listView}
              className={`${SEG_BTN} flex-none`}
              title={label}
              onClick={() => {
                setViewPrefs({ listView: v });
                setSearch({ view: undefined });
              }}
            >
              <Icon name={icon} className="size-3.5" />
              <span className="max-[1600px]:sr-only">{short}</span>
            </button>
          ))}
        </fieldset>
      </div>
      {!cluster.connected && cluster.stale && (
        <div
          className="flex shrink-0 items-center gap-2 rounded-tile border border-line bg-surface-sunken px-3 py-2 text-12-5 text-ink-2"
          role="status"
        >
          <span className="size-2 shrink-0 rounded-full bg-off" />
          <span>
            <b className="font-semibold">Stale</b> · last seen {ago(cluster.lastSeen)}. This is the last view
            the hub kept; actions are off until the agent reconnects.
          </span>
        </div>
      )}
      {(namespace || hiddenTotal > 0) && (
        <div className="flex shrink-0 flex-wrap items-center gap-2.5">
          {namespace && (
            <RemovableChip
              className="h-8 font-sans text-12-5"
              onRemove={() => setSearch({ namespace: undefined })}
              removeLabel={`Namespace ${namespace}, clear`}
              removeTitle="Clear the namespace filter"
            >
              <span className="inline-flex items-center gap-1.5">
                <span className="text-ink-3">Namespace</span>
                <span className="font-mono">{namespace}</span>
              </span>
            </RemovableChip>
          )}
          {hiddenTotal > 0 && (
            <button
              type="button"
              className={TOOL_BTN}
              aria-pressed={showHidden}
              onClick={() => setHiddenFor(showHidden ? undefined : hiddenScope)}
            >
              <Icon name={showHidden ? "check" : "clock"} className="size-3.5" />
              {showHidden && hidden.isPending
                ? "Loading finished Jobs…"
                : `Show finished Jobs (${thousands(hiddenTotal)} hidden)`}
            </button>
          )}
          {showHidden && remaining > 0 && (
            <button
              type="button"
              className={TOOL_BTN}
              disabled={hidden.isFetchingNextPage}
              onClick={() => void hidden.fetchNextPage()}
            >
              {hidden.isFetchingNextPage ? "Loading…" : `Load more (${thousands(remaining)} remaining)`}
            </button>
          )}
          {showHidden && hidden.error && (
            <span className="text-12-5 text-bad" role="alert">
              Couldn't load finished Jobs: {hidden.error.message}
            </span>
          )}
        </div>
      )}
      {graphMode && data ? (
        <Suspense fallback={<Empty>Loading the graph…</Empty>}>
          <div className="stale-able flex min-h-0 flex-1 flex-col" data-stale={!cluster.connected}>
            <ClusterGraph
              ref={graphRef}
              outline={listView === "outline"}
              cluster={cluster}
              items={items}
              namespace={namespace}
              status={search.status}
              text={text}
              selectedId={selectedId}
              onSelect={setSelectedId}
              onOpen={(r) => open(r)}
              actions={actions}
              onEscape={() => {
                if (search.filter || search.status || search.namespace) {
                  setSearch({ filter: undefined, status: undefined, namespace: undefined });
                  return true;
                }
                return false;
              }}
            />
          </div>
        </Suspense>
      ) : !readable && !data ? (
        <Empty title={`${name} is disconnected`}>Resources appear when its agent reconnects.</Empty>
      ) : windowed ? (
        win.loading && !win.total ? (
          <SkeletonRows label={`Loading resources of ${name}`} />
        ) : win.total === 0 ? (
          <Empty title={search.filter ? `Nothing matches “${search.filter}”` : "Nothing here"}>
            {search.filter || search.kind || search.status || namespace
              ? "Press Esc to clear the filter."
              : `${name} has no resources you can see.`}
          </Empty>
        ) : (
          <ResourceList
            ref={list}
            grouped={false}
            rows={rows}
            selectedId={selected?.id}
            onSelect={(r) => setSelectedId(r.id)}
            onOpen={(r) => open(r)}
            label={`${title} on ${name}`}
            requested={requested}
            stale={!cluster.connected}
            onRange={win.onRange}
          />
        )
      ) : mode === "probing" || (isPending && !listed) ? (
        <SkeletonRows label={`Loading resources of ${name}`} />
      ) : error ? (
        <Empty title="Couldn't load resources" alert>
          {error.message}
        </Empty>
      ) : rows.length === 0 ? (
        <Empty title={search.filter ? `Nothing matches “${search.filter}”` : "Nothing here"}>
          {search.filter || search.kind || search.status || namespace
            ? "Press Esc to clear the filter."
            : `${name} has no resources you can see.`}
        </Empty>
      ) : (
        <ResourceList
          ref={list}
          grouped={grouped}
          rows={rows}
          selectedId={selected?.id}
          onSelect={(r) => setSelectedId(r.id)}
          onOpen={(r) => open(r)}
          label={`${title} on ${name}`}
          motion={motion}
          requested={requested}
          stale={!cluster.connected}
        />
      )}
    </Screen>
  );
}
