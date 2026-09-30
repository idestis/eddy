package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// RequestedAtAnnotation asks a Flux controller to reconcile now.
const RequestedAtAnnotation = "reconcile.fluxcd.io/requestedAt"

// Request limits.
const (
	maxEvents       = 100
	maxEventMessage = 1024
	maxAccessChecks = 100
	// yamlBudget leaves room for the frame envelope around a YAML result.
	yamlBudget = protocol.MaxFrameBytes - 4096
)

// fieldManager is recorded in managedFields for the agent's patches.
const fieldManager = "eddy"

// Handler executes hub requests. Every cluster read and write goes through a
// client that impersonates the request identity; only SubjectAccessReviews
// use the agent's own client.
type Handler struct {
	Policy      Policy
	Served      flux.Served
	Impersonate ClientFactory
	// Self is the agent's own client, used only to create SubjectAccessReviews.
	Self   kubernetes.Interface
	Now    func() time.Time
	Logger *slog.Logger
}

// Handle runs one request and returns its JSON result. For OpLogs, lines are
// delivered through stream and the result is empty. The error, if any, is
// already mapped to a protocol error code.
func (h *Handler) Handle(ctx context.Context, req protocol.Request, stream func(protocol.LogChunk) error) (json.RawMessage, *protocol.Error) {
	if perr := h.Policy.Validate(req.Identity); perr != nil {
		return nil, perr
	}
	result, err := h.dispatch(ctx, req, stream)
	if err != nil {
		perr := toProtocolError(err)
		h.Logger.LogAttrs(ctx, slog.LevelInfo, "request failed",
			slog.String("op", string(req.Op)), slog.String("target", req.Target.ID()),
			slog.String("user", req.Identity.User), slog.Int("code", perr.Code), slog.String("error", perr.Message))
		return nil, perr
	}
	h.Logger.LogAttrs(ctx, slog.LevelDebug, "request done",
		slog.String("op", string(req.Op)), slog.String("target", req.Target.ID()), slog.String("user", req.Identity.User))
	if result == nil {
		return nil, nil
	}
	b, err := json.Marshal(result)
	if err != nil {
		return nil, &protocol.Error{Code: 500, Message: fmt.Sprintf("agent: encode %s result: %v", req.Op, err)}
	}
	return b, nil
}

func (h *Handler) dispatch(ctx context.Context, req protocol.Request, stream func(protocol.LogChunk) error) (any, error) {
	if req.Op == protocol.OpAccess {
		var args protocol.AccessArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		return h.access(ctx, req.Identity, args)
	}
	k, gvr, err := h.resolve(req.Target)
	if err != nil {
		return nil, err
	}
	cl, err := h.Impersonate(req.Identity)
	if err != nil {
		return nil, err
	}
	t := req.Target
	switch req.Op {
	case protocol.OpReconcile:
		var args protocol.ReconcileArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		return nil, h.reconcile(ctx, cl, k, gvr, t, args.WithSource)
	case protocol.OpSuspend, protocol.OpResume:
		if !k.Flux {
			return nil, badRequest("%s does not support %s", k.Kind, req.Op)
		}
		patch := map[string]any{"spec": map[string]any{"suspend": req.Op == protocol.OpSuspend}}
		if req.Op == protocol.OpResume {
			patch["metadata"] = requestedAtPatch(h.now())
		}
		return nil, mergePatch(ctx, cl, gvr, t, patch)
	case protocol.OpYAML:
		return h.yaml(ctx, cl, gvr, t)
	case protocol.OpEvents:
		return h.events(ctx, cl, k, t)
	case protocol.OpLogs:
		var args protocol.LogsArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		if k.Kind != flux.KindPod {
			return nil, badRequest("logs are only available for Pods")
		}
		return nil, streamLogs(ctx, cl.Kube, t, args, stream)
	}
	return nil, badRequest("unknown op %q", req.Op)
}

// resolve checks the target against the kind table and the served versions.
func (h *Handler) resolve(t model.Ref) (flux.Kind, schema.GroupVersionResource, error) {
	k, ok := flux.KindByName(t.Kind)
	if !ok || !k.Matches(t.Group, t.Kind) {
		return flux.Kind{}, schema.GroupVersionResource{}, badRequest("unsupported kind %s/%s", t.Group, t.Kind)
	}
	if t.Name == "" || (k.Namespaced && t.Namespace == "") {
		return flux.Kind{}, schema.GroupVersionResource{}, badRequest("target %s needs a namespace and a name", t.ID())
	}
	gvr, ok := h.Served.GVR(k.Kind)
	if !ok {
		return flux.Kind{}, schema.GroupVersionResource{}, &protocol.Error{Code: 404, Message: fmt.Sprintf("agent: %s is not served by this cluster", k.Kind)}
	}
	return k, gvr, nil
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func requestedAtPatch(now time.Time) map[string]any {
	return map[string]any{"annotations": map[string]any{RequestedAtAnnotation: now.UTC().Format(time.RFC3339Nano)}}
}

func mergePatch(ctx context.Context, cl Clients, gvr schema.GroupVersionResource, t model.Ref, patch map[string]any) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("agent: encode patch: %w", err)
	}
	_, err = cl.Dynamic.Resource(gvr).Namespace(t.Namespace).Patch(ctx, t.Name, types.MergePatchType, body, metav1.PatchOptions{FieldManager: fieldManager})
	if err != nil {
		return fmt.Errorf("agent: patch %s: %w", t.ID(), err)
	}
	return nil
}

// reconcile sets requestedAt on the target and, with withSource, on its
// source first so the target reconciles against a fresh artifact.
func (h *Handler) reconcile(ctx context.Context, cl Clients, k flux.Kind, gvr schema.GroupVersionResource, t model.Ref, withSource bool) error {
	if !k.Flux {
		return badRequest("%s does not support reconcile", k.Kind)
	}
	patch := map[string]any{"metadata": requestedAtPatch(h.now())}
	if withSource {
		obj, err := cl.Dynamic.Resource(gvr).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("agent: get %s: %w", t.ID(), err)
		}
		if src := reconcileSource(obj); src != nil {
			_, srcGVR, err := h.resolve(*src)
			if err != nil {
				return fmt.Errorf("agent: source of %s: %w", t.ID(), err)
			}
			if err := mergePatch(ctx, cl, srcGVR, *src, patch); err != nil {
				return err
			}
		}
	}
	return mergePatch(ctx, cl, gvr, t, patch)
}

// reconcileSource returns the object to reconcile before obj for
// "reconcile with source". A HelmRelease with an inline chart whose source is
// a HelmRepository refreshes through the HelmChart that helm-controller
// manages for it, "<namespace>-<name>" in the repository's namespace; other
// chart sources (GitRepository, Bucket, and chartRef targets) are reconciled
// directly. Source kinds themselves have no source.
func reconcileSource(obj *unstructured.Unstructured) *model.Ref {
	src := flux.SourceOf(obj)
	if src == nil {
		return nil
	}
	if obj.GetKind() == flux.KindHelmRelease && flux.HelmReleaseUsesChartTemplate(obj) &&
		src.Group == flux.GroupSource && src.Kind == flux.KindHelmRepository {
		return &model.Ref{
			Group:     flux.GroupSource,
			Kind:      flux.KindHelmChart,
			Namespace: src.Namespace,
			Name:      obj.GetNamespace() + "-" + obj.GetName(),
		}
	}
	return src
}

func (h *Handler) yaml(ctx context.Context, cl Clients, gvr schema.GroupVersionResource, t model.Ref) (any, error) {
	if !flux.YAMLAllowed(t.Kind) {
		return nil, &protocol.Error{Code: 403, Message: fmt.Sprintf("agent: yaml is not available for %s", t.Kind)}
	}
	obj, err := cl.Dynamic.Resource(gvr).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("agent: get %s: %w", t.ID(), err)
	}
	out, err := flux.SanitizeYAML(obj)
	if err != nil {
		return nil, err
	}
	if len(out) > yamlBudget {
		return nil, &protocol.Error{Code: 500, Message: fmt.Sprintf("agent: yaml of %s is larger than %d bytes", t.ID(), yamlBudget)}
	}
	return protocol.YAMLResult{YAML: string(out)}, nil
}

func (h *Handler) events(ctx context.Context, cl Clients, k flux.Kind, t model.Ref) (any, error) {
	sel := fields.Set{
		"involvedObject.name":      t.Name,
		"involvedObject.namespace": t.Namespace,
		"involvedObject.kind":      k.Kind,
	}.AsSelector().String()
	list, err := cl.Kube.CoreV1().Events(t.Namespace).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("agent: list events of %s: %w", t.ID(), err)
	}
	out := make([]model.Event, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, trimEvent(&list.Items[i]))
	}
	slices.SortStableFunc(out, func(a, b model.Event) int { return b.Last.Compare(a.Last) })
	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return protocol.EventsResult{Events: out}, nil
}

// trimEvent keeps the fields the UI shows, handling both the legacy
// (firstTimestamp/count) and the events.k8s.io (eventTime/series) shapes.
func trimEvent(e *corev1.Event) model.Event {
	first := e.FirstTimestamp.Time
	if first.IsZero() {
		first = e.EventTime.Time
	}
	last, count := e.LastTimestamp.Time, e.Count
	if e.Series != nil {
		last, count = e.Series.LastObservedTime.Time, e.Series.Count
	}
	if last.IsZero() {
		last = first
	}
	source := e.ReportingController
	if source == "" {
		source = e.Source.Component
	}
	return model.Event{
		Type:    e.Type,
		Reason:  e.Reason,
		Message: flux.OneLine(e.Message, maxEventMessage),
		Count:   max(count, 1),
		Source:  source,
		First:   first.UTC(),
		Last:    last.UTC(),
	}
}

// access answers SubjectAccessReviews for the identity. The reviews are
// created by the agent's own ServiceAccount, never impersonated: a user cannot
// normally create SubjectAccessReviews about themselves.
func (h *Handler) access(ctx context.Context, id protocol.Identity, args protocol.AccessArgs) (any, error) {
	if len(args.Checks) > maxAccessChecks {
		return nil, badRequest("at most %d access checks per request", maxAccessChecks)
	}
	allowed := make([]bool, len(args.Checks))
	errs := make([]error, len(args.Checks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, c := range args.Checks {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
				User:   id.User,
				Groups: slices.Clone(id.Groups),
				ResourceAttributes: &authorizationv1.ResourceAttributes{
					Verb: c.Verb, Group: c.Group, Resource: c.Resource, Subresource: c.Subresource,
					Namespace: c.Namespace, Name: c.Name,
				},
			}}
			res, err := h.Self.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
			if err != nil {
				errs[i] = fmt.Errorf("agent: subject access review %s %s/%s: %w", c.Verb, c.Group, c.Resource, err)
				return
			}
			allowed[i] = res.Status.Allowed && !res.Status.Denied
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return protocol.AccessResult{Allowed: allowed}, nil
}

func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return badRequest("invalid args: %v", err)
	}
	return nil
}

func badRequest(format string, args ...any) *protocol.Error {
	return &protocol.Error{Code: 400, Message: "agent: " + fmt.Sprintf(format, args...)}
}

// toProtocolError maps an error to the HTTP-like codes of the protocol.
// Kubernetes API errors keep their message, which names the impersonated
// user, so the UI can show why RBAC refused.
func toProtocolError(err error) *protocol.Error {
	if pe, ok := errors.AsType[*protocol.Error](err); ok {
		return pe
	}
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		s := st.Status()
		code := 500
		switch {
		case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
			code = 403
		case apierrors.IsNotFound(err):
			code = 404
		case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
			code = 409
		case apierrors.IsBadRequest(err), apierrors.IsInvalid(err), apierrors.IsMethodNotSupported(err):
			code = 400
		case apierrors.IsServiceUnavailable(err), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err), apierrors.IsTooManyRequests(err):
			code = 503
		}
		return &protocol.Error{Code: code, Message: s.Message}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &protocol.Error{Code: 503, Message: "agent: request timed out"}
	}
	if errors.Is(err, flux.ErrKindNotAllowed) {
		return &protocol.Error{Code: 403, Message: err.Error()}
	}
	return &protocol.Error{Code: 500, Message: err.Error()}
}
