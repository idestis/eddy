package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/ai"
	"github.com/idestis/eddy/internal/config"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/runtimeflags"
	"github.com/idestis/eddy/internal/store"
)

// captureProvider answers every request with "answer N" and keeps the
// requests, so tests can assert what reached the model.
type captureProvider struct {
	mu   sync.Mutex
	reqs []ai.Request
}

func (p *captureProvider) Name() string  { return "fake" }
func (p *captureProvider) Model() string { return "fake-model" }

func (p *captureProvider) Complete(_ context.Context, r ai.Request) (ai.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r.Messages = append([]ai.Message(nil), r.Messages...)
	p.reqs = append(p.reqs, r)
	return ai.Response{StopReason: ai.StopEndTurn, Content: []ai.Block{ai.TextBlock(fmt.Sprintf("answer %d", len(p.reqs)))}}, nil
}

// prompt returns everything sent in request i (0-based), system prompt included.
func (p *captureProvider) prompt(t *testing.T, i int) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.reqs) {
		t.Fatalf("provider saw %d requests, want more than %d", len(p.reqs), i)
	}
	var b strings.Builder
	b.WriteString(p.reqs[i].System)
	for _, m := range p.reqs[i].Messages {
		for _, blk := range m.Content {
			b.WriteString(blk.Text)
			b.WriteString(blk.Content)
		}
	}
	return b.String()
}

func (p *captureProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.reqs)
}

// switchFlags is a runtime flags source the test can flip.
type switchFlags struct{ ai atomic.Bool }

func (f *switchFlags) Current() runtimeflags.Flags {
	return runtimeflags.Flags{AIEnabled: f.ai.Load(), MCPEnabled: true, MCPWrites: true}
}

type chatEnv struct {
	*testEnv
	prov  *captureProvider
	flags *switchFlags
}

// Resources on dev: alice (eddy:team-a) sees team-a, bob (eddy:team-b) sees team-b.
var (
	visibleKS = res("Kustomization", "team-a", "podinfo-visible", model.StatusFailed)
	hiddenHR  = res("HelmRelease", "team-b", "payments-hidden", model.StatusFailed)
)

func newChatEnv(t *testing.T) *chatEnv {
	t.Helper()
	ce := &chatEnv{prov: &captureProvider{}, flags: &switchFlags{}}
	ce.flags.ai.Store(true)
	ce.testEnv = newEnvOpts(t, "ai: {enabled: true}\n", func(o *Options) {
		o.Flags = ce.flags
		o.AIOptions = []ai.Option{ai.WithProvider(ce.prov)}
	})
	ks, hr := visibleKS, hiddenHR
	ks.Message = "VISIBLE-SUMMARY kustomize build failed"
	hr.Message = "HIDDEN-SUMMARY-MARKER"
	ce.connectAgent("dev", testToken, []model.Resource{ks, hr})
	return ce
}

func ref(r model.Resource) map[string]string {
	return map[string]string{"cluster": "dev", "group": r.Group, "kind": r.Kind, "namespace": r.Namespace, "name": r.Name}
}

type askResult struct {
	Chat          store.Chat    `json:"chat"`
	Message       store.Message `json:"message"`
	Steps         []ai.Step     `json:"steps"`
	ContextStatus []string      `json:"contextStatus"`
}

type chatPage struct {
	Chat     store.Chat      `json:"chat"`
	Messages []store.Message `json:"messages"`
	Next     string          `json:"next"`
}

func TestAskCreatesChatAndFollowsUp(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")

	var first askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{
		"context": []any{ref(visibleKS)}, "question": "Why is podinfo failing?",
	}, &first, 200)
	c := first.Chat
	if c.ID == "" || c.Owner != "local:alice" || c.Title != "Why is podinfo failing?" || c.MessageCount != 2 {
		t.Fatalf("chat = %+v", c)
	}
	if len(c.Context) != 1 || c.Context[0].Name != "podinfo-visible" || c.Context[0].Group != "kustomize.toolkit.fluxcd.io" {
		t.Fatalf("context = %+v", c.Context)
	}
	if strings.Join(first.ContextStatus, ",") != "ok" || first.Message.Body != "answer 1" || first.Steps == nil {
		t.Fatalf("ask = %+v", first)
	}
	if p := e.prov.prompt(t, 0); !strings.Contains(p, "VISIBLE-SUMMARY") || !strings.Contains(p, `source="context:1"`) {
		t.Fatalf("context summary not sent:\n%s", p)
	}

	// A second ask continues the chat and sends the history.
	var second askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{"chatId": c.ID, "question": "And now?"}, &second, 200)
	if second.Chat.ID != c.ID || second.Chat.MessageCount != 4 || second.Chat.Title != c.Title {
		t.Fatalf("follow-up chat = %+v", second.Chat)
	}
	p := e.prov.prompt(t, 1)
	if !strings.Contains(p, `source="chat"`) || !strings.Contains(p, "Why is podinfo failing?") || !strings.Contains(p, "answer 1") {
		t.Fatalf("history not sent on the second ask:\n%s", p)
	}

	// With chatId and context, the context is replaced first.
	var third askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{
		"chatId": c.ID, "context": []any{map[string]string{"cluster": "dev"}}, "question": "What about the cluster?",
	}, &third, 200)
	if len(third.Chat.Context) != 1 || third.Chat.Context[0] != (store.ResourceRef{Cluster: "dev"}) || strings.Join(third.ContextStatus, ",") != "ok" {
		t.Fatalf("context not replaced: %+v %v", third.Chat.Context, third.ContextStatus)
	}
	if p := e.prov.prompt(t, 2); strings.Contains(p, "VISIBLE-SUMMARY") {
		t.Fatalf("old context still sent:\n%s", p)
	}

	var page chatPage
	alice.do("GET", "/api/v1/ai/chats/"+c.ID, nil, &page, 200)
	if len(page.Messages) != 6 || page.Messages[0].Body != "Why is podinfo failing?" || page.Messages[1].Author.Type != store.AuthorAI ||
		page.Messages[1].Author.Client != "fake-model" || page.Messages[1].Author.Via != "askai" {
		t.Fatalf("messages = %+v", page.Messages)
	}

	// Each ask is audited with the chat id and the context.
	evs, _, err := e.store.Audit().Query(t.Context(), store.AuditFilter{Subject: "local:alice"})
	if err != nil {
		t.Fatal(err)
	}
	var asks int
	for _, ev := range evs {
		if ev.Action != "ai.ask" {
			continue
		}
		asks++
		var d struct {
			ChatID  string   `json:"chatId"`
			Context []string `json:"context"`
		}
		if err := json.Unmarshal(ev.Detail, &d); err != nil || d.ChatID != c.ID || len(d.Context) != 1 {
			t.Errorf("audit detail = %s", ev.Detail)
		}
		if strings.Contains(string(ev.Detail), "podinfo failing") {
			t.Error("audit detail contains the question")
		}
	}
	if asks != 3 {
		t.Errorf("ai.ask events = %d, want 3", asks)
	}
}

func TestAskHiddenContextIsNotSent(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")
	var got askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{
		"context": []any{ref(hiddenHR), ref(visibleKS), map[string]string{"cluster": "dev"}}, "question": "compare them",
	}, &got, 200)
	if strings.Join(got.ContextStatus, ",") != "hidden,ok,ok" || len(got.Chat.Context) != 3 {
		t.Fatalf("status = %v, context = %+v", got.ContextStatus, got.Chat.Context)
	}
	p := e.prov.prompt(t, 0)
	for _, bad := range []string{"HIDDEN-SUMMARY-MARKER", "payments-hidden", "team-b"} {
		if strings.Contains(p, bad) {
			t.Errorf("prompt contains %q from a hidden reference", bad)
		}
	}
	if !strings.Contains(p, "VISIBLE-SUMMARY") {
		t.Error("visible reference not sent")
	}

	// bob may see the HelmRelease but not the Kustomization: the same
	// references in his own chat get the opposite status.
	bob := e.login("bob")
	bob.do("POST", "/api/v1/ai/ask", map[string]any{
		"context": []any{ref(hiddenHR), ref(visibleKS)}, "question": "compare them",
	}, &got, 200)
	if strings.Join(got.ContextStatus, ",") != "ok,hidden" {
		t.Fatalf("bob's status = %v", got.ContextStatus)
	}
	if p := e.prov.prompt(t, 1); strings.Contains(p, "VISIBLE-SUMMARY") || !strings.Contains(p, "HIDDEN-SUMMARY-MARKER") {
		t.Errorf("bob's prompt:\n%s", p)
	}
}

func TestChatOwnerIsolation(t *testing.T) {
	e := newChatEnv(t)
	alice, bob := e.login("alice"), e.login("bob")
	var first askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{"question": "mine"}, &first, 200)
	id := first.Chat.ID

	path := "/api/v1/ai/chats/" + id
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", path, nil},
		{"PATCH", path, map[string]any{"title": "stolen"}},
		{"PATCH", path, map[string]any{"context": []any{}}},
		{"DELETE", path, nil},
		{"POST", "/api/v1/ai/ask", map[string]any{"chatId": id, "question": "peek"}},
		{"GET", "/api/v1/ai/chats/not-a-chat", nil},
	} {
		if st, code := bob.errorCode(tc.method, tc.path, tc.body); st != 404 || code != "not_found" {
			t.Errorf("bob %s %s: %d %s", tc.method, tc.path, st, code)
		}
	}
	var list struct {
		Items []store.Chat `json:"items"`
	}
	bob.do("GET", "/api/v1/ai/chats", nil, &list, 200)
	if len(list.Items) != 0 {
		t.Errorf("bob lists alice's chat: %+v", list.Items)
	}
	var page chatPage
	alice.do("GET", path, nil, &page, 200)
	if page.Chat.Title != "mine" || page.Chat.MessageCount != 2 || len(page.Messages) != 2 {
		t.Fatalf("alice's chat after bob's attempts: %+v", page)
	}
	if e.prov.count() != 1 {
		t.Errorf("provider called %d times", e.prov.count())
	}
}

func TestChatCRUD(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")

	var c store.Chat
	alice.do("POST", "/api/v1/ai/chats", nil, &c, 201)
	if c.ID == "" || c.Title != "" || c.Context == nil || len(c.Context) != 0 || c.MessageCount != 0 {
		t.Fatalf("empty chat = %+v", c)
	}
	alice.do("POST", "/api/v1/ai/chats", map[string]any{"context": []any{ref(visibleKS), map[string]string{"cluster": "dev"}}}, &c, 201)
	if len(c.Context) != 2 {
		t.Fatalf("chat = %+v", c)
	}

	var list struct {
		Items []store.Chat `json:"items"`
		Next  string       `json:"next"`
	}
	alice.do("GET", "/api/v1/ai/chats?limit=1", nil, &list, 200)
	if len(list.Items) != 1 || list.Next == "" {
		t.Fatalf("first page = %+v", list)
	}
	alice.do("GET", "/api/v1/ai/chats?limit=1&cursor="+list.Next, nil, &list, 200)
	if len(list.Items) != 1 || list.Next != "" {
		t.Fatalf("second page = %+v", list)
	}
	if st, _ := alice.errorCode("GET", "/api/v1/ai/chats?cursor=%25%25", nil); st != 400 {
		t.Errorf("bad cursor: %d", st)
	}

	var patched store.Chat
	alice.do("PATCH", "/api/v1/ai/chats/"+c.ID, map[string]any{"title": "  Renamed \n chat "}, &patched, 200)
	if patched.Title != "Renamed chat" || len(patched.Context) != 2 {
		t.Fatalf("renamed = %+v", patched)
	}
	alice.do("PATCH", "/api/v1/ai/chats/"+c.ID, map[string]any{"context": []any{}}, &patched, 200)
	if patched.Title != "Renamed chat" || patched.Context == nil || len(patched.Context) != 0 {
		t.Fatalf("context cleared = %+v", patched)
	}
	for _, body := range []map[string]any{
		{"title": "   "},
		{"title": strings.Repeat("a", store.MaxTitleLen+1)},
	} {
		if st, _ := alice.errorCode("PATCH", "/api/v1/ai/chats/"+c.ID, body); st != 400 {
			t.Errorf("PATCH %v: %d", body, st)
		}
	}

	// An empty chat is named by its first question.
	var empty store.Chat
	alice.do("POST", "/api/v1/ai/chats", nil, &empty, 201)
	var asked askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{"chatId": empty.ID, "question": "name me"}, &asked, 200)
	if asked.Chat.Title != "name me" {
		t.Errorf("title after the first ask = %q", asked.Chat.Title)
	}

	alice.do("DELETE", "/api/v1/ai/chats/"+c.ID, nil, nil, 204)
	if st, _ := alice.errorCode("GET", "/api/v1/ai/chats/"+c.ID, nil); st != 404 {
		t.Errorf("deleted chat: %d", st)
	}
	if st, _ := alice.errorCode("DELETE", "/api/v1/ai/chats/"+c.ID, nil); st != 404 {
		t.Errorf("delete again: %d", st)
	}
}

func TestChatContextValidation(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")
	many := make([]any, store.MaxChatContext+1)
	for i := range many {
		many[i] = map[string]string{"cluster": "dev"}
	}
	bad := []struct {
		name string
		refs []any
	}{
		{"too many", many},
		{"no cluster", []any{map[string]string{"kind": "Kustomization", "namespace": "team-a", "name": "x"}}},
		{"unknown cluster", []any{map[string]string{"cluster": "nope"}}},
		{"cluster ref with a name", []any{map[string]string{"cluster": "dev", "name": "x"}}},
		{"kind without name", []any{map[string]string{"cluster": "dev", "kind": "Kustomization"}}},
		{"malformed kind", []any{map[string]string{"cluster": "dev", "kind": "not a kind!", "name": "x"}}},
		{"inventory row nobody sees", []any{map[string]string{"cluster": "dev", "kind": "ConfigMap", "namespace": "team-b", "name": "x"}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if st, code := alice.errorCode("POST", "/api/v1/ai/chats", map[string]any{"context": tc.refs}); st != 400 || code != "bad_request" {
				t.Errorf("create: %d %s", st, code)
			}
			if st, code := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{"context": tc.refs, "question": "q"}); st != 400 || code != "bad_request" {
				t.Errorf("ask: %d %s", st, code)
			}
		})
	}
	if e.prov.count() != 0 {
		t.Errorf("provider called for invalid context")
	}
	var list struct {
		Items []store.Chat `json:"items"`
	}
	alice.do("GET", "/api/v1/ai/chats", nil, &list, 200)
	if len(list.Items) != 0 {
		t.Errorf("invalid requests created chats: %+v", list.Items)
	}

	// A kind without its group gets the kind table's group; duplicates go.
	var c store.Chat
	k := map[string]string{"cluster": "dev", "kind": "Kustomization", "namespace": "team-a", "name": "podinfo-visible"}
	alice.do("POST", "/api/v1/ai/chats", map[string]any{"context": []any{k, ref(visibleKS), map[string]string{"cluster": "dev"}}}, &c, 201)
	if len(c.Context) != 2 || c.Context[0].Group != "kustomize.toolkit.fluxcd.io" || c.Context[1] != (store.ResourceRef{Cluster: "dev"}) {
		t.Fatalf("canonical context = %+v", c.Context)
	}

	// The question is capped at 8 KiB.
	if st, _ := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{"question": strings.Repeat("x", ai.MaxQuestionBytes+1)}); st != 400 {
		t.Errorf("long question: %d", st)
	}
	if st, _ := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{"question": "  "}); st != 400 {
		t.Errorf("blank question: %d", st)
	}
	if st, _ := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{"question": strings.Repeat("x", maxAskBody)}); st != 413 {
		t.Errorf("oversized body: %d", st)
	}
}

func TestAskKillSwitch(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")
	var first askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{"question": "before"}, &first, 200)

	e.flags.ai.Store(false)
	if st, code := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{"question": "after"}); st != 503 || code != "disabled" {
		t.Fatalf("ask with AI off: %d %s", st, code)
	}
	if st, code := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{"chatId": first.Chat.ID, "question": "after"}); st != 503 || code != "disabled" {
		t.Fatalf("follow-up with AI off: %d %s", st, code)
	}
	var list struct {
		Items []store.Chat `json:"items"`
	}
	alice.do("GET", "/api/v1/ai/chats", nil, &list, 200)
	if len(list.Items) != 1 {
		t.Fatalf("list with AI off: %+v", list)
	}
	var page chatPage
	alice.do("GET", "/api/v1/ai/chats/"+first.Chat.ID, nil, &page, 200)
	if len(page.Messages) != 2 {
		t.Fatalf("messages with AI off: %+v", page.Messages)
	}
	alice.do("PATCH", "/api/v1/ai/chats/"+first.Chat.ID, map[string]any{"title": "kept"}, nil, 200)
	alice.do("DELETE", "/api/v1/ai/chats/"+first.Chat.ID, nil, nil, 204)
	if e.prov.count() != 1 {
		t.Errorf("provider called %d times", e.prov.count())
	}
}

func TestAskAttachmentsAreNotStored(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")
	var got askResult
	alice.do("POST", "/api/v1/ai/ask", map[string]any{
		"question":    "is this sane?",
		"attachments": []any{map[string]any{"kind": "yaml", "source": "team-a/podinfo", "lines": []string{"spec:", "  marker: ATTACHMENT-MARKER"}}},
	}, &got, 200)
	if !strings.Contains(e.prov.prompt(t, 0), "ATTACHMENT-MARKER") {
		t.Fatal("attachment did not reach the model")
	}
	resp := alice.request("GET", "/api/v1/ai/chats/"+got.Chat.ID, nil)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ATTACHMENT-MARKER") {
		t.Fatalf("attachment stored in the chat: %s", b)
	}
	// Log attachments need ai.allowLogs.
	if st, _ := alice.errorCode("POST", "/api/v1/ai/ask", map[string]any{
		"question": "q", "attachments": []any{map[string]any{"kind": "logs", "source": "p", "lines": []string{"x"}}},
	}); st != 400 {
		t.Errorf("log attachment with allowLogs off: %d", st)
	}
}

func TestChatRoutesNeedCSRF(t *testing.T) {
	e := newChatEnv(t)
	alice := e.login("alice")
	var c store.Chat
	alice.do("POST", "/api/v1/ai/chats", nil, &c, 201)
	saved := alice.csrf
	alice.csrf = "" // no header and no Origin
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/ai/chats", map[string]any{}},
		{"PATCH", "/api/v1/ai/chats/" + c.ID, map[string]any{"title": "x"}},
		{"DELETE", "/api/v1/ai/chats/" + c.ID, nil},
		{"POST", "/api/v1/ai/ask", map[string]any{"question": "q"}},
	} {
		if st, _ := alice.errorCode(tc.method, tc.path, tc.body); st != 403 {
			t.Errorf("%s %s without CSRF: %d", tc.method, tc.path, st)
		}
	}
	alice.csrf = saved
	var page chatPage
	alice.do("GET", "/api/v1/ai/chats/"+c.ID, nil, &page, 200)
	if page.Chat.Title != "" {
		t.Errorf("a request without CSRF changed the chat: %+v", page.Chat)
	}
	if e.prov.count() != 0 {
		t.Error("provider called without CSRF")
	}
}

func TestRetentionPassesChatDays(t *testing.T) {
	r := retention(config.Retention{AuditDays: 90, ResolvedThreadsDays: 7, ChatDays: 30})
	if r.ChatDays != 30 || r.AuditDays != 90 || r.ResolvedThreadsDays != 7 || r.TokenPurgeAfter != tokenPurgeAfter {
		t.Fatalf("retention = %+v", r)
	}
	// The janitor's Prune removes idle chats.
	e := newEnv(t, "")
	old := time.Now().Add(-31 * 24 * time.Hour)
	c, err := e.store.Chats().Create(t.Context(), store.Chat{Owner: "local:alice", Title: "old", CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := e.store.Chats().Create(t.Context(), store.Chat{Owner: "local:alice", Title: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.store.Prune(t.Context(), time.Now(), r)
	if err != nil || st.Chats != 1 {
		t.Fatalf("prune = %+v, %v", st, err)
	}
	if _, err := e.store.Chats().Get(t.Context(), "local:alice", c.ID); err == nil {
		t.Error("old chat kept")
	}
	if _, err := e.store.Chats().Get(t.Context(), "local:alice", fresh.ID); err != nil {
		t.Errorf("fresh chat: %v", err)
	}
}
