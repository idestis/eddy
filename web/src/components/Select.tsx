// The one dropdown of the app. Native <select> menus are drawn by the OS (on macOS a
// floating menu over the trigger that ignores the theme and the dark log surface), so
// every picker uses this instead: a button that opens a listbox anchored below it
// (flipped up only when there is no room), rendered in a portal.
//
// Keyboard: on the button, ArrowDown/ArrowUp/Enter/Space open it. In the list,
// ArrowUp/ArrowDown move, Home/End jump, typing jumps to the next option starting with
// the typed text, Enter or Space picks, Escape or Tab closes. Focus returns to the button.

import {
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { pushOverlay } from "../lib/keys";
import { Icon } from "./Icon";

export interface SelectOption<T extends string | number> {
  value: T;
  label: ReactNode;
  /** Plain text for typeahead and the accessible name; defaults to `label` when it is a string. */
  text?: string;
  /** Secondary text on the right, such as a container's status. */
  meta?: ReactNode;
  disabled?: boolean;
}

export type SelectTone = "surface" | "code";

interface SelectProps<T extends string | number> {
  value: T;
  options: ReadonlyArray<SelectOption<T>>;
  onChange: (value: T) => void;
  /** The accessible name of the button (and the list). */
  label: string;
  /** "code" matches the dark log surface. */
  tone?: SelectTone;
  className?: string;
  id?: string;
  disabled?: boolean;
  /** Replaces the selected option's label on the button. */
  renderValue?: (option: SelectOption<T> | undefined) => ReactNode;
  /**
   * Replaces the default button. Spread `props` onto the one button you render. Used by
   * pickers that are not a single value, such as the "+N" overflow of a chip row.
   */
  renderTrigger?: (t: SelectTrigger) => ReactNode;
  /** Marks options as selected (a check) for multi-select lists; picking still calls `onChange`. */
  isSelected?: (value: T) => boolean;
}

export interface SelectTrigger {
  ref: (el: HTMLButtonElement | null) => void;
  open: boolean;
  props: {
    id?: string;
    type: "button";
    "aria-haspopup": "listbox";
    "aria-expanded": boolean;
    "aria-label": string;
    "aria-controls"?: string;
    disabled?: boolean;
    onClick: () => void;
    onKeyDown: (e: KeyboardEvent<HTMLButtonElement>) => void;
  };
}

const TRIGGER: Record<SelectTone, string> = {
  surface:
    "h-10 rounded-control border border-line-strong bg-surface-sunken px-3 text-14 text-ink hover:border-ink-3 aria-expanded:border-c",
  code: "h-[32px] rounded-[9px] border border-white/14 bg-code-bg px-2.5 font-mono text-12 text-code-ink hover:bg-white/6 aria-expanded:border-c",
};

const LIST: Record<SelectTone, string> = {
  surface: "border-line bg-surface text-ink shadow-pop",
  code: "border-white/14 bg-code-bg text-code-ink shadow-pop font-mono",
};

const OPTION: Record<SelectTone, string> = {
  surface: "text-13-5 data-[active=true]:bg-surface-sunken",
  code: "text-12 data-[active=true]:bg-white/10",
};

const TYPEAHEAD_MS = 600;
const GAP = 4;

export const optionText = <T extends string | number>(o: SelectOption<T>): string =>
  o.text ?? (typeof o.label === "string" ? o.label : String(o.value));

/**
 * The next enabled option from `from` in direction `step`, or `from` when there is none.
 * Exported for tests.
 */
export function stepIndex<T extends string | number>(
  options: ReadonlyArray<SelectOption<T>>,
  from: number,
  step: 1 | -1,
): number {
  for (let i = from + step; i >= 0 && i < options.length; i += step) {
    if (!options[i]?.disabled) return i;
  }
  return from;
}

/**
 * The option a typeahead buffer points at: the first enabled option after `from` whose text
 * starts with `typed` (wrapping). A repeated single letter cycles. Exported for tests.
 */
export function typeaheadIndex<T extends string | number>(
  options: ReadonlyArray<SelectOption<T>>,
  from: number,
  typed: string,
): number {
  const t = typed.toLowerCase();
  const same = t.length > 1 && [...t].every((c) => c === t[0]);
  const needle = same ? (t[0] ?? "") : t;
  const n = options.length;
  // A new single-letter search starts after the current option; a longer one includes it.
  const start = needle.length === 1 ? 1 : 0;
  for (let k = start; k < n + start; k++) {
    const i = (from + k) % n;
    const o = options[i];
    if (o && !o.disabled && optionText(o).toLowerCase().startsWith(needle)) return i;
  }
  return from;
}

function usePopoverPosition(open: boolean, trigger: HTMLElement | null, list: HTMLElement | null) {
  const [style, setStyle] = useState<CSSProperties>({ visibility: "hidden" });
  useLayoutEffect(() => {
    if (!open || !trigger || !list) return;
    const place = () => {
      const t = trigger.getBoundingClientRect();
      const h = list.offsetHeight;
      const vh = window.innerHeight;
      const vw = window.innerWidth;
      const below = t.bottom + GAP;
      // Below the trigger; above only when it does not fit below and fits better above.
      const up = below + h > vh - GAP && t.top - GAP - h >= GAP;
      const minWidth = Math.round(t.width);
      const left = Math.max(GAP, Math.min(t.left, vw - Math.max(minWidth, list.offsetWidth) - GAP));
      setStyle({
        position: "fixed",
        left,
        top: up ? t.top - GAP - h : below,
        minWidth,
        maxHeight: Math.max(120, up ? t.top - 2 * GAP : vh - below - GAP),
        transformOrigin: up ? "bottom" : "top",
      });
    };
    place();
    window.addEventListener("resize", place);
    window.addEventListener("scroll", place, true);
    return () => {
      window.removeEventListener("resize", place);
      window.removeEventListener("scroll", place, true);
    };
  }, [open, trigger, list]);
  return style;
}

export function Select<T extends string | number>({
  value,
  options,
  onChange,
  label,
  tone = "surface",
  className = "",
  id,
  disabled,
  renderValue,
  renderTrigger,
  isSelected,
}: SelectProps<T>) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [trigger, setTrigger] = useState<HTMLButtonElement | null>(null);
  const [list, setList] = useState<HTMLDivElement | null>(null);
  const typed = useRef({ text: "", at: 0 });
  const base = useId();
  const selectedIndex = options.findIndex((o) => o.value === value);
  const selected = options[selectedIndex];
  const chosen = (v: T) => (isSelected ? isSelected(v) : v === value);
  const style = usePopoverPosition(open, trigger, list);
  const optionId = (i: number) => `${base}-opt-${i}`;

  const show = useCallback(
    (at?: number) => {
      if (disabled) return;
      setActive(at ?? (selectedIndex >= 0 ? selectedIndex : stepIndex(options, -1, 1)));
      setOpen(true);
    },
    [disabled, selectedIndex, options],
  );
  const close = useCallback(
    (focusTrigger = true) => {
      setOpen(false);
      if (focusTrigger) trigger?.focus({ preventScroll: true });
    },
    [trigger],
  );
  const pick = (i: number) => {
    const o = options[i];
    if (!o || o.disabled) return;
    if (isSelected || o.value !== value) onChange(o.value);
    close();
  };

  // While open, list and detail shortcuts pause, and a click outside closes it.
  useEffect(() => {
    if (!open) return;
    const release = pushOverlay();
    const onDown = (e: PointerEvent) => {
      const t = e.target as Node;
      if (list?.contains(t) || trigger?.contains(t)) return;
      close(false);
    };
    document.addEventListener("pointerdown", onDown, true);
    return () => {
      release();
      document.removeEventListener("pointerdown", onDown, true);
    };
  }, [open, list, trigger, close]);

  useEffect(() => {
    if (open) list?.focus({ preventScroll: true });
  }, [open, list]);

  useEffect(() => {
    if (!open || !list) return;
    list.querySelector(`#${CSS.escape(optionId(active))}`)?.scrollIntoView?.({ block: "nearest" });
  });

  const onTriggerKey = (e: KeyboardEvent<HTMLButtonElement>) => {
    if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
      e.preventDefault();
      e.stopPropagation();
      show(e.key === "ArrowUp" && selectedIndex < 0 ? stepIndex(options, options.length, -1) : undefined);
    }
  };

  const onListKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const handled = () => {
      e.preventDefault();
      e.stopPropagation();
    };
    switch (e.key) {
      case "ArrowDown":
        handled();
        setActive((a) => stepIndex(options, a, 1));
        return;
      case "ArrowUp":
        handled();
        setActive((a) => stepIndex(options, a, -1));
        return;
      case "Home":
        handled();
        setActive(stepIndex(options, -1, 1));
        return;
      case "End":
        handled();
        setActive(stepIndex(options, options.length, -1));
        return;
      case "Enter":
      case " ":
        if (e.key === " " && typed.current.text && Date.now() - typed.current.at < TYPEAHEAD_MS) break;
        handled();
        pick(active);
        return;
      case "Escape":
        handled();
        close();
        return;
      case "Tab":
        close(false);
        return;
    }
    if (e.key.length === 1 && !e.metaKey && !e.ctrlKey && !e.altKey) {
      handled();
      const now = Date.now();
      const text = now - typed.current.at < TYPEAHEAD_MS ? typed.current.text + e.key : e.key;
      typed.current = { text, at: now };
      setActive((a) => typeaheadIndex(options, a, text));
    }
  };

  const triggerProps: SelectTrigger["props"] = {
    id,
    type: "button",
    "aria-haspopup": "listbox",
    "aria-expanded": open,
    "aria-label": label,
    "aria-controls": open ? `${base}-list` : undefined,
    disabled,
    onClick: () => (open ? close() : show()),
    onKeyDown: onTriggerKey,
  };

  return (
    <>
      {renderTrigger ? (
        renderTrigger({ ref: setTrigger, open, props: triggerProps })
      ) : (
        <button
          ref={setTrigger}
          {...triggerProps}
          className={`inline-flex min-w-0 items-center justify-between gap-2 text-left outline-offset-2 disabled:cursor-not-allowed disabled:opacity-50 ${TRIGGER[tone]} ${className}`}
        >
          <span className="min-w-0 truncate">{renderValue ? renderValue(selected) : selected?.label}</span>
          <Icon name="updown" className="size-3.5 shrink-0 opacity-60" />
        </button>
      )}
      {open &&
        createPortal(
          <div
            ref={setList}
            id={`${base}-list`}
            role="listbox"
            aria-label={label}
            tabIndex={-1}
            aria-activedescendant={optionId(active)}
            className={`anim-pop-in z-[60] overflow-y-auto overscroll-contain rounded-[11px] border p-1 outline-none ${LIST[tone]}`}
            style={style}
            onKeyDown={onListKey}
          >
            {options.map((o, i) => (
              // biome-ignore lint/a11y/useKeyWithClickEvents lint/a11y/useFocusableInteractive: the listbox keeps focus and handles the keys (aria-activedescendant)
              <div
                key={String(o.value)}
                id={optionId(i)}
                role="option"
                aria-selected={chosen(o.value)}
                aria-disabled={o.disabled || undefined}
                data-active={i === active}
                className={`flex cursor-default items-center gap-2 rounded-lg py-1.5 pr-2.5 pl-2 whitespace-nowrap aria-disabled:opacity-45 ${OPTION[tone]}`}
                onPointerMove={() => !o.disabled && i !== active && setActive(i)}
                onClick={() => pick(i)}
              >
                <span className="flex w-4 shrink-0 justify-center">
                  {chosen(o.value) && <Icon name="check" className="size-3.5 text-c" />}
                </span>
                <span className="min-w-0 flex-1 truncate">{o.label}</span>
                {o.meta !== undefined && (
                  <span className="shrink-0 pl-3 text-[0.92em] opacity-60">{o.meta}</span>
                )}
              </div>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}
