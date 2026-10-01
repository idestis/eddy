package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/store"
)

// Agent endpoint limits.
const (
	helloTimeout        = 10 * time.Second
	agentAuthFailures   = 10
	agentAuthWindow     = time.Minute
	maxHelloFieldLength = 128
)

// Agent session registry timings (ADR-0004).
const (
	// agentHeartbeatEvery refreshes this replica's agent_sessions rows.
	agentHeartbeatEvery = 10 * time.Second
	// agentFreshFor is how old a heartbeat may be for its row to count.
	agentFreshFor = 30 * time.Second
	// failoverGrace keeps serving a cluster's last view (requests fail with
	// ErrDisconnected) while a replacement session is found, so a primary
	// that moves to another replica does not blink the cluster to
	// Disconnected. Used only when peers are enabled.
	failoverGrace = 5 * time.Second
	// legacyInstance stands in for agents that send no Hello.Instance.
	// Their Seq is the connect time, so the newest connection wins, as it
	// did before agent replicas.
	legacyInstance = "legacy"
)

// errStaleSession refuses a connection from an agent instance that already
// has a newer one.
var errStaleSession = errors.New("a newer connection of this agent instance is active")

// agents holds every cluster session this replica knows: the local agent
// sessions (0..N per cluster, one per agent instance) and at most one
// remote session relayed from the replica that holds the cluster's agent.
// For each cluster it picks one primary session: the oldest synced local
// session, else the oldest local one, else the remote one. Only the
// primary's events reach the bus, and every request goes to it.
type agents struct {
	mu       sync.RWMutex
	local    map[string][]*agentSession // oldest first
	remote   map[string]*remoteSession
	primary  map[string]clusterSession
	graceT   map[string]*time.Timer
	graceEnd map[string]bool
	grace    time.Duration
	// stale holds the last views of disconnected clusters (stale.go).
	stale     map[string]*staleView
	staleRows int
	staleMax  int
	rvSeq     atomic.Uint64
	bus       *bus
	metrics   *metrics

	// Hooks, set before the first session is added.
	onChange  func()                        // connection state changed (Cluster status)
	onPrimary func(cluster string)          // the primary of cluster changed (peers)
	forward   func(cluster string, e event) // events of a local primary (peer subscribers)
}

func newAgents(b *bus, m *metrics) *agents {
	return &agents{
		local: map[string][]*agentSession{}, remote: map[string]*remoteSession{},
		primary: map[string]clusterSession{}, graceT: map[string]*time.Timer{}, graceEnd: map[string]bool{},
		stale: map[string]*staleView{}, staleMax: staleMaxRows, bus: b, metrics: m,
	}
}

// get returns the oldest live local session of cluster, or nil.
func (a *agents) get(cluster string) *agentSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.getLocked(cluster)
}

func (a *agents) getLocked(cluster string) *agentSession {
	for _, s := range a.local[cluster] {
		if !s.closed() {
			return s
		}
	}
	return nil
}

// session returns the primary session of cluster, or nil. During a
// failover grace period it may be a closed session: its view is still
// served and its requests fail with ErrDisconnected.
func (a *agents) session(cluster string) clusterSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.primary[cluster]
}

// localPrimary returns the local session that serves cluster: the primary
// if it is local and live, else the oldest live local session.
func (a *agents) localPrimary(cluster string) *agentSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if s, ok := a.primary[cluster].(*agentSession); ok && !s.closed() {
		return s
	}
	return a.getLocked(cluster)
}

func (a *agents) hasLocal(cluster string) bool { return a.get(cluster) != nil }

func (a *agents) remoteOf(cluster string) *remoteSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.remote[cluster]
}

// all returns every local session.
func (a *agents) all() []*agentSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var out []*agentSession
	for _, ss := range a.local {
		out = append(out, ss...)
	}
	return out
}

// add registers a local session. A session of the same agent instance
// with a lower (or equal) Seq is replaced; one with a higher Seq wins and
// add returns errStaleSession.
func (a *agents) add(s *agentSession) error {
	a.mu.Lock()
	var replaced []*agentSession
	kept := a.local[s.cluster][:0:0]
	for _, o := range a.local[s.cluster] {
		if o.instance == s.instance && !o.closed() {
			if o.seq > s.seq {
				a.mu.Unlock()
				return errStaleSession
			}
			replaced = append(replaced, o)
			continue
		}
		kept = append(kept, o)
	}
	a.local[s.cluster] = append(kept, s)
	a.countLocked()
	changed := a.reselectLocked(s.cluster)
	a.mu.Unlock()
	for _, o := range replaced {
		o.close(websocket.StatusPolicyViolation, "replaced by a new connection")
	}
	a.after(s.cluster, changed)
	return nil
}

// remove forgets a local session after it ended.
func (a *agents) remove(s *agentSession) {
	a.mu.Lock()
	ss := a.local[s.cluster]
	i := slices.Index(ss, s)
	if i < 0 {
		a.mu.Unlock()
		return
	}
	ss = slices.Delete(slices.Clone(ss), i, i+1)
	if len(ss) == 0 {
		delete(a.local, s.cluster)
	} else {
		a.local[s.cluster] = ss
	}
	a.countLocked()
	changed := a.reselectLocked(s.cluster)
	a.mu.Unlock()
	a.after(s.cluster, changed)
}

// setRemote installs (or with rs nil, removes) the relayed session of
// cluster. Removing only happens if old is still the current one.
func (a *agents) setRemote(cluster string, rs, old *remoteSession) {
	a.mu.Lock()
	cur := a.remote[cluster]
	if rs == nil {
		if cur != old || cur == nil {
			a.mu.Unlock()
			return
		}
		delete(a.remote, cluster)
	} else {
		a.remote[cluster] = rs
	}
	changed := a.reselectLocked(cluster)
	a.mu.Unlock()
	if cur != nil && cur != rs {
		cur.end()
	}
	a.after(cluster, changed)
}

// reselect re-evaluates the primary of cluster, for example after a
// remote session synced.
func (a *agents) reselect(cluster string) {
	a.mu.Lock()
	changed := a.reselectLocked(cluster)
	a.mu.Unlock()
	a.after(cluster, changed)
}

func (a *agents) countLocked() {
	n := 0
	for _, ss := range a.local {
		if len(ss) > 0 {
			n++
		}
	}
	a.metrics.agentsConnected.Store(int64(n))
}

// reselectLocked picks the primary of cluster and reports whether it
// changed. Preference: the oldest synced local session, a synced remote
// session, then the oldest unsynced local session. When the primary is
// lost and only an unsynced session (or nothing) is left, the failover
// grace period keeps the old view for a while, if one is configured.
// After it, the lost primary's view is kept as a stale view, which serves
// reads until a session syncs (an unsynced session does not replace it,
// so the cluster never blinks empty while its agent resends its snapshot).
func (a *agents) reselectLocked(cluster string) bool {
	var synced, unsynced clusterSession
	for _, s := range a.local[cluster] {
		if s.closed() {
			continue
		}
		if s.isSynced() {
			synced = s
			break
		}
		if unsynced == nil {
			unsynced = s
		}
	}
	if synced == nil {
		if r := a.remote[cluster]; r != nil && !r.closed() && r.isSynced() {
			synced = r
		}
	}
	cur, had := a.primary[cluster]
	lost := had && !a.validLocked(cluster, cur)
	inGrace := lost && a.grace > 0 && !a.graceEnd[cluster]
	want := synced
	if want == nil && !inGrace {
		if lost {
			a.keepStaleLocked(cluster, cur)
		}
		if a.stale[cluster] == nil {
			want = unsynced
		}
	}
	if want != nil {
		if t := a.graceT[cluster]; t != nil {
			t.Stop()
			delete(a.graceT, cluster)
		}
		delete(a.graceEnd, cluster)
		if want == synced {
			a.dropStaleLocked(cluster)
		}
		if had && cur == want {
			return false
		}
		a.primary[cluster] = want
		return true
	}
	if !had {
		return false
	}
	if inGrace {
		if a.graceT[cluster] == nil {
			a.graceT[cluster] = time.AfterFunc(a.grace, func() {
				a.mu.Lock()
				delete(a.graceT, cluster)
				a.graceEnd[cluster] = true
				changed := a.reselectLocked(cluster)
				a.mu.Unlock()
				a.after(cluster, changed)
			})
		}
		return false
	}
	delete(a.primary, cluster)
	delete(a.graceEnd, cluster)
	return true
}

// validLocked reports whether s is still one of cluster's live sessions.
func (a *agents) validLocked(cluster string, s clusterSession) bool {
	if s.closed() {
		return false
	}
	if r, ok := s.(*remoteSession); ok {
		return a.remote[cluster] == r
	}
	return slices.Contains(a.local[cluster], s.(*agentSession))
}

// after publishes a primary change: SSE clients refetch the cluster's
// rows, Cluster status is rewritten and peers are told.
func (a *agents) after(cluster string, changed bool) {
	a.rvSeq.Add(1) // connection state is part of the fleet version (ETags)
	if changed {
		a.bus.publish(event{kind: evResync, cluster: cluster})
		if a.onPrimary != nil {
			a.onPrimary(cluster)
		}
	}
	a.bus.publish(event{kind: evClusters})
	if a.onChange != nil {
		a.onChange()
	}
}

// emit is the event sink of every session. Only the primary's events
// reach the bus, and a local primary's events also go to peers that
// subscribed to the cluster. A standby that finishes its first snapshot
// may become the primary.
func (a *agents) emit(src clusterSession, e event) {
	cluster := src.name()
	a.mu.RLock()
	cur := a.primary[cluster]
	if cur == src {
		a.bus.publish(e)
		if _, local := src.(*agentSession); local && a.forward != nil {
			a.forward(cluster, e)
		}
	}
	a.mu.RUnlock()
	if cur != src && e.kind == evResync {
		a.reselect(cluster)
	}
}

// revalidate closes sessions whose cluster was removed or whose token is no
// longer accepted (Secret rotated or deleted), and drops relayed sessions
// of unregistered clusters.
func (a *agents) revalidate(reg *Registry, log *slog.Logger) {
	for _, s := range a.all() {
		if !reg.Accepts(s.cluster, s.tokenHash) {
			log.Warn("closing agent session: its token is no longer valid", "cluster", s.cluster)
			s.close(websocket.StatusPolicyViolation, "token no longer valid")
		}
	}
	a.mu.Lock()
	var gone []*remoteSession
	for c, r := range a.remote {
		if _, ok := reg.Get(c); !ok {
			gone = append(gone, r)
		}
	}
	for c := range a.stale {
		if _, ok := reg.Get(c); !ok {
			a.dropStaleLocked(c)
		}
	}
	a.mu.Unlock()
	for _, r := range gone {
		a.setRemote(r.cluster, nil, r)
	}
}

func (a *agents) closeAll(reason string) {
	for _, s := range a.all() {
		s.close(websocket.StatusGoingAway, reason)
	}
}

// stopGrace cancels pending failover timers (hub shutdown).
func (a *agents) stopGrace() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for c, t := range a.graceT {
		t.Stop()
		delete(a.graceT, c)
	}
}

// agentServer serves the agent listener: /agent/v1/connect and /healthz.
//
// TLS: the listener serves TLS itself when listen.agentTLS is set. Without
// it the hub accepts plain HTTP and expects TLS to terminate in front of it
// (NLB, ingress); agents refuse ws:// unless explicitly allowed for
// development, so the token is never sent in the clear by a correctly
// configured agent.
type agentServer struct {
	reg      *Registry
	agents   *agents
	failures *windowLimiter
	metrics  *metrics
	log      *slog.Logger
	base     context.Context // hub lifetime; sessions end with it
	timeout  time.Duration
	// registry records local agent sessions in agent_sessions so other
	// replicas can relay to them; nil (tests) records nothing.
	registry *sessionRegistry
	// onboarding handles join tokens and records rejected attempts; nil
	// (tests) turns both off.
	onboarding *onboarding
}

func (a *agentServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /agent/v1/connect", a.connect)
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return recoverPlain(a.log, mux)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte("ok\n"))
}

// bearerToken returns the token of an "Authorization: Bearer" header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Values("Authorization")
	if len(h) != 1 {
		return "", false
	}
	scheme, tok, ok := strings.Cut(h[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	tok = strings.TrimSpace(tok)
	return tok, tok != ""
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (a *agentServer) connect(w http.ResponseWriter, r *http.Request) {
	ip := peerIP(r)
	if a.failures.blocked(ip) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "too many failed attempts", http.StatusTooManyRequests)
		return
	}
	cluster := r.URL.Query().Get("cluster")
	tok, ok := bearerToken(r)
	if ok && auth.IsJoinToken(tok) && a.onboarding.enabled() {
		a.join(w, r, cluster, tok, ip)
		return
	}
	h, valid := a.reg.Verify(cluster, tok)
	if !ok || !valid {
		a.failures.add(ip)
		a.metrics.agentAuthFailures.Add(1)
		a.log.Warn("agent authentication failed", "cluster", truncate(cluster, 64), "peer", ip)
		detail := "the agent token is not accepted: it does not match the cluster's token Secret"
		if !ok {
			detail = "no bearer token"
		}
		a.onboarding.reject(cluster, store.AttemptBadToken, detail, ip)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// permessage-deflate with context takeover (ADR-0006), when the agent
	// offers it. The read limit applies to the decompressed message.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		a.log.Warn("agent websocket upgrade failed", "cluster", cluster, "err", err)
		return
	}
	conn.SetReadLimit(protocol.MaxFrameBytes)

	hello, err := readHello(a.base, conn, cluster)
	if err != nil {
		a.log.Warn("agent hello rejected", "cluster", cluster, "peer", ip, "err", err)
		a.onboarding.reject(cluster, helloReason(err), err.Error(), ip)
		_ = conn.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		return
	}
	s := newSession(cluster, hello, h, conn, sessionDeps{
		bus: a.agents.bus, emit: a.agents.emit, metrics: a.metrics, rvSeq: &a.agents.rvSeq, log: a.log, timeout: a.timeout,
	})
	s.instance, s.seq = hello.Instance, hello.Seq
	if s.instance == "" {
		s.instance, s.seq = legacyInstance, s.connectedAt.UnixNano()
	}
	if err := a.agents.add(s); err != nil {
		a.log.Warn("agent connection refused", "cluster", cluster, "instance", s.instance, "seq", s.seq, "err", err)
		_ = conn.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		return
	}
	if a.registry != nil {
		if err := a.registry.register(a.base, s); err != nil {
			a.log.Warn("agent connection refused: another hub replica holds a newer connection of this agent instance",
				"cluster", cluster, "instance", s.instance, "seq", s.seq)
			s.close(websocket.StatusPolicyViolation, errStaleSession.Error())
			a.agents.remove(s)
			return
		}
	}
	a.log.Info("agent connected", "cluster", cluster, "instance", s.instance, "seq", s.seq, "agentVersion", hello.AgentVersion,
		"kubernetesVersion", hello.KubernetesVersion, "fluxVersion", hello.FluxVersion, "peer", ip)
	if hello.Mode == protocol.ModeLocal {
		a.log.Warn("agent runs in local mode: it acts as a developer's kubeconfig identity, not as the Eddy user",
			"cluster", cluster, "context", hello.Context, "readOnly", hello.ReadOnly)
	}
	if a.registry != nil {
		go a.registry.heartbeat(a.base, s)
	}
	err = s.run(a.base)
	a.agents.remove(s)
	if a.registry != nil {
		a.registry.unregister(s)
	}
	a.log.Info("agent disconnected", "cluster", cluster, "instance", s.instance, "reason", closeReason(err))
}

// errProtocol marks a hello with an unsupported protocol version.
var errProtocol = errors.New("unsupported protocol version")

func helloReason(err error) string {
	if errors.Is(err, errProtocol) {
		return store.AttemptProtocol
	}
	return store.AttemptHelloRejected
}

// join serves a connection that presents a join token (ADR-0005): it
// checks the token, reads the hello, consumes the token, mints the
// permanent agent token into the cluster's token Secret, sends it in a
// credentials frame and closes. The agent reconnects with the new token.
func (a *agentServer) join(w http.ResponseWriter, r *http.Request, cluster, tok, ip string) {
	o := a.onboarding
	refuse := func(reason, detail string) {
		a.failures.add(ip)
		a.metrics.agentAuthFailures.Add(1)
		a.log.Warn("agent join refused", "cluster", truncate(cluster, 64), "peer", ip, "reason", reason)
		o.reject(cluster, reason, detail, ip)
	}
	ctx, cancel := context.WithTimeout(a.base, storeTimeout)
	jt, reason, detail := o.joinCheck(ctx, cluster, tok)
	cancel()
	if reason == store.AttemptWrongCluster {
		o.reject(jt.Cluster, reason, fmt.Sprintf("the join token was presented for cluster %q", truncate(cluster, 64)), ip)
	}
	if _, known := a.reg.Get(cluster); reason == "" && !known {
		reason, detail = store.AttemptBadToken, "unknown cluster"
	}
	if reason != "" {
		refuse(reason, detail)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		a.log.Warn("agent websocket upgrade failed", "cluster", cluster, "err", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(protocol.MaxFrameBytes)
	hello, err := readHello(a.base, conn, cluster)
	if err != nil {
		// The token is not used up by a bad hello.
		refuse(helloReason(err), err.Error())
		_ = conn.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		return
	}
	ctx, cancel = context.WithTimeout(a.base, credsTimeout)
	defer cancel()
	token, err := o.mint(ctx, cluster, jt)
	if err != nil {
		var je *joinError
		if errors.As(err, &je) {
			refuse(je.reason, je.detail)
		} else {
			a.log.Error("agent join failed", "cluster", cluster, "err", err)
			o.reject(cluster, store.AttemptCredentialsFail, "internal error while issuing the agent token", ip)
		}
		_ = conn.Close(websocket.StatusPolicyViolation, "join refused")
		return
	}
	res, err := sendCredentials(ctx, conn, token)
	switch {
	case err != nil:
		a.log.Warn("agent join: credentials not acknowledged", "cluster", cluster, "err", err)
		o.reject(cluster, store.AttemptCredentialsFail, "the agent did not acknowledge its new token: "+err.Error(), ip)
	case !res.Stored:
		a.log.Warn("agent join: the agent keeps its token in memory only", "cluster", cluster, "error", res.Error)
	}
	a.log.Info("agent joined", "cluster", cluster, "instance", hello.Instance, "agentVersion", hello.AgentVersion, "peer", ip)
	o.notify(cluster)
	_ = conn.Close(websocket.StatusNormalClosure, "credentials delivered; reconnect with them")
}

// sendCredentials sends the credentials frame and waits for the agent's
// response, ignoring any other frame.
func sendCredentials(ctx context.Context, conn *websocket.Conn, token string) (protocol.CredentialsResult, error) {
	payload, err := json.Marshal(protocol.Credentials{Token: token})
	if err != nil {
		return protocol.CredentialsResult{}, err
	}
	b, err := json.Marshal(protocol.Frame{Type: protocol.TypeCredentials, ID: "credentials-1", Payload: payload})
	if err != nil {
		return protocol.CredentialsResult{}, err
	}
	if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
		return protocol.CredentialsResult{}, fmt.Errorf("send credentials: %w", err)
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return protocol.CredentialsResult{}, fmt.Errorf("read credentials response: %w", err)
		}
		if typ != websocket.MessageText {
			continue
		}
		var f protocol.Frame
		if json.Unmarshal(data, &f) != nil || f.Type != protocol.TypeResponse || f.ID != "credentials-1" {
			continue
		}
		resp := decodeResponse(f.Payload, "agent")
		if resp.Error != nil {
			return protocol.CredentialsResult{}, errors.New(resp.Error.Message)
		}
		var res protocol.CredentialsResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			return protocol.CredentialsResult{}, fmt.Errorf("decode credentials result: %w", err)
		}
		return res, nil
	}
}

func closeReason(err error) string {
	if err == nil {
		return "closed"
	}
	if st := websocket.CloseStatus(err); st != -1 {
		return st.String()
	}
	if errors.Is(err, context.Canceled) {
		return "closed by hub"
	}
	return err.Error()
}

// readHello reads and validates the first frame of a connection.
func readHello(ctx context.Context, conn *websocket.Conn, cluster string) (protocol.Hello, error) {
	ctx, cancel := context.WithTimeout(ctx, helloTimeout)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		return protocol.Hello{}, fmt.Errorf("read hello: %w", err)
	}
	if typ != websocket.MessageText {
		return protocol.Hello{}, errors.New("hello must be a text frame")
	}
	var f protocol.Frame
	if err := json.Unmarshal(data, &f); err != nil || f.Type != protocol.TypeHello {
		return protocol.Hello{}, errors.New("first frame must be hello")
	}
	var h protocol.Hello
	if err := json.Unmarshal(f.Payload, &h); err != nil {
		return protocol.Hello{}, fmt.Errorf("decode hello: %w", err)
	}
	return h, validateHello(&h, cluster)
}

// validateHello checks the protocol major version and the cluster name, and
// bounds the free-text fields that end up in Cluster status and the UI.
func validateHello(h *protocol.Hello, cluster string) error {
	major, _, _ := strings.Cut(h.Protocol, ".")
	want, _, _ := strings.Cut(protocol.Version, ".")
	if major != want {
		return fmt.Errorf("%w %q (hub speaks %s)", errProtocol, truncate(h.Protocol, 16), protocol.Version)
	}
	if h.Cluster != cluster {
		return fmt.Errorf("hello names cluster %q but the connection is for %q", truncate(h.Cluster, 64), cluster)
	}
	switch h.Mode {
	case "", protocol.ModeLocal:
	default:
		return fmt.Errorf("unknown agent mode %q", truncate(h.Mode, 16))
	}
	for _, f := range []*string{&h.AgentVersion, &h.KubernetesVersion, &h.FluxVersion, &h.Context, &h.Instance} {
		*f = cleanText(*f, maxHelloFieldLength)
	}
	if h.Instance == legacyInstance {
		return fmt.Errorf("agent instance %q is reserved", legacyInstance)
	}
	if h.Seq < 0 {
		return errors.New("hello seq must not be negative")
	}
	if len(h.Namespaces) > 1000 {
		h.Namespaces = h.Namespaces[:1000]
	}
	h.Presets = knownPresets(h.Presets)
	h.Kinds = knownKinds(h.Kinds)
	if d := h.Diagnostics; d != nil {
		if len(d.ServedKinds) > 64 {
			d.ServedKinds = d.ServedKinds[:64]
		}
		for i := range d.ServedKinds {
			d.ServedKinds[i] = cleanText(d.ServedKinds[i], 64)
		}
		d.SARError = cleanText(d.SARError, 256)
		d.CredentialsError = cleanText(d.CredentialsError, 256)
	}
	return nil
}

// knownPresets keeps the supported preset names of a Hello.
func knownPresets(in []string) []string {
	out, _ := flux.ParsePresets(slices.DeleteFunc(slices.Clone(in), func(p string) bool {
		return !slices.Contains(flux.Presets(), p)
	}))
	return out
}

// knownKinds keeps the "<group>/<Kind>" entries of a Hello that name
// surfaced kinds of the table, without duplicates.
func knownKinds(in []string) []string {
	var out []string
	for _, s := range in {
		group, kind, ok := strings.Cut(s, "/")
		if !ok {
			continue
		}
		k, known := flux.KindByName(kind)
		if !known || !k.Surfaced || !k.Matches(group, kind) || slices.Contains(out, s) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// cleanText drops control characters and invalid UTF-8 and truncates.
func cleanText(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, s)
	return truncate(s, n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
