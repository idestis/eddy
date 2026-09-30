package threads_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/idestis/eddy/internal/audit"
	"github.com/idestis/eddy/internal/fleet"
	"github.com/idestis/eddy/internal/identity"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
	"github.com/idestis/eddy/internal/store/memory"
	"github.com/idestis/eddy/internal/threads"
)

// fakeFleet grants access from static tables. Methods other than Clusters,
// CanGet and CanPatch are not implemented (the embedded nil interface panics).
type fakeFleet struct {
	fleet.Service

	mu       sync.Mutex
	clusters map[string][]string        // user → visible clusters
	get      map[string]map[string]bool // user → "cluster/refID" → allowed
	patch    map[string]map[string]bool
	errs     map[string]error // cluster → error returned by CanGet
	getCalls int
}

func key(cluster string, r model.Ref) string { return cluster + "/" + r.ID() }

func (f *fakeFleet) Clusters(_ context.Context, p identity.Principal) ([]model.ClusterInfo, error) {
	var out []model.ClusterInfo
	for _, c := range f.clusters[p.User] {
		out = append(out, model.ClusterInfo{Name: c})
	}
	return out, nil
}

func (f *fakeFleet) CanGet(_ context.Context, p identity.Principal, cluster string, r model.Ref) (bool, error) {
	f.mu.Lock()
	f.getCalls++
	f.mu.Unlock()
	if err := f.errs[cluster]; err != nil {
		return false, err
	}
	return f.get[p.User][key(cluster, r)], nil
}

func (f *fakeFleet) CanPatch(_ context.Context, p identity.Principal, cluster string, r model.Ref) (bool, error) {
	return f.patch[p.User][key(cluster, r)], nil
}

func (f *fakeFleet) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.getCalls
}

// countingThreads counts List calls to the store.
type countingThreads struct {
	store.Threads
	mu    sync.Mutex
	lists int
}

func (c *countingThreads) List(ctx context.Context, f store.ThreadFilter) ([]store.Thread, string, error) {
	c.mu.Lock()
	c.lists++
	c.mu.Unlock()
	return c.Threads.List(ctx, f)
}

var (
	podinfo  = store.ResourceRef{Cluster: "prod", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "apps", Name: "podinfo"}
	secret   = store.ResourceRef{Cluster: "prod", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "secret", Name: "vault"}
	prodWide = store.ResourceRef{Cluster: "prod"}
	stageAll = store.ResourceRef{Cluster: "staging"}

	alice   = identity.Principal{User: "alice", Display: "Alice", Via: identity.ViaWeb}
	bob     = identity.Principal{User: "bob", Display: "Bob", Via: identity.ViaWeb}
	mallory = identity.Principal{User: "mallory", Display: "Mallory", Via: identity.ViaWeb}
)

func mref(r store.ResourceRef) string {
	return key(r.Cluster, model.Ref{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name})
}

type env struct {
	svc      *threads.Service
	st       *memory.Store
	fl       *fakeFleet
	ct       *countingThreads
	mu       sync.Mutex
	notified []store.Thread
}

// newEnv: alice and bob can get podinfo and see cluster prod; only alice can
// get the "secret" release; bob may patch podinfo; mallory sees nothing.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := memory.New()
	t.Cleanup(func() { _ = st.Close() })
	fl := &fakeFleet{
		clusters: map[string][]string{"alice": {"prod"}, "bob": {"prod"}},
		get: map[string]map[string]bool{
			"alice": {mref(podinfo): true, mref(secret): true},
			"bob":   {mref(podinfo): true},
		},
		patch: map[string]map[string]bool{"bob": {mref(podinfo): true}},
		errs:  map[string]error{},
	}
	e := &env{st: st, fl: fl, ct: &countingThreads{Threads: st.Threads()}}
	rec := audit.New(st.Audit(), slog.New(slog.DiscardHandler))
	e.svc = threads.New(e.ct, fl, rec, func(t store.Thread) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.notified = append(e.notified, t)
	})
	return e
}

func (e *env) create(t *testing.T, p identity.Principal, in threads.CreateInput) store.Thread {
	t.Helper()
	if in.Title == "" {
		in.Title = "title"
	}
	if in.Body == "" {
		in.Body = "body"
	}
	th, _, err := e.svc.Create(t.Context(), p, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return th
}

func (e *env) auditActions(t *testing.T) []string {
	t.Helper()
	evs, _, err := e.st.Audit().Query(t.Context(), store.AuditFilter{Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(evs) - 1; i >= 0; i-- {
		out = append(out, evs[i].Action+":"+string(evs[i].Result)+":"+evs[i].Subject)
	}
	return out
}

func (e *env) lastNotified(t *testing.T) store.Thread {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.notified) == 0 {
		t.Fatal("notify was not called")
	}
	return e.notified[len(e.notified)-1]
}

func TestAuthorFor(t *testing.T) {
	tests := []struct {
		name string
		p    identity.Principal
		t    store.AuthorType
		want store.Author
	}{
		{"web defaults to human", alice, "", store.Author{Type: store.AuthorHuman, Subject: "alice", Display: "Alice", Via: "web"}},
		{"mcp keeps client", identity.Principal{User: "bob", Display: "Bob", Via: identity.ViaMCP, Client: "claude-code"}, "",
			store.Author{Type: store.AuthorHuman, Subject: "bob", Display: "Bob", Via: "mcp", Client: "claude-code"}},
		{"askai defaults to ai", identity.Principal{User: "bob", Via: identity.ViaAskAI, Client: "claude-haiku"}, "",
			store.Author{Type: store.AuthorAI, Subject: "bob", Via: "askai", Client: "claude-haiku"}},
		{"explicit type wins", identity.Principal{User: "bob", Via: identity.ViaAskAI}, store.AuthorHuman,
			store.Author{Type: store.AuthorHuman, Subject: "bob", Via: "askai"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := threads.AuthorFor(tc.p, tc.t); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	e := newEnv(t)
	spoof := store.Author{Type: store.AuthorHuman, Subject: "root", Display: "Custom", Via: "system", Client: "x"}
	th, m, err := e.svc.Create(t.Context(), alice, threads.CreateInput{
		Ref: podinfo, Title: "  Rollout stuck  ", Body: "It is stuck.", Author: spoof, Meta: json.RawMessage(`{"k":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if th.Title != "Rollout stuck" || th.Type != store.ThreadDiscussion || th.Visibility != store.VisibilityResource {
		t.Fatalf("thread = %+v", th)
	}
	wantAuthor := store.Author{Type: store.AuthorHuman, Subject: "alice", Display: "Custom", Via: "web", Client: "x"}
	if th.CreatedBy != wantAuthor || m.Author != wantAuthor {
		t.Fatalf("author = %+v / %+v, want %+v (subject and via come from the principal)", th.CreatedBy, m.Author, wantAuthor)
	}
	if m.Body != "It is stuck." || string(m.Meta) != `{"k":1}` || m.ThreadID != th.ID {
		t.Fatalf("message = %+v", m)
	}
	if got := e.lastNotified(t); got.ID != th.ID {
		t.Fatalf("notified %s, want %s", got.ID, th.ID)
	}
	if got := e.auditActions(t); strings.Join(got, ",") != "thread.create:ok:alice" {
		t.Fatalf("audit = %v", got)
	}

	ask := e.create(t, identity.Principal{User: "alice", Via: identity.ViaAskAI, Client: "m"}, threads.CreateInput{Ref: podinfo, Type: store.ThreadAsk})
	if ask.Visibility != store.VisibilityPrivate || ask.CreatedBy.Type != store.AuthorAI {
		t.Fatalf("ask thread = %+v", ask)
	}
	cluster := e.create(t, bob, threads.CreateInput{Ref: prodWide})
	if cluster.Ref != prodWide {
		t.Fatalf("cluster thread ref = %+v", cluster.Ref)
	}
}

func TestCreateValidation(t *testing.T) {
	mcp := identity.Principal{User: "alice", Via: identity.ViaMCP}
	tests := []struct {
		name string
		p    identity.Principal
		in   threads.CreateInput
		want error
	}{
		{"ok", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b"}, nil},
		{"empty title", alice, threads.CreateInput{Ref: podinfo, Body: "b"}, threads.ErrInvalid},
		{"blank title", alice, threads.CreateInput{Ref: podinfo, Title: " \n\t", Body: "b"}, threads.ErrInvalid},
		{"title at limit", alice, threads.CreateInput{Ref: podinfo, Title: strings.Repeat("ü", 200), Body: "b"}, nil},
		{"title over limit", alice, threads.CreateInput{Ref: podinfo, Title: strings.Repeat("a", 201), Body: "b"}, store.ErrLimit},
		{"empty body", alice, threads.CreateInput{Ref: podinfo, Title: "t"}, threads.ErrInvalid},
		{"blank body", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: "   "}, threads.ErrInvalid},
		{"web body at 64 KiB", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: strings.Repeat("a", 64<<10)}, nil},
		{"web body over 64 KiB", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: strings.Repeat("a", 64<<10+1)}, store.ErrLimit},
		{"mcp body at 8 KiB", mcp, threads.CreateInput{Ref: podinfo, Title: "t", Body: strings.Repeat("a", 8<<10)}, nil},
		{"mcp body over 8 KiB", mcp, threads.CreateInput{Ref: podinfo, Title: "t", Body: strings.Repeat("a", 8<<10+1)}, store.ErrLimit},
		{"missing cluster", alice, threads.CreateInput{Title: "t", Body: "b"}, threads.ErrInvalid},
		{"kind without name", alice, threads.CreateInput{Ref: store.ResourceRef{Cluster: "prod", Kind: "HelmRelease"}, Title: "t", Body: "b"}, threads.ErrInvalid},
		{"cluster ref with namespace", alice, threads.CreateInput{Ref: store.ResourceRef{Cluster: "prod", Namespace: "x"}, Title: "t", Body: "b"}, threads.ErrInvalid},
		{"bad type", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b", Type: "chat"}, threads.ErrInvalid},
		{"bad visibility", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b", Visibility: "public"}, threads.ErrInvalid},
		{"bad author type", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b", Author: store.Author{Type: "bot"}}, threads.ErrInvalid},
		{"bad meta", alice, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b", Meta: json.RawMessage(`{`)}, threads.ErrInvalid},
		{"invisible target", mallory, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b"}, fleet.ErrNotFound},
		{"invisible cluster", alice, threads.CreateInput{Ref: stageAll, Title: "t", Body: "b"}, fleet.ErrNotFound},
		{"anonymous", identity.Principal{}, threads.CreateInput{Ref: podinfo, Title: "t", Body: "b"}, fleet.ErrForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			_, _, err := e.svc.Create(t.Context(), tc.p, tc.in)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(e.auditActions(t)) != 0 {
				t.Fatal("rejected create was audited as a write")
			}
		})
	}
}

func TestGetVisibility(t *testing.T) {
	e := newEnv(t)
	shared := e.create(t, alice, threads.CreateInput{Ref: podinfo})
	private := e.create(t, alice, threads.CreateInput{Ref: podinfo, Visibility: store.VisibilityPrivate})
	onSecret := e.create(t, alice, threads.CreateInput{Ref: secret})
	onCluster := e.create(t, alice, threads.CreateInput{Ref: prodWide})

	tests := []struct {
		name string
		p    identity.Principal
		id   string
		want error
	}{
		{"author sees shared", alice, shared.ID, nil},
		{"reader sees shared", bob, shared.ID, nil},
		{"no rbac, not found", mallory, shared.ID, fleet.ErrNotFound},
		{"author sees private", alice, private.ID, nil},
		{"others do not see private", bob, private.ID, fleet.ErrNotFound},
		{"target rbac applies", bob, onSecret.ID, fleet.ErrNotFound},
		{"cluster thread visible with cluster", bob, onCluster.ID, nil},
		{"cluster thread hidden without cluster", mallory, onCluster.ID, fleet.ErrNotFound},
		{"missing id", alice, "nope", fleet.ErrNotFound},
		{"anonymous", identity.Principal{}, shared.ID, fleet.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th, msgs, _, err := e.svc.Get(t.Context(), tc.p, tc.id, "", 0)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if th.ID != tc.id || len(msgs) != 1 {
					t.Fatalf("thread %s with %d messages", th.ID, len(msgs))
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if errors.Is(err, fleet.ErrForbidden) {
				t.Fatal("invisible thread reported as forbidden")
			}
		})
	}

	// A fleet failure is an error, not a silent "not found".
	e.fl.errs["prod"] = fleet.ErrDisconnected
	if _, _, _, err := e.svc.Get(t.Context(), bob, shared.ID, "", 0); !errors.Is(err, fleet.ErrDisconnected) {
		t.Fatalf("err = %v, want ErrDisconnected", err)
	}
}

func TestGetPaginatesMessages(t *testing.T) {
	e := newEnv(t)
	th := e.create(t, alice, threads.CreateInput{Ref: podinfo})
	for i := range 9 {
		if _, err := e.svc.Reply(t.Context(), bob, th.ID, fmt.Sprint(i), store.Author{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	cursor := ""
	for {
		_, msgs, next, err := e.svc.Get(t.Context(), alice, th.ID, cursor, 4)
		if err != nil {
			t.Fatal(err)
		}
		n += len(msgs)
		if next == "" {
			break
		}
		cursor = next
	}
	if n != 10 {
		t.Fatalf("paged %d messages, want 10", n)
	}
}

func TestReply(t *testing.T) {
	e := newEnv(t)
	th := e.create(t, alice, threads.CreateInput{Ref: podinfo})
	private := e.create(t, alice, threads.CreateInput{Ref: podinfo, Visibility: store.VisibilityPrivate})
	mcpBob := identity.Principal{User: "bob", Display: "Bob", Via: identity.ViaMCP, Client: "claude-code"}

	m, err := e.svc.Reply(t.Context(), mcpBob, th.ID, "on it", store.Author{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Author{Type: store.AuthorHuman, Subject: "bob", Display: "Bob", Via: "mcp", Client: "claude-code"}
	if m.Author != want {
		t.Fatalf("author = %+v, want %+v", m.Author, want)
	}
	if got := e.lastNotified(t); got.ID != th.ID || got.MessageCount != 2 {
		t.Fatalf("notified thread = %+v", got)
	}

	tests := []struct {
		name string
		p    identity.Principal
		id   string
		body string
		want error
	}{
		{"invisible", mallory, th.ID, "hi", fleet.ErrNotFound},
		{"private of someone else", bob, private.ID, "hi", fleet.ErrNotFound},
		{"empty body", bob, th.ID, "", threads.ErrInvalid},
		{"mcp body over 8 KiB", mcpBob, th.ID, strings.Repeat("a", 8<<10+1), store.ErrLimit},
		{"web body over 8 KiB is fine", bob, th.ID, strings.Repeat("a", 8<<10+1), nil},
		{"missing thread", bob, "nope", "hi", fleet.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Reply(t.Context(), tc.p, tc.id, tc.body, store.Author{}, nil)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	got := e.auditActions(t)
	want2 := []string{"thread.create:ok:alice", "thread.create:ok:alice", "thread.reply:ok:bob", "thread.reply:ok:bob"}
	if strings.Join(got, ",") != strings.Join(want2, ",") {
		t.Fatalf("audit = %v, want %v", got, want2)
	}
}

func TestReplyLimit(t *testing.T) {
	e := newEnv(t)
	th := e.create(t, alice, threads.CreateInput{Ref: podinfo})
	for range store.MaxMessagesPerThread - 1 {
		if _, err := e.svc.Reply(t.Context(), alice, th.ID, "x", store.Author{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Reply(t.Context(), alice, th.ID, "x", store.Author{}, nil); !errors.Is(err, store.ErrLimit) {
		t.Fatalf("err = %v, want ErrLimit", err)
	}
}

func TestResolveReopen(t *testing.T) {
	type step struct {
		name    string
		p       identity.Principal
		reopen  bool
		want    error
		wantSt  store.ThreadStatus
		wantAud string
	}
	tests := []struct {
		name  string
		ref   store.ResourceRef
		vis   store.Visibility
		steps []step
	}{
		{"author resolves and reopens", podinfo, store.VisibilityResource, []step{
			{"resolve", alice, false, nil, store.ThreadResolved, "thread.resolve:ok:alice"},
			{"resolve again is a no-op", alice, false, nil, store.ThreadResolved, ""},
			{"reopen", alice, true, nil, store.ThreadOpen, "thread.reopen:ok:alice"},
		}},
		{"patcher resolves", podinfo, store.VisibilityResource, []step{
			{"resolve", bob, false, nil, store.ThreadResolved, "thread.resolve:ok:bob"},
			{"reopen", bob, true, nil, store.ThreadOpen, "thread.reopen:ok:bob"},
		}},
		{"reader without patch is forbidden", podinfo, store.VisibilityResource, []step{
			{"no rbac", mallory, false, fleet.ErrNotFound, store.ThreadOpen, ""},
		}},
		{"cluster thread only by author", prodWide, store.VisibilityResource, []step{
			{"bob forbidden", bob, false, fleet.ErrForbidden, store.ThreadOpen, "thread.resolve:denied:bob"},
			{"alice resolves", alice, false, nil, store.ThreadResolved, "thread.resolve:ok:alice"},
			{"bob cannot reopen", bob, true, fleet.ErrForbidden, store.ThreadResolved, "thread.reopen:denied:bob"},
		}},
		{"private thread hidden from patcher", podinfo, store.VisibilityPrivate, []step{
			{"bob", bob, false, fleet.ErrNotFound, store.ThreadOpen, ""},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			th := e.create(t, alice, threads.CreateInput{Ref: tc.ref, Visibility: tc.vis})
			for _, s := range tc.steps {
				before := len(e.auditActions(t))
				op := e.svc.Resolve
				if s.reopen {
					op = e.svc.Reopen
				}
				got, err := op(t.Context(), s.p, th.ID)
				if s.want != nil {
					if !errors.Is(err, s.want) {
						t.Fatalf("%s: err = %v, want %v", s.name, err, s.want)
					}
				} else {
					if err != nil {
						t.Fatalf("%s: %v", s.name, err)
					}
					if got.Status != s.wantSt {
						t.Fatalf("%s: returned status %s", s.name, got.Status)
					}
					if s.wantSt == store.ThreadResolved && got.ResolvedBy == "" {
						t.Fatalf("%s: resolvedBy not set", s.name)
					}
				}
				stored, err := e.st.Threads().Get(t.Context(), th.ID)
				if err != nil {
					t.Fatal(err)
				}
				if stored.Status != s.wantSt {
					t.Fatalf("%s: stored status %s, want %s", s.name, stored.Status, s.wantSt)
				}
				aud := e.auditActions(t)[before:]
				if strings.Join(aud, ",") != s.wantAud {
					t.Fatalf("%s: audit %v, want %q", s.name, aud, s.wantAud)
				}
			}
		})
	}
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name    string
		p       identity.Principal
		want    error
		wantAud string
	}{
		{"author", alice, nil, "thread.delete:ok:alice"},
		{"patcher is not enough", bob, fleet.ErrForbidden, "thread.delete:denied:bob"},
		{"invisible", mallory, fleet.ErrNotFound, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			th := e.create(t, alice, threads.CreateInput{Ref: podinfo})
			before := len(e.auditActions(t))
			err := e.svc.Delete(t.Context(), tc.p, th.ID)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			_, getErr := e.st.Threads().Get(t.Context(), th.ID)
			if deleted := errors.Is(getErr, store.ErrNotFound); deleted != (tc.want == nil) {
				t.Fatalf("deleted = %v", deleted)
			}
			if aud := e.auditActions(t)[before:]; strings.Join(aud, ",") != tc.wantAud {
				t.Fatalf("audit %v, want %q", aud, tc.wantAud)
			}
			if tc.want == nil {
				if got := e.lastNotified(t); got.ID != th.ID {
					t.Fatalf("notified %s", got.ID)
				}
			}
		})
	}
}

func TestListFiltersByVisibility(t *testing.T) {
	e := newEnv(t)
	shared := e.create(t, alice, threads.CreateInput{Ref: podinfo})
	alicePrivate := e.create(t, alice, threads.CreateInput{Ref: podinfo, Visibility: store.VisibilityPrivate})
	onSecret := e.create(t, alice, threads.CreateInput{Ref: secret})
	onCluster := e.create(t, bob, threads.CreateInput{Ref: prodWide})
	bobPrivate := e.create(t, bob, threads.CreateInput{Ref: podinfo, Visibility: store.VisibilityPrivate})

	tests := []struct {
		name string
		p    identity.Principal
		f    store.ThreadFilter
		want []store.Thread
	}{
		{"alice", alice, store.ThreadFilter{}, []store.Thread{onCluster, onSecret, alicePrivate, shared}},
		{"bob", bob, store.ThreadFilter{}, []store.Thread{bobPrivate, onCluster, shared}},
		{"viewer cannot be spoofed", bob, store.ThreadFilter{Viewer: "alice"}, []store.Thread{bobPrivate, onCluster, shared}},
		{"mallory", mallory, store.ThreadFilter{}, nil},
		{"filter by ref", bob, store.ThreadFilter{Ref: store.ResourceRef{Namespace: "apps"}}, []store.Thread{bobPrivate, shared}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, next, err := e.svc.List(t.Context(), tc.p, tc.f)
			if err != nil {
				t.Fatal(err)
			}
			if next != "" {
				t.Fatalf("next = %q", next)
			}
			if a, b := ids(got), ids(tc.want); a != b {
				t.Fatalf("got %s, want %s", a, b)
			}
		})
	}
}

func ids(ts []store.Thread) string {
	var s []string
	for _, t := range ts {
		s = append(s, t.Title+"#"+t.ID[len(t.ID)-4:])
	}
	return strings.Join(s, ",")
}

func TestListOverFetchesAndPaginates(t *testing.T) {
	e := newEnv(t)
	// 30 threads, newest first: every third is on podinfo (bob can see it),
	// the rest on the secret release (bob cannot).
	var visible []string
	for i := range 30 {
		ref := secret
		if i%3 == 0 {
			ref = podinfo
		}
		th := e.create(t, alice, threads.CreateInput{Ref: ref, Title: fmt.Sprint(i)})
		if ref == podinfo {
			visible = append([]string{th.ID}, visible...)
		}
	}

	var got []string
	cursor := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("pagination does not terminate")
		}
		callsBefore := e.fl.calls()
		items, next, err := e.svc.List(t.Context(), bob, store.ThreadFilter{Limit: 4, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 4 {
			t.Fatalf("page of %d items", len(items))
		}
		if calls := e.fl.calls() - callsBefore; calls > 2 {
			t.Fatalf("CanGet called %d times for 2 distinct refs; want it cached per call", calls)
		}
		for _, th := range items {
			got = append(got, th.ID)
		}
		if next == "" {
			break
		}
		if len(items) < 4 {
			t.Fatalf("short page (%d) with a next cursor before the page cap", len(items))
		}
		cursor = next
	}
	if strings.Join(got, ",") != strings.Join(visible, ",") {
		t.Fatalf("paged ids\n got  %v\n want %v", got, visible)
	}
}

func TestListStopsAfterPageCap(t *testing.T) {
	e := newEnv(t)
	// The only thread bob can see is the oldest; UUIDv7 ids break
	// same-millisecond ties in creation order.
	oldest := e.create(t, alice, threads.CreateInput{Ref: podinfo, Title: "visible"})
	for i := range 60 {
		e.create(t, alice, threads.CreateInput{Ref: secret, Title: fmt.Sprint(i)})
	}

	e.ct.lists = 0
	items, next, err := e.svc.List(t.Context(), bob, store.ThreadFilter{Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if e.ct.lists != 5 {
		t.Fatalf("store.List called %d times, want the 5-page cap", e.ct.lists)
	}
	if len(items) != 0 || next == "" {
		t.Fatalf("got %d items, next %q; want an empty page with a cursor", len(items), next)
	}
	// Continuing eventually reaches the visible thread.
	var found bool
	for range 10 {
		items, next, err = e.svc.List(t.Context(), bob, store.ThreadFilter{Limit: 5, Cursor: next})
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range items {
			found = found || th.ID == oldest.ID
		}
		if next == "" {
			break
		}
	}
	if !found {
		t.Fatal("visible thread never returned")
	}
}

func TestListSkipsUndecidableThreads(t *testing.T) {
	e := newEnv(t)
	e.create(t, alice, threads.CreateInput{Ref: podinfo})
	e.fl.errs["prod"] = fleet.ErrDisconnected
	items, _, err := e.svc.List(t.Context(), alice, store.ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("got %d threads while the cluster is unreachable; want none (fail closed)", len(items))
	}
}
