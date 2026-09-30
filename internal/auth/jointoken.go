package auth

import (
	"crypto/hmac"
	"strings"
)

// Join tokens (ADR-0005) use the PAT scheme with their own prefix:
// eddy_join_<id:12><secret:32><crc:6>, base62, so secret scanners catch
// them too. They are stored as HMAC(pat pepper, "join:" ‖ secret); the
// domain prefix keeps a join-token hash from ever matching a PAT hash.
const (
	JoinTokenPrefix = "eddy_join_"
	joinHashDomain  = "join:"

	// agentTokenLen is the length of a permanent agent token minted at
	// join time (base62, about 285 bits).
	agentTokenLen = 48
)

// IsJoinToken reports whether tok has the join-token prefix. It does not
// validate the token.
func IsJoinToken(tok string) bool { return strings.HasPrefix(tok, JoinTokenPrefix) }

// NewJoinToken returns a fresh join token with its public id and secret.
func NewJoinToken() (tok, id, secret string, err error) { return newPrefixed(JoinTokenPrefix) }

// ParseJoinToken runs the offline checks (prefix, length, alphabet,
// checksum) and splits a join token into id and secret.
func ParseJoinToken(tok string) (id, secret string, ok bool) {
	return parsePrefixed(tok, JoinTokenPrefix)
}

// JoinTokenHash is the stored hash of a join token's secret.
func (s *Service) JoinTokenHash(secret string) []byte {
	return hmacSHA256(s.keys.patPepper, joinHashDomain, secret)
}

// VerifyJoinTokenHash compares a presented secret with a stored hash in
// constant time.
func (s *Service) VerifyJoinTokenHash(secret string, hash []byte) bool {
	return hmac.Equal(s.JoinTokenHash(secret), hash)
}

// NewAgentToken returns a random permanent agent token, the value the hub
// writes to a cluster's token Secret when an agent joins.
func NewAgentToken() (string, error) { return randomBase62(agentTokenLen) }
