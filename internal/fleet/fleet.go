// Package fleet is the hub's user-scoped view of every connected cluster.
// HTTP handlers, the MCP server and Ask AI all go through Service, so RBAC
// filtering, impersonation, protected-cluster checks and audit happen in one
// place. Implementations live in internal/hub.
package fleet

import (
	"context"
	"errors"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/model"
)

var (
	ErrNotFound        = errors.New("fleet: not found")
	ErrForbidden       = errors.New("fleet: forbidden")
	ErrDisconnected    = errors.New("fleet: cluster not connected")
	ErrConfirmRequired = errors.New("fleet: protected cluster requires confirmation")
	ErrDisabled        = errors.New("fleet: disabled by runtime flag")
)

// Filter narrows List. Zero fields match everything.
type Filter struct {
	Kinds     []string     // e.g. ["Kustomization","HelmRelease"]
	Namespace string       //
	Status    model.Status //
	Query     string       // case-insensitive substring over kind/namespace/name/message
	Limit     int          // 0 = no limit
}

// ActionOptions accompany writes.
type ActionOptions struct {
	WithSource bool   // reconcile the source first
	Confirm    string // must equal the cluster name on protected clusters
}

// LogOptions for pod logs.
type LogOptions struct {
	Container string
	TailLines int64
	Follow    bool
}

// Service is implemented by the hub. Every method takes the caller's
// principal explicitly; implementations must never fall back to a shared
// identity.
type Service interface {
	Clusters(ctx context.Context, p identity.Principal) ([]model.ClusterInfo, error)
	List(ctx context.Context, p identity.Principal, cluster string, f Filter) ([]model.Resource, error)
	Get(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (model.Resource, error)
	// Children returns resources owned by ref (inventory, labels, ownerReferences).
	Children(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) ([]model.Resource, error)
	YAML(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (string, error)
	Events(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) ([]model.Event, error)
	// Logs streams lines to w until ctx ends (Follow) or the tail is sent.
	Logs(ctx context.Context, p identity.Principal, cluster string, pod model.Ref, o LogOptions, w LineWriter) error

	Reconcile(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o ActionOptions) error
	Suspend(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o ActionOptions) error
	Resume(ctx context.Context, p identity.Principal, cluster string, ref model.Ref, o ActionOptions) error

	// CanGet reports whether p may get ref (SAR, cached). Used to filter threads.
	CanGet(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (bool, error)
	// CanPatch reports whether p may patch ref (SAR, cached). Used to resolve threads.
	CanPatch(ctx context.Context, p identity.Principal, cluster string, ref model.Ref) (bool, error)
}

// LineWriter receives log lines.
type LineWriter interface {
	WriteLines(lines []string) error
}

// LineWriterFunc adapts a function to LineWriter.
type LineWriterFunc func([]string) error

func (f LineWriterFunc) WriteLines(l []string) error { return f(l) }
