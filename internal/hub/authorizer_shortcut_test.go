package hub

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// rbacAllow is a small RBAC model: a cluster-wide grant answers every
// namespace too, as Kubernetes does; a namespace grant answers only that
// namespace and never the cluster-scope question.
func rbacAllow(id protocol.Identity, c protocol.AccessCheck) bool {
	for _, g := range id.Groups {
		switch {
		case g == "eddy:admins":
			return true
		case g == "eddy:flux-readers" && c.Group == "kustomize.toolkit.fluxcd.io":
			return true
		case c.Namespace != "" && g == "eddy:"+c.Namespace:
			return true
		}
	}
	return false
}

func shortcutRows() []model.Resource {
	var rs []model.Resource
	for i := range 20 {
		ns := fmt.Sprintf("ns-%02d", i)
		rs = append(rs, res("Kustomization", ns, "apps", model.StatusReady), res("Deployment", ns, "web", model.StatusReady))
	}
	nsRow := model.Resource{Ref: model.Ref{Kind: "Namespace", Name: "ns-00"}, Status: model.StatusReady}
	nsRow.ID = nsRow.Ref.ID()
	return append(rs, nsRow)
}

func TestAuthorizerClusterScopeShortcut(t *testing.T) {
	rows := shortcutRows()
	cases := []struct {
		name   string
		groups []string
		// want lists the visible ids' (kind/namespace).
		want       func(r model.Resource) bool
		maxChecks  int
		wideChecks int
	}{
		{
			name: "cluster-wide reader: one check per kind", groups: []string{"eddy:admins"},
			want: func(model.Resource) bool { return true }, maxChecks: 3, wideChecks: 3,
		},
		{
			name: "namespace-only user is still filtered per namespace", groups: []string{"eddy:ns-03", "eddy:ns-07"},
			want:      func(r model.Resource) bool { return r.Namespace == "ns-03" || r.Namespace == "ns-07" },
			maxChecks: 3 + 40, wideChecks: 3,
		},
		{
			name: "mixed: cluster-wide Flux, per-namespace workloads", groups: []string{"eddy:flux-readers", "eddy:ns-05"},
			want: func(r model.Resource) bool {
				return r.Kind == "Kustomization" || (r.Kind == "Deployment" && r.Namespace == "ns-05")
			},
			maxChecks: 3 + 20, wideChecks: 3,
		},
		{
			name: "nobody", groups: []string{"eddy:nothing"},
			want: func(model.Resource) bool { return false }, maxChecks: 3 + 40, wideChecks: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := &countingSender{allow: rbacAllow}
			a := newAuthorizer(cs.send, newMetrics())
			p := identity.Principal{User: "u", Groups: tc.groups}
			got, err := a.filter(context.Background(), p, "dev", slices.Clone(rows))
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, r := range rows {
				if tc.want(r) {
					want = append(want, r.ID)
				}
			}
			var ids []string
			for _, r := range got {
				ids = append(ids, r.ID)
			}
			if !slices.Equal(ids, want) {
				t.Fatalf("visible = %v\nwant      %v", ids, want)
			}
			var total, wide int
			for _, b := range cs.batches {
				for _, c := range b {
					total++
					if c.Namespace == "" {
						wide++
					}
				}
			}
			if total > tc.maxChecks || wide != tc.wideChecks {
				t.Fatalf("asked %d checks (%d cluster-scope), want ≤ %d (%d cluster-scope)", total, wide, tc.maxChecks, tc.wideChecks)
			}
		})
	}
}

// TestAuthorizerShortcutNeverOverGrants checks every (user, tuple) answer
// of allowedTuples against asking the namespace question directly.
func TestAuthorizerShortcutNeverOverGrants(t *testing.T) {
	var tuples []accessTuple
	for _, r := range shortcutRows() {
		t, _ := tupleOf(r.Ref)
		tuples = append(tuples, t)
	}
	for _, groups := range [][]string{{"eddy:admins"}, {"eddy:ns-01"}, {"eddy:flux-readers"}, {"eddy:flux-readers", "eddy:ns-19"}, nil} {
		cs := &countingSender{allow: rbacAllow}
		a := newAuthorizer(cs.send, newMetrics())
		p := identity.Principal{User: "u", Groups: groups}
		got, err := a.allowedTuples(context.Background(), p, "dev", "list", tuples)
		if err != nil {
			t.Fatal(err)
		}
		id := protocol.Identity{User: p.User, Groups: p.Groups}
		for _, tu := range tuples {
			if direct := rbacAllow(id, tu.check("list")); got[tu] != direct {
				t.Errorf("groups %v, tuple %+v: shortcut says %v, direct check says %v", groups, tu, got[tu], direct)
			}
		}
	}
}

// TestAuthorizerStaleFallback: while a cluster is served from a stale
// view, an expired answer (at most staleAccessGrace old) still filters
// reads; a fresh question, or one for a live cluster, fails closed.
func TestAuthorizerStaleFallback(t *testing.T) {
	cs := &countingSender{allow: rbacAllow}
	a := newAuthorizer(cs.send, newMetrics())
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	stale := false
	a.stale = func(string) bool { return stale }
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-01"}}
	known := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-01"}}
	if res, err := a.check(context.Background(), p, "dev", known); err != nil || !res[0] {
		t.Fatalf("first check: %v %v", res, err)
	}
	cs.err = fmt.Errorf("%w: dev", fleet.ErrDisconnected)
	now = now.Add(accessTTL + time.Second)

	if _, err := a.check(context.Background(), p, "dev", known); err == nil {
		t.Fatal("an expired answer was used for a cluster that is not stale")
	}
	stale = true
	if res, err := a.check(context.Background(), p, "dev", known); err != nil || !res[0] {
		t.Fatalf("stale cluster, known answer: %v %v, want the last answer", res, err)
	}
	unknown := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-02"}}
	if _, err := a.check(context.Background(), p, "dev", unknown); err == nil {
		t.Fatal("a question never asked was answered while the agent is gone")
	}
	other := identity.Principal{User: "u", Groups: []string{"eddy:ns-01", "eddy:new"}}
	if _, err := a.check(context.Background(), other, "dev", known); err == nil {
		t.Fatal("changed groups reused an old answer")
	}
	now = now.Add(staleAccessGrace)
	if _, err := a.check(context.Background(), p, "dev", known); err == nil {
		t.Fatal("an answer older than staleAccessGrace was used")
	}
}
