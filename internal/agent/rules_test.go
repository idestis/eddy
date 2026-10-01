package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

func rulesRequest(id protocol.Identity, namespaces ...string) protocol.Request {
	req := request(protocol.OpRules, model.Ref{}, protocol.RulesArgs{Namespaces: namespaces})
	req.Identity = id
	return req
}

func decodeRules(t *testing.T, raw json.RawMessage) protocol.RulesResult {
	t.Helper()
	var out protocol.RulesResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestRulesImpersonate: rules reviews are created through the impersonating
// client of the request identity, never through the agent's own client.
func TestRulesImpersonate(t *testing.T) {
	f := newOps(t)
	f.kube.PrependReactor("create", "selfsubjectrulesreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		r := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectRulesReview)
		r.Status.ResourceRules = []authorizationv1.ResourceRule{{
			Verbs: []string{"list"}, APIGroups: []string{"apps"}, Resources: []string{"deployments"},
		}}
		r.Status.Incomplete = r.Spec.Namespace == "partial"
		if r.Spec.Namespace == "broken" {
			r.Status.EvaluationError = "role missing"
		}
		return true, r, nil
	})
	f.self.PrependReactor("create", "*", func(a k8stesting.Action) (bool, runtime.Object, error) {
		t.Errorf("the agent's own client was used: %s %s", a.GetVerb(), a.GetResource().Resource)
		return true, nil, errors.New("not allowed")
	})
	raw, perr := f.h.Handle(t.Context(), rulesRequest(alice, "apps", "partial", "broken"), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	if len(f.calls) != 1 || f.calls[0].User != alice.User {
		t.Fatalf("impersonated %v, want alice once", f.calls)
	}
	out := decodeRules(t, raw)
	if len(out.Namespaces) != 3 {
		t.Fatalf("%d namespaces", len(out.Namespaces))
	}
	apps, partial, broken := out.Namespaces[0], out.Namespaces[1], out.Namespaces[2]
	if apps.Namespace != "apps" || apps.Incomplete || apps.EvaluationError != "" || len(apps.Rules) != 1 || apps.Rules[0].Resources[0] != "deployments" {
		t.Fatalf("apps: %+v", apps)
	}
	if partial.Namespace != "partial" || !partial.Incomplete {
		t.Fatalf("partial: %+v", partial)
	}
	if broken.EvaluationError != "role missing" {
		t.Fatalf("broken: %+v", broken)
	}
}

func TestRulesRejections(t *testing.T) {
	many := make([]string, protocol.MaxRulesNamespaces+1)
	for i := range many {
		many[i] = fmt.Sprintf("ns-%d", i)
	}
	cases := []struct {
		name string
		req  protocol.Request
		code int
	}{
		{"system user", rulesRequest(protocol.Identity{User: "system:admin"}, "apps"), 403},
		{"system group", rulesRequest(protocol.Identity{User: "bob", Groups: []string{"system:masters"}}, "apps"), 403},
		{"group without prefix", rulesRequest(protocol.Identity{User: "bob", Groups: []string{"admins"}}, "apps"), 403},
		{"too many namespaces", rulesRequest(alice, many...), 400},
		{"empty namespace", rulesRequest(alice, ""), 400},
		{"invalid namespace", rulesRequest(alice, "Not_A_Namespace"), 400},
		{"duplicate namespace", rulesRequest(alice, "apps", "apps"), 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOps(t)
			f.h.Policy.DenyUserPrefixes = []string{"kubernetes-admin"}
			_, perr := f.h.Handle(t.Context(), tc.req, nil)
			if perr == nil || perr.Code != tc.code {
				t.Fatalf("got %v, want %d", perr, tc.code)
			}
			if len(f.calls) != 0 {
				t.Fatal("a refused request built an impersonating client")
			}
		})
	}
}

// TestRulesLimits: too many rules in a namespace, or more than fits in a
// frame, are sent as Truncated without rules; a failed review is an Error.
func TestRulesLimits(t *testing.T) {
	f := newOps(t)
	f.h.rulesReview = func(_ context.Context, _ Clients, ns string) (*authorizationv1.SubjectRulesReviewStatus, error) {
		switch ns {
		case "huge":
			return &authorizationv1.SubjectRulesReviewStatus{ResourceRules: make([]authorizationv1.ResourceRule, protocol.MaxRulesPerNamespace+1)}, nil
		case "fail":
			return nil, errors.New("forbidden")
		}
		return &authorizationv1.SubjectRulesReviewStatus{ResourceRules: []authorizationv1.ResourceRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"pods"}}}}, nil
	}
	raw, perr := f.h.Handle(t.Context(), rulesRequest(alice, "huge", "fail", "ok"), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	out := decodeRules(t, raw)
	if h := out.Namespaces[0]; !h.Truncated || len(h.Rules) != 0 {
		t.Fatalf("huge: truncated %v with %d rules", h.Truncated, len(h.Rules))
	}
	if e := out.Namespaces[1]; e.Error == "" || !strings.Contains(e.Error, "forbidden") || len(e.Rules) != 0 {
		t.Fatalf("fail: %+v", e)
	}
	if ok := out.Namespaces[2]; ok.Truncated || len(ok.Rules) != 1 {
		t.Fatalf("ok: %+v", ok)
	}

	// The frame budget: the namespaces that no longer fit are truncated.
	big := make([]protocol.ResourceRule, 100)
	for i := range big {
		big[i] = protocol.ResourceRule{Verbs: []string{"get"}, APIGroups: []string{"g"}, Resources: []string{strings.Repeat("r", 100)}}
	}
	nss := []protocol.NamespaceRules{{Namespace: "a", Rules: big}, {Namespace: "b", Rules: big}, {Namespace: "c", Rules: big}}
	one, _ := json.Marshal(nss[0])
	fitRules(nss, 2*len(one)+10)
	if nss[0].Truncated || nss[1].Truncated || !nss[2].Truncated || nss[2].Rules != nil {
		t.Fatalf("budget: truncated %v %v %v", nss[0].Truncated, nss[1].Truncated, nss[2].Truncated)
	}
}
