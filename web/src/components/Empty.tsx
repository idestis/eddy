import type { ReactNode } from "react";

/** A centred empty or error state. */
export function Empty({ title, children, alert }: { title?: string; children?: ReactNode; alert?: boolean }) {
  return (
    <div className="px-5 py-11 text-center text-ink-3" role={alert ? "alert" : undefined}>
      {title && <strong className="mb-1 block text-15 font-semibold text-ink">{title}</strong>}
      {children}
    </div>
  );
}
