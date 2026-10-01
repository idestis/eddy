package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// inventoryEnv connects a fake agent with a Kustomization in team-a and
// inventory-only rows, including a Kind name that two API groups share.
func inventoryEnv(t *testing.T, hello *protocol.Hello) (*testEnv, *fakeAgent) {
	t.Helper()
	e := newEnv(t, "")
	rows := []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		invRow("", "ConfigMap", "team-a", "cfg", "team-a"),
		invRow("", "Secret", "team-a", "creds", "team-a"),
		invRow("example.com", "Widget", "team-a", "w", "team-a"),
		invRow("a.example.com", "Gadget", "team-a", "g", "team-a"),
		invRow("b.example.com", "Gadget", "team-a", "g", "team-a"),
		invRow("rbac.authorization.k8s.io", "ClusterRole", "", "admin", "team-a"),
	}
	if hello == nil {
		return e, e.connectAgent("dev", testToken, rows)
	}
	a, err := dialAgentHello(context.Background(), e.agentURL("dev"), testToken, *hello)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	a.sendFrame(protocol.TypeSnapshot, "", protocol.Snapshot{Resources: rows})
	go a.serve()
	waitFor(t, func() bool { s := e.hub.agents.get("dev"); return s != nil && s.size() == len(rows) })
	return e, a
}

func TestInventoryReadsAreImpersonated(t *testing.T) {
	e, a := inventoryEnv(t, nil)
	alice, ops := e.login("alice"), e.login("ops")

	var y struct{ YAML string }
	alice.do("GET", "/api/v1/clusters/dev/objects/Widget/team-a/w/yaml", nil, &y, 200)
	alice.do("GET", "/api/v1/clusters/dev/objects/configmap/team-a/cfg/events", nil, nil, 200)
	alice.do("GET", "/api/v1/clusters/dev/objects/ConfigMap/team-a/cfg/yaml?group=core", nil, nil, 200)
	reqs := append(a.recorded(protocol.OpYAML), a.recorded(protocol.OpEvents)...)
	if len(reqs) != 3 {
		t.Fatalf("agent got %d reads", len(reqs))
	}
	want := []model.Ref{
		{Group: "example.com", Kind: "Widget", Namespace: "team-a", Name: "w"},
		{Group: "", Kind: "ConfigMap", Namespace: "team-a", Name: "cfg"},
		{Group: "", Kind: "ConfigMap", Namespace: "team-a", Name: "cfg"},
	}
	yamls, events := a.recorded(protocol.OpYAML), a.recorded(protocol.OpEvents)
	got := []model.Ref{yamls[0].Target, yamls[1].Target, events[0].Target}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("read %d target %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, r := range reqs {
		if r.Identity.User != "local:alice" {
			t.Errorf("read not impersonated as alice: %+v", r.Identity)
		}
	}
	if strings.Contains(y.YAML, "ghp_") {
		t.Fatalf("hub did not redact: %q", y.YAML)
	}

	// Secret YAML is refused by the hub for any group or spelling, whether
	// or not the object exists; its events are allowed.
	for _, path := range []string{
		"/api/v1/clusters/dev/objects/Secret/team-a/creds/yaml",
		"/api/v1/clusters/dev/objects/secret/team-a/missing/yaml",
		"/api/v1/clusters/dev/objects/Secret/team-a/creds/yaml?group=example.com",
	} {
		if st, code := alice.errorCode("GET", path, nil); st != 403 || code != "forbidden" {
			t.Errorf("%s: %d %s", path, st, code)
		}
	}
	alice.do("GET", "/api/v1/clusters/dev/objects/Secret/team-a/creds/events", nil, nil, 200)
	if n := len(a.recorded(protocol.OpYAML)); n != 2 {
		t.Fatalf("Secret yaml reached the agent (%d yaml reads)", n)
	}

	// A Kind two groups share needs ?group=.
	if st, _ := alice.errorCode("GET", "/api/v1/clusters/dev/objects/Gadget/team-a/g/yaml", nil); st != 400 {
		t.Fatalf("ambiguous kind: %d", st)
	}
	alice.do("GET", "/api/v1/clusters/dev/objects/Gadget/team-a/g/yaml?group=b.example.com", nil, nil, 200)
	if last := a.recorded(protocol.OpYAML); last[len(last)-1].Target.Group != "b.example.com" {
		t.Fatalf("group not forwarded: %+v", last[len(last)-1].Target)
	}
	var obj model.Resource
	alice.do("GET", "/api/v1/clusters/dev/objects/Gadget/team-a/g?group=a.example.com", nil, &obj, 200)
	if obj.Group != "a.example.com" || !obj.InventoryOnly || obj.Project != "a.example.com" {
		t.Fatalf("object %+v", obj)
	}

	// Objects outside the view, or outside the user's visibility, are 404.
	for _, path := range []string{
		"/api/v1/clusters/dev/objects/Widget/team-a/other/yaml",
		"/api/v1/clusters/dev/objects/Widget/team-a/w/yaml?group=other.example.com",
		"/api/v1/clusters/dev/objects/ClusterRole/_/admin/yaml", // cluster-scoped list denied
	} {
		if st, _ := alice.errorCode("GET", path, nil); st != 404 {
			t.Errorf("%s: %d", path, st)
		}
	}
	ops.do("GET", "/api/v1/clusters/dev/objects/ClusterRole/_/admin/yaml", nil, nil, 200)

	// The API server's answer to the impersonated read is passed on.
	a.onRequest(func(f protocol.Frame, req protocol.Request) bool {
		if req.Op != protocol.OpYAML {
			return false
		}
		a.reply(f.ID, nil, &protocol.Error{Code: 403, Message: `configmaps "cfg" is forbidden: User "local:alice" cannot get resource "configmaps"`})
		return true
	})
	if st, code := alice.errorCode("GET", "/api/v1/clusters/dev/objects/ConfigMap/team-a/cfg/yaml", nil); st != 403 || code != "forbidden" {
		t.Fatalf("forbidden by the API server: %d %s", st, code)
	}
}

func TestKindsEndpoint(t *testing.T) {
	hello := protocol.Hello{
		Protocol: protocol.Version, Cluster: "dev", AgentVersion: "v0.2.0", KubernetesVersion: "v1.33.0",
		Presets: []string{flux.PresetKarpenter, "datadog"},
		Kinds:   []string{"kustomize.toolkit.fluxcd.io/Kustomization", "karpenter.sh/NodePool", "/Secret", "bogus"},
	}
	e, _ := inventoryEnv(t, &hello)
	alice := e.login("alice")
	var res kindsResponse
	alice.do("GET", "/api/v1/clusters/dev/kinds", nil, &res, 200)

	byKey := map[string]kindInfo{}
	for _, k := range res.Items {
		byKey[k.Group+"/"+k.Kind] = k
	}
	if len(res.Presets) != 1 || res.Presets[0] != flux.PresetKarpenter {
		t.Errorf("presets %v", res.Presets)
	}
	if k := byKey["kustomize.toolkit.fluxcd.io/Kustomization"]; !k.Watched || k.Count != 1 || k.Project != "flux" || k.Plural != "kustomizations" || !k.Namespaced {
		t.Errorf("Kustomization %+v", k)
	}
	if k := byKey["karpenter.sh/NodePool"]; !k.Watched || k.Count != 0 || k.Project != "karpenter" || k.Namespaced || k.Preset != flux.PresetKarpenter {
		t.Errorf("NodePool %+v", k)
	}
	if k := byKey["/ConfigMap"]; k.Watched || k.Count != 1 || k.Project != "kubernetes" || k.Plural != "configmaps" {
		t.Errorf("ConfigMap %+v", k)
	}
	if k := byKey["example.com/Widget"]; k.Count != 1 || k.Project != "example.com" || k.Plural != "" {
		t.Errorf("Widget %+v", k)
	}
	if k := byKey["/Secret"]; k.Watched || k.Count != 1 {
		t.Errorf("Secret (inventory only; the agent cannot claim to watch it) %+v", k)
	}
	if _, ok := byKey["rbac.authorization.k8s.io/ClusterRole"]; ok {
		t.Error("alice may not list cluster roles")
	}
	var ids []string
	for _, p := range res.Projects {
		ids = append(ids, p.ID)
	}
	if got := strings.Join(ids, ","); got != "kubernetes,flux,karpenter,a.example.com,b.example.com,example.com" {
		t.Errorf("projects %s", got)
	}
	if res.Items[0].Project != "kubernetes" {
		t.Errorf("first item %+v", res.Items[0])
	}

	var clusters struct{ Items []model.ClusterInfo }
	alice.do("GET", "/api/v1/clusters", nil, &clusters, 200)
	if p := clusters.Items[0].Presets; len(p) != 1 || p[0] != flux.PresetKarpenter {
		t.Errorf("cluster presets %v", p)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/clusters/nope/kinds", nil); st != 404 {
		t.Errorf("unknown cluster: %d", st)
	}
}

func TestSanitizeResourceProjectAndDetails(t *testing.T) {
	r := model.Resource{Ref: model.Ref{Group: flux.GroupKarpenter, Kind: flux.KindNodePool, Name: "default"}, Status: model.StatusReady, Project: "spoofed"}
	for i := range 20 {
		r.Details = append(r.Details, model.Detail{Label: "l", Value: strings.Repeat("v", 400) + string(rune('a'+i))})
	}
	r.Details = append([]model.Detail{{Label: "", Value: "x"}}, r.Details...)
	got, ok := sanitizeResource(r)
	if !ok || got.Project != "karpenter" || len(got.Details) != model.MaxDetails || len(got.Details[0].Value) > 256 {
		t.Fatalf("%+v %v", got, ok)
	}
	r.Namespace = "team-a" // a cluster-scoped kind with a namespace
	if _, ok := sanitizeResource(r); ok {
		t.Fatal("accepted a namespaced NodePool")
	}
	if got, _ := sanitizeResource(invRow("", "ConfigMap", "a", "b", "a")); got.Project != "kubernetes" {
		t.Fatalf("inventory project %q", got.Project)
	}
}
