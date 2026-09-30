package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/protocol"
)

// Agent endpoint limits.
const (
	helloTimeout        = 10 * time.Second
	agentAuthFailures   = 10
	agentAuthWindow     = time.Minute
	maxHelloFieldLength = 128
)

// agents holds the live session of every connected cluster. A new
// connection for a cluster replaces the previous one.
type agents struct {
	mu       sync.RWMutex
	sessions map[string]*agentSession
	rvSeq    atomic.Uint64
	bus      *bus
	metrics  *metrics
	onChange func()
}

func newAgents(b *bus, m *metrics) *agents {
	return &agents{sessions: map[string]*agentSession{}, bus: b, metrics: m}
}

func (a *agents) get(cluster string) *agentSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sessions[cluster]
}

func (a *agents) all() []*agentSession {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]*agentSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		out = append(out, s)
	}
	return out
}

func (a *agents) add(s *agentSession) {
	a.mu.Lock()
	old := a.sessions[s.cluster]
	a.sessions[s.cluster] = s
	a.mu.Unlock()
	if old != nil {
		old.close(websocket.StatusPolicyViolation, "replaced by a new connection")
	} else {
		a.metrics.agentsConnected.Add(1)
	}
	a.changed()
}

func (a *agents) remove(s *agentSession) {
	a.mu.Lock()
	cur := a.sessions[s.cluster] == s
	if cur {
		delete(a.sessions, s.cluster)
	}
	a.mu.Unlock()
	if cur {
		a.metrics.agentsConnected.Add(-1)
		// Clients drop the cluster's rows: the view is gone with the session.
		a.bus.publish(event{kind: evResync, cluster: s.cluster})
		a.changed()
	}
}

func (a *agents) changed() {
	a.bus.publish(event{kind: evClusters})
	if a.onChange != nil {
		a.onChange()
	}
}

// revalidate closes sessions whose cluster was removed or whose token is no
// longer accepted (Secret rotated or deleted).
func (a *agents) revalidate(reg *Registry, log *slog.Logger) {
	for _, s := range a.all() {
		if !reg.Accepts(s.cluster, s.tokenHash) {
			log.Warn("closing agent session: its token is no longer valid", "cluster", s.cluster)
			s.close(websocket.StatusPolicyViolation, "token no longer valid")
		}
	}
}

func (a *agents) closeAll(reason string) {
	for _, s := range a.all() {
		s.close(websocket.StatusGoingAway, reason)
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
	h, valid := a.reg.Verify(cluster, tok)
	if !ok || !valid {
		a.failures.add(ip)
		a.metrics.agentAuthFailures.Add(1)
		a.log.Warn("agent authentication failed", "cluster", truncate(cluster, 64), "peer", ip)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		a.log.Warn("agent websocket upgrade failed", "cluster", cluster, "err", err)
		return
	}
	conn.SetReadLimit(protocol.MaxFrameBytes)

	hello, err := readHello(a.base, conn, cluster)
	if err != nil {
		a.log.Warn("agent hello rejected", "cluster", cluster, "peer", ip, "err", err)
		_ = conn.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
		return
	}
	s := newSession(cluster, hello, h, conn, sessionDeps{
		bus: a.agents.bus, metrics: a.metrics, rvSeq: &a.agents.rvSeq, log: a.log, timeout: a.timeout,
	})
	a.agents.add(s)
	a.log.Info("agent connected", "cluster", cluster, "agentVersion", hello.AgentVersion,
		"kubernetesVersion", hello.KubernetesVersion, "fluxVersion", hello.FluxVersion, "peer", ip)
	err = s.run(a.base)
	a.agents.remove(s)
	a.log.Info("agent disconnected", "cluster", cluster, "reason", closeReason(err))
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
		return fmt.Errorf("unsupported protocol version %q (hub speaks %s)", truncate(h.Protocol, 16), protocol.Version)
	}
	if h.Cluster != cluster {
		return fmt.Errorf("hello names cluster %q but the connection is for %q", truncate(h.Cluster, 64), cluster)
	}
	for _, f := range []*string{&h.AgentVersion, &h.KubernetesVersion, &h.FluxVersion} {
		*f = cleanText(*f, maxHelloFieldLength)
	}
	if len(h.Namespaces) > 1000 {
		h.Namespaces = h.Namespaces[:1000]
	}
	return nil
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
