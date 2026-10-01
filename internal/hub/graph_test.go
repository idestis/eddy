package hub

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/model"
)

func ksRef(ns, name string) model.Ref {
	return model.Ref{Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: ns, Name: name}
}

func TestSanitizeDependsOn(t *testing.T) {
	hrRef := model.Ref{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "a", Name: "x"}
	var many []model.Ref
	for i := range 40 {
		many = append(many, ksRef("a", fmt.Sprintf("d%d", i)))
	}
	tests := []struct {
		name        string
		row         model.Resource
		deps        []model.Ref
		blocked     bool
		wantDeps    int
		wantBlocked bool
	}{
		{"kustomization keeps valid refs", res("Kustomization", "a", "k", model.StatusReconciling),
			[]model.Ref{ksRef("a", "x"), ksRef("b", "y")}, true, 2, true},
		{"other kinds and duplicates are dropped", res("Kustomization", "a", "k", model.StatusReady),
			[]model.Ref{hrRef, ksRef("a", "x"), ksRef("a", "x"), {Group: "", Kind: "Secret", Namespace: "a", Name: "s"}}, false, 1, false},
		{"invalid names are dropped", res("Kustomization", "a", "k", model.StatusReady),
			[]model.Ref{ksRef("", "x"), ksRef("a", ""), ksRef("a", "Bad_Name"), ksRef("a/b", "x"), ksRef("a", strings.Repeat("x", 254))}, false, 0, false},
		{"capped", res("Kustomization", "a", "k", model.StatusReady), many, false, model.MaxDependsOn, false},
		{"helmrelease keeps helmrelease refs only", res("HelmRelease", "a", "h", model.StatusReconciling),
			[]model.Ref{hrRef, ksRef("a", "x")}, true, 1, true},
		{"not on workloads", res("Deployment", "a", "d", model.StatusReady), []model.Ref{ksRef("a", "x")}, true, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.row
			r.DependsOn, r.Blocked = tt.deps, tt.blocked
			got, ok := sanitizeResource(r)
			if !ok {
				t.Fatal("row refused")
			}
			if len(got.DependsOn) != tt.wantDeps || got.Blocked != tt.wantBlocked {
				t.Fatalf("dependsOn %v blocked %v", got.DependsOn, got.Blocked)
			}
		})
	}
}

// graphRows: alice (team-a) sees team-a only; ops sees everything.
func graphRows() []model.Resource {
	apps := res("Kustomization", "team-a", "apps", model.StatusReconciling)
	apps.Blocked = true
	apps.Message = "Waiting for team-a/infra"
	apps.DependsOn = []model.Ref{ksRef("team-a", "infra"), ksRef("team-b", "secret-ks"), ksRef("team-a", "ghost")}
	repo := res("GitRepository", "team-a", "repo", model.StatusReady)
	apps.Source = &repo.Ref
	appsRef := apps.Ref

	web := res("HelmRelease", "team-a", "web", model.StatusReady)
	web.Owner = &appsRef
	hidden := res("HelmRelease", "team-b", "hidden", model.StatusReady)
	hidden.Owner = &appsRef
	rows := []model.Resource{
		apps, repo, web, hidden,
		res("Kustomization", "team-a", "infra", model.StatusReady),
		res("Kustomization", "team-b", "secret-ks", model.StatusFailed),
	}
	for i := range 25 {
		d := res("Deployment", "team-a", fmt.Sprintf("d%02d", i), model.StatusReady)
		d.Owner = &appsRef
		p := res("Pod", "team-a", fmt.Sprintf("p%02d", i), model.StatusReady)
		p.Owner = &d.Ref
		rows = append(rows, d, p)
	}
	return rows
}

func graphIDs(g graphResponse) (map[string]graphNode, map[string]bool) {
	nodes := map[string]graphNode{}
	for _, n := range g.Nodes {
		nodes[n.ID] = n
	}
	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[e.Type+" "+e.From+" -> "+e.To] = true
	}
	return nodes, edges
}

func TestGraphEndpoint(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, graphRows())
	alice, ops := e.login("alice"), e.login("ops")
	appsID := ksRef("team-a", "apps").ID()

	t.Run("flux graph hides other namespaces", func(t *testing.T) {
		resp := alice.request("GET", "/api/v1/clusters/dev/graph", nil)
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status %d: %s", resp.StatusCode, body)
		}
		if strings.Contains(string(body), "team-b") {
			t.Fatalf("graph names a hidden object: %s", body)
		}
		var g graphResponse
		alice.do("GET", "/api/v1/clusters/dev/graph?kinds=flux", nil, &g, 200)
		nodes, edges := graphIDs(g)
		if len(nodes) != 5 || g.Truncated {
			t.Fatalf("nodes %v truncated %v", nodes, g.Truncated)
		}
		if n := nodes[appsID]; !n.Blocked || n.Status != model.StatusReconciling {
			t.Errorf("apps %+v", n)
		}
		if n := nodes[ksRef("team-a", "ghost").ID()]; !n.Missing {
			t.Errorf("ghost %+v", n)
		}
		for _, want := range []string{
			"dependsOn " + appsID + " -> " + ksRef("team-a", "infra").ID(),
			"dependsOn " + appsID + " -> " + ksRef("team-a", "ghost").ID(),
			"source " + appsID + " -> source.toolkit.fluxcd.io/GitRepository/team-a/repo",
			"owns " + appsID + " -> helm.toolkit.fluxcd.io/HelmRelease/team-a/web",
		} {
			if !edges[want] {
				t.Errorf("missing edge %s in %v", want, edges)
			}
		}
		if len(edges) != 4 {
			t.Errorf("edges %v", edges)
		}
	})

	t.Run("the resource list strips hidden dependencies too", func(t *testing.T) {
		var list struct{ Items []model.Resource }
		alice.do("GET", "/api/v1/clusters/dev/resources?kind=Kustomization", nil, &list, 200)
		for _, r := range list.Items {
			for _, d := range r.DependsOn {
				if d.Namespace == "team-b" {
					t.Fatalf("%s depends on a hidden %s", r.ID, d.ID())
				}
			}
			if r.ID == appsID && len(r.DependsOn) != 2 {
				t.Fatalf("apps dependsOn %v", r.DependsOn)
			}
		}
		var g graphResponse
		ops.do("GET", "/api/v1/clusters/dev/graph", nil, &g, 200)
		_, edges := graphIDs(g)
		if !edges["dependsOn "+appsID+" -> "+ksRef("team-b", "secret-ks").ID()] {
			t.Errorf("ops misses the cross-namespace dependency: %v", edges)
		}
	})

	t.Run("all kinds collapses large sibling sets", func(t *testing.T) {
		var g graphResponse
		alice.do("GET", "/api/v1/clusters/dev/graph?kinds=all", nil, &g, 200)
		nodes, edges := graphIDs(g)
		gid := "group:" + appsID + "/Deployment"
		n, ok := nodes[gid]
		if !ok || n.Count != 25 || n.Statuses[model.StatusReady] != 25 || n.Owner != appsID || n.Namespace != "team-a" || n.Group != "apps" {
			t.Fatalf("group node %+v", n)
		}
		if !edges["owns "+appsID+" -> "+gid] {
			t.Errorf("no owns edge to the group: %v", edges)
		}
		for id := range nodes {
			if strings.Contains(id, "/Pod/") || strings.HasPrefix(id, "apps/Deployment/") {
				t.Errorf("collapsed row %s is a node", id)
			}
		}
	})

	t.Run("focus and hops", func(t *testing.T) {
		focus := "apps/Deployment/team-a/d03"
		var g graphResponse
		alice.do("GET", "/api/v1/clusters/dev/graph?kinds=all&hops=1&focus="+focus, nil, &g, 200)
		nodes, _ := graphIDs(g)
		if len(nodes) != 3 || g.Nodes[0].ID != focus {
			t.Fatalf("hops=1 nodes %v", nodes)
		}
		for _, id := range []string{appsID, "/Pod/team-a/p03"} {
			if _, ok := nodes[id]; !ok {
				t.Errorf("missing %s", id)
			}
		}
		alice.do("GET", "/api/v1/clusters/dev/graph?kinds=all&hops=2&focus="+focus, nil, &g, 200)
		nodes, _ = graphIDs(g)
		// d03, its pod, apps, and apps' neighbours: 24 more deployments,
		// infra, ghost, repo and web.
		if len(nodes) != 3+24+4 {
			t.Fatalf("hops=2: %d nodes", len(nodes))
		}
		alice.do("GET", "/api/v1/clusters/dev/graph?focus=kustomize.toolkit.fluxcd.io/kustomization/team-a/apps&hops=1", nil, &g, 200)
		if g.Nodes[0].ID != appsID || len(g.Nodes) != 5 {
			t.Fatalf("canonical focus: %v", g.Nodes)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for path, want := range map[string]int{
			"/api/v1/clusters/dev/graph?focus=" + ksRef("team-b", "secret-ks").ID(): 404,
			"/api/v1/clusters/dev/graph?focus=apps/Deployment/team-a/d01":           404, // not a flux kind
			"/api/v1/clusters/dev/graph?kinds=some":                                 400,
			"/api/v1/clusters/dev/graph?hops=0":                                     400,
			"/api/v1/clusters/dev/graph?focus=nope":                                 400,
			"/api/v1/clusters/nope/graph":                                           404,
		} {
			if st, _ := alice.errorCode("GET", path, nil); st != want {
				t.Errorf("%s: %d, want %d", path, st, want)
			}
		}
	})
}

func TestGraphTruncates(t *testing.T) {
	var rows []model.Resource
	for i := range graphMaxNodes + 10 {
		rows = append(rows, res("Kustomization", "a", fmt.Sprintf("k%04d", i), model.StatusReady))
	}
	g := buildGraph(rows, "", nil).response(graphOptions{Hops: graphDefaultHops})
	if !g.Truncated || len(g.Nodes) != graphMaxNodes {
		t.Fatalf("truncated %v, %d nodes", g.Truncated, len(g.Nodes))
	}
}

// expandRows: the bootstrap Kustomization in team-a applies 26 Kustomizations
// (a dependsOn chain) and 25 Deployments with a Pod each; three more
// Deployments it applies live in team-b, which alice cannot list.
func expandRows() []model.Resource {
	root := res("Kustomization", "team-a", "flux-system", model.StatusReady)
	rows := []model.Resource{root}
	for i := range 26 {
		k := res("Kustomization", "team-a", fmt.Sprintf("ks%02d", i), model.StatusReady)
		k.Owner = &root.Ref
		if i > 0 {
			k.DependsOn = []model.Ref{ksRef("team-a", fmt.Sprintf("ks%02d", i-1))}
		}
		rows = append(rows, k)
	}
	for i := range 25 {
		d := res("Deployment", "team-a", fmt.Sprintf("d%02d", i), model.StatusReady)
		d.Owner = &root.Ref
		p := res("Pod", "team-a", fmt.Sprintf("p%02d", i), model.StatusReady)
		p.Owner = &d.Ref
		rows = append(rows, d, p)
	}
	for i := range 3 {
		d := res("Deployment", "team-b", fmt.Sprintf("hidden%d", i), model.StatusReady)
		d.Owner = &root.Ref
		rows = append(rows, d)
	}
	return rows
}

func TestGraphExpand(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, expandRows())
	alice := e.login("alice")
	rootID := ksRef("team-a", "flux-system").ID()
	deployGroup := "group:" + rootID + "/Deployment"

	t.Run("flux objects are never collapsed", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/clusters/dev/graph",
			"/api/v1/clusters/dev/graph?kinds=all",
			"/api/v1/clusters/dev/graph?kinds=all&focus=" + rootID,
		} {
			var g graphResponse
			alice.do("GET", path, nil, &g, 200)
			nodes, edges := graphIDs(g)
			for i := range 26 {
				id := ksRef("team-a", fmt.Sprintf("ks%02d", i)).ID()
				if _, ok := nodes[id]; !ok {
					t.Fatalf("%s: %s is not a node", path, id)
				}
				if !edges["owns "+rootID+" -> "+id] {
					t.Errorf("%s: no owns edge to %s", path, id)
				}
			}
			if !edges["dependsOn "+ksRef("team-a", "ks25").ID()+" -> "+ksRef("team-a", "ks24").ID()] {
				t.Errorf("%s: dependsOn chain missing", path)
			}
			if _, ok := nodes["group:"+rootID+"/Kustomization"]; ok {
				t.Errorf("%s: Kustomizations collapsed", path)
			}
		}
	})

	t.Run("non-flux rows still collapse, without hidden members", func(t *testing.T) {
		var g graphResponse
		alice.do("GET", "/api/v1/clusters/dev/graph?kinds=all", nil, &g, 200)
		nodes, _ := graphIDs(g)
		if n := nodes[deployGroup]; n.Count != 25 || n.Namespace != "team-a" {
			t.Fatalf("group %+v", n)
		}
		if len(nodes) != 1+26+1 {
			t.Fatalf("%d nodes", len(nodes))
		}
	})

	t.Run("expand returns the members", func(t *testing.T) {
		for _, path := range []string{
			"/api/v1/clusters/dev/graph?kinds=all&expand=" + deployGroup,
			"/api/v1/clusters/dev/graph?kinds=all&hops=2&focus=" + rootID + "&expand=" + deployGroup + ",group:" + rootID + "/Service",
		} {
			resp := alice.request("GET", path, nil)
			body, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("%s: status %d: %s", path, resp.StatusCode, body)
			}
			if strings.Contains(string(body), "team-b") {
				t.Fatalf("%s: names a hidden object: %s", path, body)
			}
			var g graphResponse
			alice.do("GET", path, nil, &g, 200)
			nodes, edges := graphIDs(g)
			if _, ok := nodes[deployGroup]; ok {
				t.Fatalf("%s: group still collapsed", path)
			}
			for i := range 25 {
				d := "apps/Deployment/team-a/" + fmt.Sprintf("d%02d", i)
				if !edges["owns "+rootID+" -> "+d] {
					t.Fatalf("%s: no owns edge to %s", path, d)
				}
				if !edges["owns "+d+" -> /Pod/team-a/"+fmt.Sprintf("p%02d", i)] {
					t.Fatalf("%s: the pod of %s is missing", path, d)
				}
			}
			if len(nodes) != 1+26+50 || g.Truncated {
				t.Fatalf("%s: %d nodes, truncated %v", path, len(nodes), g.Truncated)
			}
		}
	})

	t.Run("invalid expand", func(t *testing.T) {
		many := make([]string, graphMaxExpand+1)
		for i := range many {
			many[i] = fmt.Sprintf("group:%s/Kind%d", rootID, i)
		}
		for _, q := range []string{
			"nope",
			"group:nope",
			"group:" + rootID,
			"group:" + rootID + "/",
			"group:" + rootID + "/Bad Kind",
			strings.Join(many, ","),
		} {
			if st, _ := alice.errorCode("GET", "/api/v1/clusters/dev/graph?kinds=all&expand="+url.QueryEscape(q), nil); st != 400 {
				t.Errorf("expand=%q: %d, want 400", q, st)
			}
		}
		ok := strings.Join(many[:graphMaxExpand], ",")
		var g graphResponse
		alice.do("GET", "/api/v1/clusters/dev/graph?kinds=all&expand="+url.QueryEscape(ok), nil, &g, 200)
	})
}

func TestGraphExpandRespectsTheCap(t *testing.T) {
	root := res("Kustomization", "a", "root", model.StatusReady)
	rows := []model.Resource{root}
	for i := range 10 {
		k := res("Kustomization", "a", fmt.Sprintf("k%02d", i), model.StatusReady)
		k.Owner = &root.Ref
		rows = append(rows, k)
	}
	for i := range graphMaxNodes + 50 {
		d := res("Deployment", "a", fmt.Sprintf("d%04d", i), model.StatusReady)
		d.Owner = &root.Ref
		rows = append(rows, d)
	}
	// Watched rows outside the group are cut before its members.
	for i := range 30 {
		rows = append(rows, res("Pod", "a", fmt.Sprintf("p%02d", i), model.StatusReady))
	}
	gid := "group:" + root.ID + "/Deployment"
	collapsed := buildGraph(rows, "", nil).response(graphOptions{Hops: graphDefaultHops})
	if collapsed.Truncated || len(collapsed.Nodes) != 1+10+1+30 {
		t.Fatalf("collapsed: %d nodes, truncated %v", len(collapsed.Nodes), collapsed.Truncated)
	}
	for _, o := range []graphOptions{
		{Hops: graphDefaultHops, Expand: map[string]bool{gid: true}},
		{Hops: 1, Focus: root.ID, Expand: map[string]bool{gid: true}},
	} {
		g := buildGraph(rows, o.Focus, o.Expand).response(o)
		nodes, _ := graphIDs(g)
		if !g.Truncated || len(g.Nodes) != graphMaxNodes {
			t.Fatalf("focus %q: %d nodes, truncated %v", o.Focus, len(g.Nodes), g.Truncated)
		}
		if _, ok := nodes[gid]; ok {
			t.Fatalf("focus %q: group node in an expanded graph", o.Focus)
		}
		for _, id := range []string{root.ID, ksRef("a", "k09").ID()} {
			if _, ok := nodes[id]; !ok {
				t.Errorf("focus %q: flux node %s cut", o.Focus, id)
			}
		}
		if o.Focus == "" {
			for id := range nodes {
				if strings.Contains(id, "/Pod/") {
					t.Errorf("pod %s kept before the expanded members", id)
				}
			}
		}
	}
}
