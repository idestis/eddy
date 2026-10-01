package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/protocol"
)

// Stale views (ADR-0006, owner decision 2): when a cluster loses its last
// session, the hub keeps the last complete view in memory and serves reads
// from it marked stale, instead of dropping it. Requests that need the
// agent (YAML, events, logs, writes) fail with ErrDisconnected. The view is
// replaced in place when a session for the cluster syncs again.
//
// Stale views are bounded by staleMaxRows across the hub (agents.staleMax);
// the oldest is evicted first.
const staleMaxRows = 1_000_000

// staleAccessGrace is how long after expiry a cached SAR answer may still
// filter reads of a stale view: no agent can be asked while the cluster is
// disconnected, and failing closed would hide the view from everyone.
// Group changes still apply at once (the cache key includes the groups);
// answers never asked before fail closed.
const staleAccessGrace = 10 * time.Minute

// staleView is the last view of a disconnected cluster.
type staleView struct {
	*clusterView
	cluster  string
	info     protocol.Hello
	lastSeen time.Time
	since    time.Time
	rows     int
}

var _ clusterSession = (*staleView)(nil)

func (v *staleView) name() string          { return v.cluster }
func (v *staleView) hello() protocol.Hello { return v.info }
func (v *staleView) closed() bool          { return true }
func (v *staleView) lastSeenAt() time.Time { return v.lastSeen }

func (v *staleView) do(context.Context, protocol.Request) (json.RawMessage, error) {
	return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, v.cluster)
}

func (v *staleView) stream(context.Context, protocol.Request, func(protocol.LogChunk) error) error {
	return fmt.Errorf("%w: %s", fleet.ErrDisconnected, v.cluster)
}

// viewOfSession returns the view a session serves.
func viewOfSession(s clusterSession) *clusterView {
	switch s := s.(type) {
	case *agentSession:
		return s.clusterView
	case *remoteSession:
		return s.clusterView
	case *staleView:
		return s.clusterView
	}
	return nil
}

// keepStaleLocked keeps the view of lost, the primary cluster just lost,
// as a stale view, and evicts the oldest stale views over the budget.
// Called with a.mu held.
func (a *agents) keepStaleLocked(cluster string, lost clusterSession) {
	v := viewOfSession(lost)
	if v == nil || !v.isSynced() {
		return
	}
	a.dropStaleLocked(cluster)
	sv := &staleView{clusterView: v, cluster: cluster, info: lost.hello(), lastSeen: lost.lastSeenAt(), since: time.Now(), rows: v.size()}
	a.stale[cluster] = sv
	a.staleRows += sv.rows
	for a.staleRows > a.staleMax && len(a.stale) > 0 {
		var oldest *staleView
		for _, s := range a.stale {
			if oldest == nil || s.since.Before(oldest.since) {
				oldest = s
			}
		}
		a.dropStaleLocked(oldest.cluster)
		// Its readers vanish: clients refetch and see it disconnected.
		a.bus.publish(event{kind: evResync, cluster: oldest.cluster})
	}
	a.metrics.staleClusters.Store(int64(len(a.stale)))
}

// dropStaleLocked forgets the stale view of cluster. Called with a.mu held.
func (a *agents) dropStaleLocked(cluster string) {
	if sv := a.stale[cluster]; sv != nil {
		a.staleRows -= sv.rows
		delete(a.stale, cluster)
		a.metrics.staleClusters.Store(int64(len(a.stale)))
	}
}

// staleOf returns the stale view of cluster, or nil. A cluster with a
// primary session has none.
func (a *agents) staleOf(cluster string) *staleView {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.primary[cluster] != nil {
		return nil
	}
	return a.stale[cluster]
}

// reader returns what serves reads of cluster: the primary session, else
// the stale view, else nil.
func (a *agents) reader(cluster string) clusterSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if s := a.primary[cluster]; s != nil {
		return s
	}
	if sv := a.stale[cluster]; sv != nil {
		return sv
	}
	return nil
}

// isStale reports whether cluster is served from a stale view.
func (a *agents) isStale(cluster string) bool { return a.staleOf(cluster) != nil }
