package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/eddy-gitops/eddy/internal/fleet"
	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/store"
)

// fakeFleet serves fixed data and records the principals it was called with.
type fakeFleet struct {
	mu         sync.Mutex
	resources  map[string]model.Resource // key: cluster + "|" + ref.ID()
	events     []model.Event
	forbidden  map[string]bool
	logs       []string
	principals []identity.Principal
	writes     int
}

func newFakeFleet() *fakeFleet {
	return &fakeFleet{resources: map[string]model.Resource{}, forbidden: map[string]bool{}}
}

func (f *fakeFleet) add(cluster string, r model.Resource) {
	r.ID = r.Ref.ID()
	f.resources[cluster+"|"+r.ID] = r
}

func (f *fakeFleet) seen(p identity.Principal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.principals = append(f.principals, p)
}

func (f *fakeFleet) Clusters(_ context.Context, p identity.Principal) ([]model.ClusterInfo, error) {
	f.seen(p)
	return []model.ClusterInfo{{Name: "prod", Connected: true}, {Name: "dev", Connected: true}, {Name: "gone"}}, nil
}

func (f *fakeFleet) List(_ context.Context, p identity.Principal, cluster string, fl fleet.Filter) ([]model.Resource, error) {
	f.seen(p)
	var out []model.Resource
	for k, r := range f.resources {
		if len(k) > len(cluster) && k[:len(cluster)+1] == cluster+"|" && (fl.Status == "" || r.Status == fl.Status) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeFleet) Get(_ context.Context, p identity.Principal, cluster string, ref model.Ref) (model.Resource, error) {
	f.seen(p)
	if f.forbidden[ref.ID()] {
		return model.Resource{}, fleet.ErrForbidden
	}
	r, ok := f.resources[cluster+"|"+ref.ID()]
	if !ok {
		return model.Resource{}, fleet.ErrNotFound
	}
	return r, nil
}

func (f *fakeFleet) Children(_ context.Context, p identity.Principal, _ string, _ model.Ref) ([]model.Resource, error) {
	f.seen(p)
	return nil, nil
}

func (f *fakeFleet) YAML(_ context.Context, p identity.Principal, _ string, _ model.Ref) (string, error) {
	f.seen(p)
	return "kind: Kustomization\nspec:\n  password: hunter2\n", nil
}

func (f *fakeFleet) Events(_ context.Context, p identity.Principal, _ string, _ model.Ref) ([]model.Event, error) {
	f.seen(p)
	return f.events, nil
}

func (f *fakeFleet) Logs(_ context.Context, p identity.Principal, _ string, _ model.Ref, o fleet.LogOptions, w fleet.LineWriter) error {
	f.seen(p)
	if o.Follow {
		return errors.New("follow not allowed")
	}
	return w.WriteLines(f.logs)
}

func (f *fakeFleet) write(p identity.Principal) error {
	f.seen(p)
	f.mu.Lock()
	f.writes++
	f.mu.Unlock()
	return nil
}

func (f *fakeFleet) Reconcile(_ context.Context, p identity.Principal, _ string, _ model.Ref, _ fleet.ActionOptions) error {
	return f.write(p)
}

func (f *fakeFleet) Suspend(_ context.Context, p identity.Principal, _ string, _ model.Ref, _ fleet.ActionOptions) error {
	return f.write(p)
}

func (f *fakeFleet) Resume(_ context.Context, p identity.Principal, _ string, _ model.Ref, _ fleet.ActionOptions) error {
	return f.write(p)
}

func (f *fakeFleet) CanGet(context.Context, identity.Principal, string, model.Ref) (bool, error) {
	return true, nil
}

func (f *fakeFleet) CanPatch(context.Context, identity.Principal, string, model.Ref) (bool, error) {
	return true, nil
}

// fakeThreads is an in-memory ThreadStore without RBAC.
type fakeThreads struct {
	mu       sync.Mutex
	threads  map[string]store.Thread
	messages map[string][]store.Message
	n        int
}

func newFakeThreads() *fakeThreads {
	return &fakeThreads{threads: map[string]store.Thread{}, messages: map[string][]store.Message{}}
}

func (t *fakeThreads) Get(_ context.Context, _ identity.Principal, id, _ string, _ int) (store.Thread, []store.Message, string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	th, ok := t.threads[id]
	if !ok {
		return store.Thread{}, nil, "", store.ErrNotFound
	}
	return th, append([]store.Message(nil), t.messages[id]...), "", nil
}

func (t *fakeThreads) Create(_ context.Context, _ identity.Principal, in CreateInput) (store.Thread, store.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n++
	th := store.Thread{
		ID: fmt.Sprintf("t%d", t.n), Ref: in.Ref, Type: in.Type, Visibility: in.Visibility,
		Title: in.Title, Status: store.ThreadOpen, CreatedBy: in.Author, CreatedAt: time.Now(),
	}
	m := store.Message{ID: fmt.Sprintf("m%d", t.n), ThreadID: th.ID, Author: in.Author, Body: in.Body, Meta: in.Meta}
	t.threads[th.ID] = th
	t.messages[th.ID] = []store.Message{m}
	return th, m, nil
}

func (t *fakeThreads) Reply(_ context.Context, _ identity.Principal, id, body string, a store.Author, meta json.RawMessage) (store.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.threads[id]; !ok {
		return store.Message{}, store.ErrNotFound
	}
	t.n++
	m := store.Message{ID: fmt.Sprintf("m%d", t.n), ThreadID: id, Author: a, Body: body, Meta: meta}
	t.messages[id] = append(t.messages[id], m)
	return m, nil
}

// fakeAudit collects audit events.
type fakeAudit struct {
	mu     sync.Mutex
	events []store.AuditEvent
}

func (a *fakeAudit) Append(_ context.Context, e store.AuditEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *fakeAudit) Query(context.Context, store.AuditFilter) ([]store.AuditEvent, string, error) {
	return nil, "", nil
}

func (a *fakeAudit) all() []store.AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]store.AuditEvent(nil), a.events...)
}

// scriptedProvider replays responses and records requests.
type scriptedProvider struct {
	mu       sync.Mutex
	script   []Response
	requests []Request
	block    chan struct{} // when set, Complete waits for it
	err      error
}

func (s *scriptedProvider) Name() string  { return "fake" }
func (s *scriptedProvider) Model() string { return "fake-model-1" }

func (s *scriptedProvider) Complete(ctx context.Context, r Request) (Response, error) {
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return Response{}, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Copy messages: the caller appends to its slice after we return.
	r.Messages = append([]Message(nil), r.Messages...)
	s.requests = append(s.requests, r)
	if s.err != nil {
		return Response{}, s.err
	}
	if len(s.requests) > len(s.script) {
		return s.script[len(s.script)-1], nil
	}
	return s.script[len(s.requests)-1], nil
}

func (s *scriptedProvider) reqs() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func toolUse(id, name, input string) Block {
	return Block{Type: BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}
}

func groupForKind(kind string) (string, bool) {
	switch kind {
	case "Kustomization":
		return "kustomize.toolkit.fluxcd.io", true
	case "HelmRelease":
		return "helm.toolkit.fluxcd.io", true
	case "Pod":
		return "", true
	}
	return "", false
}
