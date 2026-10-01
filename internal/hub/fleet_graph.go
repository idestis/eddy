package hub

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
)

// Limits of GET /api/v1/clusters/{c}/graph.
const (
	// graphMaxNodes caps the nodes of one response; the rest is cut and
	// the response says truncated.
	graphMaxNodes = 2000
	// graphCollapseOver: when an owner has more non-Flux children of one
	// kind than this, they become one group node. Flux objects are never
	// collapsed: their dependsOn and source edges are the graph.
	graphCollapseOver = 20
	// graphMaxExpand caps the group ids of one expand parameter.
	graphMaxExpand = 20
	graphDefaultHops  = 2
	graphMaxHops      = 6
)

// Edge types of the graph.
const (
	edgeDependsOn = "dependsOn" // from the dependent to its dependency
	edgeSource    = "source"    // from a Kustomization, HelmRelease or HelmChart to its source
	edgeOwns      = "owns"      // from the owner to the owned object (or group)
)

// graphOptions are the query parameters of the graph endpoint.
type graphOptions struct {
	// All includes every row of the view; otherwise only Flux kinds.
	All bool
	// Focus is a resource id; when set, only nodes within Hops edges of it
	// (in either direction) are returned.
	Focus string
	Hops  int
	// Expand lists group ids (group:<owner id>/<Kind>) returned as their
	// member nodes instead of one group node. Unknown ids are ignored.
	Expand map[string]bool
}

// graphNode is one node of the graph: a resource row, a group of
// collapsed siblings (Count > 0) or a missing dependency (Missing).
type graphNode struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name,omitempty"`
	// Status, Message, Blocked, Revision and LastChanged are the row's.
	Status        model.Status `json:"status,omitempty"`
	Message       string       `json:"message,omitempty"`
	Blocked       bool         `json:"blocked,omitempty"`
	Revision      string       `json:"revision,omitempty"`
	LastChanged   time.Time    `json:"lastChanged,omitzero"`
	InventoryOnly bool         `json:"inventoryOnly,omitempty"`
	// Missing marks a dependsOn target that is not in the view: it does
	// not exist (or the agent has not seen it yet).
	Missing bool `json:"missing,omitempty"`
	// Count, Statuses and Owner are set on group nodes: Count children of
	// Kind owned by Owner, collapsed into one node, by status.
	Count    int                  `json:"count,omitempty"`
	Statuses map[model.Status]int `json:"statuses,omitempty"`
	Owner    string               `json:"owner,omitempty"`
}

// graphEdge is a directed edge between two node ids.
type graphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}

// graphResponse is the body of GET /api/v1/clusters/{c}/graph.
type graphResponse struct {
	Nodes     []graphNode `json:"nodes"`
	Edges     []graphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
	Stale     bool        `json:"stale,omitempty"`
}

// Graph returns the dependency graph of a cluster's view as p sees it.
// Nodes are rows p may list (the same SAR filter as the resource list), so
// an edge never points at a row p could not see: owns and source edges are
// drawn only between visible rows, and DependsOn entries in namespaces p
// may not list are already removed by the filter. Non-Flux siblings of
// one kind under one owner are collapsed into a group node when there are
// more than graphCollapseOver of them, unless the focus is among them or
// below them, or the group is in o.Expand.
func (f *fleetService) Graph(ctx context.Context, p identity.Principal, cluster string, o graphOptions) (graphResponse, error) {
	if err := f.validPrincipal(p); err != nil {
		return graphResponse{}, err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return graphResponse{}, err
	}
	all, _ := s.view()
	cand := all[:0:0]
	for _, r := range all {
		if o.All || isFluxRow(r) {
			cand = append(cand, r)
		}
	}
	visible, err := f.authz.filter(ctx, p, cluster, cand)
	if err != nil {
		return graphResponse{}, err
	}
	g := buildGraph(visible, o.Focus, o.Expand)
	if o.Focus != "" && g.rows[o.Focus] == nil {
		return graphResponse{}, fmt.Errorf("%w: %s", fleet.ErrNotFound, o.Focus)
	}
	return g.response(o), nil
}

// canonicalFocus validates a focus id and canonicalises the spelling of a
// kind from the kind table.
func canonicalFocus(id string) (string, error) {
	ref, err := model.ParseRef(id)
	if err == nil {
		if k, ok := flux.KindByName(ref.Kind); ok && k.Group == ref.Group {
			ref.Kind = k.Kind
		}
	}
	if err != nil || !flux.ValidKindName(ref.Kind) {
		return "", badRequest("focus must be a resource id <group>/<Kind>/<namespace>/<name>")
	}
	return ref.ID(), nil
}

// parseGroupID validates a group node id, group:<owner id>/<Kind>.
func parseGroupID(id string) (string, error) {
	bad := badRequest("expand must list group ids group:<group>/<Kind>/<namespace>/<name>/<Kind>")
	rest, ok := strings.CutPrefix(id, groupPrefix)
	i := strings.LastIndexByte(rest, '/')
	if !ok || i < 0 {
		return "", bad
	}
	owner, kind := rest[:i], rest[i+1:]
	if _, err := model.ParseRef(owner); err != nil || !flux.ValidKindName(kind) {
		return "", bad
	}
	return id, nil
}

func isFluxRow(r model.Resource) bool {
	k, ok := flux.KindByName(r.Kind)
	return ok && k.Flux && !r.InventoryOnly && k.Group == r.Group
}

// groupPrefix starts the id of a group node: group:<owner id>/<Kind>.
const groupPrefix = "group:"

// graph is the collapsed graph before focus and the node cap apply.
type graph struct {
	rows  map[string]*model.Resource
	nodes map[string]*graphNode
	edges []graphEdge
	adj   map[string][]string // undirected
	// expanded holds the members of the groups the request expanded;
	// they are cut after Flux kinds when the graph is over the cap.
	expanded map[string]bool
}

func buildGraph(rows []model.Resource, focus string, expand map[string]bool) *graph {
	g := &graph{
		rows: make(map[string]*model.Resource, len(rows)), nodes: map[string]*graphNode{},
		adj: map[string][]string{}, expanded: map[string]bool{},
	}
	for i := range rows {
		g.rows[rows[i].ID] = &rows[i]
	}

	// Raw edges between visible rows, plus missing dependencies.
	var raw []graphEdge
	missing := map[string]model.Ref{}
	children := map[string]map[string][]string{} // owner id -> kind -> non-Flux child ids
	for i := range rows {
		r := &rows[i]
		for _, d := range r.DependsOn {
			id := d.ID()
			if g.rows[id] == nil {
				missing[id] = d
			}
			raw = append(raw, graphEdge{From: r.ID, To: id, Type: edgeDependsOn})
		}
		if r.Source != nil && g.rows[r.Source.ID()] != nil {
			raw = append(raw, graphEdge{From: r.ID, To: r.Source.ID(), Type: edgeSource})
		}
		if r.Owner != nil && r.Owner.ID() != r.ID && g.rows[r.Owner.ID()] != nil {
			o := r.Owner.ID()
			raw = append(raw, graphEdge{From: o, To: r.ID, Type: edgeOwns})
			if isFluxKind(r.Group, r.Kind) {
				continue // never collapsed, never hidden
			}
			if children[o] == nil {
				children[o] = map[string][]string{}
			}
			children[o][r.Kind] = append(children[o][r.Kind], r.ID)
		}
	}

	// The focus and its owners stay expanded.
	keep := map[string]bool{}
	for id := focus; id != "" && !keep[id]; {
		keep[id] = true
		r := g.rows[id]
		if r == nil || r.Owner == nil || g.rows[r.Owner.ID()] == nil {
			break
		}
		id = r.Owner.ID()
	}

	// rep maps a row to the node that stands for it: a group id, or ""
	// when it is hidden below a collapsed row. Rows not in rep stand for
	// themselves.
	rep := map[string]string{}
	var collapsed []string
	for owner, byKind := range children {
		for kind, ids := range byKind {
			gid := groupPrefix + owner + "/" + kind
			if expand[gid] && len(ids) > graphCollapseOver {
				for _, id := range ids {
					g.expanded[id] = true
				}
				continue
			}
			if len(ids) <= graphCollapseOver || slices.ContainsFunc(ids, func(id string) bool { return keep[id] }) {
				continue
			}
			n := &graphNode{ID: gid, Kind: kind, Owner: owner, Count: len(ids), Statuses: map[model.Status]int{}}
			for i, id := range ids {
				r := g.rows[id]
				if i == 0 {
					n.Group, n.Namespace = r.Group, r.Namespace
				}
				if r.Group != n.Group {
					n.Group = ""
				}
				if r.Namespace != n.Namespace {
					n.Namespace = ""
				}
				n.Statuses[r.Status]++
				rep[id] = gid
				collapsed = append(collapsed, id)
			}
			g.nodes[gid] = n
		}
	}
	// Everything owned (transitively) by a collapsed row is hidden.
	for len(collapsed) > 0 {
		id := collapsed[len(collapsed)-1]
		collapsed = collapsed[:len(collapsed)-1]
		for _, kids := range children[id] {
			for _, k := range kids {
				if _, done := rep[k]; !done {
					rep[k] = ""
					collapsed = append(collapsed, k)
				}
			}
		}
	}
	repOf := func(id string) string {
		if r, ok := rep[id]; ok {
			return r
		}
		return id
	}

	for id, r := range g.rows {
		if _, ok := rep[id]; !ok {
			g.nodes[id] = rowNode(r)
		}
	}
	for id, ref := range missing {
		g.nodes[id] = &graphNode{ID: id, Kind: ref.Kind, Group: ref.Group, Namespace: ref.Namespace, Name: ref.Name, Status: model.StatusUnknown, Missing: true}
	}

	seen := map[graphEdge]bool{}
	for _, e := range raw {
		e.From, e.To = repOf(e.From), repOf(e.To)
		if e.From == "" || e.To == "" || e.From == e.To || seen[e] {
			continue
		}
		seen[e] = true
		g.edges = append(g.edges, e)
		g.adj[e.From] = append(g.adj[e.From], e.To)
		g.adj[e.To] = append(g.adj[e.To], e.From)
	}
	return g
}

func rowNode(r *model.Resource) *graphNode {
	return &graphNode{
		ID: r.ID, Kind: r.Kind, Group: r.Group, Namespace: r.Namespace, Name: r.Name,
		Status: r.Status, Message: r.Message, Blocked: r.Blocked, Revision: r.Revision,
		LastChanged: r.LastChanged, InventoryOnly: r.InventoryOnly,
	}
}

// response applies the focus, hops and the node cap.
func (g *graph) response(o graphOptions) graphResponse {
	var order []string
	truncated := false
	if o.Focus != "" {
		// Breadth first from the focus, nearest nodes first.
		dist := map[string]int{o.Focus: 0}
		order = []string{o.Focus}
		for i := 0; i < len(order); i++ {
			id := order[i]
			if dist[id] == o.Hops {
				continue
			}
			// Flux kinds first at each distance, so a cut keeps them.
			next := slices.Clone(g.adj[id])
			slices.SortFunc(next, func(a, b string) int {
				return cmp.Or(cmp.Compare(g.rank(g.nodes[a]), g.rank(g.nodes[b])), cmp.Compare(a, b))
			})
			for _, n := range next {
				if _, ok := dist[n]; ok {
					continue
				}
				if len(order) == graphMaxNodes {
					truncated = true
					break
				}
				dist[n] = dist[id] + 1
				order = append(order, n)
			}
		}
	} else {
		order = make([]string, 0, len(g.nodes))
		for id := range g.nodes {
			order = append(order, id)
		}
		slices.SortFunc(order, func(a, b string) int {
			na, nb := g.nodes[a], g.nodes[b]
			return cmp.Or(cmp.Compare(g.rank(na), g.rank(nb)), cmp.Compare(na.Kind, nb.Kind),
				cmp.Compare(na.Namespace, nb.Namespace), cmp.Compare(na.Name, nb.Name), cmp.Compare(a, b))
		})
		if len(order) > graphMaxNodes {
			order, truncated = order[:graphMaxNodes], true
		}
	}

	res := graphResponse{Nodes: make([]graphNode, 0, len(order)), Edges: []graphEdge{}, Truncated: truncated}
	in := make(map[string]bool, len(order))
	for _, id := range order {
		in[id] = true
		res.Nodes = append(res.Nodes, *g.nodes[id])
	}
	for _, e := range g.edges {
		if in[e.From] && in[e.To] {
			res.Edges = append(res.Edges, e)
		}
	}
	slices.SortFunc(res.Edges, func(a, b graphEdge) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To), cmp.Compare(a.Type, b.Type))
	})
	return res
}

// rank orders nodes when the graph is cut without a focus: Flux kinds
// first, then the members of expanded groups, then watched kinds, then
// inventory-only rows.
func (g *graph) rank(n *graphNode) int {
	switch {
	case n == nil:
		return 3
	case isFluxKind(n.Group, n.Kind) && !n.InventoryOnly:
		return 0
	case g.expanded[n.ID]:
		return 1
	case n.InventoryOnly:
		return 3
	}
	return 2
}

func isFluxKind(group, kind string) bool {
	k, ok := flux.KindByName(kind)
	return ok && k.Flux && k.Group == group
}
