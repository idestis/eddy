package hub

import (
	"context"
	"slices"
	"testing"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

func invRow(group, kind, ns, name, ownerNS string) model.Resource {
	owner := model.Ref{Group: flux.GroupKustomize, Kind: flux.KindKustomization, Namespace: ownerNS, Name: "apps"}
	r := model.Resource{Ref: model.Ref{Group: group, Kind: kind, Namespace: ns, Name: name}, Status: model.StatusUnknown, InventoryOnly: true, Owner: &owner}
	r.ID = r.Ref.ID()
	return r
}

func TestSanitizeInventoryRow(t *testing.T) {
	r := invRow("", "Secret", "team-a", "creds", "team-a")
	r.Status, r.Message, r.Labels, r.Images = model.StatusReady, "leak", map[string]string{"a": "b"}, []string{"x"}
	got, ok := sanitizeResource(r)
	if !ok || got.Status != model.StatusUnknown || got.Message != "" || got.Labels != nil || got.Images != nil || !got.InventoryOnly || got.ID != "/Secret/team-a/creds" {
		t.Fatalf("sanitized %+v %v", got, ok)
	}
	bad := []model.Resource{
		func() model.Resource { r := invRow("", "Secret", "a", "b", "a"); r.Owner = nil; return r }(),
		func() model.Resource {
			r := invRow("", "Secret", "a", "b", "a")
			r.Owner = &model.Ref{Group: flux.GroupHelm, Kind: flux.KindHelmRelease, Namespace: "a", Name: "x"}
			return r
		}(),
		invRow("", "secret", "a", "b", "a"),
		invRow("", "Se/cret", "a", "b", "a"),
		invRow("", "Secret", "a", "", "a"),
	}
	for _, b := range bad {
		if _, ok := sanitizeResource(b); ok {
			t.Errorf("accepted %+v", b)
		}
	}
}

func TestAuthorizerFilterInventoryRows(t *testing.T) {
	// alice may list everything in team-a except secrets.
	cs := &countingSender{allow: func(id protocol.Identity, c protocol.AccessCheck) bool {
		return defaultAllow(id, c) && c.Resource != "secrets"
	}}
	a := newAuthorizer(cs.send, newMetrics())
	rs := []model.Resource{
		invRow("", "ConfigMap", "team-a", "cfg", "team-a"),                        // visible
		invRow("", "Secret", "team-a", "creds", "team-a"),                         // own kind denied
		invRow("cert-manager.io", "ClusterIssuer", "", "le", "team-a"),            // unknown plural: parent decides
		invRow("cert-manager.io", "ClusterIssuer", "", "le2", "team-b"),           // parent hidden
		invRow("rbac.authorization.k8s.io", "ClusterRole", "", "admin", "team-a"), // cluster-scoped list denied
		invRow(flux.GroupAutoscale, flux.KindHPA, "team-a", "web", "team-a"),      // table kind, unserved
		invRow("example.com", "Deployment", "team-a", "not-apps", "team-a"),       // same name, other group: parent decides
		invRow("", "ServiceAccount", "team-b", "sa", "team-a"),                    // own namespace denied
	}
	got, err := a.filter(context.Background(), alice, "dev", rs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if want := []string{"cfg", "le", "web", "not-apps"}; !slices.Equal(names, want) {
		t.Fatalf("visible %v, want %v", names, want)
	}
}

func TestInventoryRowsNotCountedAndDeletesFiltered(t *testing.T) {
	b := newBus()
	s := testSession(b)
	ks := res("Kustomization", "team-a", "apps", model.StatusReady)
	cm := invRow("", "ConfigMap", "team-a", "cfg", "team-a")
	hidden := invRow("", "ConfigMap", "team-b", "cfg", "team-b")
	if err := s.handle(frame(t, protocol.TypeSnapshot, protocol.Snapshot{Resources: []model.Resource{ks, cm, hidden}})); err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, byStatus := range s.tupleCounts() {
		for _, n := range byStatus {
			total += n
		}
	}
	if total != 1 {
		t.Fatalf("counted %d resources, want only the Kustomization", total)
	}

	sub := b.subscribe()
	if err := s.handle(frame(t, protocol.TypeDelta, protocol.Delta{Deletes: []string{cm.ID, hidden.ID}})); err != nil {
		t.Fatal(err)
	}
	var e event
	for e = range sub.ch {
		if e.kind == evChange {
			break
		}
	}
	if len(e.parents) != 2 || e.parents[cm.ID].Namespace != "team-a" {
		t.Fatalf("parents %v", e.parents)
	}
	cs := &countingSender{allow: defaultAllow}
	f := &fleetService{authz: newAuthorizer(cs.send, newMetrics())}
	_, dels, err := f.filterChange(context.Background(), alice, e)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(dels, []string{cm.ID}) {
		t.Fatalf("deletes %v", dels)
	}
}

func TestInventoryRowsAPI(t *testing.T) {
	e := newEnv(t, "")
	ks := res("Kustomization", "team-a", "apps", model.StatusReady)
	e.connectAgent("dev", testToken, []model.Resource{
		ks,
		invRow("", "ConfigMap", "team-a", "cfg", "team-a"),
		invRow("", "ConfigMap", "team-b", "other", "team-b"),
	})
	alice, bob := e.login("alice"), e.login("bob")
	var list struct{ Items []model.Resource }
	alice.do("GET", "/api/v1/clusters/dev/resources", nil, &list, 200)
	if len(list.Items) != 2 || !list.Items[0].InventoryOnly || list.Items[0].Name != "cfg" {
		t.Fatalf("alice sees %+v", list.Items)
	}
	var obj model.Resource
	alice.do("GET", "/api/v1/clusters/dev/objects/ConfigMap/team-a/cfg", nil, &obj, 200)
	if !obj.InventoryOnly || obj.Owner == nil || obj.Owner.Name != "apps" {
		t.Fatalf("object %+v", obj)
	}
	if st, _ := bob.errorCode("GET", "/api/v1/clusters/dev/objects/ConfigMap/team-a/cfg", nil); st != 404 {
		t.Fatalf("bob reads alice's inventory row: %d", st)
	}
	for _, sub := range []string{"yaml", "events"} {
		alice.do("GET", "/api/v1/clusters/dev/objects/ConfigMap/team-a/cfg/"+sub, nil, nil, 200)
		if st, _ := bob.errorCode("GET", "/api/v1/clusters/dev/objects/ConfigMap/team-a/cfg/"+sub, nil); st != 404 {
			t.Fatalf("bob reads %s of alice's inventory row: %d", sub, st)
		}
	}
	var clusters struct{ Items []model.ClusterInfo }
	alice.do("GET", "/api/v1/clusters", nil, &clusters, 200)
	if c := clusters.Items[0]; c.Counts[model.StatusUnknown] != 0 || c.Counts[model.StatusReady] != 1 {
		t.Fatalf("counts %+v", c.Counts)
	}
}
