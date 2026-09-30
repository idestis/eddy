import { type KeyboardEvent, useEffect, useId, useRef, useState } from "react";
import { LABEL_MAX } from "../lib/onboarding";

export const ENVIRONMENTS = ["Production", "Staging", "Development", "Edge", "Sandbox"] as const;
const OTHER = "Other…";
const OPTIONS = [...ENVIRONMENTS, OTHER] as const;

const CHIP =
  "inline-flex h-8 items-center rounded-full border border-line-strong bg-surface px-3.5 text-13 font-medium text-ink-2 hover:border-c/60 hover:text-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-c aria-checked:border-c aria-checked:bg-c-soft aria-checked:text-ink";
const PROD_CHIP =
  "aria-checked:border-prod aria-checked:bg-prod/12 aria-checked:text-prod hover:border-prod/60 focus-visible:outline-prod";

/**
 * The optional Environment field: one chip per common value and "Other…", which reveals a text
 * input. A radiogroup: arrow keys move and select, Tab enters the checked chip (or the first).
 * Activating the checked chip clears the field again.
 */
export function EnvironmentPicker({
  value,
  onChange,
  error,
}: {
  value: string;
  onChange: (v: string) => void;
  error?: string;
}) {
  const id = useId();
  const isPreset = (ENVIRONMENTS as readonly string[]).includes(value);
  const [other, setOther] = useState(value !== "" && !isPreset);
  const chips = useRef<Array<HTMLButtonElement | null>>([]);
  const text = useRef<HTMLInputElement>(null);
  const wantFocus = useRef(false);

  // The input mounts after "Other…" is chosen by click or Enter; arrow keys leave focus on the chip.
  useEffect(() => {
    if (other && wantFocus.current) text.current?.focus();
    wantFocus.current = false;
  }, [other]);

  const checked = other ? OTHER : isPreset ? value : "";
  const tabbable = checked || OPTIONS[0];

  const choose = (option: string, focusInput: boolean) => {
    if (option === OTHER) {
      wantFocus.current = focusInput;
      setOther(true);
      if (isPreset) onChange("");
      if (focusInput && other) text.current?.focus();
    } else {
      setOther(false);
      onChange(option);
    }
  };

  const clear = () => {
    setOther(false);
    onChange("");
  };

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const step = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 }[e.key];
    if (step === undefined && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    const last = OPTIONS.length - 1;
    const next =
      e.key === "Home" ? 0 : e.key === "End" ? last : (i + (step ?? 0) + OPTIONS.length) % OPTIONS.length;
    const option = OPTIONS[next];
    if (!option) return;
    choose(option, false);
    chips.current[next]?.focus();
  };

  return (
    <div className="field">
      <span id={`${id}-label`}>Environment</span>
      <div role="radiogroup" aria-labelledby={`${id}-label`} className="flex flex-wrap gap-2">
        {OPTIONS.map((option, i) => {
          const on = option === checked;
          return (
            // biome-ignore lint/a11y/useSemanticElements: see above
            <button
              key={option}
              ref={(el) => {
                chips.current[i] = el;
              }}
              type="button"
              role="radio"
              aria-checked={on}
              tabIndex={option === tabbable ? 0 : -1}
              className={`${CHIP} ${option === "Production" ? PROD_CHIP : ""}`}
              onClick={() => (on ? clear() : choose(option, true))}
              onKeyDown={(e) => onKeyDown(e, i)}
            >
              {option}
            </button>
          );
        })}
      </div>
      {other && (
        <input
          ref={text}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="Custom environment, e.g. QA"
          aria-label="Custom environment"
          maxLength={LABEL_MAX + 10}
          autoComplete="off"
          aria-invalid={Boolean(error)}
          aria-describedby={error ? `${id}-err` : undefined}
        />
      )}
      {error && (
        <span id={`${id}-err`} className="text-12 text-bad">
          {error}
        </span>
      )}
    </div>
  );
}
