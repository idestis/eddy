package ai

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
)

// fakeFleet serves fixed data and records the principals it was called with.
type fakeFleet struct {
	mu         sync.Mutex
	resources  map[string]model.Resource   // key: cluster + "|" + ref.ID()
	children   map[string][]model.Resource // key: cluster + "|" + parent ref.ID()
	events     []model.Event
	forbidden  map[string]bool
	logs       []string
	principals []identity.Principal
	canGets    []string
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

func (f *fakeFleet) Children(_ context.Context, p identity.Principal, cluster string, ref model.Ref) ([]model.Resource, error) {
	f.seen(p)
	return f.children[cluster+"|"+ref.ID()], nil
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

func (f *fakeFleet) CanGet(_ context.Context, p identity.Principal, _ string, ref model.Ref) (bool, error) {
	f.seen(p)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canGets = append(f.canGets, ref.ID())
	return !f.forbidden[ref.ID()], nil
}

func (f *fakeFleet) CanPatch(context.Context, identity.Principal, string, model.Ref) (bool, error) {
	return true, nil
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
