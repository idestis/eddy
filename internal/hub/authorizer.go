package hub

import (
	"context"
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// Authorizer cache settings (SPEC: a SAR per (group, resource, namespace),
// cached for 45 s per user). With the cluster-scope shortcut a user needs
// about one check per kind per cluster, plus one per namespace for kinds
// listed per namespace only.
const (
	accessTTL       = 45 * time.Second
	accessMaxCached = 250_000
	// maxChecksPerRequest matches the agent's limit per access request.
	maxChecksPerRequest = 100
	accessTimeout       = 15 * time.Second
)

// accessTuple is the unit of list filtering: one SubjectAccessReview
// answers for every resource of a kind in a namespace.
type accessTuple struct {
	Group     string
	Resource  string // plural
	Namespace string
}

// tupleOf maps a resource to its access tuple through the kind table.
func tupleOf(r model.Ref) (accessTuple, bool) {
	k, ok := flux.KindByName(r.Kind)
	if !ok {
		return accessTuple{}, false
	}
	return accessTuple{Group: k.Group, Resource: k.Plural, Namespace: r.Namespace}, true
}

// inventoryTuple is the access tuple of an inventory-only row's own kind:
// the kind table when (group, kind) is in it, else the known plural of an
// unwatched kind. It reports false when the plural is unknown; such a row
// is visible to whoever can see its parent.
func inventoryTuple(r model.Ref) (accessTuple, bool) {
	if k, ok := flux.KindByName(r.Kind); ok && k.Matches(r.Group, r.Kind) {
		return accessTuple{Group: k.Group, Resource: k.Plural, Namespace: r.Namespace}, true
	}
	if plural, ok := flux.InventoryPlural(r.Group, r.Kind); ok {
		return accessTuple{Group: r.Group, Resource: plural, Namespace: r.Namespace}, true
	}
	return accessTuple{}, false
}

// requiredTuples returns every tuple p must be allowed to list for r to be
// visible: the kind's own tuple for a watched resource; for an
// inventory-only row, its parent Kustomization's tuple plus the row's own
// tuple when its plural is known. ok is false when r can never be shown.
func requiredTuples(r model.Resource) (ts []accessTuple, ok bool) {
	if !r.InventoryOnly {
		t, ok := tupleOf(r.Ref)
		if !ok {
			return nil, false
		}
		return []accessTuple{t}, true
	}
	if r.Owner == nil {
		return nil, false
	}
	parent, ok := tupleOf(*r.Owner)
	if !ok {
		return nil, false
	}
	ts = []accessTuple{parent}
	if own, ok := inventoryTuple(r.Ref); ok {
		ts = append(ts, own)
	}
	return ts, true
}

func (t accessTuple) check(verb string) protocol.AccessCheck {
	return protocol.AccessCheck{Verb: verb, Group: t.Group, Resource: t.Resource, Namespace: t.Namespace}
}

type accessKey struct {
	cluster string
	subject string // hash of user and groups
	check   protocol.AccessCheck
}

type accessEntry struct {
	allowed bool
	expires time.Time
}

type accessCall struct {
	done    chan struct{}
	allowed bool
	err     error
}

// accessSender sends one batch of checks to a cluster's agent as id.
type accessSender func(ctx context.Context, cluster string, id protocol.Identity, checks []protocol.AccessCheck) ([]bool, error)

// authorizer answers "may this user do verb on resource" from the user's
// rules reviews where they are exact, else by asking the cluster's agent
// for SubjectAccessReviews. Answers are cached per
// (cluster, user and groups, check) for accessTTL, the cache is bounded,
// and concurrent identical questions share one agent round trip. Errors
// are never cached: callers fail closed.
type authorizer struct {
	send    accessSender
	metrics *metrics
	now     func() time.Time
	ttl     time.Duration
	max     int
	// rules, when set, fetches rules reviews: namespaced read checks are
	// then answered from them where that is exact (rules.go).
	rules rulesSender
	// selfReview reports whether a cluster's agent reviews as its own
	// identity (local mode), for SARs and rules alike; such a cluster needs
	// no baseline.
	selfReview func(cluster string) bool
	// staleSince reports whether a cluster is served from a stale view (its
	// agent is disconnected) and since when. Only then, and for at most
	// staleTTL, may an expired answer stand in for one the agent cannot
	// give. staleTTL 0 fails closed at once.
	staleSince func(cluster string) (time.Time, bool)
	staleTTL   time.Duration

	mu           sync.Mutex
	cache        map[accessKey]accessEntry
	inflight     map[accessKey]*accessCall
	rc           rulesCache
	baselineSubj string
}

func newAuthorizer(send accessSender, m *metrics) *authorizer {
	return &authorizer{
		send:         send,
		metrics:      m,
		now:          time.Now,
		ttl:          accessTTL,
		max:          accessMaxCached,
		staleTTL:     defaultStaleAccessTTL,
		cache:        map[accessKey]accessEntry{},
		inflight:     map[accessKey]*accessCall{},
		rc:           newRulesCache(),
		baselineSubj: subjectKey(identity.Principal{User: rulesBaselineUser}),
	}
}

// subjectKey identifies a user together with their exact groups, so a
// change of group membership never reuses old answers.
func subjectKey(p identity.Principal) string {
	g := slices.Clone(p.Groups)
	slices.Sort(g)
	h := sha256.Sum256([]byte(p.User + "\x00" + strings.Join(g, "\x00")))
	return string(h[:])
}

// check answers every check for p on cluster, in order.
func (a *authorizer) check(ctx context.Context, p identity.Principal, cluster string, checks []protocol.AccessCheck) ([]bool, error) {
	subj := subjectKey(p)
	out := make([]bool, len(checks))
	pending, err := a.answerFromRules(ctx, p, subj, cluster, checks, out)
	if err != nil {
		return nil, err
	}
	now := a.now()

	type wait struct {
		idx  []int
		call *accessCall
	}
	var (
		mine   []accessKey
		owned  = map[accessKey]*accessCall{}
		waits  = map[accessKey]*wait{}
		hits   uint64
		misses uint64
	)
	a.mu.Lock()
	for _, i := range pending {
		c := checks[i]
		k := accessKey{cluster: cluster, subject: subj, check: c}
		if e, ok := a.cache[k]; ok && now.Before(e.expires) {
			out[i] = e.allowed
			hits++
			continue
		}
		if w, ok := waits[k]; ok {
			w.idx = append(w.idx, i)
			continue
		}
		call, running := a.inflight[k]
		if !running {
			call = &accessCall{done: make(chan struct{})}
			a.inflight[k] = call
			owned[k] = call
			mine = append(mine, k)
			misses++
		}
		waits[k] = &wait{idx: []int{i}, call: call}
	}
	a.mu.Unlock()
	a.metrics.sarHits.Add(hits)
	a.metrics.sarMisses.Add(misses)

	if len(mine) > 0 {
		a.resolve(ctx, p, cluster, mine, owned)
	}
	for _, w := range waits {
		select {
		case <-w.call.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if w.call.err != nil {
			return nil, w.call.err
		}
		for _, i := range w.idx {
			out[i] = w.call.allowed
		}
	}
	return out, nil
}

// resolve asks the agent for keys (which this caller owns) in batches and
// completes their calls. It runs detached from the caller's cancellation so
// that other waiters on the same keys are not failed by it.
func (a *authorizer) resolve(ctx context.Context, p identity.Principal, cluster string, keys []accessKey, owned map[accessKey]*accessCall) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), accessTimeout)
	defer cancel()
	id := protocol.Identity{User: p.User, Groups: slices.Clone(p.Groups)}
	for start := 0; start < len(keys); start += maxChecksPerRequest {
		batch := keys[start:min(start+maxChecksPerRequest, len(keys))]
		checks := make([]protocol.AccessCheck, len(batch))
		for i, k := range batch {
			checks[i] = k.check
		}
		allowed, err := a.send(ctx, cluster, id, checks)
		if err == nil && len(allowed) != len(checks) {
			err = fmt.Errorf("hub: agent %s answered %d of %d access checks", cluster, len(allowed), len(checks))
		}
		now := a.now()
		expires := now.Add(a.ttl)
		a.mu.Lock()
		if err == nil {
			a.makeRoom(len(batch))
		}
		for i, k := range batch {
			call := owned[k]
			if err != nil {
				if e, ok := a.cache[k]; ok && a.staleUsable(cluster, err, e.expires, now) {
					call.allowed = e.allowed
				} else {
					call.err = err
				}
			} else {
				call.allowed = allowed[i]
				a.cache[k] = accessEntry{allowed: allowed[i], expires: expires}
			}
			delete(a.inflight, k)
			close(call.done)
		}
		a.mu.Unlock()
	}
}

// makeRoom evicts expired entries, and then arbitrary ones, so that n more
// entries fit under the bound. Called with a.mu held.
func (a *authorizer) makeRoom(n int) {
	if len(a.cache)+n <= a.max {
		return
	}
	now := a.now()
	for k, e := range a.cache {
		if !now.Before(e.expires) {
			delete(a.cache, k)
		}
	}
	for k := range a.cache {
		if len(a.cache)+n <= a.max {
			break
		}
		delete(a.cache, k)
	}
}

// allowedTuples answers verb for every tuple.
//
// It asks the cluster-scope question first: verb on (group, resource) in
// every namespace (namespace ""), once per kind. When that is allowed,
// every namespaced tuple of the kind is allowed: it is the check the API
// server makes for `kubectl get <kind> -A`, so nothing is over-granted.
// Per-namespace checks are asked only for kinds denied cluster-wide. This
// turns ~(kinds × namespaces) checks per user and cluster into ~kinds for
// users with cluster-wide read access.
func (a *authorizer) allowedTuples(ctx context.Context, p identity.Principal, cluster, verb string, tuples []accessTuple) (map[accessTuple]bool, error) {
	type gr struct{ group, resource string }
	wide := map[gr]int{} // index into checks
	var checks []protocol.AccessCheck
	for _, t := range tuples {
		k := gr{t.Group, t.Resource}
		if _, ok := wide[k]; !ok {
			wide[k] = len(checks)
			checks = append(checks, accessTuple{Group: t.Group, Resource: t.Resource}.check(verb))
		}
	}
	res, err := a.check(ctx, p, cluster, checks)
	if err != nil {
		return nil, err
	}
	out := make(map[accessTuple]bool, len(tuples))
	var rest []accessTuple
	for _, t := range tuples {
		switch {
		case res[wide[gr{t.Group, t.Resource}]]:
			out[t] = true
		case t.Namespace == "":
			out[t] = false // the cluster-scope check was this tuple's own
		default:
			rest = append(rest, t)
		}
	}
	if len(rest) == 0 {
		return out, nil
	}
	checks = make([]protocol.AccessCheck, len(rest))
	for i, t := range rest {
		checks[i] = t.check(verb)
	}
	if res, err = a.check(ctx, p, cluster, checks); err != nil {
		return nil, err
	}
	for i, t := range rest {
		out[t] = res[i]
	}
	return out, nil
}

// filter returns the resources whose (group, resource, namespace) p may
// list, keeping their order, with DependsOn entries p may not list removed
// (withVisibleDependencies). Unknown kinds are dropped. An inventory-only
// row needs its parent Kustomization to be listable and, when Eddy knows
// the plural of the row's kind, list on that kind in its namespace too.
func (a *authorizer) filter(ctx context.Context, p identity.Principal, cluster string, rs []model.Resource) ([]model.Resource, error) {
	if len(rs) == 0 {
		return rs, nil
	}
	seen := map[accessTuple]bool{}
	var tuples []accessTuple
	for _, r := range rs {
		ts, _ := requiredTuples(r)
		for _, t := range append(ts, dependencyTuples(r)...) {
			if !seen[t] {
				seen[t] = true
				tuples = append(tuples, t)
			}
		}
	}
	allowed, err := a.allowedTuples(ctx, p, cluster, "list", tuples)
	if err != nil {
		return nil, err
	}
	out := rs[:0:0]
	for _, r := range rs {
		if allTuplesAllowed(r, allowed) {
			out = append(out, withVisibleDependencies(r, allowed))
		}
	}
	return out, nil
}

func allTuplesAllowed(r model.Resource, allowed map[accessTuple]bool) bool {
	ts, ok := requiredTuples(r)
	if !ok {
		return false
	}
	for _, t := range ts {
		if !allowed[t] {
			return false
		}
	}
	return true
}

// size is the number of cached answers (tests).
func (a *authorizer) size() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.cache)
}
