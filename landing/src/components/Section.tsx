import type { ReactNode } from "react";

export function Section({
  id,
  labelledBy,
  alt = false,
  children,
}: {
  id?: string;
  labelledBy: string;
  alt?: boolean;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={labelledBy}
      className={`py-14 sm:py-20 ${alt ? "border-y border-line bg-page-alt" : ""}`}
    >
      <div className="wrap">{children}</div>
    </section>
  );
}

export function SectionHead({
  id,
  eyebrow,
  title,
  children,
}: {
  id: string;
  eyebrow: string;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="mb-8 max-w-[40em] sm:mb-10">
      <p className="mb-3 font-mono text-[0.78rem] leading-none font-semibold tracking-[0.08em] text-accent uppercase">
        {eyebrow}
      </p>
      <h2 id={id} className="mb-3 text-[clamp(1.55rem,3.6vw,2.15rem)] font-semibold">
        {title}
      </h2>
      {children && <div className="text-[1.05rem] text-ink-2">{children}</div>}
    </div>
  );
}
