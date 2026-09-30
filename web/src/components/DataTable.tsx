import type { ReactNode } from "react";

/** A bordered table for tokens, audit and similar lists. Cells are styled from here. */
export function DataTable({ children }: { children: ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-card border border-line bg-surface">
      <table className="w-full border-separate border-spacing-0 text-13 [&_tbody_tr:hover_td]:bg-surface-sunken [&_tbody_tr:last-child_td]:border-b-0 [&_td]:border-b [&_td]:border-line [&_td]:px-3.5 [&_td]:py-2.5 [&_td]:align-middle [&_th]:border-b [&_th]:border-line [&_th]:bg-surface-side [&_th]:px-3.5 [&_th]:py-[9px] [&_th]:text-left [&_th]:text-12 [&_th]:font-medium [&_th]:whitespace-nowrap [&_th]:text-ink-3">
        {children}
      </table>
    </div>
  );
}
