// The context of an Ask AI chat as chips that read like the rest of the UI: the kind badge,
// `namespace / name` in mono (namespace muted, name bold), and the cluster when it is not the
// current one. Also the search both the + picker and @mentions use: GET /search across the
// fleet (already filtered by the user's access), plus whole clusters by name.

import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { type KeyboardEvent, type ReactNode, useEffect, useId, useMemo, useRef, useState } from "react";
import { clustersQuery, searchQuery, useCluster } from "../api/queries";
import type { Resource, ResourceRef } from "../api/types";
import { clusterRef, contextKey, hasRef, isClusterRef, refLabel, resourceRef } from "../lib/chatContext";
import { clusterStyle } from "../lib/clusterColor";
import { kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { SEARCH_DEBOUNCE_MS, useDebounced } from "./CommandPalette";
import { Icon } from "./Icon";
import { CHIP_LABEL_CLASS, CHIP_LABEL_PROPS, RemovableChip } from "./RemovableChip";
import { Spinner, StatusIcon } from "./Status";

/** The list's kind abbreviation (KS, HR, PVC…). */
export function KindBadge({ kind }: { kind: string }) {
  return (
    <span className="shrink-0 rounded-[5px] border border-line bg-surface-sunken px-1 py-px font-mono text-10 leading-[1.4] font-semibold text-ink-2">
      {kindInfo(kind).abbr}
    </span>
  );
}

/** A cluster tinted with its colour. */
export function ClusterTag({ name, className = "" }: { name: string; className?: string }) {
  const info = useCluster(name);
  return (
    <span className={`ctag font-sans ${className}`} style={clusterStyle(info ?? { name, order: 0 })}>
      {info?.displayName || name}
    </span>
  );
}

/** `namespace / name`, the namespace muted and the name bold. */
export function RefName({ r }: { r: Pick<ResourceRef, "namespace" | "name"> }) {
  return (
    <span className="min-w-0 truncate font-mono">
      {r.namespace && (
        <>
          <span className="text-ink-3">{r.namespace}</span>
          <span className="text-ink-3"> / </span>
        </>
      )}
      <span className="font-semibold text-ink">{r.name}</span>
    </span>
  );
}

/** The chip's content: the cluster for a whole-cluster entry, else kind, name and a foreign cluster. */
export function RefContent({ r, current }: { r: ResourceRef; current?: string }) {
  if (isClusterRef(r)) return <ClusterTag name={r.cluster} className="-ml-1" />;
  return (
    <>
      <KindBadge kind={r.kind} />
      <RefName r={r} />
      {r.cluster !== current && <ClusterTag name={r.cluster} />}
    </>
  );
}

const linkTo = (r: ResourceRef) =>
  isClusterRef(r)
    ? ({ to: "/c/$cluster", params: { cluster: r.cluster } } as const)
    : detailLink(r.cluster, r);

/** One context entry: jumps to the resource, × removes it. Hidden ones are struck through. */
export function ContextChip({
  r,
  current,
  hidden,
  onRemove,
}: {
  r: ResourceRef;
  current?: string;
  hidden?: boolean;
  onRemove?: () => void;
}) {
  const label = refLabel(r);
  return (
    <RemovableChip
      onRemove={onRemove}
      removeLabel={`Remove ${label} from the chat`}
      removeTitle="Remove from the chat"
      className={hidden ? "border-dashed" : ""}
    >
      <Link
        {...CHIP_LABEL_PROPS}
        {...linkTo(r)}
        className={`${CHIP_LABEL_CLASS} ${hidden ? "line-through opacity-60 [&_*]:line-through" : ""}`}
        title={hidden ? "You no longer have access" : `Open ${label}`}
        aria-label={hidden ? `${label} (you no longer have access)` : `Open ${label}`}
        data-hidden={hidden || undefined}
      >
        <RefContent r={r} current={current} />
      </Link>
    </RemovableChip>
  );
}

/** "+ Add PVC data-0": the selection is not in the chat; a click adds it. */
export function SuggestChip({ r, onAdd }: { r: ResourceRef; onAdd: () => void }) {
  return (
    <button
      type="button"
      className="inline-flex min-h-[30px] min-w-0 max-w-full items-center gap-1.5 rounded-full border border-dashed border-line-strong px-2.5 text-12 text-ink-3 hover:border-c hover:text-ink focus-visible:ring-2 focus-visible:ring-c focus-visible:outline-none"
      onClick={onAdd}
      aria-label={isClusterRef(r) ? `Add ${r.cluster}` : `Add ${kindInfo(r.kind).abbr} ${r.name}`}
      title={`Add ${refLabel(r)} to this chat`}
    >
      <Icon name="plus" className="size-3.5 shrink-0" />
      <span className="shrink-0">Add</span>
      {isClusterRef(r) ? (
        <span className="truncate font-mono">{r.cluster}</span>
      ) : (
        <>
          <span className="shrink-0 font-mono text-10 font-semibold">{kindInfo(r.kind).abbr}</span>
          <span className="truncate font-mono">{r.name}</span>
        </>
      )}
    </button>
  );
}

// Search

export interface ContextOption {
  key: string;
  ref: ResourceRef;
  resource?: Resource;
  stale?: boolean;
}

const MAX_OPTIONS = 8;

/**
 * Options for a context query. Empty: what is on screen. Otherwise clusters by name and
 * GET /search hits across the fleet; `ns/name` narrows by namespace. Entries already in the
 * chat are left out.
 */
export function useContextOptions(
  query: string,
  {
    current,
    onScreen,
    exclude,
    enabled,
  }: {
    current?: string;
    onScreen: ReadonlyArray<{ ref: ResourceRef; resource?: Resource }>;
    exclude: readonly ResourceRef[];
    enabled: boolean;
  },
) {
  const q = query.trim();
  const slash = q.lastIndexOf("/");
  const ns = slash >= 0 ? q.slice(0, slash).toLowerCase() : "";
  const term = slash >= 0 ? q.slice(slash + 1) : q;
  const debounced = useDebounced(term, SEARCH_DEBOUNCE_MS);
  const clusters = useQuery({ ...clustersQuery, enabled });
  const remote = useQuery({
    ...searchQuery({ q: debounced, scope: "fleet", cluster: current, limit: 20 }),
    enabled: enabled && debounced !== "",
    placeholderData: keepPreviousData,
  });

  const options = useMemo((): ContextOption[] => {
    const out: ContextOption[] = [];
    const add = (o: Omit<ContextOption, "key">) => {
      if (out.length >= MAX_OPTIONS || hasRef(exclude, o.ref) || out.some((x) => x.key === contextKey(o.ref)))
        return;
      out.push({ ...o, key: contextKey(o.ref) });
    };
    if (!q) {
      for (const o of onScreen) add(o);
      if (current) add({ ref: clusterRef(current) });
      return out;
    }
    const lower = q.toLowerCase();
    for (const c of clusters.data ?? []) {
      if (c.name.toLowerCase().includes(lower) || c.displayName?.toLowerCase().includes(lower))
        add({ ref: clusterRef(c.name) });
    }
    if (!term) return out;
    for (const h of remote.data?.items ?? []) {
      if (ns && !h.resource.namespace.toLowerCase().startsWith(ns)) continue;
      add({ ref: resourceRef(h.cluster, h.resource), resource: h.resource, stale: h.stale });
    }
    return out;
  }, [q, ns, term, onScreen, current, exclude, clusters.data, remote.data]);

  return {
    options,
    searching: term !== "" && (debounced !== term || remote.isFetching),
    error: remote.isError && !remote.data,
  };
}

/** ↑/↓ wrap, the first option is active whenever the options change. */
export function useActiveOption(options: readonly ContextOption[]) {
  const [active, setActive] = useState(0);
  const sig = options.map((o) => o.key).join("|");
  // biome-ignore lint/correctness/useExhaustiveDependencies: new options start at the first
  useEffect(() => setActive(0), [sig]);
  const move = (delta: number) => {
    if (!options.length) return;
    setActive((i) => (i + delta + options.length) % options.length);
  };
  return { active: Math.min(active, Math.max(0, options.length - 1)), setActive, move };
}

/**
 * Handles the list keys of a combobox input: ↑ ↓ move, Enter (and Tab) pick, Esc closes.
 * Returns true when the key was used.
 */
export function listKeys(
  e: KeyboardEvent<HTMLElement>,
  nav: ReturnType<typeof useActiveOption>,
  options: readonly ContextOption[],
  onPick: (o: ContextOption) => void,
  onClose: () => void,
): boolean {
  if (e.nativeEvent.isComposing) return false;
  if (e.key === "ArrowDown" || e.key === "ArrowUp") {
    e.preventDefault();
    nav.move(e.key === "ArrowDown" ? 1 : -1);
    return true;
  }
  if ((e.key === "Enter" || e.key === "Tab") && !e.shiftKey) {
    const o = options[nav.active];
    if (!o) return false;
    e.preventDefault();
    onPick(o);
    return true;
  }
  if (e.key === "Escape") {
    e.preventDefault();
    e.stopPropagation();
    onClose();
    return true;
  }
  return false;
}

export function optionId(listId: string, i: number) {
  return `${listId}-o${i}`;
}

/** The results, for an input that keeps focus (aria-activedescendant). */
export function ContextListbox({
  id,
  label,
  options,
  active,
  onActive,
  onPick,
  searching,
  empty,
  current,
}: {
  id: string;
  label: string;
  options: readonly ContextOption[];
  active: number;
  onActive: (i: number) => void;
  onPick: (o: ContextOption) => void;
  searching: boolean;
  empty: ReactNode;
  current?: string;
}) {
  const list = useRef<HTMLDivElement>(null);
  // biome-ignore lint/correctness/useExhaustiveDependencies: keep the active option in view when it moves
  useEffect(() => {
    list.current?.querySelector('[aria-selected="true"]')?.scrollIntoView?.({ block: "nearest" });
  }, [active]);
  return (
    <div ref={list} id={id} role="listbox" aria-label={label} className="max-h-64 overflow-auto p-1">
      {options.length === 0 && (
        <div className="flex items-center gap-2 px-2.5 py-2 text-12-5 text-ink-3" role="status">
          {searching ? (
            <>
              <Spinner /> Searching…
            </>
          ) : (
            empty
          )}
        </div>
      )}
      {options.map((o, i) => (
        // biome-ignore lint/a11y/useKeyWithClickEvents lint/a11y/useFocusableInteractive: the input keeps focus and handles the keys (aria-activedescendant)
        <div
          key={o.key}
          id={optionId(id, i)}
          role="option"
          aria-selected={i === active}
          className={`flex min-h-9 cursor-pointer items-center gap-2 rounded-lg px-2 py-1.5 text-12-5 aria-selected:bg-surface-sunken ${o.stale ? "opacity-60" : ""}`}
          onMouseMove={() => i !== active && onActive(i)}
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => onPick(o)}
        >
          {isClusterRef(o.ref) ? (
            <>
              <Icon name="globe" className="size-3.5 shrink-0 text-ink-3" />
              <ClusterTag name={o.ref.cluster} />
              <span className="truncate text-ink-3">whole cluster</span>
            </>
          ) : (
            <>
              {o.resource && !o.resource.inventoryOnly ? (
                <StatusIcon status={o.resource.status} />
              ) : (
                <Icon name={kindInfo(o.ref.kind).icon} className="size-3.5 shrink-0 text-ink-3" />
              )}
              <KindBadge kind={o.ref.kind} />
              <RefName r={o.ref} />
              {o.ref.cluster !== current && <ClusterTag name={o.ref.cluster} className="ml-auto" />}
            </>
          )}
        </div>
      ))}
    </div>
  );
}

/** The + picker: a search box over the results, opened under the chips. */
export function ContextPicker({
  current,
  onScreen,
  exclude,
  onPick,
  onClose,
}: {
  current?: string;
  onScreen: ReadonlyArray<{ ref: ResourceRef; resource?: Resource }>;
  exclude: readonly ResourceRef[];
  onPick: (r: ResourceRef) => void;
  onClose: () => void;
}) {
  const [query, setQuery] = useState("");
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const found = useContextOptions(query, { current, onScreen, exclude, enabled: true });
  const nav = useActiveOption(found.options);
  const pick = (o: ContextOption) => onPick(o.ref);

  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (e.target instanceof Node && !root.current?.contains(e.target)) onClose();
    };
    document.addEventListener("pointerdown", onDown);
    return () => document.removeEventListener("pointerdown", onDown);
  }, [onClose]);

  return (
    <div
      ref={root}
      className="anim-fade-in absolute top-full right-3 left-3 z-30 mt-1 overflow-hidden rounded-xl border border-line-strong bg-surface shadow-pop"
    >
      <div className="flex items-center gap-2 border-b border-line px-3 py-2 text-ink-3">
        {found.searching ? <Spinner /> : <Icon name="search" className="size-3.5" />}
        <input
          role="combobox"
          aria-expanded="true"
          aria-controls={`${id}-list`}
          aria-activedescendant={found.options.length ? optionId(`${id}-list`, nav.active) : undefined}
          aria-autocomplete="list"
          aria-label="Add to the chat"
          placeholder="Add a resource or cluster…"
          autoComplete="off"
          spellCheck={false}
          className="min-w-0 flex-1 border-0 bg-transparent text-13-5 text-ink outline-none placeholder:text-ink-3"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => listKeys(e, nav, found.options, pick, onClose)}
          // biome-ignore lint/a11y/noAutofocus: the picker opens to type into
          autoFocus
        />
      </div>
      <ContextListbox
        id={`${id}-list`}
        label="Resources and clusters"
        options={found.options}
        active={nav.active}
        onActive={nav.setActive}
        onPick={pick}
        searching={found.searching}
        current={current}
        empty={
          found.error
            ? "Search failed. Try again."
            : query.trim()
              ? `Nothing matches “${query.trim()}”.`
              : "Type to search every cluster."
        }
      />
    </div>
  );
}
