import { type Ref, type TextareaHTMLAttributes, useImperativeHandle, useLayoutEffect, useRef } from "react";

interface Props extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  /** The tallest the box grows before it scrolls, in px. */
  maxHeight?: number;
  ref?: Ref<HTMLTextAreaElement | null>;
}

/**
 * A textarea that starts at one line and grows with its content up to `maxHeight`.
 * `field-sizing: content` does it natively where supported; the height is also set from
 * scrollHeight so Safari and Firefox behave the same.
 */
export function AutoGrowTextarea({ maxHeight = 160, className = "", ref, value, ...rest }: Props) {
  const inner = useRef<HTMLTextAreaElement>(null);
  useImperativeHandle(ref, () => inner.current as HTMLTextAreaElement, []);

  // biome-ignore lint/correctness/useExhaustiveDependencies: resize whenever the value changes
  useLayoutEffect(() => {
    const el = inner.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, maxHeight)}px`;
    el.style.overflowY = el.scrollHeight > maxHeight ? "auto" : "hidden";
  }, [value, maxHeight]);

  return (
    <textarea
      ref={inner}
      rows={1}
      value={value}
      className={`resize-none [field-sizing:content] ${className}`}
      {...rest}
    />
  );
}
