import type { ReactNode } from "react";

/** A keycap. The look (bordered, mono, small radius, tokens only) lives on `kbd` in styles/app.css. */
export function Kbd({ children }: { children: ReactNode }) {
  return <kbd>{children}</kbd>;
}
