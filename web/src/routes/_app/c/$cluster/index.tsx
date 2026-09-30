import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { z } from "zod";
import { resourcesQuery, useCluster, useMe } from "../../../../api/queries";
import { type Resource, STATUSES } from "../../../../api/types";
import { ClusterCards } from "../../../../components/ClusterCards";
import { Empty } from "../../../../components/Empty";
import { Icon } from "../../../../components/Icon";
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
import { AttentionIcon, StatusIcon } from "../../../../components/Status";
import { useToast } from "../../../../components/Toasts";
import { useAppState } from "../../../../lib/appState";
import { STATUS_LABEL } from "../../../../lib/format";
import { useKeys } from "../../../../lib/keys";
import { filterLabel, NAV_TREE, navNode } from "../../../../lib/kinds";
import { type DetailView, detailLink } from "../../../../lib/links";
import { recallList, rememberList } from "../../../../lib/listMemory";
import { buildRows, filterResources, type StatusFilter, statusCounts } from "../../../../lib/resourceRows";
import { useResourceActions } from "../../../../lib/useResourceActions";

const searchSchema = z.object({
  filter: z.string().optional().catch(undefined),
  kind: z.string().optional().catch(undefined),
  status: z
    .enum([...STATUSES, "attention"])
    .optional()
    .catch(undefined),
  view: z.enum(["grouped", "flat"]).optional().catch(undefined),
});

export const Route = createFileRoute("/_app/c/$cluster/")({
  validateSearch: searchSchema,
  component: ClusterPage,
});

function listLabel(kind?: string, status?: StatusFilter): string {
  const k = filterLabel(kind);
  if (status === "attention") return kind ? `${k} needing attention` : "Needs attention";
  if (status) return `${STATUS_LABEL[status]} ${k.toLowerCase()}`;
  return k;
}

type ChipStatus = "attention" | "failed" | "reconciling" | "suspended";

/** Chip colours come from the status tokens; the active chip is tinted with its colour. */
const CHIP_TONE: Record<ChipStatus, string> = {
  attention: "aria-pressed:border-attn/60 aria-pressed:bg-attn/12 [&_.n]:text-attn",
  failed: "aria-pressed:border-bad/60 aria-pressed:bg-bad/12 [&_.n]:text-bad",
  reconciling: "aria-pressed:border-run/60 aria-pressed:bg-run/12 [&_.n]:text-run",
  suspended: "aria-pressed:border-off/60 aria-pressed:bg-off/12 [&_.n]:text-off",
};

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
      className={`inline-flex h-8 items-center gap-1.5 rounded-full border border-line bg-surface px-[11px] text-12-5 whitespace-nowrap text-ink-2 hover:border-line-strong aria-pressed:text-ink ${CHIP_TONE[status]} ${count === 0 && !active ? "opacity-45" : ""}`}
    >
      {status === "attention" ? <AttentionIcon /> : <StatusIcon status={status} />}
      {status === "attention" ? "Not ready" : STATUS_LABEL[status]}
      <span className="n font-semibold tabular-nums">{count}</span>
    </button>
  );
}

function ClusterPage() {
  const { cluster: name } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const cluster = useCluster(name);
  const { data: me } = useMe();
  const toast = useToast();
  const { setSelection, ask, pane, setPane } = useAppState();
  const actions = useResourceActions(cluster);
  const list = useRef<ResourceListHandle>(null);
  const filterInput = useRef<HTMLInputElement>(null);

  const { data, isPending, error } = useQuery({
    ...resourcesQuery(name),
    enabled: cluster?.connected ?? false,
  });
  const items = data?.items ?? [];

  // Typing stays instant; the 5k-row filter runs at a lower priority.
  const text = useDeferredValue(search.filter ?? "");
  const grouped = search.view !== "flat";
  const filtered = useMemo(
    () => filterResources(items, { text, kind: search.kind, status: search.status }),
    [items, text, search.kind, search.status],
  );
  const rows = useMemo(() => buildRows(filtered, grouped), [filtered, grouped]);
  // Counts follow the kind filter, so "Failed 3" on Workloads means three failing workloads.
  const inKind = useMemo(() => filterResources(items, { kind: search.kind }), [items, search.kind]);
  const counts = useMemo(() => statusCounts(inKind), [inKind]);
  const resourceRows = useMemo(
    () => rows.flatMap((r) => (r.type === "resource" ? [r.resource] : [])),
    [rows],
  );

  const [selectedId, setSelectedId] = useState<string | undefined>(() => recallList(name).selectedId);
  const selected = resourceRows.find((r) => r.id === selectedId) ?? resourceRows[0];

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
    const at = navNode(search.kind);
    const siblings = at ? at.siblings : NAV_TREE;
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
    down: () => move(1),
    up: () => move(-1),
    top: () => setSelectedId(resourceRows[0]?.id),
    bottom: () => setSelectedId(resourceRows[resourceRows.length - 1]?.id),
    open: () => open(selected),
    back: () => {
      if (pane === "ai") setPane("details");
      else if (search.filter || search.kind || search.status)
        setSearch({ filter: undefined, kind: undefined, status: undefined });
    },
    filter: () => filterInput.current?.focus(),
    prevSection: () => cycleSection(-1),
    nextSection: () => cycleSection(1),
    reconcile: () => actions.reconcile(selected),
    reconcileSource: () => actions.reconcile(selected, true),
    suspend: () => actions.toggleSuspend(selected),
    logs: () =>
      selected?.kind === "Pod"
        ? open(selected, "logs")
        : toast("Logs are available on pods. Select a pod first."),
    thread: () => {
      if (selected)
        void navigate({ ...detailLink(name, selected), search: { view: "threads", compose: true } });
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

  const showCards = !search.filter && !search.kind && !search.status && Boolean(data);
  const title = listLabel(search.kind, search.status);

  const preview = selected ? (
    <>
      <ResourceHeader r={selected} />
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
    <Empty title="Nothing selected">Move with j and k.</Empty>
  );

  return (
    <Screen
      cluster={name}
      title={search.kind || search.status ? title.toLowerCase() : undefined}
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
      <div className="flex shrink-0 flex-wrap items-center gap-2.5">
        <label className="flex h-9 min-w-40 flex-[0_1_300px] items-center gap-[7px] rounded-control border border-line bg-surface pr-2 pl-2.5 text-ink-3 focus-within:border-c">
          <Icon name="filter" />
          <input
            ref={filterInput}
            type="text"
            placeholder="Filter this list"
            autoComplete="off"
            spellCheck={false}
            aria-label="Filter this list"
            className="min-w-0 flex-1 border-0 bg-transparent text-14 text-ink outline-none placeholder:text-ink-3"
            value={search.filter ?? ""}
            onChange={(e) => setSearch({ filter: e.target.value || undefined })}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                setSearch({ filter: undefined });
                list.current?.focus();
              } else if (e.key === "Enter" || e.key === "ArrowDown") {
                e.preventDefault();
                list.current?.focus();
              }
            }}
          />
          <kbd>/</kbd>
        </label>
        <fieldset className="no-scrollbar flex min-w-0 gap-1.5 overflow-x-auto">
          <legend className="sr-only">Status</legend>
          {(["attention", "failed", "reconciling", "suspended"] as const).map((s) => (
            <StatusChip
              key={s}
              status={s}
              count={counts[s]}
              active={search.status === s}
              onClick={() => setSearch({ status: search.status === s ? undefined : s })}
            />
          ))}
        </fieldset>
        <span className="ml-auto text-12-5 whitespace-nowrap text-ink-3" aria-live="polite">
          {filtered.length === items.length
            ? `${items.length} resources`
            : `${filtered.length} of ${items.length}`}
        </span>
        <fieldset className={`${SEG} m-0 shrink-0`}>
          <legend className="sr-only">View</legend>
          {(
            [
              ["grouped", "Grouped by kind", "layers"],
              ["flat", "Flat list", "list"],
            ] as const
          ).map(([v, label, icon]) => (
            <button
              type="button"
              key={v}
              aria-pressed={(v === "grouped") === grouped}
              className={`${SEG_BTN} flex-none`}
              title={label}
              onClick={() => setSearch({ view: v === "grouped" ? undefined : "flat" })}
            >
              <Icon name={icon} className="size-3.5" />
              <span className="max-[1600px]:sr-only">{v === "grouped" ? "Grouped" : "Flat"}</span>
            </button>
          ))}
        </fieldset>
      </div>
      {!cluster.connected ? (
        <Empty title={`${name} is disconnected`}>Resources appear when its agent reconnects.</Empty>
      ) : isPending ? (
        <Empty>Loading resources…</Empty>
      ) : error ? (
        <Empty title="Couldn't load resources" alert>
          {error.message}
        </Empty>
      ) : rows.length === 0 ? (
        <Empty title={search.filter ? `Nothing matches “${search.filter}”` : "Nothing here"}>
          {search.filter || search.kind || search.status
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
        />
      )}
    </Screen>
  );
}
