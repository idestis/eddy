package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/config"
)

func decodeMe(t *testing.T, rr *httptest.ResponseRecorder) meView {
	t.Helper()
	if rr.Code != http.StatusOK {
		t.Fatalf("me: status %d body %s", rr.Code, rr.Body)
	}
	var m meView
	if err := json.NewDecoder(rr.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func proxyReq(method, path, peer, user, groups, secret string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = peer
	if user != "" {
		r.Header.Set("X-Forwarded-Email", user)
	}
	if groups != "" {
		r.Header.Set("X-Forwarded-Groups", groups)
	}
	if secret != "" {
		r.Header.Set("X-Eddy-Proxy-Secret", secret)
	}
	return r
}

// localSession creates a local session for alice and returns its cookie jar and CSRF token.
func (e *testEnv) localSession(t *testing.T) (map[string]string, string) {
	t.Helper()
	raw, err := e.svc.createSession(context.Background(), httptestRequest(), "local:alice", "alice", ProviderLocal, nil)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{e.svc.cookieName: raw}, e.svc.csrfFor(raw)
}

func TestProxyTrust(t *testing.T) {
	tests := []struct {
		name     string
		mut      func(*config.Hub)
		build    func() *http.Request
		wantCode int
		wantUser string
	}{
		{name: "trusted loopback", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", testSecret)
		}, wantCode: 200, wantUser: "alice@example.com"},
		{name: "trusted cidr", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "10.9.8.7:4000", "alice@example.com", "", testSecret)
		}, wantCode: 200, wantUser: "alice@example.com"},
		{name: "trusted v4-mapped v6", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "[::ffff:10.9.8.7]:4000", "alice@example.com", "", testSecret)
		}, wantCode: 200, wantUser: "alice@example.com"},
		{name: "untrusted peer", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "192.168.1.5:4000", "alice@example.com", "", testSecret)
		}, wantCode: 401},
		{name: "untrusted v6 peer", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "[2001:db8::1]:4000", "alice@example.com", "", testSecret)
		}, wantCode: 401},
		{name: "xff ignored", build: func() *http.Request {
			r := proxyReq("GET", "/api/v1/me", "192.168.1.5:4000", "alice@example.com", "", testSecret)
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Real-IP", "127.0.0.1")
			return r
		}, wantCode: 401},
		{name: "wrong secret", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", testSecret+"x")
		}, wantCode: 401},
		{name: "missing secret", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", "")
		}, wantCode: 401},
		{name: "two secret headers", build: func() *http.Request {
			r := proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", testSecret)
			r.Header.Add("X-Eddy-Proxy-Secret", testSecret)
			return r
		}, wantCode: 401},
		{name: "insecure skip secret", mut: func(c *config.Hub) { c.Auth.Proxy.InsecureSkipSharedSecret = true }, build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", "")
		}, wantCode: 200, wantUser: "alice@example.com"},
		{name: "insecure skip still needs cidr", mut: func(c *config.Hub) { c.Auth.Proxy.InsecureSkipSharedSecret = true }, build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "192.168.1.5:4000", "alice@example.com", "", "")
		}, wantCode: 401},
		{name: "multiple user headers", build: func() *http.Request {
			r := proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", testSecret)
			r.Header.Add("X-Forwarded-Email", "bob@example.com")
			return r
		}, wantCode: 401},
		{name: "denied user", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "kubernetes-admin", "", testSecret)
		}, wantCode: 401},
		{name: "comma user", build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "a@example.com,b@example.com", "", testSecret)
		}, wantCode: 401},
		{name: "proxy disabled", mut: func(c *config.Hub) { c.Auth.Proxy.Enabled = false }, build: func() *http.Request {
			return proxyReq("GET", "/api/v1/me", "127.0.0.1:4000", "alice@example.com", "", testSecret)
		}, wantCode: 401},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, func(c *config.Hub) {
				enableProxy(t, c)
				if tt.mut != nil {
					tt.mut(c)
				}
			})
			rr := do(e.handler(), tt.build(), nil)
			if rr.Code != tt.wantCode {
				t.Fatalf("status %d, want %d (%s)", rr.Code, tt.wantCode, rr.Body)
			}
			if tt.wantUser != "" {
				if m := decodeMe(t, rr); m.User != tt.wantUser {
					t.Fatalf("user %q, want %q", m.User, tt.wantUser)
				}
			}
			if rr.Code == 401 && !strings.Contains(rr.Body.String(), `"code":"unauthorized"`) {
				t.Fatalf("unexpected error body %s", rr.Body)
			}
		})
	}
}

func TestProxyHeadersStrippedAndNeverOnMCP(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { enableProxy(t, c) })
	var seen []string
	h := e.svc.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("X-Forwarded-Email")+"|"+r.Header.Get("X-Eddy-Proxy-Secret")+"|"+r.Header.Get("X-Forwarded-Groups"))
	}))
	for _, peer := range []string{"192.168.1.5:1", "127.0.0.1:1"} {
		do(h, proxyReq("GET", "/x", peer, "alice@example.com", "g", testSecret), nil)
	}
	for _, s := range seen {
		if s != "||" {
			t.Fatalf("proxy headers reached the handler: %q", s)
		}
	}
	// /mcp: a fully trusted proxy request still gets no principal.
	rr := do(e.handler(), proxyReq("POST", "/mcp", "127.0.0.1:1", "alice@example.com", "", testSecret), nil)
	if rr.Code != http.StatusOK || rr.Header().Get("X-Seen-User") != "" {
		t.Fatalf("/mcp: status %d seen %q", rr.Code, rr.Header().Get("X-Seen-User"))
	}
	if len(rr.Result().Cookies()) != 0 {
		t.Fatal("/mcp minted a session")
	}
}

func TestProxySessionLifecycle(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { enableProxy(t, c) })
	h := e.handler()
	jar := map[string]string{}
	peer := "127.0.0.1:1"

	m1 := decodeMe(t, do(h, proxyReq("GET", "/api/v1/me", peer, "alice@example.com", "dev,ops", testSecret), jar))
	if jar[e.svc.cookieName] == "" || m1.CSRF == "" {
		t.Fatal("no session minted on first proxied request")
	}
	if want := []string{"eddy:authenticated", "eddy:dev", "eddy:ops"}; !slices.Equal(m1.Groups, want) {
		t.Fatalf("groups %q, want %q", m1.Groups, want)
	}
	first := jar[e.svc.cookieName]

	// Same identity: same session, same CSRF.
	m2 := decodeMe(t, do(h, proxyReq("GET", "/api/v1/me", peer, "alice@example.com", "dev,ops", testSecret), jar))
	if jar[e.svc.cookieName] != first || m2.CSRF != m1.CSRF || e.st.sess.count("alice@example.com") != 1 {
		t.Fatal("session not reused")
	}

	// Groups change: re-read every request, session record updated in place.
	m3 := decodeMe(t, do(h, proxyReq("GET", "/api/v1/me", peer, "alice@example.com", "dev", testSecret), jar))
	if !slices.Equal(m3.Groups, []string{"eddy:authenticated", "eddy:dev"}) || m3.CSRF != m1.CSRF {
		t.Fatalf("groups %q csrf changed=%v", m3.Groups, m3.CSRF != m1.CSRF)
	}
	lg, err := e.st.sess.LatestGroups(context.Background(), "alice@example.com")
	if err != nil || !slices.Equal(lg, m3.Groups) {
		t.Fatalf("LatestGroups %q, %v", lg, err)
	}

	// Identity changes: alice's session is dropped and bob gets his own.
	m4 := decodeMe(t, do(h, proxyReq("GET", "/api/v1/me", peer, "bob@example.com", "", testSecret), jar))
	if m4.User != "bob@example.com" || jar[e.svc.cookieName] == first || e.st.sess.count("alice@example.com") != 0 {
		t.Fatalf("identity change not handled: user %q, alice sessions %d", m4.User, e.st.sess.count("alice@example.com"))
	}

	// A proxy session presented without trusted headers is not honoured and is dropped.
	rr := do(h, proxyReq("GET", "/api/v1/me", "192.168.1.5:1", "bob@example.com", "", testSecret), jar)
	if rr.Code != http.StatusUnauthorized || e.st.sess.count("bob@example.com") != 0 {
		t.Fatalf("proxy session honoured without trusted headers: %d", rr.Code)
	}
}

func TestProxyConcurrentFirstRequestsShareSession(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { enableProxy(t, c) })
	h := e.handler()
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			do(h, proxyReq("GET", "/api/v1/me", "127.0.0.1:1", "alice@example.com", "", testSecret), nil)
		})
	}
	wg.Wait()
	if n := e.st.sess.count("alice@example.com"); n != 1 {
		t.Fatalf("%d sessions minted, want 1", n)
	}
}

func TestLocalLoginFlow(t *testing.T) {
	e := newEnv(t, nil)
	h := e.handler()
	jar := map[string]string{}

	csrfResp := do(h, httptest.NewRequest("GET", "/auth/csrf", nil), jar)
	var pre struct{ CSRF string }
	if err := json.NewDecoder(csrfResp.Body).Decode(&pre); err != nil || pre.CSRF == "" || jar[e.svc.preCookieName] == "" {
		t.Fatalf("pre-session: %v %q", err, pre.CSRF)
	}
	if e.svc.preCookieName != "__Host-eddy_pre" || e.svc.cookieName != "__Host-eddy_session" {
		t.Fatalf("cookie names %q %q", e.svc.preCookieName, e.svc.cookieName)
	}

	login := func(user, pw, token, origin string) *httptest.ResponseRecorder {
		body := `{"username":"` + user + `","password":"` + pw + `"}`
		r := httptest.NewRequest("POST", "/auth/local/login", strings.NewReader(body))
		if token != "" {
			r.Header.Set("X-Eddy-CSRF", token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		return do(h, r, jar)
	}

	// CSRF failures come before any hashing.
	if rr := login("alice", testPassword, "", testPublicURL); rr.Code != http.StatusForbidden {
		t.Fatalf("login without CSRF: %d", rr.Code)
	}
	if rr := login("alice", testPassword, pre.CSRF, "https://evil.example"); rr.Code != http.StatusForbidden {
		t.Fatalf("login with foreign origin: %d", rr.Code)
	}

	// Wrong password, unknown and disabled users: identical generic 401.
	var bodies []string
	for _, c := range [][2]string{{"alice", "wrong"}, {"nobody", "wrong"}, {"bob", testPassword}, {"NOT VALID", "x"}} {
		rr := login(c[0], c[1], pre.CSRF, testPublicURL)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status %d", c[0], rr.Code)
		}
		bodies = append(bodies, rr.Body.String())
	}
	for _, b := range bodies {
		if b != bodies[0] || !strings.Contains(b, genericLogin) {
			t.Fatalf("login errors differ: %q", bodies)
		}
	}

	rr := login("alice", testPassword, pre.CSRF, testPublicURL)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", rr.Code, rr.Body)
	}
	var sc *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == e.svc.cookieName {
			sc = c
		}
	}
	if sc == nil || !sc.Secure || !sc.HttpOnly || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" || sc.MaxAge != 0 || sc.Domain != "" {
		t.Fatalf("bad session cookie %+v", sc)
	}
	if _, ok := jar[e.svc.preCookieName]; ok {
		t.Fatal("pre-session cookie not cleared after login")
	}
	first := jar[e.svc.cookieName]
	m := decodeMe(t, do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar))
	if m.User != "local:alice" || m.Provider != ProviderLocal ||
		!slices.Equal(m.Groups, []string{"eddy:authenticated", "eddy:platform", "eddy:oncall"}) {
		t.Fatalf("me = %+v", m)
	}

	// Logging in again rotates the id and deletes the old session.
	do(h, httptest.NewRequest("GET", "/auth/csrf", nil), jar)
	pre2 := e.svc.preToken(strings.Split(jar[e.svc.preCookieName], ".")[0])
	if rr := login("alice", testPassword, pre2, testPublicURL); rr.Code != http.StatusNoContent {
		t.Fatalf("second login: %d", rr.Code)
	}
	if jar[e.svc.cookieName] == first || e.st.sess.count("local:alice") != 1 {
		t.Fatalf("session not rotated (sessions=%d)", e.st.sess.count("local:alice"))
	}

	// Logout needs the session CSRF token.
	out := httptest.NewRequest("POST", "/auth/logout", nil)
	out.Header.Set("Origin", testPublicURL)
	if rr := do(h, out, map[string]string{e.svc.cookieName: jar[e.svc.cookieName]}); rr.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: %d", rr.Code)
	}
	m = decodeMe(t, do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar))
	out = httptest.NewRequest("POST", "/auth/logout", nil)
	out.Header.Set("Origin", testPublicURL)
	out.Header.Set("X-Eddy-CSRF", m.CSRF)
	if rr := do(h, out, jar); rr.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rr.Code)
	}
	if e.st.sess.count("local:alice") != 0 {
		t.Fatal("session survived logout")
	}
}

func TestPreSessionExpiry(t *testing.T) {
	e := newEnv(t, nil)
	cookie, token, err := e.svc.newPreSession(e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(c, tok string) *http.Request {
		r := httptest.NewRequest("POST", loginPath, nil)
		r.AddCookie(&http.Cookie{Name: e.svc.preCookieName, Value: c})
		r.Header.Set("X-Eddy-CSRF", tok)
		return r
	}
	if !e.svc.checkPreSession(mk(cookie, token), e.clock.Now()) {
		t.Fatal("fresh pre-session rejected")
	}
	if e.svc.checkPreSession(mk(cookie, token+"x"), e.clock.Now()) {
		t.Fatal("wrong token accepted")
	}
	parts := strings.Split(cookie, ".")
	forged := parts[0] + ".9999999999." + parts[2]
	if e.svc.checkPreSession(mk(forged, token), e.clock.Now()) {
		t.Fatal("extended expiry accepted")
	}
	if e.svc.checkPreSession(mk(cookie, token), e.clock.Now().Add(11*time.Minute)) {
		t.Fatal("expired pre-session accepted")
	}
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, nil)
	h := e.handler()
	jar, token := e.localSession(t)
	preCookie, preTok, _ := e.svc.newPreSession(e.clock.Now())
	tests := []struct {
		name   string
		method string
		hdr    map[string]string
		want   int
	}{
		{name: "get needs nothing", method: "GET", want: 200},
		{name: "missing header", method: "POST", hdr: map[string]string{"Origin": testPublicURL}, want: 403},
		{name: "wrong header", method: "POST", hdr: map[string]string{"Origin": testPublicURL, "X-Eddy-CSRF": "nope"}, want: 403},
		{name: "pre-session token is not a session token", method: "POST", hdr: map[string]string{"Origin": testPublicURL, "X-Eddy-CSRF": preTok}, want: 403},
		{name: "ok with origin", method: "POST", hdr: map[string]string{"Origin": testPublicURL, "X-Eddy-CSRF": token}, want: 204},
		{name: "origin case-insensitive", method: "POST", hdr: map[string]string{"Origin": "HTTPS://EDDY.example.com", "X-Eddy-CSRF": token}, want: 204},
		{name: "bad origin", method: "POST", hdr: map[string]string{"Origin": "https://evil.example", "X-Eddy-CSRF": token}, want: 403},
		{name: "sibling origin", method: "POST", hdr: map[string]string{"Origin": "https://eddy.example.com.evil.example", "X-Eddy-CSRF": token}, want: 403},
		{name: "http origin", method: "POST", hdr: map[string]string{"Origin": "http://eddy.example.com", "X-Eddy-CSRF": token}, want: 403},
		{name: "null origin", method: "POST", hdr: map[string]string{"Origin": "null", "X-Eddy-CSRF": token}, want: 403},
		{name: "sec-fetch-site same-origin", method: "POST", hdr: map[string]string{"Sec-Fetch-Site": "same-origin", "X-Eddy-CSRF": token}, want: 204},
		{name: "sec-fetch-site cross-site", method: "POST", hdr: map[string]string{"Sec-Fetch-Site": "cross-site", "X-Eddy-CSRF": token}, want: 403},
		{name: "sec-fetch-site same-site", method: "POST", hdr: map[string]string{"Sec-Fetch-Site": "same-site", "X-Eddy-CSRF": token}, want: 403},
		{name: "origin ok but cross-site", method: "POST", hdr: map[string]string{"Origin": testPublicURL, "Sec-Fetch-Site": "cross-site", "X-Eddy-CSRF": token}, want: 403},
		{name: "no origin no fetch metadata", method: "POST", hdr: map[string]string{"X-Eddy-CSRF": token}, want: 403},
		{name: "delete is unsafe", method: "DELETE", hdr: map[string]string{"Origin": testPublicURL}, want: 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/api/v1/echo"
			if tt.method == "GET" {
				path = "/api/v1/me"
			}
			r := httptest.NewRequest(tt.method, path, nil)
			for k, v := range tt.hdr {
				r.Header.Set(k, v)
			}
			r.AddCookie(&http.Cookie{Name: e.svc.preCookieName, Value: preCookie})
			rr := do(h, r, jar)
			if tt.method == "DELETE" && rr.Code == http.StatusMethodNotAllowed {
				t.Fatal("route missing")
			}
			if rr.Code != tt.want {
				t.Fatalf("status %d, want %d (%s)", rr.Code, tt.want, rr.Body)
			}
		})
	}
}

func TestSessionExpiryAndTouch(t *testing.T) {
	e := newEnv(t, nil)
	h := e.handler()
	me := func(jar map[string]string) int {
		return do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar).Code
	}

	jar, _ := e.localSession(t)
	if me(jar) != 200 {
		t.Fatal("fresh session rejected")
	}
	e.clock.Add(30 * time.Second)
	me(jar)
	if e.st.sess.touches != 0 {
		t.Fatalf("touched within a minute (%d)", e.st.sess.touches)
	}
	e.clock.Add(time.Minute)
	me(jar)
	if e.st.sess.touches != 1 {
		t.Fatalf("touches = %d, want 1", e.st.sess.touches)
	}
	// Idle timeout.
	e.clock.Add(8*time.Hour + time.Minute)
	if me(jar) != 401 {
		t.Fatal("idle session accepted")
	}

	// Absolute timeout, even when active.
	jar, _ = e.localSession(t)
	for range 23 {
		e.clock.Add(time.Hour)
		if me(jar) != 200 {
			t.Fatal("active session expired early")
		}
	}
	e.clock.Add(time.Hour)
	if me(jar) != 401 {
		t.Fatal("session outlived the absolute timeout")
	}
}

func TestSessionMaxPerUser(t *testing.T) {
	e := newEnv(t, nil)
	for range 15 {
		e.localSession(t)
		e.clock.Add(time.Second)
	}
	if n := e.st.sess.count("local:alice"); n != e.cfg.Auth.Session.MaxPerUser {
		t.Fatalf("%d sessions, want %d", n, e.cfg.Auth.Session.MaxPerUser)
	}
}

func TestLocalSessionDroppedWhenUserDisabled(t *testing.T) {
	e := newEnv(t, nil)
	h := e.handler()
	jar, _ := e.localSession(t)
	content := strings.Replace(usersYAML(t), "groups: [platform, oncall]", "groups: [platform]\n    disabled: true", 1)
	if err := os.WriteFile(e.cfg.Auth.Local.UsersFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	// Even before the reload revokes it, a request re-checks the user; here
	// the swap happens directly so both paths are covered.
	if err := e.svc.loadUsers(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if rr := do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar); rr.Code != 401 {
		t.Fatalf("disabled user's session accepted: %d", rr.Code)
	}
}

func TestInsecureCookieNamesOnHTTP(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { c.PublicURL = "http://localhost:5173" })
	if e.svc.cookieName != "eddy_session" || e.svc.preCookieName != "eddy_pre" {
		t.Fatalf("cookie names %q %q", e.svc.cookieName, e.svc.preCookieName)
	}
	rr := httptest.NewRecorder()
	e.svc.setSessionCookie(rr, "x")
	if c := rr.Result().Cookies()[0]; c.Secure || !c.HttpOnly {
		t.Fatalf("cookie %+v", c)
	}
}

func TestProvidersRoute(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { enableProxy(t, c) })
	rr := do(e.handler(), httptest.NewRequest("GET", "/auth/providers", nil), nil)
	if strings.TrimSpace(rr.Body.String()) != `{"local":true,"proxy":true,"dev":false}` {
		t.Fatalf("providers %s", rr.Body)
	}
}

func TestNewConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*config.Hub)
	}{
		{"no key file", func(c *config.Hub) { c.Auth.KeyFile = "" }},
		{"missing users file", func(c *config.Hub) { c.Auth.Local.UsersFile = "/nonexistent/users.yaml" }},
		{"proxy without cidrs", func(c *config.Hub) { enableProxy(t, c); c.Auth.Proxy.TrustedCIDRs = nil }},
		{"proxy bad cidr", func(c *config.Hub) { enableProxy(t, c); c.Auth.Proxy.TrustedCIDRs = []string{"nope"} }},
		{"proxy short secret", func(c *config.Hub) { enableProxy(t, c); t.Setenv("EDDY_PROXY_SECRET", "short") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := baseConfig(t, usersYAML(t))
			tt.mut(cfg)
			if _, err := New(cfg, newFakeStore(), nil, discardLog()); err == nil {
				t.Fatal("New succeeded")
			}
		})
	}
}

func TestRequireCSRFLoginUsesPreSession(t *testing.T) {
	e := newEnv(t, nil)
	jar, sessTok := e.localSession(t)
	cookie, preTok, _ := e.svc.newPreSession(e.clock.Now())
	var called bool
	h := e.svc.Authenticate(e.svc.RequireCSRF(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })))
	for _, tc := range []struct {
		tok  string
		want bool
	}{{sessTok, false}, {preTok, true}} {
		called = false
		r := httptest.NewRequest("POST", loginPath, nil)
		r.Header.Set("Origin", testPublicURL)
		r.Header.Set("X-Eddy-CSRF", tc.tok)
		r.AddCookie(&http.Cookie{Name: e.svc.preCookieName, Value: cookie})
		do(h, r, jar)
		if called != tc.want {
			t.Fatalf("token accepted=%v, want %v", called, tc.want)
		}
	}
}
