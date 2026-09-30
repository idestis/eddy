package sqlite_test

import (
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/eddy-gitops/eddy/internal/store"
	"github.com/eddy-gitops/eddy/internal/store/sqlite"
	"github.com/eddy-gitops/eddy/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := sqlite.Open(t.Context(), sqlite.Options{
			Path:   filepath.Join(t.TempDir(), "eddy.db"),
			Logger: slog.New(slog.DiscardHandler),
		})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() {
			if err := s.Close(); err != nil {
				t.Errorf("close: %v", err)
			}
		})
		return s
	})
}
