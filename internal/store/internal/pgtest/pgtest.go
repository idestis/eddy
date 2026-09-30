// Package pgtest gives tests an isolated PostgreSQL schema. Tests that need
// PostgreSQL are skipped unless EDDY_TEST_POSTGRES_DSN is set, for example:
//
//	docker run -d --rm --name eddy-pg -e POSTGRES_PASSWORD=eddy -p 55432:5432 postgres:17
//	EDDY_TEST_POSTGRES_DSN='postgres://postgres:eddy@127.0.0.1:55432/postgres?sslmode=disable' \
//	  go test ./internal/store/...
package pgtest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // "pgx" driver
)

// EnvDSN names the variable holding the test server's DSN.
const EnvDSN = "EDDY_TEST_POSTGRES_DSN"

// DSN creates a schema named eddy_test_<random>, returns EDDY_TEST_POSTGRES_DSN
// with search_path set to it, and drops the schema when t ends. It skips t
// when the variable is unset.
func DSN(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvDSN)
	if base == "" {
		t.Skip(EnvDSN + " is not set")
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("pgtest: random schema name: %v", err)
	}
	schema := "eddy_test_" + hex.EncodeToString(b[:])

	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("pgtest: open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = admin.Close()
		t.Fatalf("pgtest: create schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("pgtest: drop schema %s: %v", schema, err)
		}
		_ = admin.Close()
	})
	return WithSearchPath(t, base, schema)
}

// WithSearchPath returns dsn (a URL or a keyword/value string) with
// search_path set to schema.
func WithSearchPath(t testing.TB, dsn, schema string) string {
	t.Helper()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatalf("pgtest: parse %s: %v", EnvDSN, err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " search_path=" + schema
}
