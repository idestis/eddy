//go:build dev

package auth

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/eddy-gitops/eddy/internal/config"
)

func TestDevActivation(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		listen  string
		wantErr string
	}{
		{name: "all conditions", env: "1", listen: "127.0.0.1:8080"},
		{name: "localhost", env: "1", listen: "localhost:8080"},
		{name: "ipv6 loopback", env: "1", listen: "[::1]:8080"},
		{name: "no env", env: "", listen: "127.0.0.1:8080", wantErr: "EDDY_DEV_MODE"},
		{name: "env not 1", env: "true", listen: "127.0.0.1:8080", wantErr: "EDDY_DEV_MODE"},
		{name: "all interfaces", env: "1", listen: ":8080", wantErr: "loopback"},
		{name: "public ip", env: "1", listen: "0.0.0.0:8080", wantErr: "loopback"},
		{name: "hostname", env: "1", listen: "eddy.local:8080", wantErr: "loopback"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("EDDY_DEV_MODE", tt.env)
			cfg := baseConfig(t, usersYAML(t))
			cfg.Dev.FakeLogin = true
			cfg.Listen.UI = tt.listen
			s, err := New(cfg, newFakeStore(), nil, discardLog())
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("New = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !s.Providers().Dev || !s.DevMode() {
				t.Fatalf("New = %v, dev=%v", err, err == nil && s.Providers().Dev)
			}
		})
	}
}

func TestDevLogin(t *testing.T) {
	t.Setenv("EDDY_DEV_MODE", "1")
	e := newEnv(t, func(c *config.Hub) { c.Dev.FakeLogin = true; c.Listen.UI = "127.0.0.1:8080" })
	h := e.handler()
	jar := map[string]string{}
	rr := do(h, httptest.NewRequest("GET", "/auth/dev/login?user=carol@example.com&groups=platform,system:masters&returnTo=//evil", nil), jar)
	if rr.Code != 302 || rr.Header().Get("Location") != "/" {
		t.Fatalf("dev login: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	m := decodeMe(t, do(h, httptest.NewRequest("GET", "/api/v1/me", nil), jar))
	if m.User != "dev:carol@example.com" || m.Provider != ProviderDev || !slices.Equal(m.Groups, []string{"eddy:authenticated", "eddy:platform"}) {
		t.Fatalf("me = %+v", m)
	}
	if rr := do(h, httptest.NewRequest("GET", "/auth/dev/login?user=system:admin", nil), nil); rr.Code != 400 {
		t.Fatalf("system user: %d", rr.Code)
	}
	r := httptest.NewRequest("GET", "/auth/dev/login?user=x", nil)
	r.RemoteAddr = "192.0.2.10:1"
	if rr := do(h, r, nil); rr.Code != 404 {
		t.Fatalf("non-loopback peer: %d", rr.Code)
	}
}

func TestDevLoginNotRegisteredWhenOff(t *testing.T) {
	e := newEnv(t, nil)
	if rr := do(e.handler(), httptest.NewRequest("GET", "/auth/dev/login", nil), nil); rr.Code != 404 {
		t.Fatalf("dev route registered while fakeLogin is off: %d", rr.Code)
	}
}
