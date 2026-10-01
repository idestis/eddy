package hub

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/idestis/eddy/internal/ai"
	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/mcp"
	"github.com/idestis/eddy/internal/protocol"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/storeopen"
	"github.com/idestis/eddy/internal/threads"
	"github.com/idestis/eddy/internal/ui"
	"github.com/idestis/eddy/internal/version"
)

// Server timeouts.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = time.Minute
	writeTimeout      = 2 * time.Minute // SSE and Ask AI lift it per response
	idleConnTimeout   = 2 * time.Minute
	shutdownTimeout   = 15 * time.Second
	flagsPollEvery    = 30 * time.Second
)

// Options customise New. The zero value builds everything from the config.
type Options struct {
	Log *slog.Logger
	// Store replaces the store opened from cfg.Store. The caller keeps
	// ownership and closes it.
	Store store.Store
	// Flags replaces the runtime flags file.
	Flags runtimeflags.Source
	// SPA replaces the embedded web UI.
	SPA http.Handler
	// AIOptions are passed to ai.New (tests inject a provider).
	AIOptions []ai.Option
	// RequestTimeout bounds non-streaming agent requests (default 15 s).
	RequestTimeout time.Duration
	// PodName and PeerAddr override peer.podName and peer.advertise, so
	// tests can run several replicas in one process.
	PodName  string
	PeerAddr string
	// PeerLookup replaces DNS resolution of peer.service (tests).
	PeerLookup func(ctx context.Context, host string) ([]string, error)
	// Kube replaces the management-cluster clients built from the
	// in-cluster config or kubeconfig (tests pass fakes). Ignored with
	// staticClusters.
	Kube *KubeClients
}

// KubeClients are the hub's clients for the management cluster.
type KubeClients struct {
	Dynamic dynamic.Interface
	Kube    kubernetes.Interface
}

// Hub is a configured hub. Create it with New, then call Run.
type Hub struct {
	cfg       *config.Hub
	log       *slog.Logger
	store     store.Store
	ownsStore bool
	auth      *auth.Service
	reg       *Registry
	agents    *agents
	fleet     *fleetService
	threads   *threads.Service
	ai        *ai.Service
	flags     runtimeflags.Source
	metrics   *metrics
	kube      *kubeSource
	status    *statusWriter
	bus       *bus
	registry  *sessionRegistry
	peers     *peerNode // nil without peer.listen
	onboard   *onboarding
	httpAPI   *api

	recentThreads recentSet

	ui      http.Handler
	agentH  http.Handler
	metricH http.Handler
	peerH   http.Handler

	base       context.Context
	cancelBase context.CancelFunc
	shutdown   chan struct{}
	stopOnce   sync.Once
}

// New wires the hub: store, auth, registry, authorizer, fleet, threads,
// Ask AI, MCP and the three HTTP handlers. It starts nothing; Run does.
func New(ctx context.Context, cfg *config.Hub, o Options) (*Hub, error) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	h := &Hub{cfg: cfg, log: log, metrics: newMetrics(), shutdown: make(chan struct{})}
	h.base, h.cancelBase = context.WithCancel(context.WithoutCancel(ctx))
	ok := false
	defer func() {
		if !ok {
			h.cancelBase()
			if h.ownsStore && h.store != nil {
				_ = h.store.Close()
			}
		}
	}()

	h.store = o.Store
	if h.store == nil {
		st, err := storeopen.Open(ctx, cfg.Store, log)
		if err != nil {
			return nil, err
		}
		h.store, h.ownsStore = st, true
	}
	rec := audit.New(h.store.Audit(), log)

	a, err := auth.New(cfg, h.store, rec, log)
	if err != nil {
		return nil, err
	}
	h.auth = a

	h.flags = o.Flags
	if h.flags == nil {
		h.flags = runtimeflags.NewFile(cfg.Runtime.FlagsFile, runtimeflags.Flags{
			AIEnabled: cfg.AI.Enabled, MCPEnabled: cfg.MCP.Enabled, MCPWrites: cfg.MCP.Writes, MCPAllowLogs: cfg.MCP.AllowLogs,
		}, log)
	}

	b := newBus()
	h.bus = b
	h.reg = NewRegistry()
	h.agents = newAgents(b, h.metrics)
	h.fleet = &fleetService{reg: h.reg, agents: h.agents, rec: rec, denyPrefixes: cfg.Auth.DenyUserPrefixes, log: log.With("component", "fleet")}
	h.fleet.authz = newAuthorizer(h.fleet.sendAccess, h.metrics)
	h.fleet.authz.rules = h.fleet.sendRules
	h.fleet.authz.staleSince = h.agents.staleSince
	h.fleet.authz.staleTTL = cfg.Auth.StaleAccessTTLOrDefault()
	h.fleet.authz.selfReview = func(cluster string) bool {
		s := h.agents.reader(cluster)
		return s != nil && s.hello().Mode == protocol.ModeLocal
	}
	pod := cmp.Or(o.PodName, cfg.Peer.PodName)
	if pod == "" {
		return nil, errors.New("hub: no replica name: set POD_NAME or peer.podName")
	}
	h.registry = &sessionRegistry{
		st: h.store.AgentSessions(), events: h.store.Events(), pod: pod, agents: h.agents, log: log.With("component", "agents"),
		addr: func() string {
			if h.peers == nil {
				return ""
			}
			return h.peers.selfAddr()
		},
	}
	if cfg.Peer.Listen != "" {
		_, port, err := net.SplitHostPort(cfg.Peer.Listen)
		if err != nil {
			return nil, fmt.Errorf("hub: peer.listen: %w", err)
		}
		h.agents.grace = failoverGrace
		h.peers = newPeerNode(peerNodeConfig{
			pod: pod, addr: cmp.Or(o.PeerAddr, cfg.Peer.Advertise), service: cfg.Peer.Service, port: port,
			key: h.auth.PeerKey(), lookup: o.PeerLookup, agents: h.agents, reg: h.reg, rows: h.store.AgentSessions(),
			valid: h.fleet.validPrincipal, metrics: h.metrics, log: log, timeout: o.RequestTimeout, base: h.base,
		})
		h.peerH = h.peers.handler()
	}
	h.reg.OnChange(func() {
		h.agents.rvSeq.Add(1) // the cluster set is part of the fleet version (ETags)
		h.agents.revalidate(h.reg, log)
		b.publish(event{kind: evClusters})
	})
	if len(cfg.StaticClusters) > 0 {
		loadStatic(h.reg, cfg.StaticClusters, log)
	} else if o.Kube != nil {
		if h.kube, err = newKubeSourceClients(h.reg, o.Kube.Dynamic, o.Kube.Kube, cfg.Namespace, log); err != nil {
			return nil, err
		}
		h.status = newStatusWriter(h.statusSnapshot(), h.kube.patchStatus, log)
		h.agents.onChange = h.status.kick
	} else {
		rc, err := kubeRestConfig()
		if err != nil {
			return nil, fmt.Errorf("hub: watching Cluster resources needs Kubernetes access (or set staticClusters): %w", err)
		}
		if h.kube, err = newKubeSource(h.reg, rc, cfg.Namespace, log); err != nil {
			return nil, err
		}
		h.status = newStatusWriter(h.statusSnapshot(), h.kube.patchStatus, log)
		h.agents.onChange = h.status.kick
	}

	h.onboard = newOnboarding(cfg, h.store, h.reg, h.kube, h.auth, rec, b, h.fleet, pod, log)
	if cfg.Onboarding.Enabled && h.kube == nil {
		log.Info("cluster onboarding is off: it needs Cluster resources, not staticClusters")
	}

	h.threads = threads.New(h.store.Threads(), h.fleet, rec, h.threadChanged)
	aiOpts := append([]ai.Option{ai.WithRateLimits(h.store.RateLimits())}, o.AIOptions...)
	if h.ai, err = ai.New(cfg.AI, h.fleet, h.store.Chats(), rec, h.flags, groupForKind, log, aiOpts...); err != nil {
		return nil, err
	}
	mcpH, err := mcp.NewHandler(mcp.Options{
		Config:           cfg.MCP,
		PublicURL:        cfg.PublicURL,
		Fleet:            h.fleet,
		Threads:          h.threads,
		Audit:            rec,
		Flags:            h.flags,
		Verify:           h.auth.VerifyPAT,
		GroupForKind:     groupForKind,
		ThreadWriteScope: identity.Scope(cfg.Auth.Tokens.ThreadWriteScope),
		Version:          version.Version,
		Log:              log,
		RateLimits:       h.store.RateLimits(),
	})
	if err != nil {
		return nil, err
	}

	spa := o.SPA
	if spa == nil {
		spa = ui.Handler()
		if !ui.Built() {
			log.Warn("the web UI is not built into this binary; run `task ui` (the API still works)")
		}
	}
	ap := &api{
		cfg: cfg, log: log.With("component", "api"), auth: h.auth, fleet: h.fleet, reg: h.reg, threads: h.threads,
		ai: h.ai, store: h.store, flags: h.flags, bus: b, metrics: h.metrics,
		streams: newConcurrencyLimiter(maxStreamsPerUser), logStreams: newConcurrencyLimiter(maxLogStreamsUser),
		searches:  newConcurrencyLimiter(searchPerUser),
		shutdown:  h.shutdown,
		ephemeral: cfg.EphemeralStore(),
		onboard:   h.onboard,
	}
	h.httpAPI = ap
	h.ui = ap.routes(mcpH, spa)
	h.agentH = (&agentServer{
		reg: h.reg, agents: h.agents, failures: newWindowLimiter(agentAuthFailures, agentAuthWindow),
		metrics: h.metrics, log: log.With("component", "agents"), base: h.base, timeout: o.RequestTimeout,
		registry: h.registry, onboarding: h.onboard,
	}).handler()
	h.metricH = h.metricsHandler()
	ok = true
	return h, nil
}

func groupForKind(kind string) (string, bool) {
	k, ok := flux.KindByName(kind)
	return k.Group, ok
}

// statusSnapshot returns the desired Cluster status of every registered
// cluster. A disconnected cluster keeps its last known versions and counts.
// A replica writes Connected only for clusters whose agent is connected to
// it, and leaves clusters served by another replica to that replica, so
// replicas do not overwrite each other.
func (h *Hub) statusSnapshot() func() map[string]clusterStatus {
	last := map[string]clusterStatus{} // only used from the status writer's goroutine
	return func() map[string]clusterStatus {
		out := map[string]clusterStatus{}
		for _, spec := range h.reg.List() {
			if s := h.agents.localPrimary(spec.Name); s != nil {
				st := clusterStatus{
					Phase:             "Connected",
					LastSeen:          s.lastSeenAt().Truncate(statusMinInterval),
					AgentVersion:      s.info.AgentVersion,
					KubernetesVersion: s.info.KubernetesVersion,
					FluxVersion:       s.info.FluxVersion,
					Resources:         s.size(),
				}
				last[spec.Name] = st
				out[spec.Name] = st
				continue
			}
			if h.agents.session(spec.Name) != nil {
				continue // relayed: the owning replica writes the status
			}
			if h.peers != nil {
				if owned, known := h.peers.remoteOwner(spec.Name); owned || !known {
					continue
				}
			}
			st := last[spec.Name]
			st.Phase = "Disconnected"
			if m, _ := h.reg.meta(spec.Name); m.phase == phasePending && st.LastSeen.IsZero() {
				st.Phase = phasePending // added in the UI and never connected
			}
			out[spec.Name] = st
		}
		return out
	}
}

// UIHandler serves the SPA, /api, /auth, /mcp and /healthz.
func (h *Hub) UIHandler() http.Handler { return h.ui }

// AgentHandler serves /agent/v1/connect and /healthz.
func (h *Hub) AgentHandler() http.Handler { return h.agentH }

// MetricsHandler serves /metrics, /readyz and /healthz.
func (h *Hub) MetricsHandler() http.Handler { return h.metricH }

// PeerHandler serves /peer/v1/connect, or is nil without peer.listen.
func (h *Hub) PeerHandler() http.Handler { return h.peerH }

// SetPeerAddr sets the address other replicas dial, when it is only known
// once the peer listener is bound (tests, or no POD_IP). Call it before Start.
func (h *Hub) SetPeerAddr(addr string) {
	if h.peers != nil {
		h.peers.setAddr(addr)
	}
}

// Fleet is the hub's fleet.Service.
func (h *Hub) Fleet() fleet.Service { return h.fleet }

// Auth is the hub's auth service.
func (h *Hub) Auth() *auth.Service { return h.auth }

func (h *Hub) metricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", h.metrics.handler())
	mux.HandleFunc("GET /healthz", healthz)
	// /readyz gates only on what this pod needs to serve its own traffic: the
	// store was migrated at startup (New fails otherwise) and the cluster
	// registry has synced. Peer-link or store trouble is reported as
	// "degraded" but never marks the pod unready: every replica would see the
	// same fault, and pulling all of them out of the Service would turn a
	// partial outage (cluster reads still work from memory) into a full one.
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !h.reg.Synced() {
			http.Error(w, "cluster registry not synced", http.StatusServiceUnavailable)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		var degraded []string
		if err := h.store.Ping(ctx); err != nil {
			degraded = append(degraded, "store unavailable")
		}
		if h.peers != nil {
			if err := h.peers.ready(time.Now()); err != nil {
				degraded = append(degraded, err.Error())
			}
		}
		if len(degraded) > 0 {
			_, _ = w.Write([]byte("ok (degraded: " + strings.Join(degraded, "; ") + ")\n"))
			return
		}
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	return recoverPlain(h.log, mux)
}

// Start runs the background work (auth reload, runtime flags, registry,
// Cluster status, janitor) until ctx is done. Run calls it; tests that
// serve the handlers themselves call it directly.
func (h *Hub) Start(ctx context.Context) {
	// Rows left by an earlier process with this pod name are stale.
	h.registry.forgetAll(ctx)
	go h.runEvents(ctx)
	if h.peers != nil {
		if h.peers.selfAddr() == "" {
			h.log.Warn("peer address unknown: set POD_IP or peer.advertise, or other replicas cannot relay to this one")
		}
		go h.peers.run(ctx)
	}
	go h.auth.Run(ctx)
	if f, ok := h.flags.(*runtimeflags.File); ok {
		go f.Run(ctx, flagsPollEvery)
	}
	if h.kube != nil {
		go func() {
			if err := h.kube.Run(ctx); err != nil {
				h.log.Error("cluster registry stopped", "err", err)
			}
		}()
		go h.status.Run(ctx)
	}
	go runJanitor(ctx, h.store, h.cfg.Store.Retention, h.log)
}

// Run serves the UI, agent and metrics listeners until ctx is done, then
// shuts down gracefully: SSE streams end, agent sessions close and
// in-flight requests get shutdownTimeout to finish.
func (h *Hub) Run(ctx context.Context) error {
	bg, cancelBg := context.WithCancel(ctx)
	defer cancelBg()

	type server struct {
		name string
		srv  *http.Server
		ln   net.Listener
		tls  *config.TLS
	}
	errLog := slog.NewLogLogger(h.log.Handler(), slog.LevelWarn)
	servers := []server{
		{name: "ui", srv: &http.Server{Handler: h.ui, ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout,
			WriteTimeout: writeTimeout, IdleTimeout: idleConnTimeout, MaxHeaderBytes: 64 << 10, ErrorLog: errLog}},
		// WebSocket sessions are long-lived: only the header read is bounded.
		{name: "agents", srv: &http.Server{Handler: h.agentH, ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout: idleConnTimeout, MaxHeaderBytes: 16 << 10, ErrorLog: errLog}, tls: h.cfg.Listen.AgentTLS},
		{name: "metrics", srv: &http.Server{Handler: h.metricH, ReadHeaderTimeout: readHeaderTimeout,
			ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, ErrorLog: errLog}},
	}
	addrs := []string{h.cfg.Listen.UI, h.cfg.Listen.Agents, h.cfg.Listen.Metrics}
	if h.peers != nil {
		// Peer links are long-lived WebSockets: only the header read is bounded.
		servers = append(servers, server{name: "peers", srv: &http.Server{Handler: h.peerH, ReadHeaderTimeout: readHeaderTimeout,
			IdleTimeout: idleConnTimeout, MaxHeaderBytes: 16 << 10, ErrorLog: errLog}})
		addrs = append(addrs, h.cfg.Peer.Listen)
	}
	for i := range servers {
		ln, err := net.Listen("tcp", addrs[i])
		if err != nil {
			for _, s := range servers[:i] {
				_ = s.ln.Close()
			}
			return fmt.Errorf("hub: listen %s on %s: %w", servers[i].name, addrs[i], err)
		}
		servers[i].ln = ln
	}
	if h.peers != nil && h.peers.selfAddr() == "" {
		if ap, err := netip.ParseAddrPort(servers[len(servers)-1].ln.Addr().String()); err == nil && !ap.Addr().IsUnspecified() {
			h.peers.setAddr(ap.String())
		}
	}
	h.logSummary()
	h.Start(bg)

	errc := make(chan error, len(servers))
	for _, s := range servers {
		go func() {
			var err error
			if s.tls != nil {
				s.srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
				err = s.srv.ServeTLS(s.ln, s.tls.CertFile, s.tls.KeyFile)
			} else {
				err = s.srv.Serve(s.ln)
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("hub: %s listener: %w", s.name, err)
				return
			}
			errc <- nil
		}()
		h.log.Info("listening", "listener", s.name, "addr", s.ln.Addr().String(), "tls", s.tls != nil)
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errc:
	}
	h.log.Info("shutting down")
	h.stop()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, s := range servers {
		if err := s.srv.Shutdown(sctx); err != nil {
			h.log.Warn("listener did not shut down cleanly", "listener", s.name, "err", err)
			_ = s.srv.Close()
		}
	}
	cancelBg()
	return errors.Join(runErr, h.Close())
}

// stop ends SSE streams and agent sessions.
func (h *Hub) stop() {
	h.stopOnce.Do(func() {
		close(h.shutdown)
		h.agents.stopGrace()
		h.agents.closeAll("hub shutting down")
		if h.peers != nil {
			h.peers.closeAll()
		}
		// Peers stop relaying to this replica at once instead of waiting
		// for the rows to go stale.
		h.registry.forgetAll(context.Background())
	})
}

// Close releases the hub's resources. Run calls it on the way out.
func (h *Hub) Close() error {
	h.stop()
	h.cancelBase()
	if h.ownsStore {
		h.ownsStore = false
		if err := h.store.Close(); err != nil {
			return fmt.Errorf("hub: close store: %w", err)
		}
	}
	return nil
}

// logSummary logs the effective configuration without secrets.
func (h *Hub) logSummary() {
	c := h.cfg
	mode := "kubernetes"
	if len(c.StaticClusters) > 0 {
		mode = "static"
	}
	h.log.Info("eddy hub starting",
		"version", version.Version,
		"publicURL", c.PublicURL,
		"listen", map[string]string{"ui": c.Listen.UI, "agents": c.Listen.Agents, "metrics": c.Listen.Metrics},
		"agentTLS", c.Listen.AgentTLS != nil,
		"namespace", c.Namespace,
		"clusters", mode,
		"store", map[string]any{"driver": c.Store.Driver, "ephemeral": c.EphemeralStore()},
		"peers", h.peerSummary(),
		"auth", map[string]any{"local": c.Auth.Local.Enabled, "localMode": c.Auth.Local.Mode, "proxy": c.Auth.Proxy.Enabled,
			"github": c.Auth.GitHub.Enabled, "oidc": len(c.Auth.OIDC), "dev": h.auth.DevMode()},
		"ai", map[string]any{"enabled": c.AI.Enabled, "provider": c.AI.Provider},
		"mcp", map[string]bool{"enabled": c.MCP.Enabled, "writes": c.MCP.Writes, "allowLogs": c.MCP.AllowLogs},
	)
	if h.auth.DevMode() {
		h.log.Warn("DEV MODE: fake login is enabled; never expose this hub")
	}
	if c.EphemeralStore() {
		if h.auth.DevMode() {
			h.log.Warn("EPHEMERAL STORE (memory): sessions, tokens, threads and stored audit are lost on restart")
		} else {
			h.log.Warn("EPHEMERAL STORE (memory) OUTSIDE DEV MODE: sessions, tokens, threads and stored audit are lost on every restart and not shared between replicas. Use store.driver=postgres for anything but an evaluation.")
		}
	}
	if c.Listen.AgentTLS == nil {
		h.log.Warn("agent listener serves plain HTTP; terminate TLS in front of it (agents require wss:// outside development)")
	}
}

func (h *Hub) peerSummary() map[string]any {
	if h.peers == nil {
		return map[string]any{"enabled": false}
	}
	return map[string]any{
		"enabled": true, "listen": h.cfg.Peer.Listen, "service": h.cfg.Peer.Service,
		"pod": h.peers.pod, "advertise": h.peers.selfAddr(), "keyFingerprint": h.peers.fingerprint(),
	}
}
