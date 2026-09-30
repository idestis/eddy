//go:build !dev

package auth

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eddy-gitops/eddy/internal/config"
)

func TestFakeLoginRefusedWithoutDevBuild(t *testing.T) {
	t.Setenv("EDDY_DEV_MODE", "1")
	cfg := baseConfig(t, usersYAML(t))
	cfg.Dev.FakeLogin = true
	cfg.Listen.UI = "127.0.0.1:8080"
	_, err := New(cfg, newFakeStore(), nil, discardLog())
	if err == nil || !strings.Contains(err.Error(), "-tags dev") {
		t.Fatalf("New = %v, want refusal", err)
	}
}

func TestNoDevRoute(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) {})
	rr := do(e.handler(), httptest.NewRequest("GET", "/auth/dev/login?user=x", nil), nil)
	if rr.Code != 404 {
		t.Fatalf("dev route present in a release build: %d", rr.Code)
	}
	if e.svc.Providers().Dev {
		t.Fatal("providers report dev")
	}
}
