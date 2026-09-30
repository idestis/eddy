package mcp

import (
	"strings"
	"testing"

	"github.com/eddy-gitops/eddy/internal/store"
	"github.com/eddy-gitops/eddy/internal/store/memory"
	"github.com/eddy-gitops/eddy/internal/threads"
)

// TestThreadsServiceIntegration runs thread tools against the real threads
// service to check author normalisation and its MCP body limit.
func TestThreadsServiceIntegration(t *testing.T) {
	var svc *threads.Service
	e := newEnv(t, defaultConfig(), func(o *Options) {
		svc = threads.New(memory.New().Threads(), o.Fleet, nil, nil)
		o.Threads = svc
	})
	cs := e.connect(tokRead)
	text, isErr := callTool(t, cs, "create_thread", map[string]any{"cluster": "prod", "kind": "Kustomization", "namespace": "flux-system", "name": "apps", "title": "Broken", "body": "see logs"})
	if isErr {
		t.Fatal(text)
	}
	w, _ := data[ThreadWrite](t, text)
	want := store.Author{Type: store.AuthorHuman, Subject: "local:alice", Display: "Alice", Via: "mcp", Client: "test-client"}
	if w.Thread.CreatedBy != want || w.Message.Author != want {
		t.Errorf("author = %+v / %+v, want %+v", w.Thread.CreatedBy, w.Message.Author, want)
	}
	if text, isErr := callTool(t, cs, "create_thread", map[string]any{"cluster": "prod", "title": strings.Repeat("t", store.MaxTitleLen+1), "body": "b"}); !isErr || !strings.Contains(text, "title") {
		t.Errorf("long title: %s", text)
	}
	if text, isErr := callTool(t, cs, "resolve_thread", map[string]any{"id": w.Thread.ID}); isErr {
		t.Errorf("resolve: %s", text)
	}
	text, _ = callTool(t, cs, "list_threads", map[string]any{"cluster": "prod", "status": "resolved"})
	if l, _ := data[ThreadList](t, text); len(l.Items) != 1 {
		t.Errorf("list = %s", text)
	}
}
