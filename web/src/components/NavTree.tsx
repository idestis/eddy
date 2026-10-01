// The Browse tree in the sidebar, one recursive component so every level looks and
// behaves the same:
// - connector lines run from a group's icon centre down its children, with ├─ elbows to
//   each child and └─ to the last one (app.css `.tree-branch`);
// - clicking a group row opens its page and expands it; the chevron only toggles;
// - keyboard: Enter opens, → expands (or moves into the group), ← collapses (or moves
//   to the parent);
// - expanding and collapsing animate their height with the motion tokens.

import type { KeyboardEvent, ReactNode } from "react";
import type { NavNode } from "../lib/kinds";
import { Icon } from "./Icon";

export interface NavLinkProps {
  "data-nav-id": string;
  onClick: () => void;
  onKeyDown: (e: KeyboardEvent<HTMLElement>) => void;
}

export interface NavTreeProps {
  nodes: readonly NavNode[];
  isOpen: (n: NavNode) => boolean;
  setOpen: (id: string, open: boolean) => void;
  /** Renders the row's link; spread `props` on it. */
  renderLink: (n: NavNode, props: NavLinkProps) => ReactNode;
  /** Rows to leave out (an empty "Inventory only"). */
  skip?: (n: NavNode) => boolean;
  /** Set on nested levels; the root list has no connectors. */
  parentId?: string;
}

const focusNav = (id: string | undefined) => {
  if (!id) return;
  document.querySelector<HTMLElement>(`[data-nav-id="${CSS.escape(id)}"]`)?.focus();
};

/** Where a child sits among its siblings, for its connector: ├─ or └─. Exported for tests. */
export const branchClass = (index: number, count: number): string =>
  index === count - 1 ? "tree-last" : "tree-mid";

export function NavTree({ nodes, isOpen, setOpen, renderLink, skip, parentId }: NavTreeProps) {
  const shown = nodes.filter((n) => !skip?.(n));
  return (
    <ul
      className={`flex flex-col gap-px ${parentId ? "tree-branch" : ""}`}
      data-depth={parentId ? undefined : 0}
    >
      {shown.map((n, i) => {
        const kids = n.children;
        const open = kids ? isOpen(n) : false;
        const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
          if (e.key === "ArrowRight") {
            e.preventDefault();
            e.stopPropagation();
            if (kids && !open) setOpen(n.id, true);
            else if (kids) focusNav(kids.find((k) => !skip?.(k))?.id);
          } else if (e.key === "ArrowLeft") {
            e.preventDefault();
            e.stopPropagation();
            if (kids && open) setOpen(n.id, false);
            else focusNav(parentId);
          }
        };
        return (
          <li
            key={n.id}
            className={`flex flex-col ${parentId ? branchClass(i, shown.length) : ""}`}
            data-nav-row={n.id}
          >
            <div className="flex items-center [&>a]:flex-1">
              {renderLink(n, {
                "data-nav-id": n.id,
                // A group row opens its page and expands; it never collapses from here.
                onClick: () => {
                  if (kids && !open) setOpen(n.id, true);
                },
                onKeyDown,
              })}
              {kids && (
                <button
                  type="button"
                  className="ib size-7!"
                  aria-expanded={open}
                  aria-label={`${open ? "Collapse" : "Expand"} ${n.label}`}
                  onClick={() => setOpen(n.id, !open)}
                >
                  <Icon
                    name="chev"
                    className={`size-3.5 transition-transform duration-(--duration-base) ease-standard ${open ? "rotate-90" : ""}`}
                  />
                </button>
              )}
            </div>
            {kids && (
              <div
                className={`grid transition-[grid-template-rows] duration-(--duration-slow) ease-standard ${open ? "grid-rows-[1fr]" : "grid-rows-[0fr]"}`}
                inert={!open}
                data-open={open}
              >
                <div className="min-h-0 overflow-hidden">
                  <NavTree
                    nodes={kids}
                    isOpen={isOpen}
                    setOpen={setOpen}
                    renderLink={renderLink}
                    skip={skip}
                    parentId={n.id}
                  />
                </div>
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}
