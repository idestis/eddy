import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { type KeyboardEvent, type ReactNode, useCallback, useMemo, useState } from "react";
import { type FleetResource, useCluster, useClusters, useFleetResources, useMe } from "../api/queries";
import type { ClusterInfo } from "../api/types";
import { useAppState } from "../lib/appState";
import { clusterStyle } from "../lib/clusterColor";
import { STATUS_RANK } from "../lib/format";
import { highlightParts, type Match, type Ranges, rank } from "../lib/fuzzy";
import type { KeyId } from "../lib/keys";
import { flatNav, isFlux, kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { toggleTheme } from "../lib/theme";
import { useResourceActions } from "../lib/useResourceActions";
import { ClusterTile } from "./ClusterSwitch";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";
import { KeyHint, StatusIcon } from "./Status";

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
  keys?: KeyId;
  run: () => void;
}

export type PaletteScope = "cluster" | "all";

const MAX_RESOURCES = 30;
const MAX_COMMANDS = 6;

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
  const { closePalette, selection, setHelp, ask } = useAppState();
  const navigate = useNavigate();
  const { data: me } = useMe();
  const { data: clusters = [] } = useClusters();
  const { items: fleet } = useFleetResources();
  const selCluster = useCluster(selection?.cluster);
  const actions = useResourceActions(selCluster);
  const selected = selection?.resource;
  const scoped = scope === "cluster" && routeCluster ? routeCluster : undefined;
  const resources = useMemo(
    () => (scoped ? fleet.filter((fr) => fr.cluster === scoped) : fleet),
    [fleet, scoped],
  );

  const run = useCallback(
    (fn: () => void) => () => {
      closePalette();
      fn();
    },
    [closePalette],
  );

  const commands = useMemo<PaletteCommand[]>(() => {
    const out: PaletteCommand[] = clusters.map((c, i) => ({
      id: `cluster:${c.name}`,
      label: `Switch to ${c.displayName || c.name}`,
      sub: [c.environment, c.region, c.connected ? "" : "disconnected"].filter(Boolean).join(", "),
      keywords: `ctx cluster switch ${c.name} ${c.environment ?? ""}`,
      cluster: c,
      digit: i < 9 ? i + 1 : undefined,
      run: () => void navigate({ to: "/c/$cluster", params: { cluster: c.name } }),
    }));
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
        keys: "thread",
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
      out.push({
        id: "attention",
        label: "Show what needs attention",
        sub: navCluster,
        keywords: ":failing failed errors attention",
        icon: "alert",
        run: () => void navigate({ to: "/c/$cluster", params, search: { status: "attention" } }),
      });
      for (const n of flatNav()) {
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
  }, [clusters, selected, selection, routeCluster, me, actions, navigate, ask, setHelp]);

  const raw = query.trim();
  const commandsOnly = /^[:>]/.test(raw);
  const q = raw.replace(/^[:>]\s*/, "");
  const clusterByName = useMemo(() => new Map(clusters.map((c) => [c.name, c])), [clusters]);
  const toggleScope = useCallback(() => setScope((s) => (s === "cluster" ? "all" : "cluster")), []);

  const groups = useMemo(() => {
    const out: Array<{ heading: string; items: ReactNode[] }> = [];
    const cmdItems = (list: Array<{ item: PaletteCommand; match?: Match }>) =>
      list.map(({ item: c, match }) => <CommandItem key={c.id} cmd={c} match={match} onRun={run(c.run)} />);
    const resItem = ({ item: fr, match }: { item: FleetResource; match?: Match }) => (
      <ResourceItem
        key={`${fr.cluster}/${fr.resource.id}`}
        item={fr}
        match={match}
        cluster={clusterByName.get(fr.cluster)}
        onRun={run(() => void navigate(detailLink(fr.cluster, fr.resource)))}
      />
    );
    const where = scoped ? scoped : "every cluster";

    if (!raw) {
      const attention = resources
        .filter(
          ({ resource: r }) =>
            !r.inventoryOnly && (r.status === "failed" || (r.status === "reconciling" && isFlux(r.kind))),
        )
        .sort(tiebreakFor(routeCluster))
        .slice(0, 6);
      if (attention.length)
        out.push({ heading: `Needs attention, ${where}`, items: attention.map((item) => resItem({ item })) });
      out.push({
        heading: "Clusters",
        items: cmdItems(commands.filter((c) => c.cluster).map((item) => ({ item }))),
      });
      out.push({
        heading: "Commands",
        items: cmdItems(
          commands
            .filter((c) => !c.cluster && !c.id.startsWith("nav:"))
            .slice(0, MAX_COMMANDS)
            .map((item) => ({ item })),
        ),
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
      const matched = rank(q, resources, resourceFields, MAX_RESOURCES, tiebreakFor(routeCluster));
      if (matched.length)
        out.push({ heading: scoped ? `Resources in ${scoped}` : "Resources", items: matched.map(resItem) });
      if (scoped) {
        out.push({
          heading: "Wider",
          items: [
            <Command.Item
              key="scope-all"
              value="scope-all"
              onSelect={() => setScope("all")}
              className="flex min-h-11 w-full items-center gap-[11px] rounded-control px-2.5 py-2 text-left"
            >
              <span className="flex size-[26px] shrink-0 items-center justify-center rounded-lg bg-surface-sunken text-ink-2">
                <Icon name="globe" />
              </span>
              <span className="flex-1">
                Search all clusters for <b className="font-semibold">“{raw}”</b>
              </span>
              <kbd>Tab</kbd>
            </Command.Item>,
          ],
        });
      }
      if (raw.length > 3 && me?.features.ai && selection) {
        out.push({
          heading: "Ask AI",
          items: [
            <CommandItem
              key="ask-query"
              cmd={{
                id: "ask-query",
                label: `Ask: ${raw}`,
                sub: `About ${selected?.name ?? selection.cluster}`,
                keywords: "",
                icon: "spark",
                run: () => ask(raw),
              }}
              onRun={run(() => ask(raw))}
            />,
          ],
        });
      }
    }
    if (matchedCommands.length) out.push({ heading: "Commands", items: cmdItems(matchedCommands) });
    return out;
  }, [
    raw,
    q,
    commandsOnly,
    commands,
    resources,
    scoped,
    routeCluster,
    selection,
    selected,
    me,
    clusterByName,
    navigate,
    ask,
    run,
  ]);

  const onInputKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
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

  return (
    <Modal label="Command palette" onClose={closePalette} className="palette">
      <Command label="Command palette" shouldFilter={false} loop>
        <div className="flex items-center gap-2.5 border-b border-line px-4 py-3 text-ink-3">
          <Icon name="search" />
          <Command.Input
            value={query}
            onValueChange={setQuery}
            onKeyDown={onInputKeyDown}
            placeholder={placeholder}
            aria-label="Search"
            className="min-w-0 flex-1 border-0 bg-transparent text-16 text-ink outline-none placeholder:text-ink-3"
            autoFocus
          />
          {routeCluster && (
            <fieldset
              className="flex shrink-0 rounded-tile border border-line bg-surface-sunken p-0.5"
              aria-label="Search scope"
            >
              {(["cluster", "all"] as const).map((s) => (
                <button
                  type="button"
                  key={s}
                  aria-pressed={scope === s}
                  onClick={() => setScope(s)}
                  className="inline-flex h-7 items-center gap-1.5 rounded-lg px-2.5 text-12-5 font-medium whitespace-nowrap text-ink-2 aria-pressed:bg-surface aria-pressed:text-ink aria-pressed:shadow-control"
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
          )}
        </div>
        <Command.List>
          <Command.Empty>
            No resources or commands match “{query}”{scoped ? ` in ${scoped}` : ""}.
            {scoped && (
              <>
                {" "}
                Press <kbd>Tab</kbd> to search every cluster.
              </>
            )}
          </Command.Empty>
          {groups.map((g) => (
            <Command.Group key={g.heading} heading={g.heading}>
              {g.items}
            </Command.Group>
          ))}
        </Command.List>
        <div className="flex flex-wrap gap-4 border-t border-line px-3.5 py-[9px] text-12 text-ink-3 [&>span]:inline-flex [&>span]:items-center [&>span]:gap-1.5">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> move
          </span>
          <span>
            <kbd>↵</kbd> open
          </span>
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
      </Command>
    </Modal>
  );
}

const ITEM = "flex min-h-[46px] w-full items-center gap-[11px] rounded-control px-2.5 py-2 text-left";
const ICON_BOX =
  "flex size-[26px] shrink-0 items-center justify-center rounded-lg bg-surface-sunken text-ink-2";

function CommandItem({ cmd, match, onRun }: { cmd: PaletteCommand; match?: Match; onRun: () => void }) {
  return (
    <Command.Item value={cmd.id} onSelect={onRun} className={ITEM}>
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
      {cmd.digit ? <kbd>{cmd.digit}</kbd> : cmd.keys ? <KeyHint id={cmd.keys} /> : null}
    </Command.Item>
  );
}

function ResourceItem({
  item,
  match,
  cluster,
  onRun,
}: {
  item: FleetResource;
  match?: Match;
  cluster?: ClusterInfo;
  onRun: () => void;
}) {
  const r = item.resource;
  const info = kindInfo(r.kind);
  return (
    <Command.Item
      value={`${item.cluster}/${r.id}`}
      onSelect={onRun}
      className={`${ITEM} ${r.inventoryOnly ? "opacity-60" : ""}`}
    >
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
      <span className="ctag" style={clusterStyle(cluster)}>
        {item.cluster}
      </span>
    </Command.Item>
  );
}
