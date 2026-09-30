package hub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/store"
)

// storeTimeout bounds one registry call to the store.
const storeTimeout = 5 * time.Second

// sessionRegistry records this replica's agent sessions in agent_sessions,
// where other replicas find the owner of a cluster (ADR-0004). The store is
// best effort here: when it is down, local sessions keep working and the
// heartbeat registers them again once it is back.
type sessionRegistry struct {
	st     store.AgentSessions
	events store.Events
	pod    string
	addr   func() string // this replica's peer address ("" without peers)
	agents *agents
	log    *slog.Logger
}

func (r *sessionRegistry) row(s *agentSession) store.AgentSession {
	return store.AgentSession{
		Cluster:       s.cluster,
		HubPod:        r.pod,
		HubAddr:       r.addr(),
		AgentInstance: s.instance,
		Seq:           s.seq,
		ConnectedAt:   s.connectedAt,
	}
}

// register records a new session. It returns errStaleSession when another
// replica already holds a newer connection of the same agent instance.
func (r *sessionRegistry) register(ctx context.Context, s *agentSession) error {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	err := r.st.Upsert(ctx, r.row(s))
	switch {
	case errors.Is(err, store.ErrConflict):
		return errStaleSession
	case err != nil:
		r.log.Warn("recording the agent session failed; other replicas cannot relay to it until the store is back",
			"cluster", s.cluster, "err", err)
		return nil
	}
	r.publish(ctx, s.cluster)
	return nil
}

// heartbeat refreshes the session's row every agentHeartbeatEvery until the
// session ends. A row that is gone is recorded again (the store may have
// lost it); a row another replica took over closes the session.
func (r *sessionRegistry) heartbeat(ctx context.Context, s *agentSession) {
	t := time.NewTicker(agentHeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-t.C:
		}
		hctx, cancel := context.WithTimeout(ctx, storeTimeout)
		err := r.st.Heartbeat(hctx, s.cluster, r.pod, s.instance, time.Now())
		if errors.Is(err, store.ErrNotFound) {
			err = r.st.Upsert(hctx, r.row(s))
			if errors.Is(err, store.ErrConflict) {
				cancel()
				r.log.Info("closing agent session: a newer connection of this agent instance is on another replica",
					"cluster", s.cluster, "instance", s.instance)
				s.close(websocket.StatusPolicyViolation, "replaced by a newer connection")
				return
			}
			if err == nil {
				r.publish(hctx, s.cluster)
			}
		}
		cancel()
		if err != nil && ctx.Err() == nil {
			r.log.Warn("agent session heartbeat failed", "cluster", s.cluster, "err", err)
		}
	}
}

// unregister deletes the session's row, unless another replica took it over.
func (r *sessionRegistry) unregister(s *agentSession) {
	ctx, cancel := context.WithTimeout(context.Background(), storeTimeout)
	defer cancel()
	if err := r.st.Delete(ctx, s.cluster, r.pod, s.instance); err != nil {
		r.log.Warn("removing the agent session record failed; it expires after 30s", "cluster", s.cluster, "err", err)
		return
	}
	r.publish(ctx, s.cluster)
}

// checkTakeover closes local sessions of cluster whose agent instance now
// has a newer connection on another replica. It runs on every agent event.
func (r *sessionRegistry) checkTakeover(ctx context.Context, cluster string) {
	local := r.agents.all()
	if cluster != "" {
		local = nil
		for _, s := range r.agents.all() {
			if s.cluster == cluster {
				local = append(local, s)
			}
		}
	}
	if len(local) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	rows, err := r.st.List(ctx, cluster, time.Now().Add(-agentFreshFor))
	if err != nil {
		return
	}
	for _, s := range local {
		for _, row := range rows {
			if row.Cluster == s.cluster && row.AgentInstance == s.instance && row.HubPod != r.pod && row.Seq > s.seq {
				r.log.Info("closing agent session: a newer connection of this agent instance is on another replica",
					"cluster", s.cluster, "instance", s.instance, "replica", row.HubPod)
				s.close(websocket.StatusPolicyViolation, "replaced by a newer connection")
			}
		}
	}
}

// forgetAll removes every row of this replica (start and shutdown).
func (r *sessionRegistry) forgetAll(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, storeTimeout)
	defer cancel()
	if err := r.st.DeleteByHub(ctx, r.pod); err != nil {
		r.log.Warn("clearing this replica's agent session records failed", "err", err)
		return
	}
	if r.events != nil {
		if err := r.events.Publish(ctx, store.Event{Kind: store.EventAgent}); err != nil {
			r.log.Debug("publish agent event", "err", err)
		}
	}
}

func (r *sessionRegistry) publish(ctx context.Context, cluster string) {
	if r.events == nil {
		return
	}
	if err := r.events.Publish(ctx, store.Event{Kind: store.EventAgent, Cluster: cluster}); err != nil {
		r.log.Debug("publish agent event", "cluster", cluster, "err", err)
	}
}
