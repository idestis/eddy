package hub

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// clusterSession is one source of a cluster's view and the path for its
// requests: a local agent WebSocket (*agentSession) or a relay to the hub
// replica that holds one (*remoteSession). The fleet service, the
// authorizer and SSE only use this interface, so they work the same on
// every replica.
type clusterSession interface {
	name() string
	view() ([]model.Resource, string)
	lookup(id string) (model.Resource, bool)
	size() int
	tupleCounts() map[accessTuple]map[model.Status]int
	counted() *countSnapshot
	findingList() []model.Finding
	do(ctx context.Context, req protocol.Request) (json.RawMessage, error)
	stream(ctx context.Context, req protocol.Request, onChunk func(protocol.LogChunk) error) error
	hello() protocol.Hello
	closed() bool
	lastSeenAt() time.Time
}

var (
	_ clusterSession = (*agentSession)(nil)
	_ clusterSession = (*remoteSession)(nil)
)

// clusterView is a cluster's resources as one source reported them. Views
// are replaced by a snapshot and patched by deltas; every change bumps rv,
// a hub-wide sequence, so a resourceVersion never repeats across sessions.
//
// A snapshot larger than a frame arrives in parts (a Snapshot, then
// upsert-only Deltas). The parts are staged and swapped in together, so the
// view is synced, and its sources emit a resync, only once it is complete.
type clusterView struct {
	rvSeq *atomic.Uint64

	mu        sync.RWMutex
	resources map[string]model.Resource
	synced    bool
	rv        uint64
	// counts are the live counts per tuple and status, updated with every
	// change; countSnap is their copy at countsRV (counted).
	counts    map[accessTuple]map[model.Status]int
	countSnap *countSnapshot
	countsRV  uint64
	// attention holds the ids of rows that need attention (needsAttention),
	// kept in step with resources.
	attention map[string]struct{}
	// findings are the cluster's findings (sanitizeFindings), replaced as
	// a whole.
	findings []model.Finding
	// stage is the snapshot being received, nil when none is.
	stage *stagedSnapshot
	// idx is the T1 index (index.go), nil until an index request needs it.
	idx *viewIndex
}

// stagedSnapshot is a snapshot whose parts are still arriving.
type stagedSnapshot struct {
	resources map[string]model.Resource
	findings  *protocol.FindingSet
	// parts is Snapshot.Parts: 0 for an agent that does not mark its
	// parts (the snapshot ends after a quiet period), else the frame count.
	parts   int
	started time.Time
	quiet   *time.Timer
}

// Snapshot staging for agents that do not mark their snapshot parts: the
// parts follow the Snapshot back to back, so the snapshot is complete once
// no frame arrived for legacySnapshotQuiet (agents send deltas every 250 ms
// at most), or after legacySnapshotMax at the latest.
var (
	legacySnapshotQuiet = 200 * time.Millisecond
	legacySnapshotMax   = 10 * time.Second
)

func newClusterView(rvSeq *atomic.Uint64) *clusterView {
	return &clusterView{rvSeq: rvSeq, resources: map[string]model.Resource{}, attention: map[string]struct{}{}, rv: rvSeq.Add(1)}
}

// needsAttention reports whether r is a "needs attention" row (ADR-0006):
// failed, or a Flux object that is reconciling or suspended.
func needsAttention(r model.Resource) bool {
	if r.InventoryOnly {
		return false
	}
	switch r.Status {
	case model.StatusFailed:
		return true
	case model.StatusReconciling, model.StatusSuspended:
		k, ok := flux.KindByName(r.Kind)
		return ok && k.Flux
	}
	return false
}

func sanitizedMap(rs []model.Resource) map[string]model.Resource {
	m := make(map[string]model.Resource, len(rs))
	for _, r := range rs {
		if r, ok := sanitizeResource(r); ok && len(m) < maxResources {
			m[r.ID] = r
		}
	}
	return m
}

func attentionOf(m map[string]model.Resource) map[string]struct{} {
	out := map[string]struct{}{}
	for id, r := range m {
		if needsAttention(r) {
			out[id] = struct{}{}
		}
	}
	return out
}

// replace swaps in a complete snapshot. Only surfaced kinds are kept
// (sanitizeResource).
func (v *clusterView) replace(rs []model.Resource) {
	m := sanitizedMap(rs)
	att := attentionOf(m)
	v.mu.Lock()
	v.stopStageLocked()
	v.resources, v.attention = m, att
	v.dropIndexLocked()
	v.recountLocked()
	v.synced = true
	v.rv = v.rvSeq.Add(1)
	v.mu.Unlock()
}

// startSnapshot applies the first frame of a snapshot. It reports true
// when the snapshot is complete (one part); otherwise the view keeps
// serving its current rows until the last part arrives (applyDelta
// reports it) or, for an agent that does not mark parts, until onQuiet
// commits it after a quiet period.
func (v *clusterView) startSnapshot(snap protocol.Snapshot, onQuiet func()) bool {
	if snap.Parts == 1 {
		v.replace(snap.Resources)
		v.replaceFindings(snap.Findings)
		return true
	}
	st := &stagedSnapshot{resources: sanitizedMap(snap.Resources), findings: snap.Findings, parts: snap.Parts, started: time.Now()}
	v.mu.Lock()
	v.stopStageLocked()
	v.stage = st
	if st.parts == 0 {
		st.quiet = time.AfterFunc(legacySnapshotQuiet, func() {
			if v.commitStage(st) {
				onQuiet()
			}
		})
	}
	v.mu.Unlock()
	return false
}

func (v *clusterView) stopStageLocked() {
	if v.stage != nil && v.stage.quiet != nil {
		v.stage.quiet.Stop()
	}
	v.stage = nil
}

// commitStage swaps in st if it is still the staged snapshot.
func (v *clusterView) commitStage(st *stagedSnapshot) bool {
	v.mu.Lock()
	if v.stage != st {
		v.mu.Unlock()
		return false
	}
	v.commitLocked()
	v.mu.Unlock()
	return true
}

func (v *clusterView) commitLocked() {
	st := v.stage
	v.stopStageLocked()
	v.resources, v.attention = st.resources, attentionOf(st.resources)
	v.dropIndexLocked()
	v.recountLocked()
	v.synced = true
	v.rv = v.rvSeq.Add(1)
	fs := st.findings
	if fs == nil {
		fs = &protocol.FindingSet{}
	}
	v.findings = sanitizeFindings(fs.Items)
}

// stageDeltaLocked applies d to the staged snapshot and reports whether
// that completed it.
func (v *clusterView) stageDeltaLocked(d protocol.Delta) bool {
	st := v.stage
	for _, r := range d.Upserts {
		if r, ok := sanitizeResource(r); ok {
			if _, exists := st.resources[r.ID]; exists || len(st.resources) < maxResources {
				st.resources[r.ID] = r
			}
		}
	}
	for _, id := range d.Deletes {
		delete(st.resources, id)
	}
	if d.Findings != nil {
		st.findings = d.Findings
	}
	switch {
	case st.parts > 0 && d.Part >= st.parts:
		v.commitLocked()
		return true
	case st.parts == 0 && time.Since(st.started) >= legacySnapshotMax:
		v.commitLocked()
		return true
	case st.parts == 0:
		st.quiet.Reset(legacySnapshotQuiet)
	}
	return false
}

// applied is what a delta actually changed in a view.
type applied struct {
	upserts []model.Resource
	deletes []string
	// parents maps the id of every deleted inventory-only row to its
	// owner, so SSE can filter the delete by the parent's visibility.
	parents map[string]model.Ref
	// attnUpserts are the upserted rows that need attention (new or
	// still); attnDeletes the ids that left the attention set (healed or
	// deleted). They feed the SSE attention events (ADR-0006 P2).
	attnUpserts []model.Resource
	attnDeletes []string
	// committed reports that the delta completed a staged snapshot.
	committed bool
}

// event returns the evChange event of the change.
func (a applied) event(cluster string) event {
	return event{kind: evChange, cluster: cluster, upserts: a.upserts, deletes: a.deletes, parents: a.parents,
		attnUpserts: a.attnUpserts, attnDeletes: a.attnDeletes}
}

// changeEvents are the bus events of an applied (uncommitted) delta: the
// change itself, then an evClusters that names what it touched.
func changeEvents(cluster string, ch applied, findingsChanged bool) []event {
	var why clustersWhy
	if findingsChanged {
		why = whyFindings
	}
	if len(ch.upserts) == 0 && len(ch.deletes) == 0 {
		if why == 0 {
			return nil
		}
		return []event{{kind: evClusters, cluster: cluster, why: why}}
	}
	return []event{ch.event(cluster), {kind: evClusters, cluster: cluster, why: why | whyCounts}}
}

// apply patches the view and returns what actually changed. While a
// snapshot is staged, d goes to the stage instead and nothing changes yet;
// committed reports that d completed the snapshot.
func (v *clusterView) apply(d protocol.Delta) (out applied) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stage != nil {
		out.committed = v.stageDeltaLocked(d)
		return out
	}
	v.indexBeginLocked(len(d.Upserts) + len(d.Deletes))
	var (
		upserts = make([]model.Resource, 0, len(d.Upserts))
		deletes []string
		parents map[string]model.Ref
	)
	for _, r := range d.Upserts {
		r, ok := sanitizeResource(r)
		if !ok {
			continue
		}
		old, exists := v.resources[r.ID]
		if !exists && len(v.resources) >= maxResources {
			continue
		}
		if v.counts != nil {
			if exists {
				v.countRow(old, -1)
			}
			v.countRow(r, 1)
		}
		v.resources[r.ID] = r
		if exists {
			v.indexUpsertLocked(&old, &r)
		} else {
			v.indexUpsertLocked(nil, &r)
		}
		if needsAttention(r) {
			v.attention[r.ID] = struct{}{}
			out.attnUpserts = append(out.attnUpserts, r)
		} else if _, was := v.attention[r.ID]; was {
			delete(v.attention, r.ID)
			out.attnDeletes = append(out.attnDeletes, r.ID)
		}
		upserts = append(upserts, r)
	}
	for _, id := range d.Deletes {
		if old, ok := v.resources[id]; ok {
			if v.counts != nil {
				v.countRow(old, -1)
			}
			delete(v.resources, id)
			v.indexDeleteLocked(&old)
			if _, was := v.attention[id]; was {
				delete(v.attention, id)
				out.attnDeletes = append(out.attnDeletes, id)
			}
			deletes = append(deletes, id)
			if old.InventoryOnly && old.Owner != nil {
				if parents == nil {
					parents = map[string]model.Ref{}
				}
				parents[id] = *old.Owner
			}
		}
	}
	if len(upserts) > 0 || len(deletes) > 0 {
		v.rv = v.rvSeq.Add(1)
	}
	out.upserts, out.deletes, out.parents = upserts, deletes, parents
	return out
}

// setFindings replaces the findings when fs is set and reports whether they
// changed.
func (v *clusterView) setFindings(fs *protocol.FindingSet) bool {
	if fs == nil {
		return false
	}
	clean := sanitizeFindings(fs.Items)
	v.mu.Lock()
	defer v.mu.Unlock()
	if reflect.DeepEqual(clean, v.findings) {
		return false
	}
	v.findings = clean
	v.rvSeq.Add(1) // the fleet version (ETags of /clusters) covers findings
	return true
}

// replaceFindings sets the findings of a snapshot: an agent that sends none
// has none.
func (v *clusterView) replaceFindings(fs *protocol.FindingSet) {
	if fs == nil {
		fs = &protocol.FindingSet{}
	}
	v.setFindings(fs)
}

// findingList returns the findings; the slice must not be modified.
func (v *clusterView) findingList() []model.Finding {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.findings
}

// Bounds on findings from an agent.
const (
	maxFindings     = 1000
	maxFindingText  = 600
	maxFindingGroup = 5
)

// sanitizeFindings keeps only known finding kinds with valid fields and
// caps every text, so an agent cannot inject arbitrary data.
func sanitizeFindings(in []model.Finding) []model.Finding {
	out := []model.Finding{}
	for _, f := range in {
		if len(out) == maxFindings {
			break
		}
		if f.Kind != model.FindingJobBuildup || f.Jobs == nil || f.Namespace == "" || len(f.Namespace) > 63 ||
			(f.Severity != model.SeverityWarning && f.Severity != model.SeverityInfo) {
			continue
		}
		j := *f.Jobs
		j.Groups = nil
		for _, g := range f.Jobs.Groups {
			if len(j.Groups) == maxFindingGroup {
				break
			}
			g.By, g.Name, g.Label = truncate(g.By, 32), truncate(g.Name, 320), truncate(g.Label, 320)
			if g.Owner != nil {
				o := *g.Owner
				o.Namespace = f.Namespace
				o.Group, o.Kind, o.Name = truncate(o.Group, 253), truncate(o.Kind, 63), truncate(o.Name, 253)
				g.Owner = &o
			}
			j.Groups = append(j.Groups, g)
		}
		out = append(out, model.Finding{
			ID:             string(f.Kind) + "/" + f.Namespace,
			Kind:           f.Kind,
			Severity:       f.Severity,
			Namespace:      f.Namespace,
			Message:        truncate(f.Message, maxFindingText),
			Recommendation: truncate(f.Recommendation, maxFindingText),
			Jobs:           &j,
		})
	}
	slices.SortFunc(out, func(a, b model.Finding) int { return strings.Compare(a.ID, b.ID) })
	return out
}

func (v *clusterView) isSynced() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.synced
}

// view returns a copy of every resource and the view's resourceVersion.
func (v *clusterView) view() ([]model.Resource, string) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]model.Resource, 0, len(v.resources))
	for _, r := range v.resources {
		out = append(out, r)
	}
	return out, strconv.FormatUint(v.rv, 10)
}

func (v *clusterView) lookup(id string) (model.Resource, bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	r, ok := v.resources[id]
	return r, ok
}

// version returns the view's resourceVersion without copying rows.
func (v *clusterView) version() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return strconv.FormatUint(v.rv, 10)
}

// scanIDs calls fn with every row id under the read lock. fn must be quick
// and must not call back into the view; it returns false to stop.
func (v *clusterView) scanIDs(fn func(id string) bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	for id := range v.resources {
		if !fn(id) {
			return
		}
	}
}

// rowMeta is what search needs of a row to rank it and check its
// visibility, without copying the row.
type rowMeta struct {
	found  bool
	status model.Status
	// tuple indexes the tuples rowMetas returns: the row's own tuple, or
	// -1 when its kind has none. inventory is set (and tuple unused) for
	// inventory-only rows, which need requiredTuples.
	tuple     int32
	inventory *model.Resource
}

func (m rowMeta) order(cluster, current string) int {
	return tiebreakOrder(model.Resource{Status: m.status, InventoryOnly: m.inventory != nil}, cluster, current)
}

// visible applies the list rule given the answers for tuples.
func (m rowMeta) visible(tuples []accessTuple, allowed map[accessTuple]bool) bool {
	switch {
	case !m.found:
		return false
	case m.inventory != nil:
		return allTuplesAllowed(*m.inventory, allowed)
	case m.tuple < 0:
		return false
	}
	return allowed[tuples[m.tuple]]
}

// rowMetas appends the metadata of the rows of ids to out, in order, and
// returns every tuple they need answered.
func (v *clusterView) rowMetas(ids []string, out []rowMeta) ([]rowMeta, []accessTuple) {
	var tuples []accessTuple
	index := map[accessTuple]int32{}
	add := func(t accessTuple) int32 {
		i, ok := index[t]
		if !ok {
			i = int32(len(tuples))
			index[t] = i
			tuples = append(tuples, t)
		}
		return i
	}
	kinds := map[string]accessTuple{} // kind → tuple without namespace
	v.mu.RLock()
	defer v.mu.RUnlock()
	for _, id := range ids {
		m := rowMeta{tuple: -1}
		if r, ok := v.resources[id]; ok {
			m.found, m.status = true, r.Status
			if r.InventoryOnly {
				inv := r
				m.inventory = &inv
				ts, _ := requiredTuples(inv)
				for _, t := range ts {
					add(t)
				}
			} else {
				t, known := kinds[r.Kind]
				if !known {
					t, _ = tupleOf(model.Ref{Kind: r.Kind})
					kinds[r.Kind] = t
				}
				if t.Resource != "" {
					t.Namespace = r.Namespace
					m.tuple = add(t)
				}
			}
		}
		out = append(out, m)
	}
	return out, tuples
}

// attentionRows returns a copy of the rows that need attention.
func (v *clusterView) attentionRows() []model.Resource {
	v.mu.RLock()
	defer v.mu.RUnlock()
	out := make([]model.Resource, 0, len(v.attention))
	for id := range v.attention {
		out = append(out, v.resources[id])
	}
	return out
}

func (v *clusterView) size() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.resources)
}

// groupResource names a kind by its API group and plural.
type groupResource struct{ group, resource string }

// countSnapshot is a view's resource counts at one resourceVersion.
// Inventory-only rows are not counted: their status is always unknown.
type countSnapshot struct {
	// tuples are the counts per access tuple and status.
	tuples map[accessTuple]map[model.Status]int
	// kinds are the totals per kind, over every namespace.
	kinds map[groupResource]map[model.Status]int
	// byKind lists each kind's tuples.
	byKind map[groupResource][]accessTuple
}

// countRow adds delta to r's count in the live per-tuple counts. Called
// with v.mu held for writing.
func (v *clusterView) countRow(r model.Resource, delta int) {
	if r.InventoryOnly {
		return
	}
	t, ok := tupleOf(r.Ref)
	if !ok {
		return
	}
	byStatus := v.counts[t]
	if byStatus == nil {
		byStatus = map[model.Status]int{}
		v.counts[t] = byStatus
	}
	if byStatus[r.Status] += delta; byStatus[r.Status] <= 0 {
		delete(byStatus, r.Status)
		if len(byStatus) == 0 {
			delete(v.counts, t)
		}
	}
}

// recountLocked rebuilds the live counts from the rows (after a snapshot).
func (v *clusterView) recountLocked() {
	v.counts = map[accessTuple]map[model.Status]int{}
	for _, r := range v.resources {
		v.countRow(r, 1)
	}
}

// counted returns the counts, copied from the live counts once per
// resourceVersion (the live ones change in place with every delta). The
// returned snapshot must not be modified.
func (v *clusterView) counted() *countSnapshot {
	v.mu.RLock()
	if v.countSnap != nil && v.countsRV == v.rv {
		c := v.countSnap
		v.mu.RUnlock()
		return c
	}
	v.mu.RUnlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.countSnap != nil && v.countsRV == v.rv {
		return v.countSnap
	}
	if v.counts == nil {
		v.recountLocked()
	}
	c := &countSnapshot{
		tuples: make(map[accessTuple]map[model.Status]int, len(v.counts)),
		kinds:  map[groupResource]map[model.Status]int{},
		byKind: map[groupResource][]accessTuple{},
	}
	for t, byStatus := range v.counts {
		c.tuples[t] = maps.Clone(byStatus)
		gr := groupResource{t.Group, t.Resource}
		c.byKind[gr] = append(c.byKind[gr], t)
		k := c.kinds[gr]
		if k == nil {
			k = map[model.Status]int{}
			c.kinds[gr] = k
		}
		for st, n := range byStatus {
			k[st] += n
		}
	}
	v.countSnap, v.countsRV = c, v.rv
	return c
}

// tupleCounts returns resource counts by access tuple and status, cached
// until the view changes. The returned map must not be modified.
func (v *clusterView) tupleCounts() map[accessTuple]map[model.Status]int {
	return v.counted().tuples
}

// splitResources groups resources into chunks whose JSON stays under budget
// bytes, as agents chunk snapshots. A resource that alone exceeds the
// budget is dropped.
func splitResources(rs []model.Resource, budget int) [][]model.Resource {
	var (
		chunks [][]model.Resource
		cur    []model.Resource
		size   int
	)
	for _, r := range rs {
		b, err := json.Marshal(r)
		if err != nil || len(b)+1 > budget {
			continue
		}
		if size+len(b)+1 > budget && len(cur) > 0 {
			chunks = append(chunks, cur)
			cur, size = nil, 0
		}
		cur = append(cur, r)
		size += len(b) + 1
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}
