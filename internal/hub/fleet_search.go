package hub

import (
	"cmp"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
)

// Search limits (ADR-0006).
const (
	searchDefaultLimit = 30
	searchMaxLimit     = 100
	searchMaxQuery     = 128 // runes
	searchMaxTerms     = 8
	// searchClusterDeadline bounds the scan of one cluster; clusters that
	// miss it are listed in searchResult.Partial.
	searchClusterDeadline = 150 * time.Millisecond
	// searchQueueDeadline bounds how long a search waits, from its start,
	// for scan slots (scanSlots); clusters it could not start scanning by
	// then are listed in searchResult.Partial too.
	searchQueueDeadline = 300 * time.Millisecond
	searchPerUser       = 2
	searchMaxKinds      = 32

	attentionDefaultLimit = 200
	attentionMaxLimit     = 1000
)

// searchOptions are the parameters of GET /api/v1/search.
type searchOptions struct {
	Query string
	// Cluster limits the search to one cluster (Fleet false), or names
	// the current cluster for the tiebreak (Fleet true).
	Cluster string
	Fleet   bool
	Limit   int
	// Kinds, when set, keeps only rows of these kinds (canonical names,
	// see canonicalKinds).
	Kinds []string
}

type searchItem struct {
	Cluster  string         `json:"cluster"`
	Resource model.Resource `json:"resource"`
	// Match is fuzzy.ts's Match: the score and the matched ranges of the
	// primary field (the name) and of the secondary fields [namespace,
	// kind, kind abbreviation, cluster].
	Match fuzzyMatch `json:"match"`
	Stale bool       `json:"stale,omitempty"`
}

type searchResult struct {
	Items []searchItem `json:"items"`
	// Partial lists clusters that were skipped: their scan missed the
	// deadline or their access checks failed.
	Partial []string `json:"partial"`
}

// searchHit is a matched, visible row before the final ranking. It is
// kept small: full rows are read only for the final page.
type searchHit struct {
	cluster string
	id      string
	score   float64
	order   int // the tiebreak key: lower first
}

// statusRank orders statuses for the palette tiebreak (web/src/lib/format.ts
// STATUS_RANK); inventory-only rows come last.
func statusRank(r model.Resource) int {
	if r.InventoryOnly {
		return 9
	}
	switch r.Status {
	case model.StatusFailed:
		return 0
	case model.StatusReconciling:
		return 1
	case model.StatusSuspended:
		return 2
	case model.StatusUnknown:
		return 3
	case model.StatusReady:
		return 4
	case model.StatusCompleted:
		return 5
	}
	return 6
}

// tiebreakOrder is the palette's tiebreak as one number: failing first,
// then the current cluster first.
func tiebreakOrder(r model.Resource, cluster, current string) int {
	o := statusRank(r) * 2
	if cluster != current {
		o++
	}
	return o
}

func compareHits(a, b searchHit) int {
	if c := compareRanked(a.score, b.score, a.order-b.order); c != 0 {
		return c
	}
	if c := strings.Compare(a.cluster, b.cluster); c != 0 {
		return c
	}
	return strings.Compare(a.id, b.id)
}

// splitID splits "<group>/<Kind>/<namespace>/<name>" without allocating.
func splitID(id string) (kind, ns, name string, ok bool) {
	_, rest, ok := strings.Cut(id, "/")
	if !ok {
		return "", "", "", false
	}
	kind, rest, ok = strings.Cut(rest, "/")
	if !ok {
		return "", "", "", false
	}
	ns, name, ok = strings.Cut(rest, "/")
	return kind, ns, name, ok
}

// Search ranks the rows p may list by query, like the command palette
// (fuzzy.go): in one cluster, or across the fleet. Rows of stale views are
// included and marked.
func (f *fleetService) Search(ctx context.Context, p identity.Principal, o searchOptions) (searchResult, error) {
	if err := f.validPrincipal(p); err != nil {
		return searchResult{}, err
	}
	if utf8.RuneCountInString(o.Query) > searchMaxQuery {
		return searchResult{}, badRequest("q is longer than %d characters", searchMaxQuery)
	}
	terms := queryTerms(o.Query)
	if len(terms) > searchMaxTerms {
		return searchResult{}, badRequest("q has more than %d terms", searchMaxTerms)
	}
	kinds, err := canonicalKinds(o.Kinds)
	if err != nil {
		return searchResult{}, err
	}
	limit := o.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	limit = min(limit, searchMaxLimit)
	res := searchResult{Items: []searchItem{}, Partial: []string{}}
	if len(terms) == 0 {
		return res, nil
	}
	var clusters []string
	if o.Fleet {
		for _, spec := range f.reg.List() {
			clusters = append(clusters, spec.Name)
		}
	} else {
		if _, _, err := f.session(o.Cluster); err != nil {
			return searchResult{}, err
		}
		clusters = []string{o.Cluster}
	}

	// A single-term query skips the subsequence tier while better tiers
	// fill the page: a subsequence-only row is then tier 1, below every
	// other hit. Several terms average their scores, so they always try it.
	allowSub := len(terms) > 1
	q := searchQuery{terms: terms, kinds: kinds, current: o.Cluster, limit: limit, allowSub: allowSub,
		queueDeadline: time.Now().Add(searchQueueDeadline)}
	hits, partial := f.searchClusters(ctx, p, clusters, q)
	if !allowSub && countAboveSubsequence(hits) < limit {
		q.allowSub = true
		hits, partial = f.searchClusters(ctx, p, clusters, q)
	}
	slices.SortFunc(hits, compareHits)
	hits = hits[:min(len(hits), limit)]
	for _, h := range hits {
		s := f.agents.reader(h.cluster)
		v := viewOfSession(s)
		if v == nil {
			continue
		}
		r, ok := v.lookup(h.id)
		if !ok {
			continue // deleted meanwhile
		}
		kind, ns, name, _ := splitID(h.id)
		m, _ := matchItem(o.Query, name, []string{ns, kind, abbrOf(kind), h.cluster})
		_, stale := s.(*staleView)
		res.Items = append(res.Items, searchItem{Cluster: h.cluster, Resource: r, Match: m, Stale: stale})
	}
	res.Partial = partial
	f.agents.metrics.searches.Add(1)
	if len(partial) > 0 {
		f.agents.metrics.searchPartial.Add(1)
	}
	return res, nil
}

// searchQuery is one scan's parameters.
type searchQuery struct {
	terms []string
	// kinds keeps only rows of these kinds when set.
	kinds    []string
	current  string
	limit    int
	allowSub bool
	// queueDeadline is when the search stops waiting for scan slots.
	queueDeadline time.Time
}

// scanSlots bounds the view scans that run at once across every search of
// this replica (ADR-0006 P2). Without it each search scanned up to
// GOMAXPROCS views, so a few concurrent fleet searches oversubscribed the
// CPU and slowed each other and every other request. A search's workers
// queue for slots in arrival order (a channel's senders are served FIFO)
// and keep a slot for their share of its clusters, so searches are served
// about first come, first served rather than all slowed down together;
// the per-user cap (searchPerUser, in the handler) keeps one user from
// filling the queue.
type scanSlots struct{ ch chan struct{} }

func newScanSlots(n int) *scanSlots { return &scanSlots{ch: make(chan struct{}, max(1, n))} }

// searchScanSlots is half the replica's CPUs, leaving the rest to lists,
// counts and SSE.
func searchScanSlots() int { return max(1, runtime.GOMAXPROCS(0)/2) }

// acquire takes a slot, or reports false when ctx ends or deadline passes
// first.
func (s *scanSlots) acquire(ctx context.Context, deadline time.Time) bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(time.Until(deadline))
	defer t.Stop()
	select {
	case s.ch <- struct{}{}:
		return true
	case <-ctx.Done():
	case <-t.C:
	}
	return false
}

func (s *scanSlots) release() { <-s.ch }

// searchClusters scans clusters in parallel and returns each cluster's
// best limit visible hits, and the clusters that were skipped.
//
// The scan runs on at most one worker per scan slot. A worker takes a slot
// (waiting until q.queueDeadline at most) and keeps it while it claims and
// scans clusters one after another, so a search costs a handful of
// goroutine handoffs rather than one per cluster. Clusters no worker got
// to are partial.
func (f *fleetService) searchClusters(ctx context.Context, p identity.Principal, clusters []string, q searchQuery) ([]searchHit, []string) {
	type target struct {
		name string
		v    *clusterView
	}
	var targets []target
	for _, c := range clusters {
		if v := viewOfSession(f.agents.reader(c)); v != nil {
			targets = append(targets, target{c, v})
		}
	}
	var (
		mu      sync.Mutex
		hits    []searchHit
		partial = []string{}
		wg      sync.WaitGroup
		next    atomic.Int64
	)
	f.lazyInit()
	for range min(len(targets), cap(f.scans.ch)) {
		wg.Go(func() {
			if !f.scans.acquire(ctx, q.queueDeadline) {
				return
			}
			defer f.scans.release()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(targets) {
					return
				}
				t := targets[i]
				var (
					hs  []searchHit
					err = ctx.Err()
				)
				if err == nil {
					hs, err = f.searchCluster(ctx, p, t.name, t.v, q)
				}
				mu.Lock()
				if err != nil {
					f.log.Debug("search skipped a cluster", "cluster", t.name, "err", err)
					partial = append(partial, t.name)
				} else {
					hits = append(hits, hs...)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if claimed := int(next.Load()); claimed < len(targets) {
		f.agents.metrics.searchQueueTimeouts.Add(uint64(len(targets) - claimed))
		for _, t := range targets[claimed:] {
			partial = append(partial, t.name)
		}
	}
	slices.Sort(partial)
	return hits, partial
}

func countAboveSubsequence(hits []searchHit) int {
	n := 0
	for _, h := range hits {
		if matchTier(h.score) > matchTier(tierSubsequence) {
			n++
		}
	}
	return n
}

var errSearchDeadline = fmt.Errorf("%w: search deadline", errUnavailable)

// secMatch is one secondary field's match of one term.
type secMatch struct {
	m  fieldMatch
	ok bool
}

// secondaryCache memoises the secondary-field matches of a query: the
// kind, its abbreviation and the cluster are the same for many rows, and a
// cluster has few namespaces.
type secondaryCache struct {
	terms   []string
	cluster []secMatch
	kinds   map[string][2][]secMatch // kind → [kind, abbr] per term
	nss     map[string][]secMatch
}

func newSecondaryCache(terms []string, cluster string) *secondaryCache {
	c := &secondaryCache{terms: terms, kinds: map[string][2][]secMatch{}, nss: map[string][]secMatch{}}
	c.cluster = c.field(lowerASCII(cluster))
	return c
}

func (c *secondaryCache) field(text string) []secMatch {
	out := make([]secMatch, len(c.terms))
	for i, t := range c.terms {
		out[i].m, out[i].ok = matchField(t, text, false, false)
	}
	return out
}

func (c *secondaryCache) kind(kind string) [2][]secMatch {
	k, ok := c.kinds[kind]
	if !ok {
		k = [2][]secMatch{c.field(lowerASCII(kind)), c.field(lowerASCII(abbrOf(kind)))}
		c.kinds[kind] = k
	}
	return k
}

func (c *secondaryCache) namespace(ns string) []secMatch {
	m, ok := c.nss[ns]
	if !ok {
		m = c.field(ns)
		c.nss[ns] = m
	}
	return m
}

// score is matchTerms over the name and the cached secondary matches
// [namespace, kind, abbreviation, cluster]: the same choice of the best
// field per term and the same average.
func (c *secondaryCache) score(name string, ns []secMatch, kind [2][]secMatch, allowSub bool) (float64, bool) {
	total := 0.0
	for ti, term := range c.terms {
		best, ok := matchField(term, name, true, allowSub)
		for i, sm := range [4]secMatch{ns[ti], kind[0][ti], kind[1][ti], c.cluster[ti]} {
			if sm.ok && (!ok || sm.m.score-float64(i) > best.score) {
				best, ok = sm.m, true
			}
		}
		if !ok {
			return 0, false
		}
		total += best.score
	}
	return total / float64(len(c.terms)), true
}

// searchScratch holds one cluster scan's buffers; scans reuse them so a
// broad query does not churn the heap (and the GC) of a large hub.
type searchScratch struct {
	ids    []string
	scores []float64
	metas  []rowMeta
	hits   []searchHit
	// tierIDs and tierScores are ids and scores grouped by tier, best
	// first.
	tierIDs    []string
	tierScores []float64
}

var searchScratchPool = sync.Pool{New: func() any { return &searchScratch{} }}

// searchCluster matches every row id of one view without copying rows,
// keeps the visible matches and returns the best limit of them.
func (f *fleetService) searchCluster(ctx context.Context, p identity.Principal, cluster string, v *clusterView, q searchQuery) ([]searchHit, error) {
	deadline := time.Now().Add(searchClusterDeadline)
	sc := newSecondaryCache(q.terms, cluster)
	allowSub := q.allowSub
	buf := searchScratchPool.Get().(*searchScratch)
	defer func() {
		clear(buf.ids)
		clear(buf.hits)
		clear(buf.metas)
		clear(buf.tierIDs)
		buf.ids, buf.scores, buf.metas, buf.hits = buf.ids[:0], buf.scores[:0], buf.metas[:0], buf.hits[:0]
		searchScratchPool.Put(buf)
	}()
	var (
		n    int
		late bool
	)
	v.scanIDs(func(id string) bool {
		if n++; n&2047 == 0 && time.Now().After(deadline) {
			late = true
			return false
		}
		kind, ns, name, ok := splitID(id)
		if !ok || (len(q.kinds) > 0 && !slices.Contains(q.kinds, kind)) {
			return true
		}
		name = lowerASCII(name)
		nsm, km := sc.namespace(ns), sc.kind(kind)
		score, ok := sc.score(name, nsm, km, allowSub)
		if !ok {
			return true
		}
		if !allowSub {
			// The exact score: a primary subsequence may beat a weak
			// secondary match.
			score, _ = sc.score(name, nsm, km, true)
		}
		buf.ids = append(buf.ids, id)
		buf.scores = append(buf.scores, score)
		return true
	})
	if late {
		return nil, errSearchDeadline
	}
	if len(buf.ids) == 0 {
		return nil, nil
	}
	// Hits rank by tier first (compareRanked), so once the best tiers hold
	// limit visible hits no row of a lower tier can make the page: resolve
	// and check visibility tier by tier, best first, and stop there.
	var counts [searchTiers]int
	for _, sc := range buf.scores {
		counts[tierBucket(sc)]++
	}
	starts := counts
	for t, n := searchTiers-1, 0; t >= 0; t-- {
		starts[t], n = n, n+counts[t]
	}
	buf.tierIDs = slices.Grow(buf.tierIDs[:0], len(buf.ids))[:len(buf.ids)]
	buf.tierScores = slices.Grow(buf.tierScores[:0], len(buf.ids))[:len(buf.ids)]
	pos := starts
	for i, sc := range buf.scores {
		t := tierBucket(sc)
		buf.tierIDs[pos[t]], buf.tierScores[pos[t]] = buf.ids[i], sc
		pos[t]++
	}
	for t := searchTiers - 1; t >= 0 && len(buf.hits) < q.limit; t-- {
		if counts[t] == 0 {
			continue
		}
		ids := buf.tierIDs[starts[t] : starts[t]+counts[t]]
		var tuples []accessTuple
		buf.metas, tuples = v.rowMetas(ids, buf.metas[:0])
		allowed, err := f.authz.allowedTuples(ctx, p, cluster, "list", tuples)
		if err != nil {
			return nil, err
		}
		for i, m := range buf.metas {
			if m.visible(tuples, allowed) {
				buf.hits = append(buf.hits, searchHit{cluster: cluster, id: ids[i], score: buf.tierScores[starts[t]+i], order: m.order(cluster, q.current)})
			}
		}
	}
	return slices.Clone(topHits(buf.hits, q.limit)), nil
}

// topHits returns the best k of hs, sorted (compareHits). It reorders hs.
// Broad queries match thousands of rows per cluster, so it selects with a
// k-sized heap (worst on top) instead of sorting them all.
func topHits(hs []searchHit, k int) []searchHit {
	if len(hs) <= k {
		slices.SortFunc(hs, compareHits)
		return hs
	}
	h := hs[:k]
	for i := k/2 - 1; i >= 0; i-- {
		siftWorst(h, i)
	}
	for _, x := range hs[k:] {
		if compareHits(x, h[0]) < 0 {
			h[0] = x
			siftWorst(h, 0)
		}
	}
	slices.SortFunc(h, compareHits)
	return h
}

// siftWorst restores the heap property of h below i: every parent ranks
// after (is worse than) its children.
func siftWorst(h []searchHit, i int) {
	for {
		worst, l, r := i, 2*i+1, 2*i+2
		if l < len(h) && compareHits(h[l], h[worst]) > 0 {
			worst = l
		}
		if r < len(h) && compareHits(h[r], h[worst]) > 0 {
			worst = r
		}
		if worst == i {
			return
		}
		h[i], h[worst] = h[worst], h[i]
		i = worst
	}
}

// searchTiers bounds the tiers searchCluster groups hits by; scores are at
// most 1000 (fuzzy.go), anything above shares the top bucket.
const searchTiers = 12

func tierBucket(score float64) int { return min(max(matchTier(score), 0), searchTiers-1) }

type attentionItem struct {
	Cluster  string         `json:"cluster"`
	Resource model.Resource `json:"resource"`
	Stale    bool           `json:"stale,omitempty"`
}

type attentionFinding struct {
	Cluster string        `json:"cluster"`
	Finding model.Finding `json:"finding"`
	Stale   bool          `json:"stale,omitempty"`
}

type attentionResult struct {
	Items []attentionItem `json:"items"`
	// Total counts the visible rows that need attention, before Limit.
	Total int `json:"total"`
	// Findings are the warning findings p may see (never capped: at most
	// one per namespace).
	Findings []attentionFinding `json:"findings"`
	// Partial lists clusters whose access checks failed.
	Partial []string `json:"partial"`
}

// compareAttention orders attention rows: failed, reconciling, suspended,
// then the most recently changed first.
func compareAttention(a, b model.Resource) int {
	if c := cmp.Compare(statusRank(a), statusRank(b)); c != 0 {
		return c
	}
	if c := b.LastChanged.Compare(a.LastChanged); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// Attention returns the rows that need attention (needsAttention) and the
// warning findings p may see, in cluster or, when cluster is empty, across
// the fleet: failed first, then reconciling, then suspended, most recently
// changed first.
func (f *fleetService) Attention(ctx context.Context, p identity.Principal, cluster string, limit int) (attentionResult, error) {
	if err := f.validPrincipal(p); err != nil {
		return attentionResult{}, err
	}
	if limit <= 0 {
		limit = attentionDefaultLimit
	}
	limit = min(limit, attentionMaxLimit)
	var clusters []string
	if cluster != "" {
		if _, _, err := f.session(cluster); err != nil {
			return attentionResult{}, err
		}
		clusters = []string{cluster}
	} else {
		for _, spec := range f.reg.List() {
			clusters = append(clusters, spec.Name)
		}
	}
	res := attentionResult{Items: []attentionItem{}, Findings: []attentionFinding{}, Partial: []string{}}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, c := range clusters {
		s := f.agents.reader(c)
		if s == nil {
			continue
		}
		v := viewOfSession(s)
		if v == nil {
			continue
		}
		wg.Go(func() {
			stale := f.agents.isStale(c)
			rows, err := f.authz.filter(ctx, p, c, v.attentionRows())
			var fs []model.Finding
			if err == nil {
				fs, err = f.visibleFindings(ctx, p, s)
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				res.Partial = append(res.Partial, c)
				return
			}
			// Only the best limit rows of a cluster can make the page.
			res.Total += len(rows)
			slices.SortFunc(rows, compareAttention)
			for _, r := range rows[:min(len(rows), limit)] {
				res.Items = append(res.Items, attentionItem{Cluster: c, Resource: r, Stale: stale})
			}
			for _, fd := range fs {
				if fd.Severity == model.SeverityWarning {
					res.Findings = append(res.Findings, attentionFinding{Cluster: c, Finding: fd, Stale: stale})
				}
			}
		})
	}
	wg.Wait()
	slices.SortFunc(res.Items, func(a, b attentionItem) int {
		if c := compareAttention(a.Resource, b.Resource); c != 0 {
			return c
		}
		return cmp.Compare(a.Cluster, b.Cluster)
	})
	slices.SortFunc(res.Findings, func(a, b attentionFinding) int {
		return cmp.Or(cmp.Compare(a.Cluster, b.Cluster), cmp.Compare(a.Finding.ID, b.Finding.ID))
	})
	slices.Sort(res.Partial)
	res.Items = res.Items[:min(len(res.Items), limit)]
	return res, nil
}
