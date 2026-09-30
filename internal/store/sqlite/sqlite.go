// Package sqlite is the default store.Store backend: a single SQLite file
// (normally on a PVC at /var/lib/eddy/eddy.db) opened with the pure-Go
// modernc.org/sqlite driver.
//
// The database runs in WAL mode. Writes go through one connection that
// begins every transaction with BEGIN IMMEDIATE, so writers queue in Go
// rather than failing with SQLITE_BUSY; reads use a separate pool of
// query-only connections. Schema changes are forward-only embedded
// migrations (see migrate.go). All times are stored as unix milliseconds.
//
// See docs/adr/0002-hub-storage.md.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	msqlite "modernc.org/sqlite" // also registers the "sqlite" database/sql driver
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// DefaultPath is where the Helm chart mounts the database.
const DefaultPath = "/var/lib/eddy/eddy.db"

// Options configure Open.
type Options struct {
	// Path is the database file. It defaults to DefaultPath. Its directory is
	// created with mode 0700 if missing and the file is kept at mode 0600.
	Path string
	// ReadConns is the size of the read pool. It defaults to 4.
	ReadConns int
	// Logger receives migration and backup messages. It defaults to slog.Default().
	Logger *slog.Logger
}

// Store is the SQLite backend. It is safe for concurrent use.
type Store struct {
	path string
	w    *sql.DB // single writer connection, BEGIN IMMEDIATE
	r    *sql.DB // read-only pool
	log  *slog.Logger
}

var _ store.Store = (*Store)(nil)

// Open opens (creating if needed) and migrates the database at o.Path.
// It refuses databases whose schema version is newer than this binary.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.Path == "" {
		o.Path = DefaultPath
	}
	if o.ReadConns <= 0 {
		o.ReadConns = 4
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if strings.ContainsAny(o.Path, "?#") {
		return nil, fmt.Errorf("sqlite: path %q must not contain '?' or '#'", o.Path)
	}
	if err := prepareFile(o.Path); err != nil {
		return nil, err
	}

	const common = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	w, err := sql.Open("sqlite", o.Path+"?_txlock=immediate&_pragma=journal_mode(WAL)&"+common)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open writer: %w", err)
	}
	w.SetMaxOpenConns(1)
	w.SetMaxIdleConns(1)
	w.SetConnMaxLifetime(0)
	w.SetConnMaxIdleTime(0)

	s := &Store{path: o.Path, w: w, log: o.Logger.With("component", "store.sqlite")}
	if err := w.PingContext(ctx); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", o.Path, err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = w.Close()
		return nil, err
	}

	r, err := sql.Open("sqlite", o.Path+"?_pragma=query_only(1)&"+common)
	if err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("sqlite: open readers: %w", err)
	}
	r.SetMaxOpenConns(o.ReadConns)
	r.SetMaxIdleConns(o.ReadConns)
	if err := r.PingContext(ctx); err != nil {
		_ = w.Close()
		_ = r.Close()
		return nil, fmt.Errorf("sqlite: open readers: %w", err)
	}
	s.r = r
	return s, nil
}

// prepareFile creates the parent directory (0700) and the database file
// (0600) if missing, and tightens the mode of an existing file to 0600.
func prepareFile(path string) error {
	dir := filepath.Dir(path)
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("sqlite: create directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("sqlite: create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("sqlite: create %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("sqlite: chmod %s: %w", path, err)
	}
	return nil
}

func (s *Store) Sessions() store.Sessions { return sessions{s} }
func (s *Store) Tokens() store.Tokens     { return tokens{s} }
func (s *Store) Threads() store.Threads   { return threads{s} }
func (s *Store) Audit() store.Audit       { return audit{s} }
func (s *Store) Prefs() store.Prefs       { return prefs{s} }

// Ping checks both connection pools.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.w.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: ping writer: %w", err)
	}
	if err := s.r.PingContext(ctx); err != nil {
		return fmt.Errorf("sqlite: ping readers: %w", err)
	}
	return nil
}

// Close closes both pools.
func (s *Store) Close() error {
	return errors.Join(s.r.Close(), s.w.Close())
}

// Optimize runs PRAGMA optimize. The janitor calls it daily.
func (s *Store) Optimize(ctx context.Context) error {
	if _, err := s.w.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return fmt.Errorf("sqlite: optimize: %w", err)
	}
	return nil
}

// Prune applies the retention rules, deleting at most storeutil.PruneBatch
// rows per statement so the write lock is released between batches. A zero
// duration or day count disables the corresponding rule, except for
// sessions, which are always removed once expired.
func (s *Store) Prune(ctx context.Context, now time.Time, r store.Retention) (store.PruneStats, error) {
	var st store.PruneStats
	nowMs := storeutil.Ms(now)
	var err error

	if st.Sessions, err = s.deleteBatched(ctx, "sessions", "expires_at <= ?", nowMs); err != nil {
		return st, err
	}
	if r.TokenPurgeAfter > 0 {
		cut := storeutil.Ms(now.Add(-r.TokenPurgeAfter))
		if st.Tokens, err = s.deleteBatched(ctx, "api_tokens", "expires_at <= ? OR revoked_at <= ?", cut, cut); err != nil {
			return st, err
		}
	}
	if r.AuditDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.AuditDays)))
		if st.Audit, err = s.deleteBatched(ctx, "audit_events", "ts < ?", cut); err != nil {
			return st, err
		}
	}
	if r.ResolvedThreadsDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.ResolvedThreadsDays)))
		n, err := s.deleteBatched(ctx, "threads", "status = 'resolved' AND resolved_at <= ?", cut)
		st.Threads += n
		if err != nil {
			return st, err
		}
	}
	if r.AskThreadsDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.AskThreadsDays)))
		n, err := s.deleteBatched(ctx, "threads", "type = 'ask' AND updated_at <= ?", cut)
		st.Threads += n
		if err != nil {
			return st, err
		}
	}
	return st, nil
}

// deleteBatched deletes rows of table matching where in batches, each in its
// own statement (and so its own short write transaction).
func (s *Store) deleteBatched(ctx context.Context, table, where string, args ...any) (int64, error) {
	q := "DELETE FROM " + table + " WHERE rowid IN (SELECT rowid FROM " + table + " WHERE " + where +
		" LIMIT " + fmt.Sprint(storeutil.PruneBatch) + ")"
	var total int64
	for {
		res, err := s.w.ExecContext(ctx, q, args...)
		if err != nil {
			return total, fmt.Errorf("sqlite: prune %s: %w", table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, fmt.Errorf("sqlite: prune %s: %w", table, err)
		}
		total += n
		if n < storeutil.PruneBatch {
			return total, nil
		}
	}
}

// withTx runs fn in a write transaction (BEGIN IMMEDIATE via _txlock).
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// isConstraint reports whether err is a UNIQUE or PRIMARY KEY violation.
func isConstraint(err error) bool {
	var se *msqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func nullMs(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: storeutil.Ms(*t), Valid: true}
}

func fromNullMs(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := storeutil.FromMs(v.Int64)
	return &t
}
