package hub

import (
	"context"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/identity"
)

// The visible tuple set of a user on one view's index: one bool per entry
// of the index's tuple table, answered by allowedTuples (the SAR cache and
// rules reviews) and kept for visibleTTL, so paging and refetching a list
// does not ask the authorizer again for every request.
//
// visibleTTL adds to the SAR cache's own accessTTL: a revoked permission
// stops showing rows within accessTTL + visibleTTL = 60 s, inside the 90 s
// that list ETags already allow (etag.go).
const (
	visibleTTL = 15 * time.Second
	visibleMax = 8192
)

type visibleKey struct {
	subject string
	cluster string
}

type visibleSet struct {
	ix      *viewIndex
	allowed []bool // for ix.tuples[:len(allowed)]
	expires time.Time
}

// visibleCache maps (subject, cluster) to the user's visible tuple set.
type visibleCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[visibleKey]*visibleSet
}

func newVisibleCache() *visibleCache {
	return &visibleCache{now: time.Now, m: map[visibleKey]*visibleSet{}}
}

// visibleTuples returns, for each of tuples (ix's table as of now), whether
// p may list it on cluster. A cached set for the same index is reused
// while it is fresh; tuples added to the table since are answered and
// appended.
func (f *fleetService) visibleTuples(ctx context.Context, p identity.Principal, cluster string, ix *viewIndex, tuples []accessTuple) ([]bool, error) {
	c := f.visCache()
	key := visibleKey{subject: subjectKey(p), cluster: cluster}
	now := c.now()
	var have []bool
	c.mu.Lock()
	if s := c.m[key]; s != nil && s.ix == ix && now.Before(s.expires) && len(s.allowed) <= len(tuples) {
		have = s.allowed
	}
	c.mu.Unlock()
	if len(have) == len(tuples) && have != nil {
		return have, nil
	}
	missing := tuples[len(have):]
	answers, err := f.authz.allowedTuples(ctx, p, cluster, "list", missing)
	if err != nil {
		return nil, err
	}
	allowed := make([]bool, len(have), len(tuples))
	copy(allowed, have)
	for _, t := range missing {
		allowed = append(allowed, answers[t])
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	expires := now.Add(visibleTTL)
	if s := c.m[key]; s != nil && s.ix == ix && have != nil {
		expires = s.expires // appending does not extend the set's life
	}
	if _, ok := c.m[key]; !ok && len(c.m) >= visibleMax {
		c.evictLocked(now)
	}
	c.m[key] = &visibleSet{ix: ix, allowed: allowed, expires: expires}
	return allowed, nil
}

// evictLocked drops expired sets, then arbitrary ones, to make room.
func (c *visibleCache) evictLocked(now time.Time) {
	for k, s := range c.m {
		if !now.Before(s.expires) {
			delete(c.m, k)
		}
	}
	for k := range c.m {
		if len(c.m) < visibleMax {
			return
		}
		delete(c.m, k)
	}
}

func (f *fleetService) lazyInit() {
	f.lazy.Do(func() {
		f.vis = newVisibleCache()
		f.scans = newScanSlots(searchScanSlots())
	})
}

func (f *fleetService) visCache() *visibleCache {
	f.lazyInit()
	return f.vis
}
