package hub

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
)

// GET /api/v1/clusters/{c}/resources?view=index (ADR-0006 P2): one page of
// T1 index rows, with the total and facets, filtered by the caller's list
// access and the query, sorted and paged on the hub.

const (
	indexDefaultLimit = 200
	indexMaxLimit     = 1000
	// indexMessageRunes caps an index row's message.
	indexMessageRunes = 160
	// indexMaxRevision caps an index row's revision.
	indexMaxRevision = 64
	// indexTopNamespaces is the size of the namespace facet.
	indexTopNamespaces = 50
	indexMaxQuery      = 256
	indexMaxCursor     = 2048
)

// indexSort names a sort order of the index.
type indexSort string

const (
	sortKind   indexSort = "kind"   // kind, namespace, name (the default)
	sortName   indexSort = "name"   // name, namespace, kind
	sortStatus indexSort = "status" // failed, reconciling, suspended, unknown, ready, completed; then kind, namespace, name
	sortAge    indexSort = "age"    // youngest change first; then kind, namespace, name
)

// indexQuery is a parsed index request.
type indexQuery struct {
	kinds    []string // canonical
	statuses []model.Status
	// attention is status=attention: rows that need attention (needsAttention), alone or with other statuses.
	attention bool
	namespace string
	q         string // trimmed, lower-cased
	sort      indexSort
	desc      bool
	offset    int
	cursor    *indexEntry // the key after which the page starts
	limit     int
}

// indexRow is the wire shape of an index entry.
type indexRow struct {
	ID            string       `json:"id"`
	Group         string       `json:"group"`
	Kind          string       `json:"kind"`
	Namespace     string       `json:"namespace"`
	Name          string       `json:"name"`
	Status        model.Status `json:"status"`
	Blocked       bool         `json:"blocked,omitempty"`
	Message       string       `json:"message,omitempty"`
	Revision      string       `json:"revision,omitempty"`
	Replicas      string       `json:"replicas,omitempty"`
	Completions   string       `json:"completions,omitempty"`
	Owner         *model.Ref   `json:"owner,omitempty"`
	Project       string       `json:"project,omitempty"`
	InventoryOnly bool         `json:"inventoryOnly,omitempty"`
	LastChanged   time.Time    `json:"lastChanged,omitzero"`
}

type namespaceCount struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// indexFacets count the visible rows that match every filter except the
// facet's own: kinds ignore kind=, statuses ignore status=, namespaces
// ignore namespace=.
type indexFacets struct {
	Kinds    map[string]int       `json:"kinds"`
	Statuses map[model.Status]int `json:"statuses"`
	// Namespaces are the indexTopNamespaces largest, largest first.
	Namespaces []namespaceCount `json:"namespaces"`
}

type indexPage struct {
	Items []indexRow `json:"items"`
	// Total counts the visible rows that match the filters.
	Total int `json:"total"`
	// Offset echoes the page's offset; it is absent for cursor pages.
	Offset *int `json:"offset,omitempty"`
	// Next is the cursor of the following page, absent on the last page.
	Next            string      `json:"next,omitempty"`
	Facets          indexFacets `json:"facets"`
	ResourceVersion string      `json:"resourceVersion"`
	Stale           bool        `json:"stale,omitempty"`
}

// parseIndexQuery reads the query of an index request.
func parseIndexQuery(q url.Values) (indexQuery, error) {
	out := indexQuery{sort: sortKind, limit: indexDefaultLimit}
	var kinds []string
	for _, v := range q["kind"] {
		kinds = append(kinds, strings.Split(v, ",")...)
	}
	if len(kinds) > searchMaxKinds {
		return out, badRequest("kind names at most %d kinds", searchMaxKinds)
	}
	var err error
	if out.kinds, err = canonicalKinds(kinds); err != nil {
		return out, err
	}
	for _, v := range q["status"] {
		for st := range strings.SplitSeq(v, ",") {
			if st = strings.TrimSpace(st); st == "" {
				continue
			}
			if st == "attention" {
				out.attention = true
				continue
			}
			if !containsStatus(model.Status(st)) {
				return out, badRequest("unknown status %q", truncate(st, 32))
			}
			if !slices.Contains(out.statuses, model.Status(st)) {
				out.statuses = append(out.statuses, model.Status(st))
			}
		}
	}
	if out.namespace = q.Get("namespace"); len(out.namespace) > 63 {
		return out, badRequest("namespace is too long")
	}
	if v := q.Get("q"); len(v) > indexMaxQuery {
		return out, badRequest("q is too long")
	} else {
		out.q = strings.ToLower(strings.TrimSpace(v))
	}
	switch s := indexSort(q.Get("sort")); s {
	case "":
	case sortKind, sortName, sortStatus, sortAge:
		out.sort = s
	default:
		return out, badRequest("sort must be kind, name, status or age")
	}
	switch q.Get("order") {
	case "", "asc":
	case "desc":
		out.desc = true
	default:
		return out, badRequest("order must be asc or desc")
	}
	if out.limit, err = parseLimit(q.Get("limit"), indexDefaultLimit, indexMaxLimit); err != nil {
		return out, err
	}
	cursor, offset := q.Get("cursor"), q.Get("offset")
	switch {
	case cursor != "" && offset != "":
		return out, badRequest("cursor and offset are exclusive")
	case cursor != "":
		if out.cursor, err = decodeIndexCursor(cursor, out.sort, out.desc); err != nil {
			return out, err
		}
	case offset != "":
		n, err := strconv.Atoi(offset)
		if err != nil || n < 0 || n > maxResources {
			return out, badRequest("offset must be between 0 and %d", maxResources)
		}
		out.offset = n
	}
	return out, nil
}

// indexCursor is the key of the last row of a page, plus the order it was
// taken in.
type indexCursor struct {
	Sort    indexSort `json:"s"`
	Desc    bool      `json:"d,omitempty"`
	Kind    string    `json:"k"`
	NS      string    `json:"n,omitempty"`
	Name    string    `json:"m"`
	Group   string    `json:"g,omitempty"`
	Rank    int8      `json:"r,omitempty"`
	Changed int64     `json:"t,omitempty"`
}

func encodeIndexCursor(e *indexEntry, s indexSort, desc bool) string {
	c := indexCursor{Sort: s, Desc: desc, Kind: e.kind, NS: e.ns, Name: e.name, Group: e.group}
	switch s {
	case sortStatus:
		c.Rank = e.rank
	case sortAge:
		c.Changed = e.changed.UnixNano()
	}
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeIndexCursor(s string, sort indexSort, desc bool) (*indexEntry, error) {
	bad := badRequest("invalid cursor")
	if len(s) > indexMaxCursor {
		return nil, bad
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, bad
	}
	var c indexCursor
	if json.Unmarshal(b, &c) != nil || c.Kind == "" || c.Name == "" {
		return nil, bad
	}
	if c.Sort != sort || c.Desc != desc {
		return nil, badRequest("the cursor belongs to another sort or order")
	}
	e := &indexEntry{kind: c.Kind, ns: c.NS, name: c.Name, group: c.Group, rank: c.Rank}
	if c.Changed != 0 {
		e.changed = time.Unix(0, c.Changed)
	}
	return e, nil
}

// compare orders entries for q (sort, then order). Every order ends with
// the default key, which is unique per row, so it is total.
func (q *indexQuery) compare(a, b *indexEntry) int {
	var c int
	switch q.sort {
	case sortName:
		if c = strings.Compare(a.name, b.name); c == 0 {
			if c = strings.Compare(a.ns, b.ns); c == 0 {
				if c = strings.Compare(a.kind, b.kind); c == 0 {
					c = strings.Compare(a.group, b.group)
				}
			}
		}
	case sortStatus:
		if c = cmp.Compare(a.rank, b.rank); c == 0 {
			c = compareDefault(a, b)
		}
	case sortAge:
		if c = b.changed.Compare(a.changed); c == 0 {
			c = compareDefault(a, b)
		}
	default:
		c = compareDefault(a, b)
	}
	if q.desc {
		return -c
	}
	return c
}

// containsFold reports whether s contains sub, ignoring ASCII case; sub
// is lower-case.
func containsFold(s, sub string) bool {
	n := len(sub)
	if n == 0 {
		return true
	}
	c0 := sub[0]
	for i := 0; i+n <= len(s); i++ {
		if lowerByte(s[i]) != c0 {
			continue
		}
		j := 1
		for j < n && lowerByte(s[i+j]) == sub[j] {
			j++
		}
		if j == n {
			return true
		}
	}
	return false
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// matchesText applies q= to an entry: kind, namespace, name, message and
// revision.
func (e *indexEntry) matchesText(q string) bool {
	return containsFold(e.name, q) || containsFold(e.ns, q) || containsFold(e.kind, q) ||
		containsFold(e.message, q) || containsFold(e.revision, q)
}

// row returns the wire shape of e.
func (e *indexEntry) row() indexRow {
	return indexRow{
		ID: e.id, Group: e.group, Kind: e.kind, Namespace: e.ns, Name: e.name, Status: e.status, Blocked: e.blocked,
		Message: truncateRunes(e.message, indexMessageRunes), Revision: shortRevision(e.revision),
		Replicas: e.replicas, Completions: e.completions, Owner: e.owner, Project: e.project,
		InventoryOnly: e.inventoryOnly, LastChanged: e.changed,
	}
}

// truncateRunes cuts s to at most n runes.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

// shortRevision shortens the digest of a Flux revision to 12 hex digits
// ("main@sha1:0123456789ab", "sha256:0123456789ab") and caps the rest.
func shortRevision(rev string) string {
	for _, algo := range []string{"sha1:", "sha256:", "sha512:", "blake3:"} {
		if i := strings.Index(rev, algo); i >= 0 {
			end := i + len(algo) + 12
			if end < len(rev) {
				rev = rev[:end]
			}
			break
		}
	}
	return truncate(rev, indexMaxRevision)
}

// visible reports whether e's tuples are all allowed.
func (e *indexEntry) visible(allowed []bool) bool {
	if e.t1 < 0 || int(e.t1) >= len(allowed) || !allowed[e.t1] {
		return false
	}
	return e.t2 < 0 || (int(e.t2) < len(allowed) && allowed[e.t2])
}

// indexScratch holds the matched entries of one request.
type indexScratch struct{ matched []*indexEntry }

var indexScratchPool = sync.Pool{New: func() any { return &indexScratch{} }}

// indexList answers an index request for p on cluster.
func (f *fleetService) indexList(ctx context.Context, p identity.Principal, cluster string, q indexQuery) (indexPage, error) {
	if err := f.validPrincipal(p); err != nil {
		return indexPage{}, err
	}
	_, s, err := f.session(cluster)
	if err != nil {
		return indexPage{}, err
	}
	v := viewOfSession(s)
	if v == nil {
		return indexPage{}, fmt.Errorf("%w: %s has no view", errUnavailable, cluster)
	}
	_, stale := s.(*staleView)
	buf := indexScratchPool.Get().(*indexScratch)
	defer func() {
		clear(buf.matched)
		buf.matched = buf.matched[:0]
		indexScratchPool.Put(buf)
	}()
	for range 3 {
		ix := v.index()
		allowed, err := f.visibleTuples(ctx, p, cluster, ix, v.indexTuples(ix))
		if err != nil {
			return indexPage{}, err
		}
		page, ok := f.scanPage(v, ix, allowed, q, buf)
		if ok {
			page.Stale = stale
			return page, nil
		}
		buf.matched = buf.matched[:0]
	}
	return indexPage{}, fmt.Errorf("%w: the index of %s keeps changing", errUnavailable, cluster)
}

// scanPage filters ix for q in one pass under the view's read lock,
// counting facets, then sorts and pages the matches. It reports false when
// ix was replaced meanwhile.
func (f *fleetService) scanPage(v *clusterView, ix *viewIndex, allowed []bool, q indexQuery, buf *indexScratch) (indexPage, bool) {
	kinds := map[string]int{}
	statuses := map[model.Status]int{}
	nss := map[string]int{}
	var rv uint64
	ok := v.scanIndex(ix, func(e *indexEntry) {
		if !e.visible(allowed) {
			return
		}
		mk := len(q.kinds) == 0 || slices.Contains(q.kinds, e.kind)
		mn := q.namespace == "" || e.ns == q.namespace
		ms := q.statusMatch(e)
		if !(mk && mn) && !(mk && ms) && !(mn && ms) {
			return // in no facet and not a match
		}
		if q.q != "" && !e.matchesText(q.q) {
			return
		}
		if mn && ms {
			kinds[e.kind]++
		}
		if mk && mn {
			statuses[e.status]++
		}
		if mk && ms && e.ns != "" {
			nss[e.ns]++
		}
		if mk && mn && ms {
			buf.matched = append(buf.matched, e)
		}
	}, &rv)
	if !ok {
		return indexPage{}, false
	}
	matched := buf.matched
	if q.sort != sortKind || q.desc {
		slices.SortFunc(matched, q.compare)
	}
	page := indexPage{
		Total:           len(matched),
		ResourceVersion: strconv.FormatUint(rv, 10),
		Facets:          indexFacets{Kinds: kinds, Statuses: statuses, Namespaces: topNamespaces(nss)},
	}
	start := min(q.offset, len(matched))
	if q.cursor != nil {
		start, _ = slices.BinarySearchFunc(matched, q.cursor, q.compare)
		// The cursor's own row (if still there) was on the previous page.
		if start < len(matched) && q.compare(matched[start], q.cursor) == 0 {
			start++
		}
	} else {
		off := q.offset
		page.Offset = &off
	}
	end := min(start+q.limit, len(matched))
	page.Items = make([]indexRow, 0, end-start)
	for _, e := range matched[start:end] {
		page.Items = append(page.Items, e.row())
	}
	if end < len(matched) && end > start {
		page.Next = encodeIndexCursor(matched[end-1], q.sort, q.desc)
	}
	return page, true
}

func topNamespaces(m map[string]int) []namespaceCount {
	out := make([]namespaceCount, 0, len(m))
	for _, name := range slices.Collect(maps.Keys(m)) {
		out = append(out, namespaceCount{Name: name, N: m[name]})
	}
	slices.SortFunc(out, func(a, b namespaceCount) int {
		if c := cmp.Compare(b.N, a.N); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out[:min(len(out), indexTopNamespaces)]
}

// statusMatch reports whether e passes the status filter: no filter, one of
// the listed statuses, or (status=attention) a row that needs attention.
func (q indexQuery) statusMatch(e *indexEntry) bool {
	if len(q.statuses) == 0 && !q.attention {
		return true
	}
	if slices.Contains(q.statuses, e.status) {
		return true
	}
	return q.attention && needsAttention(model.Resource{Ref: model.Ref{Kind: e.kind}, Status: e.status, InventoryOnly: e.inventoryOnly})
}
