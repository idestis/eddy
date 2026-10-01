// Remembers where the user was in each cluster's list, so "back" (h) from a
// detail page returns to the same filter and selection.

import type { StatusFilter } from "./resourceRows";

export interface ListSearch {
  filter?: string;
  kind?: string;
  status?: StatusFilter;
  view?: "grouped" | "flat" | "graph";
}

interface ListPlace {
  search: ListSearch;
  selectedId?: string;
}

const places = new Map<string, ListPlace>();

export function rememberList(cluster: string, place: ListPlace): void {
  places.set(cluster, place);
}

export function recallList(cluster: string): ListPlace {
  return places.get(cluster) ?? { search: {} };
}
