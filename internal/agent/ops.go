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
	"k8s.io/apimachinery/pkg/api/meta"
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

// JobSource lists the Jobs the cache's Job policy hides.
type JobSource interface {
	HiddenJobs(namespaces []string, offset, limit, budget int) protocol.HiddenJobsResult
}

var _ JobSource = (*Cache)(nil)

// Request limits.
const (
	maxHiddenJobNamespaces = 1000
	maxEvents              = 100
	maxEventMessage        = 1024
	maxAccessChecks        = 100
	// yamlBudget leaves room for the frame envelope around a YAML result.
	yamlBudget = protocol.MaxFrameBytes - 4096
)

// fieldManager is recorded in managedFields for the agent's patches.
const fieldManager = "eddy"

// Handler executes hub requests. Every cluster read and write goes through a
// client that impersonates the request identity (rules reviews included);
// only SubjectAccessReviews use the agent's own client.
//
// Local mode (dev builds only, see local_dev.go) swaps Impersonate and review
// for the kubeconfig's own identity and sets readOnly; release binaries never
// set those unexported fields.
type Handler struct {
	Policy Policy
	Served flux.Served
	// Kinds resolves kinds outside the watched table (inventory-only
	// objects) for yaml and events. Nil limits those reads to served table
	// kinds.
	Kinds       KindResolver
	Impersonate ClientFactory
	// Self is the agent's own client, used only to create SubjectAccessReviews.
	Self   kubernetes.Interface
	Now    func() time.Time
	Logger *slog.Logger

	// review answers one access check; nil creates a SubjectAccessReview for
	// the identity through Self.
	review accessReviewer
	// rulesReview creates one rules review as the impersonated identity;
	// nil uses SelfSubjectRulesReview. Tests replace it.
	rulesReview rulesReviewer
	// readOnly, when set, refuses every write op with a 403 carrying it.
	readOnly string

	// Pods resolves workloads to their pods for workload logs (the cache).
	// MaxLogPods caps the pods of one workload log stream and LogLineRate
	// its lines per second (defaults 20 and 2000).
	Pods PodSource
	// Jobs serves OpHiddenJobs from the cache (nil answers 503).
	Jobs        JobSource
	MaxLogPods  int
	LogLineRate int
	// openLogs opens one container log stream; nil uses the impersonated
	// clientset. Tests replace it.
	openLogs logOpener

	// MaxSAR caps SubjectAccessReviews in flight across all requests
	// (default 8).
	MaxSAR  int
	sarOnce sync.Once
	sarSem  chan struct{}
}

// sarSlots returns the agent-wide SubjectAccessReview semaphore.
func (h *Handler) sarSlots() chan struct{} {
	h.sarOnce.Do(func() {
		n := h.MaxSAR
		if n <= 0 {
			n = 8
		}
		h.sarSem = make(chan struct{}, n)
	})
	return h.sarSem
}

// accessReviewer answers one access check for id.
type accessReviewer func(ctx context.Context, id protocol.Identity, c protocol.AccessCheck) (bool, error)

// isWrite reports whether op changes cluster state.
func isWrite(op protocol.Op) bool {
	return op == protocol.OpReconcile || op == protocol.OpSuspend || op == protocol.OpResume
}

// Handle runs one request and returns its JSON result. For OpLogs, lines are
// delivered through stream and the result is empty. The error, if any, is
// already mapped to a protocol error code.
func (h *Handler) Handle(ctx context.Context, req protocol.Request, stream func(protocol.LogChunk) error) (json.RawMessage, *protocol.Error) {
	if perr := h.Policy.Validate(req.Identity); perr != nil {
		return nil, perr
	}
	if h.readOnly != "" && isWrite(req.Op) {
		h.Logger.LogAttrs(ctx, slog.LevelInfo, "write refused: read-only",
			slog.String("op", string(req.Op)), slog.String("target", req.Target.ID()), slog.String("user", req.Identity.User))
		return nil, &protocol.Error{Code: 403, Message: "agent: " + h.readOnly}
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
	if req.Op == protocol.OpRules {
		var args protocol.RulesArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		return h.rules(ctx, req.Identity, args)
	}
	if req.Op == protocol.OpHiddenJobs {
		var args protocol.HiddenJobsArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		if h.Jobs == nil {
			return nil, &protocol.Error{Code: 503, Message: "agent: hidden jobs are not available"}
		}
		if len(args.Namespaces) > maxHiddenJobNamespaces {
			return nil, badRequest("at most %d namespaces per request", maxHiddenJobNamespaces)
		}
		return h.Jobs.HiddenJobs(args.Namespaces, args.Offset, args.Limit, yamlBudget), nil
	}
	if req.Op == protocol.OpYAML || req.Op == protocol.OpEvents {
		return h.read(ctx, req)
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
	case protocol.OpLogs:
		var args protocol.LogsArgs
		if err := decodeArgs(req.Args, &args); err != nil {
			return nil, err
		}
		switch {
		case k.Kind == flux.KindPod:
			return nil, streamLogs(ctx, cl.Kube, t, args, stream)
		case isWorkloadKind(k.Kind):
			return nil, h.workloadLogs(ctx, cl, gvr, t, args, stream)
		}
		return nil, badRequest("logs are only available for Pods and workloads (Deployment, StatefulSet, DaemonSet, ReplicaSet, Job)")
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

// readTarget is a resolved yaml or events target.
type readTarget struct {
	ref        model.Ref
	gvr        schema.GroupVersionResource
	namespaced bool
}

// resolveRead resolves a yaml or events target: a served kind from the
// table, or else any kind the cluster serves, through Kinds (objects known
// only from a Kustomization inventory, and preset kinds whose preset is
// off). Whether the user may read the object is left to the API server.
func (h *Handler) resolveRead(t model.Ref) (readTarget, error) {
	if !flux.ValidKindName(t.Kind) || t.Name == "" {
		return readTarget{}, badRequest("invalid target %s", t.ID())
	}
	if k, ok := flux.KindByName(t.Kind); ok && k.Matches(t.Group, t.Kind) {
		if gvr, ok := h.Served.GVR(k.Kind); ok {
			return h.readTarget(t, gvr, k.Namespaced)
		}
	}
	notServed := &protocol.Error{Code: 404, Message: fmt.Sprintf("agent: %s/%s is not served by this cluster", t.Group, t.Kind)}
	if h.Kinds == nil {
		return readTarget{}, notServed
	}
	gvr, namespaced, err := h.Kinds.Resolve(t.Group, t.Kind)
	if meta.IsNoMatchError(err) {
		return readTarget{}, notServed
	}
	if err != nil {
		return readTarget{}, &protocol.Error{Code: 503, Message: err.Error()}
	}
	return h.readTarget(t, gvr, namespaced)
}

func (h *Handler) readTarget(t model.Ref, gvr schema.GroupVersionResource, namespaced bool) (readTarget, error) {
	if namespaced && t.Namespace == "" {
		return readTarget{}, badRequest("target %s needs a namespace", t.ID())
	}
	if !namespaced {
		t.Namespace = ""
	}
	return readTarget{ref: t, gvr: gvr, namespaced: namespaced}, nil
}

// read runs OpYAML or OpEvents as the request identity. Secret YAML is
// refused before anything is read.
func (h *Handler) read(ctx context.Context, req protocol.Request) (any, error) {
	if req.Op == protocol.OpYAML && !flux.YAMLAllowed(req.Target.Kind) {
		return nil, errSecretYAML(req.Target.Kind)
	}
	rt, err := h.resolveRead(req.Target)
	if err != nil {
		return nil, err
	}
	cl, err := h.Impersonate(req.Identity)
	if err != nil {
		return nil, err
	}
	if req.Op == protocol.OpYAML {
		return h.yaml(ctx, cl, rt)
	}
	return h.events(ctx, cl, rt)
}

func errSecretYAML(kind string) *protocol.Error {
	return &protocol.Error{Code: 403, Message: fmt.Sprintf("agent: yaml is never available for %s objects", kind)}
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

func (h *Handler) yaml(ctx context.Context, cl Clients, rt readTarget) (any, error) {
	t := rt.ref
	if rt.gvr.Group == flux.GroupCore && rt.gvr.Resource == "secrets" {
		return nil, errSecretYAML(t.Kind)
	}
	obj, err := cl.Dynamic.Resource(rt.gvr).Namespace(t.Namespace).Get(ctx, t.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("agent: get %s: %w", t.ID(), err)
	}
	if !flux.YAMLAllowed(obj.GetKind()) {
		return nil, errSecretYAML(obj.GetKind())
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

// events lists the object's events. Events about cluster-scoped objects
// are recorded in the default namespace. Events whose involved object is in
// another API group (a Kind name two groups share) are dropped.
func (h *Handler) events(ctx context.Context, cl Clients, rt readTarget) (any, error) {
	t := rt.ref
	sel := fields.Set{
		"involvedObject.name":      t.Name,
		"involvedObject.namespace": t.Namespace,
		"involvedObject.kind":      t.Kind,
	}.AsSelector().String()
	ns := t.Namespace
	if !rt.namespaced {
		ns = metav1.NamespaceDefault
	}
	list, err := cl.Kube.CoreV1().Events(ns).List(ctx, metav1.ListOptions{FieldSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("agent: list events of %s: %w", t.ID(), err)
	}
	out := make([]model.Event, 0, len(list.Items))
	for i := range list.Items {
		e := &list.Items[i]
		if av := e.InvolvedObject.APIVersion; av != "" {
			if gv, err := schema.ParseGroupVersion(av); err == nil && gv.Group != t.Group {
				continue
			}
		}
		out = append(out, trimEvent(e))
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
	review := h.review
	if review == nil {
		review = h.subjectAccessReview
	}
	allowed := make([]bool, len(args.Checks))
	errs := make([]error, len(args.Checks))
	var wg sync.WaitGroup
	sem := h.sarSlots()
	for i, c := range args.Checks {
		wg.Go(func() {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			allowed[i], errs[i] = review(ctx, id, c)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return protocol.AccessResult{Allowed: allowed}, nil
}

// subjectAccessReview asks whether id may perform c, as the agent's own
// ServiceAccount.
func (h *Handler) subjectAccessReview(ctx context.Context, id protocol.Identity, c protocol.AccessCheck) (bool, error) {
	sar := &authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
		User:               id.User,
		Groups:             slices.Clone(id.Groups),
		ResourceAttributes: resourceAttributes(c),
	}}
	res, err := h.Self.AuthorizationV1().SubjectAccessReviews().Create(ctx, sar, metav1.CreateOptions{})
	if err != nil {
		return false, fmt.Errorf("agent: subject access review %s %s/%s: %w", c.Verb, c.Group, c.Resource, err)
	}
	return res.Status.Allowed && !res.Status.Denied, nil
}

func resourceAttributes(c protocol.AccessCheck) *authorizationv1.ResourceAttributes {
	return &authorizationv1.ResourceAttributes{
		Verb: c.Verb, Group: c.Group, Resource: c.Resource, Subresource: c.Subresource,
		Namespace: c.Namespace, Name: c.Name,
	}
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
