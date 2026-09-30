package flux

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
)

// Served maps a Kind name to the API version the cluster serves for it.
// Kinds the cluster does not serve (for example Flux kinds when Flux is not
// installed) are absent.
type Served map[string]string

// GVR returns the served GroupVersionResource for kind.
func (s Served) GVR(kind string) (schema.GroupVersionResource, bool) {
	k, ok := KindByName(kind)
	if !ok {
		return schema.GroupVersionResource{}, false
	}
	v, ok := s[k.Kind]
	if !ok {
		return schema.GroupVersionResource{}, false
	}
	return k.GVR(v), true
}

// Watches reports whether (group, kind) is a surfaced kind the cluster
// serves, so the agent already summarises its objects. It is the Watched
// function for InventoryOnly.
func (s Served) Watches(group, kind string) bool {
	k, ok := KindByName(kind)
	if !ok || !k.Surfaced || !k.Matches(group, kind) {
		return false
	}
	_, ok = s[k.Kind]
	return ok
}

// Discover picks, for every kind in the table, the most preferred version in
// Kind.Versions that the cluster serves and that lists the kind's resource.
func Discover(dc discovery.DiscoveryInterface) (Served, error) {
	groups, err := dc.ServerGroups()
	if err != nil {
		return nil, fmt.Errorf("flux: discover API groups: %w", err)
	}
	servedGV := map[string]bool{}
	for _, g := range groups.Groups {
		for _, v := range g.Versions {
			servedGV[v.GroupVersion] = true
		}
	}
	resources := map[string][]string{} // group/version -> resource names
	served := Served{}
	for _, k := range kinds {
		for _, v := range k.Versions {
			gv := schema.GroupVersion{Group: k.Group, Version: v}.String()
			if !servedGV[gv] {
				continue
			}
			names, ok := resources[gv]
			if !ok {
				list, err := dc.ServerResourcesForGroupVersion(gv)
				if apierrors.IsNotFound(err) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("flux: discover resources of %s: %w", gv, err)
				}
				for _, r := range list.APIResources {
					names = append(names, r.Name)
				}
				resources[gv] = names
			}
			if slices.Contains(names, k.Plural) {
				served[k.Kind] = v
				break
			}
		}
	}
	return served, nil
}

// FluxNamespace is where Flux is conventionally installed.
const FluxNamespace = "flux-system"

// DetectFluxVersion returns the installed Flux version, best effort. It reads
// the app.kubernetes.io/version label that `flux install` puts on the Flux
// namespace, then on the source-controller Deployment, and finally falls back
// to the source-controller image tag (a controller version, not the Flux
// distribution version). It returns "" when nothing is readable.
func DetectFluxVersion(ctx context.Context, kube kubernetes.Interface, namespace string) string {
	const label = "app.kubernetes.io/version"
	if ns, err := kube.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{}); err == nil {
		if v := ns.Labels[label]; v != "" {
			return v
		}
	}
	d, err := kube.AppsV1().Deployments(namespace).Get(ctx, "source-controller", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	if v := d.Labels[label]; v != "" {
		return v
	}
	for _, c := range d.Spec.Template.Spec.Containers {
		if c.Name == "manager" || len(d.Spec.Template.Spec.Containers) == 1 {
			return imageTag(c.Image)
		}
	}
	return ""
}

// imageTag returns the tag of an image reference, ignoring any digest.
func imageTag(image string) string {
	image, _, _ = strings.Cut(image, "@")
	slash := strings.LastIndex(image, "/")
	if i := strings.LastIndex(image, ":"); i > slash {
		return image[i+1:]
	}
	return ""
}
