package hub

import (
	"context"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/store"
)

// recentThreadWindow is how long a thread change published by this replica
// suppresses the store event that echoes it back.
const recentThreadWindow = 5 * time.Second

// runEvents applies cross-replica store events (LISTEN/NOTIFY on
// eddy_events with PostgreSQL) on this replica until ctx is done:
//
//   - thread: re-read the thread and notify this replica's SSE clients;
//   - revoke: drop cached sessions of the subject;
//   - agent:  re-check agent session ownership and mirrors;
//   - resync: all of the above for everything, after events may have
//     been missed.
func (h *Hub) runEvents(ctx context.Context) {
	for ctx.Err() == nil {
		ch, err := h.store.Events().Subscribe(ctx)
		if err != nil {
			h.log.Warn("subscribing to store events failed; retrying", "err", err)
		} else {
			for e := range ch {
				h.onEvent(ctx, e)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (h *Hub) onEvent(ctx context.Context, e store.Event) {
	switch e.Kind {
	case store.EventThread:
		h.threadEvent(ctx, e.ID)
	case store.EventRevoke:
		h.auth.Invalidate(e.ID)
	case store.EventAgent:
		go h.registry.checkTakeover(ctx, e.Cluster)
		if h.peers != nil {
			h.peers.kick()
		}
	case store.EventResync:
		h.auth.Invalidate("")
		go h.registry.checkTakeover(ctx, "")
		if h.peers != nil {
			h.peers.kick()
		}
	}
}

// threadChanged fans a thread change made on this replica out: to local
// SSE clients at once, and to the other replicas through the store.
func (h *Hub) threadChanged(t store.Thread) {
	h.bus.publish(event{kind: evThread, thread: t})
	h.recentThreads.add(t.ID, time.Now())
	go func() {
		ctx, cancel := context.WithTimeout(h.base, storeTimeout)
		defer cancel()
		if err := h.store.Events().Publish(ctx, store.Event{Kind: store.EventThread, ID: t.ID}); err != nil {
			h.log.Warn("publishing a thread change failed; other replicas' clients see it on their next refresh", "err", err)
		}
	}()
}

// threadEvent handles a thread change from any replica. A deleted thread
// cannot be re-read, so other replicas' clients see its deletion on their
// next refresh.
func (h *Hub) threadEvent(ctx context.Context, id string) {
	if id == "" || h.recentThreads.seen(id, time.Now()) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	t, err := h.store.Threads().Get(ctx, id)
	if err != nil {
		return
	}
	h.bus.publish(event{kind: evThread, thread: t})
}

// recentSet remembers ids for recentThreadWindow.
type recentSet struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (r *recentSet) add(id string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]time.Time{}
	}
	for k, t := range r.m {
		if now.Sub(t) > recentThreadWindow {
			delete(r.m, k)
		}
	}
	r.m[id] = now
}

func (r *recentSet) seen(id string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.m[id]
	return ok && now.Sub(t) <= recentThreadWindow
}
