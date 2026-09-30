package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // "pgx" driver for the PostgreSQL test
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

func writeConfig(t *testing.T, dir, store string) string {
	t.Helper()
	users := filepath.Join(dir, "users.yaml")
	_ = os.WriteFile(users, []byte("users: []\n"), 0o600)
	cfg := filepath.Join(dir, "hub.yaml")
	_ = os.WriteFile(cfg, []byte(`publicURL: https://eddy.example.com
auth:
  local: {enabled: true, usersFile: `+users+`}
store: `+store+`
`), 0o600)
	return cfg
}

func TestAdminBackupRemoved(t *testing.T) {
	cfg := writeConfig(t, t.TempDir(), "{driver: memory}")
	var out, errOut bytes.Buffer
	if code := run([]string{"admin", "backup", "--config", cfg, "--out", "x"}, os.Stdin, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "pg_dump") {
		t.Fatalf("admin backup: %d %q, want 2 and a pg_dump hint", code, errOut.String())
	}
	if code := run([]string{"admin"}, os.Stdin, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "pg_dump") {
		t.Fatal("admin without a command should print the usage with the backup hint")
	}
	if code := run([]string{"admin", "revoke", "--config", cfg, "--user", "local:alice"}, os.Stdin, &out, &errOut); code != 1 {
		t.Fatal("revoke against the in-process memory store should fail")
	}
}

// TestAdminRevokePostgres runs `admin revoke` against PostgreSQL in a
// throwaway schema. It needs EDDY_TEST_POSTGRES_DSN.
func TestAdminRevokePostgres(t *testing.T) {
	base := os.Getenv("EDDY_TEST_POSTGRES_DSN")
	if base == "" {
		t.Skip("EDDY_TEST_POSTGRES_DSN is not set")
	}
	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("eddy_cmd_test_%d", time.Now().UnixNano())
	if _, err := db.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = db.Close()
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	t.Setenv("EDDY_TEST_CMD_DSN", u.String())
	cfg := writeConfig(t, t.TempDir(), "{driver: postgres, postgres: {dsnEnv: EDDY_TEST_CMD_DSN}}")

	var out, errOut bytes.Buffer
	if code := run([]string{"admin", "revoke", "--config", cfg, "--user", "local:alice"}, os.Stdin, &out, &errOut); code != 0 {
		t.Fatalf("revoke: %d %s", code, errOut.String())
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + schema + ".audit_events WHERE action = 'user.revoke'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("revoke audit rows: %d %v", n, err)
	}
}
