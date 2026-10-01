package auth

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/idestis/eddy/internal/config"
)

// ProviderOIDCPrefix prefixes OIDC provider ids on sessions and tokens
// (oidc:<id>).
const ProviderOIDCPrefix = "oidc:"

// oidcRetryEvery limits discovery retries while the issuer is unreachable.
const oidcRetryEvery = 10 * time.Second

// oidcProvider is one OpenID Connect provider: authorization code flow with
// PKCE, ID token verification (signature, iss, aud, azp, exp, nonce) and the
// allowlists of its config.
type oidcProvider struct {
	cfg     config.OIDCAuth
	secret  string
	svc     *Service
	domains map[string]bool // lower-cased allowedDomains

	mu       sync.Mutex
	provider *oidc.Provider
	lastTry  time.Time
	lastErr  error
}

func newOIDCProvider(s *Service, c config.OIDCAuth, secret string) *oidcProvider {
	p := &oidcProvider{cfg: c, secret: secret, svc: s, domains: map[string]bool{}}
	for _, d := range c.AllowedDomains {
		p.domains[strings.ToLower(strings.TrimSpace(d))] = true
	}
	return p
}

func (p *oidcProvider) info() ProviderInfo {
	return ProviderInfo{ID: p.cfg.ID, Name: p.cfg.Name, Kind: "oidc", LoginURL: "/auth/" + p.cfg.ID + "/login"}
}

func (p *oidcProvider) sessionProvider() string { return ProviderOIDCPrefix + p.cfg.ID }

// discover returns the issuer's provider, fetching discovery on first use.
// The hub starts even when the issuer is unreachable; sign-in through it
// then fails until discovery succeeds.
func (p *oidcProvider) discover() (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, nil
	}
	now := time.Now()
	if p.lastErr != nil && now.Sub(p.lastTry) < oidcRetryEvery {
		return nil, p.lastErr
	}
	p.lastTry = now
	// go-oidc keeps this context for later JWKS refreshes, so it must not
	// be a request context. The client's own timeout bounds each call.
	ctx := oidc.ClientContext(context.Background(), p.svc.httpClient)
	prov, err := oidc.NewProvider(ctx, p.cfg.Issuer)
	if err != nil {
		p.lastErr = fmt.Errorf("auth: oidc %s discovery: %w", p.cfg.ID, sanitizeOAuthErr(err))
		return nil, p.lastErr
	}
	p.provider, p.lastErr = prov, nil
	return prov, nil
}

func (p *oidcProvider) oauthConfig(prov *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.secret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  p.svc.callbackURL(p.cfg.ID),
		Scopes:       p.cfg.Scopes,
	}
}

func (p *oidcProvider) authCodeURL(_ context.Context, f oauthFlow) (string, error) {
	prov, err := p.discover()
	if err != nil {
		return "", err
	}
	opts := []oauth2.AuthCodeOption{oidc.Nonce(f.Nonce), oauth2.S256ChallengeOption(f.Verifier)}
	// Google: hint the hosted domain when exactly one is allowed (the hd
	// claim is still checked; the hint alone proves nothing).
	if p.cfg.HostedDomainClaim == "hd" && len(p.cfg.AllowedDomains) == 1 {
		opts = append(opts, oauth2.SetAuthURLParam("hd", strings.ToLower(p.cfg.AllowedDomains[0])))
	}
	return p.oauthConfig(prov).AuthCodeURL(f.State, opts...), nil
}

func (p *oidcProvider) identify(ctx context.Context, code string, f oauthFlow) (*externalIdentity, error) {
	prov, err := p.discover()
	if err != nil {
		return nil, err
	}
	tok, err := p.oauthConfig(prov).Exchange(p.svc.oauthContext(ctx), code, oauth2.VerifierOption(f.Verifier))
	if err != nil {
		return nil, fmt.Errorf("auth: oidc %s code exchange: %w", p.cfg.ID, sanitizeOAuthErr(err))
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		return nil, fmt.Errorf("auth: oidc %s: the token response has no id_token", p.cfg.ID)
	}
	// The key set is shared per provider and uses the hub's HTTP client.
	v := prov.Verifier(&oidc.Config{ClientID: p.cfg.ClientID, Now: p.svc.now})
	idt, err := v.Verify(ctx, rawID)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc %s: id token: %w", p.cfg.ID, err)
	}
	if !ctEqualString(idt.Nonce, f.Nonce) {
		return nil, fmt.Errorf("auth: oidc %s: id token nonce mismatch", p.cfg.ID)
	}
	var claims map[string]any
	if err := idt.Claims(&claims); err != nil {
		return nil, fmt.Errorf("auth: oidc %s: claims: %w", p.cfg.ID, err)
	}
	// OIDC Core 3.1.3.7: with several audiences, azp must be this client.
	if len(idt.Audience) > 1 {
		if azp, _ := claims["azp"].(string); azp != p.cfg.ClientID {
			return nil, fmt.Errorf("auth: oidc %s: id token azp is not this client", p.cfg.ID)
		}
	}
	return p.mapClaims(claims)
}

// mapClaims applies the allowlists and maps verified ID token claims to an
// identity. It is separate from identify so it can be tested directly.
func (p *oidcProvider) mapClaims(claims map[string]any) (*externalIdentity, error) {
	c := p.cfg
	email := strings.ToLower(strings.TrimSpace(claimString(claims, "email")))
	verified := claimBool(claims, "email_verified")
	requireVerified := c.RequireVerifiedEmailOrDefault()

	usesEmail := c.UsernameClaim == "email" || (len(p.domains) > 0 && c.HostedDomainClaim == "")
	if usesEmail && email == "" {
		return nil, denied("id token has no email claim")
	}
	if usesEmail && requireVerified && !verified {
		return nil, denied("email %s is not verified", email)
	}
	if len(p.domains) > 0 {
		var domain string
		if c.HostedDomainClaim != "" {
			domain = strings.ToLower(claimString(claims, c.HostedDomainClaim))
		} else if _, d, ok := strings.Cut(email, "@"); ok {
			domain = d
		}
		if domain == "" || !p.domains[domain] {
			return nil, denied("domain %q is not allowed", truncate(domain, 64))
		}
	}
	var raw []string
	if c.GroupsClaim != "" {
		raw = claimStrings(claims, c.GroupsClaim)
	}
	if len(c.AllowedGroups) > 0 && !slices.ContainsFunc(raw, func(g string) bool { return slices.Contains(c.AllowedGroups, g) }) {
		return nil, denied("not in an allowed group")
	}
	value := strings.TrimSpace(claimString(claims, c.UsernameClaim))
	if c.UsernameClaim == "email" {
		value = email
	}
	if value == "" {
		return nil, denied("id token has no %s claim", c.UsernameClaim)
	}
	subject, err := p.svc.mapper.OAuthUser(c.UserPrefix, value)
	if err != nil {
		return nil, denied("user name not allowed: %v", err)
	}
	display := strings.TrimSpace(claimString(claims, "name"))
	if display == "" || len(display) > 128 {
		display = value
	}
	return &externalIdentity{
		subject: subject,
		display: display,
		groups:  p.svc.mapper.Groups(raw, value, subject, email),
		login:   value,
	}, nil
}

func claimString(c map[string]any, k string) string {
	s, _ := c[k].(string)
	return s
}

// claimBool accepts true and "true" (some providers send strings).
func claimBool(c map[string]any, k string) bool {
	switch v := c[k].(type) {
	case bool:
		return v
	case string:
		return v == "true"
	}
	return false
}

// claimStrings accepts a string array or a single string.
func claimStrings(c map[string]any, k string) []string {
	switch v := c[k].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
