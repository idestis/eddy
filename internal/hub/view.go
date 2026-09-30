package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
type clusterView struct {
	rvSeq *atomic.Uint64

	mu        sync.RWMutex
	resources map[string]model.Resource
	synced    bool
	rv        uint64
	counts    map[accessTuple]map[model.Status]int
	countsRV  uint64
	// findings are the cluster's findings (sanitizeFindings), replaced as
	// a whole.
	findings []model.Finding
}

func newClusterView(rvSeq *atomic.Uint64) *clusterView {
	return &clusterView{rvSeq: rvSeq, resources: map[string]model.Resource{}, rv: rvSeq.Add(1)}
}

// replace swaps in a snapshot. Only surfaced kinds are kept (sanitizeResource).
func (v *clusterView) replace(rs []model.Resource) {
	m := make(map[string]model.Resource, len(rs))
	for _, r := range rs {
		if r, ok := sanitizeResource(r); ok && len(m) < maxResources {
			m[r.ID] = r
		}
	}
	v.mu.Lock()
	v.resources = m
	v.synced = true
	v.rv = v.rvSeq.Add(1)
	v.mu.Unlock()
}

// apply patches the view and returns what actually changed. parents maps
// the id of every deleted inventory-only row to its owner, so SSE can
// filter the delete by the parent's visibility.
func (v *clusterView) apply(d protocol.Delta) (upserts []model.Resource, deletes []string, parents map[string]model.Ref) {
	upserts = make([]model.Resource, 0, len(d.Upserts))
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, r := range d.Upserts {
		r, ok := sanitizeResource(r)
		if !ok {
			continue
		}
		if _, exists := v.resources[r.ID]; !exists && len(v.resources) >= maxResources {
			continue
		}
		v.resources[r.ID] = r
		upserts = append(upserts, r)
	}
	for _, id := range d.Deletes {
		if old, ok := v.resources[id]; ok {
			delete(v.resources, id)
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
	return upserts, deletes, parents
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

func (v *clusterView) size() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return len(v.resources)
}

// tupleCounts returns resource counts by access tuple and status, cached
// until the view changes. The returned map must not be modified.
func (v *clusterView) tupleCounts() map[accessTuple]map[model.Status]int {
	v.mu.RLock()
	if v.counts != nil && v.countsRV == v.rv {
		c := v.counts
		v.mu.RUnlock()
		return c
	}
	v.mu.RUnlock()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.counts != nil && v.countsRV == v.rv {
		return v.counts
	}
	c := map[accessTuple]map[model.Status]int{}
	for _, r := range v.resources {
		if r.InventoryOnly {
			continue // not counted: its status is always unknown
		}
		t, ok := tupleOf(r.Ref)
		if !ok {
			continue
		}
		if c[t] == nil {
			c[t] = map[model.Status]int{}
		}
		c[t][r.Status]++
	}
	v.counts, v.countsRV = c, v.rv
	return c
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
