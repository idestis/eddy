// The Browse tree of a cluster, built from its kinds (GET …/kinds). A hub without the
// endpoint, or a request still in flight, gives the static tree.

import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { kindsQuery } from "../api/queries";
import { buildNav, type NavNode } from "./kinds";

export function useNav(cluster: string | undefined, connected = true): readonly NavNode[] {
  const { data } = useQuery({ ...kindsQuery(cluster ?? ""), enabled: Boolean(cluster) && connected });
  return useMemo(() => buildNav(data), [data]);
}
