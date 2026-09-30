package flux

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/eddy-gitops/eddy/internal/model"
)

// Caps on text copied into a summary.
const (
	maxMessage          = 300
	maxConditionMessage = 1024
)

// Flux ownership labels, set by kustomize-controller and helm-controller on
// every object they apply.
const (
	LabelKustomizeName      = "kustomize.toolkit.fluxcd.io/name"
	LabelKustomizeNamespace = "kustomize.toolkit.fluxcd.io/namespace"
	LabelHelmName           = "helm.toolkit.fluxcd.io/name"
	LabelHelmNamespace      = "helm.toolkit.fluxcd.io/namespace"
)

// OwnerLookup returns the controller of an object that the caller has cached
// but does not surface, such as the Deployment that owns a ReplicaSet. It lets
// Summarize attribute a Pod to its Deployment rather than to a ReplicaSet.
// It may be nil.
type OwnerLookup func(ref model.Ref) (model.Ref, bool)

// Summarize turns an object of kind k into a model.Resource, applying the
// status rules of the kind. It reads only metadata, spec fields that describe
// the object (sources, intervals, replicas, images) and status.
func Summarize(k Kind, u *unstructured.Unstructured, owners OwnerLookup) model.Resource {
	obj := u.Object
	ref := model.Ref{Group: k.Group, Kind: k.Kind, Namespace: u.GetNamespace(), Name: u.GetName()}
	r := model.Resource{
		Ref:             ref,
		ID:              ref.ID(),
		Version:         schema.FromAPIVersionAndKind(u.GetAPIVersion(), u.GetKind()).Version,
		Status:          model.StatusUnknown,
		Conditions:      conditions(obj),
		Labels:          allowedLabels(u.GetLabels()),
		CreatedAt:       u.GetCreationTimestamp().UTC(),
		ResourceVersion: u.GetResourceVersion(),
		Owner:           ownerOf(u, owners),
	}
	r.LastChanged = r.CreatedAt
	for _, c := range r.Conditions {
		if c.LastTransitionTime.After(r.LastChanged) {
			r.LastChanged = c.LastTransitionTime
		}
	}

	switch {
	case k.Flux:
		summarizeFlux(k, obj, &r)
	case k.Kind == KindPod:
		summarizePod(obj, &r)
	default:
		summarizeWorkload(k, obj, &r)
	}
	if u.GetDeletionTimestamp() != nil && r.Status != model.StatusFailed {
		r.Status, r.Message = model.StatusReconciling, "Terminating"
	}
	return r
}

func conditions(obj map[string]any) []model.Condition {
	var out []model.Condition
	for _, c := range maps(obj, "status", "conditions") {
		out = append(out, model.Condition{
			Type:               str(c, "type"),
			Status:             str(c, "status"),
			Reason:             str(c, "reason"),
			Message:            OneLine(str(c, "message"), maxConditionMessage),
			LastTransitionTime: timestamp(c, "lastTransitionTime").UTC(),
		})
	}
	return out
}

func condition(cs []model.Condition, typ string) *model.Condition {
	for i := range cs {
		if cs[i].Type == typ {
			return &cs[i]
		}
	}
	return nil
}

func isTrue(c *model.Condition) bool  { return c != nil && c.Status == "True" }
func isFalse(c *model.Condition) bool { return c != nil && c.Status == "False" }

// allowedLabels keeps app.kubernetes.io/* and the Flux ownership labels.
func allowedLabels(in map[string]string) map[string]string {
	var out map[string]string
	for k, v := range in {
		switch {
		case strings.HasPrefix(k, "app.kubernetes.io/"),
			k == LabelKustomizeName, k == LabelKustomizeNamespace,
			k == LabelHelmName, k == LabelHelmNamespace:
			if out == nil {
				out = map[string]string{}
			}
			out[k] = v
		}
	}
	return out
}

// ownerOf prefers the controller ownerReference (resolved through owners),
// then the helm-controller labels, then the kustomize-controller labels.
func ownerOf(u *unstructured.Unstructured, owners OwnerLookup) *model.Ref {
	if c := controllerRef(u); c != nil {
		if owners != nil {
			if o, ok := owners(*c); ok {
				return &o
			}
		}
		return c
	}
	l := u.GetLabels()
	if n := l[LabelHelmName]; n != "" {
		return &model.Ref{Group: GroupHelm, Kind: KindHelmRelease, Namespace: labelNamespace(l[LabelHelmNamespace], u), Name: n}
	}
	if n := l[LabelKustomizeName]; n != "" {
		return &model.Ref{Group: GroupKustomize, Kind: KindKustomization, Namespace: labelNamespace(l[LabelKustomizeNamespace], u), Name: n}
	}
	return nil
}

func labelNamespace(ns string, u *unstructured.Unstructured) string {
	if ns != "" {
		return ns
	}
	return u.GetNamespace()
}

// controllerRef returns the ownerReference marked as controller, or the first
// one when none is. Owners are always in the object's namespace.
func controllerRef(u *unstructured.Unstructured) *model.Ref {
	refs := u.GetOwnerReferences()
	if len(refs) == 0 {
		return nil
	}
	pick := refs[0]
	for _, o := range refs {
		if o.Controller != nil && *o.Controller {
			pick = o
			break
		}
	}
	gv, err := schema.ParseGroupVersion(pick.APIVersion)
	if err != nil {
		return nil
	}
	return &model.Ref{Group: gv.Group, Kind: pick.Kind, Namespace: u.GetNamespace(), Name: pick.Name}
}

// generationPending reports whether the controller has not yet observed the
// latest spec.
func generationPending(obj map[string]any) bool {
	gen, _ := integer(obj, "metadata", "generation")
	obs, ok := integer(obj, "status", "observedGeneration")
	return ok && obs < gen
}

// summarizeFlux applies the Flux status rules: spec.suspend first, then the
// Stalled, Reconciling and Ready conditions.
func summarizeFlux(k Kind, obj map[string]any, r *model.Resource) {
	r.Suspended = boolean(obj, "spec", "suspend")
	r.Interval = str(obj, "spec", "interval")
	fillFluxDetails(k, obj, r)

	ready := condition(r.Conditions, "Ready")
	reconciling := condition(r.Conditions, "Reconciling")
	stalled := condition(r.Conditions, "Stalled")
	switch {
	case r.Suspended:
		r.Status, r.Message = model.StatusSuspended, "Reconciliation suspended"
	case isTrue(stalled):
		r.Status, r.Message = model.StatusFailed, stalled.Message
	case isTrue(reconciling) && isFalse(ready) && reconciling.Reason == "ProgressingWithRetry":
		// Flux retries a failed reconciliation with Reconciling=True; the
		// object is still failing.
		r.Status, r.Message = model.StatusFailed, ready.Message
	case isTrue(reconciling):
		r.Status, r.Message = model.StatusReconciling, reconciling.Message
	case ready == nil:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the controller to report status"
	case isTrue(ready) && generationPending(obj):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the controller to observe the latest spec"
	case isTrue(ready):
		r.Status, r.Message = model.StatusReady, ready.Message
	case isFalse(ready):
		r.Status, r.Message = model.StatusFailed, ready.Message
	default:
		r.Status, r.Message = model.StatusReconciling, ready.Message
	}
	r.Message = OneLine(r.Message, maxMessage)
}

func fillFluxDetails(k Kind, obj map[string]any, r *model.Resource) {
	switch k.Kind {
	case KindKustomization:
		r.Revision = str(obj, "status", "lastAppliedRevision")
		r.Source = sourceRef(mapping(obj, "spec", "sourceRef"), r.Namespace)
		r.Inventory = len(list(obj, "status", "inventory", "entries"))
	case KindHelmRelease:
		r.Source = helmReleaseSource(obj, r.Namespace)
		chart := str(obj, "spec", "chart", "spec", "chart")
		version := str(obj, "spec", "chart", "spec", "version")
		if h := maps(obj, "status", "history"); len(h) > 0 {
			chart = firstNonEmpty(str(h[0], "chartName"), chart)
			version = firstNonEmpty(str(h[0], "chartVersion"), version)
		}
		if chart != "" {
			r.Chart = chart
			if version != "" {
				r.Chart += "@" + version
			}
		}
		r.Revision = firstNonEmpty(
			str(obj, "status", "lastAppliedRevision"),
			version,
			str(obj, "status", "lastAttemptedRevision"),
		)
	case KindHelmChart:
		r.Revision = str(obj, "status", "artifact", "revision")
		r.Source = sourceRef(mapping(obj, "spec", "sourceRef"), r.Namespace)
		if chart := str(obj, "spec", "chart"); chart != "" {
			r.Chart = chart
			if v := firstNonEmpty(r.Revision, str(obj, "spec", "version")); v != "" {
				r.Chart += "@" + v
			}
		}
	case KindBucket:
		r.Revision = str(obj, "status", "artifact", "revision")
		if ep, b := str(obj, "spec", "endpoint"), str(obj, "spec", "bucketName"); ep != "" || b != "" {
			r.URL = strings.TrimSuffix(ep, "/") + "/" + b
		}
	default: // GitRepository, OCIRepository, HelmRepository
		r.Revision = str(obj, "status", "artifact", "revision")
		r.URL = str(obj, "spec", "url")
	}
}

// helmReleaseSource returns the chart source of a HelmRelease: spec.chartRef
// when set, otherwise spec.chart.spec.sourceRef.
func helmReleaseSource(obj map[string]any, namespace string) *model.Ref {
	if ref := sourceRef(mapping(obj, "spec", "chartRef"), namespace); ref != nil {
		return ref
	}
	return sourceRef(mapping(obj, "spec", "chart", "spec", "sourceRef"), namespace)
}

// sourceRef converts a Flux cross-namespace reference {apiVersion?, kind,
// name, namespace?} into a Ref. The group defaults to source.toolkit.fluxcd.io.
func sourceRef(m map[string]any, namespace string) *model.Ref {
	kind, name := str(m, "kind"), str(m, "name")
	if kind == "" || name == "" {
		return nil
	}
	group := GroupSource
	if av := str(m, "apiVersion"); av != "" {
		if gv, err := schema.ParseGroupVersion(av); err == nil {
			group = gv.Group
		}
	}
	return &model.Ref{Group: group, Kind: kind, Namespace: firstNonEmpty(str(m, "namespace"), namespace), Name: name}
}

// SourceOf returns the source a Flux object reconciles from: spec.sourceRef
// for Kustomizations and HelmCharts, the chart source for HelmReleases, and
// nil for other kinds.
func SourceOf(u *unstructured.Unstructured) *model.Ref {
	switch u.GetKind() {
	case KindKustomization, KindHelmChart:
		return sourceRef(mapping(u.Object, "spec", "sourceRef"), u.GetNamespace())
	case KindHelmRelease:
		return helmReleaseSource(u.Object, u.GetNamespace())
	}
	return nil
}

// HelmReleaseUsesChartTemplate reports whether a HelmRelease declares its
// chart inline (spec.chart), in which case helm-controller manages a HelmChart
// named "<namespace>-<name>" in the source's namespace.
func HelmReleaseUsesChartTemplate(u *unstructured.Unstructured) bool {
	return mapping(u.Object, "spec", "chartRef") == nil && mapping(u.Object, "spec", "chart") != nil
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// podSpecImages lists the distinct container images of a pod spec.
func podSpecImages(spec map[string]any) []string {
	var out []string
	for _, c := range maps(spec, "containers") {
		if img := str(c, "image"); img != "" && !slices.Contains(out, img) {
			out = append(out, img)
		}
	}
	return out
}

// summarizeWorkload applies the workload rules: replica counts, then
// ProgressDeadlineExceeded and availability.
func summarizeWorkload(k Kind, obj map[string]any, r *model.Resource) {
	r.Images = podSpecImages(mapping(obj, "spec", "template", "spec"))
	desired, ok := integer(obj, "spec", "replicas")
	if !ok {
		desired = 1
	}
	ready, _ := integer(obj, "status", "readyReplicas")
	updated, _ := integer(obj, "status", "updatedReplicas")
	total, _ := integer(obj, "status", "replicas")
	if k.Kind == KindDaemonSet {
		desired, _ = integer(obj, "status", "desiredNumberScheduled")
		ready, _ = integer(obj, "status", "numberReady")
		updated, _ = integer(obj, "status", "updatedNumberScheduled")
		total, _ = integer(obj, "status", "currentNumberScheduled")
	}
	r.Replicas = fmt.Sprintf("%d/%d", ready, desired)
	progressing := condition(r.Conditions, "Progressing")
	available := condition(r.Conditions, "Available")
	failure := condition(r.Conditions, "ReplicaFailure")

	rolledOut := updated >= desired && total <= updated
	if k.Kind == KindStatefulSet {
		cur, upd := str(obj, "status", "currentRevision"), str(obj, "status", "updateRevision")
		rolledOut = updated >= desired || cur == upd
	}
	if k.Kind == KindReplicaSet {
		rolledOut = true
	}

	switch {
	case boolean(obj, "spec", "paused"):
		r.Status, r.Message = model.StatusSuspended, "Rollout paused"
	case progressing != nil && progressing.Reason == "ProgressDeadlineExceeded":
		r.Status, r.Message = model.StatusFailed, progressing.Message
	case isTrue(failure):
		r.Status, r.Message = model.StatusFailed, failure.Message
	case generationPending(obj):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the controller to observe the latest spec"
	case !rolledOut:
		r.Status, r.Message = model.StatusReconciling, fmt.Sprintf("Rolling out: %d of %d updated", updated, desired)
	case ready < desired && isFalse(available):
		r.Status, r.Message = model.StatusFailed, available.Message
	case ready < desired:
		r.Status, r.Message = model.StatusReconciling, fmt.Sprintf("%d of %d ready", ready, desired)
	case desired == 0:
		r.Status, r.Message = model.StatusReady, "Scaled to zero"
	default:
		r.Status, r.Message = model.StatusReady, fmt.Sprintf("%d of %d ready", ready, desired)
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// failingWaitReasons are container waiting reasons that mean the Pod is
// broken rather than starting.
var failingWaitReasons = []string{
	"CrashLoopBackOff",
	"ImagePullBackOff",
	"ErrImagePull",
	"CreateContainerConfigError",
	"CreateContainerError",
	"InvalidImageName",
	"RunContainerError",
}

// summarizePod applies the Pod rules: failing waiting reasons first, then the
// phase and container readiness.
func summarizePod(obj map[string]any, r *model.Resource) {
	r.Images = podSpecImages(mapping(obj, "spec"))
	for _, c := range maps(obj, "spec", "containers") {
		if n := str(c, "name"); n != "" {
			r.Containers = append(r.Containers, n)
		}
	}
	statuses := append(maps(obj, "status", "initContainerStatuses"), maps(obj, "status", "containerStatuses")...)
	containers := len(maps(obj, "spec", "containers"))
	var readyCount int
	for _, cs := range maps(obj, "status", "containerStatuses") {
		if boolean(cs, "ready") {
			readyCount++
		}
	}
	r.Replicas = fmt.Sprintf("%d/%d", readyCount, containers)

	for _, cs := range statuses {
		reason := str(cs, "state", "waiting", "reason")
		if slices.Contains(failingWaitReasons, reason) {
			msg := fmt.Sprintf("%s: %s", str(cs, "name"), reason)
			if m := str(cs, "state", "waiting", "message"); m != "" {
				msg += ": " + m
			}
			r.Status, r.Message = model.StatusFailed, OneLine(msg, maxMessage)
			return
		}
	}

	phase := str(obj, "status", "phase")
	switch phase {
	case "Succeeded":
		r.Status, r.Message = model.StatusReady, "Completed"
	case "Failed":
		r.Status, r.Message = model.StatusFailed, firstNonEmpty(str(obj, "status", "message"), str(obj, "status", "reason"), "Failed")
	case "Pending":
		r.Status, r.Message = model.StatusReconciling, firstNonEmpty(waitingReason(statuses), "Pending")
	case "Running":
		if readyCount == containers {
			r.Status, r.Message = model.StatusReady, "Running"
		} else {
			r.Status, r.Message = model.StatusReconciling, firstNonEmpty(waitingReason(statuses), fmt.Sprintf("%d of %d containers ready", readyCount, containers))
		}
	default:
		r.Status, r.Message = model.StatusUnknown, firstNonEmpty(phase, "No status")
	}
	r.Message = OneLine(r.Message, maxMessage)
}

func waitingReason(statuses []map[string]any) string {
	for _, cs := range statuses {
		if reason := str(cs, "state", "waiting", "reason"); reason != "" {
			return str(cs, "name") + ": " + reason
		}
	}
	return ""
}
