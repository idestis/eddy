import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import { z } from "zod";
import { resourcesQuery, useCluster, useMe } from "../../../../api/queries";
import { type Resource, STATUSES } from "../../../../api/types";
import { ClusterCards } from "../../../../components/ClusterCards";
import { Icon } from "../../../../components/Icon";
import { ResourceList, type ResourceListHandle } from "../../../../components/ResourceList";
import {
  EventsList,
  ResourceActions,
  ResourceFacts,
  ResourceHeader,
} from "../../../../components/ResourceParts";
import { Screen } from "../../../../components/Screen";
import { SidePanel } from "../../../../components/SidePanel";
import { useToast } from "../../../../components/Toasts";
import { useAppState } from "../../../../lib/appState";
import { STATUS_LABEL } from "../../../../lib/format";
import { useKeys } from "../../../../lib/keys";
import { kindInfo, NAV_GROUPS } from "../../../../lib/kinds";
import { type DetailView, detailLink } from "../../../../lib/links";
import { recallList, rememberList } from "../../../../lib/listMemory";
import { buildRows, filterResources, type StatusFilter } from "../../../../lib/resourceRows";
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

function filterLabel(kind?: string, status?: StatusFilter): string {
  const k = kind ? (NAV_GROUPS.find((g) => g.id === kind)?.label ?? kindInfo(kind).plural) : "All resources";
  if (status === "attention") return kind ? `${k} needing attention` : "Needs attention";
  if (status) return `${STATUS_LABEL[status]} ${k.toLowerCase()}`;
  return k;
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
  const statusCounts = useMemo(() => {
    const counts = { attention: 0, failed: 0, reconciling: 0, suspended: 0 };
    for (const r of items) {
      if (r.status !== "ready") counts.attention++;
      if (r.status === "failed" || r.status === "reconciling" || r.status === "suspended") counts[r.status]++;
    }
    return counts;
  }, [items]);
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
  const title = filterLabel(search.kind, search.status);

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
      <h3 className="section-title">Recent events</h3>
      <EventsList cluster={name} r={selected} limit={4} />
    </>
  ) : (
    <div className="empty">
      <strong>Nothing selected</strong>
      Move with j and k.
    </div>
  );

  return (
    <Screen
      cluster={name}
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
      <div className="tools">
        <label className="filter">
          <Icon name="filter" />
          <input
            ref={filterInput}
            type="text"
            placeholder="Filter this list"
            autoComplete="off"
            spellCheck={false}
            aria-label="Filter this list"
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
        <fieldset className="chips">
          <legend className="sr-only">Status</legend>
          {(["attention", "failed", "reconciling", "suspended"] as const).map((s) => (
            <button
              type="button"
              key={s}
              className="chip"
              aria-pressed={search.status === s}
              onClick={() => setSearch({ status: search.status === s ? undefined : s })}
            >
              {s === "attention" ? "Not ready" : STATUS_LABEL[s]}
              <span className="n">{statusCounts[s]}</span>
            </button>
          ))}
          <button
            type="button"
            className="chip"
            aria-pressed={!grouped}
            onClick={() => setSearch({ view: grouped ? "flat" : undefined })}
            title="Group rows by kind"
          >
            {grouped ? "Grouped by kind" : "Flat list"}
          </button>
        </fieldset>
        <span className="cnt" aria-live="polite">
          {filtered.length === items.length
            ? `${items.length} resources`
            : `${filtered.length} of ${items.length}`}
        </span>
      </div>
      {!cluster.connected ? (
        <div className="empty">
          <strong>{name} is disconnected</strong>
          Resources appear when its agent reconnects.
        </div>
      ) : isPending ? (
        <div className="empty">Loading resources…</div>
      ) : error ? (
        <div className="empty" role="alert">
          <strong>Couldn't load resources</strong>
          {error.message}
        </div>
      ) : rows.length === 0 ? (
        <div className="empty">
          <strong>{search.filter ? `Nothing matches “${search.filter}”` : "Nothing here"}</strong>
          {search.filter || search.kind || search.status
            ? "Press Esc to clear the filter."
            : `${name} has no resources you can see.`}
        </div>
      ) : (
        <ResourceList
          ref={list}
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
