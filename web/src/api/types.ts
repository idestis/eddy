// Wire types for the hub API. They mirror internal/model, internal/store and
// docs/api.md; keep them in sync when the contract changes.

export type Status = "ready" | "failed" | "reconciling" | "suspended" | "unknown";

export const STATUSES: readonly Status[] = ["failed", "reconciling", "suspended", "unknown", "ready"];

/** Identifies an object inside one cluster (model.Ref). */
export interface Ref {
  group: string;
  kind: string;
  namespace: string;
  name: string;
}

export interface Condition {
  type: string;
  status: string;
  reason?: string;
  message?: string;
  lastTransitionTime?: string;
}

/** The summary of one Flux object or workload (model.Resource). */
export interface Resource extends Ref {
  /** "<group>/<Kind>/<namespace>/<name>" */
  id: string;
  version: string;
  status: Status;
  message?: string;
  suspended?: boolean;
  conditions?: Condition[];
  revision?: string;
  source?: Ref;
  owner?: Ref;
  replicas?: string;
  images?: string[];
  /** A Pod's container names (not init containers), for the log picker. */
  containers?: string[];
  interval?: string;
  url?: string;
  chart?: string;
  inventory?: number;
  labels?: Record<string, string>;
  /**
   * Proposed (backend backlog #1): the object is known only from a Flux inventory or Helm
   * labels and Eddy does not watch its kind (ConfigMap, Secret, ServiceAccount, CRDs…).
   * It carries kind, namespace and name only, with status "unknown".
   */
  inventoryOnly?: boolean;
  /** Proposed: Ingress hosts. */
  hosts?: string[];
  /** Proposed: Service ports, e.g. "80/TCP → 8080". */
  ports?: string[];
  /** Proposed: CronJob schedule. */
  schedule?: string;
  createdAt?: string;
  lastChanged?: string;
  resourceVersion: string;
}

export interface KubeEvent {
  type: "Normal" | "Warning";
  reason: string;
  message: string;
  count: number;
  source?: string;
  first?: string;
  last?: string;
}

export interface ClusterInfo {
  name: string;
  displayName: string;
  environment?: string;
  region?: string;
  color?: string;
  protected: boolean;
  order: number;
  connected: boolean;
  lastSeen?: string;
  agentVersion?: string;
  kubernetesVersion?: string;
  fluxVersion?: string;
  /** "local" when the agent runs in local mode (dev builds only): it acts as the developer's kubeconfig identity. */
  mode?: "local";
  /** The agent refuses reconcile, suspend and resume. */
  readOnly?: boolean;
  /** The kubeconfig context a local-mode agent serves. */
  context?: string;
  counts?: Partial<Record<Status, number>>;
}

export interface Features {
  ai: boolean;
  aiProvider?: string;
  mcp: boolean;
  mcpWrites: boolean;
  logs: boolean;
  ephemeralStore: boolean;
  devMode: boolean;
  /** The hub may add clusters and issue join tokens (ADR-0005, `onboarding.enabled`). */
  onboarding: boolean;
}

export interface Me {
  user: string;
  display: string;
  groups: string[];
  provider: string;
  csrf: string;
  features: Features;
  version: string;
}

export interface Providers {
  local: boolean;
  proxy: boolean;
  dev: boolean;
}

export interface List<T> {
  items: T[];
}

export interface Page<T> extends List<T> {
  next?: string;
}

export interface ResourceSnapshot {
  items: Resource[];
  resourceVersion: string;
}

/** The target of a thread (store.ResourceRef). Kind "" means the whole cluster. */
export interface ResourceRef extends Ref {
  cluster: string;
}

export type AuthorType = "human" | "ai" | "system";

export interface Author {
  type: AuthorType;
  subject: string;
  display: string;
  /** web | mcp | askai */
  via: string;
  /** e.g. "claude-code", or the model id for AI messages */
  client?: string;
}

export type ThreadType = "discussion" | "ask";
export type ThreadStatus = "open" | "resolved";

export interface Thread {
  id: string;
  ref: ResourceRef;
  type: ThreadType;
  visibility: "resource" | "private";
  title: string;
  status: ThreadStatus;
  createdBy: Author;
  createdAt: string;
  updatedAt: string;
  resolvedBy?: string;
  resolvedAt?: string;
  messageCount: number;
}

export interface Message {
  id: string;
  threadId: string;
  author: Author;
  body: string;
  meta?: unknown;
  createdAt: string;
}

export interface ThreadDetail {
  thread: Thread;
  messages: Message[];
  next?: string;
}

export interface AskStep {
  tool: string;
  args: Record<string, unknown>;
  bytes: number;
}

export interface AskResponse {
  threadId: string;
  message: Message;
  steps: AskStep[];
}

export type TokenScope = "read" | "operate";

export interface TokenItem {
  id: string;
  name: string;
  scopes: TokenScope[];
  createdAt: string;
  expiresAt: string;
  lastUsedAt?: string;
}

export interface CreatedToken {
  token: string;
  item: TokenItem;
}

export interface AuditEvent {
  id: number;
  ts: string;
  requestId?: string;
  subject: string;
  groups: string[];
  via: string;
  tokenId?: string;
  action: string;
  target?: ResourceRef;
  result: "ok" | "denied" | "error";
  detail?: unknown;
}

/** SSE `change` payload. */
export interface ChangeEvent {
  cluster: string;
  upserts: Resource[];
  deletes: string[];
}

// Cluster onboarding (ADR-0005).

export type ClusterPhase = "Pending" | "Connected" | "Disconnected";

/** A Cluster CR as the onboarding endpoints return it. */
export interface OnboardedCluster {
  name: string;
  displayName: string;
  environment?: string;
  region?: string;
  color?: string;
  protected: boolean;
  order: number;
  phase: ClusterPhase;
  /** Declared in the eddy-hub chart values: edit it there, not in the UI. */
  managedBy?: "helm";
}

/** What GET /clusters/permissions says the signed-in user may do in the management cluster. */
export interface ClusterPermissions {
  onboarding: boolean;
  create: boolean;
}

/** The body of POST /clusters and PATCH /clusters/{c}. */
export interface ClusterInput {
  name: string;
  displayName?: string;
  environment?: string;
  region?: string;
  /** "#RRGGBB" */
  color?: string;
  protected?: boolean;
  order?: number;
  /** Join token lifetime, e.g. "1h". */
  ttl?: string;
}

export interface JoinToken {
  token: string;
  expiresAt: string;
}

/** The install guide in every form the connect screen offers. */
export interface InstallGuide {
  hubURL: string;
  namespace: string;
  helm: string;
  values: string;
  manifests: string;
  clusterResource: string;
  networkDocs: string;
  warnings?: string[];
}

export interface CreatedCluster {
  cluster: OnboardedCluster;
  joinToken: JoinToken;
  guide: InstallGuide;
}

export interface IssuedJoinToken {
  joinToken: JoinToken;
  guide: InstallGuide;
}

export type CheckId =
  | "connected"
  | "protocol"
  | "flux"
  | "informers"
  | "sar"
  | "impersonation"
  | "namespaces"
  | "credentials";

export type CheckState = "ok" | "warn" | "fail" | "pending" | "info";

export interface ConnectionCheck {
  id: CheckId;
  label: string;
  state: CheckState;
  detail?: string;
  docs?: string;
}

export interface JoinTokenInfo {
  id: string;
  state: "active" | "used" | "expired" | "revoked";
  createdBy: string;
  createdAt: string;
  expiresAt: string;
  usedAt?: string;
}

export type AttemptReason =
  | "bad_token"
  | "join_expired"
  | "join_used"
  | "wrong_cluster"
  | "protocol_mismatch"
  | "hello_rejected"
  | "credentials_failed";

/** A rejected agent connection for this cluster name. The hub keeps the last 20. */
export interface ConnectionAttempt {
  at: string;
  reason: AttemptReason;
  detail?: string;
  peer?: string;
  hubPod?: string;
}

export interface ConnectionInfo {
  cluster: OnboardedCluster;
  checks: ConnectionCheck[];
  agents: number;
  joinToken?: JoinTokenInfo;
  attempts: ConnectionAttempt[];
  permissions: { update: boolean; edit: boolean; delete: boolean };
  /** The guide with a "<join token>" placeholder. */
  guide: InstallGuide;
}
