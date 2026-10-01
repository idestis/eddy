// Package model defines the resource summary shared by the agent, the hub and
// the UI. It is the only shape of cluster data that ever leaves a cluster:
// no Secret or ConfigMap data, no managedFields, no raw objects.
package model

import (
	"fmt"
	"strings"
	"time"
)

// Status is the normalised health of a resource.
type Status string

const (
	StatusReady       Status = "ready"
	StatusFailed      Status = "failed"
	StatusReconciling Status = "reconciling"
	StatusSuspended   Status = "suspended"
	StatusUnknown     Status = "unknown"
	// StatusCompleted is a run-to-completion object that finished
	// successfully: a Job with the Complete condition or a Pod in phase
	// Succeeded. Nothing is running and nothing needs attention.
	StatusCompleted Status = "completed"
)

// Statuses lists every Status value, in the order the UI and filters use.
var Statuses = []Status{StatusReady, StatusFailed, StatusReconciling, StatusSuspended, StatusUnknown, StatusCompleted}

// Ref identifies an object inside one cluster.
type Ref struct {
	Group     string `json:"group"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ID returns the stable id used across the protocol and the UI:
// "<group>/<Kind>/<namespace>/<name>", with an empty namespace for
// cluster-scoped objects.
func (r Ref) ID() string {
	return r.Group + "/" + r.Kind + "/" + r.Namespace + "/" + r.Name
}

func (r Ref) String() string { return r.ID() }

// ParseRef is the inverse of Ref.ID.
func ParseRef(id string) (Ref, error) {
	p := strings.Split(id, "/")
	if len(p) != 4 || p[1] == "" || p[3] == "" {
		return Ref{}, fmt.Errorf("model: invalid resource id %q", id)
	}
	return Ref{Group: p[0], Kind: p[1], Namespace: p[2], Name: p[3]}, nil
}

// Condition is a trimmed metav1.Condition.
type Condition struct {
	Type               string    `json:"type"`
	Status             string    `json:"status"`
	Reason             string    `json:"reason,omitempty"`
	Message            string    `json:"message,omitempty"`
	LastTransitionTime time.Time `json:"lastTransitionTime,omitzero"`
}

// Resource is the summary of one Flux object or workload.
type Resource struct {
	Ref
	// ID is Ref.ID(), denormalised for the UI.
	ID      string `json:"id"`
	Version string `json:"version"` // served API version, e.g. "v1"
	Status  Status `json:"status"`
	// Message is a one-line human explanation of Status.
	Message    string      `json:"message,omitempty"`
	Suspended  bool        `json:"suspended,omitempty"`
	Conditions []Condition `json:"conditions,omitempty"`
	// Revision is the last applied or fetched revision (Flux kinds).
	Revision string `json:"revision,omitempty"`
	// Source is the sourceRef (or chart source) of a Kustomization or HelmRelease.
	Source *Ref `json:"source,omitempty"`
	// Owner is the Flux object or workload that manages this one, if known.
	Owner *Ref `json:"owner,omitempty"`
	// DependsOn lists the spec.dependsOn of a Kustomization (Kustomizations)
	// or a HelmRelease (HelmReleases), in spec order, at most MaxDependsOn.
	// A namespace left out in the spec is the object's own. The hub drops
	// entries in namespaces the viewer may not list.
	DependsOn []Ref `json:"dependsOn,omitempty"`
	// Blocked marks a Kustomization or HelmRelease that is waiting for a
	// dependency (Ready=False with reason DependencyNotReady). Its Status is
	// then "reconciling", not "failed".
	Blocked bool `json:"blocked,omitempty"`
	// Replicas is "ready/desired" for workloads.
	Replicas string `json:"replicas,omitempty"`
	// Images lists container images for workloads and pods.
	Images []string `json:"images,omitempty"`
	// Containers lists a Pod's container names (not init containers), for log selection.
	Containers []string `json:"containers,omitempty"`
	// Interval is the reconcile interval for Flux kinds.
	Interval string `json:"interval,omitempty"`
	// URL is the source URL for Git, OCI, Helm and Bucket sources.
	URL string `json:"url,omitempty"`
	// Chart is "name@version" for HelmReleases and HelmCharts.
	Chart string `json:"chart,omitempty"`
	// Inventory counts entries in a Kustomization's status.inventory.
	Inventory int `json:"inventory,omitempty"`
	// Ports lists a Service's ports as "<port>/<protocol>", with " → <targetPort>"
	// when the target differs, e.g. "80/TCP → 8080".
	Ports []string `json:"ports,omitempty"`
	// Hosts lists an Ingress's rule hosts, without duplicates.
	Hosts []string `json:"hosts,omitempty"`
	// Schedule is a CronJob's cron schedule, e.g. "0 2 * * *".
	Schedule string `json:"schedule,omitempty"`
	// Completions is a Job's "succeeded/completions", e.g. "1/1". Replicas
	// is empty once a Job has finished, so no live pods are implied.
	Completions string `json:"completions,omitempty"`
	// InventoryOnly marks an object known only from a Kustomization's
	// status.inventory, whose kind Eddy does not watch (ConfigMap, Secret,
	// ServiceAccount, RBAC, CRDs…). Such a summary carries the Ref, Version,
	// Owner (the Kustomization) and Status "unknown", and nothing else: the
	// agent never reads the object itself.
	InventoryOnly bool `json:"inventoryOnly,omitempty"`
	// Labels holds a small allowlisted subset of labels (app.kubernetes.io/*,
	// Flux ownership, and a few per kind such as Karpenter's instance and
	// capacity type on NodeClaims or Pod Security levels on Namespaces).
	Labels map[string]string `json:"labels,omitempty"`
	// Project groups the kind for navigation: "kubernetes", "flux",
	// "karpenter", "external-secrets", or the API group itself for any
	// other group (flux.ProjectOf). The hub sets it on every row, inventory
	// rows included.
	Project string `json:"project,omitempty"`
	// Details are kind-specific facts in display order, e.g. a StorageClass
	// provisioner or an ExternalSecret's target Secret name. At most
	// MaxDetails, each value one line.
	Details []Detail `json:"details,omitempty"`

	CreatedAt   time.Time `json:"createdAt,omitzero"`
	LastChanged time.Time `json:"lastChanged,omitzero"`
	// ResourceVersion is the Kubernetes resourceVersion, used to order deltas.
	ResourceVersion string `json:"resourceVersion"`
}

// Detail is one labelled fact in Resource.Details.
type Detail struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// MaxDetails caps Resource.Details.
const MaxDetails = 12

// MaxDependsOn caps Resource.DependsOn.
const MaxDependsOn = 32

// Finding is something the agent noticed about a cluster that is not the
// status of one resource, such as finished Jobs piling up in a namespace.
// Findings travel beside the resources (protocol.FindingSet) and are shown
// on the cluster, never as resource rows.
type Finding struct {
	// ID is "<kind>/<namespace>", unique within a cluster.
	ID   string      `json:"id"`
	Kind FindingKind `json:"kind"`
	// Severity "warning" needs attention; "info" is context only.
	Severity  Severity `json:"severity"`
	Namespace string   `json:"namespace"`
	// Message is one line for lists; Recommendation says how to fix it.
	Message        string `json:"message"`
	Recommendation string `json:"recommendation,omitempty"`
	// Jobs is set for FindingJobBuildup.
	Jobs *JobBuildup `json:"jobs,omitempty"`
}

// FindingKind names a kind of Finding.
type FindingKind string

// FindingJobBuildup: finished Jobs in a namespace that the agent does not
// list one by one. There is one per namespace with hidden Jobs; it is a
// warning once more than JobBuildup.Threshold are hidden, info otherwise.
const FindingJobBuildup FindingKind = "job-buildup"

// Severity of a Finding.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// JobBuildup describes the finished Jobs of one namespace. The agent lists
// active Jobs, recent failures and the newest few finished Jobs of each group
// one by one and hides the rest (GET …/resources?kind=Job&includeHidden=1
// returns them). Counts cover every finished Job in the namespace, listed
// or hidden, unless they say "hidden".
type JobBuildup struct {
	// Hidden is how many finished Jobs are not listed one by one.
	Hidden int `json:"hidden"`
	// Finished counts every finished Job; Succeeded and Failed split it.
	Finished  int `json:"finished"`
	Succeeded int `json:"succeeded"`
	Failed    int `json:"failed"`
	// WithoutTTL counts finished Jobs without spec.ttlSecondsAfterFinished:
	// nothing deletes them unless an owner (a CronJob's history limits) does.
	WithoutTTL int `json:"withoutTTL"`
	// StandaloneFailed counts failed finished Jobs with no owner and no TTL,
	// which Kubernetes never garbage-collects.
	StandaloneFailed int `json:"standaloneFailed"`
	// Oldest and Newest are finish times of the oldest and newest finished Job.
	Oldest time.Time `json:"oldest,omitzero"`
	Newest time.Time `json:"newest,omitzero"`
	// Groups are the largest groups of finished Jobs, biggest first (at most
	// five). A group is a controlling owner, a well-known grouping label, a
	// generateName, a name prefix or the namespace itself.
	Groups []JobGroup `json:"groups,omitempty"`
	// Threshold is the agent's EDDY_JOB_BUILDUP_THRESHOLD: the finding is a
	// warning when Hidden exceeds it.
	Threshold int `json:"threshold"`
}

// JobGroup is one group of finished Jobs in a JobBuildup.
type JobGroup struct {
	// By says how the group was found: "owner", "label", "generateName",
	// "prefix" or "namespace".
	By string `json:"by"`
	// Name is the owner, label value, generateName or prefix ("" for the
	// namespace group); Label is a human description, e.g.
	// "prefect deployment nightly-sync" or "CronJob backup".
	Name       string `json:"name"`
	Label      string `json:"label"`
	Count      int    `json:"count"`
	Failed     int    `json:"failed,omitempty"`
	WithoutTTL int    `json:"withoutTTL,omitempty"`
	// Owner is the controlling owner when By is "owner".
	Owner *Ref `json:"owner,omitempty"`
}

// Event is a trimmed core/v1 Event about a resource.
type Event struct {
	Type    string    `json:"type"` // Normal | Warning
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Count   int32     `json:"count"`
	Source  string    `json:"source,omitempty"`
	First   time.Time `json:"first,omitzero"`
	Last    time.Time `json:"last,omitzero"`
}

// ClusterInfo is what the hub knows about a cluster; no credentials.
type ClusterInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Environment string `json:"environment,omitempty"`
	Region      string `json:"region,omitempty"`
	Color       string `json:"color,omitempty"`
	Protected   bool   `json:"protected"`
	Order       int    `json:"order"`
	Connected   bool   `json:"connected"`
	// Stale is set while the cluster is disconnected and the hub still
	// serves its last view (ADR-0006): reads return that view marked
	// stale, as of LastSeen; writes and live reads are refused.
	Stale             bool      `json:"stale,omitempty"`
	LastSeen          time.Time `json:"lastSeen,omitzero"`
	AgentVersion      string    `json:"agentVersion,omitempty"`
	KubernetesVersion string    `json:"kubernetesVersion,omitempty"`
	FluxVersion       string    `json:"fluxVersion,omitempty"`
	// Mode, ReadOnly and Context come from the connected agent's Hello. Mode
	// is "local" when the agent runs in local mode (dev builds only) and acts
	// as the developer's kubeconfig identity for Context; ReadOnly means the
	// agent refuses reconcile, suspend and resume.
	Mode     string `json:"mode,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
	Context  string `json:"context,omitempty"`
	// Presets are the watch presets the connected agent enables (for
	// example "karpenter", "externalSecrets").
	Presets []string `json:"presets,omitempty"`
	// Counts are filtered to what the viewer may list.
	Counts map[Status]int `json:"counts,omitempty"`
	// Kinds are Counts per Kind (watched kinds only; inventory-only rows
	// are not counted), filtered the same way: enough for navigation
	// without loading the cluster's resources.
	Kinds map[string]map[Status]int `json:"kinds,omitempty"`
	// Findings are filtered the same way: a job-buildup finding is shown to
	// whoever may list Jobs in its namespace.
	Findings []Finding `json:"findings,omitempty"`
}
