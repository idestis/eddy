// A row of toggle chips that stays on one line. It renders as many chips as fit, puts the
// rest behind a "+N" chip that opens the Select listbox (anchored below, in a portal, with
// the same keys), and collapses to one summary dropdown when not even one chip fits.
// Active chips are never hidden. Widths come from a hidden measuring row, so a change of
// size (a count gaining a digit, a resize) re-lays the row before it paints.

import { type ReactNode, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Icon } from "./Icon";
import { Select, type SelectOption, type SelectTone } from "./Select";

export interface OverflowItem {
  key: string;
  /** The chip as drawn in the row. It owns its click handler. */
  chip: ReactNode;
  /** The content of the row in the "+N" list (icon and label). */
  option: ReactNode;
  /** Plain text for typeahead and the accessible name. */
  text: string;
  /** Secondary text on the right of the list row, such as a count. */
  meta?: ReactNode;
  active: boolean;
}

export interface OverflowLayout {
  mode: "all" | "some" | "collapsed";
  visible: number[];
  hidden: number[];
}

/**
 * Which chips show. `widths` are the natural widths of the chips, `plus` the width of the
 * "+N" chip, `gap` the space between chips. Active chips are always visible; the others fill
 * the rest in order. When even the narrowest chip and the "+N" do not fit, the row collapses
 * to a single dropdown. Pure, exported for tests.
 */
export function computeOverflow(
  widths: readonly number[],
  active: readonly boolean[],
  plus: number,
  available: number,
  gap: number,
): OverflowLayout {
  const n = widths.length;
  const all = [...widths.keys()];
  const sum = widths.reduce((a, b) => a + b, 0);
  const total = sum + gap * Math.max(0, n - 1);
  // Nothing laid out (no layout engine, or a hidden tab): show everything rather than collapse.
  if (n === 0 || sum === 0 || total <= available) return { mode: "all", visible: all, hidden: [] };
  if (Math.min(...widths) + gap + plus > available) return { mode: "collapsed", visible: [], hidden: all };
  const show = new Set<number>();
  let used = plus;
  for (const i of all) {
    if (!active[i]) continue;
    show.add(i);
    used += (widths[i] ?? 0) + gap;
  }
  for (const i of all) {
    if (show.has(i)) continue;
    const w = (widths[i] ?? 0) + gap;
    if (used + w > available) break;
    show.add(i);
    used += w;
  }
  return {
    mode: "some",
    visible: all.filter((i) => show.has(i)),
    hidden: all.filter((i) => !show.has(i)),
  };
}

interface Measure {
  available: number;
  widths: number[];
  plus: number;
}

const same = (a: Measure | null, b: Measure) =>
  a !== null &&
  a.available === b.available &&
  a.plus === b.plus &&
  a.widths.length === b.widths.length &&
  a.widths.every((w, i) => w === b.widths[i]);

interface OverflowChipsProps {
  items: readonly OverflowItem[];
  onToggle: (key: string) => void;
  /** The accessible name of the group and of the dropdown, such as "Status". */
  label: string;
  /** The chip look of the "+N" and the summary buttons; `pressed` styling comes from aria-pressed. */
  chipClass: string;
  /** What the collapsed dropdown shows given the active items; defaults to the label. */
  summary?: (active: readonly OverflowItem[]) => ReactNode;
  /** Pixels between chips; must match the `gap` the chips are laid out with. */
  gap?: number;
  tone?: SelectTone;
  className?: string;
}

export function OverflowChips({
  items,
  onToggle,
  label,
  chipClass,
  summary,
  gap = 6,
  tone = "surface",
  className = "",
}: OverflowChipsProps) {
  const root = useRef<HTMLFieldSetElement>(null);
  const ruler = useRef<HTMLDivElement>(null);
  const [m, setM] = useState<Measure | null>(null);
  const signature = items.map((i) => i.key).join("\u0000");

  useLayoutEffect(() => {
    const el = root.current;
    const row = ruler.current;
    if (!el || !row || !signature) return;
    const measure = () => {
      const kids = [...row.children] as HTMLElement[];
      const next: Measure = {
        available: el.clientWidth,
        widths: kids.slice(0, -1).map((k) => k.offsetWidth),
        plus: kids[kids.length - 1]?.offsetWidth ?? 0,
      };
      setM((cur) => (same(cur, next) ? cur : next));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    for (const k of row.children) ro.observe(k);
    return () => ro.disconnect();
  }, [signature]);

  const fresh = m !== null && m.widths.length === items.length;
  const layout = useMemo<OverflowLayout>(
    () =>
      fresh
        ? computeOverflow(
            m.widths,
            items.map((i) => i.active),
            m.plus,
            m.available,
            gap,
          )
        : { mode: "all", visible: items.map((_, i) => i), hidden: [] },
    [fresh, m, items, gap],
  );

  const options: SelectOption<string>[] = (
    layout.mode === "collapsed" ? items.map((_, i) => i) : layout.hidden
  )
    .map((i) => items[i])
    .filter((i): i is OverflowItem => i !== undefined)
    .map((i) => ({ value: i.key, label: i.option, text: i.text, meta: i.meta }));
  const activeKeys = new Set(items.filter((i) => i.active).map((i) => i.key));
  const hiddenActive = options.some((o) => activeKeys.has(o.value));

  const plusChip = (n: number) => (
    <span className="relative inline-flex">
      +{n}
      {hiddenActive && (
        <span data-testid="overflow-dot" className="absolute -top-0.5 -right-1 size-1.5 rounded-full bg-c" />
      )}
    </span>
  );

  return (
    <fieldset
      ref={root}
      data-overflow-root
      className={`relative m-0 flex min-w-0 items-center overflow-hidden border-0 p-0 ${className}`}
      style={{ gap }}
    >
      <legend className="sr-only">{label}</legend>
      {layout.mode !== "collapsed" &&
        layout.visible.map((i) => <Chip key={items[i]?.key}>{items[i]?.chip}</Chip>)}
      {layout.mode !== "all" && (
        <Select
          value=""
          options={options}
          label={layout.mode === "collapsed" ? label : `More ${label.toLowerCase()}`}
          tone={tone}
          isSelected={(v) => activeKeys.has(v)}
          onChange={onToggle}
          renderTrigger={({ ref, props }) => (
            <button
              ref={ref}
              {...props}
              aria-pressed={layout.mode === "collapsed" ? activeKeys.size > 0 : undefined}
              className={`${chipClass} shrink-0 outline-offset-2`}
            >
              {layout.mode === "collapsed" ? (
                <>
                  {summary ? summary(items.filter((i) => i.active)) : label}
                  <Icon name="updown" className="size-3.5 shrink-0 opacity-60" />
                </>
              ) : (
                plusChip(layout.hidden.length)
              )}
            </button>
          )}
        />
      )}
      <div
        ref={ruler}
        aria-hidden="true"
        inert
        className="pointer-events-none invisible absolute top-0 left-0 flex h-0 overflow-hidden"
        style={{ gap }}
      >
        {items.map((i) => (
          <Chip key={i.key}>{i.chip}</Chip>
        ))}
        <button type="button" tabIndex={-1} className={`${chipClass} shrink-0`}>
          {plusChip(items.length)}
        </button>
      </div>
    </fieldset>
  );
}

function Chip({ children }: { children: ReactNode }) {
  return <span className="inline-flex shrink-0">{children}</span>;
}
