import type { ReactNode } from "react";

/** A page heading with an optional one-line description. */
export function PageHead({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <h1 className="text-20 font-semibold tracking-tight">{title}</h1>
      {children && <p className="text-13 text-ink-3">{children}</p>}
    </div>
  );
}
