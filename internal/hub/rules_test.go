package hub

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/protocol"
)

// ---- an RBAC reference ------------------------------------------------------

// The reference below is a direct port of Kubernetes' RBAC authorizer
// (pkg/apis/rbac/v1/evaluation_helpers.go and the rule resolver): a
// request is allowed when one rule of one binding whose subject matches
// allows it. It is written separately from rules.go on purpose.

const allAuthenticated = "system:authenticated"

func refVerbMatches(r protocol.ResourceRule, verb string) bool {
	for _, v := range r.Verbs {
		if v == "*" || v == verb {
			return true
		}
	}
	return false
}

func refGroupMatches(r protocol.ResourceRule, group string) bool {
	for _, g := range r.APIGroups {
		if g == "*" || g == group {
			return true
		}
	}
	return false
}

func refResourceMatches(r protocol.ResourceRule, combined, sub string) bool {
	for _, res := range r.Resources {
		if res == "*" {
			return true
		}
		if res == combined {
			return true
		}
		if len(sub) == 0 {
			continue
		}
		if len(res) == len(sub)+2 && strings.HasPrefix(res, "*/") && strings.HasSuffix(res, sub) {
			return true
		}
	}
	return false
}

func refNameMatches(r protocol.ResourceRule, name string) bool {
	if len(r.ResourceNames) == 0 {
		return true
	}
	return slices.Contains(r.ResourceNames, name)
}

func refRuleAllows(r protocol.ResourceRule, c protocol.AccessCheck) bool {
	combined := c.Resource
	if c.Subresource != "" {
		combined = c.Resource + "/" + c.Subresource
	}
	return refVerbMatches(r, c.Verb) && refGroupMatches(r, c.Group) && refResourceMatches(r, combined, c.Subresource) && refNameMatches(r, c.Name)
}

type refSubject struct {
	group bool
	name  string
}

type refBinding struct {
	namespace string // "" for a ClusterRoleBinding
	subjects  []refSubject
	role      int
}

// refCluster is one cluster's RBAC objects, plus how its rules reviews
// misbehave per namespace.
type refCluster struct {
	roles    [][]protocol.ResourceRule
	bindings []refBinding
	// review flags per namespace
	incomplete, evalErr, truncated, failed map[string]bool
}

func (rc *refCluster) matches(b refBinding, user string, groups []string) bool {
	for _, s := range b.subjects {
		if (!s.group && s.name == user) || (s.group && slices.Contains(groups, s.name)) {
			return true
		}
	}
	return false
}

// rulesFor is the RBAC rule resolver: the rules of every ClusterRoleBinding
// and of every RoleBinding in namespace whose subject matches.
func (rc *refCluster) rulesFor(user string, groups []string, namespace string) []protocol.ResourceRule {
	var out []protocol.ResourceRule
	for _, b := range rc.bindings {
		if (b.namespace == "" || b.namespace == namespace) && rc.matches(b, user, groups) {
			out = append(out, rc.roles[b.role]...)
		}
	}
	return out
}

// sar is a SubjectAccessReview for exactly user and groups. A namespace
// whose review is incomplete also has a webhook authorizer ahead of RBAC
// that denies every Secret there, as only an authorizer that cannot list
// its rules can deny.
func (rc *refCluster) sar(user string, groups []string, c protocol.AccessCheck) bool {
	if rc.incomplete[c.Namespace] && c.Resource == "secrets" {
		return false
	}
	for _, r := range rc.rulesFor(user, groups, c.Namespace) {
		if refRuleAllows(r, c) {
			return true
		}
	}
	return false
}

// review is a SelfSubjectRulesReview of an impersonated identity: the
// API server adds system:authenticated to its groups.
func (rc *refCluster) review(user string, groups []string, namespace string) protocol.NamespaceRules {
	nr := protocol.NamespaceRules{Namespace: namespace}
	if rc.failed[namespace] {
		nr.Error = "forbidden"
		return nr
	}
	nr.Rules = rc.rulesFor(user, append(slices.Clone(groups), allAuthenticated), namespace)
	if rc.incomplete[namespace] {
		nr.Incomplete = true
		if len(nr.Rules) > 0 && namespace < "ns-c" {
			nr.Rules = nr.Rules[:len(nr.Rules)/2] // a partial enumeration
		}
	}
	if rc.evalErr[namespace] {
		nr.EvaluationError = "clusterrole \"gone\" not found"
		nr.Rules = nil
	}
	if rc.truncated[namespace] {
		nr.Truncated, nr.Rules = true, nil
	}
	return nr
}

// ---- random RBAC ------------------------------------------------------------

var (
	propVerbs      = []string{"get", "list", "watch", "patch", "*"}
	propGroups     = []string{"", "apps", "kustomize.toolkit.fluxcd.io", "other.example.com", "*"}
	propResources  = []string{"pods", "deployments", "kustomizations", "secrets", "pods/log", "deployments/status", "*/status", "*/log", "*", "pods/*"}
	propNames      = []string{"a", "b", ""}
	propNamespaces = []string{"ns-a", "ns-b", "ns-c", "ns-d"}
	propUsers      = []string{"alice", "bob", rulesBaselineUser}
	propGroupNames = []string{"eddy:dev", "eddy:ops", "eddy:viewers", allAuthenticated}
)

func pick[T any](r *rand.Rand, xs []T) T { return xs[r.IntN(len(xs))] }

func pickSome(r *rand.Rand, xs []string, max int) []string {
	n := 1 + r.IntN(max)
	out := make([]string, 0, n)
	for range n {
		x := pick(r, xs)
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func randomRule(r *rand.Rand) protocol.ResourceRule {
	rule := protocol.ResourceRule{
		Verbs:     pickSome(r, propVerbs, 3),
		APIGroups: pickSome(r, propGroups, 2),
		Resources: pickSome(r, propResources, 3),
	}
	if r.IntN(4) == 0 {
		rule.ResourceNames = pickSome(r, propNames, 2)
	}
	if r.IntN(20) == 0 {
		rule.Verbs = nil // a rule that matches nothing
	}
	return rule
}

func randomCluster(r *rand.Rand) *refCluster {
	rc := &refCluster{
		incomplete: map[string]bool{}, evalErr: map[string]bool{}, truncated: map[string]bool{}, failed: map[string]bool{},
	}
	for range 1 + r.IntN(6) {
		var rules []protocol.ResourceRule
		for range r.IntN(4) {
			rules = append(rules, randomRule(r))
		}
		rc.roles = append(rc.roles, rules)
	}
	for range r.IntN(8) {
		b := refBinding{role: r.IntN(len(rc.roles))}
		if r.IntN(3) > 0 {
			b.namespace = pick(r, propNamespaces)
		}
		for range 1 + r.IntN(2) {
			if r.IntN(2) == 0 {
				b.subjects = append(b.subjects, refSubject{name: pick(r, propUsers)})
			} else {
				b.subjects = append(b.subjects, refSubject{group: true, name: pick(r, propGroupNames)})
			}
		}
		rc.bindings = append(rc.bindings, b)
	}
	for _, ns := range propNamespaces {
		rc.incomplete[ns] = r.IntN(8) == 0
		rc.evalErr[ns] = r.IntN(10) == 0
		rc.truncated[ns] = r.IntN(12) == 0
		rc.failed[ns] = r.IntN(12) == 0
	}
	return rc
}

func randomCheck(r *rand.Rand) protocol.AccessCheck {
	c := protocol.AccessCheck{
		Verb:     pick(r, []string{"get", "list", "watch", "list", "patch"}),
		Group:    pick(r, propGroups[:4]),
		Resource: pick(r, []string{"pods", "deployments", "kustomizations", "secrets"}),
	}
	if r.IntN(5) == 0 {
		c.Subresource = pick(r, []string{"log", "status"})
	}
	if c.Verb == "get" && r.IntN(2) == 0 {
		c.Name = pick(r, []string{"a", "b", "c"})
	}
	if r.IntN(6) > 0 {
		c.Namespace = pick(r, propNamespaces)
	}
	return c
}

// propAuthorizer is an authorizer over rc: SARs from the reference, rules
// reviews from the simulated API server. local makes the agent review as
// its own identity (SSARs), so the reference includes
// system:authenticated for both.
func propAuthorizer(rc *refCluster, local bool) (*authorizer, *atomic.Int64) {
	var sars atomic.Int64
	groupsOf := func(id protocol.Identity) []string {
		if local {
			return append(slices.Clone(id.Groups), allAuthenticated)
		}
		return id.Groups
	}
	a := newAuthorizer(func(_ context.Context, _ string, id protocol.Identity, checks []protocol.AccessCheck) ([]bool, error) {
		sars.Add(int64(len(checks)))
		out := make([]bool, len(checks))
		for i, c := range checks {
			out[i] = rc.sar(id.User, groupsOf(id), c)
		}
		return out, nil
	}, newMetrics())
	a.rules = func(_ context.Context, _ string, id protocol.Identity, namespaces []string) ([]protocol.NamespaceRules, error) {
		out := make([]protocol.NamespaceRules, len(namespaces))
		for i, ns := range namespaces {
			out[i] = rc.review(id.User, id.Groups, ns)
		}
		return out, nil
	}
	a.selfReview = func(string) bool { return local }
	return a, &sars
}

// TestRulesNeverOverGrant is the property test: for randomized RBAC, review
// failures and checks, the authorizer's answer (rules where exact, else
// SARs) equals a SubjectAccessReview of the reference for exactly the
// user and groups. Above all it never allows what the reference denies.
func TestRulesNeverOverGrant(t *testing.T) {
	const clusters = 3000
	r := rand.New(rand.NewPCG(20261001, 6))
	var answered, fallbacks, checked int
	for ci := range clusters {
		rc := randomCluster(r)
		for _, local := range []bool{false, true} {
			a, _ := propAuthorizer(rc, local)
			for range 3 {
				user := pick(r, propUsers[:2])
				var groups []string
				for _, g := range propGroupNames[:3] {
					if r.IntN(2) == 0 {
						groups = append(groups, g)
					}
				}
				p := identity.Principal{User: user, Groups: groups}
				checks := make([]protocol.AccessCheck, 1+r.IntN(30))
				for i := range checks {
					checks[i] = randomCheck(r)
				}
				got, err := a.check(context.Background(), p, "c", checks)
				if err != nil {
					t.Fatal(err)
				}
				refGroups := groups
				if local {
					refGroups = append(slices.Clone(groups), allAuthenticated)
				}
				for i, c := range checks {
					checked++
					if got[i] && !rc.sar(user, refGroups, c) {
						t.Fatalf("OVER-GRANT cluster %d (local %v): %s %v: %+v allowed, reference denies\nroles %+v\nbindings %+v",
							ci, local, user, groups, c, rc.roles, rc.bindings)
					}
				}
				for i, c := range checks {
					if want := rc.sar(user, refGroups, c); got[i] != want {
						t.Fatalf("cluster %d (local %v): %s %v: %+v = %v, reference %v", ci, local, user, groups, c, got[i], want)
					}
				}
			}
			answered += int(a.metrics.rulesAnswered.Load())
			fallbacks += int(a.metrics.rulesFallbacks.Load())
		}
	}
	t.Logf("%d checks: %d answered from rules, %d fell back to SARs", checked, answered, fallbacks)
	if answered < checked/4 || fallbacks == 0 {
		t.Fatalf("the property test does not exercise both paths: %d answered, %d fallbacks of %d", answered, fallbacks, checked)
	}
}

// ---- RBAC semantics, table-driven -------------------------------------------

func TestRuleAllows(t *testing.T) {
	rule := func(verbs, groups, resources, names string) protocol.ResourceRule {
		split := func(s string) []string {
			if s == "-" {
				return nil
			}
			return strings.Split(s, ",")
		}
		return protocol.ResourceRule{Verbs: split(verbs), APIGroups: split(groups), Resources: split(resources), ResourceNames: split(names)}
	}
	list := func(group, resource string) protocol.AccessCheck {
		return protocol.AccessCheck{Verb: "list", Group: group, Resource: resource, Namespace: "ns"}
	}
	get := func(group, resource, sub, name string) protocol.AccessCheck {
		return protocol.AccessCheck{Verb: "get", Group: group, Resource: resource, Subresource: sub, Namespace: "ns", Name: name}
	}
	cases := []struct {
		name string
		rule protocol.ResourceRule
		c    protocol.AccessCheck
		want bool
	}{
		{"exact", rule("list", "apps", "deployments", "-"), list("apps", "deployments"), true},
		{"other verb", rule("get", "apps", "deployments", "-"), list("apps", "deployments"), false},
		{"other group", rule("list", "", "deployments", "-"), list("apps", "deployments"), false},
		{"core group is the empty string", rule("list", "", "pods", "-"), list("", "pods"), true},
		{"star verb", rule("*", "apps", "deployments", "-"), list("apps", "deployments"), true},
		{"star group", rule("list", "*", "deployments", "-"), list("apps", "deployments"), true},
		{"star resource", rule("list", "apps", "*", "-"), list("apps", "statefulsets"), true},
		{"star resource covers subresources", rule("get", "", "*", "-"), get("", "pods", "log", "p"), true},
		{"resource rule does not cover its subresource", rule("get", "", "pods", "-"), get("", "pods", "log", "p"), false},
		{"subresource rule does not cover the resource", rule("list", "", "pods/log", "-"), list("", "pods"), false},
		{"exact subresource", rule("get", "", "pods/log", "-"), get("", "pods", "log", "p"), true},
		{"star subresource", rule("get", "", "*/log", "-"), get("", "pods", "log", "p"), true},
		{"star subresource, other subresource", rule("get", "", "*/log", "-"), get("", "pods", "status", "p"), false},
		{"pods/* is not a wildcard", rule("get", "", "pods/*", "-"), get("", "pods", "log", "p"), false},
		{"names never grant list", rule("list", "apps", "deployments", "web"), list("apps", "deployments"), false},
		{"names grant get of that name", rule("get", "apps", "deployments", "web"), get("apps", "deployments", "", "web"), true},
		{"names deny get of another name", rule("get", "apps", "deployments", "web"), get("apps", "deployments", "", "api"), false},
		{"names deny get without a name", rule("get", "apps", "deployments", "web"), get("apps", "deployments", "", ""), false},
		{"empty verbs match nothing", rule("-", "*", "*", "-"), list("apps", "deployments"), false},
		{"empty groups match nothing", rule("*", "-", "*", "-"), list("apps", "deployments"), false},
		{"empty resources match nothing", rule("*", "*", "-", "-"), list("apps", "deployments"), false},
		{"verbs are case-sensitive", rule("LIST", "apps", "deployments", "-"), list("apps", "deployments"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ruleAllows(&tc.rule, tc.c); got != tc.want {
				t.Fatalf("ruleAllows = %v, want %v", got, tc.want)
			}
			if ref := refRuleAllows(tc.rule, tc.c); ref != tc.want && tc.c.Name != "" {
				t.Fatalf("the reference says %v", ref)
			}
		})
	}
}

func TestCompileRulesRefusesInexactReviews(t *testing.T) {
	ok := []protocol.ResourceRule{{Verbs: []string{"list"}, APIGroups: []string{""}, Resources: []string{"pods"}}}
	cases := []struct {
		name   string
		nr     protocol.NamespaceRules
		usable bool
	}{
		{"complete", protocol.NamespaceRules{Rules: ok}, true},
		{"no rules is a complete deny", protocol.NamespaceRules{}, true},
		{"incomplete", protocol.NamespaceRules{Rules: ok, Incomplete: true}, false},
		{"evaluation error", protocol.NamespaceRules{Rules: ok, EvaluationError: "x"}, false},
		{"truncated", protocol.NamespaceRules{Truncated: true}, false},
		{"failed", protocol.NamespaceRules{Error: "forbidden"}, false},
		{"too many rules", protocol.NamespaceRules{Rules: make([]protocol.ResourceRule, protocol.MaxRulesPerNamespace+1)}, false},
		{"empty resource name", protocol.NamespaceRules{Rules: []protocol.ResourceRule{{Verbs: []string{"list"}, APIGroups: []string{""}, Resources: []string{"pods"}, ResourceNames: []string{""}}}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := compileRules(tc.nr) != nil; got != tc.usable {
				t.Fatalf("usable = %v, want %v", got, tc.usable)
			}
		})
	}
}

// ---- the authorizer with rules ----------------------------------------------

// nsPolicy: eddy:<ns> may read everything in <ns>; nothing cluster-wide.
func nsPolicy() *refCluster {
	rc := &refCluster{
		roles: [][]protocol.ResourceRule{
			{{Verbs: []string{"get", "list", "watch"}, APIGroups: []string{"*"}, Resources: []string{"*"}}},
			{{Verbs: []string{"create"}, APIGroups: []string{"authorization.k8s.io"}, Resources: []string{"selfsubjectrulesreviews", "selfsubjectaccessreviews"}}},
		},
		bindings:   []refBinding{{subjects: []refSubject{{group: true, name: allAuthenticated}}, role: 1}},
		incomplete: map[string]bool{}, evalErr: map[string]bool{}, truncated: map[string]bool{}, failed: map[string]bool{},
	}
	for i := range 20 {
		ns := fmt.Sprintf("ns-%02d", i)
		rc.bindings = append(rc.bindings, refBinding{namespace: ns, subjects: []refSubject{{group: true, name: "eddy:" + ns}}, role: 0})
	}
	return rc
}

// TestRulesCutChecks: a namespace-scoped user's list filter needs one SAR
// per kind (the cluster-scope question) and one rules review per namespace,
// instead of one SAR per (kind, namespace).
func TestRulesCutChecks(t *testing.T) {
	rc := nsPolicy()
	a, sars := propAuthorizer(rc, false)
	var reviews atomic.Int64
	inner := a.rules
	a.rules = func(ctx context.Context, cluster string, id protocol.Identity, nss []string) ([]protocol.NamespaceRules, error) {
		reviews.Add(int64(len(nss)))
		return inner(ctx, cluster, id, nss)
	}
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-03", "eddy:ns-07"}}
	rows := shortcutRows()
	got, err := a.filter(context.Background(), p, "dev", slices.Clone(rows))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Namespace != "ns-03" && r.Namespace != "ns-07" {
			t.Fatalf("visible: %s", r.ID)
		}
	}
	if len(got) != 4 {
		t.Fatalf("%d visible, want 4", len(got))
	}
	// 3 kinds cluster-wide; 20 user reviews plus the baseline of the 2
	// namespaces where the user's rules allow.
	if sars.Load() != 3 || reviews.Load() != 22 {
		t.Fatalf("%d SARs, %d rules reviews; want 3 and 22", sars.Load(), reviews.Load())
	}
	// Warm: nothing is asked again; a second user shares the baseline.
	if _, err := a.filter(context.Background(), p, "dev", slices.Clone(rows)); err != nil {
		t.Fatal(err)
	}
	if sars.Load() != 3 || reviews.Load() != 22 {
		t.Fatalf("warm: %d SARs, %d rules reviews", sars.Load(), reviews.Load())
	}
	q := identity.Principal{User: "v", Groups: []string{"eddy:ns-03"}}
	if _, err := a.filter(context.Background(), q, "dev", slices.Clone(rows)); err != nil {
		t.Fatal(err)
	}
	if sars.Load() != 6 || reviews.Load() != 42 {
		t.Fatalf("second user: %d SARs, %d rules reviews; want 6 and 42", sars.Load(), reviews.Load())
	}
	if n := a.size(); n != 6 {
		t.Fatalf("SAR cache holds %d answers, want only the 6 cluster-scope ones", n)
	}
}

// TestRulesAuthenticatedGrantFallsBack: a grant to every authenticated user
// appears in an impersonated rules review but not in the hub's SAR. The
// baseline catches it and the SAR decides.
func TestRulesAuthenticatedGrantFallsBack(t *testing.T) {
	rc := nsPolicy()
	rc.bindings = append(rc.bindings, refBinding{namespace: "ns-01", subjects: []refSubject{{group: true, name: allAuthenticated}}, role: 0})
	a, sars := propAuthorizer(rc, false)
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-02"}}
	checks := []protocol.AccessCheck{
		{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-01"},
		{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-02"},
	}
	got, err := a.check(context.Background(), p, "dev", checks)
	if err != nil {
		t.Fatal(err)
	}
	if got[0] || !got[1] {
		t.Fatalf("got %v, want [false true]", got)
	}
	if sars.Load() != 1 {
		t.Fatalf("%d SARs, want 1 (ns-01, where the baseline allows)", sars.Load())
	}
}

// TestRulesOldAgent: an agent that answers 400 to OpRules is asked SARs
// only, and is not asked for rules again for rulesOffTTL.
func TestRulesOldAgent(t *testing.T) {
	rc := nsPolicy()
	a, sars := propAuthorizer(rc, false)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	var calls atomic.Int64
	a.rules = func(context.Context, string, protocol.Identity, []string) ([]protocol.NamespaceRules, error) {
		calls.Add(1)
		return nil, &AgentError{Cluster: "dev", Code: 400, Message: `agent: unsupported kind /`}
	}
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-02"}}
	c := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-02"}}
	for i := range 3 {
		c[0].Namespace = fmt.Sprintf("ns-%02d", i+1)
		got, err := a.check(context.Background(), p, "dev", c)
		if err != nil || got[0] != (i == 1) {
			t.Fatalf("check %d: %v %v", i, got, err)
		}
	}
	if calls.Load() != 1 || sars.Load() != 3 {
		t.Fatalf("%d rules calls, %d SARs; want 1 and 3", calls.Load(), sars.Load())
	}
	now = now.Add(rulesOffTTL)
	c[0].Namespace = "ns-09"
	if _, err := a.check(context.Background(), p, "dev", c); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("not retried after rulesOffTTL: %d calls", calls.Load())
	}
}

// TestRulesErrorsFallBack: a failed rules request (timeout, refusal) is
// never cached and never grants; SARs decide.
func TestRulesErrorsFallBack(t *testing.T) {
	rc := nsPolicy()
	a, sars := propAuthorizer(rc, false)
	a.rules = func(context.Context, string, protocol.Identity, []string) ([]protocol.NamespaceRules, error) {
		return nil, errors.New("hub: agent dev did not answer")
	}
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-02"}}
	c := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-02"}, {Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-03"}}
	got, err := a.check(context.Background(), p, "dev", c)
	if err != nil || !got[0] || got[1] || sars.Load() != 2 {
		t.Fatalf("got %v %v with %d SARs", got, err, sars.Load())
	}
	a.mu.Lock()
	n := len(a.rc.entries)
	a.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d failed reviews cached", n)
	}
}

// TestRulesStale: a disconnected cluster's expired rules answer reads for
// at most staleTTL after the disconnect; never-fetched namespaces fall back
// to SARs, which fail closed.
func TestRulesStale(t *testing.T) {
	rc := nsPolicy()
	a, _ := propAuthorizer(rc, false)
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	inner := a.rules
	var down bool
	disconnected := fmt.Errorf("%w: dev", fleet.ErrDisconnected)
	a.rules = func(ctx context.Context, cluster string, id protocol.Identity, nss []string) ([]protocol.NamespaceRules, error) {
		if down {
			return nil, disconnected
		}
		return inner(ctx, cluster, id, nss)
	}
	sar := a.send
	a.send = func(ctx context.Context, cluster string, id protocol.Identity, cs []protocol.AccessCheck) ([]bool, error) {
		if down {
			return nil, disconnected
		}
		return sar(ctx, cluster, id, cs)
	}
	var since time.Time
	a.staleSince = func(string) (time.Time, bool) { return since, down }
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-02"}}
	known := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-02"}}
	if got, err := a.check(context.Background(), p, "dev", known); err != nil || !got[0] {
		t.Fatalf("live: %v %v", got, err)
	}
	down, since = true, now.Add(10*time.Second)
	now = now.Add(accessTTL + 20*time.Second)
	if got, err := a.check(context.Background(), p, "dev", known); err != nil || !got[0] {
		t.Fatalf("stale, known: %v %v", got, err)
	}
	unknown := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-05"}}
	if _, err := a.check(context.Background(), p, "dev", unknown); err == nil {
		t.Fatal("a namespace never reviewed was answered while the agent is gone")
	}
	now = since.Add(defaultStaleAccessTTL)
	if _, err := a.check(context.Background(), p, "dev", known); err == nil {
		t.Fatal("rules were used staleAccessTTL after the disconnect")
	}
}

// TestRulesCacheBounds: identical rule sets are stored once; the entry and
// rule bounds hold.
func TestRulesCacheBounds(t *testing.T) {
	c := newRulesCache()
	c.maxEnt, c.maxRules = 100, 10
	now := time.Unix(1000, 0)
	set := func(n int, verb string) *ruleSet {
		rules := make([]protocol.ResourceRule, n)
		for i := range rules {
			rules[i] = protocol.ResourceRule{Verbs: []string{verb}, APIGroups: []string{""}, Resources: []string{fmt.Sprint(i)}}
		}
		return compileRules(protocol.NamespaceRules{Rules: rules})
	}
	for i := range 50 {
		c.put(rulesKey{cluster: "c", subject: "s", namespace: fmt.Sprint(i)}, set(4, "get"), now.Add(time.Minute), now)
	}
	if len(c.entries) != 50 || len(c.sets) != 1 || c.held != 4 {
		t.Fatalf("interning: %d entries, %d sets, %d rules held", len(c.entries), len(c.sets), c.held)
	}
	for i := range 200 {
		c.put(rulesKey{cluster: "c", subject: "t", namespace: fmt.Sprint(i)}, set(4, fmt.Sprint(i)), now.Add(time.Minute), now)
		if len(c.entries) > c.maxEnt || c.held > c.maxRules {
			t.Fatalf("bounds broken: %d entries, %d rules", len(c.entries), c.held)
		}
	}
	held := 0
	for _, in := range c.sets {
		held += len(in.set.rules)
	}
	refs := 0
	for _, in := range c.sets {
		refs += in.refs
	}
	if held != c.held || refs > len(c.entries) {
		t.Fatalf("accounting: held %d vs %d, refs %d vs %d entries", held, c.held, refs, len(c.entries))
	}
}

// TestRulesSingleflight: concurrent identical questions share one review.
func TestRulesSingleflight(t *testing.T) {
	rc := nsPolicy()
	a, _ := propAuthorizer(rc, false)
	inner := a.rules
	var calls atomic.Int64
	gate := make(chan struct{})
	a.rules = func(ctx context.Context, cluster string, id protocol.Identity, nss []string) ([]protocol.NamespaceRules, error) {
		if id.User != rulesBaselineUser {
			calls.Add(int64(len(nss)))
		}
		<-gate
		return inner(ctx, cluster, id, nss)
	}
	p := identity.Principal{User: "u", Groups: []string{"eddy:ns-02"}}
	c := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "ns-02"}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			got, err := a.check(context.Background(), p, "dev", c)
			if err != nil || !got[0] {
				t.Errorf("got %v %v", got, err)
			}
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("%d reviews for one question", calls.Load())
	}
}
