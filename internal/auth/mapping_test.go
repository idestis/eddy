package auth

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/config"
)

func testMapper(mut func(*config.Auth)) *Mapper {
	a := config.Auth{
		Local:            config.LocalAuth{UserPrefix: "local:"},
		Proxy:            config.ProxyAuth{UserPrefix: ""},
		Groups:           config.Groups{Prefix: "eddy:", AllUsers: "authenticated", MaxGroups: 64, MaxGroupLength: 128},
		DenyUserPrefixes: []string{"system:", "eks:", "kubernetes-admin"},
	}
	if mut != nil {
		mut(&a)
	}
	return NewMapper(a, discardLog())
}

func TestMapperGroups(t *testing.T) {
	tests := []struct {
		name  string
		mut   func(*config.Auth)
		raw   []string
		ids   []string
		want  []string
		check func(t *testing.T, got []string)
	}{
		{name: "none", want: []string{"eddy:authenticated"}},
		{name: "prefix and trim", raw: []string{" platform ", "oncall"}, want: []string{"eddy:authenticated", "eddy:platform", "eddy:oncall"}},
		{name: "system dropped before prefix", raw: []string{"system:masters", "SYSTEM:masters", "System:authenticated", "ok"}, want: []string{"eddy:authenticated", "eddy:ok"}},
		{name: "system dropped after prefix", mut: func(a *config.Auth) { a.Groups.Prefix = "system:" }, raw: []string{"masters"}, want: []string{}},
		{name: "invalid characters dropped", raw: []string{"a b", "a,b", "a\nb", "a\x00b", "ünï", "", "   ", "a*b", "ok/team@corp"}, want: []string{"eddy:authenticated", "eddy:ok/team@corp"}},
		{name: "already prefixed is prefixed again", raw: []string{"eddy:admin"}, want: []string{"eddy:authenticated", "eddy:eddy:admin"}},
		{name: "dedupe", raw: []string{"a", "a", " a", "authenticated"}, want: []string{"eddy:authenticated", "eddy:a"}},
		{name: "static by identity", mut: func(a *config.Auth) {
			a.Groups.Static = map[string][]string{"alice@example.com": {"oncall", "system:masters"}, "local:bob": {"x"}}
		}, raw: []string{"dev"}, ids: []string{"alice@example.com", "alice@example.com"}, want: []string{"eddy:authenticated", "eddy:dev", "eddy:oncall"}},
		{name: "static by subject", mut: func(a *config.Auth) { a.Groups.Static = map[string][]string{"local:bob": {"x"}} },
			ids: []string{"bob", "local:bob"}, want: []string{"eddy:authenticated", "eddy:x"}},
		{name: "too long dropped", mut: func(a *config.Auth) { a.Groups.MaxGroupLength = 10 },
			raw: []string{"short", "waytoolonggroup"}, want: []string{"eddy:short"}},
		{name: "cap keeps allUsers first", mut: func(a *config.Auth) { a.Groups.MaxGroups = 3 },
			raw: []string{"a", "b", "c", "d"}, want: []string{"eddy:authenticated", "eddy:a", "eddy:b"}},
		{name: "no allUsers", mut: func(a *config.Auth) { a.Groups.AllUsers = "" }, raw: []string{"a"}, want: []string{"eddy:a"}},
		{name: "never derived from username", ids: []string{"alice"}, raw: nil, want: []string{"eddy:authenticated"}},
		{name: "big input capped", raw: func() []string {
			var r []string
			for i := range 500 {
				r = append(r, strings.Repeat("g", 1+i%5)+string(rune('a'+i%26)))
			}
			return r
		}(), check: func(t *testing.T, got []string) {
			if len(got) > 64 {
				t.Fatalf("got %d groups, want ≤64", len(got))
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := testMapper(tt.mut).Groups(tt.raw, tt.ids...)
			for _, g := range got {
				if strings.HasPrefix(strings.ToLower(g), "system:") {
					t.Fatalf("system group leaked: %q", g)
				}
			}
			if tt.check != nil {
				tt.check(t, got)
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMapperUsers(t *testing.T) {
	tests := []struct {
		name    string
		mut     func(*config.Auth)
		kind    string // local | proxy | dev
		in      string
		want    string
		wantErr bool
	}{
		{name: "local ok", kind: "local", in: "alice", want: "local:alice"},
		{name: "local dots", kind: "local", in: "a.b-c_d", want: "local:a.b-c_d"},
		{name: "local uppercase", kind: "local", in: "Alice", wantErr: true},
		{name: "local leading dot", kind: "local", in: ".alice", wantErr: true},
		{name: "local empty", kind: "local", in: "", wantErr: true},
		{name: "local too long", kind: "local", in: strings.Repeat("a", 64), wantErr: true},
		{name: "local 63", kind: "local", in: strings.Repeat("a", 63), want: "local:" + strings.Repeat("a", 63)},
		{name: "local colon", kind: "local", in: "a:b", wantErr: true},
		{name: "local denied prefix", kind: "local", mut: func(a *config.Auth) { a.Local.UserPrefix = "system:" }, in: "alice", wantErr: true},
		{name: "local deny config", kind: "local", mut: func(a *config.Auth) { a.DenyUserPrefixes = []string{"local:adm"} }, in: "admin", wantErr: true},

		{name: "proxy email", kind: "proxy", in: "alice@example.com", want: "alice@example.com"},
		{name: "proxy email plus", kind: "proxy", in: "alice+eddy@corp.example.com", want: "alice+eddy@corp.example.com"},
		{name: "proxy plain", kind: "proxy", in: "alice.smith", want: "alice.smith"},
		{name: "proxy prefix", kind: "proxy", mut: func(a *config.Auth) { a.Proxy.UserPrefix = "sso:" }, in: "alice@example.com", want: "sso:alice@example.com"},
		{name: "proxy comma", kind: "proxy", in: "alice@example.com,system:admin", wantErr: true},
		{name: "proxy colon", kind: "proxy", in: "system:admin", wantErr: true},
		{name: "proxy control", kind: "proxy", in: "alice\n@example.com", wantErr: true},
		{name: "proxy tab", kind: "proxy", in: "alice\t", wantErr: true},
		{name: "proxy nul", kind: "proxy", in: "alice\x00", wantErr: true},
		{name: "proxy space", kind: "proxy", in: "alice smith", wantErr: true},
		{name: "proxy empty", kind: "proxy", in: "", wantErr: true},
		{name: "proxy unicode", kind: "proxy", in: "аlice@example.com", wantErr: true},
		{name: "proxy too long", kind: "proxy", in: strings.Repeat("a", 129), wantErr: true},
		{name: "proxy display-name email", kind: "proxy", in: "Alice <alice@example.com>", wantErr: true},
		{name: "proxy kubernetes-admin", kind: "proxy", in: "kubernetes-admin", wantErr: true},
		{name: "proxy kubernetes-admin case", kind: "proxy", in: "Kubernetes-Admin@x.io", wantErr: true},
		{name: "proxy prefix makes system", kind: "proxy", mut: func(a *config.Auth) { a.Proxy.UserPrefix = "system:" }, in: "alice", wantErr: true},
		{name: "proxy prefix makes eks", kind: "proxy", mut: func(a *config.Auth) { a.Proxy.UserPrefix = "eks:" }, in: "alice", wantErr: true},
		{name: "proxy prefix with comma", kind: "proxy", mut: func(a *config.Auth) { a.Proxy.UserPrefix = "a,b:" }, in: "alice", wantErr: true},

		{name: "dev ok", kind: "dev", in: "dev", want: "dev:dev"},
		{name: "dev bad", kind: "dev", in: "a b", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testMapper(tt.mut)
			var got string
			var err error
			switch tt.kind {
			case "local":
				got, err = m.LocalUser(tt.in)
			case "proxy":
				got, err = m.ProxyUser(tt.in)
			case "dev":
				got, err = m.DevUser(tt.in)
			}
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %q, want error", got)
				}
				if !errors.Is(err, ErrInvalidIdentity) {
					t.Fatalf("error %v is not ErrInvalidIdentity", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestValidPrincipalGroups(t *testing.T) {
	m := testMapper(nil)
	got := m.validPrincipalGroups([]string{"eddy:a", "system:masters", "other:x", "eddy:a", "eddy:b c", "eddy:system:x"})
	want := []string{"eddy:a", "eddy:system:x"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestValidReturnTo(t *testing.T) {
	tests := map[string]bool{
		"/":                             true,
		"/clusters/prod?x=1#a":          true,
		"/a//b":                         true,
		"":                              false,
		"//evil.example":                false,
		"/\\evil.example":               false,
		"https://evil.example":          false,
		"evil.example":                  false,
		"javascript:alert(1)":           false,
		"/\x00":                         false,
		"/a\nb":                         false,
		" /":                            false,
		"/" + strings.Repeat("a", 3000): false,
	}
	for in, want := range tests {
		if got := ValidReturnTo(in); got != want {
			t.Errorf("ValidReturnTo(%q) = %v, want %v", in, got, want)
		}
	}
}
