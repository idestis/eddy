package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/identity"
)

const (
	testPassword  = "correct horse battery staple"
	testSecret    = "0123456789abcdef0123456789abcdef-proxy"
	testPublicURL = "https://eddy.example.com"
)

var (
	testHashOnce sync.Once
	testHash     string
)

// aliceHash is an argon2id hash of testPassword, computed once per run.
func aliceHash(t *testing.T) string {
	t.Helper()
	testHashOnce.Do(func() {
		h, err := HashPassword(testPassword)
		if err != nil {
			panic(err)
		}
		testHash = h
	})
	return testHash
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func usersYAML(t *testing.T) string {
	return `users:
  - username: alice
    passwordHash: "` + aliceHash(t) + `"
    groups: [platform, oncall]
  - username: bob
    passwordHash: "` + aliceHash(t) + `"
    groups: [viewers]
    disabled: true
`
}

// baseConfig returns a parsed config with local auth enabled.
func baseConfig(t *testing.T, users string) *config.Hub {
	t.Helper()
	dir := t.TempDir()
	key := writeFile(t, dir, "key", strings.Repeat("k", 48))
	uf := writeFile(t, dir, "users.yaml", users)
	cfg, err := config.ParseHub([]byte(`
publicURL: ` + testPublicURL + `
store: {driver: memory}
auth:
  keyFile: ` + key + `
  local:
    enabled: true
    usersFile: ` + uf + `
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

type testEnv struct {
	svc   *Service
	st    *fakeStore
	clock *fakeClock
	cfg   *config.Hub
}

func newEnv(t *testing.T, mutate func(*config.Hub)) *testEnv {
	t.Helper()
	cfg := baseConfig(t, usersYAML(t))
	if mutate != nil {
		mutate(cfg)
	}
	st := newFakeStore()
	svc, err := New(cfg, st, audit.New(nil, discardLog()), discardLog())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clock := newClock()
	svc.now = clock.Now
	return &testEnv{svc: svc, st: st, clock: clock, cfg: cfg}
}

func enableProxy(t *testing.T, cfg *config.Hub) {
	t.Helper()
	t.Setenv("EDDY_PROXY_SECRET", testSecret)
	cfg.Auth.Proxy.Enabled = true
	cfg.Auth.Proxy.TrustedCIDRs = []string{"127.0.0.1/32", "10.0.0.0/8"}
}

// handler returns Authenticate(mux) with the auth and token routes and a
// /api/v1/me-like probe that echoes the principal.
func (e *testEnv) handler() http.Handler {
	mux := http.NewServeMux()
	e.svc.Routes(mux)
	api := http.NewServeMux()
	e.svc.TokenRoutes(api)
	api.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, principalView(r, e.svc))
	})
	api.HandleFunc("POST /api/v1/echo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Seen-User", r.Header.Get("X-Forwarded-Email"))
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/api/", e.svc.RequireUser(e.svc.RequireCSRF(api)))
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := principalFrom(r); ok {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		w.Header().Set("X-Seen-User", r.Header.Get("X-Forwarded-Email"))
		w.WriteHeader(http.StatusOK)
	})
	return e.svc.Authenticate(mux)
}

// do sends req through h, carrying cookies from jar and updating it.
func do(h http.Handler, req *http.Request, jar map[string]string) *httptest.ResponseRecorder {
	for k, v := range jar {
		req.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	if req.RemoteAddr == "" || req.RemoteAddr == "192.0.2.1:1234" {
		req.RemoteAddr = "127.0.0.1:5555"
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if jar != nil {
		for _, c := range rr.Result().Cookies() {
			if c.MaxAge < 0 || c.Value == "" {
				delete(jar, c.Name)
			} else {
				jar[c.Name] = c.Value
			}
		}
	}
	return rr
}

func principalFrom(r *http.Request) (identity.Principal, bool) { return identity.From(r.Context()) }

type meView struct {
	User     string   `json:"user"`
	Groups   []string `json:"groups"`
	Provider string   `json:"provider"`
	CSRF     string   `json:"csrf"`
}

func principalView(r *http.Request, s *Service) meView {
	p, _ := principalFrom(r)
	return meView{User: p.User, Groups: p.Groups, Provider: p.Provider, CSRF: s.CSRFToken(r)}
}

func httptestRequest() *http.Request { return httptest.NewRequest(http.MethodGet, "/", nil) }
