package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/identity"
)

// --- shared helpers ----------------------------------------------------------

// logBuf is a goroutine-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// newLoggedEnv is newEnv with the service and audit logs captured.
func newLoggedEnv(t *testing.T, mutate func(*config.Hub)) (*testEnv, *logBuf) {
	t.Helper()
	cfg := baseConfig(t, usersYAML(t))
	if mutate != nil {
		mutate(cfg)
	}
	buf := &logBuf{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	st := newFakeStore()
	svc, err := New(cfg, st, audit.New(nil, log), log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	clock := newClock()
	svc.now = clock.Now
	return &testEnv{svc: svc, st: st, clock: clock, cfg: cfg}, buf
}

func pkceChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// startLogin runs GET /auth/<id>/login and returns the provider's
// authorization URL query.
func startLogin(t *testing.T, h http.Handler, jar map[string]string, id, returnTo string) url.Values {
	t.Helper()
	path := "/auth/" + id + "/login"
	if returnTo != "" {
		path += "?returnTo=" + url.QueryEscape(returnTo)
	}
	rr := do(h, httptest.NewRequest("GET", path, nil), jar)
	if rr.Code != http.StatusFound {
		t.Fatalf("login start: %d %s", rr.Code, rr.Body)
	}
	loc, err := url.Parse(rr.Header().Get("Location"))
	if err != nil || loc.Host == "" {
		t.Fatalf("login start location %q", rr.Header().Get("Location"))
	}
	return loc.Query()
}

func callback(h http.Handler, jar map[string]string, id string, q url.Values) *httptest.ResponseRecorder {
	return do(h, httptest.NewRequest("GET", "/auth/"+id+"/callback?"+q.Encode(), nil), jar)
}

// loginError returns the error code of a redirect to /login, or "".
func loginError(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: status %d body %s", rr.Code, rr.Body)
	}
	u, err := url.Parse(rr.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/login" {
		return ""
	}
	return u.Query().Get("error")
}

func me(t *testing.T, h http.Handler, jar map[string]string) meView {
	t.Helper()
	return decodeMe(t, do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar))
}

// --- fake GitHub ------------------------------------------------------------

type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	codes    map[string]string // code -> PKCE challenge
	emails   []githubEmail
	orgs     []string // active memberships
	pending  []string
	teams    [][2]string // org, slug
	tokenErr bool
}

const ghToken = "gho_testtoken_never_logged"

func newFakeGitHub(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{t: t, codes: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		chal, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		if f.tokenErr || !ok || r.Form.Get("client_secret") != "gh-secret" || pkceChallenge(r.Form.Get("code_verifier")) != chal {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"bad_verification_code"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + ghToken + `","token_type":"bearer","scope":"read:user,user:email,read:org"}`))
	})
	auth := func(next func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+ghToken {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
	page := func(r *http.Request, n int) (int, int) {
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if per <= 0 {
			per = 30
		}
		if p <= 0 {
			p = 1
		}
		lo := min((p-1)*per, n)
		return lo, min(lo+per, n)
	}
	mux.HandleFunc("GET /api/v3/user", auth(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"login":"Alice","name":"Alice Doe","id":1}`))
	}))
	mux.HandleFunc("GET /api/v3/user/emails", auth(func(w http.ResponseWriter, r *http.Request) {
		lo, hi := page(r, len(f.emails))
		_ = json.NewEncoder(w).Encode(f.emails[lo:hi])
	}))
	mux.HandleFunc("GET /api/v3/user/memberships/orgs", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != "active" {
			http.Error(w, "want state=active", http.StatusBadRequest)
			return
		}
		type m struct {
			State        string            `json:"state"`
			Organization map[string]string `json:"organization"`
		}
		var all []m
		for _, o := range f.orgs {
			all = append(all, m{"active", map[string]string{"login": o}})
		}
		for _, o := range f.pending {
			all = append(all, m{"pending", map[string]string{"login": o}})
		}
		lo, hi := page(r, len(all))
		_ = json.NewEncoder(w).Encode(all[lo:hi])
	}))
	mux.HandleFunc("GET /api/v3/user/teams", auth(func(w http.ResponseWriter, r *http.Request) {
		type tm struct {
			Slug         string            `json:"slug"`
			Organization map[string]string `json:"organization"`
		}
		var all []tm
		for _, x := range f.teams {
			all = append(all, tm{x[1], map[string]string{"login": x[0]}})
		}
		lo, hi := page(r, len(all))
		_ = json.NewEncoder(w).Encode(all[lo:hi])
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// approve plays the user approving at GitHub: it returns the callback query.
func (f *fakeGitHub) approve(authQ url.Values) url.Values {
	code := "code-" + strconv.Itoa(len(f.codes)+1) + "-" + authQ.Get("state")[:6]
	f.mu.Lock()
	f.codes[code] = authQ.Get("code_challenge")
	f.mu.Unlock()
	return url.Values{"code": {code}, "state": {authQ.Get("state")}}
}

func enableGitHub(t *testing.T, f *fakeGitHub, mut func(*config.GitHubAuth)) func(*config.Hub) {
	return func(c *config.Hub) {
		t.Setenv("GITHUB_CLIENT_SECRET", "gh-secret")
		c.Auth.GitHub = config.GitHubAuth{
			Enabled:              true,
			Name:                 "GitHub",
			ClientID:             "gh-client",
			ClientSecretEnv:      "GITHUB_CLIENT_SECRET",
			BaseURL:              f.srv.URL,
			AllowedOrganizations: []string{"acme"},
		}
		if mut != nil {
			mut(&c.Auth.GitHub)
		}
	}
}

func TestGitHubSignIn(t *testing.T) {
	tests := []struct {
		name       string
		mut        func(*config.GitHubAuth)
		setup      func(f *fakeGitHub)
		wantErr    string
		wantUser   string
		wantGroups []string
	}{
		{
			name: "org and team groups, verified primary email",
			setup: func(f *fakeGitHub) {
				f.emails = []githubEmail{{Email: "other@example.com", Verified: true}, {Email: "Alice@Example.com", Primary: true, Verified: true}}
				f.orgs = []string{"Acme", "other-org"}
				f.teams = [][2]string{{"acme", "platform"}, {"other-org", "x"}}
			},
			wantUser:   "alice@example.com",
			wantGroups: []string{"eddy:authenticated", "eddy:github:acme", "eddy:github:acme/platform"},
		},
		{
			name: "no verified primary email falls back to github:<login>",
			setup: func(f *fakeGitHub) {
				f.emails = []githubEmail{{Email: "alice@example.com", Primary: true, Verified: false}}
				f.orgs = []string{"acme"}
			},
			wantUser:   "github:alice",
			wantGroups: []string{"eddy:authenticated", "eddy:github:acme"},
		},
		{
			name: "user prefix",
			mut:  func(g *config.GitHubAuth) { g.UserPrefix = "gh-" },
			setup: func(f *fakeGitHub) {
				f.orgs = []string{"acme"}
			},
			wantUser:   "gh-github:alice",
			wantGroups: []string{"eddy:authenticated", "eddy:github:acme"},
		},
		{
			name: "teams off",
			mut:  func(g *config.GitHubAuth) { f := false; g.TeamsAsGroups = &f },
			setup: func(f *fakeGitHub) {
				f.orgs = []string{"acme"}
				f.teams = [][2]string{{"acme", "platform"}}
			},
			wantUser:   "github:alice",
			wantGroups: []string{"eddy:authenticated", "eddy:github:acme"},
		},
		{
			name: "allowed org found on the second page",
			setup: func(f *fakeGitHub) {
				for i := range 130 {
					f.orgs = append(f.orgs, fmt.Sprintf("org-%03d", i))
				}
				f.orgs = append(f.orgs, "acme")
			},
			wantUser:   "github:alice",
			wantGroups: []string{"eddy:authenticated", "eddy:github:acme"},
		},
		{
			name:    "not in an allowed org",
			setup:   func(f *fakeGitHub) { f.orgs = []string{"other-org"} },
			wantErr: oauthErrDenied,
		},
		{
			name:    "pending membership does not count",
			setup:   func(f *fakeGitHub) { f.pending = []string{"acme"} },
			wantErr: oauthErrDenied,
		},
		{
			name: "allowedTeams deny",
			mut:  func(g *config.GitHubAuth) { g.AllowedTeams = []string{"acme/sre"} },
			setup: func(f *fakeGitHub) {
				f.orgs = []string{"acme"}
				f.teams = [][2]string{{"acme", "platform"}}
			},
			wantErr: oauthErrDenied,
		},
		{
			name: "allowedTeams allow",
			mut:  func(g *config.GitHubAuth) { g.AllowedTeams = []string{"Acme/SRE"} },
			setup: func(f *fakeGitHub) {
				f.orgs = []string{"acme"}
				f.teams = [][2]string{{"acme", "sre"}}
			},
			wantUser:   "github:alice",
			wantGroups: []string{"eddy:authenticated", "eddy:github:acme", "eddy:github:acme/sre"},
		},
		{
			name: "allowAllUsers keeps every org",
			mut:  func(g *config.GitHubAuth) { g.AllowedOrganizations = nil; g.AllowAllUsers = true },
			setup: func(f *fakeGitHub) {
				f.orgs = []string{"one", "two"}
			},
			wantUser:   "github:alice",
			wantGroups: []string{"eddy:authenticated", "eddy:github:one", "eddy:github:two"},
		},
		{
			name: "denyUserPrefixes applies",
			mut:  func(g *config.GitHubAuth) { g.UserPrefix = "system:" },
			setup: func(f *fakeGitHub) {
				f.orgs = []string{"acme"}
			},
			wantErr: oauthErrDenied,
		},
		{
			name:    "token endpoint error",
			setup:   func(f *fakeGitHub) { f.orgs = []string{"acme"}; f.tokenErr = true },
			wantErr: oauthErrProvider,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeGitHub(t)
			tc.setup(f)
			e, logs := newLoggedEnv(t, enableGitHub(t, f, tc.mut))
			h := e.handler()
			jar := map[string]string{}
			q := startLogin(t, h, jar, "github", "/c/prod/flux")
			if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != "gh-client" ||
				q.Get("redirect_uri") != testPublicURL+"/auth/github/callback" || !strings.Contains(q.Get("scope"), "read:org") {
				t.Fatalf("authorization request %v", q)
			}
			rr := callback(h, jar, "github", f.approve(q))
			if got := loginError(t, rr); got != tc.wantErr {
				t.Fatalf("error %q, want %q (location %s)", got, tc.wantErr, rr.Header().Get("Location"))
			}
			if strings.Contains(logs.String(), ghToken) || strings.Contains(logs.String(), "gh-secret") {
				t.Fatal("a token or secret reached the logs")
			}
			if tc.wantErr != "" {
				if jar[e.svc.cookieName] != "" {
					t.Fatal("a failed sign-in set a session")
				}
				if !strings.Contains(logs.String(), `"result":"denied"`) {
					t.Fatalf("no denied audit: %s", logs)
				}
				return
			}
			if rr.Header().Get("Location") != "/c/prod/flux" {
				t.Fatalf("redirect %q", rr.Header().Get("Location"))
			}
			if jar[e.svc.flowCookieName] != "" {
				t.Fatal("flow cookie not cleared")
			}
			m := me(t, h, jar)
			slices.Sort(m.Groups)
			if m.User != tc.wantUser || m.Provider != ProviderGitHub || !slices.Equal(m.Groups, tc.wantGroups) {
				t.Fatalf("me %+v, want %s %v", m, tc.wantUser, tc.wantGroups)
			}
		})
	}
}

func TestGitHubSessionRotatesAndProviderRemoval(t *testing.T) {
	f := newFakeGitHub(t)
	f.orgs = []string{"acme"}
	e := newEnv(t, enableGitHub(t, f, nil))
	h := e.handler()
	jar, _ := e.localSession(t)
	old := jar[e.svc.cookieName]
	q := startLogin(t, h, jar, "github", "")
	if rr := callback(h, jar, "github", f.approve(q)); loginError(t, rr) != "" || rr.Header().Get("Location") != "/" {
		t.Fatalf("callback %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if jar[e.svc.cookieName] == old || jar[e.svc.cookieName] == "" {
		t.Fatal("session id not rotated on sign-in")
	}
	if hsh, _ := sessionHash(old); func() bool { _, err := e.st.sess.Get(context.Background(), hsh, e.clock.Now()); return err == nil }() {
		t.Fatal("old session kept")
	}
	if m := me(t, h, jar); m.User != "github:alice" {
		t.Fatalf("me %+v", m)
	}
	// Turning the provider off ends its sessions.
	e.svc.oauth = map[string]oauthProvider{}
	e.svc.sessions.clear()
	if rr := do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar); rr.Code != http.StatusUnauthorized {
		t.Fatalf("me after provider removal: %d", rr.Code)
	}
}

// --- flow cookie and callback CSRF -------------------------------------------

func TestOAuthFlowCookie(t *testing.T) {
	f := newFakeGitHub(t)
	f.orgs = []string{"acme"}
	e := newEnv(t, enableGitHub(t, f, nil))
	h := e.handler()

	v, err := e.svc.sealFlow(oauthFlow{Provider: "github", State: "s", Nonce: "n", Verifier: "v", ReturnTo: "/x", Expires: e.clock.Now().Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(v, "/x") {
		t.Fatal("flow cookie is not encrypted")
	}
	if _, ok := e.svc.openFlow(v, e.clock.Now()); !ok {
		t.Fatal("round trip failed")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(v)
	raw[len(raw)-1] ^= 1
	if _, ok := e.svc.openFlow(base64.RawURLEncoding.EncodeToString(raw), e.clock.Now()); ok {
		t.Fatal("tampered cookie accepted")
	}
	if _, ok := e.svc.openFlow(v, e.clock.Now().Add(2*time.Minute)); ok {
		t.Fatal("expired cookie accepted")
	}
	other := newEnv(t, enableGitHub(t, f, nil))
	other.svc.keys.oauthFlow = bytes.Repeat([]byte{7}, 32)
	if _, ok := other.svc.openFlow(v, e.clock.Now()); ok {
		t.Fatal("cookie accepted under another key")
	}
	bad, _ := e.svc.sealFlow(oauthFlow{Provider: "github", State: "s", Nonce: "n", Verifier: "v", ReturnTo: "//evil.example", Expires: e.clock.Now().Add(time.Minute).Unix()})
	if _, ok := e.svc.openFlow(bad, e.clock.Now()); ok {
		t.Fatal("cookie with a foreign returnTo accepted")
	}

	// Cookie attributes.
	rr := do(h, httptest.NewRequest("GET", "/auth/github/login", nil), nil)
	var fc *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "__Host-eddy_oauth" {
			fc = c
		}
	}
	if fc == nil || !fc.HttpOnly || !fc.Secure || fc.SameSite != http.SameSiteLaxMode || fc.Path != "/" || fc.MaxAge != 600 {
		t.Fatalf("flow cookie %+v", fc)
	}

	tests := []struct {
		name string
		run  func(jar map[string]string, q url.Values) *httptest.ResponseRecorder
		want string
	}{
		{"valid", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			return callback(h, jar, "github", f.approve(q))
		}, ""},
		{"bad state", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			cb := f.approve(q)
			cb.Set("state", "attacker-state")
			return callback(h, jar, "github", cb)
		}, oauthErrState},
		{"no cookie (login CSRF)", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			// The victim's browser never started this flow.
			delete(jar, e.svc.flowCookieName)
			return callback(h, jar, "github", f.approve(q))
		}, oauthErrState},
		{"tampered cookie", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			c := []byte(jar[e.svc.flowCookieName])
			c[10] ^= 1
			jar[e.svc.flowCookieName] = string(c)
			return callback(h, jar, "github", f.approve(q))
		}, oauthErrState},
		{"expired after 10 minutes", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			e.clock.Add(10*time.Minute + time.Second)
			return callback(h, jar, "github", f.approve(q))
		}, oauthErrState},
		{"replayed callback", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			cb := f.approve(q)
			saved := jar[e.svc.flowCookieName]
			if rr := callback(h, jar, "github", cb); loginError(t, rr) != "" {
				t.Fatal("first callback failed")
			}
			if jar[e.svc.flowCookieName] != "" {
				t.Fatal("flow cookie not cleared")
			}
			delete(jar, e.svc.cookieName)
			jar[e.svc.flowCookieName] = saved // a copied cookie: the code is spent
			return callback(h, jar, "github", cb)
		}, oauthErrProvider},
		{"user cancelled", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			return callback(h, jar, "github", url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})
		}, oauthErrCancelled},
		{"no code", func(jar map[string]string, q url.Values) *httptest.ResponseRecorder {
			return callback(h, jar, "github", url.Values{"state": {q.Get("state")}})
		}, oauthErrProvider},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			jar := map[string]string{}
			q := startLogin(t, h, jar, "github", "/x")
			rr := tc.run(jar, q)
			if got := loginError(t, rr); got != tc.want {
				t.Fatalf("error %q, want %q", got, tc.want)
			}
			if tc.want != "" && jar[e.svc.flowCookieName] != "" {
				t.Fatal("flow cookie kept after a failed callback")
			}
		})
	}
}

func TestOAuthReturnTo(t *testing.T) {
	f := newFakeGitHub(t)
	f.orgs = []string{"acme"}
	e := newEnv(t, enableGitHub(t, f, nil))
	h := e.handler()
	for _, bad := range []string{"//evil.example/x", "https://evil.example/", "/\\evil.example", "javascript:alert(1)", "x"} {
		rr := do(h, httptest.NewRequest("GET", "/auth/github/login?returnTo="+url.QueryEscape(bad), nil), nil)
		if got := loginError(t, rr); got != oauthErrBadRequest {
			t.Fatalf("returnTo %q: error %q location %q", bad, got, rr.Header().Get("Location"))
		}
	}
	if rr := do(h, httptest.NewRequest("GET", "/auth/nope/login", nil), nil); rr.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", rr.Code)
	}
	// An error keeps a valid returnTo so a retry lands in the same place.
	jar := map[string]string{}
	q := startLogin(t, h, jar, "github", "/c/prod")
	cb := f.approve(q)
	cb.Set("state", "x")
	rr := callback(h, jar, "github", cb)
	if rr.Header().Get("Location") != "/login?error=state&returnTo=%2Fc%2Fprod" {
		t.Fatalf("location %q", rr.Header().Get("Location"))
	}
}

func TestOAuthCallbackRateLimit(t *testing.T) {
	f := newFakeGitHub(t)
	f.orgs = []string{"acme"}
	e := newEnv(t, func(c *config.Hub) {
		enableGitHub(t, f, nil)(c)
		c.Auth.LoginRateLimit.PerIPPerMinute = 3
	})
	h := e.handler()
	for i := range 3 {
		if got := loginError(t, callback(h, map[string]string{}, "github", url.Values{"state": {"x"}, "code": {"y"}})); got != oauthErrState {
			t.Fatalf("attempt %d: %q", i, got)
		}
	}
	jar := map[string]string{}
	q := startLogin(t, h, jar, "github", "")
	if got := loginError(t, callback(h, jar, "github", f.approve(q))); got != oauthErrRateLimited {
		t.Fatalf("after the limit: %q", got)
	}
	// Another address is not affected.
	jar = map[string]string{}
	q = startLogin(t, h, jar, "github", "")
	r := httptest.NewRequest("GET", "/auth/github/callback?"+f.approve(q).Encode(), nil)
	r.RemoteAddr = "10.1.1.1:999"
	if got := loginError(t, do(h, r, jar)); got != "" {
		t.Fatalf("other ip: %q", got)
	}
	// The window passes.
	e.clock.Add(oauthFailWindow + time.Second)
	jar = map[string]string{}
	q = startLogin(t, h, jar, "github", "")
	if got := loginError(t, callback(h, jar, "github", f.approve(q))); got != "" {
		t.Fatalf("after the window: %q", got)
	}
}

// --- fake OIDC issuer -----------------------------------------------------------

type fakeIssuer struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	other  *rsa.PrivateKey
	mu     sync.Mutex
	codes  map[string]issuedCode
	claims func(nonce string) map[string]any // per test
	signer *rsa.PrivateKey                   // nil: key
}

type issuedCode struct{ nonce, challenge string }

func newFakeIssuer(t *testing.T) *fakeIssuer {
	f := &fakeIssuer{t: t, codes: map[string]issuedCode{}}
	var err error
	if f.key, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if f.other, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.srv.URL,
			"authorization_endpoint":                f.srv.URL + "/authorize",
			"token_endpoint":                        f.srv.URL + "/token",
			"jwks_uri":                              f.srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := f.key.PublicKey
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		user, pass, _ := r.BasicAuth()
		if user == "" {
			user, pass = r.Form.Get("client_id"), r.Form.Get("client_secret")
		}
		f.mu.Lock()
		c, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok || user != "oidc-client" || pass != "oidc-secret" || pkceChallenge(r.Form.Get("code_verifier")) != c.challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-never-logged", "token_type": "Bearer", "expires_in": 300,
			"id_token": f.sign(f.claims(c.nonce)),
		})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) sign(claims map[string]any) string {
	k := f.key
	if f.signer != nil {
		k = f.signer
	}
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	in := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(body)
	sum := sha256.Sum256([]byte(in))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return in + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIssuer) approve(authQ url.Values) url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	code := fmt.Sprintf("c%d", len(f.codes)+1) + authQ.Get("state")[:6]
	f.codes[code] = issuedCode{nonce: authQ.Get("nonce"), challenge: authQ.Get("code_challenge")}
	return url.Values{"code": {code}, "state": {authQ.Get("state")}}
}

func enableOIDC(t *testing.T, f *fakeIssuer, mut func(*config.OIDCAuth)) func(*config.Hub) {
	return func(c *config.Hub) {
		t.Setenv("OIDC_CORP_CLIENT_SECRET", "oidc-secret")
		o := config.OIDCAuth{ID: "corp", Preset: "dex", Issuer: f.srv.URL, ClientID: "oidc-client"}
		if mut != nil {
			mut(&o)
		}
		config.ApplyOIDCPreset(&o)
		c.Auth.OIDC = []config.OIDCAuth{o}
	}
}

func TestOIDCSignIn(t *testing.T) {
	now := newClock().Now()
	base := func(f *fakeIssuer, nonce string) map[string]any {
		return map[string]any{
			"iss": f.srv.URL, "aud": "oidc-client", "sub": "u-1",
			"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "nonce": nonce,
			"email": "Alice@Example.com", "email_verified": true, "name": "Alice Doe",
			"groups": []any{"platform", "system:masters", "eng"},
		}
	}
	tests := []struct {
		name       string
		mut        func(*config.OIDCAuth)
		claims     func(c map[string]any)
		wrongKey   bool
		wantErr    string
		wantUser   string
		wantGroups []string
	}{
		{name: "groups claim mapped, system dropped", wantUser: "alice@example.com",
			wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:platform"}},
		{name: "allowedGroups match", mut: func(o *config.OIDCAuth) { o.AllowedGroups = []string{"platform"} },
			wantUser: "alice@example.com", wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:platform"}},
		{name: "allowedGroups miss", mut: func(o *config.OIDCAuth) { o.AllowedGroups = []string{"admins"} }, wantErr: oauthErrDenied},
		{name: "wrong audience", claims: func(c map[string]any) { c["aud"] = "someone-else" }, wantErr: oauthErrProvider},
		{name: "several audiences need azp", claims: func(c map[string]any) { c["aud"] = []any{"oidc-client", "other"} }, wantErr: oauthErrProvider},
		{name: "several audiences with azp", claims: func(c map[string]any) {
			c["aud"] = []any{"oidc-client", "other"}
			c["azp"] = "oidc-client"
		}, wantUser: "alice@example.com", wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:platform"}},
		{name: "wrong issuer", claims: func(c map[string]any) { c["iss"] = "https://evil.example" }, wantErr: oauthErrProvider},
		{name: "wrong nonce", claims: func(c map[string]any) { c["nonce"] = "replayed" }, wantErr: oauthErrProvider},
		{name: "no nonce", claims: func(c map[string]any) { delete(c, "nonce") }, wantErr: oauthErrProvider},
		{name: "expired", claims: func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() }, wantErr: oauthErrProvider},
		{name: "bad signature", wrongKey: true, wantErr: oauthErrProvider},
		{name: "unverified email", claims: func(c map[string]any) { c["email_verified"] = false }, wantErr: oauthErrDenied},
		{name: "string email_verified", claims: func(c map[string]any) { c["email_verified"] = "true" },
			wantUser: "alice@example.com", wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:platform"}},
		{name: "unverified email allowed when not required", mut: func(o *config.OIDCAuth) { f := false; o.RequireVerifiedEmail = &f },
			claims: func(c map[string]any) { c["email_verified"] = false }, wantUser: "alice@example.com",
			wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:platform"}},
		{name: "allowedDomains on email", mut: func(o *config.OIDCAuth) { o.AllowedDomains = []string{"example.org"} }, wantErr: oauthErrDenied},
		{name: "username claim and prefix", mut: func(o *config.OIDCAuth) { o.UsernameClaim = "sub"; o.UserPrefix = "corp:" },
			wantUser: "corp:u-1", wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:platform"}},
		{name: "invalid user name", mut: func(o *config.OIDCAuth) { o.UsernameClaim = "name" }, wantErr: oauthErrDenied},
		{name: "static groups by email", wantUser: "alice@example.com",
			wantGroups: []string{"eddy:authenticated", "eddy:eng", "eddy:oncall", "eddy:platform"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIssuer(t)
			f.claims = func(nonce string) map[string]any {
				c := base(f, nonce)
				if tc.claims != nil {
					tc.claims(c)
				}
				return c
			}
			if tc.wrongKey {
				f.signer = f.other
			}
			e, logs := newLoggedEnv(t, func(c *config.Hub) {
				enableOIDC(t, f, tc.mut)(c)
				if tc.name == "static groups by email" {
					c.Auth.Groups.Static = map[string][]string{"alice@example.com": {"oncall"}}
				}
			})
			h := e.handler()
			jar := map[string]string{}
			q := startLogin(t, h, jar, "corp", "/threads")
			if q.Get("nonce") == "" || q.Get("code_challenge_method") != "S256" || !strings.Contains(q.Get("scope"), "openid") {
				t.Fatalf("authorization request %v", q)
			}
			rr := callback(h, jar, "corp", f.approve(q))
			if got := loginError(t, rr); got != tc.wantErr {
				t.Fatalf("error %q, want %q", got, tc.wantErr)
			}
			if strings.Contains(logs.String(), "at-never-logged") || strings.Contains(logs.String(), "oidc-secret") {
				t.Fatal("a token or secret reached the logs")
			}
			if tc.wantErr != "" {
				return
			}
			if rr.Header().Get("Location") != "/threads" {
				t.Fatalf("redirect %q", rr.Header().Get("Location"))
			}
			m := me(t, h, jar)
			slices.Sort(m.Groups)
			if m.User != tc.wantUser || m.Provider != "oidc:corp" || !slices.Equal(m.Groups, tc.wantGroups) {
				t.Fatalf("me %+v, want %s %v", m, tc.wantUser, tc.wantGroups)
			}
		})
	}
}

func TestOIDCGoogleHostedDomain(t *testing.T) {
	now := newClock().Now()
	tests := []struct {
		name    string
		hd      any
		wantErr string
	}{
		{"matching hd", "example.com", ""},
		{"hd case-insensitive", "Example.COM", ""},
		{"other workspace", "evil.example", oauthErrDenied},
		{"personal account (no hd)", nil, oauthErrDenied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIssuer(t)
			f.claims = func(nonce string) map[string]any {
				c := map[string]any{
					"iss": f.srv.URL, "aud": "oidc-client", "sub": "1",
					"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": nonce,
					// The email domain does not decide: only hd does.
					"email": "alice@example.com", "email_verified": true,
				}
				if tc.hd != nil {
					c["hd"] = tc.hd
				}
				return c
			}
			e := newEnv(t, enableOIDC(t, f, func(o *config.OIDCAuth) {
				o.Preset = "google"
				o.AllowedDomains = []string{"example.com"}
			}))
			h := e.handler()
			jar := map[string]string{}
			q := startLogin(t, h, jar, "corp", "")
			if q.Get("hd") != "example.com" {
				t.Fatalf("hd hint missing: %v", q)
			}
			if got := loginError(t, callback(h, jar, "corp", f.approve(q))); got != tc.wantErr {
				t.Fatalf("error %q, want %q", got, tc.wantErr)
			}
		})
	}
}

func TestOIDCUnreachableIssuer(t *testing.T) {
	f := newFakeIssuer(t)
	e := newEnv(t, enableOIDC(t, f, nil))
	f.srv.Close()
	rr := do(e.handler(), httptest.NewRequest("GET", "/auth/corp/login", nil), nil)
	if got := loginError(t, rr); got != oauthErrUnavailable {
		t.Fatalf("error %q", got)
	}
}

func TestOAuthCallbackForAnotherProvider(t *testing.T) {
	gh := newFakeGitHub(t)
	gh.orgs = []string{"acme"}
	iss := newFakeIssuer(t)
	e := newEnv(t, func(c *config.Hub) {
		enableGitHub(t, gh, nil)(c)
		enableOIDC(t, iss, nil)(c)
	})
	h := e.handler()
	jar := map[string]string{}
	q := startLogin(t, h, jar, "github", "")
	// The GitHub flow cookie must not complete an OIDC callback.
	if got := loginError(t, callback(h, jar, "corp", url.Values{"code": {"x"}, "state": {q.Get("state")}})); got != oauthErrState {
		t.Fatalf("error %q", got)
	}
	ps := e.svc.Providers()
	if len(ps.Providers) != 2 || ps.Providers[0].ID != "github" || ps.Providers[0].Icon != "github" ||
		ps.Providers[1].Kind != "oidc" || ps.Providers[1].LoginURL != "/auth/corp/login" {
		t.Fatalf("providers %+v", ps.Providers)
	}
}

func TestOAuthPATGroupsFollowLatestSession(t *testing.T) {
	f := newFakeGitHub(t)
	f.orgs = []string{"acme"}
	f.teams = [][2]string{{"acme", "platform"}}
	e := newEnv(t, enableGitHub(t, f, nil))
	h := e.handler()
	jar := map[string]string{}
	q := startLogin(t, h, jar, "github", "")
	if got := loginError(t, callback(h, jar, "github", f.approve(q))); got != "" {
		t.Fatal(got)
	}
	m := me(t, h, jar)
	p := identity.Principal{User: m.User, Groups: m.Groups, Provider: m.Provider, Via: identity.ViaWeb}
	ctx := context.Background()
	if _, _, err := e.svc.Issue(ctx, p, "too long", []string{"read"}, 31*24*time.Hour); err == nil {
		t.Fatal("a PAT over maxTTLProxy was issued for a GitHub user")
	}
	tok, _, err := e.svc.Issue(ctx, p, "mcp", []string{"read"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Leaving the team shrinks the PAT's groups at the next sign-in.
	f.teams = nil
	e.clock.Add(time.Minute)
	jar = map[string]string{}
	q = startLogin(t, h, jar, "github", "")
	if got := loginError(t, callback(h, jar, "github", f.approve(q))); got != "" {
		t.Fatal(got)
	}
	got, _, err := e.svc.VerifyPAT(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got.Groups, "eddy:github:acme/platform") || !slices.Contains(got.Groups, "eddy:github:acme") {
		t.Fatalf("pat groups %v", got.Groups)
	}
	// Removing the provider invalidates its tokens.
	e.svc.oauth = map[string]oauthProvider{}
	if _, _, err := e.svc.VerifyPAT(ctx, tok); err == nil {
		t.Fatal("PAT accepted after its provider was removed")
	}
}

// --- local modes -------------------------------------------------------------------

func localLogin(t *testing.T, h http.Handler, user, pw string) *httptest.ResponseRecorder {
	t.Helper()
	jar := map[string]string{}
	var pre struct{ CSRF string }
	if err := json.NewDecoder(do(h, httptest.NewRequest("GET", "/auth/csrf", nil), jar).Body).Decode(&pre); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/auth/local/login", strings.NewReader(`{"username":"`+user+`","password":"`+pw+`"}`))
	r.Header.Set("X-Eddy-CSRF", pre.CSRF)
	r.Header.Set("Origin", testPublicURL)
	return do(h, r, jar)
}

func TestLocalBreakglassMode(t *testing.T) {
	f := newFakeGitHub(t)
	e, logs := newLoggedEnv(t, func(c *config.Hub) {
		enableGitHub(t, f, nil)(c)
		c.Auth.Local.Mode = config.LocalModeBreakglass
	})
	h := e.handler()
	rr := do(h, httptest.NewRequest("GET", "/auth/providers", nil), nil)
	var ps Providers
	if err := json.NewDecoder(rr.Body).Decode(&ps); err != nil {
		t.Fatal(err)
	}
	if !ps.Local.Enabled || ps.Local.Mode != "breakglass" || len(ps.Providers) != 1 || ps.Providers[0].Name != "GitHub" {
		t.Fatalf("providers %+v", ps)
	}
	if rr := localLogin(t, h, "alice", "wrong"); rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad password: %d", rr.Code)
	}
	if rr := localLogin(t, h, "alice", testPassword); rr.Code != http.StatusNoContent {
		t.Fatalf("break-glass login: %d %s", rr.Code, rr.Body)
	}
	out := logs.String()
	if strings.Count(out, `"level":"WARN","msg":"break-glass local sign-in attempt"`) != 2 ||
		!strings.Contains(out, `"msg":"break-glass local sign-in succeeded"`) || !strings.Contains(out, `"mode":"breakglass"`) {
		t.Fatalf("break-glass logs: %s", out)
	}
}

func TestLocalAllowedUsers(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { c.Auth.Local.AllowedUsers = []string{"admin"} })
	h := e.handler()
	rr := localLogin(t, h, "alice", testPassword)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), genericLogin) {
		t.Fatalf("alice outside allowedUsers: %d %s", rr.Code, rr.Body)
	}
	// An existing session of a user outside the list is dropped too.
	jar, _ := e.localSession(t)
	if rr := do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar); rr.Code != http.StatusUnauthorized {
		t.Fatalf("session outside allowedUsers: %d", rr.Code)
	}
}

func TestMapperOAuthUser(t *testing.T) {
	m := NewMapper(config.Auth{DenyUserPrefixes: []string{"system:", "eks:"}}, discardLog())
	tests := []struct {
		prefix, value, want string
		ok                  bool
	}{
		{"", "alice@example.com", "alice@example.com", true},
		{"github:", "alice", "github:alice", true},
		{"", "system:admin", "", false},
		{"system:", "alice", "", false},
		{"eks:", "alice", "", false},
		{"", "Alice Doe", "", false},
		{"", "a,b", "", false},
		{"", "", "", false},
	}
	for _, tc := range tests {
		got, err := m.OAuthUser(tc.prefix, tc.value)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("OAuthUser(%q, %q) = %q, %v", tc.prefix, tc.value, got, err)
		}
	}
}
