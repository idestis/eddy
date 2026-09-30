// One function per hub endpoint (docs/api.md). Components use them through
// the query options in queries.ts or through mutations.

import { request, seg } from "./client";
import type {
  AskResponse,
  AuditEvent,
  ClusterInfo,
  ClusterInput,
  ClusterPermissions,
  ConnectionInfo,
  CreatedCluster,
  CreatedToken,
  IssuedJoinToken,
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

export const logsUrl = (cluster: string, namespace: string, pod: string): string =>
  `${V1}/clusters/${seg(cluster)}/pods/${seg(namespace)}/${seg(pod)}/logs`;

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
export const getObject = (cluster: string, r: Ref) => request<Resource>(objectPath(cluster, r));
export const getYaml = (cluster: string, r: Ref) =>
  request<{ yaml: string }>(`${objectPath(cluster, r)}/yaml`);
export const getEvents = (cluster: string, r: Ref) =>
  request<List<KubeEvent>>(`${objectPath(cluster, r)}/events`);

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
  kind?: string;
  namespace?: string;
  name?: string;
  status?: ThreadStatus;
  type?: "discussion" | "ask";
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

export const askAI = (body: { cluster: string; resourceId?: string; threadId?: string; question: string }) =>
  request<AskResponse>(`${V1}/ai/ask`, { method: "POST", body });

// Tokens

export const listTokens = () => request<List<TokenItem>>(`${V1}/tokens`);
export const createToken = (body: { name: string; scopes: TokenScope[]; ttl: string }) =>
  request<CreatedToken>(`${V1}/tokens`, { method: "POST", body });
export const revokeToken = (id: string) => request<void>(`${V1}/tokens/${seg(id)}`, { method: "DELETE" });

// Audit

export const listAudit = (cursor?: string) =>
  request<Page<AuditEvent>>(`${V1}/audit`, { query: { cursor, limit: 50 } });
