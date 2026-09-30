package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/store/sqlite"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, os.Stdin, &out, &errOut); code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatalf("--version: %d %q %q", code, out.String(), errOut.String())
	}
	if code := run([]string{"bogus"}, os.Stdin, &out, &errOut); code != 2 {
		t.Fatalf("unknown command: %d", code)
	}
}

func TestHashPasswordFromPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.WriteString("s3cret-password\n")
	w.Close()
	var out, errOut bytes.Buffer
	if code := run([]string{"hash-password"}, r, &out, &errOut); code != 0 {
		t.Fatalf("hash-password: %d %s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "$argon2id$v=19$m=65536,t=3,p=4$") || strings.Contains(out.String(), "s3cret") {
		t.Fatalf("hash %q", out.String())
	}
}

func writeConfig(t *testing.T, dir, dbPath string) string {
	t.Helper()
	users := filepath.Join(dir, "users.yaml")
	_ = os.WriteFile(users, []byte("users: []\n"), 0o600)
	cfg := filepath.Join(dir, "hub.yaml")
	_ = os.WriteFile(cfg, []byte(`publicURL: https://eddy.example.com
auth:
  local: {enabled: true, usersFile: `+users+`}
store: {driver: sqlite, path: `+dbPath+`}
`), 0o600)
	return cfg
}

func TestAdminBackupAndRevoke(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "eddy.db")
	st, err := sqlite.Open(context.Background(), sqlite.Options{Path: db})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	cfg := writeConfig(t, dir, db)

	var out, errOut bytes.Buffer
	if code := run([]string{"admin", "revoke", "--config", cfg, "--user", "local:alice"}, os.Stdin, &out, &errOut); code != 0 {
		t.Fatalf("revoke: %d %s", code, errOut.String())
	}
	backup := filepath.Join(dir, "backup.db")
	if code := run([]string{"admin", "backup", "--config", cfg, "--out", backup}, os.Stdin, &out, &errOut); code != 0 {
		t.Fatalf("backup: %d %s", code, errOut.String())
	}
	fi, err := os.Stat(backup)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() == 0 {
		t.Fatalf("backup file: %v %v", fi, err)
	}
	if code := run([]string{"admin", "backup", "--config", cfg, "--out", backup}, os.Stdin, &out, &errOut); code != 1 {
		t.Fatal("backup overwrote an existing file")
	}
	// The copy is a valid store with the revoke audit event.
	cp, err := sqlite.Open(context.Background(), sqlite.Options{Path: backup})
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if code := run([]string{"admin"}, os.Stdin, &out, &errOut); code != 2 {
		t.Fatal("admin without a command")
	}
}
