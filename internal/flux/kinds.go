// Package flux knows which Kubernetes kinds Eddy watches, how to find their
// served versions, and how to turn an object into a model.Resource summary.
//
// It deliberately imports no Flux API modules: every object is read as
// unstructured data, so Eddy follows Flux releases without a rebuild.
//
// The hub and the agent share the kind table in this file. The hub maps the
// {kind} path segment of its API to a Kind with KindByName and uses
// Kind.Group and Kind.Plural for SubjectAccessReview checks.
package flux

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// API groups of the supported kinds.
const (
	GroupKustomize  = "kustomize.toolkit.fluxcd.io"
	GroupHelm       = "helm.toolkit.fluxcd.io"
	GroupSource     = "source.toolkit.fluxcd.io"
	GroupApps       = "apps"
	GroupCore       = ""
	GroupBatch      = "batch"
	GroupNetworking = "networking.k8s.io"
	GroupAutoscale  = "autoscaling"
	GroupPolicy     = "policy"
	GroupStorage    = "storage.k8s.io"
	// Groups of the watch presets.
	GroupKarpenter    = "karpenter.sh"
	GroupKarpenterAWS = "karpenter.k8s.aws"
	GroupESO          = "external-secrets.io"
)

// Names of the supported kinds.
const (
	KindKustomization  = "Kustomization"
	KindHelmRelease    = "HelmRelease"
	KindGitRepository  = "GitRepository"
	KindOCIRepository  = "OCIRepository"
	KindHelmRepository = "HelmRepository"
	KindHelmChart      = "HelmChart"
	KindBucket         = "Bucket"
	KindDeployment     = "Deployment"
	KindStatefulSet    = "StatefulSet"
	KindDaemonSet      = "DaemonSet"
	KindReplicaSet     = "ReplicaSet"
	KindPod            = "Pod"
	KindService        = "Service"
	KindIngress        = "Ingress"
	KindJob            = "Job"
	KindCronJob        = "CronJob"
	KindHPA            = "HorizontalPodAutoscaler"
	KindPVC            = "PersistentVolumeClaim"
	KindNamespace      = "Namespace"
	KindStorageClass   = "StorageClass"
	KindPDB            = "PodDisruptionBudget"
	KindServiceAccount = "ServiceAccount"
	KindNetworkPolicy  = "NetworkPolicy"

	// Kinds of the karpenter preset.
	KindNodePool     = "NodePool"
	KindNodeClaim    = "NodeClaim"
	KindEC2NodeClass = "EC2NodeClass"

	// Kinds of the externalSecrets preset.
	KindExternalSecret        = "ExternalSecret"
	KindClusterExternalSecret = "ClusterExternalSecret"
	KindSecretStore           = "SecretStore"
	KindClusterSecretStore    = "ClusterSecretStore"
	KindPushSecret            = "PushSecret"

	// Kinds Eddy never watches. Secrets and ConfigMaps appear only by name,
	// from a Kustomization's inventory.
	KindSecret    = "Secret"
	KindConfigMap = "ConfigMap"
)

// Watch presets: opt-in groups of kinds (EDDY_WATCH_PRESETS, the chart's
// watch.presets). A preset kind is watched only when its preset is enabled
// and the cluster serves it.
const (
	PresetKarpenter       = "karpenter"
	PresetExternalSecrets = "externalSecrets"
)

// Presets lists every supported preset.
func Presets() []string { return []string{PresetKarpenter, PresetExternalSecrets} }

// ParsePresets validates preset names (exact spelling), dropping empty
// entries and duplicates, in the order of Presets.
func ParsePresets(names []string) ([]string, error) {
	want := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if !slices.Contains(Presets(), n) {
			return nil, fmt.Errorf("flux: unknown watch preset %q (supported: %s)", n, strings.Join(Presets(), ", "))
		}
		want[n] = true
	}
	var out []string
	for _, p := range Presets() {
		if want[p] {
			out = append(out, p)
		}
	}
	return out, nil
}

// Kind describes one watched Kubernetes kind.
type Kind struct {
	Group string
	Kind  string
	// Plural is the API resource name, as used in RBAC rules and SubjectAccessReviews.
	Plural string
	// Versions lists the API versions Eddy understands, most preferred first.
	Versions []string
	// Flux reports whether this is a Flux toolkit kind (reconcile, suspend and resume apply).
	Flux       bool
	Namespaced bool
	// Surfaced reports whether resources of this kind are sent to the hub.
	// ReplicaSets are watched only to attribute Pods to their Deployment:
	// surfacing them would add one row per old rollout revision.
	Surfaced bool
	// Preset names the watch preset that enables this kind; empty for kinds
	// that are always watched.
	Preset string
}

var kinds = []Kind{
	{Group: GroupKustomize, Kind: KindKustomization, Plural: "kustomizations", Versions: []string{"v1", "v1beta2", "v1beta1"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupHelm, Kind: KindHelmRelease, Plural: "helmreleases", Versions: []string{"v2", "v2beta2", "v2beta1"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupSource, Kind: KindGitRepository, Plural: "gitrepositories", Versions: []string{"v1", "v1beta2", "v1beta1"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupSource, Kind: KindOCIRepository, Plural: "ocirepositories", Versions: []string{"v1", "v1beta2"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupSource, Kind: KindHelmRepository, Plural: "helmrepositories", Versions: []string{"v1", "v1beta2", "v1beta1"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupSource, Kind: KindHelmChart, Plural: "helmcharts", Versions: []string{"v1", "v1beta2", "v1beta1"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupSource, Kind: KindBucket, Plural: "buckets", Versions: []string{"v1", "v1beta2", "v1beta1"}, Flux: true, Namespaced: true, Surfaced: true},
	{Group: GroupApps, Kind: KindDeployment, Plural: "deployments", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupApps, Kind: KindStatefulSet, Plural: "statefulsets", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupApps, Kind: KindDaemonSet, Plural: "daemonsets", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupApps, Kind: KindReplicaSet, Plural: "replicasets", Versions: []string{"v1"}, Namespaced: true},
	{Group: GroupCore, Kind: KindPod, Plural: "pods", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupBatch, Kind: KindJob, Plural: "jobs", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupBatch, Kind: KindCronJob, Plural: "cronjobs", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupAutoscale, Kind: KindHPA, Plural: "horizontalpodautoscalers", Versions: []string{"v2"}, Namespaced: true, Surfaced: true},
	{Group: GroupCore, Kind: KindService, Plural: "services", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupNetworking, Kind: KindIngress, Plural: "ingresses", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupCore, Kind: KindPVC, Plural: "persistentvolumeclaims", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupCore, Kind: KindNamespace, Plural: "namespaces", Versions: []string{"v1"}, Surfaced: true},
	{Group: GroupCore, Kind: KindServiceAccount, Plural: "serviceaccounts", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupStorage, Kind: KindStorageClass, Plural: "storageclasses", Versions: []string{"v1"}, Surfaced: true},
	{Group: GroupPolicy, Kind: KindPDB, Plural: "poddisruptionbudgets", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},
	{Group: GroupNetworking, Kind: KindNetworkPolicy, Plural: "networkpolicies", Versions: []string{"v1"}, Namespaced: true, Surfaced: true},

	{Group: GroupKarpenter, Kind: KindNodePool, Plural: "nodepools", Versions: []string{"v1", "v1beta1"}, Surfaced: true, Preset: PresetKarpenter},
	{Group: GroupKarpenter, Kind: KindNodeClaim, Plural: "nodeclaims", Versions: []string{"v1", "v1beta1"}, Surfaced: true, Preset: PresetKarpenter},
	{Group: GroupKarpenterAWS, Kind: KindEC2NodeClass, Plural: "ec2nodeclasses", Versions: []string{"v1", "v1beta1"}, Surfaced: true, Preset: PresetKarpenter},

	{Group: GroupESO, Kind: KindExternalSecret, Plural: "externalsecrets", Versions: []string{"v1", "v1beta1"}, Namespaced: true, Surfaced: true, Preset: PresetExternalSecrets},
	{Group: GroupESO, Kind: KindClusterExternalSecret, Plural: "clusterexternalsecrets", Versions: []string{"v1", "v1beta1"}, Surfaced: true, Preset: PresetExternalSecrets},
	{Group: GroupESO, Kind: KindSecretStore, Plural: "secretstores", Versions: []string{"v1", "v1beta1"}, Namespaced: true, Surfaced: true, Preset: PresetExternalSecrets},
	{Group: GroupESO, Kind: KindClusterSecretStore, Plural: "clustersecretstores", Versions: []string{"v1", "v1beta1"}, Surfaced: true, Preset: PresetExternalSecrets},
	{Group: GroupESO, Kind: KindPushSecret, Plural: "pushsecrets", Versions: []string{"v1alpha1"}, Namespaced: true, Surfaced: true, Preset: PresetExternalSecrets},
}

func (k Kind) clone() Kind {
	k.Versions = slices.Clone(k.Versions)
	return k
}

// All returns a copy of the kind table, Flux kinds first.
func All() []Kind {
	out := make([]Kind, len(kinds))
	for i, k := range kinds {
		out[i] = k.clone()
	}
	return out
}

// KindByName looks a kind up by its Kind name (for example "Kustomization").
// The match ignores case, so API paths may use "kustomization"; callers should
// use the returned Kind.Kind as the canonical spelling. Kind names are unique
// across the table, so the name alone identifies the API group.
//
// This function is part of the hub contract: it maps API path segments to
// (Group, Plural) for SubjectAccessReviews. Only kinds in the table are known;
// Secrets and ConfigMaps never are.
func KindByName(kind string) (Kind, bool) {
	for _, k := range kinds {
		if strings.EqualFold(k.Kind, kind) {
			return k.clone(), true
		}
	}
	return Kind{}, false
}

// GVRFor returns the GroupVersionResource for a kind at the given served
// version. It reports false for an unknown kind or an empty version.
func GVRFor(kind, servedVersion string) (schema.GroupVersionResource, bool) {
	k, ok := KindByName(kind)
	if !ok || servedVersion == "" {
		return schema.GroupVersionResource{}, false
	}
	return k.GVR(servedVersion), true
}

// GVR returns the GroupVersionResource of k at version.
func (k Kind) GVR(version string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: k.Group, Version: version, Resource: k.Plural}
}

// Matches reports whether k has the given API group and Kind name (exact case).
func (k Kind) Matches(group, kind string) bool {
	return k.Group == group && k.Kind == kind
}

// YAMLAllowed reports whether the yaml operation may return objects of
// kind. It is false only for Secrets (a kind named "Secret" in any API
// group, compared without case): their YAML is never read. Every other kind,
// watched or known only from a Kustomization inventory, is allowed, and
// SanitizeYAML always removes data, binaryData and stringData (ConfigMaps
// keep their metadata only) and env values before anything leaves the
// cluster. Whether the user may read the object is decided by the API
// server: the read is impersonated.
func YAMLAllowed(kind string) bool {
	return !strings.EqualFold(kind, KindSecret)
}
