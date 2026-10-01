package flux

import "strings"

// Project is the upstream project an API group belongs to, for grouping
// kinds in the UI's navigation (for example "karpenter" for karpenter.sh
// and karpenter.k8s.aws).
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Well-known projects, in the order the UI lists them.
var (
	ProjectKubernetes      = Project{ID: "kubernetes", Name: "Kubernetes"}
	ProjectFlux            = Project{ID: "flux", Name: "Flux"}
	ProjectKarpenter       = Project{ID: "karpenter", Name: "Karpenter"}
	ProjectExternalSecrets = Project{ID: "external-secrets", Name: "External Secrets"}
)

// KnownProjects lists the well-known projects in display order.
func KnownProjects() []Project {
	return []Project{ProjectKubernetes, ProjectFlux, ProjectKarpenter, ProjectExternalSecrets}
}

// kubernetesGroups are the built-in API groups (the core group is "").
var kubernetesGroups = map[string]bool{
	GroupCore: true, GroupApps: true, GroupBatch: true, GroupNetworking: true, GroupPolicy: true,
	GroupStorage: true, GroupAutoscale: true,
	"rbac.authorization.k8s.io": true, "apiextensions.k8s.io": true, "apiregistration.k8s.io": true,
	"admissionregistration.k8s.io": true, "scheduling.k8s.io": true, "coordination.k8s.io": true,
	"discovery.k8s.io": true, "node.k8s.io": true, "certificates.k8s.io": true,
	"flowcontrol.apiserver.k8s.io": true, "resource.k8s.io": true,
}

// ProjectOf maps an API group to its project. Built-in groups belong to
// Kubernetes, *.fluxcd.io to Flux, karpenter.sh and karpenter.k8s.aws to
// Karpenter and external-secrets.io (with its *.external-secrets.io
// generator groups) to External Secrets. Any other group is its own
// project, named after the group.
func ProjectOf(group string) Project {
	switch {
	case kubernetesGroups[group]:
		return ProjectKubernetes
	case group == "fluxcd.io" || strings.HasSuffix(group, ".fluxcd.io"):
		return ProjectFlux
	case group == GroupKarpenter || group == GroupKarpenterAWS:
		return ProjectKarpenter
	case group == GroupESO || strings.HasSuffix(group, "."+GroupESO):
		return ProjectExternalSecrets
	}
	return Project{ID: group, Name: group}
}
