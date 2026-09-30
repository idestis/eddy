package hub

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

func TestRegistryVerify(t *testing.T) {
	cur, prev := sha256.Sum256([]byte("current-token")), sha256.Sum256([]byte("previous-token"))
	r := NewRegistry()
	r.replace(map[string]clusterEntry{
		"prod":    {spec: ClusterSpec{Name: "prod"}, tokens: [][32]byte{cur, prev}},
		"notoken": {spec: ClusterSpec{Name: "notoken"}},
	})
	tests := []struct {
		name, cluster, token string
		want                 bool
	}{
		{"current token", "prod", "current-token", true},
		{"previous token during rotation", "prod", "previous-token", true},
		{"wrong token", "prod", "current-token-x", false},
		{"empty token", "prod", "", false},
		{"token of another cluster", "notoken", "current-token", false},
		{"unknown cluster takes the dummy path", "nope", "current-token", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, ok := r.Verify(tc.cluster, tc.token)
			if ok != tc.want {
				t.Fatalf("Verify = %v, want %v", ok, tc.want)
			}
			if h != sha256.Sum256([]byte(tc.token)) {
				t.Fatal("Verify must return the token hash")
			}
		})
	}
	// Rotation completes: the previous token stops working.
	r.replace(map[string]clusterEntry{"prod": {spec: ClusterSpec{Name: "prod"}, tokens: [][32]byte{cur}}})
	if r.Accepts("prod", prev) {
		t.Fatal("previous token still accepted after rotation")
	}
}

func TestHashTokenTrimsWhitespace(t *testing.T) {
	a, _ := hashToken([]byte("tok\n"))
	b, _ := hashToken([]byte("tok"))
	if a != b {
		t.Fatal("trailing newline must not change the token")
	}
	if _, ok := hashToken([]byte(" \n")); ok {
		t.Fatal("blank token accepted")
	}
}

func TestAgentAuth(t *testing.T) {
	e := newEnv(t, "")
	tests := []struct {
		name, cluster, header string
		want                  int
	}{
		{"no header", "dev", "", http.StatusUnauthorized},
		{"wrong scheme", "dev", "Basic " + testToken, http.StatusUnauthorized},
		{"wrong token", "dev", "Bearer nope", http.StatusUnauthorized},
		{"token of another cluster", "prod", "Bearer " + testToken, http.StatusUnauthorized},
		{"unknown cluster", "ghost", "Bearer " + testToken, http.StatusUnauthorized},
		{"good token without upgrade", "dev", "Bearer " + testToken, http.StatusUpgradeRequired},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", e.agents.URL+"/agent/v1/connect?cluster="+tc.cluster, nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	t.Run("websocket with a good token", func(t *testing.T) {
		a := e.connectAgent("dev", testToken, nil)
		defer a.close()
	})
}

func TestAgentOnlyRoutes(t *testing.T) {
	e := newEnv(t, "")
	for path, want := range map[string]int{"/healthz": 200, "/api/v1/me": 404, "/": 404, "/mcp": 404} {
		resp, err := http.Get(e.agents.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestAgentAuthRateLimit(t *testing.T) {
	e := newEnv(t, "")
	get := func(tok string) int {
		req, _ := http.NewRequest("GET", e.agents.URL+"/agent/v1/connect?cluster=dev", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := range agentAuthFailures {
		if got := get("bad"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i, got)
		}
	}
	if got := get("bad"); got != http.StatusTooManyRequests {
		t.Fatalf("after %d failures: status %d, want 429", agentAuthFailures, got)
	}
	// Even the right token is refused while the IP is blocked.
	if got := get(testToken); got != http.StatusTooManyRequests {
		t.Fatalf("good token while blocked: status %d, want 429", got)
	}
}

func TestValidateHello(t *testing.T) {
	tests := []struct {
		name  string
		hello protocol.Hello
		ok    bool
	}{
		{"same version", protocol.Hello{Protocol: protocol.Version, Cluster: "dev"}, true},
		{"same major, newer minor", protocol.Hello{Protocol: protocol.Version + ".3", Cluster: "dev"}, true},
		{"other major", protocol.Hello{Protocol: "2", Cluster: "dev"}, false},
		{"missing version", protocol.Hello{Cluster: "dev"}, false},
		{"cluster mismatch", protocol.Hello{Protocol: protocol.Version, Cluster: "prod"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.hello
			if err := validateHello(&h, "dev"); (err == nil) != tc.ok {
				t.Fatalf("validateHello = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	h := protocol.Hello{Protocol: protocol.Version, Cluster: "dev", AgentVersion: "v1\n\x00" + strings.Repeat("x", 500)}
	if err := validateHello(&h, "dev"); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(h.AgentVersion, "\n\x00") || len(h.AgentVersion) > maxHelloFieldLength {
		t.Fatalf("hello field not cleaned: %q", h.AgentVersion)
	}
}

func TestHelloMismatchClosesConnection(t *testing.T) {
	e := newEnv(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, e.agentURL("dev"), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": []string{"Bearer " + testToken}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	a := &fakeAgent{conn: conn}
	a.sendFrame(protocol.TypeHello, "", protocol.Hello{Protocol: protocol.Version, Cluster: "prod"})
	_, _, err = conn.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("read error %v, want policy violation close", err)
	}
	if e.hub.agents.get("dev") != nil {
		t.Fatal("session registered despite a bad hello")
	}
}

func TestNewConnectionReplacesOld(t *testing.T) {
	e := newEnv(t, "")
	first := e.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-a", "one", "ready")})
	old := e.hub.agents.get("dev")
	second := e.connectAgent("dev", testToken, []model.Resource{res("Kustomization", "team-a", "a", "ready"), res("Kustomization", "team-a", "b", "ready")})
	_ = second
	if e.hub.agents.get("dev") == old {
		t.Fatal("old session still current")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := first.conn.Read(ctx); err != nil {
			break
		}
	}
	if got := e.hub.metrics.agentsConnected.Load(); got != 1 {
		t.Fatalf("agentsConnected = %d, want 1", got)
	}
}

func TestSessionClosedWhenTokenRevoked(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, nil)
	// The Secret is rotated without keeping the old token.
	e.hub.reg.replace(map[string]clusterEntry{
		"dev": {spec: ClusterSpec{Name: "dev"}, tokens: [][32]byte{sha256.Sum256([]byte("new"))}},
	})
	waitFor(t, func() bool { return e.hub.agents.get("dev") == nil })
}

func TestAgentHandlerOtherMethods(t *testing.T) {
	srv := httptest.NewServer((&agentServer{reg: NewRegistry(), agents: newAgents(newBus(), newMetrics()),
		failures: newWindowLimiter(1, time.Minute), metrics: newMetrics(), log: quietLog(), base: context.Background()}).handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/agent/v1/connect", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("POST connect: status %d", resp.StatusCode)
	}
}
