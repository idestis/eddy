package hub

import (
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/model"
)

// The T1 index of a view (ADR-0006 P2): one small entry per row, kept in
// the default list order (kind, namespace, name, group) and patched with
// every delta, so GET …/resources?view=index filters, counts facets and
// pages without copying or sorting the full summaries.
//
// An index is built on the first index request for a view and dropped
// again when nobody asked for it for indexIdle, so only clusters people
// look at pay its memory (about 230 B per row: the strings share the
// summary's memory). Entries are immutable; a change replaces the entry.

const (
	// indexIdle drops a view's index after this long without a request.
	indexIdle = 5 * time.Minute
	// indexBulk is the delta size above which the index is dropped and
	// rebuilt by the next request instead of being patched row by row.
	indexBulk = 512
	// indexMaxTuples bounds the tuple table, which only grows; a larger
	// table is compacted by a rebuild.
	indexMaxTuples = 1 << 16
)

// indexEntry is one row of the index.
type indexEntry struct {
	id, group, kind, ns, name string
	status                    model.Status
	rank                      int8 // statusRank
	blocked, inventoryOnly    bool
	message, revision         string
	replicas, completions     string
	project                   string
	owner                     *model.Ref
	// changed is LastChanged, or CreatedAt when unset.
	changed time.Time
	// t1 and t2 index the tuples p must be allowed to list for the row to
	// be visible (requiredTuples): t1 < 0 means never, t2 < 0 none.
	t1, t2 int32
}

// viewIndex is a view's index. It is guarded by the view's mutex: entries
// and tupleIdx change only under the write lock. tuples is append-only, so
// a reader may keep a copy of its slice header after unlocking.
type viewIndex struct {
	entries  []*indexEntry // sorted by compareDefault
	tuples   []accessTuple
	tupleIdx map[accessTuple]int32
	lastUsed atomic.Int64 // unix nanoseconds
}

// compareDefault is the default list order: kind, namespace, name, group
// (buildRows in the web UI; the group only breaks ties).
func compareDefault(a, b *indexEntry) int {
	if c := strings.Compare(a.kind, b.kind); c != 0 {
		return c
	}
	if c := strings.Compare(a.ns, b.ns); c != 0 {
		return c
	}
	if c := strings.Compare(a.name, b.name); c != 0 {
		return c
	}
	return strings.Compare(a.group, b.group)
}

func (ix *viewIndex) intern(t accessTuple) int32 {
	i, ok := ix.tupleIdx[t]
	if !ok {
		i = int32(len(ix.tuples))
		ix.tuples = append(ix.tuples, t)
		ix.tupleIdx[t] = i
	}
	return i
}

// entryOf derives r's index entry and interns its tuples.
func (ix *viewIndex) entryOf(r *model.Resource) *indexEntry {
	e := &indexEntry{
		id: r.ID, group: r.Group, kind: r.Kind, ns: r.Namespace, name: r.Name,
		status: r.Status, rank: int8(statusRank(*r)), blocked: r.Blocked, inventoryOnly: r.InventoryOnly,
		message: r.Message, revision: r.Revision, replicas: r.Replicas, completions: r.Completions,
		project: r.Project, owner: r.Owner, changed: r.LastChanged, t1: -1, t2: -1,
	}
	if e.changed.IsZero() {
		e.changed = r.CreatedAt
	}
	if ts, ok := requiredTuples(*r); ok {
		e.t1 = ix.intern(ts[0])
		if len(ts) > 1 {
			e.t2 = ix.intern(ts[1])
		}
	}
	return e
}

func newViewIndex(n int) *viewIndex {
	return &viewIndex{entries: make([]*indexEntry, 0, n), tupleIdx: map[accessTuple]int32{}}
}

// buildIndex derives the index of rows. Called with v.mu held (read or
// write).
func buildIndex(rows map[string]model.Resource) *viewIndex {
	ix := newViewIndex(len(rows))
	for id := range rows {
		r := rows[id]
		ix.entries = append(ix.entries, ix.entryOf(&r))
	}
	slices.SortFunc(ix.entries, compareDefault)
	return ix
}

// index returns the view's index, building it when there is none. The
// build runs under the read lock and is installed only if the view did not
// change meanwhile.
func (v *clusterView) index() *viewIndex {
	now := time.Now().UnixNano()
	for range 3 {
		v.mu.RLock()
		ix, rv := v.idx, v.rv
		if ix == nil {
			ix = buildIndex(v.resources)
		}
		v.mu.RUnlock()
		v.mu.Lock()
		switch {
		case v.idx != nil:
			ix = v.idx
		case v.rv == rv:
			v.idx = ix
		default:
			ix = nil // changed while building: try again
		}
		v.mu.Unlock()
		if ix != nil {
			ix.lastUsed.Store(now)
			return ix
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.idx == nil {
		v.idx = buildIndex(v.resources)
	}
	v.idx.lastUsed.Store(now)
	return v.idx
}

// dropIndexLocked forgets the index (a new snapshot, a bulk delta, or
// nobody used it lately). Called with v.mu held for writing.
func (v *clusterView) dropIndexLocked() { v.idx = nil }

// indexBeginLocked is called before a delta of n changes is applied; it
// drops the index when patching it would cost more than a rebuild or it
// was not used lately. Called with v.mu held for writing.
func (v *clusterView) indexBeginLocked(n int) {
	if v.idx == nil {
		return
	}
	if n > indexBulk || len(v.idx.tuples) > indexMaxTuples ||
		time.Since(time.Unix(0, v.idx.lastUsed.Load())) > indexIdle {
		v.dropIndexLocked()
	}
}

// indexUpsertLocked replaces old (when it existed) with r. Called with
// v.mu held for writing.
func (v *clusterView) indexUpsertLocked(old *model.Resource, r *model.Resource) {
	ix := v.idx
	if ix == nil {
		return
	}
	e := ix.entryOf(r)
	if old != nil {
		if i, ok := slices.BinarySearchFunc(ix.entries, e, compareDefault); ok {
			ix.entries[i] = e // same key (the id is the key)
			return
		}
	}
	i, _ := slices.BinarySearchFunc(ix.entries, e, compareDefault)
	ix.entries = slices.Insert(ix.entries, i, e)
}

// indexDeleteLocked removes old. Called with v.mu held for writing.
func (v *clusterView) indexDeleteLocked(old *model.Resource) {
	ix := v.idx
	if ix == nil {
		return
	}
	key := &indexEntry{kind: old.Kind, ns: old.Namespace, name: old.Name, group: old.Group}
	if i, ok := slices.BinarySearchFunc(ix.entries, key, compareDefault); ok {
		ix.entries = slices.Delete(ix.entries, i, i+1)
	}
}

// indexTuples returns ix's tuple table as of now. Called without the lock:
// it takes the read lock for the slice header only.
func (v *clusterView) indexTuples(ix *viewIndex) []accessTuple {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return ix.tuples
}

// scanIndex calls fn with every entry of ix in the default order under the
// read lock, sets *rv to the view's resourceVersion, and reports false when ix is no longer the view's index (it
// was dropped or rebuilt meanwhile). fn must be quick.
func (v *clusterView) scanIndex(ix *viewIndex, fn func(e *indexEntry), rv *uint64) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.idx != ix {
		return false
	}
	*rv = v.rv
	for _, e := range ix.entries {
		fn(e)
	}
	return true
}
