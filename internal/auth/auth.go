// Package auth signs users in and turns them into an identity.Principal.
//
// It implements local users (users.yaml with argon2id or bcrypt hashes),
// trusted reverse-proxy headers, server-side browser sessions, CSRF
// protection, login rate limiting, personal access tokens for MCP and the
// dev-only fake login. Every sign-in path goes through one mapping function
// (mapping.go) so user and group rules cannot drift between providers.
//
// See docs/adr/0003-mvp-security.md.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// Provider names recorded on principals, sessions and tokens.
const (
	ProviderLocal = "local"
	ProviderProxy = "proxy"
	ProviderDev   = "dev"
)

// Providers reports which sign-in methods are enabled (GET /auth/providers).
type Providers struct {
	Local bool `json:"local"`
	Proxy bool `json:"proxy"`
	Dev   bool `json:"dev"`
}

// Service is the auth layer of the hub. Create it with New.
type Service struct {
	cfg    *config.Hub
	st     store.Store
	rec    *audit.Recorder
	log    *slog.Logger
	now    func() time.Time
	mapper *Mapper
	keys   keys

	origin string // publicURL origin, e.g. https://eddy.example.com

	// local users
	users     atomic.Pointer[userSet]
	usersHash atomic.Value // [32]byte of the last loaded file content
	hasher    *hasher

	// proxy
	proxyCIDRs  []netip.Prefix
	proxySecret []byte
	proxyLog    *logThrottle
	recentMu    sync.Mutex
	recent      map[string]recentProxySession

	limiter  *loginLimiter
	sessions *sessionCache

	devActive bool

	cookieName    string
	preCookieName string
	secureCookies bool
}

// New validates the auth configuration, loads key material and the users
// file, and returns a ready Service. It refuses to start (returns an error)
// on any unsafe configuration, including a dev fake login that cannot be
// activated.
func New(cfg *config.Hub, st store.Store, rec *audit.Recorder, log *slog.Logger) (*Service, error) {
	if cfg == nil || st == nil {
		return nil, errors.New("auth: config and store are required")
	}
	if log == nil {
		log = slog.Default()
	}
	if rec == nil {
		rec = audit.New(nil, log)
	}
	log = log.With("component", "auth")
	s := &Service{
		cfg:           cfg,
		st:            st,
		rec:           rec,
		log:           log,
		now:           time.Now,
		mapper:        NewMapper(cfg.Auth, log),
		hasher:        newHasher(cfg.Auth.LoginRateLimit.MaxConcurrentHashes),
		proxyLog:      newLogThrottle(10 * time.Second),
		recent:        map[string]recentProxySession{},
		secureCookies: cfg.SecureCookies(),
	}
	if st.RateLimits() == nil {
		return nil, errors.New("auth: the store has no rate limits")
	}
	s.limiter = newLoginLimiter(st.RateLimits(), cfg.Auth.LoginRateLimit, func() time.Time { return s.now() })
	s.sessions = newSessionCache()
	if s.secureCookies {
		s.cookieName, s.preCookieName = "__Host-eddy_session", "__Host-eddy_pre"
	} else {
		s.cookieName, s.preCookieName = "eddy_session", "eddy_pre"
	}

	u, err := url.Parse(cfg.PublicURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("auth: publicURL %q must be an absolute http(s) URL", cfg.PublicURL)
	}
	s.origin = canonicalOrigin(u)

	active, err := devActivation(cfg)
	if err != nil {
		return nil, err
	}
	s.devActive = active

	k, err := loadKeys(cfg.Auth.KeyFile)
	if err != nil {
		return nil, err
	}
	s.keys = k

	if cfg.Auth.Local.Enabled {
		if cfg.Auth.Local.UsersFile == "" {
			return nil, errors.New("auth: auth.local.usersFile is required")
		}
		if err := s.loadUsers(context.Background(), true); err != nil {
			return nil, err
		}
		// Pay for the dummy hash now so the first unknown-user login is not
		// measurably slower than the rest.
		dummyHash()
	} else {
		s.users.Store(&userSet{bySubject: map[string]*User{}, byName: map[string]*User{}})
	}

	if p := cfg.Auth.Proxy; p.Enabled {
		if len(p.TrustedCIDRs) == 0 {
			return nil, errors.New("auth: auth.proxy.trustedCIDRs is required")
		}
		for _, c := range p.TrustedCIDRs {
			pfx, err := netip.ParsePrefix(c)
			if err != nil {
				return nil, fmt.Errorf("auth: auth.proxy.trustedCIDRs %q: %w", c, err)
			}
			s.proxyCIDRs = append(s.proxyCIDRs, pfx.Masked())
		}
		if p.InsecureSkipSharedSecret {
			log.Warn("auth.proxy.insecureSkipSharedSecret is set: proxy identity headers are trusted on the TCP peer address alone")
		} else {
			sec := os.Getenv(p.SharedSecretEnv)
			if len(sec) < 32 {
				return nil, fmt.Errorf("auth: proxy shared secret %s must hold ≥32 bytes", p.SharedSecretEnv)
			}
			s.proxySecret = []byte(sec)
		}
	}
	if s.devActive {
		log.Warn("DEV FAKE LOGIN IS ENABLED: anyone on this machine can sign in as any user", "route", "/auth/dev/login")
	}
	return s, nil
}

// Run performs background work until ctx is done: users-file hot reload
// (every 10s), cache garbage collection and the dev-mode warning.
func (s *Service) Run(ctx context.Context) {
	reload := time.NewTicker(10 * time.Second)
	defer reload.Stop()
	gc := time.NewTicker(time.Minute)
	defer gc.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-reload.C:
			if s.cfg.Auth.Local.Enabled {
				if err := s.loadUsers(ctx, false); err != nil {
					s.log.Error("users file reload failed; keeping the previous users", "err", err)
				}
			}
		case <-gc.C:
			s.sessions.gc(s.now())
			s.gcRecent()
			if s.devActive {
				s.log.Warn("DEV FAKE LOGIN IS ENABLED: never run this build in production")
			}
		}
	}
}

// Providers reports the enabled sign-in methods.
func (s *Service) Providers() Providers {
	return Providers{Local: s.cfg.Auth.Local.Enabled, Proxy: s.cfg.Auth.Proxy.Enabled, Dev: s.devActive}
}

// DevMode reports whether the dev fake login is active (for /api/v1/me).
func (s *Service) DevMode() bool { return s.devActive }

// RevokeUser deletes every session and revokes every token of subject. It
// backs `eddy-hub admin revoke --user`.
func (s *Service) RevokeUser(ctx context.Context, subject string) error {
	if subject == "" {
		return errors.New("auth: subject is required")
	}
	now := s.now()
	var errs []error
	if err := s.st.Sessions().DeleteBySubject(ctx, subject); err != nil {
		errs = append(errs, fmt.Errorf("auth: delete sessions of %s: %w", subject, err))
	}
	if err := s.st.Tokens().RevokeBySubject(ctx, subject, now); err != nil {
		errs = append(errs, fmt.Errorf("auth: revoke tokens of %s: %w", subject, err))
	}
	s.forgetRecent(subject)
	s.revoked(ctx, subject)
	res := store.AuditOK
	if len(errs) > 0 {
		res = store.AuditError
	}
	s.rec.Record(ctx, identity.Principal{User: "admin", Via: identity.ViaSystem}, "user.revoke",
		store.ResourceRef{}, res, map[string]string{"subject": truncate(subject, 256)})
	return errors.Join(errs...)
}

// canonicalOrigin returns scheme://host[:port] in lower case with the
// default port removed, as browsers send it in the Origin header.
func canonicalOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

// isLoopbackListen reports whether a listen address binds only loopback.
func isLoopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return a.Unmap().IsLoopback()
}

// devActivation decides whether the dev fake login may run. It returns an
// error whenever dev.fakeLogin is requested but any condition fails, so the
// hub refuses to start rather than silently running without it.
func devActivation(cfg *config.Hub) (bool, error) {
	if !cfg.Dev.FakeLogin {
		return false, nil
	}
	var why []string
	if !devBuild {
		why = append(why, "the binary was not built with -tags dev")
	}
	if os.Getenv("EDDY_DEV_MODE") != "1" {
		why = append(why, "EDDY_DEV_MODE=1 is not set")
	}
	if !isLoopbackListen(cfg.Listen.UI) {
		why = append(why, fmt.Sprintf("listen.ui %q is not a loopback address", cfg.Listen.UI))
	}
	if len(why) > 0 {
		return false, fmt.Errorf("auth: dev.fakeLogin is set but refused: %s", strings.Join(why, "; "))
	}
	return true, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// Do not split a UTF-8 sequence.
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// logThrottle limits a noisy log line to one per interval.
type logThrottle struct {
	mu    sync.Mutex
	every time.Duration
	last  time.Time
}

func newLogThrottle(every time.Duration) *logThrottle { return &logThrottle{every: every} }

func (t *logThrottle) allow(now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.Sub(t.last) < t.every {
		return false
	}
	t.last = now
	return true
}
