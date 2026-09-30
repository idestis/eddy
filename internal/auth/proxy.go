package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/store"
)

// proxyIdentity is a user asserted by a trusted reverse proxy.
type proxyIdentity struct {
	subject string
	display string
	groups  []string
}

// recentProxySession lets concurrent first requests of one proxy user share
// a session instead of each minting its own (which would leave the SPA with
// a CSRF token for a session its cookie no longer names).
type recentProxySession struct {
	raw    string
	groups []string
	at     time.Time
}

var errProxyRejected = errors.New("auth: proxy identity rejected")

func isMCPPath(p string) bool { return p == "/mcp" || strings.HasPrefix(p, "/mcp/") }

// stripProxyHeaders removes every proxy identity header so nothing
// downstream can read an untrusted value.
func (s *Service) stripProxyHeaders(r *http.Request) {
	p := s.cfg.Auth.Proxy
	for _, h := range []string{p.UserHeader, p.GroupsHeader, p.SharedSecretHeader} {
		if h != "" {
			r.Header.Del(h)
		}
	}
}

// peerTrusted reports whether the TCP peer (r.RemoteAddr, never
// X-Forwarded-For) is inside trustedCIDRs.
func (s *Service) peerTrusted(remoteAddr string) bool {
	ap, err := netip.ParseAddrPort(remoteAddr)
	var a netip.Addr
	if err == nil {
		a = ap.Addr()
	} else if a, err = netip.ParseAddr(remoteAddr); err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	for _, p := range s.proxyCIDRs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// proxyAuth reads and always strips the proxy identity headers. It returns
// nil, nil when the request carries no trusted identity (headers from an
// untrusted peer or without the shared secret are ignored and logged), and
// errProxyRejected when a trusted proxy sent an identity that must not be
// used (several user headers, an invalid or denied user).
func (s *Service) proxyAuth(r *http.Request) (*proxyIdentity, error) {
	p := s.cfg.Auth.Proxy
	users := r.Header.Values(p.UserHeader)
	groupVals := r.Header.Values(p.GroupsHeader)
	secrets := r.Header.Values(p.SharedSecretHeader)
	s.stripProxyHeaders(r)

	if !p.Enabled || len(users) == 0 {
		return nil, nil
	}
	now := s.now()
	peer := peerIP(r)
	if !s.peerTrusted(r.RemoteAddr) {
		if s.proxyLog.allow(now) {
			s.log.Warn("stripped proxy identity headers from an untrusted peer", "peer", peer)
		}
		return nil, nil
	}
	if !p.InsecureSkipSharedSecret {
		if len(secrets) != 1 || !ctEqualBytes([]byte(secrets[0]), s.proxySecret) {
			if s.proxyLog.allow(now) {
				s.log.Warn("stripped proxy identity headers: shared secret missing or wrong", "peer", peer)
			}
			return nil, nil
		}
	}
	if len(users) > 1 {
		s.log.Warn("rejected proxy request with several user headers", "peer", peer, "count", len(users))
		return nil, errProxyRejected
	}
	value := strings.TrimSpace(users[0])
	subject, err := s.mapper.ProxyUser(value)
	if err != nil {
		s.log.Warn("rejected proxy user", "peer", peer, "reason", err.Error())
		return nil, errProxyRejected
	}
	var raw []string
	if p.GroupsHeader != "" {
		sep := p.GroupsSeparator
		if sep == "" {
			sep = ","
		}
		for _, v := range groupVals {
			raw = append(raw, strings.Split(v, sep)...)
		}
	}
	return &proxyIdentity{subject: subject, display: value, groups: s.mapper.Groups(raw, value, subject)}, nil
}

func ctEqualBytes(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	ha, hb := sha256.Sum256(a), sha256.Sum256(b)
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// Authenticate resolves the caller from proxy headers or the session cookie
// and stores the principal with identity.With. Unauthenticated requests
// pass through without one; use RequireUser to reject them. Proxy headers
// are never honoured on /mcp, where only PATs are accepted.
func (s *Service) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMCPPath(r.URL.Path) {
			s.stripProxyHeaders(r)
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		now := s.now()
		ident, perr := s.proxyAuth(r)
		raw, sess, hasSess := s.readSession(ctx, r, now)
		if perr != nil {
			if hasSess {
				s.deleteSession(ctx, raw)
				s.clearCookie(w, s.cookieName)
			}
			writeError(w, http.StatusUnauthorized, "unauthorized", "proxy identity rejected")
			return
		}
		var (
			p  identity.Principal
			ok bool
		)
		switch {
		case ident != nil:
			raw, p, ok = s.proxySession(ctx, w, r, ident, raw, sess, hasSess, now)
		case hasSess:
			p, ok = s.sessionPrincipal(ctx, w, raw, sess, now)
		}
		if ok {
			ctx = identity.With(ctx, p)
			ctx = context.WithValue(ctx, sessionCtxKey{}, raw)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// proxySession finds or mints the hub session of a proxy user. A session
// whose identity differs from the headers is dropped. Groups come from the
// headers on every request; when they change, the stored session is
// replaced under the same id so LatestGroups (used to shrink PAT groups)
// stays current and the CSRF token does not change.
func (s *Service) proxySession(ctx context.Context, w http.ResponseWriter, r *http.Request, id *proxyIdentity,
	raw string, sess store.Session, hasSess bool, now time.Time) (string, identity.Principal, bool) {
	p := identity.Principal{
		User:     id.subject,
		Groups:   id.groups,
		Display:  id.display,
		Provider: ProviderProxy,
		Via:      identity.ViaWeb,
		Scopes:   []identity.Scope{identity.ScopeRead, identity.ScopeOperate},
	}
	if hasSess && (sess.Provider != ProviderProxy || sess.Subject != id.subject) {
		s.log.Info("dropping session: proxy identity differs from session identity", "sessionUser", sess.Subject, "proxyUser", id.subject)
		s.deleteSession(ctx, raw)
		hasSess = false
	}
	if hasSess {
		if !equalGroups(sess.Groups, id.groups) {
			s.replaceSessionGroups(ctx, sess, id.groups, now)
		} else {
			s.touch(ctx, raw, sess, now)
		}
		return raw, p, true
	}
	return s.proxyNewSession(ctx, w, r, id, p, now)
}

// proxyNewSession mints (or reuses a just-minted) session for a proxy user.
// Creation is serialised so a burst of first requests shares one session.
func (s *Service) proxyNewSession(ctx context.Context, w http.ResponseWriter, r *http.Request, id *proxyIdentity,
	p identity.Principal, now time.Time) (string, identity.Principal, bool) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	if rs, ok := s.reuseRecentLocked(ctx, id, now); ok {
		s.setSessionCookie(w, rs)
		return rs, p, true
	}
	nraw, err := s.createSession(ctx, r, id.subject, id.display, ProviderProxy, id.groups)
	if err != nil {
		s.log.Error("create proxy session failed", "err", err)
		// The proxy identity is still valid for this request; only the
		// session (and so CSRF) is missing.
		return "", p, true
	}
	s.recent[id.subject] = recentProxySession{raw: nraw, groups: id.groups, at: now}
	s.setSessionCookie(w, nraw)
	s.rec.Record(ctx, p, "login", store.ResourceRef{}, store.AuditOK, map[string]string{"provider": ProviderProxy})
	return nraw, p, true
}

func (s *Service) replaceSessionGroups(ctx context.Context, sess store.Session, groups []string, now time.Time) {
	if err := s.st.Sessions().Delete(ctx, sess.IDHash); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("replace session groups: delete failed", "err", err)
		return
	}
	sess.Groups = groups
	sess.LastSeenAt = now.UTC()
	sess.ExpiresAt = s.sessionExpiry(sess.CreatedAt, now).UTC()
	if err := s.st.Sessions().Create(ctx, sess); err != nil {
		s.log.Error("replace session groups: create failed", "err", err)
	}
}

func (s *Service) reuseRecentLocked(ctx context.Context, id *proxyIdentity, now time.Time) (string, bool) {
	rs, ok := s.recent[id.subject]
	if !ok || now.Sub(rs.at) > recentProxyWindow || !equalGroups(rs.groups, id.groups) {
		return "", false
	}
	h, ok := sessionHash(rs.raw)
	if !ok {
		return "", false
	}
	sess, err := s.st.Sessions().Get(ctx, h, now)
	if err != nil || sess.Subject != id.subject || sess.Provider != ProviderProxy {
		return "", false
	}
	return rs.raw, true
}

func (s *Service) forgetRecent(subject string) {
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	delete(s.recent, subject)
}

func (s *Service) gcRecent() {
	now := s.now()
	s.recentMu.Lock()
	defer s.recentMu.Unlock()
	for k, v := range s.recent {
		if now.Sub(v.at) > recentProxyWindow {
			delete(s.recent, k)
		}
	}
}
