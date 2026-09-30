// Package protocol defines the JSON frames exchanged between hub and agent
// over the agent-initiated WebSocket at /agent/v1/connect.
package protocol

import (
	"encoding/json"

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
}

// ModeLocal is the Hello.Mode of a local-mode agent.
const ModeLocal = "local"

// Snapshot replaces the hub's view of the cluster. When the full state does
// not fit in MaxFrameBytes, the agent sends the first chunk as a Snapshot and
// the rest immediately as upsert-only Deltas.
type Snapshot struct {
	Resources []model.Resource `json:"resources"`
}

// Delta carries changes since the previous snapshot or delta.
type Delta struct {
	Upserts []model.Resource `json:"upserts,omitempty"`
	Deletes []string         `json:"deletes,omitempty"` // Resource ids
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
)

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

// LogsArgs for OpLogs. Target is the Pod.
type LogsArgs struct {
	Container string `json:"container,omitempty"`
	Follow    bool   `json:"follow,omitempty"`
	TailLines int64  `json:"tailLines,omitempty"` // agent caps at 5000
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
type LogChunk struct {
	Lines []string `json:"lines"`
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
