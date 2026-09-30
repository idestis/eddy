// Package storeopen selects and opens the store.Store backend named in
// hub.yaml. It lives outside package store because the backends import
// store, and store cannot import them back.
package storeopen

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
	"github.com/idestis/eddy/internal/store/postgres"
)

// Driver names accepted in store.driver.
const (
	DriverPostgres = "postgres"
	DriverMemory   = "memory"
)

// DefaultDSNEnv is the variable read for the postgres DSN when
// cfg.Postgres.DSNEnv is empty.
const DefaultDSNEnv = "EDDY_DATABASE_URL"

// Open returns the backend selected by cfg.Driver ("" means postgres). For
// postgres it reads the DSN from the environment variable named by
// cfg.Postgres.DSNEnv. The memory driver logs a warning: its data does not
// survive a restart and is not shared between replicas.
func Open(ctx context.Context, cfg config.Store, log *slog.Logger) (store.Store, error) {
	if log == nil {
		log = slog.Default()
	}
	driver := cfg.Driver
	if driver == "" {
		driver = DriverPostgres
	}
	switch driver {
	case DriverPostgres:
		env := cfg.Postgres.DSNEnv
		if env == "" {
			env = DefaultDSNEnv
		}
		dsn := os.Getenv(env)
		if dsn == "" {
			return nil, fmt.Errorf("store: open postgres: %s is not set", env)
		}
		s, err := postgres.Open(ctx, postgres.Options{DSN: dsn, MaxOpenConns: cfg.Postgres.MaxOpenConns, Logger: log})
		if err != nil {
			return nil, fmt.Errorf("store: open postgres: %w", err)
		}
		return s, nil
	case DriverMemory:
		log.WarnContext(ctx, "store is ephemeral: sessions, tokens, threads and stored audit events are lost on restart and not shared between replicas", "driver", driver)
		return memory.New(), nil
	default:
		return nil, fmt.Errorf("store: unknown driver %q (want %q or %q)", cfg.Driver, DriverPostgres, DriverMemory)
	}
}
