package storeopen_test

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eddy-gitops/eddy/internal/config"
	"github.com/eddy-gitops/eddy/internal/store/memory"
	"github.com/eddy-gitops/eddy/internal/store/sqlite"
	"github.com/eddy-gitops/eddy/internal/store/storeopen"
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
		{"sqlite default driver", config.Store{}, "sqlite", false, false},
		{"sqlite ephemeral warns", config.Store{Driver: "sqlite", Ephemeral: true}, "sqlite", true, false},
		{"unknown driver", config.Store{Driver: "postgres"}, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cfg.Driver != "memory" {
				tc.cfg.Path = filepath.Join(t.TempDir(), "eddy.db")
			}
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
			case "sqlite":
				if _, ok := s.(*sqlite.Store); !ok {
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
