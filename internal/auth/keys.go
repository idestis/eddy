package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/hkdf"
)

// minKeyBytes is the minimum size of auth.keyFile.
const minKeyBytes = 32

// keys are sub-keys derived from auth.keyFile with HKDF-SHA256. Deriving
// one key per purpose means a CSRF token can never double as a PAT hash.
type keys struct {
	csrf       []byte
	patPepper  []byte
	preSession []byte
	// peer authenticates hub replicas to each other (ADR-0004).
	peer []byte
	// oauthFlow encrypts the OAuth/OIDC flow cookie (state, nonce, PKCE).
	oauthFlow []byte
}

func loadKeys(path string) (keys, error) {
	if path == "" {
		return keys{}, fmt.Errorf("auth: auth.keyFile is required (≥%d random bytes)", minKeyBytes)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return keys{}, fmt.Errorf("auth: read key file: %w", err)
	}
	// Secrets mounted from text often carry a trailing newline; it adds no entropy.
	b = bytes.TrimSpace(b)
	if len(b) < minKeyBytes {
		return keys{}, fmt.Errorf("auth: key file %s holds %d bytes, need ≥%d", path, len(b), minKeyBytes)
	}
	return deriveKeys(b)
}

func deriveKeys(master []byte) (keys, error) {
	var k keys
	for _, d := range []struct {
		info string
		dst  *[]byte
	}{
		{"csrf", &k.csrf},
		{"pat-pepper", &k.patPepper},
		{"pre-session", &k.preSession},
		{"peer", &k.peer},
		{"oauth-flow", &k.oauthFlow},
	} {
		out := make([]byte, 32)
		if _, err := io.ReadFull(hkdf.New(sha256.New, master, []byte("eddy-auth-v1"), []byte(d.info)), out); err != nil {
			return keys{}, fmt.Errorf("auth: derive %s key: %w", d.info, err)
		}
		*d.dst = out
	}
	return k, nil
}

func hmacSHA256(key []byte, parts ...string) []byte {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write([]byte(p))
	}
	return m.Sum(nil)
}

// PeerKey returns the key hub replicas authenticate each other with:
// HKDF(auth.keyFile, info "peer"). Every replica mounts the same key file,
// so they derive the same key.
func (s *Service) PeerKey() []byte { return bytes.Clone(s.keys.peer) }
