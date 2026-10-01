package flux

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/idestis/eddy/internal/model"
)

// Summaries of the cluster plumbing kinds that are always watched:
// Namespaces, StorageClasses, PodDisruptionBudgets, ServiceAccounts and
// NetworkPolicies. None of them reads more than metadata, a few spec fields
// and status.

// maxDetailValue caps one Resource.Details value.
const maxDetailValue = 200

// addDetail appends a labelled fact, skipping empty values and staying
// within model.MaxDetails.
func addDetail(r *model.Resource, label, value string) {
	value = OneLine(value, maxDetailValue)
	if value == "" || len(r.Details) >= model.MaxDetails {
		return
	}
	r.Details = append(r.Details, model.Detail{Label: label, Value: value})
}

// scalar renders a string, integer, float or boolean field; resource
// quantities arrive as either strings or numbers.
func scalar(obj map[string]any, path ...string) string {
	v, _ := field(obj, path...)
	switch n := v.(type) {
	case string:
		return n
	case int64:
		return strconv.FormatInt(n, 10)
	case int:
		return strconv.Itoa(n)
	case int32:
		return strconv.FormatInt(int64(n), 10)
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(n)
	}
	return ""
}

// summarizeNamespace: Active is ready, Terminating is reconciling (with the
// content the namespace is still waiting on, when a condition says).
func summarizeNamespace(obj map[string]any, r *model.Resource) {
	phase := firstNonEmpty(str(obj, "status", "phase"), "Active")
	switch phase {
	case "Active":
		r.Status, r.Message = model.StatusReady, "Active"
	case "Terminating":
		msg := "Terminating"
		for _, typ := range []string{"NamespaceDeletionContentFailure", "NamespaceContentRemaining", "NamespaceFinalizersRemaining"} {
			if c := condition(r.Conditions, typ); isTrue(c) && c.Message != "" {
				msg += ": " + c.Message
				break
			}
		}
		r.Status, r.Message = model.StatusReconciling, msg
	default:
		r.Status, r.Message = model.StatusUnknown, phase
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// Default-class annotations of a StorageClass (Trim keeps them).
const (
	AnnotationDefaultStorageClass     = "storageclass.kubernetes.io/is-default-class"
	annotationDefaultStorageClassBeta = "storageclass.beta.kubernetes.io/is-default-class"
)

// summarizeStorageClass: always ready; the message and details say what it
// provisions and how.
func summarizeStorageClass(obj map[string]any, r *model.Resource) {
	prov := str(obj, "provisioner")
	reclaim := firstNonEmpty(str(obj, "reclaimPolicy"), "Delete")
	binding := firstNonEmpty(str(obj, "volumeBindingMode"), "Immediate")
	isDefault := str(obj, "metadata", "annotations", AnnotationDefaultStorageClass) == "true" ||
		str(obj, "metadata", "annotations", annotationDefaultStorageClassBeta) == "true"
	parts := []string{firstNonEmpty(prov, "no provisioner")}
	if isDefault {
		parts = append(parts, "default")
	}
	parts = append(parts, reclaim, binding)
	r.Status, r.Message = model.StatusReady, OneLine(strings.Join(parts, " · "), maxMessage)
	addDetail(r, "Provisioner", prov)
	if isDefault {
		addDetail(r, "Default class", "yes")
	}
	addDetail(r, "Reclaim policy", reclaim)
	addDetail(r, "Volume binding mode", binding)
	if v, ok := field(obj, "allowVolumeExpansion"); ok {
		if b, ok := v.(bool); ok {
			addDetail(r, "Volume expansion", map[bool]string{true: "allowed", false: "not allowed"}[b])
		}
	}
}

// summarizePDB: Replicas is "currentHealthy/desiredHealthy".
//   - a DisruptionAllowed=False condition for any reason other than
//     InsufficientPods (for example SyncFailed) is failed;
//   - fewer healthy pods than required is reconciling (a rollout or a
//     failing workload; the pods themselves say which);
//   - zero disruptions allowed with enough healthy pods is ready, with a
//     message that it blocks voluntary evictions (node drains);
//   - otherwise ready.
func summarizePDB(obj map[string]any, r *model.Resource) {
	current, _ := integer(obj, "status", "currentHealthy")
	desired, _ := integer(obj, "status", "desiredHealthy")
	allowed, _ := integer(obj, "status", "disruptionsAllowed")
	expected, _ := integer(obj, "status", "expectedPods")
	r.Replicas = fmt.Sprintf("%d/%d", current, desired)
	if v := scalar(obj, "spec", "minAvailable"); v != "" {
		addDetail(r, "Min available", v)
	}
	if v := scalar(obj, "spec", "maxUnavailable"); v != "" {
		addDetail(r, "Max unavailable", v)
	}
	addDetail(r, "Healthy", fmt.Sprintf("%d of %d required", current, desired))
	addDetail(r, "Disruptions allowed", strconv.FormatInt(allowed, 10))
	addDetail(r, "Expected pods", strconv.FormatInt(expected, 10))
	cond := condition(r.Conditions, "DisruptionAllowed")
	switch {
	case isFalse(cond) && cond.Reason != "InsufficientPods":
		r.Status, r.Message = model.StatusFailed, firstNonEmpty(cond.Message, cond.Reason)
	case generationPending(obj):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the controller to observe the latest spec"
	case expected == 0:
		r.Status, r.Message = model.StatusReady, "No pods selected"
	case current < desired:
		r.Status, r.Message = model.StatusReconciling, fmt.Sprintf("%d of %d required pods healthy", current, desired)
	case allowed == 0:
		r.Status, r.Message = model.StatusReady, "0 disruptions allowed · blocks voluntary evictions"
	default:
		r.Status, r.Message = model.StatusReady, fmt.Sprintf("%d disruptions allowed · %d of %d healthy", allowed, current, expected)
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// summarizeServiceAccount: a ServiceAccount has no status; it is ready.
// Only its name is shown (no token or Secret references).
func summarizeServiceAccount(r *model.Resource) {
	r.Status = model.StatusReady
}

// summarizeNetworkPolicy: always ready; the message names the policy types
// and the selected pods.
func summarizeNetworkPolicy(obj map[string]any, r *model.Resource) {
	var types []string
	for _, t := range list(obj, "spec", "policyTypes") {
		if s, ok := t.(string); ok && s != "" {
			types = append(types, s)
		}
	}
	if len(types) == 0 {
		// The API server defaults policyTypes; this only covers old objects.
		types = []string{"Ingress"}
		if len(list(obj, "spec", "egress")) > 0 {
			types = append(types, "Egress")
		}
	}
	sel := selectorString(mapping(obj, "spec", "podSelector"))
	if sel == "" {
		sel = "all pods"
	}
	r.Status = model.StatusReady
	r.Message = OneLine(strings.Join(types, ", ")+" · "+sel, maxMessage)
	addDetail(r, "Policy types", strings.Join(types, ", "))
	addDetail(r, "Pod selector", sel)
	addDetail(r, "Ingress rules", strconv.Itoa(len(list(obj, "spec", "ingress"))))
	addDetail(r, "Egress rules", strconv.Itoa(len(list(obj, "spec", "egress"))))
}

// selectorString renders a label selector's matchLabels ("a=b, c=d") and
// the number of matchExpressions.
func selectorString(sel map[string]any) string {
	var parts []string
	for k, v := range mapping(sel, "matchLabels") {
		if s, ok := v.(string); ok {
			parts = append(parts, k+"="+s)
		}
	}
	sort.Strings(parts)
	if n := len(list(sel, "matchExpressions")); n > 0 {
		parts = append(parts, fmt.Sprintf("%d expressions", n))
	}
	return strings.Join(parts, ", ")
}
