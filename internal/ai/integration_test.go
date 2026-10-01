package ai

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
)

// TestAskStoresChat runs Ask against the memory store to check what is
// stored: the owner, both authors, and never the attachments.
func TestAskStoresChat(t *testing.T) {
	fl := newFakeFleet()
	fl.add("prod", ksResource())
	chats := memory.New().Chats()
	prov := &scriptedProvider{script: []Response{{StopReason: StopEndTurn, Content: []Block{TextBlock("Because of X.")}}}}
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	svc, err := New(config.AI{Enabled: true, AllowLogs: true}, fl, chats, nil, runtimeflags.Static{AIEnabled: true}, groupForKind, log, WithProvider(prov))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	resp, err := svc.Ask(ctx, alice, AskRequest{
		Context:     refs(ksStoreRef),
		Question:    "why?",
		Attachments: []Attachment{{Kind: "logs", Source: "flux-system/kc", Lines: []string{"ATTACHED-LOG-LINE"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := chats.Get(ctx, alice.User, resp.Chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.Owner != alice.User || c.Title != "why?" || c.MessageCount != 2 || len(c.Context) != 1 {
		t.Errorf("chat = %+v", c)
	}
	msgs, _, err := chats.Messages(ctx, alice.User, c.ID, "", 10)
	if err != nil {
		t.Fatal(err)
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
	for _, m := range msgs {
		if strings.Contains(m.Body, "ATTACHED-LOG-LINE") || strings.Contains(string(m.Meta), "ATTACHED-LOG-LINE") {
			t.Errorf("attachment stored in message %+v", m)
		}
	}
	if !strings.Contains(prov.requests[0].Messages[0].Content[0].Text, "ATTACHED-LOG-LINE") {
		t.Error("attachment did not reach the model")
	}

	bob := alice
	bob.User = "local:bob"
	if _, err := svc.Ask(ctx, bob, AskRequest{ChatID: c.ID, Question: "peek"}); !errors.Is(err, fleet.ErrNotFound) {
		t.Errorf("other user's chat: err = %v", err)
	}
	if list, _, _ := chats.List(ctx, bob.User, "", 0); len(list) != 0 {
		t.Errorf("bob has chats: %+v", list)
	}
}
