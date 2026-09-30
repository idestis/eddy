// Package inproc holds process-local implementations of the store parts that
// hub replicas share: rate-limit counters, the agent session registry and
// the event fan-out. The memory backend uses all three. They are only
// correct for a single replica, because nothing leaves the process.
//
// Broker is also the local fan-out stage of backends whose events arrive
// from elsewhere (the postgres LISTEN connection).
package inproc

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// ---- rate limits ----

// RateLimits implements store.RateLimits with a map. The zero value is not
// usable; use NewRateLimits.
type RateLimits struct {
	mu   sync.Mutex
	keys map[string]rateWindow
}

type rateWindow struct {
	count   int
	resetMs int64 // window end, unix ms
}

var _ store.RateLimits = (*RateLimits)(nil)

// NewRateLimits returns an empty set of counters.
func NewRateLimits() *RateLimits { return &RateLimits{keys: map[string]rateWindow{}} }

func (r *RateLimits) Hit(_ context.Context, key string, window time.Duration, limit int, now time.Time) (int, bool, error) {
	if err := storeutil.CheckRateLimit(key, window); err != nil {
		return 0, false, err
	}
	nowMs := storeutil.Ms(now)
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.keys[key]
	if ok && w.resetMs > nowMs {
		w.count++
	} else {
		w = rateWindow{count: 1, resetMs: nowMs + window.Milliseconds()}
	}
	r.keys[key] = w
	return w.count, limit <= 0 || w.count <= limit, nil
}

func (r *RateLimits) Get(_ context.Context, key string, now time.Time) (int, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.keys[key]
	if !ok || w.resetMs <= storeutil.Ms(now) {
		return 0, time.Time{}, nil
	}
	return w.count, storeutil.FromMs(w.resetMs), nil
}

func (r *RateLimits) Reset(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, key)
	return nil
}

// Prune deletes windows that ended at or before now.
func (r *RateLimits) Prune(now time.Time) int64 {
	nowMs := storeutil.Ms(now)
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for k, w := range r.keys {
		if w.resetMs <= nowMs {
			delete(r.keys, k)
			n++
		}
	}
	return n
}

// ---- agent sessions ----

// AgentSessions implements store.AgentSessions with a map. The zero value is
// not usable; use NewAgentSessions.
type AgentSessions struct {
	mu   sync.Mutex
	rows map[agentKey]store.AgentSession
}

type agentKey struct{ cluster, instance string }

var _ store.AgentSessions = (*AgentSessions)(nil)

// NewAgentSessions returns an empty registry.
func NewAgentSessions() *AgentSessions {
	return &AgentSessions{rows: map[agentKey]store.AgentSession{}}
}

func (a *AgentSessions) Upsert(_ context.Context, s store.AgentSession) error {
	s, err := storeutil.PrepareAgentSession(s)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	k := agentKey{s.Cluster, s.AgentInstance}
	if old, ok := a.rows[k]; ok && !(s.Seq > old.Seq || (s.Seq == old.Seq && s.HubPod == old.HubPod)) {
		return store.ErrConflict
	}
	a.rows[k] = s
	return nil
}

func (a *AgentSessions) Heartbeat(_ context.Context, cluster, hubPod, agentInstance string, at time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := agentKey{cluster, agentInstance}
	s, ok := a.rows[k]
	if !ok || s.HubPod != hubPod {
		return store.ErrNotFound
	}
	s.HeartbeatAt = storeutil.Norm(at)
	a.rows[k] = s
	return nil
}

func (a *AgentSessions) Delete(_ context.Context, cluster, hubPod, agentInstance string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	k := agentKey{cluster, agentInstance}
	if s, ok := a.rows[k]; ok && s.HubPod == hubPod {
		delete(a.rows, k)
	}
	return nil
}

func (a *AgentSessions) List(_ context.Context, cluster string, freshAfter time.Time) ([]store.AgentSession, error) {
	fresh := storeutil.Ms(freshAfter)
	a.mu.Lock()
	var out []store.AgentSession
	for _, s := range a.rows {
		if (cluster == "" || s.Cluster == cluster) && storeutil.Ms(s.HeartbeatAt) > fresh {
			out = append(out, s)
		}
	}
	a.mu.Unlock()
	slices.SortFunc(out, CompareAgentSessions)
	return out, nil
}

// CompareAgentSessions is the order of AgentSessions.List: oldest
// connection first, then HubPod, AgentInstance and Cluster.
func CompareAgentSessions(x, y store.AgentSession) int {
	return cmp.Or(
		x.ConnectedAt.Compare(y.ConnectedAt),
		cmp.Compare(x.HubPod, y.HubPod),
		cmp.Compare(x.AgentInstance, y.AgentInstance),
		cmp.Compare(x.Cluster, y.Cluster),
	)
}

func (a *AgentSessions) DeleteByHub(_ context.Context, hubPod string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, s := range a.rows {
		if s.HubPod == hubPod {
			delete(a.rows, k)
		}
	}
	return nil
}

// Prune deletes sessions whose heartbeat is at or before cut.
func (a *AgentSessions) Prune(cut time.Time) int64 {
	cutMs := storeutil.Ms(cut)
	a.mu.Lock()
	defer a.mu.Unlock()
	var n int64
	for k, s := range a.rows {
		if storeutil.Ms(s.HeartbeatAt) <= cutMs {
			delete(a.rows, k)
			n++
		}
	}
	return n
}

// ---- events ----

// SubscriberBuffer is how many undelivered events a subscriber may have
// queued before further events to it are dropped.
const SubscriberBuffer = 256

// ErrClosed is returned by Broker.Subscribe after Close.
var ErrClosed = errors.New("store: events closed")

// Broker fans events out to local subscribers. The zero value is not
// usable; use NewBroker.
type Broker struct {
	mu     sync.Mutex
	subs   map[chan store.Event]struct{}
	closed bool
	done   chan struct{}
}

// NewBroker returns a broker with no subscribers.
func NewBroker() *Broker {
	return &Broker{subs: map[chan store.Event]struct{}{}, done: make(chan struct{})}
}

// Subscribe registers a subscriber until ctx is done or the broker closes.
func (b *Broker) Subscribe(ctx context.Context) (<-chan store.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := make(chan store.Event, SubscriberBuffer)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrClosed
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
		case <-b.done:
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}()
	return ch, nil
}

// Broadcast delivers e to every subscriber without blocking. A subscriber
// whose buffer is full misses e.
func (b *Broker) Broadcast(e store.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Close closes every subscriber channel. Later Subscribe calls fail.
func (b *Broker) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	close(b.done)
	for ch := range b.subs {
		delete(b.subs, ch)
		close(ch)
	}
}

// Events implements store.Events within one process.
type Events struct{ *Broker }

var _ store.Events = Events{}

// NewEvents returns an in-process event bus.
func NewEvents() Events { return Events{NewBroker()} }

func (e Events) Publish(_ context.Context, ev store.Event) error {
	if _, err := storeutil.EncodeEvent(ev); err != nil {
		return err
	}
	e.Broadcast(ev)
	return nil
}
