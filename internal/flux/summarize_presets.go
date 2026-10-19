package flux

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/idestis/eddy/internal/model"
)

// Summaries of the watch preset kinds: Karpenter (karpenter.sh,
// karpenter.k8s.aws), External Secrets (external-secrets.io), CloudNativePG
// (postgresql.cnpg.io), RabbitMQ (rabbitmq.com) and Datadog
// (datadoghq.com). Most share summarizeConditions and add a few descriptive
// details; operators that report a phase or component counts get their own
// rules. Secret names are
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

// binaryQuantity rewrites a byte quantity in the largest binary unit that
// keeps it at 1 or more, with at most one decimal: "273098696Ki" becomes
// "260.4Gi". Unparseable input is returned unchanged.
func binaryQuantity(s string) string {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		return s
	}
	v := q.AsApproximateFloat64()
	units := []string{"", "Ki", "Mi", "Gi", "Ti", "Pi", "Ei"}
	i := 0
	for ; i < len(units)-1 && v >= 1024; i++ {
		v /= 1024
	}
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0") + units[i]
}

// --- Karpenter ---------------------------------------------------------------

// summarizeNodePool: the Ready condition, with node count and CPU and
// memory usage against spec.limits in the message and details.
func summarizeNodePool(obj map[string]any, r *model.Resource) {
	c := summarizeConditions(obj, r)
	nodes := firstNonEmpty(scalar(obj, "status", "resources", "nodes"), scalar(obj, "status", "nodes"))
	cpu := usageOf(scalar(obj, "status", "resources", "cpu"), scalar(obj, "spec", "limits", "cpu"))
	mem := usageOf(binaryQuantity(scalar(obj, "status", "resources", "memory")), binaryQuantity(scalar(obj, "spec", "limits", "memory")))
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

// --- CloudNativePG -----------------------------------------------------------

// cnpgClusterRef is the CNPG Cluster a Backup, ScheduledBackup or Pooler
// names in spec.cluster.name.
func cnpgClusterRef(obj map[string]any, namespace string) *model.Ref {
	name := str(obj, "spec", "cluster", "name")
	if name == "" {
		return nil
	}
	return &model.Ref{Group: GroupCNPG, Kind: KindCNPGCluster, Namespace: namespace, Name: name}
}

// cnpgFailedPhase reports whether a Cluster phase needs a human: the
// operator's phases for unrecoverable states, missing objects, image
// catalog or plugin errors and waiting for user action.
func cnpgFailedPhase(phase string) bool {
	p := strings.ToLower(phase)
	for _, s := range []string{"unrecoverable", "cannot", "unable", "error", "waiting for user", "missing"} {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// summarizeCNPGCluster: hibernated is suspended; a failing phase is failed;
// the Ready condition True is ready, anything else is reconciling with the
// phase as message. Instances, primary, image, storage and the last
// successful backup are details; a False LastBackupSucceeded or
// ContinuousArchiving condition is noted on a ready cluster.
func summarizeCNPGCluster(obj map[string]any, r *model.Resource) {
	phase := str(obj, "status", "phase")
	reason := str(obj, "status", "phaseReason")
	primary := str(obj, "status", "currentPrimary")
	image := firstNonEmpty(str(obj, "status", "image"), str(obj, "spec", "imageName"))
	desired, _ := integer(obj, "spec", "instances")
	ready, _ := integer(obj, "status", "readyInstances")
	if desired > 0 {
		r.Replicas = fmt.Sprintf("%d/%d", ready, desired)
	}
	if image != "" {
		r.Images = []string{image}
	}
	addDetail(r, "Instances", r.Replicas)
	addDetail(r, "Primary", primary)
	addDetail(r, "Image", image)
	addDetail(r, "Storage", scalar(obj, "spec", "storage", "size"))
	addDetail(r, "WAL storage", scalar(obj, "spec", "walStorage", "size"))
	if t := timestamp(obj, "status", "lastSuccessfulBackup").UTC(); !t.IsZero() {
		addDetail(r, "Last backup", t.Format(time.RFC3339))
	}

	readyCond := condition(r.Conditions, "Ready")
	switch {
	case isTrue(condition(r.Conditions, "cnpg.io/hibernation")):
		r.Suspended = true
		r.Status, r.Message = model.StatusSuspended, "Hibernated"
	case cnpgFailedPhase(phase) && reason != "":
		r.Status, r.Message = model.StatusFailed, phase+": "+reason
	case cnpgFailedPhase(phase):
		r.Status, r.Message = model.StatusFailed, phase
	case isTrue(readyCond) && generationPending(obj):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the operator to observe the latest spec"
	case isTrue(readyCond):
		var parts []string
		if r.Replicas != "" {
			parts = append(parts, r.Replicas+" instances")
		}
		if primary != "" {
			parts = append(parts, "primary "+primary)
		}
		for _, typ := range []string{"ContinuousArchiving", "LastBackupSucceeded"} {
			if c := condition(r.Conditions, typ); isFalse(c) {
				parts = append(parts, typ+": "+firstNonEmpty(c.Reason, "False"))
			}
		}
		r.Status, r.Message = model.StatusReady, firstNonEmpty(strings.Join(parts, " · "), phase, "Ready")
	case phase == "" && readyCond == nil:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the operator to report status"
	default:
		r.Status, r.Message = model.StatusReconciling, firstNonEmpty(phase, conditionText(readyCond), "Reconciling")
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// summarizeBackup follows status.phase: completed is ready, failed (and
// walArchivingFailing) is failed with status.error, anything else is
// reconciling.
func summarizeBackup(obj map[string]any, r *model.Resource) {
	r.Source = cnpgClusterRef(obj, r.Namespace)
	phase := str(obj, "status", "phase")
	addDetail(r, "Cluster", str(obj, "spec", "cluster", "name"))
	addDetail(r, "Method", firstNonEmpty(str(obj, "status", "method"), str(obj, "spec", "method")))
	started := timestamp(obj, "status", "startedAt").UTC()
	stopped := timestamp(obj, "status", "stoppedAt").UTC()
	if !started.IsZero() {
		addDetail(r, "Started", started.Format(time.RFC3339))
	}
	if !stopped.IsZero() {
		addDetail(r, "Stopped", stopped.Format(time.RFC3339))
		if stopped.After(r.LastChanged) {
			r.LastChanged = stopped
		}
	}
	switch phase {
	case "completed":
		msg := "Completed"
		if !started.IsZero() && !stopped.IsZero() && !stopped.Before(started) {
			msg += " in " + stopped.Sub(started).Round(time.Second).String()
		}
		r.Status, r.Message = model.StatusCompleted, msg
	case "failed", "walArchivingFailing":
		r.Status, r.Message = model.StatusFailed, firstNonEmpty(str(obj, "status", "error"), phase)
	case "":
		r.Status, r.Message = model.StatusReconciling, "Pending"
	default:
		r.Status, r.Message = model.StatusReconciling, strings.ToUpper(phase[:1])+phase[1:]
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// summarizeScheduledBackup: spec.suspend is suspended; otherwise ready
// once the operator has checked the schedule, with the next run as message.
// Whether a run failed shows on its Backup and on the Cluster.
func summarizeScheduledBackup(obj map[string]any, r *model.Resource) {
	r.Source = cnpgClusterRef(obj, r.Namespace)
	r.Schedule = str(obj, "spec", "schedule")
	addDetail(r, "Cluster", str(obj, "spec", "cluster", "name"))
	addDetail(r, "Method", str(obj, "spec", "method"))
	last := timestamp(obj, "status", "lastScheduleTime").UTC()
	next := timestamp(obj, "status", "nextScheduleTime").UTC()
	if !last.IsZero() {
		addDetail(r, "Last schedule", last.Format(time.RFC3339))
	}
	if !next.IsZero() {
		addDetail(r, "Next schedule", next.Format(time.RFC3339))
	}
	switch {
	case boolean(obj, "spec", "suspend"):
		r.Suspended = true
		r.Status, r.Message = model.StatusSuspended, "Suspended"
	case !next.IsZero():
		r.Status, r.Message = model.StatusReady, "Next backup at "+next.Format(time.RFC3339)
	case !timestamp(obj, "status", "lastCheckTime").IsZero():
		r.Status, r.Message = model.StatusReady, "Scheduled"
	default:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the operator to report status"
	}
}

// summarizePooler: spec.pgbouncer.paused is suspended; ready once
// status.instances reaches spec.instances (default 1).
func summarizePooler(obj map[string]any, r *model.Resource) {
	r.Source = cnpgClusterRef(obj, r.Namespace)
	desired, ok := integer(obj, "spec", "instances")
	if !ok {
		desired = 1
	}
	current, reported := integer(obj, "status", "instances")
	r.Replicas = fmt.Sprintf("%d/%d", current, desired)
	addDetail(r, "Cluster", str(obj, "spec", "cluster", "name"))
	addDetail(r, "Type", str(obj, "spec", "type"))
	addDetail(r, "Pool mode", str(obj, "spec", "pgbouncer", "poolMode"))
	addDetail(r, "Instances", r.Replicas)
	switch {
	case boolean(obj, "spec", "pgbouncer", "paused"):
		r.Suspended = true
		r.Status, r.Message = model.StatusSuspended, "Paused"
	case !reported:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the operator to report status"
	case current >= desired:
		r.Status, r.Message = model.StatusReady, r.Replicas+" instances"
	default:
		r.Status, r.Message = model.StatusReconciling, r.Replicas+" instances"
	}
}

// --- RabbitMQ ----------------------------------------------------------------

// summarizeRabbitmqCluster reads the operator's conditions: a False
// ReconcileSuccess or ClusterAvailable is failed, a False AllReplicasReady
// is reconciling, all True is ready (with a False NoWarnings noted).
func summarizeRabbitmqCluster(obj map[string]any, r *model.Resource) {
	desired, ok := integer(obj, "spec", "replicas")
	if !ok {
		desired = 1
	}
	image := str(obj, "spec", "image")
	if image != "" {
		r.Images = []string{image}
	}
	reconciled := condition(r.Conditions, "ReconcileSuccess")
	available := condition(r.Conditions, "ClusterAvailable")
	replicas := condition(r.Conditions, "AllReplicasReady")
	warnings := condition(r.Conditions, "NoWarnings")
	if isTrue(replicas) {
		r.Replicas = fmt.Sprintf("%d/%d", desired, desired)
	}
	addDetail(r, "Replicas", fmt.Sprint(desired))
	addDetail(r, "Image", image)
	addDetail(r, "Service type", str(obj, "spec", "service", "type"))
	addDetail(r, "Storage", scalar(obj, "spec", "persistence", "storage"))
	switch {
	case reconciled == nil && available == nil && replicas == nil:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the operator to report status"
	case isFalse(reconciled):
		r.Status, r.Message = model.StatusFailed, conditionText(reconciled)
	case isFalse(available):
		r.Status, r.Message = model.StatusFailed, conditionText(available)
	case !isTrue(replicas):
		r.Status, r.Message = model.StatusReconciling, firstNonEmpty(conditionText(replicas), "Waiting for replicas")
	case generationPending(obj):
		r.Status, r.Message = model.StatusReconciling, "Waiting for the operator to observe the latest spec"
	default:
		r.Status, r.Message = model.StatusReady, fmt.Sprintf("%d/%d replicas", desired, desired)
		if isFalse(warnings) {
			r.Message += " · " + firstNonEmpty(warnings.Message, warnings.Reason, "warnings")
		}
	}
	r.Message = OneLine(r.Message, maxMessage)
}

// --- Datadog -----------------------------------------------------------------

// ddComponent is the status of one DatadogAgent component: the node agent
// DaemonSet(s) or a Deployment.
type ddComponent struct {
	name         string
	ready, total int64
	state        string
}

// ddComponents reads status.agent (or the sum of status.agentList, one per
// profile), status.clusterAgent and status.clusterChecksRunner.
func ddComponents(obj map[string]any) []ddComponent {
	var out []ddComponent
	agents := maps(obj, "status", "agentList")
	if a := mapping(obj, "status", "agent"); len(agents) == 0 && a != nil {
		agents = []map[string]any{a}
	}
	if len(agents) > 0 {
		c := ddComponent{name: "Node agent"}
		for _, a := range agents {
			ready, _ := integer(a, "ready")
			desired, _ := integer(a, "desired")
			c.ready += ready
			c.total += desired
			if s := str(a, "state"); c.state == "" || s == "Failed" {
				c.state = s
			}
		}
		out = append(out, c)
	}
	for _, d := range []struct{ key, name string }{{"clusterAgent", "Cluster agent"}, {"clusterChecksRunner", "Cluster checks runner"}} {
		m := mapping(obj, "status", d.key)
		if m == nil {
			continue
		}
		ready, _ := integer(m, "readyReplicas")
		total, _ := integer(m, "replicas")
		out = append(out, ddComponent{name: d.name, ready: ready, total: total, state: str(m, "state")})
	}
	return out
}

// summarizeDatadogAgent: a True condition whose type mentions an error, or
// a component in state Failed, is failed; a component with fewer ready
// pods than desired is reconciling; otherwise ready, with ready/desired
// per component. Replicas is the node agent's.
func summarizeDatadogAgent(obj map[string]any, r *model.Resource) {
	addDetail(r, "Site", str(obj, "spec", "global", "site"))
	comps := ddComponents(obj)
	var parts []string
	var failed, pending string
	for _, c := range comps {
		count := fmt.Sprintf("%d/%d", c.ready, c.total)
		addDetail(r, c.name, count)
		if c.name == "Node agent" {
			r.Replicas = count
		}
		parts = append(parts, strings.ToLower(c.name[:1])+c.name[1:]+" "+count)
		switch {
		case c.state == "Failed" && failed == "":
			failed = c.name + " failed"
		case c.ready < c.total && pending == "":
			pending = c.name + " " + count + " ready"
		}
	}
	for _, c := range r.Conditions {
		if isTrue(&c) && strings.Contains(c.Type, "Error") && failed == "" {
			failed = conditionText(&c)
			if failed == "" {
				failed = c.Type
			}
		}
	}
	switch {
	case failed != "":
		r.Status, r.Message = model.StatusFailed, failed
	case len(comps) == 0:
		r.Status, r.Message = model.StatusUnknown, "Waiting for the operator to report status"
	case pending != "":
		r.Status, r.Message = model.StatusReconciling, pending
	default:
		msg := strings.Join(parts, " · ")
		r.Status, r.Message = model.StatusReady, strings.ToUpper(msg[:1])+msg[1:]
	}
	r.Message = OneLine(r.Message, maxMessage)
}
