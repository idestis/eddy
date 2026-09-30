package auth

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/idestis/eddy/internal/identity"
)

func TestHashPasswordFormatAndVerify(t *testing.T) {
	h := aliceHash(t)
	re := regexp.MustCompile(`^\$argon2id\$v=19\$m=65536,t=3,p=4\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)
	if !re.MatchString(h) {
		t.Fatalf("unexpected hash format %q", h)
	}
	ph, err := parsePasswordHash(h)
	if err != nil {
		t.Fatal(err)
	}
	if !ph.verify(testPassword) {
		t.Fatal("correct password rejected")
	}
	if ph.verify(testPassword+"x") || ph.verify("") {
		t.Fatal("wrong password accepted")
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password hashed")
	}
}

func TestParsePasswordHash(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	argon := func(params string) string { return "$argon2id$v=19$" + params + "$" + salt + "$" + key }
	bc10, _ := bcrypt.GenerateFromPassword([]byte("pw"), 10)
	bc4, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	tests := []struct {
		name    string
		hash    string
		wantErr string
	}{
		{name: "argon ok", hash: argon("m=65536,t=3,p=4")},
		{name: "argon stronger ok", hash: argon("m=131072,t=4,p=2")},
		{name: "argon low memory", hash: argon("m=4096,t=3,p=4"), wantErr: "weaker"},
		{name: "argon low time", hash: argon("m=65536,t=1,p=4"), wantErr: "weaker"},
		{name: "argon zero threads", hash: argon("m=65536,t=3,p=0"), wantErr: "weaker"},
		{name: "argon huge memory", hash: argon("m=4194304,t=3,p=4"), wantErr: "exceed"},
		{name: "argon missing param", hash: argon("m=65536,t=3"), wantErr: "must set"},
		{name: "argon dup param", hash: argon("m=65536,m=65536,t=3,p=4"), wantErr: "malformed"},
		{name: "argon unknown param", hash: argon("m=65536,t=3,p=4,x=1"), wantErr: "unknown"},
		{name: "argon old version", hash: "$argon2id$v=16$m=65536,t=3,p=4$" + salt + "$" + key, wantErr: "version"},
		{name: "argon short salt", hash: "$argon2id$v=19$m=65536,t=3,p=4$" + base64.RawStdEncoding.EncodeToString(make([]byte, 8)) + "$" + key, wantErr: "salt"},
		{name: "argon padded b64", hash: "$argon2id$v=19$m=65536,t=3,p=4$" + base64.StdEncoding.EncodeToString(make([]byte, 16)) + "$" + key, wantErr: "salt"},
		{name: "argon malformed", hash: "$argon2id$v=19$m=65536,t=3,p=4$" + salt, wantErr: "malformed"},
		{name: "argon2i", hash: "$argon2i$v=19$m=65536,t=3,p=4$" + salt + "$" + key, wantErr: "argon2id"},
		{name: "bcrypt cost 10", hash: string(bc10), wantErr: "below the minimum 12"},
		{name: "bcrypt cost 4", hash: string(bc4), wantErr: "below the minimum 12"},
		{name: "bcrypt 2y", hash: "$2y$12$" + strings.Repeat("a", 53), wantErr: "$2a$/$2b$"},
		{name: "bcrypt garbage", hash: "$2b$12$short", wantErr: "invalid bcrypt"},
		{name: "bcrypt too costly", hash: "$2b$20$" + strings.Repeat("a", 53), wantErr: "above the maximum"},
		{name: "md5 crypt", hash: "$1$abc$def", wantErr: "unsupported"},
		{name: "sha512 crypt", hash: "$6$abc$def", wantErr: "unsupported"},
		{name: "plaintext", hash: "hunter2", wantErr: "unsupported"},
		{name: "empty", hash: "", wantErr: "empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePasswordHash(tt.hash)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestBcryptCost12Accepted(t *testing.T) {
	h, err := bcrypt.GenerateFromPassword([]byte("pw-12"), 12)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{string(h), "$2a$" + string(h)[4:]} {
		ph, err := parsePasswordHash(s)
		if err != nil {
			t.Fatalf("cost 12 rejected: %v", err)
		}
		if !ph.verify("pw-12") || ph.verify("nope") {
			t.Fatal("bcrypt verify mismatch")
		}
	}
}

func TestHasherSemaphore(t *testing.T) {
	h := newHasher(1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.verify(ctx, nil, "x"); err == nil {
		t.Fatal("verify ran without a semaphore slot")
	}
	<-h.sem
	ok, err := h.verify(context.Background(), nil, "x")
	if err != nil || ok {
		t.Fatalf("dummy verify = %v, %v; want false, nil", ok, err)
	}
}

func TestParseUsers(t *testing.T) {
	good := aliceHash(t)
	bc10, _ := bcrypt.GenerateFromPassword([]byte("pw"), 10)
	m := testMapper(nil)
	tests := []struct {
		name    string
		yaml    string
		wantErr string
		check   func(t *testing.T, u map[string]*User)
	}{
		{name: "ok", yaml: "users:\n- username: alice\n  passwordHash: \"" + good + "\"\n  groups: [platform]\n", check: func(t *testing.T, u map[string]*User) {
			if u["alice"] == nil || u["alice"].Subject != "local:alice" {
				t.Fatalf("got %+v", u)
			}
		}},
		{name: "empty file", yaml: "users: []\n"},
		{name: "bad username", yaml: "users:\n- username: Alice\n  passwordHash: \"" + good + "\"\n", wantErr: "username must match"},
		{name: "duplicate", yaml: "users:\n- username: a\n  passwordHash: \"" + good + "\"\n- username: a\n  passwordHash: \"" + good + "\"\n", wantErr: "duplicate"},
		{name: "weak bcrypt", yaml: "users:\n- username: a\n  passwordHash: \"" + string(bc10) + "\"\n", wantErr: "below the minimum 12"},
		{name: "plaintext", yaml: "users:\n- username: a\n  passwordHash: hunter2\n", wantErr: "unsupported"},
		{name: "system group", yaml: "users:\n- username: a\n  passwordHash: \"" + good + "\"\n  groups: [\"system:masters\"]\n", wantErr: "system"},
		{name: "bad group", yaml: "users:\n- username: a\n  passwordHash: \"" + good + "\"\n  groups: [\"a b\"]\n", wantErr: "group must match"},
		{name: "unknown field", yaml: "users:\n- username: a\n  password: x\n", wantErr: "parse"},
		{name: "reports all", yaml: "users:\n- username: A\n- username: b\n  passwordHash: x\n", wantErr: "users[1]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := ParseUsers([]byte(tt.yaml), m)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, u)
			}
		})
	}
}

func TestNewRejectsWeakUsersFile(t *testing.T) {
	bc10, _ := bcrypt.GenerateFromPassword([]byte("pw"), 10)
	cfg := baseConfig(t, "users:\n- username: a\n  passwordHash: \""+string(bc10)+"\"\n")
	if _, err := New(cfg, newFakeStore(), nil, discardLog()); err == nil || !strings.Contains(err.Error(), "below the minimum") {
		t.Fatalf("New = %v, want weak-hash error", err)
	}
}

func TestUsersReloadRevokes(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	alice := identity.Principal{User: "local:alice", Provider: ProviderLocal, Via: identity.ViaWeb, Groups: []string{"eddy:authenticated"}}
	if _, _, err := e.svc.Issue(ctx, alice, "t", []string{"read"}, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.createSession(ctx, httptestRequest(), "local:alice", "alice", ProviderLocal, nil); err != nil {
		t.Fatal(err)
	}
	// Unchanged content: nothing happens.
	if err := e.svc.loadUsers(ctx, false); err != nil {
		t.Fatal(err)
	}
	if e.st.sess.count("local:alice") != 1 {
		t.Fatal("session removed on unchanged reload")
	}
	// Change alice's password.
	other, err := HashPassword("another password")
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Replace(usersYAML(t), aliceHash(t), other, 1)
	if err := os.WriteFile(e.cfg.Auth.Local.UsersFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.loadUsers(ctx, false); err != nil {
		t.Fatal(err)
	}
	if e.st.sess.count("local:alice") != 0 {
		t.Fatal("session survived password change")
	}
	n, _ := e.st.tok.CountActive(ctx, "local:alice", e.clock.Now())
	if n != 0 {
		t.Fatal("token survived password change")
	}
	// A broken file keeps the previous users.
	if err := os.WriteFile(e.cfg.Auth.Local.UsersFile, []byte("users: [{username: X}]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.loadUsers(ctx, false); err == nil {
		t.Fatal("broken users file accepted")
	}
	if e.svc.localUser("local:alice") == nil {
		t.Fatal("previous users dropped after failed reload")
	}
}

func TestLoadKeys(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadKeys(""); err == nil {
		t.Fatal("empty key path accepted")
	}
	if _, err := loadKeys(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key file accepted")
	}
	short := writeFile(t, dir, "short", strings.Repeat("a", 31)+"\n\n")
	if _, err := loadKeys(short); err == nil {
		t.Fatal("31-byte key accepted")
	}
	k, err := loadKeys(writeFile(t, dir, "ok", strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if string(k.csrf) == string(k.patPepper) || string(k.csrf) == string(k.preSession) || len(k.csrf) != 32 {
		t.Fatal("sub-keys are not independent")
	}
}
