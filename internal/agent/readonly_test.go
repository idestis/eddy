package agent

import (
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/protocol"
)

func TestReadOnlyRefusesWritesOnly(t *testing.T) {
	f := newOps(t, fixtures()...)
	f.h.readOnly = "read-only local mode (start with --allow-writes)"
	for _, op := range []protocol.Op{protocol.OpReconcile, protocol.OpSuspend, protocol.OpResume} {
		_, perr := f.h.Handle(t.Context(), request(op, ksRef, nil), nil)
		if perr == nil || perr.Code != 403 || !strings.Contains(perr.Message, "--allow-writes") {
			t.Fatalf("%s: want 403 read-only, got %v", op, perr)
		}
	}
	if targets, _ := patches(f); len(targets) != 0 {
		t.Fatalf("read-only handler patched %v", targets)
	}
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpYAML, ksRef, nil), nil); perr != nil {
		t.Fatalf("reads must still work: %v", perr)
	}
	// Policy still runs first: a system identity gets the impersonation error.
	req := request(protocol.OpYAML, ksRef, nil)
	req.Identity = protocol.Identity{User: "system:admin"}
	if _, perr := f.h.Handle(t.Context(), req, nil); perr == nil || perr.Code != 403 {
		t.Fatalf("system user: %v", perr)
	}
}
