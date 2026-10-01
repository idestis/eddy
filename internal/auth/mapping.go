package auth

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/idestis/eddy/internal/config"
)

// This file is the single identity mapping used by every provider (local,
// proxy, dev, GitHub and OIDC). It is a security boundary: its output is
// exactly what the agent impersonates.

var (
	// groupRE is the allowed shape of a raw group before prefixing.
	groupRE = regexp.MustCompile(`^[A-Za-z0-9._:/@-]+$`)
	// proxyUserRE and emailRE are the accepted shapes of a proxy user header value.
	proxyUserRE = regexp.MustCompile(`^[a-zA-Z0-9._@-]{1,128}$`)
	emailRE     = regexp.MustCompile(`^[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?)+$`)
)

const maxEmailLen = 254

// ErrInvalidIdentity is returned for user names that must not be impersonated.
var ErrInvalidIdentity = errors.New("auth: invalid identity")

// Mapper turns provider identities into Kubernetes users and groups.
type Mapper struct {
	prefix       string
	allUsers     string
	static       map[string][]string
	maxGroups    int
	maxGroupLen  int
	deny         []string
	localPrefix  string
	proxyPrefix  string
	log          *slog.Logger
	warnThrottle *logThrottle
}

// NewMapper builds a Mapper from the auth config.
func NewMapper(a config.Auth, log *slog.Logger) *Mapper {
	if log == nil {
		log = slog.Default()
	}
	m := &Mapper{
		prefix:       a.Groups.Prefix,
		allUsers:     a.Groups.AllUsers,
		static:       a.Groups.Static,
		maxGroups:    a.Groups.MaxGroups,
		maxGroupLen:  a.Groups.MaxGroupLength,
		deny:         a.DenyUserPrefixes,
		localPrefix:  a.Local.UserPrefix,
		proxyPrefix:  a.Proxy.UserPrefix,
		log:          log,
		warnThrottle: newLogThrottle(time.Minute),
	}
	if m.prefix == "" {
		m.prefix = "eddy:"
	}
	if m.maxGroups <= 0 {
		m.maxGroups = 64
	}
	if m.maxGroupLen <= 0 {
		m.maxGroupLen = 128
	}
	if m.localPrefix == "" {
		m.localPrefix = "local:"
	}
	return m
}

func validateRawGroup(g string) error {
	switch {
	case g == "":
		return errors.New("empty group")
	case !groupRE.MatchString(g):
		return fmt.Errorf("group must match %s", groupRE)
	case isSystem(g):
		return errors.New("system: groups are never allowed")
	}
	return nil
}

func isSystem(s string) bool { return strings.HasPrefix(strings.ToLower(s), "system:") }

// Groups maps raw provider groups to impersonation groups:
//  1. trim and validate each group; drop system:* (before prefixing),
//  2. add the prefix and drop system:* again (after prefixing),
//  3. always include <prefix><allUsers>,
//  4. add groups.static entries for any of identities,
//  5. dedupe, then drop groups over maxGroupLength and beyond maxGroups
//     (logged as a WARN).
//
// identities are the keys looked up in groups.static, typically the raw
// provider value (e.g. the email) and the mapped subject. Groups are never
// derived from the user name itself.
func (m *Mapper) Groups(raw []string, identities ...string) []string {
	out := make([]string, 0, len(raw)+2)
	seen := make(map[string]bool, len(raw)+2)
	var invalid, tooLong, overCap int

	add := func(g string) {
		g = strings.TrimSpace(g)
		if validateRawGroup(g) != nil {
			invalid++
			return
		}
		pg := m.prefix + g
		if isSystem(pg) || !groupRE.MatchString(pg) {
			invalid++
			return
		}
		if len(pg) > m.maxGroupLen {
			tooLong++
			return
		}
		if seen[pg] {
			return
		}
		if len(out) >= m.maxGroups {
			overCap++
			return
		}
		seen[pg] = true
		out = append(out, pg)
	}
	if m.allUsers != "" {
		add(m.allUsers)
	}
	for _, g := range raw {
		add(g)
	}
	seenID := map[string]bool{}
	for _, id := range identities {
		if id == "" || seenID[id] {
			continue
		}
		seenID[id] = true
		for _, g := range m.static[id] {
			add(g)
		}
	}
	if (invalid > 0 || tooLong > 0 || overCap > 0) && m.warnThrottle.allow(time.Now()) {
		m.log.Warn("dropped groups during mapping", "invalid", invalid, "tooLong", tooLong, "overCap", overCap,
			"maxGroups", m.maxGroups, "maxGroupLength", m.maxGroupLen)
	}
	return out
}

// LocalUser maps a local username to its subject (userPrefix + username).
func (m *Mapper) LocalUser(username string) (string, error) {
	if !usernameRE.MatchString(username) {
		return "", fmt.Errorf("%w: local username must match %s", ErrInvalidIdentity, usernameRE)
	}
	return m.checkSubject(m.localPrefix + username)
}

// ProxyUser maps a proxy user header value to its subject (proxy
// userPrefix + value). The value must be an email address or match
// ^[a-zA-Z0-9._@-]{1,128}$.
func (m *Mapper) ProxyUser(value string) (string, error) {
	if err := validateExternalUser(value); err != nil {
		return "", err
	}
	return m.checkSubject(m.proxyPrefix + value)
}

// OAuthUser maps a GitHub or OIDC user name to prefix + value. The value
// must be an email address or match ^[a-zA-Z0-9._@-]{1,128}$, like a proxy
// user; the prefix comes from config (and "github:" for GitHub logins).
func (m *Mapper) OAuthUser(prefix, value string) (string, error) {
	if err := validateExternalUser(value); err != nil {
		return "", err
	}
	return m.checkSubject(prefix + value)
}

// DevUser maps a dev fake-login user to "dev:<value>".
func (m *Mapper) DevUser(value string) (string, error) {
	if err := validateExternalUser(value); err != nil {
		return "", err
	}
	return m.checkSubject("dev:" + value)
}

func validateExternalUser(v string) error {
	if proxyUserRE.MatchString(v) {
		return nil
	}
	if len(v) <= maxEmailLen && emailRE.MatchString(v) {
		return nil
	}
	return fmt.Errorf("%w: user must be an email address or match %s", ErrInvalidIdentity, proxyUserRE)
}

// checkSubject enforces the rules on the final Kubernetes user name.
func (m *Mapper) checkSubject(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("%w: empty user", ErrInvalidIdentity)
	}
	if strings.Contains(s, ",") {
		return "", fmt.Errorf("%w: user contains a comma", ErrInvalidIdentity)
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return "", fmt.Errorf("%w: user contains a control character", ErrInvalidIdentity)
		}
	}
	if isSystem(s) {
		return "", fmt.Errorf("%w: system: users are never allowed", ErrInvalidIdentity)
	}
	ls := strings.ToLower(s)
	for _, d := range m.deny {
		if d != "" && strings.HasPrefix(ls, strings.ToLower(d)) {
			return "", fmt.Errorf("%w: user matches denyUserPrefixes", ErrInvalidIdentity)
		}
	}
	return s, nil
}

// validPrincipalGroups is a defence-in-depth filter applied to stored
// groups (sessions, PAT snapshots) before they are used again.
func (m *Mapper) validPrincipalGroups(gs []string) []string {
	out := make([]string, 0, len(gs))
	seen := map[string]bool{}
	for _, g := range gs {
		if !strings.HasPrefix(g, m.prefix) || isSystem(g) || !groupRE.MatchString(g) || len(g) > m.maxGroupLen || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	if len(out) > m.maxGroups {
		out = out[:m.maxGroups]
	}
	return out
}
