// Browser tab titles: "<resource or page> · <cluster> · eddy", with the product name
// always in lower case, for example "apps · staging · eddy" or "fleet · eddy".

import { useEffect } from "react";

export const TITLE_SEPARATOR = " · ";

export function pageTitle(...parts: Array<string | undefined | null | false>): string {
  return [...parts.filter((p): p is string => typeof p === "string" && p.trim() !== ""), "eddy"].join(
    TITLE_SEPARATOR,
  );
}

/** Sets document.title while the calling component is mounted. */
export function useTitle(...parts: Array<string | undefined | null | false>): void {
  const title = pageTitle(...parts);
  useEffect(() => {
    document.title = title;
  }, [title]);
}
