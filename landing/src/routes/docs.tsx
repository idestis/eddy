import { createFileRoute, Link, Outlet } from "@tanstack/react-router";
import { docsNav } from "../lib/docs";

export const Route = createFileRoute("/docs")({
  component: DocsLayout,
});

function DocsLayout() {
  return (
    <div className="wrap grid gap-6 pt-8 pb-16 lg:grid-cols-[220px_minmax(0,1fr)] lg:gap-12 lg:pt-10 lg:pb-24">
      <nav aria-label="Documentation" className="self-start text-[0.92rem] lg:sticky lg:top-20">
        <h2 className="mb-3 font-mono text-xs leading-none font-semibold tracking-[0.08em] text-ink-3 uppercase">
          Docs
        </h2>
        <ul className="m-0 flex list-none flex-wrap gap-2 p-0 lg:flex-col lg:gap-0.5">
          {docsNav.map((d) => (
            <li key={d.to}>
              <Link
                to={d.to}
                activeOptions={{ exact: true }}
                activeProps={{ "aria-current": "page" }}
                className="flex min-h-10 items-center rounded-lg border border-line bg-surface px-3 text-ink-2 no-underline hover:bg-surface-sunken hover:text-ink hover:no-underline aria-[current=page]:border-accent/40 aria-[current=page]:bg-accent/10 aria-[current=page]:font-semibold aria-[current=page]:text-ink lg:border-transparent lg:bg-transparent"
              >
                {d.title}
              </Link>
            </li>
          ))}
        </ul>
      </nav>
      <article className="prose">
        <Outlet />
      </article>
    </div>
  );
}
