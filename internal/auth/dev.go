//go:build dev

package auth

import (
	"net/http"
	"net/netip"
	"strings"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// devBuild is true only in binaries built with -tags dev. Release images
// are built without it, so the fake login route does not exist there.
const devBuild = true

func (s *Service) devRoutes(mux *http.ServeMux) {
	if !s.devActive {
		return
	}
	mux.HandleFunc("GET /auth/dev/login", s.handleDevLogin)
}

// handleDevLogin mints a session for any user: GET /auth/dev/login?user=&groups=a,b&returnTo=/.
// Groups go through the normal mapping.
func (s *Service) handleDevLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.devActive || !loopbackPeer(r.RemoteAddr) {
		writeError(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	q := r.URL.Query()
	user := q.Get("user")
	if user == "" {
		user = "dev"
	}
	subject, err := s.mapper.DevUser(user)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid user")
		return
	}
	var raw []string
	if g := q.Get("groups"); g != "" {
		raw = strings.Split(g, ",")
	}
	groups := s.mapper.Groups(raw, user, subject)
	if c, err := r.Cookie(s.cookieName); err == nil {
		s.deleteSession(ctx, c.Value)
	}
	sid, err := s.createSession(ctx, r, subject, user, ProviderDev, groups)
	if err != nil {
		s.log.Error("create dev session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.setSessionCookie(w, sid)
	p := identity.Principal{User: subject, Groups: groups, Display: user, Provider: ProviderDev, Via: identity.ViaWeb}
	s.rec.Record(ctx, p, "login", store.ResourceRef{}, store.AuditOK, map[string]string{"provider": ProviderDev})
	s.log.Warn("dev fake login", "user", subject)
	to := q.Get("returnTo")
	if !ValidReturnTo(to) {
		to = "/"
	}
	http.Redirect(w, r, to, http.StatusFound)
}

func loopbackPeer(remoteAddr string) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	return err == nil && ap.Addr().Unmap().IsLoopback()
}
