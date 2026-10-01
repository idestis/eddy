package config

import (
	"slices"
	"strings"
	"testing"
)

func TestHubOAuthConfig(t *testing.T) {
	const base = "publicURL: https://eddy.example.com\nstore: {driver: memory}\n"
	tests := []struct {
		name    string
		yaml    string
		wantErr string
		check   func(t *testing.T, h *Hub)
	}{
		{name: "github alone is a sign-in method", yaml: `auth:
  github: {enabled: true, clientID: abc, allowedOrganizations: [acme]}
`, check: func(t *testing.T, h *Hub) {
			g := h.Auth.GitHub
			if g.ClientSecretEnv != "GITHUB_CLIENT_SECRET" || g.Name != "GitHub" || !g.TeamsAsGroupsOrDefault() {
				t.Fatalf("github defaults %+v", g)
			}
			if h.Auth.Local.Mode != LocalModeNormal {
				t.Fatalf("local mode %q", h.Auth.Local.Mode)
			}
		}},
		{name: "github needs an org allowlist", yaml: "auth:\n  github: {enabled: true, clientID: abc}\n", wantErr: "allowedOrganizations is required"},
		{name: "github allowAllUsers", yaml: "auth:\n  github: {enabled: true, clientID: abc, allowAllUsers: true, teamsAsGroups: false}\n",
			check: func(t *testing.T, h *Hub) {
				if h.Auth.GitHub.TeamsAsGroupsOrDefault() {
					t.Fatal("teamsAsGroups false ignored")
				}
			}},
		{name: "github needs clientID", yaml: "auth:\n  github: {enabled: true, allowAllUsers: true}\n", wantErr: "clientID is required"},
		{name: "github secret env must be set", yaml: "auth:\n  github: {enabled: true, clientID: a, allowAllUsers: true, clientSecretEnv: NOPE_UNSET}\n", wantErr: "NOPE_UNSET"},
		{name: "github bad team", yaml: "auth:\n  github: {enabled: true, clientID: a, allowedOrganizations: [acme], allowedTeams: [platform]}\n", wantErr: "org/team-slug"},
		{name: "github GHES must be https", yaml: "auth:\n  github: {enabled: true, clientID: a, allowedOrganizations: [acme], baseURL: 'http://ghe.example.com'}\n", wantErr: "baseURL must be an https URL"},
		{name: "google preset", yaml: `auth:
  oidc:
    - {id: google, preset: google, clientID: x, allowedDomains: [example.com]}
`, check: func(t *testing.T, h *Hub) {
			o := h.Auth.OIDC[0]
			if o.Issuer != "https://accounts.google.com" || o.HostedDomainClaim != "hd" || o.Name != "Google" ||
				o.ClientSecretEnv != "OIDC_GOOGLE_CLIENT_SECRET" || !o.RequireVerifiedEmailOrDefault() || o.UsernameClaim != "email" {
				t.Fatalf("google preset %+v", o)
			}
		}},
		{name: "google needs allowedDomains", yaml: "auth:\n  oidc:\n    - {id: google, preset: google, clientID: x}\n", wantErr: "needs allowedDomains"},
		{name: "entra preset", yaml: `auth:
  oidc:
    - {id: corp-ad, preset: entra, issuer: 'https://login.microsoftonline.com/tid/v2.0', clientID: x}
`, check: func(t *testing.T, h *Hub) {
			o := h.Auth.OIDC[0]
			if o.UsernameClaim != "preferred_username" || o.RequireVerifiedEmailOrDefault() || o.GroupsClaim != "groups" ||
				o.ClientSecretEnv != "OIDC_CORP_AD_CLIENT_SECRET" {
				t.Fatalf("entra preset %+v", o)
			}
		}},
		{name: "dex preset over loopback http", yaml: "auth:\n  oidc:\n    - {id: dex, preset: dex, issuer: 'http://127.0.0.1:5556/dex', clientID: x}\n",
			check: func(t *testing.T, h *Hub) {
				if !slices.Contains(h.Auth.OIDC[0].Scopes, "groups") {
					t.Fatalf("dex scopes %v", h.Auth.OIDC[0].Scopes)
				}
			}},
		{name: "issuer must be https", yaml: "auth:\n  oidc:\n    - {id: okta, preset: okta, issuer: 'http://okta.example.com', clientID: x}\n", wantErr: "issuer must be an https URL"},
		{name: "reserved id", yaml: "auth:\n  oidc:\n    - {id: github, issuer: 'https://idp.example.com', clientID: x, clientSecretEnv: OIDC_GOOGLE_CLIENT_SECRET}\n", wantErr: "reserved"},
		{name: "bad id", yaml: "auth:\n  oidc:\n    - {id: 'Bad_ID', issuer: 'https://idp.example.com', clientID: x, clientSecretEnv: OIDC_GOOGLE_CLIENT_SECRET}\n", wantErr: "must match"},
		{name: "duplicate id", yaml: `auth:
  oidc:
    - {id: dex, preset: dex, issuer: 'https://dex.example.com', clientID: x}
    - {id: dex, preset: dex, issuer: 'https://dex.example.com', clientID: x}
`, wantErr: "used twice"},
		{name: "unknown preset", yaml: "auth:\n  oidc:\n    - {id: kc, preset: keycloak, issuer: 'https://kc.example.com', clientID: x, clientSecretEnv: OIDC_GOOGLE_CLIENT_SECRET}\n", wantErr: "not supported"},
		{name: "scopes need openid", yaml: "auth:\n  oidc:\n    - {id: dex, issuer: 'https://dex.example.com', clientID: x, scopes: [email]}\n", wantErr: "must include openid"},
		{name: "breakglass needs another method", yaml: "auth:\n  local: {enabled: true, usersFile: /u, mode: breakglass}\n", wantErr: "breakglass needs another"},
		{name: "breakglass with github", yaml: `auth:
  local: {enabled: true, usersFile: /u, mode: breakglass, allowedUsers: [admin]}
  github: {enabled: true, clientID: abc, allowedOrganizations: [acme]}
`, check: func(t *testing.T, h *Hub) {
			if h.Auth.Local.Mode != LocalModeBreakglass || len(h.Auth.Local.AllowedUsers) != 1 {
				t.Fatalf("local %+v", h.Auth.Local)
			}
		}},
		{name: "bad local mode", yaml: "auth:\n  local: {enabled: true, usersFile: /u, mode: hidden}\n", wantErr: "normal or breakglass"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITHUB_CLIENT_SECRET", "s3cret")
			t.Setenv("OIDC_GOOGLE_CLIENT_SECRET", "s3cret")
			t.Setenv("OIDC_CORP_AD_CLIENT_SECRET", "s3cret")
			t.Setenv("OIDC_DEX_CLIENT_SECRET", "s3cret")
			t.Setenv("OIDC_OKTA_CLIENT_SECRET", "s3cret")
			h, err := ParseHub([]byte(base + tc.yaml))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, h)
		})
	}
}
