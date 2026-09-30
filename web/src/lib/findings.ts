// Cluster findings (ClusterInfo.findings): conditions about a cluster that are not a single
// resource, such as a build-up of finished Jobs. Only "warning" findings need attention.

import type { ClusterInfo, Finding } from "../api/types";

const inNamespace = (f: Finding, namespace: string | undefined) => !namespace || f.namespace === namespace;

/** Warning findings of a cluster, optionally limited to one namespace. */
export function warningFindings(cluster: ClusterInfo | undefined, namespace?: string): Finding[] {
  return (cluster?.findings ?? []).filter((f) => f.severity === "warning" && inNamespace(f, namespace));
}

/** How many finished Jobs the agent hides from the list, optionally in one namespace. */
export function hiddenJobCount(cluster: ClusterInfo | undefined, namespace?: string): number {
  let n = 0;
  for (const f of cluster?.findings ?? []) {
    if (f.kind === "job-buildup" && inNamespace(f, namespace)) n += f.jobs?.hidden ?? 0;
  }
  return n;
}
