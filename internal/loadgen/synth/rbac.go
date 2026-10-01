package synth

import (
	"fmt"
	"slices"
	"strings"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/protocol"
)

// Role is a simulated user's RBAC shape.
type Role string

const (
	// RoleAdmin may list everything cluster-wide (a ClusterRoleBinding).
	RoleAdmin Role = "admin"
	// RoleTeam may list everything in its team's namespaces only
	// (RoleBindings), nothing cluster-wide.
	RoleTeam Role = "team"
	// RoleMixed may list Flux kinds cluster-wide and everything else in
	// its team's namespaces.
	RoleMixed Role = "mixed"
)

// Group names of the policy, as the hub sends them (with the eddy: prefix).
const (
	GroupAdmin       = "eddy:platform"
	GroupFluxViewers = "eddy:flux-viewers"
	groupTeamPrefix  = "eddy:team-"
)

// User is one simulated user.
type User struct {
	Name   string
	Role   Role
	Team   int
	Groups []string
}

// Users returns n users: 20 % admins, 50 % team users and 30 % mixed.
func Users(n int) []User { return UsersOf(n, "") }

// UsersOf returns n users of one role, or the default mix when role is "".
func UsersOf(n int, only Role) []User {
	out := make([]User, n)
	for i := range out {
		role := RoleTeam
		switch i % 10 {
		case 0, 5:
			role = RoleAdmin
		case 1, 6, 8:
			role = RoleMixed
		}
		if only != "" {
			role = only
		}
		u := User{Name: fmt.Sprintf("user%03d@example.com", i), Role: role, Team: i % Teams}
		team := fmt.Sprintf("%s%02d", groupTeamPrefix, u.Team)
		switch role {
		case RoleAdmin:
			u.Groups = []string{GroupAdmin}
		case RoleTeam:
			u.Groups = []string{team}
		case RoleMixed:
			u.Groups = []string{GroupFluxViewers, team}
		}
		out[i] = u
	}
	return out
}

// Allow is the fake SubjectAccessReview answer for id. It follows
// Kubernetes RBAC: a cluster-wide grant also answers every namespace.
func Allow(id protocol.Identity, c protocol.AccessCheck) bool {
	if c.Verb != "list" && c.Verb != "get" && c.Verb != "watch" {
		return slices.Contains(id.Groups, GroupAdmin)
	}
	if slices.Contains(id.Groups, GroupAdmin) {
		return true
	}
	if slices.Contains(id.Groups, GroupFluxViewers) && strings.HasSuffix(c.Group, ".toolkit.fluxcd.io") {
		return true
	}
	if c.Namespace == "" {
		return false
	}
	for _, g := range id.Groups {
		if t, ok := strings.CutPrefix(g, groupTeamPrefix); ok && strings.HasPrefix(c.Namespace, "team-"+t+"-") {
			return true
		}
	}
	return false
}

// Rules is the fake SelfSubjectRulesReview of id in namespace, consistent
// with Allow. Like a real impersonated review it includes what every
// authenticated user may do (create self reviews), which the hub's
// baseline user also gets.
func Rules(id protocol.Identity, namespace string) []protocol.ResourceRule {
	read := []string{"get", "list", "watch"}
	out := []protocol.ResourceRule{{
		Verbs: []string{"create"}, APIGroups: []string{"authorization.k8s.io"},
		Resources: []string{"selfsubjectaccessreviews", "selfsubjectrulesreviews"},
	}}
	if slices.Contains(id.Groups, GroupAdmin) {
		out = append(out, protocol.ResourceRule{Verbs: []string{"*"}, APIGroups: []string{"*"}, Resources: []string{"*"}})
	}
	if slices.Contains(id.Groups, GroupFluxViewers) {
		out = append(out, protocol.ResourceRule{Verbs: read, APIGroups: fluxGroups(), Resources: []string{"*"}})
	}
	for _, g := range id.Groups {
		if t, ok := strings.CutPrefix(g, groupTeamPrefix); ok && strings.HasPrefix(namespace, "team-"+t+"-") {
			out = append(out, protocol.ResourceRule{Verbs: read, APIGroups: []string{"*"}, Resources: []string{"*"}})
		}
	}
	return out
}

// fluxGroups lists the API groups of the kind table ending in
// .toolkit.fluxcd.io.
func fluxGroups() []string {
	var out []string
	for _, k := range flux.All() {
		if strings.HasSuffix(k.Group, ".toolkit.fluxcd.io") && !slices.Contains(out, k.Group) {
			out = append(out, k.Group)
		}
	}
	return out
}
