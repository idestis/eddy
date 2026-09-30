package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/store"
)

const (
	loginPath    = "/auth/local/login"
	maxAuthBody  = 4 << 10
	genericLogin = "invalid username or password"
)

// writeError writes the docs/api.md error shape.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ValidReturnTo reports whether s is a safe post-login redirect target: a
// local path that starts with "/" but not "//" or "/\", with no scheme,
// host or control characters.
func ValidReturnTo(s string) bool {
	if len(s) == 0 || len(s) > 2048 || s[0] != '/' {
		return false
	}
	if len(s) > 1 && (s[1] == '/' || s[1] == '\\') {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return false
	}
	return true
}

// Routes registers the /auth endpoints. The handlers check CSRF and Origin
// themselves, so wrapping them in RequireCSRF as well is harmless. Wrap
// them in Authenticate so logout sees the session.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/csrf", s.handleCSRF)
	mux.HandleFunc("GET /auth/providers", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, s.Providers())
	})
	mux.HandleFunc("POST "+loginPath, s.handleLogin)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
	s.devRoutes(mux)
}

func (s *Service) handleCSRF(w http.ResponseWriter, _ *http.Request) {
	cookie, token, err := s.newPreSession(s.now())
	if err != nil {
		s.log.Error("pre-session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.preCookieName,
		Value:    cookie,
		Path:     "/",
		Secure:   s.secureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(preSessionTTL / time.Second),
	})
	writeJSON(w, http.StatusOK, map[string]string{"csrf": token})
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	ReturnTo string `json:"returnTo,omitempty"`
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !s.cfg.Auth.Local.Enabled {
		writeError(w, http.StatusNotFound, "not_found", "local login is not enabled")
		return
	}
	if !s.checkOrigin(r) || !s.checkPreSession(r, s.now()) {
		writeError(w, http.StatusForbidden, "forbidden", "missing or invalid CSRF token or origin")
		return
	}
	if !s.limiter.allowIP(peerIP(r)) {
		writeError(w, http.StatusTooManyRequests, "rate_limited", "too many login attempts, try again later")
		return
	}
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid login request")
		return
	}
	if req.ReturnTo != "" && !ValidReturnTo(req.ReturnTo) {
		writeError(w, http.StatusBadRequest, "bad_request", "returnTo must be a local path")
		return
	}
	logName := truncate(req.Username, 64)
	deny := func(reason string) {
		s.rec.Record(ctx, identity.Principal{Via: identity.ViaWeb, Provider: ProviderLocal}, "login", store.ResourceRef{},
			store.AuditDenied, map[string]string{"provider": ProviderLocal, "username": logName, "reason": reason})
		writeError(w, http.StatusUnauthorized, "unauthorized", genericLogin)
	}
	if s.limiter.locked(req.Username) {
		deny("locked out")
		return
	}
	var (
		u  *User
		ph *passwordHash
	)
	pw := req.Password
	if usernameRE.MatchString(req.Username) {
		if us := s.users.Load(); us != nil {
			u = us.byName[req.Username]
		}
	}
	if u != nil && !u.Disabled && len(pw) <= maxPasswordBytes {
		ph = &u.hash
	}
	if len(pw) > maxPasswordBytes {
		pw = pw[:maxPasswordBytes]
	}
	ok, err := s.hasher.verify(ctx, ph, pw)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "internal", "login unavailable, try again")
		return
	}
	if !ok {
		if s.limiter.fail(req.Username) {
			s.log.Warn("login lockout", "username", logName, "peer", peerIP(r))
		}
		deny("bad credentials")
		return
	}
	s.limiter.success(req.Username)

	// Rotate: any session the browser already had is discarded.
	if c, err := r.Cookie(s.cookieName); err == nil {
		s.deleteSession(ctx, c.Value)
	}
	groups := s.mapper.Groups(u.Groups, u.Username, u.Subject)
	raw, err := s.createSession(ctx, r, u.Subject, u.Username, ProviderLocal, groups)
	if err != nil {
		s.log.Error("create session failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.setSessionCookie(w, raw)
	s.clearCookie(w, s.preCookieName)
	p := identity.Principal{User: u.Subject, Groups: groups, Display: u.Username, Provider: ProviderLocal, Via: identity.ViaWeb}
	s.rec.Record(ctx, p, "login", store.ResourceRef{}, store.AuditOK, map[string]string{"provider": ProviderLocal, "username": logName})
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	raw, _ := ctx.Value(sessionCtxKey{}).(string)
	if !s.checkOrigin(r) || (raw != "" && !ctEqualString(r.Header.Get(csrfHeader), s.csrfFor(raw))) {
		writeError(w, http.StatusForbidden, "forbidden", "missing or invalid CSRF token or origin")
		return
	}
	if raw == "" {
		// No valid session was resolved (or Authenticate did not run):
		// still delete whatever the cookie names so nothing lingers.
		if c, err := r.Cookie(s.cookieName); err == nil {
			raw = c.Value
		}
	}
	if raw != "" {
		s.deleteSession(ctx, raw)
	}
	s.clearCookie(w, s.cookieName)
	if p, ok := identity.From(ctx); ok {
		s.forgetRecent(p.User)
		s.rec.Record(ctx, p, "logout", store.ResourceRef{}, store.AuditOK, nil)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// --- /api/v1/tokens --------------------------------------------------------

type tokenItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func toItem(t store.Token) tokenItem {
	sc := t.Scopes
	if sc == nil {
		sc = []string{}
	}
	return tokenItem{ID: t.ID, Name: t.Name, Scopes: sc, CreatedAt: t.CreatedAt, ExpiresAt: t.ExpiresAt, LastUsedAt: t.LastUsedAt}
}

// parseTTL accepts Go durations ("720h") and whole days ("30d").
func parseTTL(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	if d, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(d)
		if err != nil || n <= 0 || n > 3650 {
			return 0, fmt.Errorf("%w: invalid ttl %q", ErrBadRequest, truncate(s, 32))
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%w: invalid ttl %q", ErrBadRequest, truncate(s, 32))
	}
	return d, nil
}

// TokenRoutes registers the PAT management API. The caller applies
// Authenticate, RequireUser and RequireCSRF. PAT-authenticated principals
// are refused: tokens are managed from a browser session only.
func (s *Service) TokenRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/tokens", s.handleListTokens)
	mux.HandleFunc("POST /api/v1/tokens", s.handleCreateToken)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.handleRevokeToken)
}

func (s *Service) webPrincipal(w http.ResponseWriter, r *http.Request) (identity.Principal, bool) {
	p, ok := identity.From(r.Context())
	if !ok || p.Via != identity.ViaWeb || p.User == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
		return identity.Principal{}, false
	}
	return p, true
}

func (s *Service) handleListTokens(w http.ResponseWriter, r *http.Request) {
	p, ok := s.webPrincipal(w, r)
	if !ok {
		return
	}
	ts, err := s.ListTokens(r.Context(), p.User)
	if err != nil {
		s.log.Error("list tokens failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	items := make([]tokenItem, 0, len(ts))
	for _, t := range ts {
		items = append(items, toItem(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createTokenRequest struct {
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
	TTL    string   `json:"ttl"`
}

func (s *Service) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.webPrincipal(w, r)
	if !ok {
		return
	}
	var req createTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid token request")
		return
	}
	ttl, err := parseTTL(req.TTL)
	if err == nil {
		var tok string
		var t store.Token
		tok, t, err = s.Issue(ctx, p, req.Name, req.Scopes, ttl)
		if err == nil {
			s.rec.Record(ctx, p, "token.create", store.ResourceRef{}, store.AuditOK, map[string]any{
				"id": t.ID, "name": t.Name, "scopes": t.Scopes, "expiresAt": t.ExpiresAt,
			})
			writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "item": toItem(t)})
			return
		}
	}
	switch {
	case errors.Is(err, ErrBadRequest):
		writeError(w, http.StatusBadRequest, "bad_request", strings.TrimPrefix(err.Error(), ErrBadRequest.Error()+": "))
	case errors.Is(err, ErrTokenLimit):
		s.rec.Record(ctx, p, "token.create", store.ResourceRef{}, store.AuditDenied, map[string]string{"reason": "limit"})
		writeError(w, http.StatusConflict, "conflict", strings.TrimPrefix(err.Error(), ErrTokenLimit.Error()+": "))
	default:
		s.log.Error("create token failed", "err", err)
		s.rec.Record(ctx, p, "token.create", store.ResourceRef{}, store.AuditError, nil)
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func (s *Service) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.webPrincipal(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	err := s.RevokeToken(ctx, p.User, id)
	switch {
	case err == nil:
		s.rec.Record(ctx, p, "token.revoke", store.ResourceRef{}, store.AuditOK, map[string]string{"id": id})
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "token not found")
	default:
		s.log.Error("revoke token failed", "err", err)
		s.rec.Record(ctx, p, "token.revoke", store.ResourceRef{}, store.AuditError, map[string]string{"id": truncate(id, 12)})
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
	}
}
