package hub

import (
	"slices"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
)

// hasDependsOn reports whether rows of kind k may carry DependsOn and
// Blocked: Kustomizations and HelmReleases.
func hasDependsOn(k flux.Kind) bool {
	return k.Matches(flux.GroupKustomize, flux.KindKustomization) || k.Matches(flux.GroupHelm, flux.KindHelmRelease)
}

// sanitizeDependsOn returns the DependsOn of r (whose kind sanitizeResource
// has already canonicalised) that the hub accepts from an agent: only on
// Kustomizations and HelmReleases, each entry of the row's own kind with a
// valid namespace and name, without duplicates, at most model.MaxDependsOn.
func sanitizeDependsOn(r model.Resource) []model.Ref {
	k, ok := flux.KindByName(r.Kind)
	if !ok || !hasDependsOn(k) || len(r.DependsOn) == 0 {
		return nil
	}
	out := make([]model.Ref, 0, min(len(r.DependsOn), model.MaxDependsOn))
	for _, d := range r.DependsOn {
		if len(out) == model.MaxDependsOn {
			break
		}
		if d.Group != k.Group || d.Kind != k.Kind ||
			len(validation.IsDNS1123Label(d.Namespace)) > 0 || len(validation.IsDNS1123Subdomain(d.Name)) > 0 ||
			slices.Contains(out, d) {
			continue
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// dependencyTuples returns the access tuples of r's DependsOn targets.
func dependencyTuples(r model.Resource) []accessTuple {
	var out []accessTuple
	for _, d := range r.DependsOn {
		if t, ok := tupleOf(d); ok && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// withVisibleDependencies drops the DependsOn entries whose (group,
// resource, namespace) the viewer may not list, so an edge never names an
// object the viewer could not see. A target in a listable namespace is
// kept even when it does not exist: that is a missing dependency, which
// reveals nothing. r's own slice is never modified (rows are shared with
// the view).
func withVisibleDependencies(r model.Resource, allowed map[accessTuple]bool) model.Resource {
	hidden := false
	for _, d := range r.DependsOn {
		if t, ok := tupleOf(d); !ok || !allowed[t] {
			hidden = true
			break
		}
	}
	if !hidden {
		return r
	}
	var keep []model.Ref
	for _, d := range r.DependsOn {
		if t, ok := tupleOf(d); ok && allowed[t] {
			keep = append(keep, d)
		}
	}
	r.DependsOn = keep
	return r
}
