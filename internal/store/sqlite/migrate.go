package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Migrations are forward-only SQL files named NNNN_description.sql, numbered
// contiguously from 0001. Each runs in its own transaction. There are no down
// migrations; a binary refuses to open a database migrated by a newer one.

//go:embed migrations/*.sql
var embedded embed.FS

// migrationFS is the migration source; tests replace it.
var migrationFS fs.FS = embedded

// keepBackups is how many pre-migration backups are kept next to the database.
const keepBackups = 2

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	files, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("sqlite: list migrations: %w", err)
	}
	var out []migration
	for _, f := range files {
		base := path.Base(f)
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || len(num) != 4 || v <= 0 {
			return nil, fmt.Errorf("sqlite: migration %s: name must be NNNN_description.sql", base)
		}
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("sqlite: read migration %s: %w", base, err)
		}
		out = append(out, migration{version: v, name: base, sql: string(b)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("sqlite: migrations must be numbered contiguously from 0001; found %s at position %d", m.name, i+1)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("sqlite: no migrations embedded")
	}
	return out, nil
}

// migrate brings the schema up to date. Before changing a database that
// already holds tables it writes a VACUUM INTO backup next to the file.
func (s *Store) migrate(ctx context.Context) error {
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	if _, err := s.w.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at INTEGER NOT NULL
) STRICT`); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}
	current, err := s.schemaVersion(ctx, s.w)
	if err != nil {
		return err
	}
	latest := migs[len(migs)-1].version
	if current > latest {
		return fmt.Errorf("sqlite: database schema version %d is newer than this binary supports (%d); refusing to start (was eddy-hub downgraded?)", current, latest)
	}
	pending := migs[current:]
	if len(pending) == 0 {
		return nil
	}

	var tables int
	if err := s.w.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name NOT IN ('schema_migrations', 'sqlite_sequence')`,
	).Scan(&tables); err != nil {
		return fmt.Errorf("sqlite: inspect schema: %w", err)
	}
	if tables > 0 {
		if err := s.backup(ctx, pending[0].version); err != nil {
			return err
		}
	}

	for _, m := range pending {
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			v, err := s.schemaVersion(ctx, tx)
			if err != nil {
				return err
			}
			if v >= m.version {
				return nil // applied concurrently
			}
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("sqlite: apply migration %s: %w", m.name, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
				m.version, time.Now().UnixMilli()); err != nil {
				return fmt.Errorf("sqlite: record migration %s: %w", m.name, err)
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.log.InfoContext(ctx, "applied store migration", "version", m.version, "name", m.name)
	}
	return nil
}

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *Store) schemaVersion(ctx context.Context, q querier) (int, error) {
	var v int
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("sqlite: read schema version: %w", err)
	}
	return v, nil
}

// backup writes <path>.pre-NNNN with VACUUM INTO, where NNNN is the first
// migration about to be applied, and keeps only the newest keepBackups copies.
func (s *Store) backup(ctx context.Context, version int) error {
	target := fmt.Sprintf("%s.pre-%04d", s.path, version)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sqlite: remove stale backup %s: %w", target, err)
	}
	if _, err := s.w.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("sqlite: backup to %s: %w", target, err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return fmt.Errorf("sqlite: chmod backup %s: %w", target, err)
	}
	s.log.InfoContext(ctx, "backed up store before migration", "backup", target)

	old, err := filepath.Glob(s.path + ".pre-[0-9][0-9][0-9][0-9]")
	if err != nil {
		return fmt.Errorf("sqlite: list backups: %w", err)
	}
	slices.Sort(old)
	for len(old) > keepBackups {
		if err := os.Remove(old[0]); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("sqlite: remove old backup %s: %w", old[0], err)
		}
		old = old[1:]
	}
	return nil
}
