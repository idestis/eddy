package flux

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/idestis/eddy/internal/model"
)

// ParseInventoryID parses a Kustomization status.inventory.entries[].id,
// which Flux formats as "<namespace>_<name>_<group>_<kind>". The namespace is
// empty for cluster-scoped objects and the group is empty for the core API.
//
// Namespaces and API groups are DNS names and Kinds are identifiers, so none
// of them contains "_". Some object names (RBAC roles, for example) may, so
// everything between the first and the last two separators is the name.
func ParseInventoryID(id string) (model.Ref, error) {
	p := strings.Split(id, "_")
	if len(p) < 4 {
		return model.Ref{}, fmt.Errorf("flux: invalid inventory id %q", id)
	}
	ref := model.Ref{
		Namespace: p[0],
		Name:      strings.Join(p[1:len(p)-2], "_"),
		Group:     p[len(p)-2],
		Kind:      p[len(p)-1],
	}
	if ref.Name == "" || ref.Kind == "" {
		return model.Ref{}, fmt.Errorf("flux: invalid inventory id %q", id)
	}
	return ref, nil
}

// inventoryPlurals maps kinds that Eddy does not watch, but that commonly
// appear in a Kustomization's inventory, to their API resource names. The
// hub uses it to filter inventory-only rows by the user's own list
// permission on the kind; a kind missing here is visible to whoever can
// see the Kustomization that applied it.
var inventoryPlurals = map[[2]string]string{
	{GroupCore, "ConfigMap"}:                                           "configmaps",
	{GroupCore, "Secret"}:                                              "secrets",
	{GroupCore, "Endpoints"}:                                           "endpoints",
	{GroupCore, "ResourceQuota"}:                                       "resourcequotas",
	{GroupCore, "LimitRange"}:                                          "limitranges",
	{GroupCore, "PersistentVolume"}:                                    "persistentvolumes",
	{GroupCore, "ReplicationController"}:                               "replicationcontrollers",
	{"rbac.authorization.k8s.io", "Role"}:                              "roles",
	{"rbac.authorization.k8s.io", "RoleBinding"}:                       "rolebindings",
	{"rbac.authorization.k8s.io", "ClusterRole"}:                       "clusterroles",
	{"rbac.authorization.k8s.io", "ClusterRoleBinding"}:                "clusterrolebindings",
	{"apiextensions.k8s.io", "CustomResourceDefinition"}:               "customresourcedefinitions",
	{"apiregistration.k8s.io", "APIService"}:                           "apiservices",
	{GroupNetworking, "IngressClass"}:                                  "ingressclasses",
	{"scheduling.k8s.io", "PriorityClass"}:                             "priorityclasses",
	{"admissionregistration.k8s.io", "MutatingWebhookConfiguration"}:   "mutatingwebhookconfigurations",
	{"admissionregistration.k8s.io", "ValidatingWebhookConfiguration"}: "validatingwebhookconfigurations",
	{"coordination.k8s.io", "Lease"}:                                   "leases",
	{"discovery.k8s.io", "EndpointSlice"}:                              "endpointslices",
	{"image.toolkit.fluxcd.io", "ImageRepository"}:                     "imagerepositories",
	{"image.toolkit.fluxcd.io", "ImagePolicy"}:                         "imagepolicies",
	{"image.toolkit.fluxcd.io", "ImageUpdateAutomation"}:               "imageupdateautomations",
	{"notification.toolkit.fluxcd.io", "Alert"}:                        "alerts",
	{"notification.toolkit.fluxcd.io", "Provider"}:                     "providers",
	{"notification.toolkit.fluxcd.io", "Receiver"}:                     "receivers",
}

// InventoryPlural returns the API resource name of an unwatched kind that
// may appear as an inventory-only row, when Eddy knows it. Kinds in the
// watched table resolve through KindByName instead.
func InventoryPlural(group, kind string) (string, bool) {
	p, ok := inventoryPlurals[[2]string{group, kind}]
	return p, ok
}

// InventoryKind reports whether kind names a kind in the inventory plural
// table (in any group), for list filters on inventory-only rows. It matches
// case-insensitively and returns the canonical spelling.
func InventoryKind(kind string) (string, bool) {
	for k := range inventoryPlurals {
		if strings.EqualFold(k[1], kind) {
			return k[1], true
		}
	}
	return "", false
}

// Bounds on inventory-only rows, against a runaway inventory.
const (
	maxInventoryRows = 5000
	maxInventoryName = 253
	maxInventoryKind = 63
)

// Watched reports whether the agent watches (group, kind) itself: such
// inventory entries are already summaries of their own and are not
// repeated as inventory-only rows.
type Watched func(group, kind string) bool

// InventoryOnly returns a summary for every entry in a Kustomization's
// status.inventory whose kind is not watched: Group, Kind, Namespace, Name
// and the entry's version, Status "unknown", InventoryOnly set and Owner
// pointing at the Kustomization. It reads nothing but the inventory, so the
// objects themselves (Secrets and ConfigMaps included) are never fetched.
//
// HelmReleases have no inventory in their status; objects a HelmRelease
// applies appear only when their kind is watched (through the
// helm.toolkit.fluxcd.io labels).
func InventoryOnly(ks *unstructured.Unstructured, watched Watched) []model.Resource {
	if ks.GetKind() != KindKustomization || ks.GroupVersionKind().Group != GroupKustomize {
		return nil
	}
	owner := model.Ref{Group: GroupKustomize, Kind: KindKustomization, Namespace: ks.GetNamespace(), Name: ks.GetName()}
	var out []model.Resource
	seen := map[string]bool{}
	for _, e := range maps(ks.Object, "status", "inventory", "entries") {
		if len(out) == maxInventoryRows {
			break
		}
		ref, err := ParseInventoryID(str(e, "id"))
		if err != nil || len(ref.Name) > maxInventoryName || len(ref.Kind) > maxInventoryKind || !validKindName(ref.Kind) {
			continue
		}
		if watched != nil && watched(ref.Group, ref.Kind) {
			continue
		}
		id := ref.ID()
		if seen[id] {
			continue
		}
		seen[id] = true
		o := owner
		out = append(out, model.Resource{
			Ref:           ref,
			ID:            id,
			Version:       OneLine(str(e, "v"), 32),
			Status:        model.StatusUnknown,
			InventoryOnly: true,
			Owner:         &o,
			Project:       ProjectOf(ref.Group).ID,
		})
	}
	return out
}

// validKindName reports whether s looks like a Kind: letters and digits,
// starting with an upper-case letter.
func validKindName(s string) bool {
	if s == "" || s[0] < 'A' || s[0] > 'Z' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

// ValidKindName is validKindName for other packages (the hub validates
// inventory-only rows from agents with it).
func ValidKindName(s string) bool { return len(s) <= maxInventoryKind && validKindName(s) }
