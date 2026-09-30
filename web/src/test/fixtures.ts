import type { Resource } from "../api/types";

/** A minimal valid Resource for tests. */
export function resource(name: string, patch: Partial<Resource> = {}): Resource {
  const kind = patch.kind ?? "Kustomization";
  const namespace = patch.namespace ?? "flux-system";
  const group = patch.group ?? "kustomize.toolkit.fluxcd.io";
  return {
    group,
    kind,
    namespace,
    name,
    id: `${group}/${kind}/${namespace}/${name}`,
    version: "v1",
    status: "ready",
    resourceVersion: "1",
    ...patch,
  };
}
