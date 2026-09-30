package storeopen_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/store/internal/pgtest"
	"github.com/idestis/eddy/internal/store/memory"
	"github.com/idestis/eddy/internal/store/postgres"
	"github.com/idestis/eddy/internal/store/storeopen"
)

func TestOpen(t *testing.T) {
	tests := []struct {
		name     string
		cfg      config.Store
		wantType string
		wantWarn bool
		wantErr  bool
	}{
		{"memory warns", config.Store{Driver: "memory"}, "memory", true, false},
		{"postgres is the default driver", config.Store{Postgres: config.Postgres{DSNEnv: "EDDY_TEST_UNSET_DSN"}}, "", false, true},
		{"sqlite is gone", config.Store{Driver: "sqlite"}, "", false, true},
		{"unknown driver", config.Store{Driver: "mysql"}, "", false, true},
		{"postgres without DSN", config.Store{Driver: "postgres", Postgres: config.Postgres{DSNEnv: "EDDY_TEST_UNSET_DSN"}}, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&buf, nil))
			s, err := storeopen.Open(t.Context(), tc.cfg, log)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			switch tc.wantType {
			case "memory":
				if _, ok := s.(*memory.Store); !ok {
					t.Fatalf("got %T", s)
				}
			}
			if err := s.Ping(t.Context()); err != nil {
				t.Fatal(err)
			}
			warned := strings.Contains(buf.String(), `"level":"WARN"`)
			if warned != tc.wantWarn {
				t.Fatalf("warned = %v, want %v; log: %s", warned, tc.wantWarn, buf.String())
			}
		})
	}
}

func TestOpenPostgres(t *testing.T) {
	dsn := pgtest.DSN(t)
	t.Setenv("EDDY_TEST_STOREOPEN_DSN", dsn)
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	s, err := storeopen.Open(t.Context(), config.Store{
		Driver:   "postgres",
		Postgres: config.Postgres{DSNEnv: "EDDY_TEST_STOREOPEN_DSN", MaxOpenConns: 2},
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, ok := s.(*postgres.Store); !ok {
		t.Fatalf("got %T", s)
	}
	if err := s.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Fatalf("unexpected warning: %s", buf.String())
	}
}
