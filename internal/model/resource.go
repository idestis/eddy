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
)

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
	// InventoryOnly marks an object known only from a Kustomization's
	// status.inventory, whose kind Eddy does not watch (ConfigMap, Secret,
	// ServiceAccount, RBAC, CRDs…). Such a summary carries the Ref, Version,
	// Owner (the Kustomization) and Status "unknown", and nothing else: the
	// agent never reads the object itself.
	InventoryOnly bool `json:"inventoryOnly,omitempty"`
	// Labels holds a small allowlisted subset of labels (app.kubernetes.io/*, Flux ownership).
	Labels map[string]string `json:"labels,omitempty"`

	CreatedAt   time.Time `json:"createdAt,omitzero"`
	LastChanged time.Time `json:"lastChanged,omitzero"`
	// ResourceVersion is the Kubernetes resourceVersion, used to order deltas.
	ResourceVersion string `json:"resourceVersion"`
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
	Name              string    `json:"name"`
	DisplayName       string    `json:"displayName"`
	Environment       string    `json:"environment,omitempty"`
	Region            string    `json:"region,omitempty"`
	Color             string    `json:"color,omitempty"`
	Protected         bool      `json:"protected"`
	Order             int       `json:"order"`
	Connected         bool      `json:"connected"`
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
	// Counts are filtered to what the viewer may list.
	Counts map[Status]int `json:"counts,omitempty"`
}
