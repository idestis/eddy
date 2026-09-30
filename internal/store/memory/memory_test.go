package memory_test

import (
	"testing"

	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
	"github.com/idestis/eddy/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s := memory.New()
		t.Cleanup(func() { _ = s.Close() })
		return s
	})
}
