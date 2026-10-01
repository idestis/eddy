// Wire types for the hub API. They mirror internal/model, internal/store and
// docs/api.md; keep them in sync when the contract changes.

/** "completed" is a finished Job (Complete condition) or a Pod in phase Succeeded: healthy, not live. */
export type Status = "ready" | "failed" | "reconciling" | "suspended" | "unknown" | "completed";

export const STATUSES: readonly Status[] = [
  "failed",
  "reconciling",
  "suspended",
  "unknown",
  "ready",
  "completed",
];

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
  /**
   * spec.dependsOn of a Kustomization (other Kustomizations) or a HelmRelease (other
   * HelmReleases), in spec order. A namespace left out in the spec is the object's own.
   */
  dependsOn?: Ref[];
  /**
   * Waiting for a dependency (Ready=False, reason DependencyNotReady). The status is then
   * "reconciling" and the message reads "Waiting for ns/name".
   */
  blocked?: boolean;
  /** Empty for finished Jobs; see `completions`. */
  replicas?: string;
  /** Jobs only: "succeeded/completions", e.g. "1/1". */
  completions?: string;
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
  /** The owning project: "kubernetes", "flux", "karpenter", "external-secrets" or the API group itself. */
  project?: string;
  /** Kind-specific facts in display order (at most 12), e.g. a NodePool's CPU usage against its limit. */
  details?: Array<{ label: string; value: string }>;
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

/** One row of GET …/kinds: a kind the agent watches, or one that appears only as inventory rows. */
export interface KindSummary {
  /** The API group; "" for core. */
  group: string;
  kind: string;
  plural?: string;
  namespaced: boolean;
  project: string;
  /** False: the kind appears only as inventory-only rows. */
  watched: boolean;
  preset?: string;
  count: number;
}

export interface KindsResponse {
  items: KindSummary[];
  projects: Array<{ id: string; name: string }>;
  presets: string[];
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
  /** Watch presets the agent has on ("karpenter", "externalSecrets"). */
  presets?: string[];
  /** The kubeconfig context a local-mode agent serves. */
  context?: string;
  counts?: Partial<Record<Status, number>>;
  /** The hub is still computing counts for this cluster; a `counts` event fills them in. */
  countsPending?: boolean;
  /** `counts` per Kind (watched kinds only, inventory-only rows not counted), for navigation. */
  kinds?: KindCounts;
  /** The agent is gone but the hub still serves its last view, read-only; see `lastSeen`. */
  stale?: boolean;
  /** Cluster-level findings such as a build-up of finished Jobs. They are not resources. */
  findings?: Finding[];
}

/** Per-Kind status counts: `{Kind: {status: n}}`. */
export type KindCounts = Record<string, Partial<Record<Status, number>>>;

export type FindingSeverity = "warning" | "info";

export interface JobGroup {
  by: "owner" | "label" | "generateName" | "prefix" | "namespace";
  name: string;
  label: string;
  count: number;
  failed?: number;
  withoutTTL?: number;
  owner?: Ref;
}

export interface JobBuildup {
  hidden: number;
  finished: number;
  succeeded: number;
  failed: number;
  withoutTTL: number;
  standaloneFailed: number;
  oldest?: string;
  newest?: string;
  groups?: JobGroup[];
  threshold: number;
}

/** One per namespace with hidden finished Jobs. Only "warning" needs attention. */
export interface Finding {
  /** "job-buildup/<namespace>" */
  id: string;
  kind: "job-buildup";
  severity: FindingSeverity;
  namespace: string;
  message: string;
  recommendation?: string;
  jobs?: JobBuildup;
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
  /** Logs of workloads other than Pods (Deployments, Jobs…). */
  workloadLogs: boolean;
  /** Ask AI accepts log lines as attachments (`ai.allowLogs`). */
  aiLogs?: boolean;
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

export type LocalMode = "normal" | "breakglass";

/** A GitHub or OIDC sign-in provider from GET /auth/providers. */
export interface SignInProvider {
  id: string;
  name: string;
  kind: "github" | "oidc";
  /** A mark the SPA ships ("github"); absent means a neutral icon. */
  icon?: "github";
  /** GET this (with ?returnTo=) to start the browser flow. */
  loginURL: string;
}

export interface Providers {
  local: { enabled: boolean; mode: LocalMode };
  proxy: boolean;
  dev: boolean;
  providers: SignInProvider[];
}

export interface List<T> {
  items: T[];
}

export interface Page<T> extends List<T> {
  next?: string;
}

/**
 * Edge types of GET …/graph. Directions follow the spec fields: `dependsOn` goes from the
 * dependent to its dependency, `source` from the consumer to its source, `owns` from the
 * owner to the owned object (or group).
 */
export type GraphEdgeType = "dependsOn" | "source" | "owns";

/** A node of GET …/graph: a resource, a group of collapsed siblings, or a missing dependency. */
export interface GraphNode {
  /** A resource id, or `group:<ownerId>/<Kind>` for collapsed siblings. */
  id: string;
  kind: string;
  group: string;
  namespace: string;
  name?: string;
  status?: Status;
  message?: string;
  blocked?: boolean;
  revision?: string;
  lastChanged?: string;
  inventoryOnly?: boolean;
  /** A dependsOn target that is not in the view (absent, or not visible to the user). */
  missing?: boolean;
  /** Group nodes: how many siblings, by status, under which owner. */
  count?: number;
  statuses?: Partial<Record<Status, number>>;
  owner?: string;
}

export interface GraphEdge {
  from: string;
  to: string;
  type: GraphEdgeType;
}

export interface GraphResponse {
  nodes: GraphNode[];
  edges: GraphEdge[];
  truncated?: boolean;
  stale?: boolean;
}

export interface ResourceSnapshot {
  items: Resource[];
  resourceVersion: string;
  /** Served from the hub's last view of a disconnected cluster. */
  stale?: boolean;
}

/**
 * One row of the paged list (`…/resources?view=index`, ADR-0006 T1): the columns the list
 * renders and filters on, with short keys. The group is implied by the kind table unless `g`
 * says otherwise.
 */
/** One compact row of `…/resources?view=index` (ADR-0006 T1); full details load by id. */
export interface IndexRow {
  id: string;
  group: string;
  kind: string;
  namespace: string;
  name: string;
  status: Status;
  blocked?: boolean;
  /** Message, truncated by the hub. */
  message?: string;
  /** Short revision. */
  revision?: string;
  replicas?: string;
  completions?: string;
  owner?: Ref;
  project?: string;
  inventoryOnly?: boolean;
  lastChanged?: string;
}

export interface IndexPage {
  items: IndexRow[];
  /** Rows matching the filter, across every page. Its presence marks a hub that pages. */
  total: number;
  next?: string;
  facets?: {
    kinds?: Record<string, number>;
    statuses?: Partial<Record<Status, number>>;
    /** The most common namespaces among the matching rows. */
    namespaces?: { name: string; n: number }[];
  };
  stale?: boolean;
}

/** GET …/resources?kind=Job&includeHidden=1: listed Jobs plus a page of hidden finished ones. */
export interface JobsSnapshot extends ResourceSnapshot {
  hidden: { total: number; next?: string };
}

/** The target of a thread or an Ask AI context entry (store.ResourceRef). Kind "" means the whole cluster. */
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

export type ThreadType = "discussion";
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

/** Log lines or a YAML excerpt sent with a question (POST /ai/ask `attachments`). */
export interface AskAttachment {
  kind: "logs" | "yaml";
  /** Logs: "<ns>/<pod>/<container>" or "<ns>/<workload> · N pods". YAML: the resource id "<group>/<Kind>/<ns>/<name>". */
  source: string;
  lines: string[];
}

/** An Ask AI conversation (ADR-0007). It belongs to its owner, not to a resource. */
export interface Chat {
  id: string;
  owner: string;
  title: string;
  /** At most 10 references, from any cluster; kind "" means a whole cluster. */
  context: ResourceRef[];
  createdAt: string;
  updatedAt: string;
  messageCount: number;
}

/** GET /ai/chats/{id}: the chat and a page of its messages, oldest first. */
export interface ChatDetail {
  chat: Chat;
  messages: Message[];
  next?: string;
}

/** Whether the asking user can still see a context reference ("hidden" ones are not sent). */
export type ContextStatus = "ok" | "hidden";

/** POST /ai/ask. Without `chatId` a chat is created with `context`. */
export interface AskRequest {
  chatId?: string;
  /** Replaces the chat's context first; sent only after the user changed it. */
  context?: ResourceRef[];
  question: string;
  attachments?: AskAttachment[];
}

export interface AskResponse {
  chat: Chat;
  message: Message;
  steps: AskStep[];
  /** Same order as `chat.context`. */
  contextStatus: ContextStatus[];
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

/** SSE `change` payload. Sent only for watched clusters by hubs that take `watch=`. */
export interface ChangeEvent {
  cluster: string;
  upserts: Resource[];
  deletes: string[];
}

/** SSE `counts` payload: a cluster's counts changed (at most 1/s per cluster); replaces `counts` and `kinds`. */
export interface CountsEvent {
  cluster: string;
  counts?: Partial<Record<Status, number>>;
  kinds?: KindCounts;
  connected?: boolean;
}

/**
 * SSE `attention` payload (every cluster, watched ones too): rows that now need attention,
 * ids that left the set, and, when present, the cluster's findings (all severities).
 */
export interface AttentionEvent {
  cluster: string;
  upserts: Resource[];
  deletes: string[];
  findings?: Finding[];
}

/** One hit of GET /api/v1/search. Ranges are offsets into the name and the secondary fields. */
export interface SearchHit {
  cluster: string;
  resource: Resource;
  match: {
    score: number;
    primary: Array<[number, number]>;
    /** Namespace, kind, kind abbreviation, cluster. */
    secondary: Array<Array<[number, number]>>;
  };
  stale?: boolean;
}

export interface SearchResponse {
  items: SearchHit[];
  /** Clusters skipped because their scan was too slow or their access checks failed. */
  partial?: string[];
}

export interface AttentionItem {
  cluster: string;
  resource: Resource;
  stale?: boolean;
}

export interface AttentionFinding {
  cluster: string;
  finding: Finding;
  stale?: boolean;
}

/** GET /api/v1/attention. */
export interface AttentionResponse {
  items: AttentionItem[];
  total: number;
  findings: AttentionFinding[];
  partial?: string[];
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

/** A marker entry in a workload log stream: not a log line; `line` explains it. */
export type LogMarker = "forbidden" | "ended" | "error" | "dropped";

/** One entry of a workload log `log` event (docs/api.md, "Workload logs"). */
export interface WorkloadLogEntry {
  pod: string;
  container?: string;
  line: string;
  /** The kubelet timestamp (RFC 3339), when the line had one. */
  ts?: string;
  marker?: LogMarker;
}

export interface WorkloadPod {
  name: string;
  containers: string[];
  status: string;
  createdAt?: string;
}

/** The `pods` event: the streamed pods, newest first. total > limit means only the newest are streamed. */
export interface WorkloadPodsEvent {
  pods: WorkloadPod[];
  total: number;
  limit: number;
}
