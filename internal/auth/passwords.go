package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// argon2id parameters for new hashes (RFC 9106, second recommendation).
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 4
	argonSaltLen = 16
	argonKeyLen  = 32

	// Upper bounds for hashes loaded from users.yaml, so a bad file cannot
	// turn every login into a memory or CPU bomb.
	argonMaxMemory  = 256 * 1024
	argonMaxTime    = 10
	argonMaxThreads = 16
	bcryptMinCost   = 12
	bcryptMaxCost   = 15

	maxPasswordBytes = 1024
)

// HashPassword returns an argon2id PHC string for pw
// ($argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>).
func HashPassword(pw string) (string, error) {
	if pw == "" {
		return "", errors.New("auth: password must not be empty")
	}
	if len(pw) > maxPasswordBytes {
		return "", fmt.Errorf("auth: password longer than %d bytes", maxPasswordBytes)
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// passwordHash is a parsed, validated hash from users.yaml.
type passwordHash struct {
	raw string
	// argon2id
	argon         bool
	m, t          uint32
	p             uint8
	salt, derived []byte
}

// parsePasswordHash accepts argon2id (m ≥ 64 MiB, t ≥ 3) and bcrypt
// $2a$/$2b$ with cost ≥ 12. Anything else is rejected with a reason.
func parsePasswordHash(h string) (passwordHash, error) {
	switch {
	case strings.HasPrefix(h, "$argon2id$"):
		return parseArgon2id(h)
	case strings.HasPrefix(h, "$2a$"), strings.HasPrefix(h, "$2b$"):
		cost, err := bcrypt.Cost([]byte(h))
		if err != nil {
			return passwordHash{}, fmt.Errorf("invalid bcrypt hash: %w", err)
		}
		if cost < bcryptMinCost {
			return passwordHash{}, fmt.Errorf("bcrypt cost %d is below the minimum %d; rehash with `eddy hash-password`", cost, bcryptMinCost)
		}
		if cost > bcryptMaxCost {
			return passwordHash{}, fmt.Errorf("bcrypt cost %d is above the maximum %d", cost, bcryptMaxCost)
		}
		return passwordHash{raw: h}, nil
	case strings.HasPrefix(h, "$2y$"), strings.HasPrefix(h, "$2x$"), strings.HasPrefix(h, "$2$"):
		return passwordHash{}, errors.New("only $2a$/$2b$ bcrypt hashes are accepted (htpasswd $2y$ hashes can be relabelled $2b$)")
	case strings.HasPrefix(h, "$argon2i$"), strings.HasPrefix(h, "$argon2d$"):
		return passwordHash{}, errors.New("only argon2id is accepted, not argon2i/argon2d")
	case h == "":
		return passwordHash{}, errors.New("passwordHash is empty")
	default:
		return passwordHash{}, errors.New("unsupported hash algorithm: use argon2id (`eddy hash-password`) or bcrypt cost ≥12")
	}
}

func parseArgon2id(h string) (passwordHash, error) {
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	parts := strings.Split(h, "$")
	if len(parts) != 6 {
		return passwordHash{}, errors.New("malformed argon2id hash")
	}
	if parts[2] != "v=19" {
		return passwordHash{}, fmt.Errorf("unsupported argon2id version %q (need v=19)", parts[2])
	}
	var m, t, p uint64
	seen := map[string]bool{}
	for kv := range strings.SplitSeq(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || seen[k] {
			return passwordHash{}, errors.New("malformed argon2id parameters")
		}
		seen[k] = true
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return passwordHash{}, fmt.Errorf("malformed argon2id parameter %q", kv)
		}
		switch k {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			p = n
		default:
			return passwordHash{}, fmt.Errorf("unknown argon2id parameter %q", k)
		}
	}
	if !seen["m"] || !seen["t"] || !seen["p"] {
		return passwordHash{}, errors.New("argon2id hash must set m, t and p")
	}
	switch {
	case m < argonMemory || t < argonTime || p < 1:
		return passwordHash{}, fmt.Errorf("argon2id parameters m=%d,t=%d,p=%d are weaker than m=%d,t=%d,p=1", m, t, p, argonMemory, argonTime)
	case m > argonMaxMemory || t > argonMaxTime || p > argonMaxThreads:
		return passwordHash{}, fmt.Errorf("argon2id parameters m=%d,t=%d,p=%d exceed the maximum m=%d,t=%d,p=%d", m, t, p, argonMaxMemory, argonMaxTime, argonMaxThreads)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 16 {
		return passwordHash{}, errors.New("argon2id salt must be ≥16 bytes of unpadded base64")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return passwordHash{}, errors.New("argon2id key must be 16–64 bytes of unpadded base64")
	}
	return passwordHash{raw: h, argon: true, m: uint32(m), t: uint32(t), p: uint8(p), salt: salt, derived: key}, nil
}

// verify reports whether pw matches. The comparison is constant time.
func (h passwordHash) verify(pw string) bool {
	if len(pw) > maxPasswordBytes {
		return false
	}
	if h.argon {
		got := argon2.IDKey([]byte(pw), h.salt, h.t, h.m, h.p, uint32(len(h.derived)))
		return subtle.ConstantTimeCompare(got, h.derived) == 1
	}
	return bcrypt.CompareHashAndPassword([]byte(h.raw), []byte(pw)) == nil
}

// hasher runs password verification behind a semaphore so concurrent logins
// cannot exhaust memory (each argon2id run allocates 64 MiB).
type hasher struct {
	sem chan struct{}
}

// The dummy hash is process-wide: computing it costs one argon2id run.
var (
	dummyOnce sync.Once
	dummy     passwordHash
)

func newHasher(n int) *hasher {
	if n <= 0 {
		n = 4
	}
	return &hasher{sem: make(chan struct{}, n)}
}

// dummyHash is verified for unknown or disabled users so every failed login
// costs the same.
func dummyHash() passwordHash {
	dummyOnce.Do(func() {
		pw := make([]byte, 24)
		_, _ = rand.Read(pw)
		s, err := HashPassword(base64.RawStdEncoding.EncodeToString(pw))
		if err == nil {
			dummy, err = parsePasswordHash(s)
		}
		if err != nil {
			// Unreachable with a working CSPRNG; fall back to a fixed salt so
			// the timing still matches.
			salt := make([]byte, argonSaltLen)
			dummy = passwordHash{argon: true, m: argonMemory, t: argonTime, p: argonThreads, salt: salt, derived: make([]byte, argonKeyLen)}
		}
	})
	return dummy
}

// verify checks pw against ph (or the dummy hash when ph is nil). It returns
// ctx.Err() if the semaphore could not be acquired.
func (h *hasher) verify(ctx context.Context, ph *passwordHash, pw string) (bool, error) {
	select {
	case h.sem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-h.sem }()
	if ph == nil {
		d := dummyHash()
		d.verify(pw)
		return false, nil
	}
	return ph.verify(pw), nil
}
