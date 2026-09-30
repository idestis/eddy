package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/store"
)

var scannerRE = regexp.MustCompile(`^eddy_pat_[0-9A-Za-z]{50}$`)

func webAlice() identity.Principal {
	return identity.Principal{User: "local:alice", Display: "alice", Provider: ProviderLocal, Via: identity.ViaWeb,
		Groups: []string{"eddy:authenticated", "eddy:platform", "eddy:oncall"}}
}

func TestPATFormatAndChecksum(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		tok, id, secret, err := newPAT()
		if err != nil {
			t.Fatal(err)
		}
		if !scannerRE.MatchString(tok) || seen[tok] {
			t.Fatalf("bad or repeated token %q", tok)
		}
		seen[tok] = true
		gid, gsec, ok := parsePAT(tok)
		if !ok || gid != id || gsec != secret {
			t.Fatalf("parse(%q) = %q %q %v", tok, gid, gsec, ok)
		}
		// Any single-character change must break the checksum or the alphabet.
		for _, pos := range []int{len(patPrefix), len(patPrefix) + 20, len(tok) - 1} {
			b := []byte(tok)
			if b[pos] == 'a' {
				b[pos] = 'b'
			} else {
				b[pos] = 'a'
			}
			if _, _, ok := parsePAT(string(b)); ok {
				t.Fatalf("mutated token %q accepted", b)
			}
		}
	}
	for _, bad := range []string{"", "eddy_pat_", "ghp_" + strings.Repeat("a", 50), "eddy_pat_" + strings.Repeat("a", 49),
		"eddy_pat_" + strings.Repeat("a", 51), "eddy_pat_" + strings.Repeat("-", 50), "EDDY_PAT_" + strings.Repeat("a", 50)} {
		if _, _, ok := parsePAT(bad); ok {
			t.Fatalf("parsePAT(%q) accepted", bad)
		}
	}
	if got := patChecksum(""); len(got) != patCRCLen || got != "000000" {
		t.Fatalf("checksum padding: %q", got)
	}
}

func TestIssueValidation(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { enableProxy(t, c) })
	ctx := context.Background()
	proxy := identity.Principal{User: "alice@example.com", Provider: ProviderProxy, Via: identity.ViaWeb, Groups: []string{"eddy:authenticated"}}
	tests := []struct {
		name    string
		p       identity.Principal
		tname   string
		scopes  []string
		ttl     time.Duration
		wantErr error
		want    []string
	}{
		{name: "read", p: webAlice(), tname: "cli", scopes: []string{"read"}, want: []string{"read"}},
		{name: "operate implies read", p: webAlice(), tname: "cli", scopes: []string{"operate"}, want: []string{"read", "operate"}},
		{name: "dupes", p: webAlice(), tname: "cli", scopes: []string{"read", "read", "operate"}, want: []string{"read", "operate"}},
		{name: "no scopes", p: webAlice(), tname: "cli", wantErr: ErrBadRequest},
		{name: "bad scope", p: webAlice(), tname: "cli", scopes: []string{"admin"}, wantErr: ErrBadRequest},
		{name: "empty name", p: webAlice(), tname: " ", scopes: []string{"read"}, wantErr: ErrBadRequest},
		{name: "control in name", p: webAlice(), tname: "a\nb", scopes: []string{"read"}, wantErr: ErrBadRequest},
		{name: "long name", p: webAlice(), tname: strings.Repeat("n", 65), scopes: []string{"read"}, wantErr: ErrBadRequest},
		{name: "local 90d ok", p: webAlice(), tname: "cli", scopes: []string{"read"}, ttl: 90 * 24 * time.Hour, want: []string{"read"}},
		{name: "local 91d", p: webAlice(), tname: "cli", scopes: []string{"read"}, ttl: 91 * 24 * time.Hour, wantErr: ErrBadRequest},
		{name: "proxy 30d ok", p: proxy, tname: "cli", scopes: []string{"read"}, ttl: 30 * 24 * time.Hour, want: []string{"read"}},
		{name: "proxy 31d", p: proxy, tname: "cli", scopes: []string{"read"}, ttl: 31 * 24 * time.Hour, wantErr: ErrBadRequest},
		{name: "negative ttl", p: webAlice(), tname: "cli", scopes: []string{"read"}, ttl: -time.Hour, wantErr: ErrBadRequest},
		{name: "from a PAT", p: func() identity.Principal { p := webAlice(); p.Via = identity.ViaMCP; return p }(), tname: "cli", scopes: []string{"read"}, wantErr: ErrBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok, row, err := e.svc.Issue(ctx, tt.p, tt.tname, tt.scopes, tt.ttl)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !scannerRE.MatchString(tok) || row.Hash != nil || !slices.Equal(row.Scopes, tt.want) {
				t.Fatalf("tok %q row %+v", tok, row)
			}
			if tt.ttl == 0 && !row.ExpiresAt.Equal(row.CreatedAt.Add(30*24*time.Hour)) {
				t.Fatalf("default ttl not applied: %v", row.ExpiresAt.Sub(row.CreatedAt))
			}
			stored := e.st.tok.m[row.ID]
			if strings.Contains(string(stored.Hash), tok[len(patPrefix)+patIDLen:len(tok)-patCRCLen]) || len(stored.Hash) != 32 {
				t.Fatal("stored hash looks like plaintext")
			}
		})
	}
}

func TestIssueMaxPerUser(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	for i := range e.cfg.Auth.Tokens.MaxPerUser {
		if _, _, err := e.svc.Issue(ctx, webAlice(), "t", []string{"read"}, time.Hour); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}
	if _, _, err := e.svc.Issue(ctx, webAlice(), "t", []string{"read"}, time.Hour); !errors.Is(err, ErrTokenLimit) {
		t.Fatalf("err %v, want ErrTokenLimit", err)
	}
	// Expired tokens stop counting.
	e.clock.Add(2 * time.Hour)
	if _, _, err := e.svc.Issue(ctx, webAlice(), "t", []string{"read"}, time.Hour); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPAT(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	tok, row, err := e.svc.Issue(ctx, webAlice(), "cli", []string{"operate"}, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, exp, err := e.svc.VerifyPAT(ctx, tok)
	if err != nil {
		t.Fatal(err)
	}
	if p.User != "local:alice" || p.Via != identity.ViaMCP || p.TokenID != row.ID || !exp.Equal(row.ExpiresAt) ||
		!p.Has(identity.ScopeRead) || !p.Has(identity.ScopeOperate) || p.Provider != ProviderLocal {
		t.Fatalf("principal %+v exp %v", p, exp)
	}
	if want := []string{"eddy:authenticated", "eddy:platform", "eddy:oncall"}; !slices.Equal(p.Groups, want) {
		t.Fatalf("groups %q", p.Groups)
	}

	// Right id and a valid checksum but the wrong secret.
	id, secret, _ := parsePAT(tok)
	other := strings.Repeat("Z", patSecretLen)
	if other == secret {
		other = strings.Repeat("Y", patSecretLen)
	}
	forged := patPrefix + id + other + patChecksum(id+other)
	if _, _, err := e.svc.VerifyPAT(ctx, forged); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("forged secret: %v", err)
	}
	// Unknown id with valid checksum.
	unk := strings.Repeat("A", patIDLen)
	if _, _, err := e.svc.VerifyPAT(ctx, patPrefix+unk+secret+patChecksum(unk+secret)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown id: %v", err)
	}
	// Checksum typo.
	repl := "0"
	if tok[len(tok)-1] == '0' {
		repl = "1"
	}
	if _, _, err := e.svc.VerifyPAT(ctx, tok[:len(tok)-1]+repl); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("typo: %v", err)
	}

	// MarkUsed is throttled to once per 5 minutes.
	marks := e.st.tok.marks
	e.svc.VerifyPAT(ctx, tok)
	if e.st.tok.marks != marks {
		t.Fatal("MarkUsed not throttled")
	}
	e.clock.Add(6 * time.Minute)
	e.svc.VerifyPAT(ctx, tok)
	if e.st.tok.marks != marks+1 {
		t.Fatal("MarkUsed not called after 5 minutes")
	}

	// Expiry.
	e.clock.Add(2 * time.Hour)
	if _, _, err := e.svc.VerifyPAT(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired: %v", err)
	}
}

func TestVerifyPATRevocation(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	tok, row, _ := e.svc.Issue(ctx, webAlice(), "cli", []string{"read"}, time.Hour)
	if err := e.svc.RevokeToken(ctx, "local:bob", row.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke other user's token: %v", err)
	}
	if err := e.svc.RevokeToken(ctx, "local:alice", row.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.VerifyPAT(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("revoked: %v", err)
	}
	tok2, _, _ := e.svc.Issue(ctx, webAlice(), "cli", []string{"read"}, time.Hour)
	if err := e.svc.RevokeUser(ctx, "local:alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.VerifyPAT(ctx, tok2); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("after RevokeUser: %v", err)
	}
}

func TestVerifyPATLiveLocalGroups(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	tok, _, _ := e.svc.Issue(ctx, webAlice(), "cli", []string{"read"}, time.Hour)

	// Groups change in users.yaml: used live on the next call.
	content := strings.Replace(usersYAML(t), "groups: [platform, oncall]", "groups: [platform]", 1)
	if err := os.WriteFile(e.cfg.Auth.Local.UsersFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.loadUsers(ctx, false); err != nil {
		t.Fatal(err)
	}
	p, _, err := e.svc.VerifyPAT(ctx, tok)
	if err != nil || !slices.Equal(p.Groups, []string{"eddy:authenticated", "eddy:platform"}) {
		t.Fatalf("groups %q err %v", p.Groups, err)
	}

	// Removed user: token invalid. (Swap the user set directly so the
	// reload's revocation does not mask the live check.)
	e.svc.users.Store(&userSet{byName: map[string]*User{}, bySubject: map[string]*User{}})
	if _, _, err := e.svc.VerifyPAT(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("missing user: %v", err)
	}
	// Disabled user: token invalid.
	u := &User{Username: "alice", Subject: "local:alice", Disabled: true}
	e.svc.users.Store(&userSet{byName: map[string]*User{"alice": u}, bySubject: map[string]*User{"local:alice": u}})
	if _, _, err := e.svc.VerifyPAT(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("disabled user: %v", err)
	}
}

func TestVerifyPATProxyGroupsShrink(t *testing.T) {
	e := newEnv(t, func(c *config.Hub) { enableProxy(t, c) })
	ctx := context.Background()
	p := identity.Principal{User: "alice@example.com", Provider: ProviderProxy, Via: identity.ViaWeb,
		Groups: []string{"eddy:authenticated", "eddy:dev", "eddy:ops"}}
	tok, _, err := e.svc.Issue(ctx, p, "cli", []string{"read"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// No session on record: the snapshot is used.
	got, _, err := e.svc.VerifyPAT(ctx, tok)
	if err != nil || !slices.Equal(got.Groups, p.Groups) {
		t.Fatalf("no session: %q %v", got.Groups, err)
	}
	// Latest session lost eddy:ops and gained eddy:admin: result only shrinks.
	if _, err := e.svc.createSession(ctx, httptestRequest(), p.User, p.User, ProviderProxy,
		[]string{"eddy:authenticated", "eddy:dev", "eddy:admin"}); err != nil {
		t.Fatal(err)
	}
	got, _, err = e.svc.VerifyPAT(ctx, tok)
	if err != nil || !slices.Equal(got.Groups, []string{"eddy:authenticated", "eddy:dev"}) {
		t.Fatalf("shrunk groups %q %v", got.Groups, err)
	}
	// Tampered snapshot with a system group never comes back.
	for id, row := range e.st.tok.m {
		row.Groups = append(row.Groups, "system:masters")
		e.st.tok.m[id] = row
	}
	e.st.sess.m = map[string]store.Session{}
	got, _, _ = e.svc.VerifyPAT(ctx, tok)
	if slices.Contains(got.Groups, "system:masters") {
		t.Fatal("system group leaked from snapshot")
	}
	// Proxy auth turned off: proxy tokens stop working.
	e.cfg.Auth.Proxy.Enabled = false
	if _, _, err := e.svc.VerifyPAT(ctx, tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("proxy disabled: %v", err)
	}
}

func TestTokenRoutes(t *testing.T) {
	e := newEnv(t, nil)
	h := e.handler()
	jar, csrf := e.localSession(t)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Origin", testPublicURL)
		r.Header.Set("X-Eddy-CSRF", csrf)
		return do(h, r, jar)
	}
	rr := send("POST", "/api/v1/tokens", `{"name":"laptop","scopes":["read"],"ttl":"720h"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var created struct {
		Token string
		Item  tokenItem
	}
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil || !scannerRE.MatchString(created.Token) {
		t.Fatalf("create body: %v %q", err, created.Token)
	}
	if rr := send("POST", "/api/v1/tokens", `{"name":"x","scopes":["read"],"ttl":"100d"}`); rr.Code != 400 {
		t.Fatalf("over-long ttl: %d", rr.Code)
	}
	if rr := send("POST", "/api/v1/tokens", `{"name":"x","scopes":["root"]}`); rr.Code != 400 {
		t.Fatalf("bad scope: %d", rr.Code)
	}
	rr = send("GET", "/api/v1/tokens", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), created.Token) || !strings.Contains(rr.Body.String(), created.Item.ID) {
		t.Fatalf("list: %d %s", rr.Code, rr.Body)
	}
	if rr := send("DELETE", "/api/v1/tokens/"+created.Item.ID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rr.Code)
	}
	if rr := send("DELETE", "/api/v1/tokens/"+created.Item.ID, ""); rr.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rr.Code)
	}
	if rr := send("GET", "/api/v1/tokens", ""); strings.Contains(rr.Body.String(), created.Item.ID) {
		t.Fatal("revoked token listed")
	}
	// A PAT is never accepted on /api.
	r := httptest.NewRequest("GET", "/api/v1/tokens", nil)
	r.Header.Set("Authorization", "Bearer "+created.Token)
	if rr := do(h, r, nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("PAT on /api: %d", rr.Code)
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "720h": 720 * time.Hour, "30d": 30 * 24 * time.Hour, " 1h ": time.Hour} {
		if got, err := parseTTL(in); err != nil || got != want {
			t.Errorf("parseTTL(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"x", "-1h", "0d", "1.5d", "0s"} {
		if _, err := parseTTL(in); err == nil {
			t.Errorf("parseTTL(%q) accepted", in)
		}
	}
}
