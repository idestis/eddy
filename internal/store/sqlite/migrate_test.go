package sqlite

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func open(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(t.Context(), Options{Path: path, Logger: quiet})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestFileModesAndPragmas(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "eddy")
	path := filepath.Join(dir, "eddy.db")
	s := open(t, path)
	defer s.Close()

	for p, want := range map[string]os.FileMode{dir: 0o700 | os.ModeDir, path: 0o600} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != want {
			t.Errorf("%s mode = %v, want %v", p, fi.Mode(), want)
		}
	}

	ctx := t.Context()
	pragmas := []struct {
		db   string
		name string
		want string
	}{
		{"writer", "journal_mode", "wal"},
		{"writer", "foreign_keys", "1"},
		{"writer", "busy_timeout", "5000"},
		{"writer", "synchronous", "1"},
		{"reader", "foreign_keys", "1"},
		{"reader", "busy_timeout", "5000"},
		{"reader", "query_only", "1"},
	}
	for _, p := range pragmas {
		db := s.w
		if p.db == "reader" {
			db = s.r
		}
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+p.name).Scan(&got); err != nil {
			t.Fatalf("%s pragma %s: %v", p.db, p.name, err)
		}
		if got != p.want {
			t.Errorf("%s pragma %s = %q, want %q", p.db, p.name, got, p.want)
		}
	}

	if _, err := s.r.ExecContext(ctx, `INSERT INTO user_prefs (subject, data, updated_at) VALUES ('x', '{}', 0)`); err == nil {
		t.Error("read pool accepted a write")
	}
}

func TestExistingFileModeIsTightened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eddy.db")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := open(t, path)
	defer s.Close()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestDataSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eddy.db")
	s := open(t, path)
	if err := s.Prefs().Put(t.Context(), "alice", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = open(t, path)
	defer s.Close()
	got, err := s.Prefs().Get(t.Context(), "alice")
	if err != nil || string(got) != `{"a":1}` {
		t.Fatalf("prefs after reopen = %s, %v", got, err)
	}
	backups, _ := filepath.Glob(path + ".pre-*")
	if len(backups) != 0 {
		t.Errorf("reopen without pending migrations wrote backups %v", backups)
	}
}

func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "eddy.db")
	s := open(t, path)
	if _, err := s.w.ExecContext(t.Context(), `INSERT INTO schema_migrations (version, applied_at) VALUES (9999, 0)`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := Open(t.Context(), Options{Path: path, Logger: quiet})
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("open newer schema: err = %v", err)
	}
}

func TestRejectsBadPath(t *testing.T) {
	if _, err := Open(t.Context(), Options{Path: filepath.Join(t.TempDir(), "a?b.db")}); err == nil {
		t.Fatal("path with '?' accepted")
	}
}

func TestMigrationBackups(t *testing.T) {
	initSQL, err := embedded.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	withMigrations := func(t *testing.T, extra ...string) {
		fsys := fstest.MapFS{"migrations/0001_init.sql": {Data: initSQL}}
		for i, sql := range extra {
			fsys[filepath.ToSlash(filepath.Join("migrations", fmtVersion(i+2)+"_test.sql"))] = &fstest.MapFile{Data: []byte(sql)}
		}
		old := migrationFS
		migrationFS = fsys
		t.Cleanup(func() { migrationFS = old })
	}

	path := filepath.Join(t.TempDir(), "eddy.db")
	withMigrations(t)
	s := open(t, path)
	ctx := t.Context()
	if err := s.Prefs().Put(ctx, "alice", []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if b, _ := filepath.Glob(path + ".pre-*"); len(b) != 0 {
		t.Fatalf("fresh database was backed up: %v", b)
	}

	// Three upgrades, each adding one migration: only the last two backups stay.
	steps := []string{
		`CREATE TABLE extra2 (id INTEGER PRIMARY KEY) STRICT;`,
		`CREATE TABLE extra3 (id INTEGER PRIMARY KEY) STRICT;`,
		`CREATE TABLE extra4 (id INTEGER PRIMARY KEY) STRICT;`,
	}
	for i := range steps {
		withMigrations(t, steps[:i+1]...)
		s := open(t, path)
		v, err := s.schemaVersion(ctx, s.w)
		if err != nil || v != i+2 {
			t.Fatalf("schema version = %d, %v; want %d", v, err, i+2)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	backups, err := filepath.Glob(path + ".pre-*")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(backups)
	want := []string{path + ".pre-0003", path + ".pre-0004"}
	if !slices.Equal(backups, want) {
		t.Fatalf("backups = %v, want %v", backups, want)
	}

	// The newest backup is a usable database at the version before 0004.
	fi, err := os.Stat(want[1])
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("backup mode = %v, want 0600", fi.Mode().Perm())
	}
	withMigrations(t, steps[:2]...)
	b := open(t, want[1])
	defer b.Close()
	v, err := b.schemaVersion(ctx, b.r)
	if err != nil || v != 3 {
		t.Fatalf("backup schema version = %d, %v; want 3", v, err)
	}
	got, err := b.Prefs().Get(ctx, "alice")
	if err != nil || string(got) != `{"v":1}` {
		t.Fatalf("backup prefs = %s, %v", got, err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	initSQL, err := embedded.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	old := migrationFS
	t.Cleanup(func() { migrationFS = old })
	migrationFS = fstest.MapFS{
		"migrations/0001_init.sql": {Data: initSQL},
		"migrations/0002_bad.sql":  {Data: []byte(`CREATE TABLE ok2 (id INTEGER); THIS IS NOT SQL;`)},
	}
	path := filepath.Join(t.TempDir(), "eddy.db")
	if _, err := Open(t.Context(), Options{Path: path, Logger: quiet}); err == nil {
		t.Fatal("bad migration applied")
	}
	migrationFS = fstest.MapFS{"migrations/0001_init.sql": {Data: initSQL}}
	s := open(t, path)
	defer s.Close()
	var n int
	if err := s.r.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_schema WHERE name = 'ok2'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("partial migration was committed")
	}
}

func TestLoadMigrationsValidatesNames(t *testing.T) {
	tests := []struct {
		name string
		fs   fstest.MapFS
		ok   bool
	}{
		{"embedded", nil, true},
		{"gap", fstest.MapFS{"migrations/0001_a.sql": {}, "migrations/0003_c.sql": {}}, false},
		{"bad name", fstest.MapFS{"migrations/1_a.sql": {}}, false},
		{"empty", fstest.MapFS{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var fsys = migrationFS
			if tc.fs != nil {
				fsys = tc.fs
			}
			_, err := loadMigrations(fsys)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

var quiet = slog.New(slog.DiscardHandler)

func fmtVersion(v int) string { return fmt.Sprintf("%04d", v) }
