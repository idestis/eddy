package flux

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/model"
)

// Summaries of the watch preset kinds: Karpenter (karpenter.sh,
// karpenter.k8s.aws) and External Secrets (external-secrets.io). They share
// summarizeConditions and add a few descriptive details. Secret names are
// shown (an ExternalSecret's target, a PushSecret's source); Secrets are
// never read.

// Labels kept on preset kinds in addition to allowedLabels.
var presetLabels = []string{
	"karpenter.sh/nodepool",
	"karpenter.sh/capacity-type",
	"node.kubernetes.io/instance-type",
	"topology.kubernetes.io/zone",
	"karpenter.k8s.aws/instance-family",
}

// rawCondition returns the status.conditions entry of type typ as read from
// the object (model.Condition drops observedGeneration).
func rawCondition(obj map[string]any, typ string) map[string]any {
	for _, c := range maps(obj, "status", "conditions") {
		if str(c, "type") == typ {
			return c
		}
	}
	return nil
}

// summarizeConditions is the generic rule for controllers that follow the
// Kubernetes condition conventions:
//   - spec.suspend is suspended;
//   - the first of the Ready, Available and Synced conditions decides: True
//     is ready, False is failed ("Reason: message"), Unknown is reconciling;
//   - a True condition whose controller has not observed the latest spec
//     (status.observedGeneration or the condition's own observedGeneration
//     behind metadata.generation) is reconciling;
//   - no condition at all is unknown.
//
// It returns the deciding condition, or nil.
func summarizeConditions(obj map[string]any, r *model.Resource) *model.Condition {
	if boolean(obj, "spec", "suspend") {
		r.Suspended = true
		r.Status, r.Message = model.StatusSuspended, "Suspended"
		return nil
	}
	var c *model.Condition
	for _, typ := range []string{"Ready", "Available", "Synced"} {
		if c = condition(r.Conditions, typ); c != nil {
			break
		}
	}
	switch {
	case c == nil:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the controller to report status"
	case isTrue(c) && (generationPending(obj) || conditionStale(obj, c.Type)):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the controller to observe the latest spec"
	case isTrue(c):
		r.Status, r.Message = model.StatusReady, firstNonEmpty(c.Message, c.Reason, c.Type)
	case isFalse(c):
		r.Status, r.Message = model.StatusFailed, conditionText(c)
	default:
		r.Status, r.Message = model.StatusReconciling, firstNonEmpty(conditionText(c), "Reconciling")
	}
	r.Message = OneLine(r.Message, maxMessage)
	return c
}

// conditionStale reports whether the condition's own observedGeneration is
// behind metadata.generation.
func conditionStale(obj map[string]any, typ string) bool {
	gen, _ := integer(obj, "metadata", "generation")
	obs, ok := integer(rawCondition(obj, typ), "observedGeneration")
	return ok && gen > 0 && obs < gen
}

// conditionText renders "Reason: message", or whichever is set.
func conditionText(c *model.Condition) string {
	switch {
	case c.Reason != "" && c.Message != "":
		return c.Reason + ": " + c.Message
	case c.Message != "":
		return c.Message
	}
	return c.Reason
}

// usageOf renders "used / limit" for one resource, or just one side.
func usageOf(used, limit string) string {
	switch {
	case used != "" && limit != "":
		return used + " / " + limit
	case limit != "":
		return "0 / " + limit
	}
	return used
}

// --- Karpenter ---------------------------------------------------------------

// summarizeNodePool: the Ready condition, with node count and CPU and
// memory usage against spec.limits in the message and details.
func summarizeNodePool(obj map[string]any, r *model.Resource) {
	c := summarizeConditions(obj, r)
	nodes := firstNonEmpty(scalar(obj, "status", "resources", "nodes"), scalar(obj, "status", "nodes"))
	cpu := usageOf(scalar(obj, "status", "resources", "cpu"), scalar(obj, "spec", "limits", "cpu"))
	mem := usageOf(scalar(obj, "status", "resources", "memory"), scalar(obj, "spec", "limits", "memory"))
	addDetail(r, "Nodes", nodes)
	addDetail(r, "CPU", cpu)
	addDetail(r, "Memory", mem)
	if ref := mapping(obj, "spec", "template", "spec", "nodeClassRef"); ref != nil {
		addDetail(r, "Node class", strings.Trim(str(ref, "kind")+"/"+str(ref, "name"), "/"))
	}
	if w, ok := integer(obj, "spec", "weight"); ok {
		addDetail(r, "Weight", fmt.Sprint(w))
	}
	if r.Status == model.StatusReady && isTrue(c) {
		var parts []string
		if nodes != "" {
			parts = append(parts, nodes+" nodes")
		}
		if cpu != "" {
			parts = append(parts, "CPU "+cpu)
		}
		if mem != "" {
			parts = append(parts, "memory "+mem)
		}
		if len(parts) > 0 {
			r.Message = OneLine(strings.Join(parts, " · "), maxMessage)
		}
	}
}

// summarizeNodeClaim follows the NodeClaim lifecycle: Launched, then
// Registered, then Initialized, then Ready. A False Launched condition (for
// example InsufficientCapacityError) is failed; a step that has not
// happened yet is reconciling. The instance type, capacity type and zone
// come from the labels Karpenter sets.
func summarizeNodeClaim(obj map[string]any, r *model.Resource) {
	labels := mapping(obj, "metadata", "labels")
	instance := str(labels, "node.kubernetes.io/instance-type")
	capacity := str(labels, "karpenter.sh/capacity-type")
	zone := str(labels, "topology.kubernetes.io/zone")
	node := str(obj, "status", "nodeName")
	addDetail(r, "Instance type", instance)
	addDetail(r, "Capacity type", capacity)
	addDetail(r, "Zone", zone)
	addDetail(r, "Node", node)
	addDetail(r, "Node pool", str(labels, "karpenter.sh/nodepool"))
	addDetail(r, "Image", str(obj, "status", "imageID"))

	launched := condition(r.Conditions, "Launched")
	registered := condition(r.Conditions, "Registered")
	initialized := condition(r.Conditions, "Initialized")
	ready := condition(r.Conditions, "Ready")
	drifted := condition(r.Conditions, "Drifted")
	switch {
	case isFalse(launched) && launched.Reason != "" && launched.Reason != "Launching":
		r.Status, r.Message = model.StatusFailed, "Launch failed: "+conditionText(launched)
	case !isTrue(launched):
		r.Status, r.Message = model.StatusReconciling, "Launching"
	case !isTrue(registered):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the node to register"
	case !isTrue(initialized):
		r.Status, r.Message = model.StatusReconciling, "Initializing the node"
	case isFalse(ready):
		r.Status, r.Message = model.StatusFailed, conditionText(ready)
	case ready != nil && !isTrue(ready):
		r.Status, r.Message = model.StatusReconciling, firstNonEmpty(conditionText(ready), "Not ready")
	default:
		var parts []string
		for _, p := range []string{instance, capacity, zone, node} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		r.Status, r.Message = model.StatusReady, firstNonEmpty(strings.Join(parts, " · "), "Ready")
		if isTrue(drifted) {
			r.Message += " · drifted"
		}
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// summarizeEC2NodeClass: the Ready condition, with the AMI family or alias
// and the node role.
func summarizeEC2NodeClass(obj map[string]any, r *model.Resource) {
	c := summarizeConditions(obj, r)
	family := str(obj, "spec", "amiFamily")
	var alias string
	for _, t := range maps(obj, "spec", "amiSelectorTerms") {
		if a := str(t, "alias"); a != "" {
			alias = a
			break
		}
	}
	addDetail(r, "AMI family", family)
	addDetail(r, "AMI alias", alias)
	addDetail(r, "Role", firstNonEmpty(str(obj, "spec", "role"), str(obj, "spec", "instanceProfile")))
	addDetail(r, "AMIs", countOf(len(list(obj, "status", "amis"))))
	if r.Status == model.StatusReady && isTrue(c) {
		var parts []string
		for _, p := range []string{family, alias} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			r.Message = OneLine(strings.Join(parts, " · "), maxMessage)
		}
	}
}

func countOf(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprint(n)
}

// --- External Secrets --------------------------------------------------------

// storeRef turns an ESO secretStoreRef ({name, kind}) into a Ref. The kind
// defaults to SecretStore, which lives in the referring object's namespace.
func storeRef(m map[string]any, namespace string) *model.Ref {
	name := str(m, "name")
	if name == "" {
		return nil
	}
	kind := firstNonEmpty(str(m, "kind"), KindSecretStore)
	ref := &model.Ref{Group: GroupESO, Kind: kind, Name: name}
	if kind == KindSecretStore {
		ref.Namespace = namespace
	}
	return ref
}

func refLabel(ref *model.Ref) string {
	if ref == nil {
		return ""
	}
	return ref.Kind + "/" + ref.Name
}

// lastRefresh records status.refreshTime as a detail and in LastChanged.
func lastRefresh(obj map[string]any, r *model.Resource) {
	t := timestamp(obj, "status", "refreshTime").UTC()
	if t.IsZero() {
		return
	}
	addDetail(r, "Last refresh", t.Format(time.RFC3339))
	if t.After(r.LastChanged) {
		r.LastChanged = t
	}
}

// summarizeExternalSecret: the Ready condition (SecretSyncedError and the
// like are failed), with the target Secret's name, the store and the
// refresh interval.
func summarizeExternalSecret(obj map[string]any, r *model.Resource) {
	summarizeConditions(obj, r)
	r.Interval = scalar(obj, "spec", "refreshInterval")
	r.Source = storeRef(mapping(obj, "spec", "secretStoreRef"), r.Namespace)
	target := firstNonEmpty(str(obj, "spec", "target", "name"), str(obj, "status", "binding", "name"), r.Name)
	addDetail(r, "Target secret", target)
	addDetail(r, "Store", refLabel(r.Source))
	addDetail(r, "Refresh interval", r.Interval)
	addDetail(r, "Creation policy", str(obj, "spec", "target", "creationPolicy"))
	lastRefresh(obj, r)
}

// summarizeClusterExternalSecret: the Ready condition, failed while any
// namespace failed, with provisioned and failed namespace counts.
func summarizeClusterExternalSecret(obj map[string]any, r *model.Resource) {
	summarizeConditions(obj, r)
	spec := mapping(obj, "spec", "externalSecretSpec")
	r.Interval = scalar(spec, "refreshInterval")
	r.Source = storeRef(mapping(spec, "secretStoreRef"), "")
	provisioned := len(list(obj, "status", "provisionedNamespaces"))
	failed := maps(obj, "status", "failedNamespaces")
	addDetail(r, "External secret", firstNonEmpty(str(obj, "spec", "externalSecretName"), r.Name))
	addDetail(r, "Target secret", str(spec, "target", "name"))
	addDetail(r, "Store", refLabel(r.Source))
	addDetail(r, "Namespaces", fmt.Sprintf("%d provisioned, %d failed", provisioned, len(failed)))
	addDetail(r, "Refresh interval", r.Interval)
	if len(failed) > 0 && r.Status != model.StatusSuspended {
		var names []string
		for _, f := range failed {
			if n := str(f, "namespace"); n != "" && !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
		msg := fmt.Sprintf("Failed in %d namespaces", len(failed))
		if len(names) > 0 {
			msg += ": " + strings.Join(names, ", ")
		}
		if reason := str(failed[0], "reason"); reason != "" {
			msg += " (" + reason + ")"
		}
		r.Status, r.Message = model.StatusFailed, OneLine(msg, maxMessage)
	}
}

// providerName returns the provider key of a SecretStore's spec.provider
// (for example "aws" or "vault").
func providerName(obj map[string]any) string {
	var names []string
	for k := range mapping(obj, "spec", "provider") {
		names = append(names, k)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// summarizeSecretStore (SecretStore and ClusterSecretStore): the Ready
// condition, with the provider and capabilities.
func summarizeSecretStore(obj map[string]any, r *model.Resource) {
	c := summarizeConditions(obj, r)
	provider := providerName(obj)
	capabilities := str(obj, "status", "capabilities")
	addDetail(r, "Provider", provider)
	addDetail(r, "Capabilities", capabilities)
	if v := scalar(obj, "spec", "refreshInterval"); v != "" && v != "0" {
		addDetail(r, "Refresh interval", v)
	}
	if r.Status == model.StatusReady && isTrue(c) && provider != "" {
		msg := provider
		if capabilities != "" {
			msg += " · " + capabilities
		}
		if c.Message != "" {
			msg += " · " + c.Message
		}
		r.Message = OneLine(msg, maxMessage)
	}
}

// summarizePushSecret: the Ready condition, with the source Secret's name,
// the stores it pushes to and the refresh interval.
func summarizePushSecret(obj map[string]any, r *model.Resource) {
	summarizeConditions(obj, r)
	r.Interval = scalar(obj, "spec", "refreshInterval")
	var stores []string
	for _, s := range maps(obj, "spec", "secretStoreRefs") {
		if ref := storeRef(s, r.Namespace); ref != nil {
			stores = append(stores, refLabel(ref))
		} else if lbl := selectorString(mapping(s, "labelSelector")); lbl != "" {
			stores = append(stores, firstNonEmpty(str(s, "kind"), KindSecretStore)+" ("+lbl+")")
		}
	}
	addDetail(r, "Source secret", str(obj, "spec", "selector", "secret", "name"))
	addDetail(r, "Stores", strings.Join(stores, ", "))
	addDetail(r, "Refresh interval", r.Interval)
	lastRefresh(obj, r)
}
