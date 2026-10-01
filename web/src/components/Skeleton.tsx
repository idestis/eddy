// Placeholders shown while data loads (ADR-0006 "Render order"): counts first, then rows.
// The shimmer is a CSS class (`skeleton` in app.css), off under prefers-reduced-motion.

/** A grey bar where a number or a short text will appear. */
export function SkeletonText({ className = "w-16", label }: { className?: string; label?: string }) {
  return (
    <span className="inline-flex" role={label ? "status" : undefined}>
      <span
        className={`skeleton inline-block h-[0.9em] translate-y-[0.1em] rounded ${className}`}
        aria-hidden="true"
      />
      {label && <span className="sr-only">{label}</span>}
    </span>
  );
}

const WIDTHS = ["w-[38%]", "w-[52%]", "w-[30%]", "w-[46%]", "w-[60%]", "w-[34%]"];

/** Rows of the resource list's height (40 px) while the first page loads. */
export function SkeletonRows({
  count = 12,
  label = "Loading resources",
}: {
  count?: number;
  label?: string;
}) {
  return (
    <div
      className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-card border border-line bg-surface"
      role="status"
      aria-label={label}
      aria-busy="true"
    >
      {Array.from({ length: count }, (_, i) => (
        <div
          // biome-ignore lint/suspicious/noArrayIndexKey: placeholders are positional
          key={i}
          className="flex h-10 shrink-0 items-center gap-3 border-b border-line px-3.5 last:border-b-0"
          aria-hidden="true"
        >
          <span className="skeleton size-4 shrink-0 rounded-full" />
          <span className={`skeleton h-3 rounded ${WIDTHS[i % WIDTHS.length]}`} />
          <span className="skeleton ml-auto h-3 w-14 rounded" />
        </div>
      ))}
    </div>
  );
}
