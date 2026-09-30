import { useNavigate } from "@tanstack/react-router";
import { Command } from "cmdk";
import { type ReactNode, useCallback, useMemo, useState } from "react";
import { type FleetResource, useCluster, useClusters, useFleetResources, useMe } from "../api/queries";
import type { ClusterInfo } from "../api/types";
import { useAppState } from "../lib/appState";
import { clusterStyle } from "../lib/clusterColor";
import { rank } from "../lib/fuzzy";
import { hint, type KeyId } from "../lib/keys";
import { isFlux, kindInfo, NAV_GROUPS } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { toggleTheme } from "../lib/theme";
import { useResourceActions } from "../lib/useResourceActions";
import { Icon, type IconName } from "./Icon";
import { Modal } from "./Modal";
import { Keys, StatusIcon } from "./Status";

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

const MAX_RESOURCES = 30;
const MAX_COMMANDS = 6;

/** ⌘K: fuzzy search across the resources of every cluster, plus commands. A leading ":" limits to commands. */
export function CommandPalette({ initialQuery }: { initialQuery: string }) {
  const [query, setQuery] = useState(initialQuery);
  const { closePalette, selection, setHelp, ask } = useAppState();
  const navigate = useNavigate();
  const { data: me } = useMe();
  const { data: clusters = [] } = useClusters();
  const { items: resources } = useFleetResources();
  const selCluster = useCluster(selection?.cluster);
  const actions = useResourceActions(selCluster);
  const selected = selection?.resource;

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
            ...detailLink(selection.cluster, selected, "threads"),
            search: { view: "threads", compose: true },
          }),
      });
    }
    if (selection) {
      const params = { cluster: selection.cluster };
      out.push({
        id: "attention",
        label: "Show what needs attention",
        sub: selection.cluster,
        keywords: ":failing failed errors attention",
        icon: "alert",
        run: () => void navigate({ to: "/c/$cluster", params, search: { status: "attention" } }),
      });
      for (const g of NAV_GROUPS) {
        out.push({
          id: `nav:${g.id}`,
          label: `Show ${g.label}`,
          sub: selection.cluster,
          keywords: `:${g.id} ${g.label}`,
          icon: g.icon,
          run: () => void navigate({ to: "/c/$cluster", params, search: { kind: g.id } }),
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
  }, [clusters, selected, selection, me, actions, navigate, ask, setHelp]);

  const raw = query.trim();
  const commandsOnly = /^[:>]/.test(raw);
  const q = raw.replace(/^[:>]\s*/, "");
  const clusterByName = useMemo(() => new Map(clusters.map((c) => [c.name, c])), [clusters]);

  const groups = useMemo(() => {
    const out: Array<{ heading: string; items: ReactNode[] }> = [];
    const cmdItems = (list: PaletteCommand[]) =>
      list.map((c) => <CommandItem key={c.id} cmd={c} onRun={run(c.run)} />);
    const resItem = (fr: FleetResource) => (
      <ResourceItem
        key={`${fr.cluster}/${fr.resource.id}`}
        item={fr}
        cluster={clusterByName.get(fr.cluster)}
        onRun={run(() => void navigate(detailLink(fr.cluster, fr.resource)))}
      />
    );

    if (!raw) {
      const attention = resources
        .filter(({ resource: r }) => r.status === "failed" || (r.status === "reconciling" && isFlux(r.kind)))
        .sort((a, b) => Number(b.cluster === selection?.cluster) - Number(a.cluster === selection?.cluster))
        .slice(0, 6);
      if (attention.length)
        out.push({ heading: "Needs attention, every cluster", items: attention.map(resItem) });
      out.push({ heading: "Clusters", items: cmdItems(commands.filter((c) => c.cluster)) });
      out.push({
        heading: "Commands",
        items: cmdItems(commands.filter((c) => !c.cluster).slice(0, MAX_COMMANDS)),
      });
      return out;
    }

    const matchedCommands = rank(
      q,
      commands,
      (c) => `${c.label} ${c.keywords}`,
      commandsOnly ? 50 : MAX_COMMANDS,
    );
    if (!commandsOnly) {
      const matched = rank(
        q,
        resources,
        ({ cluster, resource: r }) =>
          `${r.name} ${r.kind} ${kindInfo(r.kind).abbr} ${r.namespace} ${cluster}`,
        MAX_RESOURCES,
        (fr) => (fr.cluster === selection?.cluster ? 0.5 : 0),
      );
      if (matched.length) out.push({ heading: "Resources", items: matched.map(resItem) });
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
  }, [raw, q, commandsOnly, commands, resources, selection, selected, me, clusterByName, navigate, ask, run]);

  return (
    <Modal label="Command palette" onClose={closePalette} className="palette">
      <Command label="Command palette" shouldFilter={false} loop>
        <div className="pin">
          <Icon name="search" />
          <Command.Input
            value={query}
            onValueChange={setQuery}
            placeholder="Search every cluster, or type : for commands"
            aria-label="Search"
            autoFocus
          />
        </div>
        <Command.List>
          <Command.Empty>No resources or commands match “{query}”.</Command.Empty>
          {groups.map((g) => (
            <Command.Group key={g.heading} heading={g.heading}>
              {g.items}
            </Command.Group>
          ))}
        </Command.List>
        <div className="pfoot">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> move
          </span>
          <span>
            <kbd>↵</kbd> open
          </span>
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

function CommandItem({ cmd, onRun }: { cmd: PaletteCommand; onRun: () => void }) {
  const keys = cmd.digit ? [String(cmd.digit)] : cmd.keys ? hint(cmd.keys) : undefined;
  return (
    <Command.Item value={cmd.id} onSelect={onRun} className="pi">
      {cmd.cluster ? (
        <span className="ic swi" style={clusterStyle(cmd.cluster)} />
      ) : (
        <span className="ic">{cmd.icon && <Icon name={cmd.icon} />}</span>
      )}
      <span className="pl">
        <span className="pt">{cmd.label}</span>
        {cmd.sub && <span className="ps">{cmd.sub}</span>}
      </span>
      {keys && <Keys keys={keys} />}
    </Command.Item>
  );
}

function ResourceItem({
  item,
  cluster,
  onRun,
}: {
  item: FleetResource;
  cluster?: ClusterInfo;
  onRun: () => void;
}) {
  const r = item.resource;
  const sub = [
    r.kind,
    r.namespace ? `in ${r.namespace}` : "",
    r.status !== "ready" && r.message ? `· ${r.message}` : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <Command.Item value={`${item.cluster}/${r.id}`} onSelect={onRun} className="pi">
      <span className="ic">
        <StatusIcon status={r.status} label />
      </span>
      <span className="pl">
        <span className="pt mono">
          <span className="ab">{kindInfo(r.kind).abbr}</span>
          {r.name}
        </span>
        <span className="ps">{sub}</span>
      </span>
      <span className="ctag" style={clusterStyle(cluster)}>
        {item.cluster}
      </span>
    </Command.Item>
  );
}
