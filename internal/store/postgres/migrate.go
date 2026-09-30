package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Migrations are forward-only SQL files named NNNN_description.sql, numbered
// contiguously from 0001. There are no down migrations. Within one minor
// release a migration may only add (tables, columns, indexes), so an older
// replica keeps working during a rolling upgrade.

//go:embed migrations/*.sql
var embedded embed.FS

// migrationFS is the migration source; tests replace it.
var migrationFS fs.FS = embedded

// migrationLock is the pg_advisory_xact_lock key that serialises migrations
// across replicas ("eddy" in ASCII). Advisory locks are per database, so
// hubs in different schemas of one database also queue behind each other,
// which is harmless.
const migrationLock int64 = 0x65646479

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations(fsys fs.FS) ([]migration, error) {
	files, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("postgres: list migrations: %w", err)
	}
	var out []migration
	for _, f := range files {
		base := path.Base(f)
		num, _, ok := strings.Cut(base, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil || len(num) != 4 || v <= 0 {
			return nil, fmt.Errorf("postgres: migration %s: name must be NNNN_description.sql", base)
		}
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("postgres: read migration %s: %w", base, err)
		}
		out = append(out, migration{version: v, name: base, sql: string(b)})
	}
	slices.SortFunc(out, func(a, b migration) int { return a.version - b.version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("postgres: migrations must be numbered contiguously from 0001; found %s at position %d", m.name, i+1)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("postgres: no migrations embedded")
	}
	return out, nil
}

// migrate applies pending migrations one transaction at a time. Each
// transaction takes the advisory lock first, then re-reads the schema
// version, so of N replicas starting together exactly one applies each
// migration and the others see it done.
func (s *Store) migrate(ctx context.Context) error {
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}
	latest := migs[len(migs)-1].version
	for {
		var applied *migration
		err := s.withTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrationLock); err != nil {
				return mapErr("lock migrations", err)
			}
			if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY,
  applied_at BIGINT NOT NULL
)`); err != nil {
				return mapErr("create schema_migrations", err)
			}
			var current int
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
				return mapErr("read schema version", err)
			}
			if current > latest {
				return fmt.Errorf("postgres: database schema version %d is newer than this binary supports (%d); refusing to start (was eddy-hub downgraded?)", current, latest)
			}
			if current == latest {
				return nil
			}
			m := migs[current]
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return mapErr("apply migration "+m.name, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES ($1, $2)`,
				m.version, time.Now().UnixMilli()); err != nil {
				return mapErr("record migration "+m.name, err)
			}
			applied = &m
			return nil
		})
		if err != nil {
			return err
		}
		if applied == nil {
			return nil
		}
		s.log.InfoContext(ctx, "applied store migration", "version", applied.version, "name", applied.name)
	}
}

// SchemaVersion returns the highest applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, mapErr("read schema version", err)
	}
	return v, nil
}
