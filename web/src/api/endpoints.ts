// One function per hub endpoint (docs/api.md). Components use them through
// the query options in queries.ts or through mutations.

import { request, seg } from "./client";
import type {
  AskRequest,
  AskResponse,
  AttentionResponse,
  AuditEvent,
  Chat,
  ChatDetail,
  ClusterInfo,
  ClusterInput,
  ClusterPermissions,
  ConnectionInfo,
  CreatedCluster,
  CreatedToken,
  Finding,
  GraphResponse,
  IndexPage,
  IssuedJoinToken,
  JobsSnapshot,
  KindsResponse,
  KubeEvent,
  List,
  Me,
  Message,
  OnboardedCluster,
  Page,
  Providers,
  Ref,
  Resource,
  ResourceRef,
  ResourceSnapshot,
  SearchResponse,
  Thread,
  ThreadDetail,
  ThreadStatus,
  TokenItem,
  TokenScope,
} from "./types";

const V1 = "/api/v1";

export const objectPath = (cluster: string, r: Pick<Ref, "kind" | "namespace" | "name">): string =>
  `${V1}/clusters/${seg(cluster)}/objects/${seg(r.kind)}/${seg(r.namespace)}/${seg(r.name)}`;

export const streamUrl = `${V1}/stream`;

/**
 * The stream URL for a watch set (ADR-0006): full deltas for these clusters only, `counts`
 * and `attention` for the rest. An empty set still sends `watch=` so a new hub streams no
 * deltas at all (the fleet page); an older hub ignores the parameter and streams everything.
 */
export const streamUrlFor = (watch: readonly string[]): string =>
  `${streamUrl}?watch=${watch.map(encodeURIComponent).join(",")}`;

export const logsUrl = (cluster: string, namespace: string, pod: string): string =>
  `${V1}/clusters/${seg(cluster)}/pods/${seg(namespace)}/${seg(pod)}/logs`;

/** SSE logs of every pod of a Deployment, StatefulSet, DaemonSet or Job. */
export const workloadLogsUrl = (cluster: string, r: Pick<Ref, "kind" | "namespace" | "name">): string =>
  `${V1}/clusters/${seg(cluster)}/workloads/${seg(r.kind)}/${seg(r.namespace)}/${seg(r.name)}/logs`;

// Session

export const getCsrf = () => request<{ csrf: string }>("/auth/csrf");
export const getProviders = () => request<Providers>("/auth/providers");
export const login = (body: { username: string; password: string; returnTo?: string }, csrf: string) =>
  request<void>("/auth/local/login", { method: "POST", body, csrf });
export const logout = () => request<void>("/auth/logout", { method: "POST" });
export const getMe = () => request<Me>(`${V1}/me`);

// Preferences

export type Prefs = Record<string, unknown>;

export const getPrefs = () => request<{ data: Prefs }>(`${V1}/prefs`);
export const putPrefs = (data: Prefs) =>
  request<{ data: Prefs }>(`${V1}/prefs`, { method: "PUT", body: { data } });

// Clusters and resources

export const getClusters = () => request<List<ClusterInfo>>(`${V1}/clusters`);
export const getResources = (cluster: string, signal?: AbortSignal) =>
  request<ResourceSnapshot>(`${V1}/clusters/${seg(cluster)}/resources`, { signal });
export interface IndexQuery {
  kind?: string;
  status?: string;
  namespace?: string;
  q?: string;
  sort?: "kind" | "status" | "name" | "age";
  order?: "asc" | "desc";
  offset?: number;
  cursor?: string;
  limit?: number;
}
/**
 * One page of a cluster's list (`view=index`, ADR-0006). A hub that does not page ignores the
 * parameters and answers the whole snapshot, which has no `total`.
 */
export const getIndexPage = (cluster: string, q: IndexQuery, signal?: AbortSignal) =>
  request<IndexPage | ResourceSnapshot>(`${V1}/clusters/${seg(cluster)}/resources`, {
    query: { view: "index", ...q },
    signal,
  });
export interface GraphQuery {
  kinds: "flux" | "all";
  focus?: string;
  hops?: number;
  /** Comma-separated group ids returned as their members (at most 20). */
  expand?: string;
}
/** The dependency graph of a cluster (docs/api.md "Graph"). */
export const getGraph = (cluster: string, q: GraphQuery, signal?: AbortSignal) =>
  request<GraphResponse>(`${V1}/clusters/${seg(cluster)}/graph`, { query: { ...q }, signal });
/** Jobs including the hidden finished ones; pass `cursor` (hidden.next) for further pages. */
export const getJobsWithHidden = (
  cluster: string,
  q: { namespace?: string; cursor?: string; limit?: number },
  signal?: AbortSignal,
) =>
  request<JobsSnapshot>(`${V1}/clusters/${seg(cluster)}/resources`, {
    query: { kind: "Job", includeHidden: 1, ...q },
    signal,
  });
export interface SearchQuery {
  q: string;
  scope: "fleet" | "cluster";
  /** With scope "cluster" the cluster to search; with "fleet" the current one, for the tiebreak. */
  cluster?: string;
  kind?: string;
  limit?: number;
}
/** Server-side palette search (docs/api.md "Search and attention"). */
export const search = (q: SearchQuery, signal?: AbortSignal) =>
  request<SearchResponse>(`${V1}/search`, { query: { ...q }, signal });
/** Rows that need attention, across the fleet when `cluster` is empty. */
export const getAttention = (cluster: string | undefined, signal?: AbortSignal) =>
  request<AttentionResponse>(`${V1}/attention`, { query: { cluster: cluster || undefined }, signal });
export const getFindings = (cluster: string) =>
  request<List<Finding>>(`${V1}/clusters/${seg(cluster)}/findings`);
export const getKinds = (cluster: string, signal?: AbortSignal) =>
  request<KindsResponse>(`${V1}/clusters/${seg(cluster)}/kinds`, { signal });

/**
 * The `group` query of an object read. It is always sent: the same kind name can live in
 * several API groups, and the hub answers 400 when it is ambiguous. `core` is the core group.
 */
export const groupQuery = (r: Pick<Ref, "group">): { group: string } => ({ group: r.group || "core" });

export const getObject = (cluster: string, r: Ref) =>
  request<Resource>(objectPath(cluster, r), { query: groupQuery(r) });
export const getYaml = (cluster: string, r: Ref) =>
  request<{ yaml: string }>(`${objectPath(cluster, r)}/yaml`, { query: groupQuery(r) });
export const getEvents = (cluster: string, r: Ref) =>
  request<List<KubeEvent>>(`${objectPath(cluster, r)}/events`, { query: groupQuery(r) });

// Cluster onboarding (ADR-0005)

export const getClusterPermissions = () => request<ClusterPermissions>(`${V1}/clusters/permissions`);
export const createCluster = (body: ClusterInput) =>
  request<CreatedCluster>(`${V1}/clusters`, { method: "POST", body });
export const updateCluster = (name: string, body: Partial<ClusterInput> & { confirm?: string }) =>
  request<{ cluster: OnboardedCluster }>(`${V1}/clusters/${seg(name)}`, { method: "PATCH", body });
export const createJoinToken = (name: string, ttl?: string) =>
  request<IssuedJoinToken>(`${V1}/clusters/${seg(name)}/join-token`, { method: "POST", body: { ttl } });
export const deleteCluster = (name: string, confirm?: string) =>
  request<void>(`${V1}/clusters/${seg(name)}`, { method: "DELETE", body: confirm ? { confirm } : {} });
export const getConnection = (name: string) =>
  request<ConnectionInfo>(`${V1}/clusters/${seg(name)}/connection`);

export type Action = "reconcile" | "suspend" | "resume";

export interface ActionBody {
  withSource?: boolean;
  confirm?: string;
}

export const postAction = (cluster: string, r: Ref, action: Action, body: ActionBody) =>
  request<void>(`${objectPath(cluster, r)}/${action}`, { method: "POST", body });

// Threads

export interface ThreadQuery {
  cluster?: string;
  /** API group of `kind` ("core" for the core group), so kinds outside the table resolve. */
  group?: string;
  kind?: string;
  namespace?: string;
  name?: string;
  status?: ThreadStatus;
  type?: "discussion";
  cursor?: string;
  limit?: number;
}

export const listThreads = (q: ThreadQuery) => request<Page<Thread>>(`${V1}/threads`, { query: { ...q } });
export const getThread = (id: string) => request<ThreadDetail>(`${V1}/threads/${seg(id)}`);
export const createThread = (body: { ref: ResourceRef; title: string; body: string }) =>
  request<{ thread: Thread; message: Message }>(`${V1}/threads`, {
    method: "POST",
    body: { ...body, type: "discussion" },
  });
export const replyThread = (id: string, body: string) =>
  request<Message>(`${V1}/threads/${seg(id)}/messages`, { method: "POST", body: { body } });
export const setThreadStatus = (id: string, status: ThreadStatus) =>
  request<Thread>(`${V1}/threads/${seg(id)}/${status === "resolved" ? "resolve" : "reopen"}`, {
    method: "POST",
  });

// Ask AI

export const listChats = (q: { cursor?: string; limit?: number } = {}) =>
  request<Page<Chat>>(`${V1}/ai/chats`, { query: { ...q } });
export const createChat = (body: { context?: ResourceRef[] }) =>
  request<Chat>(`${V1}/ai/chats`, { method: "POST", body });
export const getChat = (id: string, q: { cursor?: string; limit?: number } = {}) =>
  request<ChatDetail>(`${V1}/ai/chats/${seg(id)}`, { query: { ...q } });
export const updateChat = (id: string, body: { title?: string; context?: ResourceRef[] }) =>
  request<Chat>(`${V1}/ai/chats/${seg(id)}`, { method: "PATCH", body });
export const deleteChat = (id: string) => request<void>(`${V1}/ai/chats/${seg(id)}`, { method: "DELETE" });
export const askAI = (body: AskRequest) => request<AskResponse>(`${V1}/ai/ask`, { method: "POST", body });

// Tokens

export const listTokens = () => request<List<TokenItem>>(`${V1}/tokens`);
export const createToken = (body: { name: string; scopes: TokenScope[]; ttl: string }) =>
  request<CreatedToken>(`${V1}/tokens`, { method: "POST", body });
export const revokeToken = (id: string) => request<void>(`${V1}/tokens/${seg(id)}`, { method: "DELETE" });

// Audit

export const listAudit = (cursor?: string) =>
  request<Page<AuditEvent>>(`${V1}/audit`, { query: { cursor, limit: 50 } });
