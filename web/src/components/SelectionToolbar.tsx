// The floating toolbar over a selection of lines (logs or YAML): "Ask AI about selection"
// and "Ask AI with context". The parent places it (`pos`, relative to its own box) and keeps
// the selection alive by cancelling mousedown.

import type { Ref } from "react";
import { Icon } from "./Icon";

export function SelectionToolbar({
  count,
  pos,
  toolbarRef,
  contextTitle,
  onSelection,
  onContext,
}: {
  count: number;
  pos: { top: number; left: number } | null;
  toolbarRef?: Ref<HTMLDivElement>;
  contextTitle: string;
  onSelection: () => void;
  onContext: () => void;
}) {
  return (
    <div
      ref={toolbarRef}
      role="toolbar"
      aria-label={`${count} selected line${count === 1 ? "" : "s"}`}
      className={`anim-pop-in absolute z-10 flex items-center gap-1 rounded-[11px] border border-white/14 bg-code-bg p-1 text-12-5 text-code-ink shadow-pop ${pos ? "" : "invisible"}`}
      style={pos ? { top: pos.top, left: pos.left } : undefined}
      onMouseDown={(e) => e.preventDefault()}
    >
      <span className="px-1.5 text-11-5 text-code-dim tabular-nums">
        {count} line{count === 1 ? "" : "s"}
      </span>
      <button
        type="button"
        className="inline-flex h-7 items-center gap-1.5 rounded-lg bg-c px-2.5 font-semibold text-c-ink hover:brightness-110"
        onClick={onSelection}
      >
        <Icon name="spark" className="size-3.5" />
        Ask AI about selection
      </button>
      <button
        type="button"
        className="inline-flex h-7 items-center gap-1.5 rounded-lg px-2.5 hover:bg-white/10"
        title={contextTitle}
        onClick={onContext}
      >
        Ask AI with context
      </button>
    </div>
  );
}
