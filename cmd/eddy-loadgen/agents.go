//go:build dev

package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/agent"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/loadgen/synth"
	"github.com/idestis/eddy/internal/protocol"
)

// sarCounter counts the access checks and rules reviews (one per
// namespace) the fake agents answer, per user.
type sarCounter struct {
	mu            sync.Mutex
	byUser        map[string]int64
	rulesByUser   map[string]int64
	requests      atomic.Int64
	rulesRequests atomic.Int64
}

func (c *sarCounter) addRules(user string, n int) {
	c.mu.Lock()
	if c.rulesByUser == nil {
		c.rulesByUser = map[string]int64{}
	}
	c.rulesByUser[user] += int64(n)
	c.mu.Unlock()
	c.rulesRequests.Add(1)
}

func (c *sarCounter) rulesSnapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.rulesByUser))
	for k, v := range c.rulesByUser {
		out[k] = v
	}
	return out
}

func (c *sarCounter) add(user string, n int) {
	c.mu.Lock()
	if c.byUser == nil {
		c.byUser = map[string]int64{}
	}
	c.byUser[user] += int64(n)
	c.mu.Unlock()
	c.requests.Add(1)
}

func (c *sarCounter) snapshot() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int64, len(c.byUser))
	for k, v := range c.byUser {
		out[k] = v
	}
	return out
}

// fakeHandler answers hub requests like an agent: access checks and rules
// reviews from the synthetic RBAC policy; every other op is refused.
// noRules answers OpRules like an agent that predates it.
type fakeHandler struct {
	sar     *sarCounter
	noRules bool
}

func (h *fakeHandler) Handle(_ context.Context, req protocol.Request, _ func(protocol.LogChunk) error) (json.RawMessage, *protocol.Error) {
	if req.Op == protocol.OpRules && !h.noRules {
		var args protocol.RulesArgs
		if err := json.Unmarshal(req.Args, &args); err != nil {
			return nil, &protocol.Error{Code: 400, Message: "bad rules args"}
		}
		out := make([]protocol.NamespaceRules, len(args.Namespaces))
		for i, ns := range args.Namespaces {
			out[i] = protocol.NamespaceRules{Namespace: ns, Rules: synth.Rules(req.Identity, ns)}
		}
		h.sar.addRules(req.Identity.User, len(args.Namespaces))
		b, _ := json.Marshal(protocol.RulesResult{Namespaces: out})
		return b, nil
	}
	if req.Op != protocol.OpAccess {
		return nil, &protocol.Error{Code: 400, Message: "loadgen agent: unsupported op " + string(req.Op)}
	}
	var args protocol.AccessArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return nil, &protocol.Error{Code: 400, Message: "bad access args"}
	}
	out := make([]bool, len(args.Checks))
	for i, c := range args.Checks {
		out[i] = synth.Allow(req.Identity, c)
	}
	h.sar.add(req.Identity.User, len(args.Checks))
	b, _ := json.Marshal(protocol.AccessResult{Allowed: out})
	return b, nil
}

// watchedKinds is Hello.Kinds of a fake agent: every surfaced kind
// without a preset.
func watchedKinds() []string {
	var out []string
	for _, k := range flux.All() {
		if k.Surfaced && k.Preset == "" {
			out = append(out, k.Group+"/"+k.Kind)
		}
	}
	return out
}

// fakeAgent is one synthetic cluster's agent process.
type fakeAgent struct {
	session *agent.Session
	cluster *synth.Cluster
	cancel  context.CancelFunc
	done    chan struct{}
}

func startAgent(ctx context.Context, url, token, instance string, c *synth.Cluster, h *fakeHandler, opts agentOptions) *fakeAgent {
	ctx, cancel := context.WithCancel(ctx)
	s := &agent.Session{
		URL:     url,
		Cluster: c.Name,
		Token:   token,
		Hello: protocol.Hello{
			AgentVersion: "loadgen", KubernetesVersion: "v1.33.0", FluxVersion: "v2.7.0",
			Kinds: watchedKinds(),
		},
		Source:        c,
		Handler:       h,
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		Instance:      instance,
		MaxConcurrent: 64,
	}
	opts.apply(s)
	a := &fakeAgent{session: s, cluster: c, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		_ = s.Run(ctx)
	}()
	return a
}

func (a *fakeAgent) stop() {
	a.cancel()
	select {
	case <-a.done:
	case <-time.After(5 * time.Second):
	}
}

// connectedAgents counts the fake agents whose hub connection is up.
func connectedAgents(as []*fakeAgent) int {
	n := 0
	for _, a := range as {
		if a.session.Connected() {
			n++
		}
	}
	return n
}
