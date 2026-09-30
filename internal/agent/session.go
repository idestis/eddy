package agent

import (
	"context"
	crand "crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// Session timings. Tests shorten them through the Session fields.
const (
	defaultDeltaInterval  = 250 * time.Millisecond
	defaultPingInterval   = 20 * time.Second
	defaultMaxConcurrent  = 16
	defaultMaxLogStreams  = 8
	dialTimeout           = 15 * time.Second
	writeTimeout          = 30 * time.Second
	pingTimeout           = 10 * time.Second
	requestTimeout        = 30 * time.Second
	logsRequestTimeout    = 2 * time.Minute
	backoffBase           = 500 * time.Millisecond
	backoffMax            = 30 * time.Second
	backoffResetAfter     = 30 * time.Second
	frameEnvelopeHeadroom = 4096
)

// Source provides the state the session sends: a full snapshot on connect,
// then the changes since.
type Source interface {
	Snapshot() []model.Resource
	Drain() (upserts []model.Resource, deletes []string)
}

// FindingSource is optionally implemented by a Source (the Cache): its
// findings travel in the snapshot and, when their version changes, in a
// delta.
type FindingSource interface {
	Findings() ([]model.Finding, uint64)
}

// RequestHandler executes one hub request. For OpLogs it calls stream for
// every chunk and returns no result.
type RequestHandler interface {
	Handle(ctx context.Context, req protocol.Request, stream func(protocol.LogChunk) error) (json.RawMessage, *protocol.Error)
}

// Session keeps one WebSocket connection to the hub alive, reconnecting with
// jittered exponential backoff capped at 30 s.
//
// On every connection it sends Hello, then the snapshot, then a Delta at most
// every DeltaInterval. A frame may not exceed protocol.MaxFrameBytes, so a
// large snapshot is split: the first chunk travels as the Snapshot frame
// (which replaces the hub's view) and the remaining resources follow
// immediately as Delta frames carrying only upserts. The hub must therefore
// treat a Snapshot as "replace" and apply the following Deltas on top, which
// is what it does for any Delta anyway.
type Session struct {
	URL     string // hub endpoint, e.g. wss://hub.example.com/agent/v1/connect
	Cluster string
	Token   string
	// Creds, when set, replaces Token: it may hold a join token that the
	// hub trades for a permanent token (ADR-0005).
	Creds   *Credentials
	TLS     *tls.Config // nil uses the system roots
	Hello   protocol.Hello
	Source  Source
	Handler RequestHandler
	Logger  *slog.Logger

	DeltaInterval time.Duration
	PingInterval  time.Duration
	// MaxConcurrent caps requests in flight; MaxLogStreams caps the log
	// streams among them. Excess requests get a 503.
	MaxConcurrent int
	MaxLogStreams int
	// Instance identifies this agent process to the hub (Hello.Instance);
	// empty uses one random id per process.
	Instance string

	connected atomic.Bool
	seq       atomic.Int64
}

// processInstance is Hello.Instance for every session of this process.
var processInstance = newInstanceID()

func newInstanceID() string {
	var b [12]byte
	if _, err := crand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to a
		// time-based id rather than refusing to start.
		return fmt.Sprintf("t%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// errRejoin ends a join connection after the hub sent credentials, so the
// session reconnects at once with the new token.
var errRejoin = errors.New("agent: received credentials from the hub; reconnecting with them")

func (s *Session) token() string {
	if s.Creds != nil {
		return s.Creds.Current()
	}
	return s.Token
}

// Connected reports whether a hub connection is up and its snapshot was sent.
func (s *Session) Connected() bool { return s.connected.Load() }

// Run connects until ctx is done.
func (s *Session) Run(ctx context.Context) error {
	attempt := 0
	for {
		start := time.Now()
		joining := s.Creds != nil && s.Creds.Joining()
		err := s.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errRejoin) {
			s.Logger.Info("agent: joined the hub; reconnecting with the permanent token")
			attempt = 0
			continue
		}
		if joining {
			// Another replica may have joined with the same join token and
			// stored the permanent token in the Secret.
			if changed, rerr := s.Creds.Refresh(ctx); rerr != nil {
				s.Logger.Warn("agent: re-read token secret", "error", rerr)
			} else if changed {
				s.Logger.Info("agent: found a permanent token in the token secret; using it")
				attempt = 0
				continue
			}
		}
		if time.Since(start) > backoffResetAfter {
			attempt = 0
		}
		delay := backoff(attempt)
		attempt++
		s.Logger.Warn("agent: hub connection lost", "error", err, "retryIn", delay.String())
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// backoff returns a random delay in [d/2, d] where d doubles per attempt up
// to backoffMax.
func backoff(attempt int) time.Duration {
	d := backoffMax
	if attempt < 16 {
		d = min(backoffBase<<attempt, backoffMax)
	}
	return d/2 + rand.N(d/2+1)
}

func (s *Session) dialURL() (string, error) {
	u, err := url.Parse(s.URL)
	if err != nil {
		return "", fmt.Errorf("agent: parse hub url: %w", err)
	}
	q := u.Query()
	q.Set("cluster", s.Cluster)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (s *Session) connectOnce(ctx context.Context) error {
	target, err := s.dialURL()
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if s.TLS != nil {
		transport.TLSClientConfig = s.TLS.Clone()
	}
	token := s.token()
	joining := isJoinToken(token)
	dialCtx, cancelDial := context.WithTimeout(ctx, dialTimeout)
	conn, resp, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport},
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	cancelDial()
	if err != nil {
		if resp != nil {
			return fmt.Errorf("agent: dial hub: http %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("agent: dial hub: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(protocol.MaxFrameBytes)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c := &sessionConn{conn: conn}

	hello := s.Hello
	hello.Protocol, hello.Cluster = protocol.Version, s.Cluster
	hello.Instance = s.Instance
	if hello.Instance == "" {
		hello.Instance = processInstance
	}
	hello.Seq = s.seq.Add(1)
	if s.Creds != nil && hello.Diagnostics != nil {
		d := *hello.Diagnostics
		d.CredentialsError = s.Creds.LastError()
		hello.Diagnostics = &d
	}
	if err := c.send(ctx, protocol.TypeHello, "", hello); err != nil {
		return err
	}
	if joining {
		// A join connection only trades the join token for credentials: no
		// snapshot, no deltas and no requests.
		s.Logger.Info("agent: joining the hub with a join token", "url", s.URL, "cluster", s.Cluster)
		errc := make(chan error, 2)
		go func() { errc <- s.joinLoop(ctx, c) }()
		go func() { errc <- s.pingLoop(ctx, c) }()
		err := <-errc
		cancel()
		conn.CloseNow()
		return err
	}
	if err := s.sendSnapshot(ctx, c); err != nil {
		return err
	}
	s.connected.Store(true)
	defer s.connected.Store(false)
	s.Logger.Info("agent: connected to hub", "url", s.URL, "cluster", s.Cluster)

	var inflight sync.WaitGroup
	errc := make(chan error, 3)
	go func() { errc <- s.readLoop(ctx, c, &inflight) }()
	go func() { errc <- s.deltaLoop(ctx, c) }()
	go func() { errc <- s.pingLoop(ctx, c) }()
	err = <-errc
	cancel()
	conn.CloseNow()
	inflight.Wait()
	return err
}

// joinLoop waits for the hub's credentials frame, stores the token and
// answers. It returns errRejoin once the credentials are answered.
func (s *Session) joinLoop(ctx context.Context, c *sessionConn) error {
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("agent: join: read from hub: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var f protocol.Frame
		if err := json.Unmarshal(data, &f); err != nil || f.Type != protocol.TypeCredentials {
			continue
		}
		var cr protocol.Credentials
		res := protocol.CredentialsResult{Error: "invalid credentials frame"}
		if err := json.Unmarshal(f.Payload, &cr); err == nil && s.Creds != nil {
			res = s.Creds.Accept(ctx, cr.Token)
		}
		if res.Error != "" {
			s.Logger.Warn("agent: could not store the token from the hub; keeping it in memory", "error", res.Error)
		}
		b, err := json.Marshal(res)
		if err != nil {
			return fmt.Errorf("agent: encode credentials result: %w", err)
		}
		if err := c.send(ctx, protocol.TypeResponse, f.ID, protocol.Response{Result: b}); err != nil {
			return err
		}
		if s.Creds != nil && !s.Creds.Joining() {
			return errRejoin
		}
	}
}

// findings returns the source's findings when their version differs from
// *sent, and records the new version.
func (s *Session) findings(sent *uint64, force bool) *protocol.FindingSet {
	fs, ok := s.Source.(FindingSource)
	if !ok {
		return nil
	}
	items, ver := fs.Findings()
	if !force && ver == *sent {
		return nil
	}
	*sent = ver
	if items == nil {
		items = []model.Finding{}
	}
	if len(items) > maxFindings {
		items = items[:maxFindings]
	}
	return &protocol.FindingSet{Items: items}
}

// maxFindings caps the findings of one frame (one per namespace at most).
const maxFindings = 500

func (s *Session) sendSnapshot(ctx context.Context, c *sessionConn) error {
	chunks, skipped := splitResources(s.Source.Snapshot(), protocol.MaxFrameBytes-frameEnvelopeHeadroom)
	if skipped > 0 {
		s.Logger.Warn("agent: resources too large for a frame were skipped", "count", skipped)
	}
	first := []model.Resource{}
	if len(chunks) > 0 {
		first = chunks[0]
	}
	if err := c.send(ctx, protocol.TypeSnapshot, "", protocol.Snapshot{Resources: first, Findings: s.findings(&c.findingsVer, true)}); err != nil {
		return err
	}
	for _, rs := range chunks[min(1, len(chunks)):] {
		if err := c.send(ctx, protocol.TypeDelta, "", protocol.Delta{Upserts: rs}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Session) deltaLoop(ctx context.Context, c *sessionConn) error {
	t := time.NewTicker(orDefault(s.DeltaInterval, defaultDeltaInterval))
	defer t.Stop()
	budget := protocol.MaxFrameBytes - frameEnvelopeHeadroom
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		upserts, deletes := s.Source.Drain()
		if f := s.findings(&c.findingsVer, false); f != nil {
			if err := c.send(ctx, protocol.TypeDelta, "", protocol.Delta{Findings: f}); err != nil {
				return err
			}
		}
		if len(upserts) == 0 && len(deletes) == 0 {
			continue
		}
		// Changes lost to a failed send are recovered by the snapshot of the
		// next connection.
		chunks, skipped := splitResources(upserts, budget)
		if skipped > 0 {
			s.Logger.Warn("agent: resources too large for a frame were skipped", "count", skipped)
		}
		for _, rs := range chunks {
			if err := c.send(ctx, protocol.TypeDelta, "", protocol.Delta{Upserts: rs}); err != nil {
				return err
			}
		}
		for ids := range splitIDs(deletes, budget) {
			if err := c.send(ctx, protocol.TypeDelta, "", protocol.Delta{Deletes: ids}); err != nil {
				return err
			}
		}
	}
}

func (s *Session) pingLoop(ctx context.Context, c *sessionConn) error {
	t := time.NewTicker(orDefault(s.PingInterval, defaultPingInterval))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		if err := c.send(ctx, protocol.TypePing, "", nil); err != nil {
			return err
		}
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		err := c.conn.Ping(pctx)
		cancel()
		if err != nil {
			return fmt.Errorf("agent: ping hub: %w", err)
		}
	}
}

func (s *Session) readLoop(ctx context.Context, c *sessionConn, inflight *sync.WaitGroup) error {
	sem := make(chan struct{}, orDefault(s.MaxConcurrent, defaultMaxConcurrent))
	logSem := make(chan struct{}, orDefault(s.MaxLogStreams, defaultMaxLogStreams))
	var mu sync.Mutex
	cancels := map[string]context.CancelFunc{}
	for {
		typ, data, err := c.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("agent: read from hub: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var f protocol.Frame
		if err := json.Unmarshal(data, &f); err != nil {
			s.Logger.Warn("agent: invalid frame from hub", "error", err)
			continue
		}
		switch f.Type {
		case protocol.TypeRequest:
			mu.Lock()
			_, dup := cancels[f.ID]
			mu.Unlock()
			if f.ID == "" || dup {
				s.reject(ctx, c, f, &protocol.Error{Code: 400, Message: "agent: missing or duplicate request id"})
				continue
			}
			select {
			case sem <- struct{}{}:
			default:
				s.reject(ctx, c, f, &protocol.Error{Code: 503, Message: "agent: too many concurrent requests"})
				continue
			}
			var op struct {
				Op protocol.Op `json:"op"`
			}
			_ = json.Unmarshal(f.Payload, &op)
			isLogs := op.Op == protocol.OpLogs
			if isLogs {
				select {
				case logSem <- struct{}{}:
				default:
					<-sem
					s.reject(ctx, c, f, &protocol.Error{Code: 503, Message: "agent: too many concurrent log streams"})
					continue
				}
			}
			rctx, cancel := context.WithCancel(ctx)
			mu.Lock()
			cancels[f.ID] = cancel
			mu.Unlock()
			inflight.Go(func() {
				defer func() {
					mu.Lock()
					delete(cancels, f.ID)
					mu.Unlock()
					cancel()
					if isLogs {
						<-logSem
					}
					<-sem
				}()
				s.serve(rctx, c, f)
			})
		case protocol.TypeCancel:
			mu.Lock()
			if cancel, ok := cancels[f.ID]; ok {
				cancel()
			}
			mu.Unlock()
		case protocol.TypePing:
		default:
			s.Logger.Debug("agent: ignoring frame", "type", string(f.Type))
		}
	}
}

// serve runs one request. Logs are streamed as Stream frames and always end
// with a StreamEnd frame, whose payload carries an Error on failure; a
// cancellation by the hub ends the stream without an error. Every other op
// gets exactly one Response frame.
func (s *Session) serve(ctx context.Context, c *sessionConn, f protocol.Frame) {
	var req protocol.Request
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		s.reply(ctx, c, f.ID, nil, &protocol.Error{Code: 400, Message: "agent: invalid request: " + err.Error()})
		return
	}
	if req.Op != protocol.OpLogs {
		rctx, cancel := context.WithTimeout(ctx, requestTimeout)
		defer cancel()
		result, perr := s.Handler.Handle(rctx, req, nil)
		if len(result) > protocol.MaxFrameBytes-frameEnvelopeHeadroom {
			result, perr = nil, &protocol.Error{Code: 500, Message: "agent: result exceeds the frame limit"}
		}
		s.reply(ctx, c, f.ID, result, perr)
		return
	}

	rctx := ctx
	var args protocol.LogsArgs
	if err := decodeArgs(req.Args, &args); err == nil && !args.Follow {
		var cancel context.CancelFunc
		rctx, cancel = context.WithTimeout(ctx, logsRequestTimeout)
		defer cancel()
	}
	_, perr := s.Handler.Handle(rctx, req, func(chunk protocol.LogChunk) error {
		return c.send(rctx, protocol.TypeStream, f.ID, chunk)
	})
	if ctx.Err() != nil {
		perr = nil // cancelled by the hub or the connection is gone
	}
	if err := c.send(context.WithoutCancel(ctx), protocol.TypeStreamEnd, f.ID, protocol.Response{Error: perr}); err != nil {
		s.Logger.Debug("agent: send stream end", "id", f.ID, "error", err)
	}
}

// reject answers a request that will not run, in the frame type the hub
// expects for its op: StreamEnd for logs, Response otherwise.
func (s *Session) reject(ctx context.Context, c *sessionConn, f protocol.Frame, perr *protocol.Error) {
	var req protocol.Request
	if json.Unmarshal(f.Payload, &req) == nil && req.Op == protocol.OpLogs {
		if err := c.send(ctx, protocol.TypeStreamEnd, f.ID, protocol.Response{Error: perr}); err != nil {
			s.Logger.Debug("agent: send stream end", "id", f.ID, "error", err)
		}
		return
	}
	s.reply(ctx, c, f.ID, nil, perr)
}

func (s *Session) reply(ctx context.Context, c *sessionConn, id string, result json.RawMessage, perr *protocol.Error) {
	if err := c.send(ctx, protocol.TypeResponse, id, protocol.Response{Result: result, Error: perr}); err != nil {
		s.Logger.Debug("agent: send response", "id", id, "error", err)
	}
}

// sessionConn writes frames. coder/websocket allows concurrent writers.
type sessionConn struct {
	conn *websocket.Conn
	// findingsVer is the findings version last sent on this connection;
	// only the snapshot and then the delta loop touch it, in that order.
	findingsVer uint64
}

func (c *sessionConn) send(ctx context.Context, typ protocol.FrameType, id string, payload any) error {
	f := protocol.Frame{Type: typ, ID: id}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("agent: encode %s frame: %w", typ, err)
		}
		f.Payload = b
	}
	b, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("agent: encode %s frame: %w", typ, err)
	}
	if len(b) > protocol.MaxFrameBytes {
		return fmt.Errorf("agent: %s frame of %d bytes exceeds the frame limit", typ, len(b))
	}
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := c.conn.Write(wctx, websocket.MessageText, b); err != nil {
		return fmt.Errorf("agent: write %s frame: %w", typ, err)
	}
	return nil
}

// splitResources groups resources into chunks whose JSON stays under budget
// bytes. A resource that alone exceeds the budget is skipped and counted.
func splitResources(rs []model.Resource, budget int) (chunks [][]model.Resource, skipped int) {
	var cur []model.Resource
	size := 0
	for _, r := range rs {
		b, err := json.Marshal(r)
		if err != nil || len(b)+1 > budget {
			skipped++
			continue
		}
		if size+len(b)+1 > budget && len(cur) > 0 {
			chunks = append(chunks, cur)
			cur, size = nil, 0
		}
		cur = append(cur, r)
		size += len(b) + 1
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks, skipped
}

// splitIDs yields groups of ids whose JSON stays under budget bytes.
func splitIDs(ids []string, budget int) func(func([]string) bool) {
	return func(yield func([]string) bool) {
		start, size := 0, 0
		for i, id := range ids {
			n := len(id) + 8 // quotes, comma and escaping headroom
			if size+n > budget && i > start {
				if !yield(ids[start:i]) {
					return
				}
				start, size = i, 0
			}
			size += n
		}
		if start < len(ids) {
			yield(ids[start:])
		}
	}
}

func orDefault[T int | time.Duration](v, def T) T {
	if v > 0 {
		return v
	}
	return def
}
