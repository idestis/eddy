package hub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// BackupSQLite writes a consistent copy of the SQLite store at dbPath to
// out with VACUUM INTO. It is safe while the hub is running (WAL mode).
// out must not exist; it is created with mode 0600 because the store holds
// personal data (emails, thread text), although no credentials.
func BackupSQLite(ctx context.Context, dbPath, out string) error {
	if dbPath == "" || out == "" {
		return errors.New("hub: backup needs the database path and an output file")
	}
	if strings.ContainsAny(dbPath, "?#") {
		return fmt.Errorf("hub: database path %q must not contain '?' or '#'", dbPath)
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("hub: backup source: %w", err)
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("hub: backup target %s already exists", out)
	}
	abs, err := filepath.Abs(out)
	if err != nil {
		return fmt.Errorf("hub: backup target: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return fmt.Errorf("hub: open %s: %w", dbPath, err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, abs); err != nil {
		return fmt.Errorf("hub: backup to %s: %w", abs, err)
	}
	if err := os.Chmod(abs, 0o600); err != nil {
		return fmt.Errorf("hub: chmod %s: %w", abs, err)
	}
	return nil
}
