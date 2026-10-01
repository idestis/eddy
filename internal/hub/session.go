package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
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

// agentSession is one connected agent. It keeps its own view of the
// cluster for as long as the connection lives, and multiplexes hub requests
// over it. With several agent replicas a cluster has several sessions; the
// agents registry picks one as the primary and only the primary's changes
// reach the bus.
type agentSession struct {
	*clusterView
	cluster     string
	info        protocol.Hello
	instance    string // Hello.Instance, or legacyInstance
	seq         int64
	tokenHash   [32]byte
	conn        *websocket.Conn
	connectedAt time.Time
	log         *slog.Logger
	emit        func(clusterSession, event)
	metrics     *metrics
	limiter     *rate.Limiter
	timeout     time.Duration

	lastSeen atomic.Int64 // unix nanoseconds of the last frame

	reqs requestTable

	done      chan struct{}
	closeOnce sync.Once
}

type pendingRequest struct {
	stream bool
	final  chan protocol.Response // capacity 1
	chunks chan protocol.LogChunk // stream only
}

// requestTable correlates request ids with their pending replies.
type requestTable struct {
	prefix  string
	mu      sync.Mutex
	pending map[string]*pendingRequest
	nextID  atomic.Uint64
}

func (t *requestTable) register(stream bool) (string, *pendingRequest) {
	id := t.prefix + strconv.FormatUint(t.nextID.Add(1), 36)
	p := &pendingRequest{stream: stream, final: make(chan protocol.Response, 1)}
	if stream {
		p.chunks = make(chan protocol.LogChunk, streamBuffer)
	}
	t.mu.Lock()
	if t.pending == nil {
		t.pending = map[string]*pendingRequest{}
	}
	t.pending[id] = p
	t.mu.Unlock()
	return id, p
}

func (t *requestTable) unregister(id string) {
	t.mu.Lock()
	delete(t.pending, id)
	t.mu.Unlock()
}

// finish delivers the final reply of id; unknown ids are ignored.
func (t *requestTable) finish(id string, resp protocol.Response) {
	t.mu.Lock()
	p, ok := t.pending[id]
	delete(t.pending, id)
	t.mu.Unlock()
	if !ok {
		return
	}
	select {
	case p.final <- resp:
	default:
	}
}

// chunk delivers one stream chunk. It reports false when the consumer is
// too slow; the caller then ends the stream.
func (t *requestTable) chunk(id string, c protocol.LogChunk) bool {
	t.mu.Lock()
	p, ok := t.pending[id]
	t.mu.Unlock()
	if !ok || !p.stream {
		return true
	}
	select {
	case p.chunks <- c:
		return true
	default:
		return false
	}
}

type sessionDeps struct {
	bus *bus
	// emit receives the session's events; nil publishes them to bus.
	emit    func(clusterSession, event)
	metrics *metrics
	rvSeq   *atomic.Uint64
	log     *slog.Logger
	timeout time.Duration
}

func newSession(cluster string, hello protocol.Hello, tokenHash [32]byte, conn *websocket.Conn, d sessionDeps) *agentSession {
	s := &agentSession{
		clusterView: newClusterView(d.rvSeq),
		cluster:     cluster,
		info:        hello,
		tokenHash:   tokenHash,
		conn:        conn,
		connectedAt: time.Now(),
		log:         d.log.With("cluster", cluster),
		emit:        d.emit,
		metrics:     d.metrics,
		limiter:     rate.NewLimiter(frameRate, frameBurst),
		timeout:     d.timeout,
		reqs:        requestTable{prefix: "h"},
		done:        make(chan struct{}),
	}
	if s.emit == nil {
		b := d.bus
		s.emit = func(_ clusterSession, e event) { b.publish(e) }
	}
	if s.timeout <= 0 {
		s.timeout = defaultRequestTimeout
	}
	s.lastSeen.Store(time.Now().UnixNano())
	return s
}

func (s *agentSession) name() string          { return s.cluster }
func (s *agentSession) hello() protocol.Hello { return s.info }
func (s *agentSession) lastSeenAt() time.Time { return timeFromNanos(s.lastSeen.Load()) }

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
		if typ != websocket.MessageText {
			continue
		}
		var f protocol.Frame
		if err := json.Unmarshal(data, &f); err != nil {
			s.metrics.framesInvalid.Add(1)
			s.log.Warn("invalid frame from agent", "err", err)
			continue
		}
		// Replies to the hub's own requests are bounded by the hub's
		// requests in flight; only frames the agent sends on its own are
		// rate limited. Waiting here also stops pongs from being read, so
		// throttling a burst of SAR answers would make the agent's pings
		// time out and the session drop.
		if f.Type != protocol.TypeResponse && !s.limiter.Allow() {
			s.metrics.framesThrottled.Add(1)
			if err := s.limiter.Wait(ctx); err != nil {
				return fmt.Errorf("hub: agent %s: %w", s.cluster, err)
			}
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
		s.reqs.finish(f.ID, decodeResponse(f.Payload, "agent"))
	case protocol.TypeStream:
		var chunk protocol.LogChunk
		if err := json.Unmarshal(f.Payload, &chunk); err != nil {
			return fmt.Errorf("decode stream chunk: %w", err)
		}
		if !s.reqs.chunk(f.ID, chunk) {
			// The consumer is too slow: end its stream instead of blocking
			// every other frame of this agent.
			s.reqs.finish(f.ID, protocol.Response{Error: &protocol.Error{Code: 503, Message: "log stream consumer too slow"}})
			go s.cancel(f.ID)
		}
	case protocol.TypePing:
	default:
		s.log.Debug("ignoring agent frame", "type", string(f.Type))
	}
	return nil
}

// decodeResponse reads a Response payload; an unreadable one becomes a 500.
func decodeResponse(payload json.RawMessage, from string) protocol.Response {
	var resp protocol.Response
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &resp); err != nil {
			resp = protocol.Response{Error: &protocol.Error{Code: 500, Message: "invalid response from " + from}}
		}
	}
	return resp
}

// sanitizeResource accepts only surfaced kinds from the kind table and
// recomputes the id, so an agent cannot inject other kinds (Secrets) or
// mislabel an object.
//
// Inventory-only rows are the exception: any valid Kind name is accepted,
// but the row is reduced to its Ref, version, owner (which must be a
// Kustomization) and resourceVersion, with Status "unknown".
func sanitizeResource(r model.Resource) (model.Resource, bool) {
	if r.InventoryOnly {
		return sanitizeInventoryRow(r)
	}
	k, ok := flux.KindByName(r.Kind)
	if !ok || !k.Surfaced || r.Group != k.Group || r.Name == "" || (k.Namespaced && r.Namespace == "") || (!k.Namespaced && r.Namespace != "") {
		return model.Resource{}, false
	}
	r.Kind = k.Kind
	r.ID = r.Ref.ID()
	r.Project = flux.ProjectOf(k.Group).ID
	r.Details = sanitizeDetails(r.Details)
	r.DependsOn, r.Blocked = sanitizeDependsOn(r), r.Blocked && hasDependsOn(k)
	return r, true
}

// sanitizeDetails caps the number and size of Resource.Details.
func sanitizeDetails(in []model.Detail) []model.Detail {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Detail, 0, min(len(in), model.MaxDetails))
	for _, d := range in {
		if len(out) == model.MaxDetails {
			break
		}
		d.Label, d.Value = cleanText(d.Label, 64), cleanText(d.Value, 256)
		if d.Label == "" || d.Value == "" {
			continue
		}
		out = append(out, d)
	}
	return out
}

func sanitizeInventoryRow(r model.Resource) (model.Resource, bool) {
	o := r.Owner
	if r.Name == "" || len(r.Name) > 253 || len(r.Namespace) > 63 || len(r.Group) > 253 || !flux.ValidKindName(r.Kind) ||
		o == nil || o.Group != flux.GroupKustomize || o.Kind != flux.KindKustomization || o.Name == "" || o.Namespace == "" {
		return model.Resource{}, false
	}
	owner := *o
	out := model.Resource{
		Ref:             r.Ref,
		ID:              r.Ref.ID(),
		Version:         truncate(r.Version, 32),
		Status:          model.StatusUnknown,
		InventoryOnly:   true,
		Owner:           &owner,
		Project:         flux.ProjectOf(r.Group).ID,
		ResourceVersion: truncate(r.ResourceVersion, 64),
	}
	return out, true
}

func (s *agentSession) applySnapshot(snap protocol.Snapshot) {
	if s.startSnapshot(snap, s.snapshotDone) {
		s.snapshotDone()
	}
}

// snapshotDone announces a complete snapshot: SSE clients refetch and the
// session may become the primary.
func (s *agentSession) snapshotDone() {
	s.emit(s, event{kind: evResync, cluster: s.cluster})
	s.emit(s, event{kind: evClusters})
}

func (s *agentSession) applyDelta(d protocol.Delta) {
	ch := s.apply(d)
	// Findings travel as a full replacement and are independent of snapshot
	// staging, so they are applied even when this delta commits a staged view;
	// returning first would drop them (a reconnect race).
	findingsChanged := s.setFindings(d.Findings)
	if ch.committed {
		s.snapshotDone()
		if findingsChanged {
			s.emit(s, event{kind: evFindings, cluster: s.cluster, findings: s.findingList()})
		}
		return
	}
	if findingsChanged {
		s.emit(s, event{kind: evFindings, cluster: s.cluster, findings: s.findingList()})
	}
	for _, e := range changeEvents(s.cluster, ch, findingsChanged) {
		s.emit(s, e)
	}
}

// --- requests -------------------------------------------------------------

// send writes one frame to the agent. ctx, usually the caller's request,
// only decides whether the frame is still wanted: coder/websocket closes the
// connection when the context of a write in progress ends, so a caller that
// gives up mid-write would otherwise disconnect the cluster for everyone.
// The write itself is bounded by frameWriteTimeout, and close ends it.
func (s *agentSession) send(ctx context.Context, typ protocol.FrameType, id string, payload any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("hub: write to agent %s: %w", s.cluster, err)
	}
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
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), frameWriteTimeout)
	defer cancel()
	if err := s.conn.Write(wctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("hub: write to agent %s: %w", s.cluster, err)
	}
	return nil
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
	id, p := s.reqs.register(false)
	defer s.reqs.unregister(id)
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
	id, p := s.reqs.register(true)
	defer s.reqs.unregister(id)
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
	return requestCtxError(ctx, op, s.cluster)
}
