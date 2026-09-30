import { Link, useRouterState } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { REPO_URL } from "../lib/site";
import { LogoMark } from "./icons";
import { ThemeToggle } from "./ThemeToggle";

const linkClass =
  "inline-flex min-h-10 items-center rounded-lg px-3 text-[0.93rem] text-ink-2 no-underline hover:bg-surface-sunken hover:text-ink hover:no-underline aria-[current=page]:bg-surface-sunken aria-[current=page]:text-ink";

function NavLinks() {
  return (
    <>
      <li>
        <Link
          to="/"
          hash="features"
          className={linkClass}
          activeOptions={{ includeHash: true }}
          activeProps={{ "aria-current": undefined }}
        >
          Features
        </Link>
      </li>
      <li>
        <Link
          to="/"
          hash="how"
          className={linkClass}
          activeOptions={{ includeHash: true }}
          activeProps={{ "aria-current": undefined }}
        >
          How it works
        </Link>
      </li>
      <li>
        <Link to="/docs/" className={linkClass} activeProps={{ "aria-current": "page" }}>
          Docs
        </Link>
      </li>
      <li>
        <a href={REPO_URL} className={linkClass}>
          GitHub
        </a>
      </li>
    </>
  );
}

export function Header() {
  const menu = useRef<HTMLDetailsElement>(null);
  const href = useRouterState({ select: (s) => s.location.href });

  // The mobile menu is a <details>, so it works without JS; close it after any navigation.
  // biome-ignore lint/correctness/useExhaustiveDependencies: href is the trigger
  useEffect(() => {
    if (menu.current) menu.current.open = false;
  }, [href]);

  return (
    <header className="sticky top-0 z-20 border-b border-line bg-page/80 backdrop-blur-lg backdrop-saturate-150">
      <div className="wrap relative flex h-14 items-center justify-between gap-3">
        <Link
          to="/"
          aria-label="Eddy home"
          className="inline-flex min-h-10 items-center gap-2.5 text-[1.1rem] font-bold tracking-tight text-ink no-underline hover:no-underline"
        >
          <LogoMark className="size-6 text-accent" />
          Eddy
        </Link>

        <div className="flex items-center gap-1">
          <nav aria-label="Primary" className="hidden sm:block">
            <ul className="m-0 flex list-none items-center gap-0.5 p-0">
              <NavLinks />
            </ul>
          </nav>
          <ThemeToggle />
          <details
            ref={menu}
            className="group sm:hidden"
            onKeyDown={(e) => {
              if (e.key === "Escape" && menu.current) {
                menu.current.open = false;
                menu.current.querySelector("summary")?.focus();
              }
            }}
          >
            <summary
              aria-label="Menu"
              className="inline-grid size-10 cursor-pointer list-none place-items-center rounded-lg text-ink-2 hover:bg-surface-sunken hover:text-ink [&::-webkit-details-marker]:hidden"
            >
              <svg
                viewBox="0 0 20 20"
                width="20"
                height="20"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
                aria-hidden="true"
              >
                <path className="group-open:hidden" d="M3 6h14M3 10h14M3 14h14" />
                <path className="hidden group-open:block" d="M5 5l10 10M15 5L5 15" />
              </svg>
            </summary>
            <nav
              aria-label="Primary"
              className="absolute inset-x-0 top-full border-b border-line bg-page px-4 pt-1 pb-3 shadow-window"
            >
              <ul className="m-0 grid list-none p-0">
                <NavLinks />
              </ul>
            </nav>
          </details>
        </div>
      </div>
    </header>
  );
}
