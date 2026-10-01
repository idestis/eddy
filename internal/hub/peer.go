package hub

import (
	"cmp"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/store"
)

// Peer channel settings (ADR-0004).
const (
	peerPath        = "/peer/v" + protocol.PeerVersion + "/connect"
	peerAuthScheme  = "EddyPeer"
	peerAddrHeader  = "X-Eddy-Peer-Addr"
	peerReplyHeader = "X-Eddy-Peer-Auth"
	// peerSkew is how far a peer's clock may be from ours.
	peerSkew        = 60 * time.Second
	peerPingEvery   = 20 * time.Second
	peerIdleTimeout = 90 * time.Second
	peerDialTimeout = 10 * time.Second
	// peerOutBuffer frames may wait for a slow peer before the link is
	// dropped (it redials and resubscribes).
	peerOutBuffer = 4096
	peerReadLimit = protocol.MaxFrameBytes + 64<<10
	// peerEvaluateEvery re-reads agent_sessions and re-resolves peer DNS.
	peerEvaluateEvery = 10 * time.Second
	// peerLinkGrace is how long a discovered peer may stay unlinked, or a
	// mirror unsynced, before readiness fails. A peer we are not meant to
	// dial is dialled anyway after half of it.
	peerLinkGrace = 30 * time.Second
	// peerSnapshotBudget leaves room for the peer envelope around a chunk.
	peerSnapshotBudget = protocol.MaxFrameBytes - 8192
)

// peerNode is this replica's side of the peer channel. It holds one link
// per peer replica, serves subscriptions to the clusters whose agents are
// connected here, and keeps a remote session (a mirror) for every cluster
// whose agent is connected elsewhere.
//
// Links are authenticated both ways with HMAC(k_peer, …): k_peer is derived
// from the hub key file, which every replica mounts from one Secret. Each
// pair of replicas shares one link; when both dial at once, the link
// dialled by the lexically smaller pod name wins.
type peerNode struct {
	pod     string
	key     []byte
	service string // headless Service name; "" turns discovery off
	port    string // peer port of discovered addresses
	lookup  func(ctx context.Context, host string) ([]string, error)
	agents  *agents
	reg     *Registry
	rows    store.AgentSessions
	valid   func(identity.Principal) error
	metrics *metrics
	log     *slog.Logger
	timeout time.Duration
	base    context.Context
	started time.Time

	addrMu sync.RWMutex
	addr   string

	mu         sync.Mutex
	links      map[string]*peerLink // by peer pod
	dialing    map[string]bool      // by address
	discovered map[string]time.Time // address → first seen
	resolved   bool
	owners     map[string]string // cluster → pod of the oldest fresh remote session
	ownersAt   time.Time
	authFailAt time.Time

	nonceMu sync.Mutex
	nonces  map[string]time.Time

	kickCh chan struct{}
}

type peerNodeConfig struct {
	pod, addr, service, port string
	key                      []byte
	lookup                   func(ctx context.Context, host string) ([]string, error)
	agents                   *agents
	reg                      *Registry
	rows                     store.AgentSessions
	valid                    func(identity.Principal) error
	metrics                  *metrics
	log                      *slog.Logger
	timeout                  time.Duration
	base                     context.Context
}

func newPeerNode(c peerNodeConfig) *peerNode {
	if c.lookup == nil {
		c.lookup = net.DefaultResolver.LookupHost
	}
	n := &peerNode{
		pod: c.pod, key: c.key, service: c.service, port: c.port, lookup: c.lookup,
		agents: c.agents, reg: c.reg, rows: c.rows, valid: c.valid, metrics: c.metrics,
		log: c.log.With("component", "peers"), timeout: c.timeout, base: c.base, started: time.Now(),
		addr:  c.addr,
		links: map[string]*peerLink{}, dialing: map[string]bool{}, discovered: map[string]time.Time{},
		owners: map[string]string{}, nonces: map[string]time.Time{}, kickCh: make(chan struct{}, 1),
	}
	c.agents.forward = n.forward
	c.agents.onPrimary = n.onPrimary
	return n
}

func (n *peerNode) selfAddr() string {
	n.addrMu.RLock()
	defer n.addrMu.RUnlock()
	return n.addr
}

// setAddr sets the advertised address once the listener is bound.
func (n *peerNode) setAddr(a string) {
	n.addrMu.Lock()
	n.addr = a
	n.addrMu.Unlock()
}

// kick asks for an immediate evaluation of mirrors and links.
func (n *peerNode) kick() {
	select {
	case n.kickCh <- struct{}{}:
	default:
	}
}

// fingerprint identifies k_peer in logs without revealing it; replicas with
// different fingerprints cannot link (their key Secrets differ).
func (n *peerNode) fingerprint() string {
	return peerMAC(n.key, "fingerprint")[:12]
}

// peerMAC is HMAC-SHA256(key, "eddy-peer-v1" ‖ 0 ‖ part ‖ 0 ‖ part …) in
// unpadded base64url.
func peerMAC(key []byte, parts ...string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("eddy-peer-v1"))
	for _, p := range parts {
		m.Write([]byte{0})
		m.Write([]byte(p))
	}
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// validPodName accepts DNS-1123-ish names; ':' separates header fields.
func validPodName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' || r >= 'A' && r <= 'Z' || r == '_') {
			return false
		}
	}
	return true
}

// ---- listener --------------------------------------------------------------

// handler serves the peer listener: /peer/v1/connect only.
func (n *peerNode) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+peerPath, n.accept)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return recoverPlain(n.log, mux)
}

// verifyDial checks the Authorization header of a peer dial and returns the
// dialer's pod, address and MAC.
func (n *peerNode) verifyDial(r *http.Request, now time.Time) (pod, from, mac string, err error) {
	h := r.Header.Values("Authorization")
	if len(h) != 1 {
		return "", "", "", errors.New("missing authorization")
	}
	scheme, cred, ok := strings.Cut(h[0], " ")
	if !ok || scheme != peerAuthScheme {
		return "", "", "", errors.New("wrong authorization scheme")
	}
	parts := strings.Split(strings.TrimSpace(cred), ":")
	if len(parts) != 3 {
		return "", "", "", errors.New("malformed credentials")
	}
	pod, unix, mac := parts[0], parts[1], parts[2]
	from = r.Header.Get(peerAddrHeader)
	if !validPodName(pod) || pod == n.pod {
		return "", "", "", errors.New("invalid pod name")
	}
	if _, _, err := net.SplitHostPort(from); err != nil {
		return "", "", "", errors.New("invalid peer address")
	}
	t, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return "", "", "", errors.New("invalid timestamp")
	}
	if d := now.Sub(time.Unix(t, 0)); d > peerSkew || d < -peerSkew {
		return "", "", "", fmt.Errorf("clock skew of %s exceeds %s", d.Round(time.Second), peerSkew)
	}
	want := peerMAC(n.key, "dial", pod, unix, n.selfAddr(), from)
	if !hmac.Equal([]byte(mac), []byte(want)) {
		return "", "", "", errors.New("bad signature (do all replicas mount the same hub key?)")
	}
	if !n.useNonce(mac, now) {
		return "", "", "", errors.New("replayed credentials")
	}
	return pod, from, mac, nil
}

// useNonce records a dial MAC and reports whether it was new. MACs are
// kept for twice the allowed skew, which covers their validity.
func (n *peerNode) useNonce(mac string, now time.Time) bool {
	n.nonceMu.Lock()
	defer n.nonceMu.Unlock()
	for k, exp := range n.nonces {
		if now.After(exp) {
			delete(n.nonces, k)
		}
	}
	if _, seen := n.nonces[mac]; seen {
		return false
	}
	if len(n.nonces) >= 10000 {
		return false
	}
	n.nonces[mac] = now.Add(2 * peerSkew)
	return true
}

func (n *peerNode) accept(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	pod, from, mac, err := n.verifyDial(r, now)
	if err != nil {
		n.metrics.peerAuthFailures.Add(1)
		n.log.Warn("peer authentication failed", "peer", peerIP(r), "err", err)
		if n.isDiscoveredIP(peerIP(r)) {
			n.mu.Lock()
			n.authFailAt = now
			n.mu.Unlock()
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	unix := strconv.FormatInt(now.Unix(), 10)
	w.Header().Set(peerReplyHeader, n.pod+":"+unix+":"+peerMAC(n.key, "accept", n.pod, unix, pod, mac))
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		n.log.Warn("peer websocket upgrade failed", "peer", pod, "err", err)
		return
	}
	conn.SetReadLimit(peerReadLimit)
	l := n.newLink(pod, from, pod, conn)
	if !n.addLink(l) {
		return
	}
	l.run()
}

// ---- dialing ---------------------------------------------------------------

// dial opens a link to the replica at addr.
func (n *peerNode) dial(ctx context.Context, addr string) (*peerLink, error) {
	self := n.selfAddr()
	if self == "" {
		return nil, errors.New("hub: peer address unknown")
	}
	unix := strconv.FormatInt(time.Now().Unix(), 10)
	mac := peerMAC(n.key, "dial", n.pod, unix, addr, self)
	ctx, cancel := context.WithTimeout(ctx, peerDialTimeout)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws://"+addr+peerPath, &websocket.DialOptions{
		HTTPHeader: http.Header{
			"Authorization": []string{peerAuthScheme + " " + n.pod + ":" + unix + ":" + mac},
			peerAddrHeader:  []string{self},
		},
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			n.mu.Lock()
			n.authFailAt = time.Now()
			n.mu.Unlock()
			return nil, fmt.Errorf("hub: peer %s refused our credentials (do all replicas mount the same hub key?)", addr)
		}
		return nil, fmt.Errorf("hub: dial peer %s: %w", addr, err)
	}
	pod, err := n.verifyAccept(resp.Header.Get(peerReplyHeader), mac)
	if err != nil {
		conn.CloseNow()
		n.mu.Lock()
		n.authFailAt = time.Now()
		n.mu.Unlock()
		return nil, fmt.Errorf("hub: peer %s: %w", addr, err)
	}
	conn.SetReadLimit(peerReadLimit)
	l := n.newLink(pod, addr, n.pod, conn)
	if !n.addLink(l) {
		return nil, fmt.Errorf("hub: peer %s: already linked", pod)
	}
	go l.run()
	return l, nil
}

// verifyAccept checks the accepting peer's reply header, which binds its
// pod name to our dial MAC.
func (n *peerNode) verifyAccept(h, dialMAC string) (string, error) {
	parts := strings.Split(h, ":")
	if len(parts) != 3 || !validPodName(parts[0]) || parts[0] == n.pod {
		return "", errors.New("missing or malformed peer reply")
	}
	t, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errors.New("malformed peer reply")
	}
	if d := time.Since(time.Unix(t, 0)); d > peerSkew || d < -peerSkew {
		return "", fmt.Errorf("peer clock skew of %s exceeds %s", d.Round(time.Second), peerSkew)
	}
	if !hmac.Equal([]byte(parts[2]), []byte(peerMAC(n.key, "accept", parts[0], parts[1], n.pod, dialMAC))) {
		return "", errors.New("bad peer reply signature (do all replicas mount the same hub key?)")
	}
	return parts[0], nil
}

// dialAsync dials addr in the background unless a dial is under way.
func (n *peerNode) dialAsync(addr string) {
	if addr == "" || addr == n.selfAddr() {
		return
	}
	n.mu.Lock()
	if n.dialing[addr] || n.linkedAddrLocked(addr) {
		n.mu.Unlock()
		return
	}
	n.dialing[addr] = true
	n.mu.Unlock()
	go func() {
		_, err := n.dial(n.base, addr)
		n.mu.Lock()
		delete(n.dialing, addr)
		n.mu.Unlock()
		if err != nil {
			if n.base.Err() == nil {
				n.log.Debug("peer dial failed", "addr", addr, "err", err)
			}
			return
		}
		n.kick()
	}()
}

// ---- links -----------------------------------------------------------------

func (n *peerNode) linkedAddrLocked(addr string) bool {
	for _, l := range n.links {
		if l.addr == addr && !l.isClosed() {
			return true
		}
	}
	return false
}

func (n *peerNode) link(pod string) *peerLink {
	n.mu.Lock()
	defer n.mu.Unlock()
	if l := n.links[pod]; l != nil && !l.isClosed() {
		return l
	}
	return nil
}

// addLink registers l, resolving a duplicate: the link dialled by the
// lexically smaller pod name wins. It reports whether l was kept.
func (n *peerNode) addLink(l *peerLink) bool {
	n.mu.Lock()
	old := n.links[l.pod]
	if old != nil && !old.isClosed() {
		winner := min(n.pod, l.pod)
		if old.dialer == winner && l.dialer != winner {
			n.mu.Unlock()
			l.close("duplicate link")
			return false
		}
	}
	n.links[l.pod] = l
	n.metrics.peerLinks.Store(int64(len(n.links)))
	n.mu.Unlock()
	if old != nil {
		old.close("replaced by a new link")
	}
	n.log.Info("peer linked", "peer", l.pod, "addr", l.addr, "dialer", l.dialer)
	n.kick()
	return true
}

func (n *peerNode) removeLink(l *peerLink) {
	n.mu.Lock()
	if n.links[l.pod] == l {
		delete(n.links, l.pod)
	}
	n.metrics.peerLinks.Store(int64(len(n.links)))
	n.mu.Unlock()
	n.kick()
}

func (n *peerNode) allLinks() []*peerLink {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]*peerLink, 0, len(n.links))
	for _, l := range n.links {
		out = append(out, l)
	}
	return out
}

// closeAll drops every link (shutdown).
func (n *peerNode) closeAll() {
	for _, l := range n.allLinks() {
		l.close("hub shutting down")
	}
}

// peerLink is one authenticated WebSocket to a peer replica. Both sides
// may subscribe and send requests over it; a request's reply travels the
// other way, so ids never collide.
type peerLink struct {
	node   *peerNode
	pod    string // the peer's pod name
	addr   string // the peer's advertised address
	dialer string // pod name of the side that dialled
	conn   *websocket.Conn
	out    chan []byte
	ctx    context.Context
	cancel context.CancelFunc

	lastSeen atomic.Int64
	reqs     requestTable

	mu      sync.Mutex
	serving map[string]context.CancelFunc // the peer's requests we run
	subs    map[string]*peerSub           // clusters the peer subscribed to here
	mirrors map[string]*remoteSession     // clusters we subscribed to there

	closeOnce sync.Once
}

func (n *peerNode) newLink(pod, addr, dialer string, conn *websocket.Conn) *peerLink {
	ctx, cancel := context.WithCancel(n.base)
	l := &peerLink{
		node: n, pod: pod, addr: addr, dialer: dialer, conn: conn,
		out: make(chan []byte, peerOutBuffer), ctx: ctx, cancel: cancel,
		reqs:    requestTable{prefix: "p"},
		serving: map[string]context.CancelFunc{}, subs: map[string]*peerSub{}, mirrors: map[string]*remoteSession{},
	}
	l.lastSeen.Store(time.Now().UnixNano())
	return l
}

func (l *peerLink) isClosed() bool { return l.ctx.Err() != nil }

// close ends the link once: pending requests fail with ErrDisconnected,
// served requests are cancelled and mirrors end.
func (l *peerLink) close(reason string) {
	l.closeOnce.Do(func() {
		l.cancel()
		go func() {
			if err := l.conn.Close(websocket.StatusGoingAway, reason); err != nil {
				l.conn.CloseNow()
			}
		}()
		l.mu.Lock()
		serving := l.serving
		l.serving = map[string]context.CancelFunc{}
		l.subs = map[string]*peerSub{}
		mirrors := l.mirrors
		l.mirrors = map[string]*remoteSession{}
		l.mu.Unlock()
		for _, c := range serving {
			c()
		}
		for c, rs := range mirrors {
			l.node.agents.setRemote(c, nil, rs)
			rs.end()
		}
		l.node.removeLink(l)
		l.node.log.Info("peer link closed", "peer", l.pod, "reason", reason)
	})
}

// run serves the link until it fails.
func (l *peerLink) run() {
	go l.writeLoop()
	go l.pingLoop()
	err := l.readLoop()
	l.close(closeReason(err))
}

func (l *peerLink) writeLoop() {
	for {
		select {
		case <-l.ctx.Done():
			return
		case b := <-l.out:
			wctx, cancel := context.WithTimeout(l.ctx, frameWriteTimeout)
			err := l.conn.Write(wctx, websocket.MessageText, b)
			cancel()
			if err != nil {
				l.close("write failed")
				return
			}
		}
	}
}

func (l *peerLink) pingLoop() {
	t := time.NewTicker(peerPingEvery)
	defer t.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
		}
		if time.Since(time.Unix(0, l.lastSeen.Load())) > peerIdleTimeout {
			l.close("peer idle")
			return
		}
		_ = l.enqueue(protocol.PeerFrame{Frame: protocol.Frame{Type: protocol.TypePing}})
	}
}

func (l *peerLink) readLoop() error {
	for {
		typ, data, err := l.conn.Read(l.ctx)
		if err != nil {
			return err
		}
		l.lastSeen.Store(time.Now().UnixNano())
		if typ != websocket.MessageText {
			continue
		}
		var pf protocol.PeerFrame
		if err := json.Unmarshal(data, &pf); err != nil {
			l.node.log.Warn("invalid peer frame", "peer", l.pod, "err", err)
			continue
		}
		l.handle(pf)
	}
}

func encodePeer(pf protocol.PeerFrame) ([]byte, error) {
	b, err := json.Marshal(pf)
	if err != nil {
		return nil, fmt.Errorf("hub: encode peer frame: %w", err)
	}
	if len(b) > peerReadLimit {
		return nil, fmt.Errorf("hub: peer frame of %d bytes exceeds the limit", len(b))
	}
	return b, nil
}

// enqueue queues a frame without blocking. A full queue means the peer is
// too slow: the link is dropped and rebuilt from scratch.
func (l *peerLink) enqueue(pf protocol.PeerFrame) error {
	b, err := encodePeer(pf)
	if err != nil {
		return err
	}
	if l.isClosed() {
		return fmt.Errorf("%w: peer link to %s closed", fleet.ErrDisconnected, l.pod)
	}
	select {
	case l.out <- b:
		return nil
	default:
		// Callers may hold the agents lock (event forwarding), and close
		// takes it: close in the background.
		go l.close("peer too slow")
		return fmt.Errorf("%w: peer link to %s overflowed", fleet.ErrDisconnected, l.pod)
	}
}

// sendWait queues a frame, waiting for room (log streams).
func (l *peerLink) sendWait(ctx context.Context, pf protocol.PeerFrame) error {
	b, err := encodePeer(pf)
	if err != nil {
		return err
	}
	t := time.NewTimer(frameWriteTimeout)
	defer t.Stop()
	select {
	case l.out <- b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ctx.Done():
		return fmt.Errorf("%w: peer link to %s closed", fleet.ErrDisconnected, l.pod)
	case <-t.C:
		return fmt.Errorf("%w: peer %s is not reading", errUnavailable, l.pod)
	}
}

func payload(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

func (l *peerLink) handle(pf protocol.PeerFrame) {
	f, c := pf.Frame, pf.Cluster
	switch f.Type {
	case protocol.TypeSubscribe:
		l.node.serveSubscribe(l, c)
	case protocol.TypeUnsubscribe:
		if f.ID == protocol.PeerEnded {
			if rs := l.mirror(c); rs != nil {
				l.node.agents.setRemote(c, nil, rs)
				rs.end()
				l.node.kick()
			}
			return
		}
		l.mu.Lock()
		delete(l.subs, c)
		l.mu.Unlock()
	case protocol.TypeHello, protocol.TypeSnapshot, protocol.TypeDelta:
		rs := l.mirror(c)
		if rs == nil {
			return
		}
		if err := rs.handle(f); err != nil {
			l.node.log.Warn("invalid relayed frame", "peer", l.pod, "cluster", c, "type", string(f.Type), "err", err)
		}
	case protocol.TypeRequest:
		l.mu.Lock()
		_, dup := l.serving[f.ID]
		l.mu.Unlock()
		if f.ID == "" || dup {
			return
		}
		ctx, cancel := context.WithCancel(l.ctx)
		l.mu.Lock()
		l.serving[f.ID] = cancel
		l.mu.Unlock()
		go func() {
			defer func() {
				l.mu.Lock()
				delete(l.serving, f.ID)
				l.mu.Unlock()
				cancel()
			}()
			l.node.serveRequest(ctx, l, c, f)
		}()
	case protocol.TypeCancel:
		l.mu.Lock()
		cancel := l.serving[f.ID]
		l.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	case protocol.TypeResponse, protocol.TypeStreamEnd:
		l.reqs.finish(f.ID, decodeResponse(f.Payload, "peer"))
	case protocol.TypeStream:
		var chunk protocol.LogChunk
		if err := json.Unmarshal(f.Payload, &chunk); err != nil {
			return
		}
		if !l.reqs.chunk(f.ID, chunk) {
			l.reqs.finish(f.ID, protocol.Response{Error: &protocol.Error{Code: 503, Message: "log stream consumer too slow"}})
			_ = l.enqueue(protocol.PeerFrame{Cluster: c, Frame: protocol.Frame{Type: protocol.TypeCancel, ID: f.ID}})
		}
	case protocol.TypePing:
	default:
		// Peers of a newer minor version may send frame types we do not
		// know; the protocol only grows additively.
	}
}

// subscribed reports whether sub is still the peer's subscription to its
// cluster. Called with sub.mu held (the lock order is sub.mu, then l.mu).
func (l *peerLink) subscribed(sub *peerSub) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.subs[sub.cluster] == sub
}

func (l *peerLink) mirror(cluster string) *remoteSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.mirrors[cluster]
}

func (l *peerLink) addMirror(cluster string, rs *remoteSession) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.isClosed() {
		return false
	}
	l.mirrors[cluster] = rs
	return true
}

func (l *peerLink) removeMirror(cluster string, rs *remoteSession) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mirrors[cluster] == rs {
		delete(l.mirrors, cluster)
	}
}

// ---- requests (subscriber side) --------------------------------------------

// do sends a request to the peer and waits for its response.
func (l *peerLink) do(ctx context.Context, cluster string, req protocol.Request) (json.RawMessage, error) {
	id, p := l.reqs.register(false)
	defer l.reqs.unregister(id)
	if err := l.enqueue(protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeRequest, ID: id, Payload: payload(req)}}); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", fleet.ErrDisconnected, cluster, err)
	}
	select {
	case resp := <-p.final:
		if resp.Error != nil {
			return nil, fromRelayError(cluster, resp.Error)
		}
		return resp.Result, nil
	case <-ctx.Done():
		l.sendCancel(cluster, id)
		return nil, requestCtxError(ctx, req.Op, cluster)
	case <-l.ctx.Done():
		return nil, fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster)
	}
}

// stream relays a streaming request, calling onChunk for every chunk.
func (l *peerLink) stream(ctx context.Context, cluster string, req protocol.Request, onChunk func(protocol.LogChunk) error) error {
	id, p := l.reqs.register(true)
	defer l.reqs.unregister(id)
	if err := l.enqueue(protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeRequest, ID: id, Payload: payload(req)}}); err != nil {
		return fmt.Errorf("%w: %s: %w", fleet.ErrDisconnected, cluster, err)
	}
	for {
		select {
		case c := <-p.chunks:
			if err := onChunk(c); err != nil {
				l.sendCancel(cluster, id)
				return err
			}
		case resp := <-p.final:
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
			return fromRelayError(cluster, resp.Error)
		case <-ctx.Done():
			l.sendCancel(cluster, id)
			return requestCtxError(ctx, req.Op, cluster)
		case <-l.ctx.Done():
			return fmt.Errorf("%w: %s", fleet.ErrDisconnected, cluster)
		}
	}
}

func (l *peerLink) sendCancel(cluster, id string) {
	_ = l.enqueue(protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeCancel, ID: id}})
}

// ---- requests (owner side) -------------------------------------------------

// serveRequest runs a relayed request on the local agent session. The
// identity is validated again here (no system: user or group, no denied
// user), as the agent then does once more.
func (n *peerNode) serveRequest(ctx context.Context, l *peerLink, cluster string, f protocol.Frame) {
	var req protocol.Request
	reply := func(typ protocol.FrameType, resp protocol.Response) {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), frameWriteTimeout)
		defer cancel()
		if err := l.sendWait(wctx, protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: typ, ID: f.ID, Payload: payload(resp)}}); err != nil {
			n.log.Debug("peer reply failed", "peer", l.pod, "err", err)
		}
	}
	final := protocol.TypeResponse
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		reply(final, protocol.Response{Error: &protocol.Error{Code: 400, Message: "invalid relayed request"}})
		return
	}
	if req.Op == protocol.OpLogs {
		final = protocol.TypeStreamEnd
	}
	if err := n.valid(identity.Principal{User: req.Identity.User, Groups: req.Identity.Groups}); err != nil {
		n.log.Warn("relayed request refused: invalid identity", "peer", l.pod, "cluster", cluster, "user", truncate(req.Identity.User, 64))
		reply(final, protocol.Response{Error: &protocol.Error{Code: 403, Message: "identity refused"}})
		return
	}
	s := n.agents.localPrimary(cluster)
	if s == nil {
		reply(final, protocol.Response{Error: toRelayError(fleet.ErrDisconnected)})
		return
	}
	n.metrics.peerRelayed.Add(1)
	if req.Op == protocol.OpLogs {
		err := s.stream(ctx, req, func(c protocol.LogChunk) error {
			return l.sendWait(ctx, protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeStream, ID: f.ID, Payload: payload(c)}})
		})
		if ctx.Err() != nil {
			return // cancelled by the subscriber, or the link is gone
		}
		reply(final, protocol.Response{Error: toRelayError(err)})
		return
	}
	raw, err := s.do(ctx, req)
	if ctx.Err() != nil && err != nil && errors.Is(err, context.Canceled) {
		return
	}
	reply(final, protocol.Response{Result: raw, Error: toRelayError(err)})
}

// ---- subscriptions (owner side) --------------------------------------------

// peerSub is a peer's subscription to a cluster whose agent is connected
// here. While a snapshot is being prepared, forwarded events are buffered
// and sent after it, so the subscriber never applies a delta that the
// snapshot then silently undoes.
type peerSub struct {
	link    *peerLink
	cluster string

	mu      sync.Mutex
	pending bool
	buf     []protocol.PeerFrame
	gen     uint64
}

func (n *peerNode) serveSubscribe(l *peerLink, cluster string) {
	if _, ok := n.reg.Get(cluster); !ok {
		_ = l.enqueue(protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeUnsubscribe, ID: protocol.PeerEnded}})
		return
	}
	sub := &peerSub{link: l, cluster: cluster, pending: true}
	l.mu.Lock()
	l.subs[cluster] = sub
	l.mu.Unlock()
	n.resend(sub)
}

// resend sends the current primary's Hello and a full snapshot, then
// whatever was buffered meanwhile. Without a local session the
// subscription ends. An unsynced session sends nothing yet: its snapshot
// arrives as a resync event, which calls resend again.
func (n *peerNode) resend(sub *peerSub) {
	l := sub.link
	s := n.agents.localPrimary(sub.cluster)
	if s == nil {
		l.mu.Lock()
		if l.subs[sub.cluster] == sub {
			delete(l.subs, sub.cluster)
		}
		l.mu.Unlock()
		_ = l.enqueue(protocol.PeerFrame{Cluster: sub.cluster, Frame: protocol.Frame{Type: protocol.TypeUnsubscribe, ID: protocol.PeerEnded}})
		return
	}
	sub.mu.Lock()
	sub.pending = true
	sub.buf = nil
	sub.gen++
	gen := sub.gen
	sub.mu.Unlock()
	if !s.isSynced() {
		return
	}
	rs, _ := s.view()
	chunks := splitResources(rs, peerSnapshotBudget)
	frames := []protocol.PeerFrame{
		{Cluster: sub.cluster, Frame: protocol.Frame{Type: protocol.TypeHello, Payload: payload(s.hello())}},
	}
	snap := protocol.Snapshot{Resources: []model.Resource{}, Findings: &protocol.FindingSet{Items: s.findingList()}, Parts: max(1, len(chunks))}
	if len(chunks) > 0 {
		snap.Resources = chunks[0]
	}
	frames = append(frames, protocol.PeerFrame{Cluster: sub.cluster, Frame: protocol.Frame{Type: protocol.TypeSnapshot, Payload: payload(snap)}})
	for i, c := range chunks[min(1, len(chunks)):] {
		frames = append(frames, protocol.PeerFrame{Cluster: sub.cluster, Frame: protocol.Frame{Type: protocol.TypeDelta, Payload: payload(protocol.Delta{Upserts: c, Part: i + 2})}})
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.gen != gen {
		return // a newer resend took over
	}
	if !l.subscribed(sub) {
		// The peer unsubscribed or subscribed again since this resend
		// started (resends run in the background): the view read above may
		// be older than deltas already sent to the newer subscription, and
		// applying it after them would silently undo them.
		return
	}
	for _, f := range append(frames, sub.buf...) {
		if l.enqueue(f) != nil {
			return
		}
	}
	sub.pending, sub.buf = false, nil
}

// forward sends a local primary's event to the peers subscribed to its
// cluster. It runs under the agents lock, so it only queues.
func (n *peerNode) forward(cluster string, e event) {
	var subs []*peerSub
	for _, l := range n.allLinks() {
		l.mu.Lock()
		if s := l.subs[cluster]; s != nil {
			subs = append(subs, s)
		}
		l.mu.Unlock()
	}
	for _, sub := range subs {
		switch e.kind {
		case evResync:
			go n.resend(sub)
		case evChange:
			for _, f := range deltaFrames(cluster, e) {
				sub.send(f)
			}
		case evFindings:
			sub.send(protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeDelta,
				Payload: payload(protocol.Delta{Findings: &protocol.FindingSet{Items: e.findings}})}})
		}
	}
}

func (sub *peerSub) send(f protocol.PeerFrame) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.pending {
		if len(sub.buf) < peerOutBuffer {
			sub.buf = append(sub.buf, f)
		} else {
			go sub.link.close("peer subscription backlog overflowed")
		}
		return
	}
	_ = sub.link.enqueue(f)
}

// deltaFrames splits a change event into delta frames under the frame limit.
func deltaFrames(cluster string, e event) []protocol.PeerFrame {
	var out []protocol.PeerFrame
	for _, c := range splitResources(e.upserts, peerSnapshotBudget) {
		out = append(out, protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeDelta, Payload: payload(protocol.Delta{Upserts: c})}})
	}
	if len(e.deletes) > 0 {
		out = append(out, protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeDelta, Payload: payload(protocol.Delta{Deletes: e.deletes})}})
	}
	return out
}

// onPrimary runs after the primary of cluster changed here: subscribers
// get the new primary's view (or learn it is gone) and mirrors are
// re-evaluated.
func (n *peerNode) onPrimary(cluster string) {
	for _, l := range n.allLinks() {
		l.mu.Lock()
		sub := l.subs[cluster]
		l.mu.Unlock()
		if sub != nil {
			go n.resend(sub)
		}
	}
	n.kick()
}

// ---- mirrors and discovery -------------------------------------------------

// run keeps links and mirrors in shape until ctx is done: every
// peerEvaluateEvery, and whenever something changed (kick).
func (n *peerNode) run(ctx context.Context) {
	t := time.NewTicker(peerEvaluateEvery)
	defer t.Stop()
	for {
		n.discover(ctx)
		n.evaluate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.kickCh:
		}
	}
}

// discover resolves the peer Service and dials peers without a link. The
// lexically smaller address dials; the other side dials too once a peer
// has stayed unlinked for half of peerLinkGrace.
func (n *peerNode) discover(ctx context.Context) {
	if n.service == "" {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	hosts, err := n.lookup(lctx, n.service)
	cancel()
	if err != nil {
		n.log.Debug("peer discovery failed", "service", n.service, "err", err)
		return
	}
	self := n.selfAddr()
	selfHost, _, _ := net.SplitHostPort(self)
	now := time.Now()
	seen := map[string]bool{}
	n.mu.Lock()
	n.resolved = true
	var dial []string
	for _, h := range hosts {
		if h == selfHost {
			continue
		}
		addr := net.JoinHostPort(h, n.port)
		seen[addr] = true
		first, ok := n.discovered[addr]
		if !ok {
			first = now
			n.discovered[addr] = now
		}
		if !n.linkedAddrLocked(addr) && (self < addr || now.Sub(first) > peerLinkGrace/2) {
			dial = append(dial, addr)
		}
	}
	for addr := range n.discovered {
		if !seen[addr] {
			delete(n.discovered, addr)
		}
	}
	n.mu.Unlock()
	for _, a := range dial {
		n.dialAsync(a)
	}
}

// evaluate reads agent_sessions and makes the mirrors match: a cluster with
// a local agent session needs none; otherwise it mirrors the replica that
// holds the oldest fresh session, keeping a working mirror as long as its
// owner still holds a fresh session.
func (n *peerNode) evaluate(ctx context.Context) {
	lctx, cancel := context.WithTimeout(ctx, storeTimeout)
	rows, err := n.rows.List(lctx, "", time.Now().Add(-agentFreshFor))
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			n.log.Warn("reading agent sessions failed; keeping current mirrors", "err", err)
		}
		return
	}
	owners := map[string][]store.AgentSession{}
	for _, r := range rows {
		if r.HubPod != n.pod && r.HubAddr != "" {
			owners[r.Cluster] = append(owners[r.Cluster], r)
		}
	}
	oldest := map[string]string{}
	for c, rs := range owners {
		oldest[c] = rs[0].HubPod
	}
	n.mu.Lock()
	n.owners, n.ownersAt = oldest, time.Now()
	n.mu.Unlock()

	for _, spec := range n.reg.List() {
		c := spec.Name
		cur := n.agents.remoteOf(c)
		if n.agents.hasLocal(c) {
			if cur != nil && n.agents.session(c) != clusterSession(cur) {
				n.agents.setRemote(c, nil, cur)
			}
			continue
		}
		rs := owners[c]
		if cur != nil && !cur.closed() && slices.ContainsFunc(rs, func(r store.AgentSession) bool { return r.HubPod == cur.owner }) {
			continue
		}
		if cur != nil {
			n.agents.setRemote(c, nil, cur)
		}
		if len(rs) == 0 {
			continue
		}
		n.subscribe(c, rs[0])
	}
}

// subscribe mirrors cluster from the replica in row, dialling it first if
// there is no link yet (the next evaluation then subscribes).
func (n *peerNode) subscribe(cluster string, row store.AgentSession) {
	l := n.link(row.HubPod)
	if l == nil {
		n.dialAsync(row.HubAddr)
		return
	}
	rs := newRemoteSession(cluster, l, n.agents, n.timeout)
	if !l.addMirror(cluster, rs) {
		return
	}
	n.agents.setRemote(cluster, rs, nil)
	if err := l.enqueue(protocol.PeerFrame{Cluster: cluster, Frame: protocol.Frame{Type: protocol.TypeSubscribe}}); err != nil {
		n.agents.setRemote(cluster, nil, rs)
		rs.end()
	}
}

// remoteOwner reports whether another replica holds a fresh agent session
// for cluster, as of the last evaluation; ok is false when that
// evaluation is too old to tell.
func (n *peerNode) remoteOwner(cluster string) (owned, ok bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if time.Since(n.ownersAt) > 3*peerEvaluateEvery {
		return false, false
	}
	_, owned = n.owners[cluster]
	return owned, true
}

func (n *peerNode) isDiscoveredIP(ip string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for addr := range n.discovered {
		if h, _, _ := net.SplitHostPort(addr); h == ip {
			return true
		}
	}
	return false
}

// ready reports why this replica should not receive traffic yet, or nil:
// a discovered peer without a link, a recent peer authentication failure
// (the replicas' hub keys differ), or a mirror whose owner is reachable but
// that has not synced.
func (n *peerNode) ready(now time.Time) error {
	n.mu.Lock()
	if n.service != "" && !n.resolved && now.Sub(n.started) < peerLinkGrace {
		n.mu.Unlock()
		return errors.New("peer discovery has not run yet")
	}
	if !n.authFailAt.IsZero() && now.Sub(n.authFailAt) < peerLinkGrace {
		n.mu.Unlock()
		return errors.New("peer authentication failed recently (do all replicas mount the same hub key?)")
	}
	var missing []string
	for addr, first := range n.discovered {
		if !n.linkedAddrLocked(addr) && now.Sub(first) > peerLinkGrace {
			missing = append(missing, addr)
		}
	}
	links := make([]*peerLink, 0, len(n.links))
	for _, l := range n.links {
		links = append(links, l)
	}
	n.mu.Unlock()
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("no link to peer %s", strings.Join(missing, ", "))
	}
	for _, l := range links {
		l.mu.Lock()
		var stale []string
		for c, rs := range l.mirrors {
			if !rs.isSynced() && now.Sub(rs.created) > peerLinkGrace {
				stale = append(stale, c)
			}
		}
		l.mu.Unlock()
		if len(stale) > 0 {
			return fmt.Errorf("mirror of %s from %s has not synced", strings.Join(stale, ", "), l.pod)
		}
	}
	return nil
}

// peers lists the linked replicas, for logs and tests.
func (n *peerNode) peers() []string {
	var out []string
	for _, l := range n.allLinks() {
		if !l.isClosed() {
			out = append(out, l.pod)
		}
	}
	slices.SortFunc(out, cmp.Compare[string])
	return out
}
