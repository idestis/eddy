import { Link } from "@tanstack/react-router";
import { type KeyboardEvent, useMemo, useState } from "react";
import { refId } from "../api/queries";
import type { Resource } from "../api/types";
import { STATUS_RANK } from "../lib/format";
import { kindInfo } from "../lib/kinds";
import { detailLink } from "../lib/links";
import { Icon } from "./Icon";
import { StatusIcon } from "./Status";

/** Index of resources by their owner's id, built from Resource.owner. */
export function childrenIndex(items: readonly Resource[]): Map<string, Resource[]> {
  const index = new Map<string, Resource[]>();
  for (const r of items) {
    if (!r.owner) continue;
    const key = refId(r.owner);
    const list = index.get(key);
    if (list) list.push(r);
    else index.set(key, [r]);
  }
  for (const list of index.values()) {
    list.sort(
      (a, b) =>
        kindInfo(a.kind).order - kindInfo(b.kind).order ||
        STATUS_RANK[a.status] - STATUS_RANK[b.status] ||
        a.name.localeCompare(b.name),
    );
  }
  return index;
}

interface NodeProps {
  cluster: string;
  r: Resource;
  index: Map<string, Resource[]>;
  depth: number;
  seen: ReadonlySet<string>;
  /** The first top-level item is the tree's tab stop. */
  first?: boolean;
}

function TreeNode({ cluster, r, index, depth, seen, first }: NodeProps) {
  const kids = seen.has(r.id) ? [] : (index.get(r.id) ?? []);
  const [open, setOpen] = useState(depth < 1);
  const nextSeen = useMemo(() => new Set([...seen, r.id]), [seen, r.id]);
  const hasKids = kids.length > 0;

  // Left and Right follow the WAI-ARIA tree pattern: expand, collapse, or step in and out.
  const onKeyDown = (e: KeyboardEvent<HTMLLIElement>) => {
    if (e.target !== e.currentTarget) return;
    const item = e.currentTarget;
    if (e.key === "ArrowRight") {
      if (hasKids && !open) setOpen(true);
      else item.querySelector<HTMLElement>("[role='group'] > [role='treeitem']")?.focus();
    } else if (e.key === "ArrowLeft") {
      if (hasKids && open) setOpen(false);
      else item.parentElement?.closest<HTMLElement>("[role='treeitem']")?.focus();
    } else if (e.key === "Enter") {
      item.querySelector<HTMLAnchorElement>(".tnode a")?.click();
    } else return;
    e.preventDefault();
    e.stopPropagation();
  };

  return (
    <li
      role="treeitem"
      tabIndex={first ? 0 : -1}
      aria-expanded={hasKids ? open : undefined}
      aria-level={depth + 1}
      aria-selected={false}
      aria-label={`${r.kind} ${r.name}, ${r.status}`}
      onKeyDown={onKeyDown}
    >
      <div className="tnode">
        <button
          type="button"
          className={`tog${hasKids ? "" : " leaf"}`}
          aria-hidden="true"
          tabIndex={-1}
          onClick={() => setOpen(!open)}
        >
          <Icon name="chev" />
        </button>
        <StatusIcon status={r.status} />
        <span className="kind">{kindInfo(r.kind).abbr}</span>
        <Link {...detailLink(cluster, r)} tabIndex={-1}>
          {r.name}
        </Link>
        {hasKids && <span className="muted">{kids.length}</span>}
        {r.status !== "ready" && r.message && <span className="ms">{r.message}</span>}
      </div>
      {open && hasKids && (
        // biome-ignore lint/a11y/useSemanticElements: role="group" is the ARIA tree pattern's container for child items
        <ul role="group">
          {kids.map((k) => (
            <TreeNode key={k.id} cluster={cluster} r={k} index={index} depth={depth + 1} seen={nextSeen} />
          ))}
        </ul>
      )}
    </li>
  );
}

/** Moves focus between visible tree items with Up, Down, Home and End. */
function onTreeKeyDown(e: KeyboardEvent<HTMLUListElement>) {
  const moves: Record<string, (i: number, n: number) => number> = {
    ArrowDown: (i, n) => Math.min(n - 1, i + 1),
    ArrowUp: (i) => Math.max(0, i - 1),
    Home: () => 0,
    End: (_i, n) => n - 1,
  };
  const move = moves[e.key];
  if (!move) return;
  const items = [...e.currentTarget.querySelectorAll<HTMLElement>("[role='treeitem']")];
  const current = document.activeElement?.closest<HTMLElement>("[role='treeitem']");
  const i = current ? items.indexOf(current) : -1;
  items[move(i, items.length)]?.focus();
  e.preventDefault();
  e.stopPropagation();
}

/** What this object manages: its owned resources, recursively (ownerRefs, Flux labels and inventory). */
export function ChildrenTree({
  cluster,
  root,
  items,
}: {
  cluster: string;
  root: Resource;
  items: readonly Resource[];
}) {
  const index = useMemo(() => childrenIndex(items), [items]);
  const kids = index.get(root.id) ?? [];
  const seen = useMemo(() => new Set([root.id]), [root.id]);
  if (!kids.length) return <p className="muted">{root.name} manages no resources that Eddy watches.</p>;
  return (
    <ul
      className="tree"
      // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: an ARIA tree built from nested lists, with keyboard support
      role="tree"
      aria-label={`Resources managed by ${root.name}`}
      onKeyDown={onTreeKeyDown}
    >
      {kids.map((k, i) => (
        <TreeNode key={k.id} cluster={cluster} r={k} index={index} depth={0} seen={seen} first={i === 0} />
      ))}
    </ul>
  );
}
