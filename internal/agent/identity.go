package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/eddy-gitops/eddy/internal/protocol"
)

// Policy is the agent's own check on who it may impersonate. The hub applies
// the same rules; the agent re-checks them so that a compromised or buggy hub
// cannot make it act as a privileged identity.
type Policy struct {
	// AllowedGroupPrefixes: every group must start with one of these.
	AllowedGroupPrefixes []string
	// AllowedGroups, when set, is an exact allowlist on top of the prefixes.
	AllowedGroups []string
	// DenyUserPrefixes rejects matching user names in addition to "system:".
	DenyUserPrefixes []string
}

// Validate returns a 403 protocol error when id must not be impersonated:
// an empty user, a "system:" or denied user, a "system:" group, or a group
// outside the allowed prefixes or the exact allowlist.
func (p Policy) Validate(id protocol.Identity) *protocol.Error {
	deny := func(format string, args ...any) *protocol.Error {
		return &protocol.Error{Code: 403, Message: "agent: refusing to impersonate: " + fmt.Sprintf(format, args...)}
	}
	if strings.TrimSpace(id.User) == "" {
		return deny("empty user")
	}
	if strings.HasPrefix(id.User, "system:") {
		return deny("system user %q", id.User)
	}
	for _, prefix := range p.DenyUserPrefixes {
		if prefix != "" && strings.HasPrefix(id.User, prefix) {
			return deny("user %q matches a denied prefix", id.User)
		}
	}
	for _, g := range id.Groups {
		if strings.HasPrefix(g, "system:") {
			return deny("system group %q", g)
		}
		if !slices.ContainsFunc(p.AllowedGroupPrefixes, func(prefix string) bool {
			return prefix != "" && strings.HasPrefix(g, prefix)
		}) {
			return deny("group %q has no allowed prefix", g)
		}
		if len(p.AllowedGroups) > 0 && !slices.Contains(p.AllowedGroups, g) {
			return deny("group %q is not in the allowed groups", g)
		}
	}
	return nil
}
