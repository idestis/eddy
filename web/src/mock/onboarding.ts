// Mock-mode pieces of cluster onboarding (ADR-0005): join tokens, the install guide
// in the same shape the hub renders, and the checklists for connected clusters.

import type { ConnectionCheck, InstallGuide, OnboardedCluster } from "../api/types";

export const HUB_URL = "wss://eddy-agents.acme.dev/agent/v1/connect";
export const AGENT_NAMESPACE = "eddy-system";
const CHART = "oci://ghcr.io/idestis/charts/eddy-agent";
const CHART_VERSION = "1.0.0";
const DOCS = "https://github.com/idestis/eddy/blob/main/docs/install.md";
export const NETWORK_DOCS = `${DOCS}#network-requirements`;
export const IMPERSONATION_DOCS = `${DOCS}#pin-impersonation`;
export const TOKEN_PLACEHOLDER = "<join token>";

const BASE62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
const base62 = (n: number) =>
  Array.from({ length: n }, () => BASE62[Math.floor(Math.random() * 62)]).join("");

/** `eddy_join_<id:12><secret:32><crc:6>`; the mock does not compute a real checksum. */
export function joinToken(): { id: string; token: string } {
  const id = base62(12);
  return { id, token: `eddy_join_${id}${base62(32)}${base62(6)}` };
}

/** The install guide, mirroring the hub's rendering. */
export function installGuide(c: OnboardedCluster, token: string, warnings?: string[]): InstallGuide {
  const helm = [
    `helm upgrade --install eddy-agent ${CHART} \\`,
    `  --version ${CHART_VERSION} \\`,
    `  --namespace ${AGENT_NAMESPACE} --create-namespace \\`,
    `  --set cluster.name=${c.name} \\`,
    `  --set hub.url=${HUB_URL} \\`,
    `  --set-string joinToken=${token}`,
  ].join("\n");

  const values = [
    "# values.yaml for the eddy-agent chart",
    `# helm upgrade --install eddy-agent ${CHART} --version ${CHART_VERSION} -n ${AGENT_NAMESPACE} -f values.yaml`,
    "cluster:",
    `  name: ${c.name}`,
    "hub:",
    `  url: ${HUB_URL}`,
    "# Single use and short-lived, so committing it is acceptable; a SOPS or",
    "# External Secrets reference (token.existingSecret) is better.",
    `joinToken: ${token}`,
  ].join("\n");

  const manifests = [
    "apiVersion: v1",
    "kind: Namespace",
    "metadata:",
    `  name: ${AGENT_NAMESPACE}`,
    "---",
    "apiVersion: v1",
    "kind: Secret",
    "metadata:",
    "  name: eddy-agent-token",
    `  namespace: ${AGENT_NAMESPACE}`,
    "type: Opaque",
    "stringData:",
    `  token: ${token}`,
    "---",
    "apiVersion: v1",
    "kind: ServiceAccount",
    "metadata:",
    "  name: eddy-agent",
    `  namespace: ${AGENT_NAMESPACE}`,
    "---",
    "# The agent's only write permission: its own token Secret.",
    "apiVersion: rbac.authorization.k8s.io/v1",
    "kind: Role",
    "metadata:",
    "  name: eddy-agent-token",
    `  namespace: ${AGENT_NAMESPACE}`,
    "rules:",
    '  - apiGroups: [""]',
    '    resources: ["secrets"]',
    '    resourceNames: ["eddy-agent-token"]',
    '    verbs: ["get", "update"]',
    "---",
    "# ClusterRole and bindings for read-only Flux access and impersonation are omitted here;",
    "# render them with `helm template` for the full set.",
    "apiVersion: apps/v1",
    "kind: Deployment",
    "metadata:",
    "  name: eddy-agent",
    `  namespace: ${AGENT_NAMESPACE}`,
    "spec:",
    "  replicas: 1",
    "  selector:",
    "    matchLabels: { app.kubernetes.io/name: eddy-agent }",
    "  template:",
    "    metadata:",
    "      labels: { app.kubernetes.io/name: eddy-agent }",
    "    spec:",
    "      serviceAccountName: eddy-agent",
    "      containers:",
    "        - name: agent",
    `          image: ghcr.io/idestis/eddy-agent:v${CHART_VERSION}`,
    "          env:",
    `            - { name: EDDY_CLUSTER, value: ${c.name} }`,
    `            - { name: EDDY_HUB_URL, value: "${HUB_URL}" }`,
    "            - { name: EDDY_TOKEN_SECRET, value: eddy-agent-token }",
  ].join("\n");

  const spec = [
    `  displayName: ${JSON.stringify(c.displayName || c.name)}`,
    c.environment ? `  environment: ${JSON.stringify(c.environment)}` : "",
    c.region ? `  region: ${JSON.stringify(c.region)}` : "",
    c.color ? `  color: "${c.color}"` : "",
    `  protected: ${c.protected}`,
    `  order: ${c.order}`,
  ].filter(Boolean);
  const clusterResource = [
    "apiVersion: gitops.eddy.dev/v1alpha1",
    "kind: Cluster",
    "metadata:",
    `  name: ${c.name}`,
    "  namespace: eddy",
    "spec:",
    ...spec,
  ].join("\n");

  return {
    hubURL: HUB_URL,
    namespace: AGENT_NAMESPACE,
    helm,
    values,
    manifests,
    clusterResource,
    networkDocs: NETWORK_DOCS,
    warnings,
  };
}

/** The checklist of a healthy, long-connected cluster. */
export function greenChecks(opts: {
  flux?: string;
  resources: number;
  namespaces?: string;
  impersonation?: "ok" | "warn";
}): ConnectionCheck[] {
  return [
    { id: "connected", label: "Agent connected", state: "ok", detail: "1 agent session" },
    { id: "protocol", label: "Protocol version", state: "ok", detail: "v1, compatible with this hub" },
    {
      id: "flux",
      label: "Flux detected",
      state: "ok",
      detail: `Flux ${opts.flux ?? "v2.7.0"}, 12 kinds served`,
    },
    {
      id: "informers",
      label: "Informers synced",
      state: "ok",
      detail: `${opts.resources} resources`,
    },
    { id: "sar", label: "SubjectAccessReview works", state: "ok" },
    opts.impersonation === "warn"
      ? {
          id: "impersonation",
          label: "Impersonation pinned",
          state: "warn",
          detail:
            "The agent may impersonate system:masters. Limit its ClusterRole to users and eddy: groups.",
          docs: IMPERSONATION_DOCS,
        }
      : {
          id: "impersonation",
          label: "Impersonation pinned",
          state: "ok",
          detail: "The agent cannot impersonate system:masters.",
          docs: IMPERSONATION_DOCS,
        },
    {
      id: "namespaces",
      label: "Watched namespaces",
      state: "info",
      detail: opts.namespaces ?? "All namespaces",
    },
    {
      id: "credentials",
      label: "Permanent token stored",
      state: "ok",
      detail: `${AGENT_NAMESPACE}/eddy-agent-token`,
    },
  ];
}

/** The checklist before any agent has connected. */
export function pendingChecks(lastSeen?: string): ConnectionCheck[] {
  return [
    {
      id: "connected",
      label: "Agent connected",
      state: lastSeen ? "fail" : "pending",
      detail: lastSeen ? `No agent session. Last seen ${lastSeen}.` : "Waiting for the agent to dial in",
    },
    { id: "protocol", label: "Protocol version", state: "pending" },
    { id: "flux", label: "Flux detected", state: "pending" },
    { id: "informers", label: "Informers synced", state: "pending" },
    { id: "sar", label: "SubjectAccessReview works", state: "pending" },
    { id: "impersonation", label: "Impersonation pinned", state: "pending", docs: IMPERSONATION_DOCS },
    { id: "namespaces", label: "Watched namespaces", state: "pending" },
    { id: "credentials", label: "Permanent token stored", state: "pending" },
  ];
}
