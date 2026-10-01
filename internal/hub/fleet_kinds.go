package hub

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
)

// kindInfo is one entry of GET /api/v1/clusters/{c}/kinds.
type kindInfo struct {
	Group string `json:"group"`
	Kind  string `json:"kind"`
	// Plural is the API resource name, when Eddy knows it.
	Plural     string `json:"plural,omitempty"`
	Namespaced bool   `json:"namespaced"`
	Project    string `json:"project"`
	// Watched: the agent summarises objects of this kind itself. Otherwise
	// the kind appears only as inventory-only rows.
	Watched bool `json:"watched"`
	// Preset is the watch preset that enables the kind, if any.
	Preset string `json:"preset,omitempty"`
	// Count is the number of rows of the kind the user may see.
	Count int `json:"count"`
}

// kindsResponse is the body of GET /api/v1/clusters/{c}/kinds.
type kindsResponse struct {
	Items []kindInfo `json:"items"`
	// Projects lists the projects of Items, in display order.
	Projects []flux.Project `json:"projects"`
	// Presets are the watch presets the cluster's agent enables.
	Presets []string `json:"presets"`
	// Stale is set when the cluster is disconnected and these are the
	// counts of its last view (ADR-0006).
	Stale bool `json:"stale,omitempty"`
}

// Kinds lists the kinds of a cluster's view for navigation: every kind the
// agent watches (count 0 included) and every other kind with at least one
// row p may see. Counts are RBAC-filtered like the resource list.
func (f *fleetService) Kinds(ctx context.Context, p identity.Principal, cluster string) (kindsResponse, error) {
	if err := f.validPrincipal(p); err != nil {
		return kindsResponse{}, err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return kindsResponse{}, err
	}
	all, _ := s.view()
	hello := s.hello()

	type key struct{ group, kind string }
	infos := map[key]*kindInfo{}
	get := func(group, kind string) *kindInfo {
		k := key{group, kind}
		if ki, ok := infos[k]; ok {
			return ki
		}
		ki := &kindInfo{Group: group, Kind: kind, Project: flux.ProjectOf(group).ID}
		if t, ok := flux.KindByName(kind); ok && t.Matches(group, kind) {
			ki.Plural, ki.Namespaced, ki.Preset = t.Plural, t.Namespaced, t.Preset
		} else if plural, ok := flux.InventoryPlural(group, kind); ok {
			ki.Plural = plural
		}
		infos[k] = ki
		return ki
	}

	watched := map[key]bool{}
	if hello.Kinds != nil {
		for _, gk := range hello.Kinds {
			if group, kind, ok := strings.Cut(gk, "/"); ok {
				watched[key{group, kind}] = true
			}
		}
	} else {
		// Older agents do not list their kinds: a kind is watched when the
		// view holds a summary of it.
		for _, r := range all {
			if !r.InventoryOnly {
				watched[key{r.Group, r.Kind}] = true
			}
		}
	}
	for k := range watched {
		get(k.group, k.kind).Watched = true
	}

	visible, err := f.authz.filter(ctx, p, cluster, all)
	if err != nil {
		return kindsResponse{}, err
	}
	for _, r := range visible {
		ki := get(r.Group, r.Kind)
		ki.Count++
		if r.Namespace != "" {
			ki.Namespaced = true
		}
	}

	order := map[string]int{}
	for i, pr := range flux.KnownProjects() {
		order[pr.ID] = i
	}
	rank := func(project string) int {
		if i, ok := order[project]; ok {
			return i
		}
		return len(order)
	}
	res := kindsResponse{Items: []kindInfo{}, Projects: []flux.Project{}, Presets: slices.Clone(hello.Presets)}
	if res.Presets == nil {
		res.Presets = []string{}
	}
	seen := map[string]bool{}
	for _, ki := range infos {
		res.Items = append(res.Items, *ki)
		if !seen[ki.Project] {
			seen[ki.Project] = true
			res.Projects = append(res.Projects, flux.ProjectOf(ki.Group))
		}
	}
	slices.SortFunc(res.Items, func(a, b kindInfo) int {
		return cmp.Or(cmp.Compare(rank(a.Project), rank(b.Project)), cmp.Compare(a.Project, b.Project),
			cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Group, b.Group))
	})
	slices.SortFunc(res.Projects, func(a, b flux.Project) int {
		return cmp.Or(cmp.Compare(rank(a.ID), rank(b.ID)), cmp.Compare(a.ID, b.ID))
	})
	return res, nil
}
