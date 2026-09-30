package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/flux"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/protocol"
)

// Session limits and timings.
const (
	// defaultRequestTimeout bounds every non-streaming agent request.
	defaultRequestTimeout = 15 * time.Second
	// frameWriteTimeout bounds one frame write to the agent.
	frameWriteTimeout = 10 * time.Second
	// idleTimeout closes a session that sent nothing (agents ping every 20 s).
	idleTimeout = 90 * time.Second
	// maxResources bounds one cluster's view, against a runaway agent.
	maxResources = 200_000
	// streamBuffer is the number of log chunks queued for a slow consumer
	// before its stream is cancelled.
	streamBuffer = 256
	// Per-agent frame rate: sustained and burst. Reading slows down (the
	// agent's writes back up) rather than dropping frames.
	frameRate  = 200
	frameBurst = 400
)

// agentSession is one connected agent. It owns the cluster's resource view
// for as long as the connection lives, and multiplexes hub requests over it.
type agentSession struct {
	cluster     string
	hello       protocol.Hello
	tokenHash   [32]byte
	conn        *websocket.Conn
	connectedAt time.Time
	log         *slog.Logger
	bus         *bus
	metrics     *metrics
	rvSeq       *atomic.Uint64
	limiter     *rate.Limiter
	timeout     time.Duration

	lastSeen atomic.Int64 // unix nanoseconds of the last frame

	mu        sync.RWMutex
	resources map[string]model.Resource
	synced    bool
	rv        uint64
	counts    map[accessTuple]map[model.Status]int
	countsRV  uint64

	reqMu   sync.Mutex
	pending map[string]*pendingRequest
	nextID  atomic.Uint64

	done      chan struct{}
	closeOnce sync.Once
}

type pendingRequest struct {
	stream bool
	final  chan protocol.Response // capacity 1
	chunks chan protocol.LogChunk // stream only
}

type sessionDeps struct {
	bus     *bus
	metrics *metrics
	rvSeq   *atomic.Uint64
	log     *slog.Logger
	timeout time.Duration
}

func newSession(cluster string, hello protocol.Hello, tokenHash [32]byte, conn *websocket.Conn, d sessionDeps) *agentSession {
	s := &agentSession{
		cluster:     cluster,
		hello:       hello,
		tokenHash:   tokenHash,
		conn:        conn,
		connectedAt: time.Now(),
		log:         d.log.With("cluster", cluster),
		bus:         d.bus,
		metrics:     d.metrics,
		rvSeq:       d.rvSeq,
		limiter:     rate.NewLimiter(frameRate, frameBurst),
		timeout:     d.timeout,
		resources:   map[string]model.Resource{},
		pending:     map[string]*pendingRequest{},
		done:        make(chan struct{}),
	}
	if s.timeout <= 0 {
		s.timeout = defaultRequestTimeout
	}
	s.rv = s.rvSeq.Add(1)
	s.lastSeen.Store(time.Now().UnixNano())
	return s
}

// run reads frames until the connection fails, ctx ends or close is called.
func (s *agentSession) run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(idleTimeout / 3)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				cancel()
				return
			case <-t.C:
				if time.Since(time.Unix(0, s.lastSeen.Load())) > idleTimeout {
					s.close(websocket.StatusGoingAway, "agent idle")
					cancel()
					return
				}
			}
		}
	}()
	err := s.readLoop(ctx)
	s.close(websocket.StatusGoingAway, "session ended")
	return err
}

// close ends the session once; pending requests fail with ErrDisconnected.
func (s *agentSession) close(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		close(s.done)
		// Close waits for the agent's close frame; never block the caller.
		go func() {
			if err := s.conn.Close(code, reason); err != nil {
				s.conn.CloseNow()
			}
		}()
	})
}

func (s *agentSession) closed() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *agentSession) readLoop(ctx context.Context) error {
	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("hub: read from agent %s: %w", s.cluster, err)
		}
		s.lastSeen.Store(time.Now().UnixNano())
		if !s.limiter.Allow() {
			s.metrics.framesThrottled.Add(1)
			if err := s.limiter.Wait(ctx); err != nil {
				return fmt.Errorf("hub: agent %s: %w", s.cluster, err)
			}
		}
		if typ != websocket.MessageText {
			continue
		}
		var f protocol.Frame
		if err := json.Unmarshal(data, &f); err != nil {
			s.metrics.framesInvalid.Add(1)
			s.log.Warn("invalid frame from agent", "err", err)
			continue
		}
		s.metrics.frame(f.Type)
		if err := s.handle(f); err != nil {
			s.metrics.framesInvalid.Add(1)
			s.log.Warn("invalid frame from agent", "type", string(f.Type), "err", err)
		}
	}
}

func (s *agentSession) handle(f protocol.Frame) error {
	switch f.Type {
	case protocol.TypeSnapshot:
		var snap protocol.Snapshot
		if err := json.Unmarshal(f.Payload, &snap); err != nil {
			return fmt.Errorf("decode snapshot: %w", err)
		}
		s.applySnapshot(snap)
	case protocol.TypeDelta:
		var d protocol.Delta
		if err := json.Unmarshal(f.Payload, &d); err != nil {
			return fmt.Errorf("decode delta: %w", err)
		}
		s.applyDelta(d)
	case protocol.TypeResponse, protocol.TypeStreamEnd:
		var resp protocol.Response
		if len(f.Payload) > 0 {
			if err := json.Unmarshal(f.Payload, &resp); err != nil {
				resp = protocol.Response{Error: &protocol.Error{Code: 500, Message: "invalid response from agent"}}
			}
		}
		s.finish(f.ID, resp)
	case protocol.TypeStream:
		var chunk protocol.LogChunk
		if err := json.Unmarshal(f.Payload, &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		s.chunk(f.ID, chunk)
	case protocol.TypePing:
	default:
		s.log.Debug("ignoring agent frame", "type", string(f.Type))
	}
	return nil
}

// sanitizeResource accepts only surfaced kinds from the kind table and
// recomputes the id, so an agent cannot inject other kinds (Secrets) or
// mislabel an object.
func sanitizeResource(r model.Resource) (model.Resource, bool) {
	k, ok := flux.KindByName(r.Kind)
	if !ok || !k.Surfaced || r.Group != k.Group || r.Name == "" || (k.Namespaced && r.Namespace == "") {
		return model.Resource{}, false
	}
	r.Kind = k.Kind
	r.ID = r.Ref.ID()
	return r, true
}

func (s *agentSession) applySnapshot(snap protocol.Snapshot) {
	m := make(map[string]model.Resource, len(snap.Resources))
	for _, r := range snap.Resources {
		if r, ok := sanitizeResource(r); ok && len(m) < maxResources {
			m[r.ID] = r
		}
	}
	s.mu.Lock()
	s.resources = m
	s.synced = true
	s.rv = s.rvSeq.Add(1)
	s.mu.Unlock()
	s.bus.publish(event{kind: evResync, cluster: s.cluster})
	s.bus.publish(event{kind: evClusters})
}

func (s *agentSession) applyDelta(d protocol.Delta) {
	upserts := make([]model.Resource, 0, len(d.Upserts))
	var deletes []string
	s.mu.Lock()
	for _, r := range d.Upserts {
		r, ok := sanitizeResource(r)
		if !ok {
			continue
		}
		if _, exists := s.resources[r.ID]; !exists && len(s.resources) >= maxResources {
			continue
		}
		s.resources[r.ID] = r
		upserts = append(upserts, r)
	}
	for _, id := range d.Deletes {
		if _, ok := s.resources[id]; ok {
			delete(s.resources, id)
			deletes = append(deletes, id)
		}
	}
	if len(upserts) > 0 || len(deletes) > 0 {
		s.rv = s.rvSeq.Add(1)
	}
	s.mu.Unlock()
	if len(upserts) == 0 && len(deletes) == 0 {
		return
	}
	s.bus.publish(event{kind: evChange, cluster: s.cluster, upserts: upserts, deletes: deletes})
	s.bus.publish(event{kind: evClusters})
}

// view returns a copy of every resource and the view's resourceVersion.
func (s *agentSession) view() ([]model.Resource, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]model.Resource, 0, len(s.resources))
	for _, r := range s.resources {
		out = append(out, r)
	}
	return out, strconv.FormatUint(s.rv, 10)
}

func (s *agentSession) lookup(id string) (model.Resource, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.resources[id]
	return r, ok
}

func (s *agentSession) size() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.resources)
}

// tupleCounts returns resource counts by access tuple and status, cached
// until the view changes. The returned map must not be modified.
func (s *agentSession) tupleCounts() map[accessTuple]map[model.Status]int {
	s.mu.RLock()
	if s.counts != nil && s.countsRV == s.rv {
		c := s.counts
		s.mu.RUnlock()
		return c
	}
	s.mu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts != nil && s.countsRV == s.rv {
		return s.counts
	}
	c := map[accessTuple]map[model.Status]int{}
	for _, r := range s.resources {
		t, ok := tupleOf(r.Ref)
		if !ok {
			continue
		}
		if c[t] == nil {
			c[t] = map[model.Status]int{}
		}
		c[t][r.Status]++
	}
	s.counts, s.countsRV = c, s.rv
	return c
}

// --- requests -------------------------------------------------------------

func (s *agentSession) send(ctx context.Context, typ protocol.FrameType, id string, payload any) error {
	f := protocol.Frame{Type: typ, ID: id}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("hub: encode %s frame: %w", typ, err)
		}
		f.Payload = b
	}
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("hub: encode %s frame: %w", typ, err)
	}
	if len(b) > protocol.MaxFrameBytes {
		return fmt.Errorf("hub: %s frame of %d bytes exceeds the frame limit", typ, len(b))
	}
	wctx, cancel := context.WithTimeout(ctx, frameWriteTimeout)
	defer cancel()
	if err := s.conn.Write(wctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("hub: write to agent %s: %w", s.cluster, err)
	}
	return nil
}

func (s *agentSession) register(stream bool) (string, *pendingRequest) {
	id := "h" + strconv.FormatUint(s.nextID.Add(1), 36)
	p := &pendingRequest{stream: stream, final: make(chan protocol.Response, 1)}
	if stream {
		p.chunks = make(chan protocol.LogChunk, streamBuffer)
	}
	s.reqMu.Lock()
	s.pending[id] = p
	s.reqMu.Unlock()
	return id, p
}

func (s *agentSession) unregister(id string) {
	s.reqMu.Lock()
	delete(s.pending, id)
	s.reqMu.Unlock()
}

func (s *agentSession) finish(id string, resp protocol.Response) {
	s.reqMu.Lock()
	p, ok := s.pending[id]
	delete(s.pending, id)
	s.reqMu.Unlock()
	if !ok {
		return
	}
	select {
	case p.final <- resp:
	default:
	}
}

func (s *agentSession) chunk(id string, c protocol.LogChunk) {
	s.reqMu.Lock()
	p, ok := s.pending[id]
	s.reqMu.Unlock()
	if !ok || !p.stream {
		return
	}
	select {
	case p.chunks <- c:
	default:
		// The consumer is too slow: end its stream instead of blocking
		// every other frame of this agent.
		s.finish(id, protocol.Response{Error: &protocol.Error{Code: 503, Message: "log stream consumer too slow"}})
		go s.cancel(id)
	}
}

// cancel tells the agent to stop request id. Failures are ignored: the
// request ends with the connection anyway.
func (s *agentSession) cancel(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), frameWriteTimeout)
	defer cancel()
	if err := s.send(ctx, protocol.TypeCancel, id, nil); err != nil {
		s.log.Debug("send cancel", "id", id, "err", err)
	}
}

// do runs a non-streaming request and returns its result.
func (s *agentSession) do(ctx context.Context, req protocol.Request) (json.RawMessage, error) {
	if s.closed() {
		return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, s.cluster)
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	id, p := s.register(false)
	defer s.unregister(id)
	if err := s.send(ctx, protocol.TypeRequest, id, req); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", fleet.ErrDisconnected, s.cluster, err)
	}
	select {
	case resp := <-p.final:
		if resp.Error != nil {
			return nil, agentError(s.cluster, resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		go s.cancel(id)
		return nil, s.ctxError(ctx, req.Op)
	case <-s.done:
		return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, s.cluster)
	}
}

// stream runs a streaming request (logs), calling onChunk for every chunk
// until the agent ends the stream, onChunk fails or ctx ends.
func (s *agentSession) stream(ctx context.Context, req protocol.Request, onChunk func(protocol.LogChunk) error) error {
	if s.closed() {
		return fmt.Errorf("%w: %s", fleet.ErrDisconnected, s.cluster)
	}
	id, p := s.register(true)
	defer s.unregister(id)
	if err := s.send(ctx, protocol.TypeRequest, id, req); err != nil {
		return fmt.Errorf("%w: %s: %w", fleet.ErrDisconnected, s.cluster, err)
	}
	stop := func(err error) error {
		go s.cancel(id)
		return err
	}
	for {
		select {
		case c := <-p.chunks:
			if err := onChunk(c); err != nil {
				return stop(err)
			}
		case resp := <-p.final:
			// Chunks queued before the end frame still belong to the stream.
			for {
				select {
				case c := <-p.chunks:
					if err := onChunk(c); err != nil {
						return err
					}
					continue
				default:
				}
				break
			}
			return agentError(s.cluster, resp.Error)
		case <-ctx.Done():
			return stop(s.ctxError(ctx, req.Op))
		case <-s.done:
			return fmt.Errorf("%w: %s", fleet.ErrDisconnected, s.cluster)
		}
	}
}

func (s *agentSession) ctxError(ctx context.Context, op protocol.Op) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s request to cluster %s timed out", errUnavailable, op, s.cluster)
	}
	return ctx.Err()
}
