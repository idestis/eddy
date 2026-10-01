package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/store"
)

// gatedStore holds back a replica's store event subscription until gate is
// closed, and closes listed once the replica's first read of agent_sessions
// has returned. Together they force the start-up interleaving in which an
// agent connects to another replica after this one's first evaluation but
// before it listens for store events.
type gatedStore struct {
	store.Store
	gate   chan struct{}
	listed chan struct{}
	once   sync.Once
}

func newGatedStore(st store.Store) *gatedStore {
	return &gatedStore{Store: st, gate: make(chan struct{}), listed: make(chan struct{})}
}

func (g *gatedStore) Events() store.Events { return gatedEvents{Events: g.Store.Events(), g: g} }

func (g *gatedStore) AgentSessions() store.AgentSessions {
	return gatedSessions{AgentSessions: g.Store.AgentSessions(), g: g}
}

type gatedEvents struct {
	store.Events
	g *gatedStore
}

func (e gatedEvents) Subscribe(ctx context.Context) (<-chan store.Event, error) {
	select {
	case <-e.g.gate:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.Events.Subscribe(ctx)
}

type gatedSessions struct {
	store.AgentSessions
	g *gatedStore
}

func (s gatedSessions) List(ctx context.Context, cluster string, freshAfter time.Time) ([]store.AgentSession, error) {
	rows, err := s.AgentSessions.List(ctx, cluster, freshAfter)
	s.g.once.Do(func() { close(s.g.listed) })
	return rows, err
}

// TestMirrorAfterEventMissedAtStart: replica B evaluated its mirrors (no
// agent anywhere yet), then the agent connected to A, whose store event B
// missed because it was not listening yet. B must still mirror the cluster
// as soon as it listens, not peerEvaluateEvery later: every new
// subscription to store events starts with a resync.
func TestMirrorAfterEventMissedAtStart(t *testing.T) {
	open := sharedStores(t)
	a := newReplica(t, open(), "hub-0", "")
	gs := newGatedStore(open())
	b := newReplica(t, gs, "hub-1", "")
	select {
	case <-gs.listed:
	case <-time.After(5 * time.Second):
		t.Fatal("replica B never evaluated its mirrors")
	}
	a.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)})
	waitFor(t, func() bool {
		rows, err := a.store.AgentSessions().List(context.Background(), "dev", time.Now().Add(-agentFreshFor))
		return err == nil && len(rows) == 1
	})
	close(gs.gate)

	// Well under peerEvaluateEvery, so only the resync can make it.
	deadline := time.Now().Add(peerEvaluateEvery / 3)
	for {
		if r, ok := b.hub.agents.session("dev").(*remoteSession); ok && r.size() == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("replica B did not mirror dev after it started listening for store events")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestResendOfReplacedSubscription: resends run in the background, so one
// may finish after the peer unsubscribed or subscribed again. It must send
// nothing then: its view can be older than the deltas the newer
// subscription already got, and the subscriber would apply it after them.
func TestResendOfReplacedSubscription(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	met := newMetrics()
	ag := newAgents(newBus(), met)
	reg := NewRegistry()
	reg.replace(map[string]clusterEntry{"dev": {spec: ClusterSpec{Name: "dev"}}})
	n := newPeerNode(peerNodeConfig{pod: "hub-0", agents: ag, reg: reg, metrics: met, log: quietLog(), base: ctx})

	s := newSession("dev", protocol.Hello{Protocol: protocol.Version, Cluster: "dev"}, [32]byte{}, nil,
		sessionDeps{emit: ag.emit, metrics: met, rvSeq: &ag.rvSeq, log: quietLog()})
	s.instance, s.seq = "i-1", 1
	s.applySnapshot(protocol.Snapshot{Resources: []model.Resource{res("Kustomization", "team-a", "apps", model.StatusReady)}, Parts: 1})
	if err := ag.add(s); err != nil {
		t.Fatal(err)
	}

	l := n.newLink("hub-1", "127.0.0.1:1", "hub-1", nil)
	n.mu.Lock()
	n.links[l.pod] = l
	n.mu.Unlock()
	drain := func() int { // frames queued for the peer
		n := 0
		for {
			select {
			case <-l.out:
				n++
			default:
				return n
			}
		}
	}

	old := &peerSub{link: l, cluster: "dev", pending: true}
	cur := &peerSub{link: l, cluster: "dev", pending: true}
	l.mu.Lock()
	l.subs["dev"] = cur
	l.mu.Unlock()
	n.resend(cur)
	if got := drain(); got != 2 {
		t.Fatalf("current subscription got %d frames, want hello and snapshot", got)
	}
	s.applyDelta(protocol.Delta{Upserts: []model.Resource{res("Kustomization", "team-a", "web", model.StatusReady)}})
	if got := drain(); got != 1 {
		t.Fatalf("current subscription got %d frames for a change, want one delta", got)
	}

	n.resend(old)
	if got := drain(); got != 0 {
		t.Fatalf("a replaced subscription's resend sent %d frames", got)
	}
	l.mu.Lock()
	delete(l.subs, "dev")
	l.mu.Unlock()
	n.resend(cur)
	if got := drain(); got != 0 {
		t.Fatalf("an ended subscription's resend sent %d frames", got)
	}
}
