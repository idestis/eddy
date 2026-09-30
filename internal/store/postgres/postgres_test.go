package postgres

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/pgtest"
	"github.com/idestis/eddy/internal/store/storetest"
)

var quiet = slog.New(slog.DiscardHandler)

func open(t *testing.T, dsn string) *Store {
	t.Helper()
	s, err := Open(t.Context(), Options{DSN: dsn, Logger: quiet})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func TestConformance(t *testing.T) {
	pgtest.DSN(t) // skip early when no server is configured
	storetest.Run(t, func(t *testing.T) store.Store { return open(t, pgtest.DSN(t)) })
}

func TestOpenRequiresDSN(t *testing.T) {
	if _, err := Open(t.Context(), Options{}); err == nil {
		t.Fatal("Open without a DSN succeeded")
	}
}

func TestTablePersistence(t *testing.T) {
	s := open(t, pgtest.DSN(t))
	rows, err := s.db.QueryContext(t.Context(), `SELECT c.relname, c.relpersistence FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = current_schema() AND c.relkind = 'r' ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, p string
		if err := rows.Scan(&name, &p); err != nil {
			t.Fatal(err)
		}
		got[name] = p
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"schema_migrations": "p",
		"api_tokens":        "p",
		"threads":           "p",
		"messages":          "p",
		"audit_events":      "p",
		"user_prefs":        "p",
		"sessions":          "u",
		"rate_limits":       "u",
		"agent_sessions":    "u",
	}
	for name, p := range want {
		if got[name] != p {
			t.Errorf("table %s: relpersistence %q, want %q", name, got[name], p)
		}
	}
	if len(got) != len(want) {
		t.Errorf("tables = %v, want %v", got, want)
	}
}

func TestMigrateConcurrentAndIdempotent(t *testing.T) {
	dsn := pgtest.DSN(t)
	const replicas = 6
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for range replicas {
		wg.Go(func() {
			s, err := Open(context.Background(), Options{DSN: dsn, Logger: quiet, MaxOpenConns: 2})
			if err == nil {
				err = s.Close()
			}
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("concurrent open: %v", errors.Join(errs...))
	}
	s := open(t, dsn)
	v, err := s.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	migs, err := loadMigrations(embedded)
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migs) {
		t.Fatalf("schema version %d, want %d", v, len(migs))
	}
	var n int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migs) {
		t.Fatalf("schema_migrations has %d rows, want %d", n, len(migs))
	}
}

func TestMigrateRefusesNewerSchema(t *testing.T) {
	dsn := pgtest.DSN(t)
	s := open(t, dsn)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 0)`); err != nil {
		t.Fatal(err)
	}
	_, err := Open(t.Context(), Options{DSN: dsn, Logger: quiet})
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("Open on a newer schema: got %v", err)
	}
}

func TestMigrateAppliesLaterMigrations(t *testing.T) {
	dsn := pgtest.DSN(t)
	open(t, dsn)
	init, err := embedded.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	migrationFS = fstest.MapFS{
		"migrations/0001_init.sql":  {Data: init},
		"migrations/0002_extra.sql": {Data: []byte(`CREATE TABLE extra (id BIGINT PRIMARY KEY); CREATE INDEX extra_id ON extra (id);`)},
	}
	t.Cleanup(func() { migrationFS = embedded })
	s := open(t, dsn)
	v, err := s.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if v != 2 {
		t.Fatalf("schema version %d, want 2", v)
	}
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO extra (id) VALUES (1)`); err != nil {
		t.Fatalf("migration 0002 not applied: %v", err)
	}
}

func TestLoadMigrationsRejectsGaps(t *testing.T) {
	for name, fsys := range map[string]fstest.MapFS{
		"gap":      {"migrations/0001_a.sql": {}, "migrations/0003_c.sql": {}},
		"bad name": {"migrations/1_a.sql": {}},
		"empty":    {},
	} {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: loadMigrations succeeded", name)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	s := open(t, pgtest.DSN(t))
	ctx := t.Context()
	by := store.Author{Type: store.AuthorHuman, Subject: "alice", Via: "web"}

	// PostgreSQL TEXT cannot hold NUL bytes.
	_, _, err := s.Threads().Create(ctx,
		store.Thread{Ref: store.ResourceRef{Cluster: "prod"}, Type: store.ThreadDiscussion, Visibility: store.VisibilityResource, Title: "nul\x00", CreatedBy: by},
		store.Message{Body: "hi", Author: by})
	if !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("NUL in title: got %v, want ErrInvalid", err)
	}

	// Check constraints named *_len map to ErrLimit, enum checks to ErrInvalid.
	_, err = s.db.ExecContext(ctx, `INSERT INTO user_prefs (subject, data, updated_at) VALUES ('x', repeat('a', 20000), 0)`)
	if err = mapErr("put prefs", err); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("oversized prefs: got %v, want ErrLimit", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO audit_events (ts, subject, groups, via, action, result) VALUES (0, 'a', '[]', 'web', 'x', 'maybe')`)
	if err = mapErr("append audit", err); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("bad audit result: got %v, want ErrInvalid", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO messages (id, thread_id, author, author_type, via, body, created_at) VALUES ('m', 'missing', 'a', 'human', 'web', 'b', 0)`)
	if err = mapErr("insert message", err); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("orphan message: got %v, want ErrNotFound", err)
	}
	if err := mapErr("x", context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("context error not wrapped: %v", err)
	}
}

func TestStatementTimeoutIsTransient(t *testing.T) {
	s := open(t, pgtest.DSN(t))
	ctx := t.Context()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET statement_timeout = '50ms'`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(ctx, `SELECT pg_sleep(1)`)
	if err = mapErr("sleep", err); !errors.Is(err, ErrTransient) {
		t.Fatalf("statement timeout: got %v, want ErrTransient", err)
	}
	_, _ = conn.ExecContext(ctx, `RESET statement_timeout`)
}

// TestEventsAcrossStores checks that an event published through one Store
// (one replica) reaches subscribers of another sharing the database.
func TestEventsAcrossStores(t *testing.T) {
	dsn := pgtest.DSN(t)
	a, b := open(t, dsn), open(t, dsn)
	ch, err := b.Events().Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := store.Event{Kind: store.EventAgent, Cluster: "prod"}
	if err := a.Events().Publish(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	if got := next(t, ch); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestEventsReconnect kills the listener's backend and checks that the
// subscriber gets a resync and then keeps receiving events.
func TestEventsReconnect(t *testing.T) {
	s := open(t, pgtest.DSN(t))
	ctx := t.Context()
	ch, err := s.Events().Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := s.listen.pid.Load()
	if pid == 0 {
		t.Fatal("listener has no connection")
	}
	var killed bool
	if err := s.db.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, int64(pid)).Scan(&killed); err != nil || !killed {
		t.Fatalf("terminate listener backend %d: %v %v", pid, killed, err)
	}
	if got := next(t, ch); got.Kind != store.EventResync {
		t.Fatalf("after reconnect got %+v, want a resync", got)
	}
	if s.listen.pid.Load() == pid {
		t.Fatal("listener did not reconnect")
	}
	want := store.Event{Kind: store.EventThread, ID: "t1"}
	if err := s.Events().Publish(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got := next(t, ch); got != want {
		t.Fatalf("after reconnect got %+v, want %+v", got, want)
	}
}

func TestCloseEndsSubscriptions(t *testing.T) {
	s, err := Open(t.Context(), Options{DSN: pgtest.DSN(t), Logger: quiet})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.Events().Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("event after close")
		}
	case <-time.After(storetest.EventTimeout):
		t.Fatal("subscription not closed by Close")
	}
	if _, err := s.Events().Subscribe(t.Context()); err == nil {
		t.Fatal("subscribe after close succeeded")
	}
}

func next(t *testing.T, ch <-chan store.Event) store.Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("channel closed")
		}
		return e
	case <-time.After(storetest.EventTimeout):
		t.Fatal("no event")
	}
	return store.Event{}
}
