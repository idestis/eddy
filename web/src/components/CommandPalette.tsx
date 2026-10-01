import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { isAttentionRow } from "../api/delta";
import {
  attentionQuery,
  type FleetResource,
  resourcesQuery,
  searchQuery,
  useCluster,
  useFleetResources,
  useMe,
} from "../api/queries";
import type { ClusterInfo, SearchResponse } from "../api/types";
import { useAppState } from "../lib/appState";
import { clusterStyle } from "../lib/clusterColor";
import { STATUS_RANK } from "../lib/format";
import { highlightParts, type Match, type Ranges, rank } from "../lib/fuzzy";
import type { KeyId } from "../lib/keys";
import { flatNav, isFlux, isNotable, kindInfo, projectName } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { useOrderedClusters, usePrefs } from "../lib/prefs";
import { toggleTheme } from "../lib/theme";
import { useNav } from "../lib/useNav";
import { useResourceActions } from "../lib/useResourceActions";
import { ClusterTile } from "./ClusterSwitch";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";
import { KeyHint, Spinner, StatusIcon } from "./Status";
import { TabIndicator, useTabIndicator } from "./TabIndicator";

interface PaletteCommand {
  id: string;
  label: string;
  sub?: string;
  /** Extra words that match in search but are not shown. */
  keywords: string;
  icon?: IconName;
  cluster?: ClusterInfo;
  /** The digit that switches to this cluster. */
  digit?: number;
  /** The cluster you are in: dimmed, badged and listed last. */
  current?: boolean;
  keys?: KeyId;
  run: () => void;
}

export type PaletteScope = "cluster" | "all";

const MAX_RESOURCES = 30;
const MAX_COMMANDS = 6;
/** Quiet time before an "All clusters" query goes to the hub. */
export const SEARCH_DEBOUNCE_MS = 120;

/** Renders text with the matched characters marked. */
function Highlight({ text, ranges }: { text: string; ranges: Ranges | undefined }) {
  if (!ranges?.length) return <>{text}</>;
  return (
    <>
      {highlightParts(text, ranges).map((p, i) =>
        // biome-ignore lint/suspicious/noArrayIndexKey: parts are positional
        p.hit ? <mark key={i}>{p.text}</mark> : <span key={i}>{p.text}</span>,
      )}
    </>
  );
}

const resourceFields = ({ cluster, resource: r }: FleetResource) => ({
  primary: r.name,
  secondary: [r.namespace, r.kind, kindInfo(r.kind).abbr, cluster],
});

/** Failing first, then the current cluster, when two matches are equally good. */
const tiebreakFor = (current: string | undefined) => (a: FleetResource, b: FleetResource) =>
  (a.resource.inventoryOnly ? 9 : STATUS_RANK[a.resource.status]) -
    (b.resource.inventoryOnly ? 9 : STATUS_RANK[b.resource.status]) ||
  Number(b.cluster === current) - Number(a.cluster === current);

/** A resource hit, ranked locally (fuzzy.ts) or by the hub (GET /search). */
interface Hit {
  item: FleetResource & { stale?: boolean };
  match?: Match;
}

/** `value` after `ms` without a change. */
export function useDebounced<T>(value: T, ms: number): T {
  const [out, setOut] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setOut(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return out;
}

const hitsOf = (res: SearchResponse | null | undefined): Hit[] =>
  (res?.items ?? []).map(({ cluster, resource, match, stale }) => ({
    item: { cluster, resource, stale },
    match: { score: match.score, primary: match.primary ?? [], secondary: match.secondary ?? [] },
  }));

/**
 * Resource search for the palette (ADR-0006). This cluster ranks the list the page already
 * loaded, so results are instant. All clusters (and this cluster when its list is not loaded,
 * as in a windowed cluster) asks GET /search after 120 ms without typing; a newer query aborts
 * the one in flight, and the previous results stay, dimmed, until it answers. A hub without
 * /search gets the old way: snapshots, ranked here.
 */
export function usePaletteResources(q: string, scoped: string | undefined, routeCluster: string | undefined) {
  const routeInfo = useCluster(routeCluster);
  const readable = Boolean(routeInfo?.connected || routeInfo?.stale);
  const debounced = useDebounced(q, SEARCH_DEBOUNCE_MS);
  const [missing, setMissing] = useState(false);
  const fleet = !scoped;
  // The cache only: the palette never loads a whole cluster, except from an old hub.
  const local = useQuery({
    ...resourcesQuery(routeCluster ?? ""),
    enabled: Boolean(scoped) && readable && missing,
  });
  const localLoaded = Boolean(scoped && local.data);
  const remote = useQuery({
    ...searchQuery({
      q: debounced,
      scope: fleet ? "fleet" : "cluster",
      cluster: routeCluster,
      limit: MAX_RESOURCES,
    }),
    enabled: !localLoaded && !missing && debounced !== "",
    placeholderData: keepPreviousData,
  });
  // The empty state's "Needs attention", from GET /attention unless the list is loaded.
  const attention = useQuery({
    ...attentionQuery(fleet ? "" : (routeCluster ?? "")),
    enabled: !localLoaded && q === "" && !missing,
  });
  useEffect(() => {
    if (remote.data === null || attention.data === null) setMissing(true);
  }, [remote.data, attention.data]);
  const snapshots = useFleetResources(fleet && missing);

  const localItems = useMemo(
    (): FleetResource[] =>
      routeCluster ? (local.data?.items ?? []).map((resource) => ({ cluster: routeCluster, resource })) : [],
    [local.data, routeCluster],
  );
  const pool = fleet
    ? missing
      ? snapshots.items
      : undefined
    : localLoaded || missing
      ? localItems
      : undefined;

  const hits = useMemo((): Hit[] => {
    if (!q) return [];
    if (pool) return rank(q, pool, resourceFields, MAX_RESOURCES, tiebreakFor(routeCluster));
    // Results for a query that is not the current one stay only while they can still match.
    return remote.data && (remote.isPlaceholderData || debounced !== q) && !q.startsWith(debounced)
      ? []
      : hitsOf(remote.data);
  }, [q, pool, routeCluster, remote.data, remote.isPlaceholderData, debounced]);

  const needsAttention = useMemo((): Hit[] => {
    const rows = pool
      ? pool
          .filter(({ resource: r }) => isAttentionRow(r) && r.status !== "suspended")
          .sort(tiebreakFor(routeCluster))
      : (attention.data?.items ?? []).filter(({ resource: r }) => r.status !== "suspended");
    return rows.slice(0, 6).map((item) => ({ item }));
  }, [pool, attention.data, routeCluster]);

  const remoteActive = !pool && !missing && q !== "";
  return {
    hits,
    needsAttention,
    /** A request is pending or in flight: show the spinner. */
    searching: remoteActive && (debounced !== q || remote.isFetching),
    /** The hits shown answer an older query. */
    outdated: remoteActive && (debounced !== q || remote.isPlaceholderData),
    partial: remoteActive ? (remote.data?.partial ?? []) : [],
    error: remoteActive && remote.isError && !remote.data ? remote.error : null,
  };
}

/**
 * ⌘K: search resources and commands. Inside a cluster it searches that cluster; Tab (or the
 * scope toggle) widens it to every cluster. On /fleet it searches every cluster. A leading
 * ":" limits results to commands.
 */
export function CommandPalette({
  initialQuery,
  routeCluster,
}: {
  initialQuery: string;
  routeCluster: string | undefined;
}) {
  const [query, setQuery] = useState(initialQuery);
  const [scope, setScope] = useState<PaletteScope>(routeCluster ? "cluster" : "all");
  const scopeSeg = useTabIndicator<HTMLFieldSetElement>(scope);
  const { closePalette, selection, setHelp, ask, startNewChat } = useAppState();
  const { clusters: clusterPrefs, togglePin } = usePrefs();
  const navigate = useNavigate();
  const { data: me } = useMe();
  const clusters = useOrderedClusters();
  const selCluster = useCluster(selection?.cluster);
  const actions = useResourceActions(selCluster);
  const selected = selection?.resource;
  const navTree = useNav(routeCluster ?? selection?.cluster, true);
  const scoped = scope === "cluster" && routeCluster ? routeCluster : undefined;

  const run = useCallback(
    (fn: () => void) => () => {
      closePalette();
      fn();
    },
    [closePalette],
  );

  const commands = useMemo<PaletteCommand[]>(() => {
    // Digits follow the displayed order (the same as the 1-9 shortcuts); the current cluster goes last.
    const switchTo: PaletteCommand[] = clusters.map((c, i) => ({
      id: `cluster:${c.name}`,
      label: c.name === routeCluster ? c.displayName || c.name : `Switch to ${c.displayName || c.name}`,
      sub: [c.environment, c.region, c.connected ? "" : "disconnected"].filter(Boolean).join(", "),
      keywords: `ctx cluster switch ${c.name} ${c.environment ?? ""}`,
      cluster: c,
      digit: i < 9 ? i + 1 : undefined,
      current: c.name === routeCluster,
      run: () => {
        if (c.name !== routeCluster) void navigate({ to: "/c/$cluster", params: { cluster: c.name } });
      },
    }));
    const out: PaletteCommand[] = [
      ...switchTo.filter((c) => !c.current),
      ...switchTo.filter((c) => c.current),
    ];
    if (selected && selection) {
      const info = kindInfo(selected.kind);
      const where = `${selected.kind} on ${selection.cluster}`;
      if (me?.features.ai) {
        out.push({
          id: "ask",
          label: `Ask AI about ${selected.name}`,
          keywords: "ai ask explain why claude",
          icon: "spark",
          keys: "ask",
          run: () => ask(),
        });
      }
      if (isFlux(selected.kind) && !actions.readOnly) {
        out.push({
          id: "reconcile",
          label: `Reconcile ${selected.name}`,
          sub: where,
          keywords: "sync reconcile flux",
          icon: "sync",
          keys: "reconcile",
          run: () => actions.reconcile(selected),
        });
        if (info.hasSource) {
          out.push({
            id: "reconcile-source",
            label: `Reconcile ${selected.name} with source`,
            sub: "Fetch the source first, then apply",
            keywords: "sync reconcile with source",
            icon: "sync",
            keys: "reconcileSource",
            run: () => actions.reconcile(selected, true),
          });
        }
        out.push({
          id: "suspend",
          label: `${selected.suspended ? "Resume" : "Suspend"} ${selected.name}`,
          sub: where,
          keywords: "suspend resume pause",
          icon: selected.suspended ? "play" : "pause",
          keys: "suspend",
          run: () => actions.toggleSuspend(selected),
        });
      }
      if (selected.kind === "Pod" && me?.features.logs) {
        out.push({
          id: "logs",
          label: `Show logs for ${selected.name}`,
          keywords: "logs tail",
          icon: "term",
          keys: "logs",
          run: () => void navigate(detailLink(selection.cluster, selected, "logs")),
        });
      }
      if (selected.owner) {
        const owner = { ...selected.owner };
        out.push({
          id: "owner",
          label: `Jump to the owner of ${selected.name}`,
          sub: `${owner.kind}/${owner.name}`,
          keywords: "trace owner parent",
          icon: "trace",
          keys: "owner",
          run: () => void navigate(detailLink(selection.cluster, owner)),
        });
      }
      out.push({
        id: "thread",
        label: `New thread about ${selected.name}`,
        keywords: "comment thread discuss note",
        icon: "chat",
        keys: "compose",
        run: () =>
          void navigate({
            ...detailLink(selection.cluster, selected),
            search: { view: "threads", compose: true },
          }),
      });
    }
    const navCluster = routeCluster ?? selection?.cluster;
    if (navCluster) {
      const params = { cluster: navCluster };
      const navInfo = clusters.find((c) => c.name === navCluster);
      if (navInfo) {
        const pinned = clusterPrefs.pins.includes(navInfo.name);
        out.push({
          id: "pin",
          label: pinned ? "Unpin cluster" : "Pin cluster",
          sub: `${navInfo.name}: ${pinned ? "stop keeping it first" : "keep it first everywhere"}`,
          keywords: "pin unpin star favorite favourite cluster top",
          icon: "star",
          run: () => togglePin(navInfo.name),
        });
      }
      if (me?.features.ai && routeCluster) {
        out.push({
          id: "new-chat",
          label: "New Ask AI chat",
          sub: `Start a fresh conversation about ${selected?.name ?? routeCluster}`,
          keywords: "ai ask new chat conversation clear reset",
          icon: "plus",
          run: () => startNewChat(),
        });
      }
      out.push({
        id: "attention",
        label: "Show what needs attention",
        sub: navCluster,
        keywords: ":failing failed errors attention",
        icon: "alert",
        run: () => void navigate({ to: "/c/$cluster", params, search: { status: "attention" } }),
      });
      for (const n of flatNav(navTree)) {
        out.push({
          id: `nav:${n.id}`,
          label: `Go to ${n.label}`,
          sub: navCluster,
          keywords: `show browse ${n.id} ${n.kinds.join(" ")}`,
          icon: n.icon,
          run: () => void navigate({ to: "/c/$cluster", params, search: { kind: n.id } }),
        });
      }
    }
    out.push(
      {
        id: "fleet",
        label: "Fleet overview",
        keywords: "fleet home all clusters",
        icon: "globe",
        keys: "fleet",
        run: () => void navigate({ to: "/fleet" }),
      },
      {
        id: "threads",
        label: "Open threads",
        keywords: "threads comments discussions",
        icon: "chat",
        keys: "threads",
        run: () => void navigate({ to: "/threads" }),
      },
      {
        id: "tokens",
        label: "Access tokens",
        sub: "Create a token for Claude Code or other MCP clients",
        keywords: "pat token mcp claude settings",
        icon: "key",
        keys: "tokens",
        run: () => void navigate({ to: "/settings/tokens" }),
      },
      {
        id: "audit",
        label: "My audit log",
        keywords: "audit history log",
        icon: "clock",
        keys: "audit",
        run: () => void navigate({ to: "/audit" }),
      },
      {
        id: "theme",
        label: "Toggle light and dark theme",
        keywords: "theme dark light appearance",
        icon: "moon",
        run: () => void toggleTheme(),
      },
      {
        id: "help",
        label: "Keyboard shortcuts",
        keywords: "help keys shortcuts",
        icon: "keyboard",
        keys: "help",
        run: () => setHelp(true),
      },
    );
    return out;
  }, [
    clusters,
    clusterPrefs.pins,
    togglePin,
    startNewChat,
    selected,
    selection,
    routeCluster,
    me,
    actions,
    navigate,
    navTree,
    ask,
    setHelp,
  ]);

  const raw = query.trim();
  const commandsOnly = /^[:>]/.test(raw);
  const q = raw.replace(/^[:>]\s*/, "");
  const clusterByName = useMemo(() => new Map(clusters.map((c) => [c.name, c])), [clusters]);
  const toggleScope = useCallback(() => setScope((s) => (s === "cluster" ? "all" : "cluster")), []);
  const found = usePaletteResources(commandsOnly ? "" : q, scoped, routeCluster);

  const digitsLive = query === "";

  const groups = useMemo((): PaletteGroup[] => {
    const out: PaletteGroup[] = [];
    const cmdEntry = ({ item: c, match }: { item: PaletteCommand; match?: Match }): PaletteEntry => ({
      value: c.id,
      run: run(c.run),
      className: c.current ? "opacity-60" : "",
      node: <CommandItem cmd={c} match={match} digitsLive={digitsLive} />,
    });
    const resEntry = ({ item: fr, match }: Hit): PaletteEntry => ({
      value: `${fr.cluster}/${fr.resource.id}`,
      run: run(() => void navigate(detailLink(fr.cluster, fr.resource))),
      className: fr.resource.inventoryOnly || fr.stale ? "opacity-60" : "",
      node: <ResourceItem item={fr} match={match} cluster={clusterByName.get(fr.cluster)} />,
    });
    const where = scoped ? scoped : "every cluster";

    if (!raw) {
      if (found.needsAttention.length)
        out.push({ heading: `Needs attention, ${where}`, entries: found.needsAttention.map(resEntry) });
      out.push({
        heading: "Clusters",
        entries: commands.filter((c) => c.cluster).map((item) => cmdEntry({ item })),
      });
      out.push({
        heading: "Commands",
        entries: commands
          .filter((c) => !c.cluster && !c.id.startsWith("nav:"))
          .slice(0, MAX_COMMANDS)
          .map((item) => cmdEntry({ item })),
      });
      return out;
    }

    const matchedCommands = rank(
      q,
      commands,
      (c) => ({ primary: c.label, secondary: [c.keywords] }),
      commandsOnly ? 50 : MAX_COMMANDS,
    );
    if (!commandsOnly) {
      if (found.hits.length || found.searching)
        out.push({
          heading: scoped ? `Resources in ${scoped}` : "Resources",
          entries: found.hits.map(resEntry),
          busy: found.searching,
          dim: found.outdated,
          note: found.partial.length ? `Skipped, too slow to answer: ${found.partial.join(", ")}` : undefined,
        });
      if (scoped) {
        out.push({
          heading: "Wider",
          entries: [
            {
              value: "scope-all",
              run: () => setScope("all"),
              node: (
                <>
                  <span className={ICON_BOX}>
                    <Icon name="globe" />
                  </span>
                  <span className="flex-1">
                    Search all clusters for <b className="font-semibold">“{raw}”</b>
                  </span>
                  <kbd>Tab</kbd>
                </>
              ),
            },
          ],
        });
      }
      if (raw.length > 3 && me?.features.ai && selection) {
        const cmd: PaletteCommand = {
          id: "ask-query",
          label: `Ask: ${raw}`,
          sub: `About ${selected?.name ?? selection.cluster}`,
          keywords: "",
          icon: "spark",
          run: () => ask(raw),
        };
        out.push({ heading: "Ask AI", entries: [cmdEntry({ item: cmd })] });
      }
    }
    if (matchedCommands.length) out.push({ heading: "Commands", entries: matchedCommands.map(cmdEntry) });
    return out;
  }, [
    raw,
    q,
    commandsOnly,
    commands,
    found,
    scoped,
    selection,
    selected,
    me,
    clusterByName,
    navigate,
    ask,
    run,
    digitsLive,
  ]);

  const onInputKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    // With nothing typed yet, 1-9 jump straight to that cluster (same digits as the shortcuts).
    if (query === "" && /^[1-9]$/.test(e.key) && !e.metaKey && !e.ctrlKey && !e.altKey) {
      const target = clusters[Number(e.key) - 1];
      if (target) {
        e.preventDefault();
        e.stopPropagation();
        closePalette();
        if (target.name !== routeCluster)
          void navigate({ to: "/c/$cluster", params: { cluster: target.name } });
        return;
      }
    }
    // Tab toggles the scope instead of leaving the input.
    if (e.key === "Tab" && !e.shiftKey && routeCluster) {
      e.preventDefault();
      e.stopPropagation();
      toggleScope();
    }
  };

  const placeholder = scoped
    ? `Search ${scoped}, or type : for commands`
    : "Search every cluster, or type : for commands";

  const empty = (
    <>
      {found.error
        ? `Search failed: ${found.error.message}`
        : `No resources or commands match “${query}”${scoped ? ` in ${scoped}` : ""}.`}
      {scoped && (
        <>
          {" "}
          Press <kbd>Tab</kbd> to search every cluster.
        </>
      )}
    </>
  );

  return (
    <Modal label="Command palette" onClose={closePalette} className="palette">
      <Listbox
        groups={groups}
        query={query}
        onQueryChange={setQuery}
        onInputKeyDown={onInputKeyDown}
        placeholder={placeholder}
        busy={found.searching}
        empty={empty}
        scopeToggle={
          routeCluster && (
            <fieldset
              ref={scopeSeg.list}
              className="relative flex shrink-0 rounded-tile border border-line bg-surface-sunken p-0.5"
              aria-label="Search scope"
            >
              <TabIndicator ref={scopeSeg.indicator} variant="raised" />
              {(["cluster", "all"] as const).map((s) => (
                <button
                  type="button"
                  key={s}
                  aria-pressed={scope === s}
                  onClick={() => setScope(s)}
                  className="seg-btn relative inline-flex h-7 items-center gap-1.5 rounded-lg px-2.5 text-12-5 font-medium whitespace-nowrap text-ink-2 transition-colors duration-(--duration-base) aria-pressed:text-ink"
                >
                  {s === "cluster" ? (
                    <>
                      <span className="size-2 rounded-full bg-c" />
                      {routeCluster}
                    </>
                  ) : (
                    <>
                      <Icon name="globe" className="size-3.5" />
                      All clusters
                    </>
                  )}
                </button>
              ))}
            </fieldset>
          )
        }
      />
      <div className="flex flex-wrap gap-4 border-t border-line px-3.5 py-[9px] text-12 text-ink-3 [&>span]:inline-flex [&>span]:items-center [&>span]:gap-1.5">
        <span>
          <kbd>↑</kbd>
          <kbd>↓</kbd> move
        </span>
        <span>
          <kbd>↵</kbd> open
        </span>
        {digitsLive && clusters.length > 0 && (
          <span>
            <kbd>1–9</kbd> switch
          </span>
        )}
        {routeCluster && (
          <span>
            <kbd>Tab</kbd> {scoped ? "all clusters" : `only ${routeCluster}`}
          </span>
        )}
        <span>
          <kbd>:</kbd> commands only
        </span>
        <span>
          <kbd>esc</kbd> close
        </span>
      </div>
    </Modal>
  );
}

/** One row of the palette: what it shows and what Enter (or a click) does. */
interface PaletteEntry {
  value: string;
  run: () => void;
  node: ReactNode;
  className?: string;
}

interface PaletteGroup {
  heading: string;
  entries: PaletteEntry[];
  /** A request for this group is in flight. */
  busy?: boolean;
  /** The entries answer an older query. */
  dim?: boolean;
  note?: string;
}

/**
 * The palette's combobox: the input keeps focus and owns the keys, the listbox shows the
 * active option through aria-activedescendant (the WAI-ARIA combobox pattern). ↑/↓ (and
 * Ctrl+N/P, Ctrl+J/K) move and wrap, Enter runs, the pointer selects on move. A new query
 * selects the first option; results arriving for the same query keep the selection.
 */
function Listbox({
  groups,
  query,
  onQueryChange,
  onInputKeyDown,
  placeholder,
  busy,
  empty,
  scopeToggle,
}: {
  groups: readonly PaletteGroup[];
  query: string;
  onQueryChange: (q: string) => void;
  onInputKeyDown: (e: KeyboardEvent<HTMLInputElement>) => void;
  placeholder: string;
  busy: boolean;
  empty: ReactNode;
  scopeToggle: ReactNode;
}) {
  const id = useId();
  const listRef = useRef<HTMLDivElement>(null);
  const flat = useMemo(() => groups.flatMap((g) => g.entries), [groups]);
  // undefined: the first option, whatever arrives first.
  const [picked, setPicked] = useState<string | undefined>();
  const [scrollTick, setScrollTick] = useState(0);
  const index = Math.max(
    0,
    flat.findIndex((e) => e.value === picked),
  );
  const active = flat[index];
  const optionId = (i: number) => `${id}-o${i}`;

  useEffect(() => {
    if (!scrollTick) return;
    listRef.current?.querySelector('[aria-selected="true"]')?.scrollIntoView?.({ block: "nearest" });
  }, [scrollTick]);

  const move = (delta: number) => {
    if (!flat.length) return;
    const next = flat[(index + delta + flat.length) % flat.length];
    setPicked(next?.value);
    setScrollTick((t) => t + 1);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    onInputKeyDown(e);
    if (e.defaultPrevented) return;
    const ctrl = e.ctrlKey && !e.metaKey && !e.altKey;
    if (e.key === "ArrowDown" || (ctrl && (e.key === "n" || e.key === "j"))) {
      e.preventDefault();
      move(1);
    } else if (e.key === "ArrowUp" || (ctrl && (e.key === "p" || e.key === "k"))) {
      e.preventDefault();
      move(-1);
    } else if (e.key === "Enter" && !e.nativeEvent.isComposing) {
      e.preventDefault();
      active?.run();
    }
  };

  let at = 0;
  return (
    <div className="flex min-h-0 flex-col">
      <div className="flex items-center gap-2.5 border-b border-line px-4 py-3 text-ink-3">
        {busy ? <Spinner className="text-ink-3" /> : <Icon name="search" />}
        <input
          role="combobox"
          aria-expanded="true"
          aria-controls={`${id}-list`}
          aria-activedescendant={active ? optionId(index) : undefined}
          aria-autocomplete="list"
          aria-busy={busy}
          value={query}
          onChange={(e) => {
            onQueryChange(e.target.value);
            setPicked(undefined);
          }}
          onKeyDown={onKeyDown}
          placeholder={placeholder}
          aria-label="Search"
          autoComplete="off"
          spellCheck={false}
          className="min-w-0 flex-1 border-0 bg-transparent text-16 text-ink outline-none placeholder:text-ink-3"
          // biome-ignore lint/a11y/noAutofocus: the palette opens to type into
          autoFocus
        />
        {scopeToggle}
      </div>
      <div ref={listRef} id={`${id}-list`} role="listbox" aria-label="Results" className="palette-list">
        {flat.length === 0 && !busy && (
          <div className="palette-empty" role="status">
            {empty}
          </div>
        )}
        {groups.map((g, gi) => {
          if (!g.entries.length && !g.busy) return null;
          const headingId = `${id}-g${gi}`;
          return (
            // biome-ignore lint/a11y/useSemanticElements: an option group inside a listbox is role="group"
            <div key={g.heading} role="group" aria-labelledby={headingId} aria-busy={g.busy || undefined}>
              <div id={headingId} className="palette-heading flex items-center gap-2">
                {g.heading}
                {g.busy && <span className="font-normal">searching…</span>}
              </div>
              {g.note && <div className="px-2.5 pb-1 text-11-5 text-ink-3">{g.note}</div>}
              <div className={g.dim ? "opacity-60 transition-opacity" : "transition-opacity"}>
                {g.entries.map((e) => {
                  const i = at++;
                  return (
                    // biome-ignore lint/a11y/useKeyWithClickEvents lint/a11y/useFocusableInteractive: the input keeps focus and handles the keys (aria-activedescendant)
                    <div
                      key={e.value}
                      id={optionId(i)}
                      role="option"
                      aria-selected={i === index}
                      data-value={e.value}
                      className={`${ITEM} cursor-pointer ${e.className ?? ""}`}
                      onMouseMove={() => i !== index && setPicked(e.value)}
                      onMouseDown={(ev) => ev.preventDefault()}
                      onClick={() => e.run()}
                    >
                      {e.node}
                    </div>
                  );
                })}
              </div>
            </div>
          );
        })}
      </div>
    </div>
  );
}

const ITEM = "flex min-h-[46px] w-full items-center gap-[11px] rounded-control px-2.5 py-2 text-left";
const ICON_BOX =
  "flex size-[26px] shrink-0 items-center justify-center rounded-lg bg-surface-sunken text-ink-2";

function CommandItem({
  cmd,
  match,
  digitsLive = false,
}: {
  cmd: PaletteCommand;
  match?: Match;
  /** The search box is empty, so 1-9 switch clusters; the digit hints fade out once you type. */
  digitsLive?: boolean;
}) {
  return (
    <>
      {cmd.cluster ? (
        <ClusterTile cluster={cmd.cluster} size="sm" />
      ) : (
        <span className={ICON_BOX}>{cmd.icon && <Icon name={cmd.icon} />}</span>
      )}
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate font-medium">
          <Highlight text={cmd.label} ranges={match?.primary} />
        </span>
        {cmd.sub && <span className="truncate text-12 text-ink-3">{cmd.sub}</span>}
      </span>
      {cmd.current ? (
        <span className="badge">current</span>
      ) : cmd.digit ? (
        <kbd
          className={`transition-opacity duration-150 ${digitsLive ? "opacity-100" : "opacity-0"}`}
          aria-hidden={!digitsLive}
        >
          {cmd.digit}
        </kbd>
      ) : cmd.keys ? (
        <KeyHint id={cmd.keys} />
      ) : null}
    </>
  );
}

function ResourceItem({
  item,
  match,
  cluster,
}: {
  item: FleetResource & { stale?: boolean };
  match?: Match;
  cluster?: ClusterInfo;
}) {
  const r = item.resource;
  const info = kindInfo(r.kind);
  return (
    <>
      <span className={ICON_BOX}>
        {r.inventoryOnly ? <Icon name={info.icon} label={r.kind} /> : <StatusIcon status={r.status} label />}
      </span>
      <span className="flex min-w-0 flex-1 flex-col">
        <span className="truncate font-mono text-13">
          <span className="mr-1.5 text-11 text-ink-3">{info.abbr}</span>
          <Highlight text={r.name} ranges={match?.primary} />
        </span>
        <span className="truncate text-12 text-ink-3">
          <Highlight text={r.kind} ranges={match?.secondary[1]} />
          {isNotable(r.project) && r.project && ` (${projectName(r.project)})`}
          {r.namespace && (
            <>
              {" in "}
              <Highlight text={r.namespace} ranges={match?.secondary[0]} />
            </>
          )}
          {r.inventoryOnly
            ? " · managed by Flux, not watched by Eddy"
            : r.status !== "ready" && r.message
              ? ` · ${r.message}`
              : ""}
        </span>
      </span>
      {item.stale && (
        <span className="badge" title={`${item.cluster} is disconnected; this is its last known state`}>
          stale
        </span>
      )}
      <span className="ctag" style={clusterStyle(cluster)}>
        {item.cluster}
      </span>
    </>
  );
}
