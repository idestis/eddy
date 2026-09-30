// Package identity defines the authenticated principal that flows from the
// auth layer through the hub, MCP and Ask AI down to agent impersonation.
package identity

import (
	"context"
	"slices"
)

// Via records which channel a request came through. It is written to audit.
type Via string

const (
	ViaWeb    Via = "web"
	ViaMCP    Via = "mcp"
	ViaAskAI  Via = "askai"
	ViaSystem Via = "system"
)

// Scope limits what a personal access token may do. Browser sessions have
// both scopes; Kubernetes RBAC still decides in the end.
type Scope string

const (
	ScopeRead    Scope = "read"    // read tools and thread writes
	ScopeOperate Scope = "operate" // reconcile, suspend, resume
)

// Principal is a signed-in user after group mapping. User and Groups are
// exactly what the agent impersonates.
type Principal struct {
	User     string   `json:"user"`
	Groups   []string `json:"groups"`   // already prefixed (eddy:...), no system:*
	Display  string   `json:"display"`  // human-friendly name for the UI
	Provider string   `json:"provider"` // local | proxy | dev
	Via      Via      `json:"via"`
	TokenID  string   `json:"tokenId,omitempty"` // PAT id when Via == ViaMCP
	Scopes   []Scope  `json:"scopes,omitempty"`
	Client   string   `json:"client,omitempty"` // MCP clientInfo.name or AI model id; untrusted
}

// Has reports whether the principal carries scope s.
func (p Principal) Has(s Scope) bool { return slices.Contains(p.Scopes, s) }

type ctxKey struct{}

// With returns a context carrying p.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// From returns the principal in ctx, if any.
func From(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}
