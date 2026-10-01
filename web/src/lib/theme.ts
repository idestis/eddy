// Light/dark theme. The system preference applies unless the user picked one;
// the choice is kept in localStorage and set as <html data-theme>, and saved with the
// view preferences so it follows the user to other browsers.

import { getViewPrefs, setViewPrefs, subscribeViewPrefs } from "./viewPrefs";

export type Theme = "light" | "dark";

const STORAGE_KEY = "eddy.theme";

function stored(): Theme | null {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    return v === "light" || v === "dark" ? v : null;
  } catch {
    return null;
  }
}

export function effectiveTheme(): Theme {
  const explicit = document.documentElement.dataset.theme;
  if (explicit === "light" || explicit === "dark") return explicit;
  return matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

/** Applies the stored choice; call before the first render. */
export function initTheme(): void {
  const t = stored() ?? getViewPrefs().theme ?? null;
  if (t) document.documentElement.dataset.theme = t;
  // A theme picked on another device arrives with the hub's prefs.
  subscribeViewPrefs(() => {
    const v = getViewPrefs().theme;
    if (v && v !== document.documentElement.dataset.theme) apply(v);
  });
}

function apply(t: Theme): void {
  document.documentElement.dataset.theme = t;
  try {
    localStorage.setItem(STORAGE_KEY, t);
  } catch {
    // Private mode or blocked storage: the toggle still works for this page.
  }
}

export function toggleTheme(): Theme {
  const next: Theme = effectiveTheme() === "dark" ? "light" : "dark";
  apply(next);
  setViewPrefs({ theme: next });
  return next;
}
