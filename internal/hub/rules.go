package hub

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/protocol"
)

// Rules reviews (ADR-0006 P1.1). A user without cluster-wide read needs one
// SubjectAccessReview per (kind, namespace). Instead, the hub asks the agent
// for one SelfSubjectRulesReview per (user, namespace), created as the
// impersonated user, and answers every namespaced read check of that
// namespace from the rules.
//
// Two facts make a rules answer differ from a SubjectAccessReview, and the
// hub accounts for both so that a rules answer never allows what the SAR
// would deny:
//
//   - Impersonation adds the group system:authenticated, which the hub's
//     SubjectAccessReviews do not carry. A rule granted to every
//     authenticated user would show up in the user's rules. So an allow is
//     taken from the rules only when the baseline rules, those of a user
//     with no bindings and no groups but system:authenticated
//     (rulesBaselineUser), do not allow the same check. Otherwise the check
//     goes to a SubjectAccessReview. Local-mode agents review as their own
//     kubeconfig identity for both SARs and rules, so no baseline is needed.
//   - A review is only an enumeration of RBAC (and other rule-listing
//     authorizers). When the review is incomplete (a webhook, or any
//     authorizer that cannot list rules, which is also every authorizer
//     that can deny), reports an evaluation error, or was truncated, the
//     namespace is answered by SubjectAccessReviews.
//
// A deny from complete rules is exact: the review's subject (the user's
// groups plus system:authenticated) is a superset of the SAR's, and RBAC is
// additive, so a check no rule allows is denied by the SAR too.
const (
	// rulesBaselineUser is the user whose rules are the baseline. It is not
	// a real user; bindings to it only make the baseline larger, which
	// sends more checks to SubjectAccessReviews and never grants more.
	rulesBaselineUser = "eddy:rules-baseline"
	// rulesMaxEntries bounds the cached reviews, apart from the SAR cache:
	// about 150 B each, so ~145 MiB when full. A user without cluster-wide
	// read needs one per namespace per cluster (20k at 100 × 200), so this
	// holds 50 such users at that scale.
	rulesMaxEntries = 1_000_000
	// rulesMaxRules bounds the rules held by the cache. Identical rule
	// sets (most namespaces of one user) are stored once.
	rulesMaxRules = 1_000_000
	// rulesOffTTL is how long a cluster whose agent does not know OpRules
	// is answered by SubjectAccessReviews only before the hub tries again.
	rulesOffTTL = 5 * time.Minute
	// rulesParallel bounds the rules requests of one fetch in flight.
	rulesParallel = 4
)

// rulesSender asks a cluster's agent for the rules of id in namespaces
// (at most protocol.MaxRulesNamespaces).
type rulesSender func(ctx context.Context, cluster string, id protocol.Identity, namespaces []string) ([]protocol.NamespaceRules, error)

// ruleSet is the complete resource rules of a subject in one namespace.
type ruleSet struct {
	rules []protocol.ResourceRule
	hash  [32]byte
}

// compileRules returns the rules of nr, or nil when they must not answer
// checks: an incomplete, failed or truncated review, or a rule whose
// resourceNames holds the empty name (which RBAC would match against a
// request without a name, such as list).
func compileRules(nr protocol.NamespaceRules) *ruleSet {
	if nr.Incomplete || nr.EvaluationError != "" || nr.Truncated || nr.Error != "" || len(nr.Rules) > protocol.MaxRulesPerNamespace {
		return nil
	}
	for _, r := range nr.Rules {
		if slices.Contains(r.ResourceNames, "") {
			return nil
		}
	}
	b, err := json.Marshal(nr.Rules)
	if err != nil {
		return nil
	}
	return &ruleSet{rules: nr.Rules, hash: sha256.Sum256(b)}
}

// allows reports whether one rule of s allows c, with Kubernetes RBAC
// semantics (rbac.RuleAllows): each of verb, API group, resource and name
// must match within the same rule.
func (s *ruleSet) allows(c protocol.AccessCheck) bool {
	for i := range s.rules {
		if ruleAllows(&s.rules[i], c) {
			return true
		}
	}
	return false
}

func ruleAllows(r *protocol.ResourceRule, c protocol.AccessCheck) bool {
	return matchesOrStar(r.Verbs, c.Verb) &&
		matchesOrStar(r.APIGroups, c.Group) &&
		resourceMatches(r.Resources, c.Resource, c.Subresource) &&
		resourceNameMatches(r.ResourceNames, c.Name)
}

// matchesOrStar is RBAC's VerbMatches and APIGroupMatches.
func matchesOrStar(have []string, want string) bool {
	for _, h := range have {
		if h == "*" || h == want {
			return true
		}
	}
	return false
}

// resourceMatches is RBAC's ResourceMatches: "*" matches every resource and
// subresource, "<resource>/<sub>" exactly that subresource, and "*/<sub>"
// that subresource of every resource. A rule for a resource never matches
// its subresources, and a subresource rule never matches the resource.
func resourceMatches(have []string, resource, sub string) bool {
	combined := resource
	if sub != "" {
		combined = resource + "/" + sub
	}
	for _, h := range have {
		if h == "*" || h == combined {
			return true
		}
		if sub != "" && h == "*/"+sub {
			return true
		}
	}
	return false
}

// resourceNameMatches is RBAC's ResourceNameMatches: a rule without names
// matches every request, a rule with names only requests for one of them.
// A request without a name (list, watch of a collection) never matches a
// rule with names.
func resourceNameMatches(have []string, name string) bool {
	if len(have) == 0 {
		return true
	}
	return name != "" && slices.Contains(have, name)
}

// rulesEligible reports whether c may be answered from a namespace's rules:
// a read of a namespaced resource in a valid namespace. Cluster-scoped and
// all-namespace checks (namespace "") stay SubjectAccessReviews.
func rulesEligible(c protocol.AccessCheck) bool {
	switch c.Verb {
	case "get", "list", "watch":
	default:
		return false
	}
	return c.Namespace != "" && c.Resource != "" && len(validation.IsDNS1123Label(c.Namespace)) == 0
}

type rulesKey struct {
	cluster   string
	subject   string
	namespace string
}

// rulesEntry is a cached review. set is nil when the namespace must be
// answered by SubjectAccessReviews.
type rulesEntry struct {
	set     *ruleSet
	expires int64 // unix nanoseconds, to keep entries small
}

type rulesCall struct {
	done chan struct{}
	set  *ruleSet
}

type internedSet struct {
	set  *ruleSet
	refs int
}

// rulesCache is the authorizer's cache of reviews, apart from its SAR
// answers. Its fields are guarded by authorizer.mu.
type rulesCache struct {
	entries  map[rulesKey]rulesEntry
	inflight map[rulesKey]*rulesCall
	sets     map[[32]byte]*internedSet
	held     int                  // rules in sets
	off      map[string]time.Time // cluster → until
	maxEnt   int
	maxRules int
}

func newRulesCache() rulesCache {
	return rulesCache{
		entries: map[rulesKey]rulesEntry{}, inflight: map[rulesKey]*rulesCall{},
		sets: map[[32]byte]*internedSet{}, off: map[string]time.Time{},
		maxEnt: rulesMaxEntries, maxRules: rulesMaxRules,
	}
}

// put stores set (interned) under k, replacing any entry. Called with
// authorizer.mu held.
func (c *rulesCache) put(k rulesKey, set *ruleSet, expires time.Time, now time.Time) {
	c.drop(k)
	r := 0
	if set != nil && c.sets[set.hash] == nil {
		r = len(set.rules)
	}
	c.makeRoom(1, r, now)
	if set != nil {
		if in := c.sets[set.hash]; in != nil {
			in.refs++
			set = in.set
		} else {
			c.sets[set.hash] = &internedSet{set: set, refs: 1}
			c.held += len(set.rules)
		}
	}
	c.entries[k] = rulesEntry{set: set, expires: expires.UnixNano()}
}

// drop removes k. Called with authorizer.mu held.
func (c *rulesCache) drop(k rulesKey) {
	e, ok := c.entries[k]
	if !ok {
		return
	}
	delete(c.entries, k)
	if e.set == nil {
		return
	}
	if in := c.sets[e.set.hash]; in != nil {
		if in.refs--; in.refs <= 0 {
			delete(c.sets, e.set.hash)
			c.held -= len(in.set.rules)
		}
	}
}

// makeRoom evicts expired entries, then arbitrary ones, until n more
// entries and r more rules fit. Called with authorizer.mu held.
func (c *rulesCache) makeRoom(n, r int, now time.Time) {
	fits := func() bool { return len(c.entries)+n <= c.maxEnt && c.held+r <= c.maxRules }
	if fits() {
		return
	}
	for k, e := range c.entries {
		if now.UnixNano() >= e.expires {
			c.drop(k)
		}
	}
	for k := range c.entries {
		if fits() {
			return
		}
		c.drop(k)
	}
}

// rulesFor returns the rule sets of id in namespaces (distinct), from the
// cache or the agent; a nil set means "ask SubjectAccessReviews". Only a
// cancelled ctx is an error.
func (a *authorizer) rulesFor(ctx context.Context, id protocol.Identity, subj, cluster string, namespaces []string) (map[string]*ruleSet, error) {
	now := a.now()
	out := make(map[string]*ruleSet, len(namespaces))
	waits := map[string]*rulesCall{}
	owned := map[string]*rulesCall{}
	var mine []string
	var hits uint64
	a.mu.Lock()
	for _, ns := range namespaces {
		k := rulesKey{cluster: cluster, subject: subj, namespace: ns}
		if e, ok := a.rc.entries[k]; ok && now.UnixNano() < e.expires {
			out[ns] = e.set
			hits++
			continue
		}
		call, running := a.rc.inflight[k]
		if !running {
			call = &rulesCall{done: make(chan struct{})}
			a.rc.inflight[k] = call
			owned[ns] = call
			mine = append(mine, ns)
		}
		waits[ns] = call
	}
	a.mu.Unlock()
	a.metrics.rulesHits.Add(hits)
	a.metrics.rulesMisses.Add(uint64(len(mine)))
	if len(mine) > 0 {
		a.fetchRules(ctx, id, subj, cluster, mine, owned)
	}
	for ns, call := range waits {
		select {
		case <-call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		out[ns] = call.set
	}
	return out, nil
}

// fetchRules asks the agent for namespaces (owned by this caller) in
// batches and completes their calls, detached from the caller's
// cancellation like resolve.
func (a *authorizer) fetchRules(ctx context.Context, id protocol.Identity, subj, cluster string, namespaces []string, owned map[string]*rulesCall) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessTimeout)
	defer cancel()
	var wg sync.WaitGroup
	sem := make(chan struct{}, rulesParallel)
	for start := 0; start < len(namespaces); start += protocol.MaxRulesNamespaces {
		batch := namespaces[start:min(start+protocol.MaxRulesNamespaces, len(namespaces))]
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			res, err := a.rules(ctx, cluster, id, batch)
			a.storeRules(cluster, subj, batch, res, err, owned)
		})
	}
	wg.Wait()
}

func (a *authorizer) storeRules(cluster, subj string, batch []string, res []protocol.NamespaceRules, err error, owned map[string]*rulesCall) {
	byNS := make(map[string]protocol.NamespaceRules, len(res))
	for _, nr := range res {
		byNS[nr.Namespace] = nr
	}
	now := a.now()
	expires := now.Add(a.ttl)
	a.mu.Lock()
	defer a.mu.Unlock()
	if err != nil {
		if ae, ok := errors.AsType[*AgentError](err); ok && ae.Code == 400 {
			// An agent that does not know the op (or refuses the request):
			// SubjectAccessReviews only for a while.
			a.rc.off[cluster] = now.Add(rulesOffTTL)
		}
	}
	for _, ns := range batch {
		k := rulesKey{cluster: cluster, subject: subj, namespace: ns}
		call := owned[ns]
		switch {
		case err == nil:
			var set *ruleSet
			if nr, ok := byNS[ns]; ok {
				set = compileRules(nr)
			}
			a.rc.put(k, set, expires, now)
			call.set = set
		default:
			if e, ok := a.rc.entries[k]; ok && a.staleUsable(cluster, err, time.Unix(0, e.expires), now) {
				call.set = e.set
			}
		}
		delete(a.rc.inflight, k)
		close(call.done)
	}
}

// rulesOff reports whether cluster is answered by SARs only. Called with
// authorizer.mu held.
func (a *authorizer) rulesOffLocked(cluster string, now time.Time) bool {
	until, ok := a.rc.off[cluster]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(a.rc.off, cluster)
	return false
}

// answerFromRules answers the checks it can from rules reviews into out and
// returns the indices of the rest, for SubjectAccessReviews.
func (a *authorizer) answerFromRules(ctx context.Context, p identity.Principal, subj, cluster string, checks []protocol.AccessCheck, out []bool) ([]int, error) {
	all := make([]int, len(checks))
	for i := range all {
		all[i] = i
	}
	if a.rules == nil {
		return all, nil
	}
	a.mu.Lock()
	off := a.rulesOffLocked(cluster, a.now())
	a.mu.Unlock()
	if off {
		return all, nil
	}
	var namespaces []string
	seen := map[string]bool{}
	var rest []int
	var eligible []int
	for i, c := range checks {
		if !rulesEligible(c) {
			rest = append(rest, i)
			continue
		}
		eligible = append(eligible, i)
		if !seen[c.Namespace] {
			seen[c.Namespace] = true
			namespaces = append(namespaces, c.Namespace)
		}
	}
	if len(eligible) == 0 {
		return all, nil
	}
	id := protocol.Identity{User: p.User, Groups: slices.Clone(p.Groups)}
	user, err := a.rulesFor(ctx, id, subj, cluster, namespaces)
	if err != nil {
		return nil, err
	}
	// The baseline is needed only where the user's rules would allow.
	var base map[string]*ruleSet
	if a.selfReview == nil || !a.selfReview(cluster) {
		var need []string
		needSeen := map[string]bool{}
		for _, i := range eligible {
			c := checks[i]
			if s := user[c.Namespace]; s != nil && !needSeen[c.Namespace] && s.allows(c) {
				needSeen[c.Namespace] = true
				need = append(need, c.Namespace)
			}
		}
		if len(need) > 0 {
			bid := protocol.Identity{User: rulesBaselineUser}
			if base, err = a.rulesFor(ctx, bid, a.baselineSubj, cluster, need); err != nil {
				return nil, err
			}
		} else {
			base = map[string]*ruleSet{}
		}
	}
	var answered, fallback uint64
	for _, i := range eligible {
		allowed, ok := decideFromRules(checks[i], user[checks[i].Namespace], base)
		if ok {
			out[i] = allowed
			answered++
		} else {
			rest = append(rest, i)
			fallback++
		}
	}
	a.metrics.rulesAnswered.Add(answered)
	a.metrics.rulesFallbacks.Add(fallback)
	slices.Sort(rest)
	return rest, nil
}

// decideFromRules answers c from the user's rules in its namespace and,
// unless base is nil (no baseline needed), the baseline rules. ok is false
// when c must be asked as a SubjectAccessReview.
func decideFromRules(c protocol.AccessCheck, user *ruleSet, base map[string]*ruleSet) (allowed, ok bool) {
	if user == nil {
		return false, false
	}
	if !user.allows(c) {
		return false, true
	}
	if base == nil {
		return true, true
	}
	b := base[c.Namespace]
	if b == nil || b.allows(c) {
		return false, false
	}
	return true, true
}

// staleUsable reports whether an expired answer (SAR or rules) may stand in
// for one the agent cannot give: the cluster is served from a stale view,
// it lost its agent at most staleTTL ago, and the answer expired at most
// staleTTL ago.
func (a *authorizer) staleUsable(cluster string, err error, expires, now time.Time) bool {
	if a.staleTTL <= 0 || a.staleSince == nil || err == nil || !errors.Is(err, fleet.ErrDisconnected) {
		return false
	}
	since, ok := a.staleSince(cluster)
	return ok && now.Before(since.Add(a.staleTTL)) && now.Before(expires.Add(a.staleTTL))
}

// sendRules is the authorizer's rules transport: an OpRules request.
func (f *fleetService) sendRules(ctx context.Context, cluster string, id protocol.Identity, namespaces []string) ([]protocol.NamespaceRules, error) {
	s := f.agents.session(cluster)
	if s == nil {
		return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster)
	}
	args, err := json.Marshal(protocol.RulesArgs{Namespaces: namespaces})
	if err != nil {
		return nil, fmt.Errorf("hub: encode rules request: %w", err)
	}
	raw, err := s.do(ctx, protocol.Request{Op: protocol.OpRules, Identity: id, Args: args})
	if err != nil {
		return nil, err
	}
	var res protocol.RulesResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("hub: decode rules result from %s: %w", cluster, err)
	}
	return res.Namespaces, nil
}
