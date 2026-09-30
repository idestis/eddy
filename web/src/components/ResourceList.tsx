import { useVirtualizer } from "@tanstack/react-virtual";
import { memo, type Ref, useEffect, useImperativeHandle, useRef } from "react";
import type { Resource } from "../api/types";
import { age, revisionOf } from "../lib/format";
import { kindInfo } from "../lib/kinds";
import type { Row } from "../lib/resourceRows";
import { StatusPill } from "./Status";

const ROW_HEIGHT = 52;
const GROUP_HEIGHT = 34;

export const rowDomId = (id: string): string => `row-${id.replace(/[^\w-]/g, "_")}`;

interface ResourceListProps {
  rows: Row[];
  selectedId: string | undefined;
  onSelect: (r: Resource) => void;
  onOpen: (r: Resource) => void;
  label: string;
  ref?: Ref<ResourceListHandle>;
}

export interface ResourceListHandle {
  focus: () => void;
}

const ResourceRow = memo(function ResourceRow({
  r,
  selected,
  onSelect,
  onOpen,
}: {
  r: Resource;
  selected: boolean;
  onSelect: (r: Resource) => void;
  onOpen: (r: Resource) => void;
}) {
  const info = kindInfo(r.kind);
  return (
    // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard selection is handled by the listbox (j/k/Enter)
    <div
      id={rowDomId(r.id)}
      role="option"
      tabIndex={-1}
      aria-selected={selected}
      className={`row-main ${r.status}`}
      onClick={() => (selected ? onOpen(r) : onSelect(r))}
      onDoubleClick={() => onOpen(r)}
    >
      <span className="nmc">
        <span className="name" title={r.name}>
          {r.name}
        </span>
        <span className="ns">{r.namespace || "cluster-scoped"}</span>
      </span>
      <span className="kd c-kind">{info.kind}</span>
      <StatusPill status={r.status} />
      <span className="msg c-msg" title={r.message}>
        {r.message}
      </span>
      <span className="rev c-rev" title={r.revision ?? r.chart}>
        {revisionOf(r)}
      </span>
      <span className="age c-age" title={r.createdAt}>
        {age(r.createdAt)}
      </span>
    </div>
  );
});

/**
 * The virtualised resource table. Only visible rows are in the DOM, so it
 * stays smooth with thousands of rows. The container is a listbox that keeps
 * focus; the selected row is announced with aria-activedescendant.
 */
export function ResourceList({ rows, selectedId, onSelect, onOpen, label, ref }: ResourceListProps) {
  const scroller = useRef<HTMLDivElement>(null);
  useImperativeHandle(ref, () => ({ focus: () => scroller.current?.focus() }), []);

  const virtualizer = useVirtualizer({
    count: rows.length,
    getScrollElement: () => scroller.current,
    estimateSize: (i) => (rows[i]?.type === "group" ? GROUP_HEIGHT : ROW_HEIGHT),
    getItemKey: (i) => rows[i]?.key ?? i,
    overscan: 12,
  });

  const selectedIndex = selectedId ? rows.findIndex((r) => r.key === selectedId) : -1;
  useEffect(() => {
    if (selectedIndex >= 0) virtualizer.scrollToIndex(selectedIndex, { align: "auto" });
  }, [selectedIndex, virtualizer]);

  return (
    <div className="table">
      <div className="thead" aria-hidden="true">
        <span>Name</span>
        <span className="c-kind">Kind</span>
        <span>Status</span>
        <span className="c-msg">Message</span>
        <span className="c-rev">Revision</span>
        <span className="r c-age">Age</span>
      </div>
      <div
        ref={scroller}
        className="rows"
        role="listbox"
        aria-label={label}
        tabIndex={0}
        aria-activedescendant={selectedId && selectedIndex >= 0 ? rowDomId(selectedId) : undefined}
      >
        <div className="rows-inner" style={{ height: virtualizer.getTotalSize() }}>
          {virtualizer.getVirtualItems().map((v) => {
            const row = rows[v.index];
            if (!row) return null;
            return (
              <div
                key={v.key}
                className="vrow"
                style={{ height: v.size, transform: `translateY(${v.start}px)` }}
              >
                {row.type === "group" ? (
                  <div className="ghead" role="presentation">
                    {kindInfo(row.kind).plural}
                    <span className="n">{row.count}</span>
                    {row.failing > 0 && <span className="hb failed">{row.failing} failing</span>}
                  </div>
                ) : (
                  <ResourceRow
                    r={row.resource}
                    selected={row.key === selectedId}
                    onSelect={onSelect}
                    onOpen={onOpen}
                  />
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
