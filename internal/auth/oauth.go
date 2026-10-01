package auth

// This file is the browser flow shared by every OAuth 2.0 / OIDC provider
// (oauth_github.go, oauth_oidc.go):
//
//	GET /auth/{provider}/login?returnTo=/path
//	    state, nonce and a PKCE verifier go into a short-lived encrypted
//	    cookie (AES-256-GCM, key from HKDF(auth.keyFile, "oauth-flow")),
//	    then 302 to the identity provider.
//	GET /auth/{provider}/callback?code=&state=
//	    the cookie is read and always cleared, state must match it, the code
//	    is exchanged (with the PKCE verifier), the provider maps the user
//	    through the shared Mapper, and a new server-side session is minted.
//
// The identity provider never calls the hub: both legs are browser
// redirects. The hub only calls the provider's token, userinfo/API and JWKS
// endpoints. Access tokens and ID tokens are used once and never stored or
// logged.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

const (
	// oauthFlowTTL bounds a sign-in round trip through the identity provider.
	oauthFlowTTL = 10 * time.Minute
	// oauthExchangeTimeout bounds the back-channel calls of one callback.
	oauthExchangeTimeout = 20 * time.Second
	// oauthFailWindow is the window of the per-IP callback failure limit.
	oauthFailWindow = time.Minute
	maxOAuthCode    = 2048
)

// Error codes the SPA's /login page understands (?error=<code>). They are
// deliberately coarse: the reason is only in the audit log.
const (
	oauthErrState       = "state"           // missing, expired or mismatched flow (or a login CSRF attempt)
	oauthErrDenied      = "denied"          // the identity is not allowed to sign in
	oauthErrProvider    = "provider"        // the provider failed or returned something invalid
	oauthErrCancelled   = "cancelled"       // the user declined at the provider
	oauthErrRateLimited = "rate_limited"    // too many failed callbacks from this address
	oauthErrUnavailable = "unavailable"     // the provider or the store cannot be reached
	oauthErrBadRequest  = "invalid_request" // e.g. a returnTo that is not a local path
)

// externalIdentity is a user mapped from a provider, ready for a session.
type externalIdentity struct {
	subject string   // Kubernetes user, already through Mapper.checkSubject
	display string   // shown in the UI
	groups  []string // already through Mapper.Groups
	login   string   // provider login or email, for the audit log only
}

// errOAuthDenied wraps a reason the identity was refused (allowlists,
// unverified email, invalid user name). The reason is audited, never shown.
type errOAuthDenied struct{ reason string }

func (e *errOAuthDenied) Error() string { return "auth: sign-in denied: " + e.reason }

func denied(format string, a ...any) error { return &errOAuthDenied{reason: fmt.Sprintf(format, a...)} }

// oauthProvider is one configured sign-in provider.
type oauthProvider interface {
	// info describes the provider for GET /auth/providers.
	info() ProviderInfo
	// sessionProvider is the provider name stored on sessions and tokens.
	sessionProvider() string
	// authCodeURL returns the authorization URL to redirect the browser to.
	authCodeURL(ctx context.Context, f oauthFlow) (string, error)
	// identify exchanges code and maps the user. It returns *errOAuthDenied
	// when the user must not sign in.
	identify(ctx context.Context, code string, f oauthFlow) (*externalIdentity, error)
}

// ProviderInfo is one entry of GET /auth/providers → providers.
type ProviderInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // github | oidc
	// Icon names a mark the SPA ships ("github"); empty means a neutral icon.
	Icon     string `json:"icon,omitempty"`
	LoginURL string `json:"loginURL"`
}

// oauthFlow is the content of the flow cookie.
type oauthFlow struct {
	Provider string `json:"p"`
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	ReturnTo string `json:"r"`
	Expires  int64  `json:"e"`
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Service) flowAEAD() (cipher.AEAD, error) {
	b, err := aes.NewCipher(s.keys.oauthFlow)
	if err != nil {
		return nil, fmt.Errorf("auth: flow cipher: %w", err)
	}
	return cipher.NewGCM(b)
}

// sealFlow encrypts and authenticates f for the flow cookie.
func (s *Service) sealFlow(f oauthFlow) (string, error) {
	aead, err := s.flowAEAD()
	if err != nil {
		return "", err
	}
	pt, err := json.Marshal(f)
	if err != nil {
		return "", fmt.Errorf("auth: encode flow: %w", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("auth: flow nonce: %w", err)
	}
	ct := aead.Seal(nonce, nonce, pt, []byte(s.flowCookieName))
	return base64.RawURLEncoding.EncodeToString(ct), nil
}

// openFlow returns the flow in the cookie value if it is authentic and
// not expired.
func (s *Service) openFlow(v string, now time.Time) (oauthFlow, bool) {
	if v == "" || len(v) > 4096 {
		return oauthFlow{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(v)
	if err != nil {
		return oauthFlow{}, false
	}
	aead, err := s.flowAEAD()
	if err != nil || len(raw) < aead.NonceSize()+aead.Overhead() {
		return oauthFlow{}, false
	}
	pt, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(s.flowCookieName))
	if err != nil {
		return oauthFlow{}, false
	}
	var f oauthFlow
	if err := json.Unmarshal(pt, &f); err != nil {
		return oauthFlow{}, false
	}
	if f.State == "" || f.Nonce == "" || f.Verifier == "" || !now.Before(time.Unix(f.Expires, 0)) {
		return oauthFlow{}, false
	}
	if f.ReturnTo != "" && !ValidReturnTo(f.ReturnTo) {
		return oauthFlow{}, false
	}
	return f, true
}

// callbackURL is <publicURL>/auth/<id>/callback, registered at the provider.
func (s *Service) callbackURL(id string) string {
	return strings.TrimSuffix(s.cfg.PublicURL, "/") + "/auth/" + id + "/callback"
}

// loginRedirect sends the browser back to the SPA's sign-in page with an
// error code (and returnTo, so a retry lands where the user was going).
func loginRedirect(w http.ResponseWriter, r *http.Request, code, returnTo string) {
	q := url.Values{"error": {code}}
	if returnTo != "" && returnTo != "/" && ValidReturnTo(returnTo) {
		q.Set("returnTo", returnTo)
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/login?"+q.Encode(), http.StatusFound)
}

func (s *Service) oauthRoutes(mux *http.ServeMux) {
	if len(s.oauth) == 0 {
		return
	}
	mux.HandleFunc("GET /auth/{provider}/login", s.handleOAuthLogin)
	mux.HandleFunc("GET /auth/{provider}/callback", s.handleOAuthCallback)
}

func (s *Service) handleOAuthLogin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("provider")
	prov, ok := s.oauth[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown sign-in provider")
		return
	}
	returnTo := r.URL.Query().Get("returnTo")
	if returnTo == "" {
		returnTo = "/"
	}
	if !ValidReturnTo(returnTo) {
		loginRedirect(w, r, oauthErrBadRequest, "")
		return
	}
	f := oauthFlow{Provider: id, ReturnTo: returnTo, Expires: s.now().Add(oauthFlowTTL).Unix()}
	var err error
	if f.State, err = randomToken(); err == nil {
		if f.Nonce, err = randomToken(); err == nil {
			f.Verifier = oauth2.GenerateVerifier()
		}
	}
	if err != nil {
		s.log.Error("oauth flow setup failed", "provider", id, "err", err)
		loginRedirect(w, r, oauthErrUnavailable, returnTo)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), oauthExchangeTimeout)
	defer cancel()
	target, err := prov.authCodeURL(ctx, f)
	if err != nil {
		s.log.Error("oauth provider unavailable", "provider", id, "err", err)
		loginRedirect(w, r, oauthErrUnavailable, returnTo)
		return
	}
	v, err := s.sealFlow(f)
	if err != nil {
		s.log.Error("oauth flow cookie failed", "provider", id, "err", err)
		loginRedirect(w, r, oauthErrUnavailable, returnTo)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.flowCookieName,
		Value:    v,
		Path:     "/",
		Secure:   s.secureCookies,
		HttpOnly: true,
		// Lax: the cookie must come back on the provider's top-level
		// redirect to the callback, which is a cross-site navigation.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(oauthFlowTTL / time.Second),
	})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

// idpErrorRE keeps only well-formed OAuth error codes for the audit log.
var idpErrorRE = regexp.MustCompile(`^[a-z_]{1,64}$`)

func (s *Service) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("provider")
	prov, ok := s.oauth[id]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", "unknown sign-in provider")
		return
	}
	now := s.now()
	ip := peerIP(r)
	// The flow cookie is single-use: clear it whatever happens next.
	var cookieVal string
	if c, err := r.Cookie(s.flowCookieName); err == nil {
		cookieVal = c.Value
	}
	s.clearCookie(w, s.flowCookieName)

	failKey := "login:oauthfail:" + ipKey(ip)
	if n, _, err := s.st.RateLimits().Get(ctx, failKey, now); err != nil {
		s.log.Error("oauth callback rate limit unavailable", "err", err)
		loginRedirect(w, r, oauthErrUnavailable, "")
		return
	} else if n >= s.limiter.perIP {
		loginRedirect(w, r, oauthErrRateLimited, "")
		return
	}

	f, flowOK := s.openFlow(cookieVal, now)
	returnTo := ""
	if flowOK {
		returnTo = f.ReturnTo
	}
	fail := func(code, reason, login string) {
		if _, _, err := s.st.RateLimits().Hit(ctx, failKey, oauthFailWindow, 0, now); err != nil {
			s.log.Error("recording a failed oauth callback failed", "err", err)
		}
		detail := map[string]string{"provider": prov.sessionProvider(), "reason": reason}
		if login != "" {
			detail["username"] = truncate(login, 64)
		}
		s.rec.Record(ctx, identity.Principal{Via: identity.ViaWeb, Provider: prov.sessionProvider()}, "login",
			store.ResourceRef{}, store.AuditDenied, detail)
		loginRedirect(w, r, code, returnTo)
	}

	q := r.URL.Query()
	switch {
	case !flowOK:
		fail(oauthErrState, "missing, invalid or expired flow cookie", "")
		return
	case f.Provider != id:
		fail(oauthErrState, "flow started for another provider", "")
		return
	case !ctEqualString(q.Get("state"), f.State):
		fail(oauthErrState, "state mismatch", "")
		return
	}
	if e := q.Get("error"); e != "" {
		if !idpErrorRE.MatchString(e) {
			e = "invalid"
		}
		code := oauthErrProvider
		if e == "access_denied" {
			code = oauthErrCancelled
		}
		fail(code, "provider returned error "+e, "")
		return
	}
	code := q.Get("code")
	if code == "" || len(code) > maxOAuthCode {
		fail(oauthErrProvider, "missing or oversized code", "")
		return
	}

	xctx, cancel := context.WithTimeout(ctx, oauthExchangeTimeout)
	defer cancel()
	ident, err := prov.identify(xctx, code, f)
	if err != nil {
		var d *errOAuthDenied
		if errors.As(err, &d) {
			fail(oauthErrDenied, d.reason, "")
			return
		}
		// err never carries tokens: providers build their own messages.
		s.log.Warn("oauth sign-in failed", "provider", id, "err", err)
		fail(oauthErrProvider, "provider exchange or verification failed", "")
		return
	}

	// Rotate: any session the browser already had is discarded.
	if c, err := r.Cookie(s.cookieName); err == nil {
		s.deleteSession(ctx, c.Value)
	}
	raw, err := s.createSession(ctx, r, ident.subject, ident.display, prov.sessionProvider(), ident.groups)
	if err != nil {
		s.log.Error("create session failed", "err", err)
		loginRedirect(w, r, oauthErrUnavailable, returnTo)
		return
	}
	s.setSessionCookie(w, raw)
	if err := s.st.RateLimits().Reset(ctx, failKey); err != nil {
		s.log.Error("resetting oauth callback failures failed", "err", err)
	}
	p := identity.Principal{User: ident.subject, Groups: ident.groups, Display: ident.display,
		Provider: prov.sessionProvider(), Via: identity.ViaWeb}
	s.rec.Record(ctx, p, "login", store.ResourceRef{}, store.AuditOK,
		map[string]string{"provider": prov.sessionProvider(), "username": truncate(ident.login, 64)})
	to := f.ReturnTo
	if to == "" {
		to = "/"
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, to, http.StatusFound)
}

// oauthBySession returns the configured provider whose sessions carry name.
func (s *Service) oauthBySession(name string) oauthProvider {
	for _, p := range s.oauth {
		if p.sessionProvider() == name {
			return p
		}
	}
	return nil
}

// oauthContext makes the oauth2 package use the hub's HTTP client.
func (s *Service) oauthContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, s.httpClient)
}
