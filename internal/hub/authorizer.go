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
// cached for 45 s per user).
const (
	accessTTL       = 45 * time.Second
	accessMaxCached = 100_000
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

// authorizer answers "may this user do verb on resource" by asking the
// cluster's agent for SubjectAccessReviews. Answers are cached per
// (cluster, user and groups, check) for accessTTL, the cache is bounded,
// and concurrent identical questions share one agent round trip. Errors
// are never cached: callers fail closed.
type authorizer struct {
	send    accessSender
	metrics *metrics
	now     func() time.Time
	ttl     time.Duration
	max     int

	mu       sync.Mutex
	cache    map[accessKey]accessEntry
	inflight map[accessKey]*accessCall
}

func newAuthorizer(send accessSender, m *metrics) *authorizer {
	return &authorizer{
		send:     send,
		metrics:  m,
		now:      time.Now,
		ttl:      accessTTL,
		max:      accessMaxCached,
		cache:    map[accessKey]accessEntry{},
		inflight: map[accessKey]*accessCall{},
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
	for i, c := range checks {
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
		expires := a.now().Add(a.ttl)
		a.mu.Lock()
		if err == nil {
			a.makeRoom(len(batch))
		}
		for i, k := range batch {
			call := owned[k]
			if err != nil {
				call.err = err
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
func (a *authorizer) allowedTuples(ctx context.Context, p identity.Principal, cluster, verb string, tuples []accessTuple) (map[accessTuple]bool, error) {
	checks := make([]protocol.AccessCheck, len(tuples))
	for i, t := range tuples {
		checks[i] = t.check(verb)
	}
	res, err := a.check(ctx, p, cluster, checks)
	if err != nil {
		return nil, err
	}
	out := make(map[accessTuple]bool, len(tuples))
	for i, t := range tuples {
		out[t] = res[i]
	}
	return out, nil
}

// filter returns the resources whose (group, resource, namespace) p may
// list, keeping their order. Unknown kinds are dropped. An inventory-only
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
		for _, t := range ts {
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
			out = append(out, r)
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
