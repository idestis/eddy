package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

// usernameRE is the local username rule from ADR-0003 §3.1.
var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// User is one validated entry of users.yaml.
type User struct {
	Username string
	Subject  string   // mapped Kubernetes user, e.g. local:alice
	Groups   []string // raw (unprefixed) groups
	Disabled bool
	hash     passwordHash
}

type userSet struct {
	byName    map[string]*User
	bySubject map[string]*User
}

type usersFile struct {
	Users []struct {
		Username     string   `json:"username"`
		PasswordHash string   `json:"passwordHash"`
		Groups       []string `json:"groups"`
		Disabled     bool     `json:"disabled"`
	} `json:"users"`
}

// ParseUsers parses and validates users.yaml content; m supplies the
// subject rules. Every problem is reported, not just the first.
func ParseUsers(b []byte, m *Mapper) (map[string]*User, error) {
	var f usersFile
	if err := yaml.UnmarshalStrict(b, &f); err != nil {
		return nil, fmt.Errorf("auth: parse users file: %w", err)
	}
	out := make(map[string]*User, len(f.Users))
	var errs []error
	for i, u := range f.Users {
		where := fmt.Sprintf("users[%d]", i)
		if u.Username != "" {
			where = fmt.Sprintf("users[%d] (%s)", i, truncate(u.Username, 64))
		}
		if !usernameRE.MatchString(u.Username) {
			errs = append(errs, fmt.Errorf("%s: username must match %s", where, usernameRE))
			continue
		}
		if _, dup := out[u.Username]; dup {
			errs = append(errs, fmt.Errorf("%s: duplicate username", where))
			continue
		}
		subject, err := m.LocalUser(u.Username)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		ph, err := parsePasswordHash(u.PasswordHash)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", where, err))
			continue
		}
		var gerr error
		for _, g := range u.Groups {
			if err := validateRawGroup(strings.TrimSpace(g)); err != nil {
				gerr = errors.Join(gerr, fmt.Errorf("%s: group %q: %w", where, truncate(g, 64), err))
			}
		}
		if gerr != nil {
			errs = append(errs, gerr)
			continue
		}
		out[u.Username] = &User{Username: u.Username, Subject: subject, Groups: u.Groups, Disabled: u.Disabled, hash: ph}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("auth: invalid users file: %w", err)
	}
	return out, nil
}

// loadUsers reads the users file and swaps it in if the content changed.
// On a reload it kills the sessions and PATs of users that were removed,
// disabled or had their password changed.
func (s *Service) loadUsers(ctx context.Context, initial bool) error {
	b, err := os.ReadFile(s.cfg.Auth.Local.UsersFile)
	if err != nil {
		return fmt.Errorf("auth: read users file: %w", err)
	}
	sum := sha256.Sum256(b)
	if prev, ok := s.usersHash.Load().([32]byte); ok && prev == sum {
		return nil
	}
	users, err := ParseUsers(b, s.mapper)
	if err != nil {
		return err
	}
	next := &userSet{byName: users, bySubject: make(map[string]*User, len(users))}
	for _, u := range users {
		next.bySubject[u.Subject] = u
	}
	old := s.users.Swap(next)
	s.usersHash.Store(sum)
	if initial || old == nil {
		s.log.Info("users file loaded", "users", len(users))
		return nil
	}
	s.log.Info("users file reloaded", "users", len(users))
	for name, ou := range old.byName {
		nu, ok := users[name]
		var reason string
		switch {
		case !ok:
			reason = "removed"
		case nu.Disabled && !ou.Disabled:
			reason = "disabled"
		case nu.hash.raw != ou.hash.raw:
			reason = "password changed"
		default:
			continue
		}
		s.log.Info("revoking sessions and tokens of local user", "user", ou.Subject, "reason", reason)
		if err := s.RevokeUser(ctx, ou.Subject); err != nil {
			s.log.Error("revoke local user failed", "user", ou.Subject, "err", err)
		}
	}
	return nil
}

// localAllowed applies auth.local.allowedUsers (empty allows everyone).
func (s *Service) localAllowed(username string) bool {
	return len(s.allowedLocal) == 0 || s.allowedLocal[username]
}

// localUser returns the enabled local user for subject, or nil.
func (s *Service) localUser(subject string) *User {
	us := s.users.Load()
	if us == nil {
		return nil
	}
	u := us.bySubject[subject]
	if u == nil || u.Disabled || !s.localAllowed(u.Username) {
		return nil
	}
	return u
}
