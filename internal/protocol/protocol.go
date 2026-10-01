// Package protocol defines the JSON frames exchanged between hub and agent
// over the agent-initiated WebSocket at /agent/v1/connect.
package protocol

import (
	"encoding/json"
	"time"

	"github.com/idestis/eddy/internal/model"
)

// Version is bumped on incompatible frame changes. The hub rejects agents
// with a different major version in Hello.
const Version = "1"

// MaxFrameBytes caps any single frame in either direction.
const MaxFrameBytes = 1 << 20

// FrameType enumerates frame kinds.
type FrameType string

const (
	TypeHello     FrameType = "hello"     // agent → hub, first frame
	TypeSnapshot  FrameType = "snapshot"  // agent → hub, full state after hello
	TypeDelta     FrameType = "delta"     // agent → hub, batched changes (≈250 ms)
	TypeRequest   FrameType = "request"   // hub → agent
	TypeResponse  FrameType = "response"  // agent → hub, terminal reply to a request
	TypeStream    FrameType = "stream"    // agent → hub, one chunk of a streamed reply
	TypeStreamEnd FrameType = "streamEnd" // agent → hub, stream finished (payload may hold an Error)
	TypeCancel    FrameType = "cancel"    // hub → agent, stop a streamed request
	TypePing      FrameType = "ping"      // either way, application keepalive
	// TypeCredentials is hub → agent on a join connection (ADR-0005): the
	// payload is Credentials, and the agent answers with a TypeResponse of
	// the same ID whose Result is CredentialsResult. The hub then closes
	// the connection and the agent reconnects with the new token. Agents
	// that do not know the frame ignore it.
	TypeCredentials FrameType = "credentials"
)

// Frame is the envelope of every message. ID correlates request, response,
// stream and cancel frames; it is empty for hello, snapshot and delta.
type Frame struct {
	Type    FrameType       `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Hello is sent by the agent right after the WebSocket upgrade. The cluster
// name is also carried in the URL (?cluster=) and must match.
type Hello struct {
	Protocol          string   `json:"protocol"`
	Cluster           string   `json:"cluster"`
	AgentVersion      string   `json:"agentVersion"`
	KubernetesVersion string   `json:"kubernetesVersion"`
	FluxVersion       string   `json:"fluxVersion,omitempty"`
	Namespaces        []string `json:"namespaces,omitempty"` // empty = whole cluster
	// Mode is "local" for an agent in local mode (dev builds only): it runs on
	// a developer machine with the kubeconfig's own identity instead of
	// impersonating the user. Empty for a normal in-cluster agent.
	Mode string `json:"mode,omitempty"`
	// ReadOnly is set when the agent refuses every write (local mode without
	// --allow-writes, or a --protect match). The hub then refuses writes too.
	ReadOnly bool `json:"readOnly,omitempty"`
	// Context is the kubeconfig context a local-mode agent serves.
	Context string `json:"context,omitempty"`
	// Instance is a random id chosen once per agent process. Several
	// instances of one cluster (agent replicas) connect side by side; the
	// hub uses the oldest healthy one and keeps the others as standbys.
	Instance string `json:"instance,omitempty"`
	// Seq counts the process's dials, starting at 1. A connection from the
	// same Instance with a higher Seq replaces the older one on every hub
	// replica. Agents that send no Instance are treated as one instance
	// whose newest connection wins.
	Seq int64 `json:"seq,omitempty"`
	// Diagnostics are the agent's self-checks for the hub's connection
	// checklist (ADR-0005). Absent from older agents.
	Diagnostics *Diagnostics `json:"diagnostics,omitempty"`
	// Presets are the enabled watch presets (EDDY_WATCH_PRESETS), whether
	// or not the cluster serves their kinds. Absent from older agents.
	Presets []string `json:"presets,omitempty"`
	// Kinds lists the surfaced kinds the agent watches, as "<group>/<Kind>"
	// (core kinds as "/Pod"). The hub reports them as watched on the kinds
	// endpoint. Absent from older agents.
	Kinds []string `json:"kinds,omitempty"`
}

// Diagnostics is what an agent found out about itself before connecting.
type Diagnostics struct {
	// ServedKinds lists the kinds from the kind table the cluster serves.
	ServedKinds []string `json:"servedKinds,omitempty"`
	// SAROK reports whether the agent may create SubjectAccessReviews;
	// SARError explains a failure.
	SAROK    bool   `json:"sarOK"`
	SARError string `json:"sarError,omitempty"`
	// ImpersonationPinned is false when the agent may impersonate the
	// group system:masters (a SelfSubjectAccessReview said yes), which
	// means impersonation.groups is not pinned. Nil when the check failed.
	ImpersonationPinned *bool `json:"impersonationPinned,omitempty"`
	// InformersSynced of InformersTotal informers have listed once.
	InformersSynced int `json:"informersSynced"`
	InformersTotal  int `json:"informersTotal"`
	// CredentialsError is set when the agent could not store the token it
	// received in a credentials frame in its token Secret; it then uses
	// the token from memory until it restarts.
	CredentialsError string `json:"credentialsError,omitempty"`
}

// ModeLocal is the Hello.Mode of a local-mode agent.
const ModeLocal = "local"

// Snapshot replaces the hub's view of the cluster. When the full state does
// not fit in MaxFrameBytes, the agent sends the first chunk as a Snapshot and
// the rest immediately as upsert-only Deltas.
type Snapshot struct {
	Resources []model.Resource `json:"resources"`
	// Findings, when set, replaces the cluster's findings (absent from
	// older agents, which have none).
	Findings *FindingSet `json:"findings,omitempty"`
	// Parts is the number of frames of this snapshot: this Snapshot is part
	// 1 and parts 2..Parts follow at once as Deltas with Delta.Part set.
	// The hub swaps the snapshot in, and calls the view synced, only after
	// the last part. 0 (older agents) means the parts are not marked; the
	// hub then ends the snapshot after a short quiet period.
	Parts int `json:"parts,omitempty"`
}

// Delta carries changes since the previous snapshot or delta.
type Delta struct {
	Upserts []model.Resource `json:"upserts,omitempty"`
	Deletes []string         `json:"deletes,omitempty"` // Resource ids
	// Findings, when set, replaces the cluster's findings.
	Findings *FindingSet `json:"findings,omitempty"`
	// Part is set on the continuation frames of a chunked snapshot: the
	// part number, 2..Snapshot.Parts. Such a Delta carries upserts only.
	Part int `json:"part,omitempty"`
}

// FindingSet is the complete set of a cluster's findings.
type FindingSet struct {
	Items []model.Finding `json:"items"`
}

// Identity is the user an agent impersonates. The agent re-validates it:
// no system: user or group, every group must match an allowed prefix.
type Identity struct {
	User   string   `json:"user"`
	Groups []string `json:"groups"`
}

// Op names a request operation.
type Op string

const (
	OpReconcile Op = "reconcile"
	OpSuspend   Op = "suspend"
	OpResume    Op = "resume"
	OpYAML      Op = "yaml"
	OpEvents    Op = "events"
	OpLogs      Op = "logs"
	OpAccess    Op = "access"
	// OpHiddenJobs lists the finished Jobs the agent's Job policy does not
	// surface. It is answered from the agent's cache, like OpAccess without
	// impersonation: the hub restricts Namespaces to those where the caller
	// may list Jobs (SAR) and filters the result again.
	OpHiddenJobs Op = "hiddenJobs"
	// OpRules lists the resource rules of the request identity, one
	// SelfSubjectRulesReview per namespace, created by the agent as the
	// impersonated identity (so the review evaluates the user's own
	// permissions, never the agent's). The hub answers namespaced read
	// checks from the rules when they are complete. Agents that do not know
	// the op answer 400; the hub then asks SubjectAccessReviews as before.
	OpRules Op = "rules"
)

// Rules limits: namespaces per OpRules request, and resource rules per
// namespace. A namespace with more rules is sent with Truncated set and no
// rules.
const (
	MaxRulesNamespaces   = 50
	MaxRulesPerNamespace = 2000
)

// RulesArgs for OpRules: distinct, non-empty namespace names.
type RulesArgs struct {
	Namespaces []string `json:"namespaces"`
}

// ResourceRule is one rule of a SelfSubjectRulesReview, with the RBAC
// meaning of its fields ("*" matches every verb, group or resource,
// "*/<sub>" every resource's subresource, and ResourceNames, when set,
// limits the rule to requests for those names).
type ResourceRule struct {
	Verbs         []string `json:"verbs"`
	APIGroups     []string `json:"apiGroups,omitempty"`
	Resources     []string `json:"resources,omitempty"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

// NamespaceRules is the rules review of one namespace. The hub uses Rules
// only when Incomplete, EvaluationError, Truncated and Error are all
// unset; otherwise it asks SubjectAccessReviews for the namespace.
type NamespaceRules struct {
	Namespace string         `json:"namespace"`
	Rules     []ResourceRule `json:"rules,omitempty"`
	// Incomplete and EvaluationError are the review's status fields.
	Incomplete      bool   `json:"incomplete,omitempty"`
	EvaluationError string `json:"evaluationError,omitempty"`
	// Truncated is set when the rules did not fit (MaxRulesPerNamespace,
	// or the frame budget); Rules is then empty.
	Truncated bool `json:"truncated,omitempty"`
	// Error is set when the review could not be created.
	Error string `json:"error,omitempty"`
}

// RulesResult answers RulesArgs, one entry per namespace, in order.
type RulesResult struct {
	Namespaces []NamespaceRules `json:"namespaces"`
}

// HiddenJobsArgs for OpHiddenJobs. Jobs are ordered by namespace, then
// newest finished first.
type HiddenJobsArgs struct {
	Namespaces []string `json:"namespaces"`
	Offset     int      `json:"offset,omitempty"`
	Limit      int      `json:"limit,omitempty"` // agent default 500, cap 1000
}

// HiddenJobsResult answers HiddenJobsArgs. Next is the Offset of the next
// page, 0 when there is none. Items are cut short to fit a frame.
type HiddenJobsResult struct {
	Items []model.Resource `json:"items"`
	Total int              `json:"total"`
	Next  int              `json:"next,omitempty"`
}

// Request is the payload of a TypeRequest frame.
type Request struct {
	Op       Op              `json:"op"`
	Identity Identity        `json:"identity"`
	Target   model.Ref       `json:"target,omitzero"`
	Args     json.RawMessage `json:"args,omitempty"`
}

// ReconcileArgs for OpReconcile.
type ReconcileArgs struct {
	WithSource bool `json:"withSource,omitempty"`
}

// LogsArgs for OpLogs. Target is a Pod, or a workload: a Deployment,
// StatefulSet, DaemonSet, ReplicaSet or Job. For a workload the agent finds
// its current pods in its cache, streams every pod × container as
// LogChunk.Entries and, with Follow, picks up new pods and ends the streams
// of deleted ones. A Pod target streams LogChunk.Lines, as before.
type LogsArgs struct {
	Container string `json:"container,omitempty"`
	Follow    bool   `json:"follow,omitempty"`
	// TailLines is per pod (and container). A Pod target defaults to 1000
	// and caps at 5000; a workload target defaults to 100 and caps at 1000.
	TailLines int64 `json:"tailLines,omitempty"`
	// SinceSeconds, when set, starts each stream that many seconds back.
	SinceSeconds int64 `json:"sinceSeconds,omitempty"`
	// Pods limits a workload stream to these pod names (workload targets
	// only; names that are not pods of the workload are ignored).
	Pods []string `json:"pods,omitempty"`
	// AllContainers streams every container of each pod when Container is
	// empty (workload targets only). Nil means true.
	AllContainers *bool `json:"allContainers,omitempty"`
}

// AccessCheck is one SubjectAccessReview question.
type AccessCheck struct {
	Verb        string `json:"verb"`
	Group       string `json:"group"`
	Resource    string `json:"resource"` // plural, e.g. "kustomizations"
	Subresource string `json:"subresource,omitempty"`
	Namespace   string `json:"namespace,omitempty"`
	Name        string `json:"name,omitempty"`
}

// AccessArgs for OpAccess.
type AccessArgs struct {
	Checks []AccessCheck `json:"checks"`
}

// AccessResult answers AccessArgs, in order.
type AccessResult struct {
	Allowed []bool `json:"allowed"`
}

// YAMLResult for OpYAML. The agent strips managedFields, Secret data and
// last-applied-configuration before sending.
type YAMLResult struct {
	YAML string `json:"yaml"`
}

// EventsResult for OpEvents.
type EventsResult struct {
	Events []model.Event `json:"events"`
}

// LogChunk is the payload of a TypeStream frame for OpLogs. Logs always
// stream (even without Follow): zero or more Stream frames, then exactly one
// StreamEnd whose payload is a Response that may carry an Error. Every other
// op gets exactly one Response; writes return an empty Result.
//
// A Pod target sends Lines. A workload target sends Entries, and Pods once
// at the start and again whenever the set of streamed pods changes; a chunk
// may carry both.
type LogChunk struct {
	Lines   []string   `json:"lines,omitempty"`
	Entries []LogEntry `json:"entries,omitempty"`
	Pods    *LogPods   `json:"pods,omitempty"`
}

// LogEntry is one line of a workload log stream, or a marker about it.
type LogEntry struct {
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
	Line      string `json:"line"`
	// TS is the kubelet timestamp of the line, when it had one.
	TS time.Time `json:"ts,omitzero"`
	// Marker is set on entries that are not log lines; Line then explains.
	Marker LogMarker `json:"marker,omitempty"`
}

// LogMarker names a non-line LogEntry.
type LogMarker string

const (
	// MarkerForbidden: the user may not read this pod's logs; the pod is
	// skipped (sent once per pod).
	MarkerForbidden LogMarker = "forbidden"
	// MarkerEnded: the stream of this pod (or container) ended because the
	// pod was deleted or left the streamed set.
	MarkerEnded LogMarker = "ended"
	// MarkerError: a stream failed; Line holds the reason.
	MarkerError LogMarker = "error"
	// MarkerDropped: lines were dropped by the stream's rate limit; Pod is
	// empty and Line says how many.
	MarkerDropped LogMarker = "dropped"
)

// LogPods is the set of pods a workload log stream follows.
type LogPods struct {
	Pods []LogPod `json:"pods"`
	// Total counts the workload's matching pods; when it exceeds Limit only
	// the newest Limit pods are streamed.
	Total int `json:"total"`
	Limit int `json:"limit"`
}

// LogPod is one streamed pod.
type LogPod struct {
	Name       string       `json:"name"`
	Containers []string     `json:"containers"`
	Status     model.Status `json:"status"`
	CreatedAt  time.Time    `json:"createdAt,omitzero"`
}

// Response is the payload of TypeResponse and, optionally, TypeStreamEnd.
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error codes mirror HTTP semantics so the hub can map them directly.
type Error struct {
	Code    int    `json:"code"` // 400, 403, 404, 409, 500, 503
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Credentials is the payload of a TypeCredentials frame: the permanent
// agent token that replaces the join token.
type Credentials struct {
	Token string `json:"token"`
}

// CredentialsResult answers Credentials. Stored is false when the agent
// could not write its token Secret; Error says why.
type CredentialsResult struct {
	Stored bool   `json:"stored"`
	Error  string `json:"error,omitempty"`
}

// ---- hub peer channel (peer/v1, ADR-0004) ----

// PeerVersion is the version in the peer endpoint path /peer/v<PeerVersion>/connect.
// Changes within one version are additive only; a peer ignores frame types
// it does not know.
const PeerVersion = "1"

// Frame types used only between hub replicas. Every other frame on the peer
// channel is one of the agent frame types above, relayed unchanged.
const (
	// TypeSubscribe asks the peer that holds an agent session for Cluster to
	// stream that cluster's view: a Hello, a Snapshot (chunked like an
	// agent's), then Deltas.
	TypeSubscribe FrameType = "subscribe"
	// TypeUnsubscribe stops a subscription. Sent by the subscriber it means
	// "stop sending"; sent by the owner with ID PeerEnded it means the owner
	// no longer holds an agent session for Cluster.
	TypeUnsubscribe FrameType = "unsubscribe"
)

// PeerEnded is the Frame.ID of an owner-initiated TypeUnsubscribe.
const PeerEnded = "ended"

// PeerFrame is the envelope of every message on the peer channel. Cluster
// names the cluster Frame is about; it is empty for link-level pings.
// Request frames carry the original caller's Identity, which the owner and
// then the agent validate again.
type PeerFrame struct {
	Cluster string `json:"cluster,omitempty"`
	Frame   Frame  `json:"frame"`
}
