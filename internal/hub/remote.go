package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/protocol"
)

// remoteSession is a cluster whose agent is connected to another hub
// replica (the owner). This replica subscribed to the cluster over the
// peer link: the owner sent the agent's Hello and a snapshot, and forwards
// every delta, so the view here stays as fresh as the owner's. Requests
// travel to the owner with the caller's identity, and the owner runs them
// on its agent session.
type remoteSession struct {
	*clusterView
	cluster string
	owner   string // pod name of the owning replica
	link    *peerLink
	agents  *agents
	timeout time.Duration
	created time.Time

	info atomic.Pointer[protocol.Hello]

	done    chan struct{}
	endOnce sync.Once
}

func newRemoteSession(cluster string, l *peerLink, a *agents, timeout time.Duration) *remoteSession {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	return &remoteSession{
		clusterView: newClusterView(&a.rvSeq),
		cluster:     cluster,
		owner:       l.pod,
		link:        l,
		agents:      a,
		// The owner applies its own timeout first, so its answer (a timeout
		// error from the agent session) arrives before this one fires.
		timeout: timeout + 2*time.Second,
		created: time.Now(),
		done:    make(chan struct{}),
	}
}

func (r *remoteSession) name() string { return r.cluster }

func (r *remoteSession) hello() protocol.Hello {
	if h := r.info.Load(); h != nil {
		return *h
	}
	return protocol.Hello{}
}

func (r *remoteSession) closed() bool {
	select {
	case <-r.done:
		return true
	default:
		return r.link.isClosed()
	}
}

func (r *remoteSession) lastSeenAt() time.Time { return timeFromNanos(r.link.lastSeen.Load()) }

// end stops the subscription once. It tells the owner, unless the link is
// already gone.
func (r *remoteSession) end() {
	r.endOnce.Do(func() {
		close(r.done)
		r.link.removeMirror(r.cluster, r)
		if !r.link.isClosed() {
			_ = r.link.enqueue(protocol.PeerFrame{Cluster: r.cluster, Frame: protocol.Frame{Type: protocol.TypeUnsubscribe}})
		}
	})
}

// handle applies a frame the owner forwarded.
func (r *remoteSession) handle(f protocol.Frame) error {
	switch f.Type {
	case protocol.TypeHello:
		var h protocol.Hello
		if err := json.Unmarshal(f.Payload, &h); err != nil {
			return fmt.Errorf("decode relayed hello: %w", err)
		}
		if err := validateHello(&h, r.cluster); err != nil {
			return fmt.Errorf("relayed hello: %w", err)
		}
		r.info.Store(&h)
	case protocol.TypeSnapshot:
		if r.info.Load() == nil {
			return errors.New("relayed snapshot before hello")
		}
		var snap protocol.Snapshot
		if err := json.Unmarshal(f.Payload, &snap); err != nil {
			return fmt.Errorf("decode relayed snapshot: %w", err)
		}
		if r.startSnapshot(snap, r.snapshotDone) {
			r.snapshotDone()
		}
	case protocol.TypeDelta:
		var d protocol.Delta
		if err := json.Unmarshal(f.Payload, &d); err != nil {
			return fmt.Errorf("decode relayed delta: %w", err)
		}
		ch := r.apply(d)
		// As on the agent session: findings apply even when the delta commits a staged view.
		findingsChanged := r.setFindings(d.Findings)
		if ch.committed {
			r.snapshotDone()
			if findingsChanged {
				r.agents.emit(r, event{kind: evClusters, cluster: r.cluster, why: whyFindings})
			}
			return nil
		}
		for _, e := range changeEvents(r.cluster, ch, findingsChanged) {
			r.agents.emit(r, e)
		}
	}
	return nil
}

// snapshotDone announces a complete relayed snapshot.
func (r *remoteSession) snapshotDone() {
	r.agents.emit(r, event{kind: evResync, cluster: r.cluster})
	r.agents.emit(r, event{kind: evClusters})
}

// do relays a non-streaming request to the owner.
func (r *remoteSession) do(ctx context.Context, req protocol.Request) (json.RawMessage, error) {
	if r.closed() {
		return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, r.cluster)
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	return r.link.do(ctx, r.cluster, req)
}

// stream relays a log stream from the owner.
func (r *remoteSession) stream(ctx context.Context, req protocol.Request, onChunk func(protocol.LogChunk) error) error {
	if r.closed() {
		return fmt.Errorf("%w: %s", fleet.ErrDisconnected, r.cluster)
	}
	return r.link.stream(ctx, r.cluster, req, onChunk)
}

// relayError is the peer-channel form of an error from the owner's agent
// session. Agent errors keep their code; the two hub-side conditions use
// codes an agent never sends.
const (
	relayDisconnected = 502 // fleet.ErrDisconnected on the owner
	relayUnavailable  = 504 // errUnavailable (a timeout) on the owner
)

func toRelayError(err error) *protocol.Error {
	var ae *AgentError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae):
		return &protocol.Error{Code: ae.Code, Message: ae.Message}
	case errors.Is(err, fleet.ErrDisconnected):
		return &protocol.Error{Code: relayDisconnected, Message: "agent disconnected"}
	case errors.Is(err, fleet.ErrForbidden):
		return &protocol.Error{Code: 403, Message: err.Error()}
	case errors.Is(err, errUnavailable), errors.Is(err, context.DeadlineExceeded):
		return &protocol.Error{Code: relayUnavailable, Message: "request timed out on the replica that holds the agent"}
	}
	return &protocol.Error{Code: 500, Message: "relay failed"}
}

func fromRelayError(cluster string, pe *protocol.Error) error {
	switch {
	case pe == nil:
		return nil
	case pe.Code == relayDisconnected:
		return fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster)
	case pe.Code == relayUnavailable:
		return fmt.Errorf("%w: %s: %s", errUnavailable, cluster, pe.Message)
	}
	return agentError(cluster, pe)
}

// requestCtxError maps an ended request context to the hub's errors.
func requestCtxError(ctx context.Context, op protocol.Op, cluster string) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s request to cluster %s timed out", errUnavailable, op, cluster)
	}
	return ctx.Err()
}
