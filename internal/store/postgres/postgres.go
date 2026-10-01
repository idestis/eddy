// Package postgres is the store.Store backend for PostgreSQL 14 or later,
// and the only one that supports several hub replicas (ADR-0004).
//
// It runs on database/sql with the pgx stdlib driver ("pgx"), plus one raw
// pgx connection per process that LISTENs on the eddy_events channel for
// cross-replica notifications (see events.go). The SQL sticks to portable
// features: BIGINT unix-millisecond times, BYTEA, GENERATED ALWAYS AS
// IDENTITY, INSERT … ON CONFLICT … DO UPDATE, RETURNING, row-value keyset
// cursors and partial indexes. It needs no extensions, no stored procedures
// and no jsonb operators: JSON is stored as TEXT.
//
// # LOGGED and UNLOGGED tables
//
// Durable user data lives in ordinary (LOGGED) tables: api_tokens, threads,
// messages, chats, chat_messages, audit_events and user_prefs. Throwaway state lives in UNLOGGED
// tables: sessions, rate_limits and agent_sessions. UNLOGGED tables skip
// the write-ahead log, so their frequent small writes (session touches,
// every rate-limit hit, a heartbeat per agent every 10 s) cost no WAL and
// no replication lag. The price is that PostgreSQL truncates them after a
// crash and does not copy them to streaming replicas, so a crash or a
// failover signs everyone out, resets rate-limit windows, and makes agent
// sessions re-register on their next heartbeat. None of that loses user
// content.
//
// # Concurrency
//
// Transactions run at READ COMMITTED, the PostgreSQL default. Updates that
// depend on current state are single conditional statements rather than
// read-then-write (for example AddMessage increments message_count only
// WHERE message_count < 1000, and a rate-limit hit is one upsert), so they
// stay correct across replicas without SERIALIZABLE retries.
//
// # Migrations
//
// Schema changes are forward-only SQL files embedded in the binary (see
// migrate.go). Each runs in its own transaction holding a transaction-level
// advisory lock, so replicas starting together apply each migration exactly
// once, and a binary refuses to start on a schema newer than itself.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/internal/storeutil"
)

// Pool defaults.
const (
	DefaultMaxOpenConns    = 10
	DefaultConnMaxIdleTime = 5 * time.Minute
	DefaultConnMaxLifetime = 30 * time.Minute
)

// ErrTransient wraps errors that are expected to go away on retry:
// serialization failures, deadlocks, lock and statement timeouts, and lost
// connections. Callers may retry the whole operation, or report the store
// as unavailable.
var ErrTransient = errors.New("postgres: transient error")

// Options configure Open.
type Options struct {
	// DSN is a libpq connection string or URL, for example
	// "postgres://eddy@db:5432/eddy?sslmode=verify-full". PG* environment
	// variables fill in whatever it leaves out. Required.
	DSN string
	// MaxOpenConns caps the pool. It defaults to DefaultMaxOpenConns. The
	// events listener uses one more connection outside the pool.
	MaxOpenConns int
	// ConnMaxIdleTime closes pooled connections idle for longer. It
	// defaults to DefaultConnMaxIdleTime.
	ConnMaxIdleTime time.Duration
	// ConnMaxLifetime recycles pooled connections, so a pool follows a
	// failover or a DNS change. It defaults to DefaultConnMaxLifetime.
	ConnMaxLifetime time.Duration
	// Logger receives migration and listener messages. It defaults to
	// slog.Default().
	Logger *slog.Logger
}

// Store is the PostgreSQL backend. It is safe for concurrent use.
type Store struct {
	db     *sql.DB
	log    *slog.Logger
	listen *listener
}

var _ store.Store = (*Store)(nil)

// Open connects, checks the connection and migrates the schema. It refuses
// a database whose schema version is newer than this binary.
func Open(ctx context.Context, o Options) (*Store, error) {
	if o.DSN == "" {
		return nil, errors.New("postgres: DSN is required")
	}
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = DefaultMaxOpenConns
	}
	if o.ConnMaxIdleTime <= 0 {
		o.ConnMaxIdleTime = DefaultConnMaxIdleTime
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = DefaultConnMaxLifetime
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	log := o.Logger.With("component", "store.postgres")

	db, err := sql.Open("pgx", o.DSN)
	if err != nil {
		// The error can quote the DSN, which may hold a password.
		return nil, errors.New("postgres: invalid DSN")
	}
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetMaxIdleConns(o.MaxOpenConns)
	db.SetConnMaxIdleTime(o.ConnMaxIdleTime)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)

	s := &Store{db: db, log: log, listen: newListener(o.DSN, log)}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Sessions() store.Sessions           { return sessions{s} }
func (s *Store) Tokens() store.Tokens               { return tokens{s} }
func (s *Store) Threads() store.Threads             { return threads{s} }
func (s *Store) Chats() store.Chats                 { return chats{s} }
func (s *Store) Audit() store.Audit                 { return audit{s} }
func (s *Store) Prefs() store.Prefs                 { return prefs{s} }
func (s *Store) RateLimits() store.RateLimits       { return rateLimits{s} }
func (s *Store) AgentSessions() store.AgentSessions { return agentSessions{s} }
func (s *Store) Events() store.Events               { return events{s} }

func (s *Store) JoinTokens() store.JoinTokens                 { return joinTokens{s} }
func (s *Store) ConnectionAttempts() store.ConnectionAttempts { return attempts{s} }

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return mapErr("ping", err)
	}
	return nil
}

// Close stops the events listener, closes every subscription and closes
// the pool.
func (s *Store) Close() error {
	s.listen.close()
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("postgres: close: %w", err)
	}
	return nil
}

// Prune applies the retention rules, deleting at most storeutil.PruneBatch
// rows per statement so no statement holds row locks for long. A zero
// duration or day count disables the corresponding rule, except for
// sessions and rate-limit windows, which are always removed once expired,
// and agent sessions, removed once their heartbeat is older than
// store.AgentSessionPruneAfter.
func (s *Store) Prune(ctx context.Context, now time.Time, r store.Retention) (store.PruneStats, error) {
	var st store.PruneStats
	nowMs := storeutil.Ms(now)
	var err error

	if st.Sessions, err = s.deleteBatched(ctx, "sessions", "id_hash", "expires_at <= $1", nowMs); err != nil {
		return st, err
	}
	if st.RateLimits, err = s.deleteBatched(ctx, "rate_limits", "key", "reset_at <= $1", nowMs); err != nil {
		return st, err
	}
	agentCut := storeutil.Ms(now.Add(-store.AgentSessionPruneAfter))
	if st.AgentSessions, err = s.deleteBatched(ctx, "agent_sessions", "(cluster, agent_instance)",
		"heartbeat_at <= $1", agentCut); err != nil {
		return st, err
	}
	joinCut := storeutil.Ms(now.Add(-store.JoinTokenPruneAfter))
	if st.JoinTokens, err = s.deleteBatched(ctx, "join_tokens", "id",
		"expires_at <= $1 OR used_at <= $1 OR revoked_at <= $1", joinCut); err != nil {
		return st, err
	}
	if r.TokenPurgeAfter > 0 {
		cut := storeutil.Ms(now.Add(-r.TokenPurgeAfter))
		if st.Tokens, err = s.deleteBatched(ctx, "api_tokens", "id", "expires_at <= $1 OR revoked_at <= $1", cut); err != nil {
			return st, err
		}
	}
	if r.AuditDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.AuditDays)))
		if st.Audit, err = s.deleteBatched(ctx, "audit_events", "id", "ts < $1", cut); err != nil {
			return st, err
		}
	}
	if r.ResolvedThreadsDays > 0 {
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.ResolvedThreadsDays)))
		n, err := s.deleteBatched(ctx, "threads", "id", "status = 'resolved' AND resolved_at <= $1", cut)
		st.Threads += n
		if err != nil {
			return st, err
		}
	}
	if r.ChatDays > 0 {
		// chat_messages go with their chat (ON DELETE CASCADE).
		cut := storeutil.Ms(now.Add(-storeutil.Days(r.ChatDays)))
		if st.Chats, err = s.deleteBatched(ctx, "chats", "id", "updated_at <= $1", cut); err != nil {
			return st, err
		}
	}
	return st, nil
}

// deleteBatched deletes rows of table matching where, identified by key (a
// column or a row value), in batches of storeutil.PruneBatch, each in its
// own statement. table, key and where are constants from this package.
func (s *Store) deleteBatched(ctx context.Context, table, key, where string, args ...any) (int64, error) {
	q := "DELETE FROM " + table + " WHERE " + key + " IN (SELECT " + strings.Trim(key, "()") +
		" FROM " + table + " WHERE " + where + " LIMIT " + strconv.Itoa(storeutil.PruneBatch) + ")"
	var total int64
	for {
		res, err := s.db.ExecContext(ctx, q, args...)
		if err != nil {
			return total, mapErr("prune "+table, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, mapErr("prune "+table, err)
		}
		total += n
		if n < storeutil.PruneBatch {
			return total, nil
		}
	}
}

// withTx runs fn in a READ COMMITTED transaction.
func (s *Store) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return mapErr("begin", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return mapErr("commit", err)
	}
	return nil
}

// SQLSTATE codes this package maps. See
// https://www.postgresql.org/docs/current/errcodes-appendix.html.
const (
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
	codeForeignKeyViolation = "23503"
	codeNotNullViolation    = "23502"
	codeStringTooLong       = "22001"
	codeBadEncoding         = "22021" // character_not_in_repertoire, e.g. a NUL byte in TEXT
	codeUntranslatable      = "22P05"
	codeInvalidText         = "22P02"
	codeSerialization       = "40001"
	codeDeadlock            = "40P01"
	codeLockNotAvailable    = "55P03"
	codeQueryCanceled       = "57014" // includes statement_timeout
	codeAdminShutdown       = "57P01"
	codeCannotConnectNow    = "57P03"
)

// mapErr wraps err with the operation name and maps constraint violations
// to the store's sentinel errors:
//
//   - unique violation → store.ErrConflict
//   - check violation of a *_len or *_max constraint → store.ErrLimit
//   - any other check, not-null or encoding violation → store.ErrInvalid
//   - foreign-key violation (the parent row is gone) → store.ErrNotFound
//   - serialization failures, deadlocks, timeouts, lost connections → ErrTransient
func mapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case codeUniqueViolation:
			return fmt.Errorf("postgres: %s: %w (%s)", op, store.ErrConflict, pe.ConstraintName)
		case codeCheckViolation:
			if strings.HasSuffix(pe.ConstraintName, "_len") || strings.HasSuffix(pe.ConstraintName, "_max") {
				return fmt.Errorf("postgres: %s: %w (%s)", op, store.ErrLimit, pe.ConstraintName)
			}
			return fmt.Errorf("postgres: %s: %w (%s)", op, store.ErrInvalid, pe.ConstraintName)
		case codeForeignKeyViolation:
			return fmt.Errorf("postgres: %s: %w (%s)", op, store.ErrNotFound, pe.ConstraintName)
		case codeNotNullViolation, codeStringTooLong, codeBadEncoding, codeUntranslatable, codeInvalidText:
			return fmt.Errorf("postgres: %s: %w: %s", op, store.ErrInvalid, pe.Message)
		case codeSerialization, codeDeadlock, codeLockNotAvailable, codeQueryCanceled,
			codeAdminShutdown, codeCannotConnectNow:
			return fmt.Errorf("postgres: %s: %w: %w", op, ErrTransient, err)
		}
		return fmt.Errorf("postgres: %s: %w", op, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("postgres: %s: %w", op, err)
	}
	if pgconn.SafeToRetry(err) || pgconn.Timeout(err) || errors.Is(err, sql.ErrConnDone) {
		return fmt.Errorf("postgres: %s: %w: %w", op, ErrTransient, err)
	}
	return fmt.Errorf("postgres: %s: %w", op, err)
}

// affectedOne turns "no row changed" into store.ErrNotFound.
func affectedOne(res sql.Result, err error, op string) error {
	if err != nil {
		return mapErr(op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return mapErr(op, err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// args numbers query parameters: a.add(v) appends v and returns "$n".
type args []any

func (a *args) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

type scanner interface{ Scan(dest ...any) error }

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
