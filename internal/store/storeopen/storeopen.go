// Package storeopen selects and opens the store.Store backend named in
// hub.yaml. It lives outside package store because the backends import
// store, and store cannot import them back.
package storeopen

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
	"github.com/idestis/eddy/internal/store/sqlite"
)

// Driver names accepted in store.driver.
const (
	DriverSQLite = "sqlite"
	DriverMemory = "memory"
)

// Open returns the backend selected by cfg.Driver ("" means sqlite). It logs
// a warning when the data will not survive a restart: with the memory driver,
// or when cfg.Ephemeral says the sqlite file is not on a persistent volume.
func Open(ctx context.Context, cfg config.Store, log *slog.Logger) (store.Store, error) {
	if log == nil {
		log = slog.Default()
	}
	driver := cfg.Driver
	if driver == "" {
		driver = DriverSQLite
	}
	const lost = "store is ephemeral: sessions, tokens, threads and stored audit events are lost on restart"
	switch driver {
	case DriverMemory:
		log.WarnContext(ctx, lost, "driver", driver)
		return memory.New(), nil
	case DriverSQLite:
		path := cfg.Path
		if path == "" {
			path = sqlite.DefaultPath
		}
		s, err := sqlite.Open(ctx, sqlite.Options{Path: path, Logger: log})
		if err != nil {
			return nil, fmt.Errorf("store: open sqlite: %w", err)
		}
		if cfg.Ephemeral {
			log.WarnContext(ctx, lost, "driver", driver, "path", path)
		}
		return s, nil
	default:
		return nil, fmt.Errorf("store: unknown driver %q (want %q or %q)", cfg.Driver, DriverSQLite, DriverMemory)
	}
}
