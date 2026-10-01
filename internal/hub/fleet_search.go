package hub

import (
	"cmp"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
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
	searchPerUser         = 2

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
	hits, partial := f.searchClusters(ctx, p, clusters, terms, o.Cluster, limit, allowSub)
	if !allowSub && countAboveSubsequence(hits) < limit {
		hits, partial = f.searchClusters(ctx, p, clusters, terms, o.Cluster, limit, true)
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

// searchClusters scans clusters in parallel and returns each cluster's
// best limit visible hits, and the clusters that were skipped.
func (f *fleetService) searchClusters(ctx context.Context, p identity.Principal, clusters, terms []string, current string, limit int, allowSub bool) ([]searchHit, []string) {
	var (
		mu      sync.Mutex
		hits    []searchHit
		partial = []string{}
		wg      sync.WaitGroup
		sem     = make(chan struct{}, runtime.GOMAXPROCS(0))
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
			sem <- struct{}{}
			defer func() { <-sem }()
			hs, err := f.searchCluster(ctx, p, c, v, terms, current, limit, allowSub)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				f.log.Debug("search skipped a cluster", "cluster", c, "err", err)
				partial = append(partial, c)
				return
			}
			hits = append(hits, hs...)
		})
	}
	wg.Wait()
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
}

var searchScratchPool = sync.Pool{New: func() any { return &searchScratch{} }}

// searchCluster matches every row id of one view without copying rows,
// keeps the visible matches and returns the best limit of them.
func (f *fleetService) searchCluster(ctx context.Context, p identity.Principal, cluster string, v *clusterView, terms []string, current string, limit int, allowSub bool) ([]searchHit, error) {
	deadline := time.Now().Add(searchClusterDeadline)
	sc := newSecondaryCache(terms, cluster)
	buf := searchScratchPool.Get().(*searchScratch)
	defer func() {
		clear(buf.ids)
		clear(buf.hits)
		clear(buf.metas)
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
		if !ok {
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
	var tuples []accessTuple
	buf.metas, tuples = v.rowMetas(buf.ids, buf.metas)
	allowed, err := f.authz.allowedTuples(ctx, p, cluster, "list", tuples)
	if err != nil {
		return nil, err
	}
	for i, m := range buf.metas {
		if m.visible(tuples, allowed) {
			buf.hits = append(buf.hits, searchHit{cluster: cluster, id: buf.ids[i], score: buf.scores[i], order: m.order(cluster, current)})
		}
	}
	slices.SortFunc(buf.hits, compareHits)
	return slices.Clone(buf.hits[:min(len(buf.hits), limit)]), nil
}

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
