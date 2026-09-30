package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

// PAT format: eddy_pat_<id:12><secret:32><crc:6>, all base62. The CRC is
// CRC32 (IEEE) of the 44 id+secret characters, base62-encoded and
// left-padded with '0' to 6 characters.
const (
	patPrefix    = "eddy_pat_"
	patIDLen     = 12
	patSecretLen = 32
	patCRCLen    = 6
	patBodyLen   = patIDLen + patSecretLen + patCRCLen
	patLen       = len(patPrefix) + patBodyLen

	markUsedEvery   = 5 * time.Minute
	maxTokenNameLen = 64
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Token errors. ErrInvalidToken is deliberately uninformative: callers
// return it to clients as a plain 401.
var (
	ErrInvalidToken = errors.New("auth: invalid token")
	ErrBadRequest   = errors.New("auth: bad request")
	ErrTokenLimit   = errors.New("auth: token limit reached")
)

func randomBase62(n int) (string, error) {
	b := make([]byte, n)
	n62 := big.NewInt(int64(len(base62)))
	for i := range b {
		v, err := rand.Int(rand.Reader, n62)
		if err != nil {
			return "", fmt.Errorf("auth: random: %w", err)
		}
		b[i] = base62[v.Int64()]
	}
	return string(b), nil
}

func patChecksum(body string) string {
	v := uint64(crc32.ChecksumIEEE([]byte(body)))
	out := make([]byte, patCRCLen)
	for i := patCRCLen - 1; i >= 0; i-- {
		out[i] = base62[v%62]
		v /= 62
	}
	return string(out)
}

func isBase62(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z') {
			return false
		}
	}
	return true
}

// parsePAT splits a token into id and secret after the cheap offline checks
// (prefix, length, alphabet, checksum).
func parsePAT(tok string) (id, secret string, ok bool) {
	if len(tok) != patLen || !strings.HasPrefix(tok, patPrefix) {
		return "", "", false
	}
	body := tok[len(patPrefix):]
	if !isBase62(body) {
		return "", "", false
	}
	payload, crc := body[:patIDLen+patSecretLen], body[patIDLen+patSecretLen:]
	if !hmac.Equal([]byte(patChecksum(payload)), []byte(crc)) {
		return "", "", false
	}
	return payload[:patIDLen], payload[patIDLen:], true
}

func newPAT() (tok, id, secret string, err error) {
	if id, err = randomBase62(patIDLen); err != nil {
		return
	}
	if secret, err = randomBase62(patSecretLen); err != nil {
		return
	}
	return patPrefix + id + secret + patChecksum(id+secret), id, secret, nil
}

func (s *Service) patHash(secret string) []byte { return hmacSHA256(s.keys.patPepper, secret) }

// normalizeScopes validates scopes; operate implies read.
func normalizeScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("%w: at least one scope is required", ErrBadRequest)
	}
	var read, operate bool
	for _, sc := range in {
		switch identity.Scope(sc) {
		case identity.ScopeRead:
			read = true
		case identity.ScopeOperate:
			read, operate = true, true
		default:
			return nil, fmt.Errorf("%w: unknown scope %q (read, operate)", ErrBadRequest, truncate(sc, 32))
		}
	}
	out := []string{}
	if read {
		out = append(out, string(identity.ScopeRead))
	}
	if operate {
		out = append(out, string(identity.ScopeOperate))
	}
	return out, nil
}

func validTokenName(n string) bool {
	if n == "" || len(n) > maxTokenNameLen || !utf8.ValidString(n) {
		return false
	}
	for _, r := range n {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (s *Service) maxTTL(provider string) time.Duration {
	if provider == ProviderLocal {
		return s.cfg.Auth.Tokens.MaxTTLLocal.Duration
	}
	// Proxy (and dev) users: Eddy cannot see IdP deprovisioning.
	return s.cfg.Auth.Tokens.MaxTTLProxy.Duration
}

// Issue mints a PAT for p, a browser-session principal. ttl 0 means the
// default TTL; a ttl above the provider's maximum is rejected. It returns
// the plaintext token, shown to the user exactly once, and the stored row
// (without hash).
func (s *Service) Issue(ctx context.Context, p identity.Principal, name string, scopes []string, ttl time.Duration) (string, store.Token, error) {
	if p.User == "" || p.Via != identity.ViaWeb {
		return "", store.Token{}, fmt.Errorf("%w: tokens can only be created from a browser session", ErrBadRequest)
	}
	name = strings.TrimSpace(name)
	if !validTokenName(name) {
		return "", store.Token{}, fmt.Errorf("%w: name must be 1-%d printable characters", ErrBadRequest, maxTokenNameLen)
	}
	sc, err := normalizeScopes(scopes)
	if err != nil {
		return "", store.Token{}, err
	}
	if ttl == 0 {
		ttl = s.cfg.Auth.Tokens.DefaultTTL.Duration
	}
	if maxTTL := s.maxTTL(p.Provider); ttl > maxTTL {
		return "", store.Token{}, fmt.Errorf("%w: ttl exceeds the maximum of %s", ErrBadRequest, maxTTL)
	}
	if ttl < time.Minute {
		return "", store.Token{}, fmt.Errorf("%w: ttl must be at least 1m", ErrBadRequest)
	}
	now := s.now().UTC()
	tok, id, secret, err := newPAT()
	if err != nil {
		return "", store.Token{}, err
	}
	t := store.Token{
		ID:        id,
		Hash:      s.patHash(secret),
		Subject:   p.User,
		Display:   p.Display,
		Provider:  p.Provider,
		Groups:    slices.Clone(p.Groups),
		Name:      name,
		Scopes:    sc,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	// The store checks the per-user cap and inserts atomically, so
	// concurrent requests on several replicas cannot exceed it.
	if err := s.st.Tokens().Create(ctx, t, s.cfg.Auth.Tokens.MaxPerUser); err != nil {
		if errors.Is(err, store.ErrLimit) {
			return "", store.Token{}, fmt.Errorf("%w: at most %d active tokens per user", ErrTokenLimit, s.cfg.Auth.Tokens.MaxPerUser)
		}
		return "", store.Token{}, fmt.Errorf("auth: create token: %w", err)
	}
	t.Hash = nil
	return tok, t, nil
}

// VerifyPAT authenticates a bearer PAT for /mcp. It returns the owner's
// principal (Via mcp, with TokenID and Scopes) and the token's expiry.
//
// Groups at use time:
//   - local users: resolved live from users.yaml; a missing or disabled
//     user makes the token invalid;
//   - proxy users: snapshot ∩ groups of the user's latest browser session.
//     With no session on record the snapshot is used unchanged (groups can
//     still only shrink relative to issue time, never grow);
//   - dev users: the snapshot, and only while dev login is active.
//
// Every failure returns ErrInvalidToken (store outages return a wrapped
// error so callers can answer 5xx). Tokens are never logged.
func (s *Service) VerifyPAT(ctx context.Context, token string) (identity.Principal, time.Time, error) {
	id, secret, ok := parsePAT(token)
	if !ok {
		return identity.Principal{}, time.Time{}, ErrInvalidToken
	}
	now := s.now()
	t, err := s.st.Tokens().Get(ctx, id, now)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return identity.Principal{}, time.Time{}, ErrInvalidToken
		}
		return identity.Principal{}, time.Time{}, fmt.Errorf("auth: token lookup: %w", err)
	}
	if !hmac.Equal(s.patHash(secret), t.Hash) {
		return identity.Principal{}, time.Time{}, ErrInvalidToken
	}
	if t.RevokedAt != nil || !now.Before(t.ExpiresAt) {
		return identity.Principal{}, time.Time{}, ErrInvalidToken
	}
	if _, err := s.mapper.checkSubject(t.Subject); err != nil {
		return identity.Principal{}, time.Time{}, ErrInvalidToken
	}
	scopes, err := normalizeScopes(t.Scopes)
	if err != nil {
		return identity.Principal{}, time.Time{}, ErrInvalidToken
	}
	p := identity.Principal{
		User:     t.Subject,
		Display:  t.Display,
		Provider: t.Provider,
		Via:      identity.ViaMCP,
		TokenID:  t.ID,
	}
	for _, sc := range scopes {
		p.Scopes = append(p.Scopes, identity.Scope(sc))
	}
	switch t.Provider {
	case ProviderLocal:
		if !s.cfg.Auth.Local.Enabled {
			return identity.Principal{}, time.Time{}, ErrInvalidToken
		}
		u := s.localUser(t.Subject)
		if u == nil {
			return identity.Principal{}, time.Time{}, ErrInvalidToken
		}
		p.Groups = s.mapper.Groups(u.Groups, u.Username, u.Subject)
	case ProviderProxy:
		if !s.cfg.Auth.Proxy.Enabled {
			return identity.Principal{}, time.Time{}, ErrInvalidToken
		}
		snap := s.mapper.validPrincipalGroups(t.Groups)
		latest, err := s.st.Sessions().LatestGroups(ctx, t.Subject)
		switch {
		case err == nil:
			p.Groups = intersect(snap, latest)
		case errors.Is(err, store.ErrNotFound):
			p.Groups = snap
		default:
			return identity.Principal{}, time.Time{}, fmt.Errorf("auth: latest session groups: %w", err)
		}
	case ProviderDev:
		if !s.devActive {
			return identity.Principal{}, time.Time{}, ErrInvalidToken
		}
		p.Groups = s.mapper.validPrincipalGroups(t.Groups)
	default:
		return identity.Principal{}, time.Time{}, ErrInvalidToken
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= markUsedEvery {
		if err := s.st.Tokens().MarkUsed(context.WithoutCancel(ctx), t.ID, now.UTC()); err != nil {
			s.log.Error("mark token used failed", "tokenId", t.ID, "err", err)
		}
	}
	return p, t.ExpiresAt, nil
}

func intersect(a, b []string) []string {
	out := make([]string, 0, len(a))
	for _, g := range a {
		if slices.Contains(b, g) {
			out = append(out, g)
		}
	}
	return out
}

// ListTokens returns the subject's non-revoked tokens, newest first.
func (s *Service) ListTokens(ctx context.Context, subject string) ([]store.Token, error) {
	ts, err := s.st.Tokens().List(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	out := ts[:0]
	for _, t := range ts {
		if t.RevokedAt == nil {
			t.Hash = nil
			out = append(out, t)
		}
	}
	return out, nil
}

// RevokeToken revokes one of the subject's tokens. store.ErrNotFound is
// returned for unknown ids and tokens of other users.
func (s *Service) RevokeToken(ctx context.Context, subject, id string) error {
	if len(id) != patIDLen || !isBase62(id) {
		return store.ErrNotFound
	}
	return s.st.Tokens().Revoke(ctx, subject, id, s.now().UTC())
}
