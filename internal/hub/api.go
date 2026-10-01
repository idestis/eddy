package hub

import (
	"log/slog"
	"net/http"

	"github.com/idestis/eddy/internal/ai"
	"github.com/idestis/eddy/internal/auth"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/threads"
)

// api holds the UI listener's handlers.
type api struct {
	cfg        *config.Hub
	log        *slog.Logger
	auth       *auth.Service
	fleet      *fleetService
	reg        *Registry
	threads    *threads.Service
	ai         *ai.Service
	store      store.Store
	flags      runtimeflags.Source
	bus        *bus
	metrics    *metrics
	streams    *concurrencyLimiter
	logStreams *concurrencyLimiter
	searches   *concurrencyLimiter
	shutdown   <-chan struct{}
	ephemeral  bool
	onboard    *onboarding
}

// routes builds the UI listener's handler:
//
//	/api/…   Authenticate → RequireUser → RequireCSRF → API routes
//	/auth/…  Authenticate → sign-in routes (they check CSRF themselves)
//	/mcp     the MCP handler, which does its own bearer auth (no cookies)
//	/healthz liveness
//	/        the embedded SPA
//
// Everything is wrapped in the access log, panic recovery, body cap and
// security headers. GET responses under /api/v1 may be gzipped
// (compress.go lists the routes that never are).
func (a *api) routes(mcpHandler, spa http.Handler) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/me", a.handleMe)
	m.HandleFunc("GET /api/v1/prefs", a.handlePrefs)
	m.HandleFunc("PUT /api/v1/prefs", a.handlePutPrefs)
	m.HandleFunc("GET /api/v1/clusters", a.handleClusters)
	m.HandleFunc("POST /api/v1/clusters", a.handleCreateCluster)
	m.HandleFunc("GET /api/v1/clusters/permissions", a.handleClusterPermissions)
	m.HandleFunc("PATCH /api/v1/clusters/{cluster}", a.handleUpdateCluster)
	m.HandleFunc("DELETE /api/v1/clusters/{cluster}", a.handleDeleteCluster)
	m.HandleFunc("POST /api/v1/clusters/{cluster}/join-token", a.handleJoinToken)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/connection", a.handleConnection)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/resources", a.handleResources)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/objects/{kind}/{ns}/{name}", a.handleObject)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/objects/{kind}/{ns}/{name}/children", a.handleChildren)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/objects/{kind}/{ns}/{name}/yaml", a.handleYAML)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/objects/{kind}/{ns}/{name}/events", a.handleEvents)
	m.HandleFunc("POST /api/v1/clusters/{cluster}/objects/{kind}/{ns}/{name}/{action}", a.handleAction)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/pods/{ns}/{name}/logs", a.handleLogs)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/workloads/{kind}/{ns}/{name}/logs", a.handleWorkloadLogs)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/findings", a.handleFindings)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/kinds", a.handleKinds)
	m.HandleFunc("GET /api/v1/clusters/{cluster}/graph", a.handleGraph)
	m.HandleFunc("GET /api/v1/stream", a.handleStream)
	m.HandleFunc("GET /api/v1/search", a.handleSearch)
	m.HandleFunc("GET /api/v1/attention", a.handleAttention)

	m.HandleFunc("GET /api/v1/threads", a.handleListThreads)
	m.HandleFunc("POST /api/v1/threads", a.handleCreateThread)
	m.HandleFunc("GET /api/v1/threads/{id}", a.handleGetThread)
	m.HandleFunc("POST /api/v1/threads/{id}/messages", a.handleReply)
	m.HandleFunc("POST /api/v1/threads/{id}/resolve", a.handleResolve)
	m.HandleFunc("POST /api/v1/threads/{id}/reopen", a.handleReopen)
	m.HandleFunc("DELETE /api/v1/threads/{id}", a.handleDeleteThread)

	m.HandleFunc("GET /api/v1/ai/chats", a.handleListChats)
	m.HandleFunc("POST /api/v1/ai/chats", a.handleCreateChat)
	m.HandleFunc("GET /api/v1/ai/chats/{id}", a.handleGetChat)
	m.HandleFunc("PATCH /api/v1/ai/chats/{id}", a.handlePatchChat)
	m.HandleFunc("DELETE /api/v1/ai/chats/{id}", a.handleDeleteChat)
	m.HandleFunc("POST /api/v1/ai/ask", a.handleAsk)
	m.HandleFunc("GET /api/v1/audit", a.handleAudit)
	a.auth.TokenRoutes(m)
	m.HandleFunc("/api/", notFoundAPI)
	apiH := a.auth.Authenticate(a.auth.RequireUser(recordUser(a.auth.RequireCSRF(m))))

	am := http.NewServeMux()
	a.auth.Routes(am)
	am.HandleFunc("/auth/", notFoundAPI)
	authH := a.auth.Authenticate(recordUser(am))

	root := http.NewServeMux()
	root.Handle("/api/", gzipResponses(a.metrics, apiH))
	root.Handle("/auth/", authH)
	root.Handle("/mcp", mcpHandler)
	// The peer endpoint lives on its own listener only.
	root.HandleFunc("/peer/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not found", http.StatusNotFound) })
	root.HandleFunc("GET /healthz", healthz)
	root.Handle("/", spa)
	return observe(a.log, a.metrics, securityHeaders(a.cfg.SecureCookies(), root))
}
