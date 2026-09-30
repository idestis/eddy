package hub

import (
	"sync"
	"sync/atomic"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
)

// eventKind enumerates what the bus carries to SSE clients.
type eventKind int

const (
	evChange     eventKind = iota // resource upserts and deletes on one cluster
	evResync                      // a cluster's view was replaced (snapshot or reconnect)
	evClusters                    // connection state or counts may have changed
	evThread                      // a thread changed
	evConnection                  // a cluster's onboarding state changed (ADR-0005)
	evFindings                    // a cluster's findings changed (relayed to peers; SSE clients get them via clusters)
)

// event is published unfiltered; each subscriber filters it for its own
// user before sending anything.
type event struct {
	kind    eventKind
	cluster string
	upserts []model.Resource
	deletes []string
	// parents maps deleted inventory-only row ids to their owners.
	parents map[string]model.Ref
	thread  store.Thread
	// findings is the new finding set of an evFindings event.
	findings []model.Finding
}

// subBuffer is the per-subscriber queue. A subscriber that falls this far
// behind is marked overflowed and resynced rather than blocking publishers.
const subBuffer = 512

type subscriber struct {
	ch       chan event
	overflow atomic.Bool
}

// bus fans events out to SSE subscribers without ever blocking publishers.
type bus struct {
	mu   sync.RWMutex
	subs map[*subscriber]struct{}
}

func newBus() *bus { return &bus{subs: map[*subscriber]struct{}{}} }

func (b *bus) subscribe() *subscriber {
	s := &subscriber{ch: make(chan event, subBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *bus) unsubscribe(s *subscriber) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

func (b *bus) publish(e event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- e:
		default:
			s.overflow.Store(true)
		}
	}
}
