import { useEffect, useState } from "react";

const KEY = "eddy-theme";

/** Flips light/dark on <html data-theme> and remembers the choice. The no-flash init script is in index.html. */
export function ThemeToggle() {
  const [ready, setReady] = useState(false);
  useEffect(() => setReady(true), []);

  // Reserve the space before hydration so the header does not shift, and show nothing without JS.
  if (!ready) return <span aria-hidden="true" className="size-10 shrink-0" />;

  const toggle = () => {
    const root = document.documentElement;
    const current =
      root.dataset.theme ?? (matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light");
    const next = current === "dark" ? "light" : "dark";
    root.dataset.theme = next;
    try {
      localStorage.setItem(KEY, next);
    } catch {
      // Storage can be blocked; the choice then lasts for this page view only.
    }
  };

  return (
    <button
      type="button"
      onClick={toggle}
      aria-label="Switch colour theme"
      className="inline-grid size-10 shrink-0 cursor-pointer place-items-center rounded-lg text-ink-2 hover:bg-surface-sunken hover:text-ink"
    >
      <svg viewBox="0 0 20 20" width="18" height="18" aria-hidden="true">
        <circle cx="10" cy="10" r="7" fill="none" stroke="currentColor" strokeWidth="1.6" />
        <path d="M10 3a7 7 0 0 1 0 14z" fill="currentColor" />
      </svg>
    </button>
  );
}
