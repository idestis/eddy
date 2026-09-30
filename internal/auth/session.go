package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

const (
	// sessionCacheTTL is how long a session read from the store is trusted
	// without asking the store again (ADR-0004).
	sessionCacheTTL   = 30 * time.Second
	sessionCacheMax   = 10000
	sessionIDBytes    = 32
	touchEvery        = time.Minute
	preSessionTTL     = 10 * time.Minute
	recentProxyWindow = time.Minute
)

type sessionCtxKey struct{}

// newSessionID returns the cookie value and its storage hash.
func newSessionID() (string, []byte, error) {
	b := make([]byte, sessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("auth: session id: %w", err)
	}
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(b), h[:], nil
}

// sessionHash validates a cookie value and returns sha256 of the raw id.
func sessionHash(raw string) ([]byte, bool) {
	if len(raw) != base64.RawURLEncoding.EncodedLen(sessionIDBytes) {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) != sessionIDBytes {
		return nil, false
	}
	h := sha256.Sum256(b)
	return h[:], true
}

func (s *Service) sessionExpiry(created, seen time.Time) time.Time {
	idle := seen.Add(s.cfg.Auth.Session.IdleTimeout.Duration)
	abs := created.Add(s.cfg.Auth.Session.AbsoluteTimeout.Duration)
	if abs.Before(idle) {
		return abs
	}
	return idle
}

// createSession stores a new session and returns its cookie value.
func (s *Service) createSession(ctx context.Context, r *http.Request, subject, display, provider string, groups []string) (string, error) {
	raw, h, err := newSessionID()
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	sess := store.Session{
		IDHash:     h,
		Subject:    subject,
		Display:    display,
		Groups:     groups,
		Provider:   provider,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  s.sessionExpiry(now, now),
		UserAgent:  truncate(r.UserAgent(), 256),
		RemoteIP:   peerIP(r),
	}
	if err := s.st.Sessions().Create(ctx, sess); err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	if s.cfg.Auth.Session.MaxPerUser > 0 {
		if err := s.st.Sessions().DeleteOldestBySubject(ctx, subject, s.cfg.Auth.Session.MaxPerUser); err != nil {
			s.log.Error("enforce session maxPerUser failed", "user", subject, "err", err)
		} else {
			// Replicas may cache a session that was just trimmed.
			s.revoked(ctx, subject)
		}
	}
	return raw, nil
}

// errSessionStore reports that the session could not be checked because the
// store failed and no cached copy was fresh enough. Authenticate answers
// 503 rather than treating the user as signed out.
var errSessionStore = errors.New("auth: session store unavailable")

// readSession returns the session named by the request cookie, if valid.
// A session read from the store is cached for sessionCacheTTL, so a short
// store outage does not sign everyone out; revocations reach the cache
// through Invalidate (fed by store events on every replica).
func (s *Service) readSession(ctx context.Context, r *http.Request, now time.Time) (string, store.Session, bool, error) {
	c, err := r.Cookie(s.cookieName)
	if err != nil {
		return "", store.Session{}, false, nil
	}
	h, ok := sessionHash(c.Value)
	if !ok {
		return "", store.Session{}, false, nil
	}
	sess, ok := s.sessions.get(h, now)
	if !ok {
		sess, err = s.st.Sessions().Get(ctx, h, now)
		if err != nil {
			s.sessions.drop(h)
			if errors.Is(err, store.ErrNotFound) {
				return "", store.Session{}, false, nil
			}
			s.log.Error("session lookup failed", "err", err)
			return "", store.Session{}, false, errSessionStore
		}
		s.sessions.put(h, sess, now)
	}
	// The store already filters expired sessions; check again so a lax
	// backend (or a cached copy) cannot extend a session.
	if !now.Before(sess.ExpiresAt) || !now.Before(s.sessionExpiry(sess.CreatedAt, sess.LastSeenAt)) {
		s.sessions.drop(h)
		return "", store.Session{}, false, nil
	}
	return c.Value, sess, true, nil
}

func (s *Service) deleteSession(ctx context.Context, raw string) {
	h, ok := sessionHash(raw)
	if !ok {
		return
	}
	subject := s.sessions.subject(h)
	s.sessions.drop(h)
	if err := s.st.Sessions().Delete(ctx, h); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("delete session failed", "err", err)
	}
	if subject != "" {
		s.revoked(ctx, subject)
	}
}

// revoked drops the subject's cached sessions here and tells the other
// replicas to do the same (store.EventRevoke).
func (s *Service) revoked(ctx context.Context, subject string) {
	s.sessions.dropSubject(subject)
	ev := s.st.Events()
	if ev == nil {
		return
	}
	if err := ev.Publish(ctx, store.Event{Kind: store.EventRevoke, ID: subject}); err != nil {
		s.log.Warn("publishing a session revocation failed; other replicas may accept the session for up to 30s",
			"user", subject, "err", err)
	}
}

// Invalidate drops cached sessions after a store.EventRevoke: id is a
// subject ("" drops everything, for store.EventResync). The hub calls it
// for every revoke event, including this replica's own.
func (s *Service) Invalidate(id string) {
	if id == "" {
		s.sessions.clear()
		return
	}
	s.sessions.dropSubject(id)
}

// touch extends the idle timeout at most once a minute.
func (s *Service) touch(ctx context.Context, raw string, sess store.Session, now time.Time) {
	if now.Sub(sess.LastSeenAt) < touchEvery {
		return
	}
	h, ok := sessionHash(raw)
	if !ok {
		return
	}
	exp := s.sessionExpiry(sess.CreatedAt, now).UTC()
	if err := s.st.Sessions().Touch(ctx, h, now.UTC(), exp); err != nil {
		s.log.Error("touch session failed", "err", err)
		return
	}
	s.sessions.touched(h, now.UTC(), exp)
}

func (s *Service) setSessionCookie(w http.ResponseWriter, raw string) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    raw,
		Path:     "/",
		Secure:   s.secureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Service) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		Secure:   s.secureCookies,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// csrfFor derives the CSRF token of a session; it is never stored.
func (s *Service) csrfFor(raw string) string {
	return base64.RawURLEncoding.EncodeToString(hmacSHA256(s.keys.csrf, "csrf:", raw))
}

// CSRFToken returns the CSRF token of the request's session ("" if none).
// GET /api/v1/me returns it to the SPA.
func (s *Service) CSRFToken(r *http.Request) string {
	raw, _ := r.Context().Value(sessionCtxKey{}).(string)
	if raw == "" {
		return ""
	}
	return s.csrfFor(raw)
}

// sessionPrincipal re-validates a stored session and builds its principal.
// Local users' groups are re-resolved from users.yaml on every request.
func (s *Service) sessionPrincipal(ctx context.Context, w http.ResponseWriter, raw string, sess store.Session, now time.Time) (identity.Principal, bool) {
	invalidate := func(reason string) (identity.Principal, bool) {
		s.log.Info("dropping session", "user", sess.Subject, "provider", sess.Provider, "reason", reason)
		s.deleteSession(ctx, raw)
		s.clearCookie(w, s.cookieName)
		return identity.Principal{}, false
	}
	if _, err := s.mapper.checkSubject(sess.Subject); err != nil {
		return invalidate("subject no longer allowed")
	}
	p := identity.Principal{
		User:     sess.Subject,
		Display:  sess.Display,
		Provider: sess.Provider,
		Via:      identity.ViaWeb,
		Scopes:   []identity.Scope{identity.ScopeRead, identity.ScopeOperate},
	}
	switch sess.Provider {
	case ProviderLocal:
		if !s.cfg.Auth.Local.Enabled {
			return invalidate("local auth disabled")
		}
		u := s.localUser(sess.Subject)
		if u == nil {
			return invalidate("local user missing or disabled")
		}
		p.Groups = s.mapper.Groups(u.Groups, u.Username, u.Subject)
	case ProviderDev:
		if !s.devActive {
			return invalidate("dev login inactive")
		}
		p.Groups = s.mapper.validPrincipalGroups(sess.Groups)
	case ProviderProxy:
		// Proxy sessions are valid only together with matching, trusted
		// identity headers (handled in proxySession).
		return invalidate("proxy session without trusted proxy identity")
	default:
		return invalidate("unknown provider")
	}
	s.touch(ctx, raw, sess, now)
	return p, true
}

// --- pre-session CSRF for login -------------------------------------------

// newPreSession returns the signed pre-session cookie value and its CSRF token.
func (s *Service) newPreSession(now time.Time) (cookie, token string, err error) {
	n := make([]byte, 16)
	if _, err := rand.Read(n); err != nil {
		return "", "", fmt.Errorf("auth: pre-session nonce: %w", err)
	}
	nonce := base64.RawURLEncoding.EncodeToString(n)
	exp := strconv.FormatInt(now.Add(preSessionTTL).Unix(), 10)
	sig := base64.RawURLEncoding.EncodeToString(hmacSHA256(s.keys.preSession, "pre:", nonce, ".", exp))
	return nonce + "." + exp + "." + sig, s.preToken(nonce), nil
}

func (s *Service) preToken(nonce string) string {
	return base64.RawURLEncoding.EncodeToString(hmacSHA256(s.keys.preSession, "pre-csrf:", nonce))
}

// checkPreSession verifies the pre-session cookie signature and expiry and
// that the header token matches it.
func (s *Service) checkPreSession(r *http.Request, now time.Time) bool {
	c, err := r.Cookie(s.preCookieName)
	if err != nil {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 3 {
		return false
	}
	nonce, exp, sig := parts[0], parts[1], parts[2]
	want := hmacSHA256(s.keys.preSession, "pre:", nonce, ".", exp)
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, want) {
		return false
	}
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || !now.Before(time.Unix(e, 0)) {
		return false
	}
	return ctEqualString(r.Header.Get(csrfHeader), s.preToken(nonce))
}

// --- CSRF middleware -------------------------------------------------------

const csrfHeader = "X-Eddy-CSRF"

func isUnsafe(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	}
	return true
}

// checkOrigin requires Origin to equal the publicURL origin, or
// Sec-Fetch-Site: same-origin when Origin is absent. A Sec-Fetch-Site other
// than same-origin is always rejected.
func (s *Service) checkOrigin(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	sfs := r.Header.Values("Sec-Fetch-Site")
	if len(origins) > 1 || len(sfs) > 1 {
		return false
	}
	if len(sfs) == 1 && sfs[0] != "same-origin" {
		return false
	}
	if len(origins) == 1 {
		return strings.EqualFold(strings.TrimSpace(origins[0]), s.origin) && origins[0] != "null"
	}
	return len(sfs) == 1
}

// RequireCSRF rejects unsafe requests without a valid X-Eddy-CSRF header
// and a same-origin Origin/Sec-Fetch-Site. With a session the header must
// equal the session's token; the login route instead uses the pre-session
// token. Apply it after Authenticate.
func (s *Service) RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isUnsafe(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.csrfOK(r) {
			writeError(w, http.StatusForbidden, "forbidden", "missing or invalid CSRF token or origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) csrfOK(r *http.Request) bool {
	if !s.checkOrigin(r) {
		return false
	}
	// Login always uses the pre-session token, even when the browser still
	// holds a session (re-login rotates it).
	if r.URL.Path == loginPath {
		return s.checkPreSession(r, s.now())
	}
	if raw, _ := r.Context().Value(sessionCtxKey{}).(string); raw != "" {
		return ctEqualString(r.Header.Get(csrfHeader), s.csrfFor(raw))
	}
	return false
}

// RequireUser answers 401 unless Authenticate put a principal in the context.
func (s *Service) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := identity.From(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "sign in required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ctEqualString compares in constant time (length leaks only via sha256).
func ctEqualString(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// peerIP is the TCP peer address. X-Forwarded-For is never consulted.
func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func equalGroups(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// --- session cache -----------------------------------------------------------

// sessionCache holds sessions read from the store for sessionCacheTTL. It is
// bounded; when full, stale entries go first, then everything.
type sessionCache struct {
	mu sync.Mutex
	m  map[string]cachedSession // key: string(sha256 of the session id)
}

type cachedSession struct {
	sess store.Session
	at   time.Time
}

func newSessionCache() *sessionCache { return &sessionCache{m: map[string]cachedSession{}} }

func (c *sessionCache) get(h []byte, now time.Time) (store.Session, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[string(h)]
	if !ok || now.Sub(e.at) >= sessionCacheTTL || now.Before(e.at) {
		return store.Session{}, false
	}
	return e.sess, true
}

func (c *sessionCache) put(h []byte, sess store.Session, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= sessionCacheMax {
		c.gcLocked(now)
		if len(c.m) >= sessionCacheMax {
			clear(c.m)
		}
	}
	sess.Groups = slices.Clone(sess.Groups)
	c.m[string(h)] = cachedSession{sess: sess, at: now}
}

func (c *sessionCache) touched(h []byte, seen, expires time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[string(h)]; ok {
		e.sess.LastSeenAt, e.sess.ExpiresAt = seen, expires
		c.m[string(h)] = e
	}
}

func (c *sessionCache) subject(h []byte) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[string(h)].sess.Subject
}

func (c *sessionCache) drop(h []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, string(h))
}

func (c *sessionCache) dropSubject(subject string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if e.sess.Subject == subject {
			delete(c.m, k)
		}
	}
}

func (c *sessionCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.m)
}

func (c *sessionCache) gc(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gcLocked(now)
}

func (c *sessionCache) gcLocked(now time.Time) {
	for k, e := range c.m {
		if now.Sub(e.at) >= sessionCacheTTL || now.Before(e.at) {
			delete(c.m, k)
		}
	}
}
