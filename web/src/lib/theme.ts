// Light/dark theme. The system preference applies unless the user picked one;
// the choice is kept in localStorage and set as <html data-theme>.

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
  const t = stored();
  if (t) document.documentElement.dataset.theme = t;
}

export function toggleTheme(): Theme {
  const next: Theme = effectiveTheme() === "dark" ? "light" : "dark";
  document.documentElement.dataset.theme = next;
  try {
    localStorage.setItem(STORAGE_KEY, next);
  } catch {
    // Private mode or blocked storage: the toggle still works for this page.
  }
  return next;
}
