// One segmented control for a section's views (Tree | Graph | Outline, Grouped | Flat |
// Graph | Outline), and the one-line header it sits in. On narrow widths, measured on the
// header's container: the caption goes first, then the meta text moves into the control's
// tooltip, then the control shows icons only (each button keeps its aria-label). Focus rings
// are inset (`.seg-btn`), so they never overlap a neighbour.

import type { ReactNode } from "react";
import { Icon, type IconName } from "./Icon";
import { SEG, SEG_BTN } from "./SidePanel";

export interface ViewOption<V extends string> {
  value: V;
  label: string;
  icon: IconName;
  title: string;
}

export function ViewSwitch<V extends string>({
  legend,
  options,
  value,
  onChange,
  title,
  compact = "@max-md:sr-only",
}: {
  legend: string;
  options: ReadonlyArray<ViewOption<V>>;
  value: V;
  onChange: (v: V) => void;
  /** The group's tooltip: the meta text, once it no longer fits beside it. */
  title?: string;
  /** When labels hide (a container-query variant). */
  compact?: string;
}) {
  return (
    <fieldset className={`${SEG} m-0 shrink-0 p-[2px]`} title={title}>
      <legend className="sr-only">{legend}</legend>
      {options.map((o) => (
        <button
          key={o.value}
          type="button"
          aria-pressed={value === o.value}
          aria-label={o.label}
          className={`${SEG_BTN} h-[26px] flex-none aria-pressed:bg-ink`}
          title={o.title}
          onClick={() => onChange(o.value)}
        >
          <Icon name={o.icon} className="size-3.5" />
          <span className={compact} aria-hidden="true">
            {o.label}
          </span>
        </button>
      ))}
    </fieldset>
  );
}

/**
 * A section header on one line: title and muted caption on the left; on the right, optional
 * extra controls, the view switch and muted meta text.
 */
export function SectionToolbar({
  title,
  caption,
  extra,
  views,
  meta,
}: {
  title: ReactNode;
  caption?: ReactNode;
  extra?: ReactNode;
  views?: ReactNode;
  meta?: string;
}) {
  return (
    <div className="@container mt-[22px] mb-2.5">
      <div className="flex min-h-[30px] min-w-0 items-center gap-3">
        <h3 className="shrink-0 text-13 font-semibold">{title}</h3>
        {caption && <span className="min-w-0 truncate text-12-5 text-ink-3 @max-2xl:hidden">{caption}</span>}
        <div className="ml-auto flex shrink-0 items-center gap-2.5">
          {extra}
          {views}
          {meta && (
            <span
              className="text-12-5 whitespace-nowrap text-ink-3 tabular-nums @max-xl:hidden"
              aria-live="polite"
            >
              {meta}
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
