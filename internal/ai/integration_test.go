package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/eddy-gitops/eddy/internal/config"
	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/runtimeflags"
	"github.com/eddy-gitops/eddy/internal/store"
	"github.com/eddy-gitops/eddy/internal/store/memory"
	"github.com/eddy-gitops/eddy/internal/threads"
)

// TestAskWithThreadsService runs Ask against the real threads service to
// check author normalisation and private visibility.
func TestAskWithThreadsService(t *testing.T) {
	fl := newFakeFleet()
	fl.add("prod", ksResource())
	th := threads.New(memory.New().Threads(), fl, nil, nil)
	prov := &scriptedProvider{script: []Response{{StopReason: StopEndTurn, Content: []Block{TextBlock("Because of X.")}}}}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	svc, err := New(config.AI{Enabled: true}, fl, th, nil, runtimeflags.Static{AIEnabled: true}, groupForKind, log, WithProvider(prov))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	resp, err := svc.Ask(ctx, alice, AskRequest{Cluster: "prod", ResourceID: ksRef.ID(), Question: "why?"})
	if err != nil {
		t.Fatal(err)
	}
	thread, msgs, _, err := th.Get(ctx, alice, resp.ThreadID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Type != store.ThreadAsk || thread.Visibility != store.VisibilityPrivate {
		t.Errorf("thread = %+v", thread)
	}
	if len(msgs) != 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
	if a := msgs[0].Author; a.Type != store.AuthorHuman || a.Via != "web" || a.Subject != alice.User {
		t.Errorf("question author = %+v", a)
	}
	if a := msgs[1].Author; a.Type != store.AuthorAI || a.Via != "askai" || a.Subject != alice.User || a.Client != "fake-model-1" {
		t.Errorf("answer author = %+v", a)
	}

	bob := alice
	bob.User = "local:bob"
	if _, err := svc.Ask(ctx, bob, AskRequest{Cluster: "prod", ThreadID: resp.ThreadID, Question: "peek"}); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("other user's private thread: err = %v", err)
	}
}
