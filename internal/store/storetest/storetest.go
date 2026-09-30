// Package storetest is the conformance suite every store.Store backend must
// pass. A backend's test calls Run with a constructor for a fresh, empty
// store:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) store.Store {
//			s := memory.New()
//			t.Cleanup(func() { s.Close() })
//			return s
//		})
//	}
//
// newStore is called once per subtest and is responsible for closing the
// store (typically with t.Cleanup).
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/idestis/eddy/internal/store"
)

// Run executes the conformance suite against the backend built by newStore.
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	t.Helper()
	tests := []struct {
		name string
		fn   func(t *testing.T, s store.Store)
	}{
		{"Ping", testPing},
		{"Sessions/CreateGet", testSessionCreateGet},
		{"Sessions/Expiry", testSessionExpiry},
		{"Sessions/Touch", testSessionTouch},
		{"Sessions/Delete", testSessionDelete},
		{"Sessions/LatestGroups", testSessionLatestGroups},
		{"Sessions/DeleteOldest", testSessionDeleteOldest},
		{"Tokens/CreateGet", testTokenCreateGet},
		{"Tokens/GetExcludesInactive", testTokenGetExcludesInactive},
		{"Tokens/List", testTokenList},
		{"Tokens/CountActive", testTokenCountActive},
		{"Tokens/CreateMaxActive", testTokenCreateMaxActive},
		{"Tokens/CreateMaxActiveConcurrent", testTokenCreateMaxActiveConcurrent},
		{"Tokens/Revoke", testTokenRevoke},
		{"Tokens/MarkUsed", testTokenMarkUsed},
		{"Threads/CreateGet", testThreadCreateGet},
		{"Threads/Validation", testThreadValidation},
		{"Threads/AddMessage", testThreadAddMessage},
		{"Threads/MessagePagination", testMessagePagination},
		{"Threads/ListFilters", testThreadListFilters},
		{"Threads/ListPagination", testThreadListPagination},
		{"Threads/SetStatus", testThreadSetStatus},
		{"Threads/DeleteCascades", testThreadDeleteCascades},
		{"Threads/ConcurrentAddMessageLimit", testConcurrentAddMessage},
		{"Audit/AppendQuery", testAuditAppendQuery},
		{"Audit/Filters", testAuditFilters},
		{"Audit/Pagination", testAuditPagination},
		{"Audit/DetailLimit", testAuditDetailLimit},
		{"Prefs", testPrefs},
		{"Prune/Sessions", testPruneSessions},
		{"Prune/Tokens", testPruneTokens},
		{"Prune/Audit", testPruneAudit},
		{"Prune/AuditBatches", testPruneAuditBatches},
		{"Prune/ResolvedThreads", testPruneResolvedThreads},
		{"Prune/AskThreads", testPruneAskThreads},
		{"Prune/Disabled", testPruneDisabled},
		{"Prune/RateLimitsAndAgentSessions", testPruneShared},
		{"RateLimits/Window", testRateLimitWindow},
		{"RateLimits/GetReset", testRateLimitGetReset},
		{"RateLimits/Validation", testRateLimitValidation},
		{"RateLimits/Concurrent", testRateLimitConcurrent},
		{"AgentSessions/UpsertList", testAgentUpsertList},
		{"AgentSessions/Takeover", testAgentTakeover},
		{"AgentSessions/Heartbeat", testAgentHeartbeat},
		{"AgentSessions/Delete", testAgentDelete},
		{"AgentSessions/Validation", testAgentValidation},
		{"Events/PublishSubscribe", testEventsPublishSubscribe},
		{"Events/Unsubscribe", testEventsUnsubscribe},
		{"Events/Validation", testEventsValidation},
		{"JoinTokens/CreateGetList", testJoinCreateGetList},
		{"JoinTokens/Consume", testJoinConsume},
		{"JoinTokens/ConsumeConcurrent", testJoinConsumeConcurrent},
		{"JoinTokens/RevokeAndPrune", testJoinRevokePrune},
		{"ConnectionAttempts", testConnectionAttempts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newStore(t))
		})
	}
}

// t0 has sub-millisecond precision on purpose: backends store unix ms.
var t0 = time.Date(2026, 9, 1, 12, 0, 0, 123456789, time.UTC)

// ms normalises a time the way every backend must.
func ms(t time.Time) time.Time { return time.UnixMilli(t.UnixMilli()).UTC() }

func ptr[T any](v T) *T { return &v }

func check(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func wantErr(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got error %v, want %v", what, err, target)
	}
}

func equal[T any](t *testing.T, got, want T, what string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n got  %+v\n want %+v", what, got, want)
	}
}

func ctx(t *testing.T) context.Context { return t.Context() }

func testPing(t *testing.T, s store.Store) {
	check(t, s.Ping(ctx(t)), "ping")
}

// ---- sessions ----

func session(id, subject string, created time.Time, groups ...string) store.Session {
	return store.Session{
		IDHash:     []byte("hash-" + id),
		Subject:    subject,
		Display:    "Display " + subject,
		Groups:     groups,
		Provider:   "local",
		CreatedAt:  created,
		LastSeenAt: created,
		ExpiresAt:  created.Add(8 * time.Hour),
		UserAgent:  "test-agent",
		RemoteIP:   "10.0.0.1",
	}
}

func normSession(v store.Session) store.Session {
	v.CreatedAt, v.LastSeenAt, v.ExpiresAt = ms(v.CreatedAt), ms(v.LastSeenAt), ms(v.ExpiresAt)
	if len(v.Groups) == 0 {
		v.Groups = nil
	}
	return v
}

func testSessionCreateGet(t *testing.T, s store.Store) {
	for _, groups := range [][]string{nil, {"eddy:dev", "eddy:ops"}} {
		v := session(fmt.Sprint(len(groups)), "alice", t0, groups...)
		check(t, s.Sessions().Create(ctx(t), v), "create")
		got, err := s.Sessions().Get(ctx(t), v.IDHash, t0)
		check(t, err, "get")
		equal(t, got, normSession(v), "session round trip")
	}
	err := s.Sessions().Create(ctx(t), session("0", "bob", t0))
	wantErr(t, err, store.ErrConflict, "duplicate id hash")
	_, err = s.Sessions().Get(ctx(t), []byte("missing"), t0)
	wantErr(t, err, store.ErrNotFound, "missing session")
}

func testSessionExpiry(t *testing.T, s store.Store) {
	v := session("a", "alice", t0)
	check(t, s.Sessions().Create(ctx(t), v), "create")
	tests := []struct {
		name  string
		now   time.Time
		found bool
	}{
		{"before expiry", v.ExpiresAt.Add(-time.Millisecond), true},
		{"at expiry", v.ExpiresAt, false},
		{"after expiry", v.ExpiresAt.Add(time.Second), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Sessions().Get(ctx(t), v.IDHash, tc.now)
			if tc.found {
				check(t, err, "get")
			} else {
				wantErr(t, err, store.ErrNotFound, "get expired")
			}
		})
	}
}

func testSessionTouch(t *testing.T, s store.Store) {
	v := session("a", "alice", t0)
	check(t, s.Sessions().Create(ctx(t), v), "create")
	seen, exp := t0.Add(time.Hour), t0.Add(9*time.Hour)
	check(t, s.Sessions().Touch(ctx(t), v.IDHash, seen, exp), "touch")
	got, err := s.Sessions().Get(ctx(t), v.IDHash, t0.Add(8*time.Hour+time.Minute))
	check(t, err, "get after touch extended expiry")
	equal(t, got.LastSeenAt, ms(seen), "last seen")
	equal(t, got.ExpiresAt, ms(exp), "expires")
	equal(t, got.CreatedAt, ms(t0), "created unchanged")
	wantErr(t, s.Sessions().Touch(ctx(t), []byte("missing"), seen, exp), store.ErrNotFound, "touch missing")
}

func testSessionDelete(t *testing.T, s store.Store) {
	a1, a2, b := session("a1", "alice", t0), session("a2", "alice", t0), session("b", "bob", t0)
	for _, v := range []store.Session{a1, a2, b} {
		check(t, s.Sessions().Create(ctx(t), v), "create")
	}
	check(t, s.Sessions().Delete(ctx(t), a1.IDHash), "delete")
	_, err := s.Sessions().Get(ctx(t), a1.IDHash, t0)
	wantErr(t, err, store.ErrNotFound, "deleted session")
	check(t, s.Sessions().Delete(ctx(t), a1.IDHash), "delete is idempotent")

	check(t, s.Sessions().DeleteBySubject(ctx(t), "alice"), "delete by subject")
	_, err = s.Sessions().Get(ctx(t), a2.IDHash, t0)
	wantErr(t, err, store.ErrNotFound, "alice's other session")
	_, err = s.Sessions().Get(ctx(t), b.IDHash, t0)
	check(t, err, "bob's session survives")
}

func testSessionDeleteOldest(t *testing.T, s store.Store) {
	a1 := session("a1", "alice", t0)
	a2 := session("a2", "alice", t0.Add(time.Hour))
	a3 := session("a3", "alice", t0.Add(2*time.Hour))
	b := session("b", "bob", t0)
	for _, v := range []store.Session{a1, a2, a3, b} {
		check(t, s.Sessions().Create(ctx(t), v), "create")
	}
	check(t, s.Sessions().DeleteOldestBySubject(ctx(t), "alice", 2), "trim to 2")
	_, err := s.Sessions().Get(ctx(t), a1.IDHash, t0)
	wantErr(t, err, store.ErrNotFound, "oldest trimmed")
	for _, v := range []store.Session{a2, a3, b} {
		_, err := s.Sessions().Get(ctx(t), v.IDHash, t0)
		check(t, err, "kept "+v.Subject)
	}
	check(t, s.Sessions().DeleteOldestBySubject(ctx(t), "alice", 5), "keep more than exist")
}

func testSessionLatestGroups(t *testing.T, s store.Store) {
	_, err := s.Sessions().LatestGroups(ctx(t), "alice")
	wantErr(t, err, store.ErrNotFound, "no sessions")

	old := session("old", "alice", t0, "eddy:old")
	recent := session("new", "alice", t0.Add(time.Hour), "eddy:new", "eddy:ops")
	other := session("bob", "bob", t0.Add(2*time.Hour), "eddy:bob")
	for _, v := range []store.Session{old, recent, other} {
		check(t, s.Sessions().Create(ctx(t), v), "create")
	}
	got, err := s.Sessions().LatestGroups(ctx(t), "alice")
	check(t, err, "latest groups")
	equal(t, got, []string{"eddy:new", "eddy:ops"}, "latest by last seen")

	check(t, s.Sessions().Touch(ctx(t), old.IDHash, t0.Add(3*time.Hour), t0.Add(11*time.Hour)), "touch old")
	got, err = s.Sessions().LatestGroups(ctx(t), "alice")
	check(t, err, "latest groups after touch")
	equal(t, got, []string{"eddy:old"}, "touch makes a session the latest")

	check(t, s.Sessions().Create(ctx(t), session("none", "carol", t0)), "create carol")
	got, err = s.Sessions().LatestGroups(ctx(t), "carol")
	check(t, err, "latest groups carol")
	if len(got) != 0 {
		t.Fatalf("carol groups = %v, want empty", got)
	}
}

// ---- tokens ----

func token(id, subject string, created time.Time) store.Token {
	return store.Token{
		ID:        id,
		Hash:      []byte("hmac-" + id),
		Subject:   subject,
		Display:   "Display " + subject,
		Provider:  "proxy",
		Groups:    []string{"eddy:dev"},
		Name:      "laptop " + id,
		Scopes:    []string{"read"},
		CreatedAt: created,
		ExpiresAt: created.Add(30 * 24 * time.Hour),
	}
}

func normToken(v store.Token) store.Token {
	v.CreatedAt, v.ExpiresAt = ms(v.CreatedAt), ms(v.ExpiresAt)
	if v.LastUsedAt != nil {
		v.LastUsedAt = ptr(ms(*v.LastUsedAt))
	}
	if v.RevokedAt != nil {
		v.RevokedAt = ptr(ms(*v.RevokedAt))
	}
	if len(v.Groups) == 0 {
		v.Groups = nil
	}
	if len(v.Scopes) == 0 {
		v.Scopes = nil
	}
	if len(v.Hash) == 0 {
		v.Hash = nil
	}
	return v
}

func testTokenCreateGet(t *testing.T, s store.Store) {
	v := token("aaaaaaaaaaaa", "alice", t0)
	v.Scopes = []string{"read", "operate"}
	v.LastUsedAt = ptr(t0.Add(time.Minute))
	check(t, s.Tokens().Create(ctx(t), v, 0), "create")
	got, err := s.Tokens().Get(ctx(t), v.ID, t0)
	check(t, err, "get")
	equal(t, got, normToken(v), "token round trip includes hash")

	dupID := token("aaaaaaaaaaaa", "bob", t0)
	dupID.Hash = []byte("other")
	wantErr(t, s.Tokens().Create(ctx(t), dupID, 0), store.ErrConflict, "duplicate id")
	dupHash := token("bbbbbbbbbbbb", "bob", t0)
	dupHash.Hash = v.Hash
	wantErr(t, s.Tokens().Create(ctx(t), dupHash, 0), store.ErrConflict, "duplicate hash")

	_, err = s.Tokens().Get(ctx(t), "missing", t0)
	wantErr(t, err, store.ErrNotFound, "missing token")
}

func testTokenGetExcludesInactive(t *testing.T, s store.Store) {
	active := token("active000000", "alice", t0)
	revoked := token("revoked00000", "alice", t0)
	revoked.RevokedAt = ptr(t0.Add(time.Hour))
	expired := token("expired00000", "alice", t0)
	expired.ExpiresAt = t0.Add(time.Hour)
	for _, v := range []store.Token{active, revoked, expired} {
		check(t, s.Tokens().Create(ctx(t), v, 0), "create "+v.ID)
	}
	now := t0.Add(2 * time.Hour)
	tests := []struct {
		id    string
		now   time.Time
		found bool
	}{
		{active.ID, now, true},
		{revoked.ID, now, false},
		{expired.ID, now, false},
		{expired.ID, expired.ExpiresAt, false},
		{expired.ID, expired.ExpiresAt.Add(-time.Millisecond), true},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s@%s", tc.id, tc.now.Format(time.TimeOnly)), func(t *testing.T) {
			_, err := s.Tokens().Get(ctx(t), tc.id, tc.now)
			if tc.found {
				check(t, err, "get")
			} else {
				wantErr(t, err, store.ErrNotFound, "get inactive")
			}
		})
	}
}

func testTokenList(t *testing.T, s store.Store) {
	got, err := s.Tokens().List(ctx(t), "alice")
	check(t, err, "list empty")
	if len(got) != 0 {
		t.Fatalf("empty list = %v", got)
	}
	var want []store.Token
	for i := range 4 {
		v := token(fmt.Sprintf("alice%07d", i), "alice", t0.Add(time.Duration(i)*time.Minute))
		if i == 1 {
			v.RevokedAt = ptr(t0.Add(time.Hour))
		}
		check(t, s.Tokens().Create(ctx(t), v, 0), "create")
		v.Hash = nil
		want = append(want, normToken(v))
	}
	check(t, s.Tokens().Create(ctx(t), token("bob000000000", "bob", t0), 0), "create bob")
	slices.Reverse(want)

	got, err = s.Tokens().List(ctx(t), "alice")
	check(t, err, "list")
	for _, v := range got {
		if v.Hash != nil {
			t.Fatalf("List returned a hash for token %s", v.ID)
		}
	}
	equal(t, got, want, "alice's tokens, newest first, revoked included")
}

func testTokenCountActive(t *testing.T, s store.Store) {
	now := t0.Add(2 * time.Hour)
	a := token("a00000000000", "alice", t0)
	b := token("b00000000000", "alice", t0)
	b.RevokedAt = ptr(t0.Add(time.Hour))
	c := token("c00000000000", "alice", t0)
	c.ExpiresAt = t0.Add(time.Hour)
	d := token("d00000000000", "bob", t0)
	for _, v := range []store.Token{a, b, c, d} {
		check(t, s.Tokens().Create(ctx(t), v, 0), "create")
	}
	n, err := s.Tokens().CountActive(ctx(t), "alice", now)
	check(t, err, "count")
	equal(t, n, 1, "active alice tokens")
	n, err = s.Tokens().CountActive(ctx(t), "nobody", now)
	check(t, err, "count nobody")
	equal(t, n, 0, "active tokens of unknown subject")
}

func testTokenCreateMaxActive(t *testing.T, s store.Store) {
	a := token("a00000000000", "alice", t0)
	b := token("b00000000000", "alice", t0)
	b.RevokedAt = ptr(t0) // inactive: does not count
	c := token("c00000000000", "alice", t0.Add(-40*24*time.Hour))
	for _, v := range []store.Token{a, b, c} {
		check(t, s.Tokens().Create(ctx(t), v, 0), "create "+v.ID)
	}
	d := token("d00000000000", "alice", t0)
	check(t, s.Tokens().Create(ctx(t), d, 2), "second active token under a cap of 2")
	e := token("e00000000000", "alice", t0)
	wantErr(t, s.Tokens().Create(ctx(t), e, 2), store.ErrLimit, "third active token under a cap of 2")
	_, err := s.Tokens().Get(ctx(t), e.ID, t0)
	wantErr(t, err, store.ErrNotFound, "a refused token is not stored")
	check(t, s.Tokens().Create(ctx(t), token("f00000000000", "bob", t0), 1), "another subject has its own cap")
	check(t, s.Tokens().Create(ctx(t), e, 0), "no cap")
	check(t, s.Tokens().Revoke(ctx(t), "alice", a.ID, t0), "revoke")
	check(t, s.Tokens().Create(ctx(t), token("g00000000000", "alice", t0), 3), "revoking frees a slot")
}

func testTokenCreateMaxActiveConcurrent(t *testing.T, s store.Store) {
	const tries, capN = 16, 3
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		ok, lim int
	)
	for i := range tries {
		wg.Go(func() {
			err := s.Tokens().Create(ctx(t), token(fmt.Sprintf("r%011d", i), "alice", t0), capN)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, store.ErrLimit):
				lim++
			default:
				t.Errorf("create %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	equal(t, ok, capN, "tokens created concurrently under the cap")
	equal(t, lim, tries-capN, "tokens refused")
	n, err := s.Tokens().CountActive(ctx(t), "alice", t0)
	check(t, err, "count")
	equal(t, n, capN, "active tokens after the race")
}

func testTokenRevoke(t *testing.T, s store.Store) {
	a := token("a00000000000", "alice", t0)
	a2 := token("a20000000000", "alice", t0)
	b := token("b00000000000", "bob", t0)
	for _, v := range []store.Token{a, a2, b} {
		check(t, s.Tokens().Create(ctx(t), v, 0), "create")
	}
	wantErr(t, s.Tokens().Revoke(ctx(t), "bob", a.ID, t0), store.ErrNotFound, "revoke someone else's token")
	wantErr(t, s.Tokens().Revoke(ctx(t), "alice", "missing", t0), store.ErrNotFound, "revoke missing token")

	at := t0.Add(time.Minute)
	check(t, s.Tokens().Revoke(ctx(t), "alice", a.ID, at), "revoke")
	check(t, s.Tokens().Revoke(ctx(t), "alice", a.ID, at.Add(time.Hour)), "revoke again is idempotent")
	_, err := s.Tokens().Get(ctx(t), a.ID, at)
	wantErr(t, err, store.ErrNotFound, "revoked token")
	list, err := s.Tokens().List(ctx(t), "alice")
	check(t, err, "list")
	for _, v := range list {
		if v.ID == a.ID {
			equal(t, v.RevokedAt, ptr(ms(at)), "first revocation time is kept")
		}
	}

	check(t, s.Tokens().RevokeBySubject(ctx(t), "alice", at), "revoke by subject")
	_, err = s.Tokens().Get(ctx(t), a2.ID, at)
	wantErr(t, err, store.ErrNotFound, "alice's other token")
	_, err = s.Tokens().Get(ctx(t), b.ID, at)
	check(t, err, "bob's token survives")
}

func testTokenMarkUsed(t *testing.T, s store.Store) {
	a := token("a00000000000", "alice", t0)
	check(t, s.Tokens().Create(ctx(t), a, 0), "create")
	used := t0.Add(5 * time.Minute)
	check(t, s.Tokens().MarkUsed(ctx(t), a.ID, used), "mark used")
	got, err := s.Tokens().Get(ctx(t), a.ID, used)
	check(t, err, "get")
	equal(t, got.LastUsedAt, ptr(ms(used)), "last used")
	wantErr(t, s.Tokens().MarkUsed(ctx(t), "missing", used), store.ErrNotFound, "mark missing")
}

// ---- threads ----

var refA = store.ResourceRef{Cluster: "prod", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "apps", Name: "podinfo"}

func human(subject string) store.Author {
	return store.Author{Type: store.AuthorHuman, Subject: subject, Display: strings.ToUpper(subject), Via: "web"}
}

func newThread(ref store.ResourceRef, title string, by store.Author, at time.Time) store.Thread {
	return store.Thread{Ref: ref, Type: store.ThreadDiscussion, Visibility: store.VisibilityResource, Title: title, CreatedBy: by, CreatedAt: at}
}

func msg(body string, by store.Author, at time.Time) store.Message {
	return store.Message{Author: by, Body: body, CreatedAt: at}
}

func mustCreate(t *testing.T, s store.Store, th store.Thread) store.Thread {
	t.Helper()
	got, _, err := s.Threads().Create(ctx(t), th, msg("first", th.CreatedBy, th.CreatedAt))
	check(t, err, "create thread")
	return got
}

func testThreadCreateGet(t *testing.T, s store.Store) {
	by := store.Author{Type: store.AuthorAI, Subject: "alice", Display: "Alice", Via: "askai", Client: "claude-haiku"}
	in := store.Thread{Ref: refA, Type: store.ThreadAsk, Visibility: store.VisibilityPrivate, Title: "Why is podinfo failing?", CreatedBy: by, CreatedAt: t0}
	first := store.Message{Author: by, Body: "Because…", Meta: json.RawMessage(`{"steps":[{"tool":"get"}]}`)}
	th, m, err := s.Threads().Create(ctx(t), in, first)
	check(t, err, "create")
	if th.ID == "" || m.ID == "" {
		t.Fatalf("ids not generated: thread %q message %q", th.ID, m.ID)
	}
	want := in
	want.ID = th.ID
	want.CreatedAt = ms(t0)
	want.UpdatedAt = ms(t0)
	want.Status = store.ThreadOpen
	want.MessageCount = 1
	equal(t, th, want, "created thread")

	got, err := s.Threads().Get(ctx(t), th.ID)
	check(t, err, "get")
	equal(t, got, want, "stored thread")

	wantMsg := store.Message{ID: m.ID, ThreadID: th.ID, Author: by, Body: first.Body, Meta: first.Meta, CreatedAt: ms(t0)}
	equal(t, m, wantMsg, "created first message")
	msgs, next, err := s.Threads().Messages(ctx(t), th.ID, "", 0)
	check(t, err, "messages")
	equal(t, next, "", "no next page")
	equal(t, len(msgs), 1, "message count")
	equal(t, msgs[0].ID, wantMsg.ID, "stored message id")
	equal(t, msgs[0].Author, wantMsg.Author, "stored message author")
	equal(t, msgs[0].Body, wantMsg.Body, "stored message body")
	equal(t, msgs[0].CreatedAt, wantMsg.CreatedAt, "stored message time")
	var meta any
	check(t, json.Unmarshal(msgs[0].Meta, &meta), "stored meta is JSON")
	equal(t, meta, any(map[string]any{"steps": []any{map[string]any{"tool": "get"}}}), "stored meta")

	fixed := newThread(refA, "fixed id", human("bob"), t0)
	fixed.ID = "caller-id"
	got, _, err = s.Threads().Create(ctx(t), fixed, msg("x", human("bob"), t0))
	check(t, err, "create with caller id")
	equal(t, got.ID, "caller-id", "caller-supplied id is kept")
	_, _, err = s.Threads().Create(ctx(t), fixed, msg("x", human("bob"), t0))
	wantErr(t, err, store.ErrConflict, "duplicate thread id")

	_, err = s.Threads().Get(ctx(t), "missing")
	wantErr(t, err, store.ErrNotFound, "missing thread")
}

func testThreadValidation(t *testing.T, s store.Store) {
	by := human("alice")
	tests := []struct {
		name    string
		mutate  func(th *store.Thread, m *store.Message)
		wantErr error // nil = must succeed; errAny = any error
	}{
		{"title at limit", func(th *store.Thread, _ *store.Message) { th.Title = strings.Repeat("é", store.MaxTitleLen) }, nil},
		{"title over limit", func(th *store.Thread, _ *store.Message) { th.Title = strings.Repeat("a", store.MaxTitleLen+1) }, store.ErrLimit},
		{"body at limit", func(_ *store.Thread, m *store.Message) { m.Body = strings.Repeat("a", store.MaxMessageBytes) }, nil},
		{"body over limit", func(_ *store.Thread, m *store.Message) { m.Body = strings.Repeat("a", store.MaxMessageBytes+1) }, store.ErrLimit},
		{"multibyte body over limit", func(_ *store.Thread, m *store.Message) { m.Body = strings.Repeat("é", store.MaxMessageBytes/2+1) }, store.ErrLimit},
		{"bad type", func(th *store.Thread, _ *store.Message) { th.Type = "chat" }, errAny},
		{"bad visibility", func(th *store.Thread, _ *store.Message) { th.Visibility = "public" }, errAny},
		{"missing cluster", func(th *store.Thread, _ *store.Message) { th.Ref.Cluster = "" }, errAny},
		{"bad author type", func(_ *store.Thread, m *store.Message) { m.Author.Type = "bot" }, errAny},
		{"invalid meta", func(_ *store.Thread, m *store.Message) { m.Meta = json.RawMessage(`{`) }, errAny},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			th, m := newThread(refA, "title", by, t0), msg("body", by, t0)
			tc.mutate(&th, &m)
			_, _, err := s.Threads().Create(ctx(t), th, m)
			switch tc.wantErr {
			case nil:
				check(t, err, "create")
			case errAny:
				if err == nil {
					t.Fatal("create succeeded, want an error")
				}
			default:
				wantErr(t, err, tc.wantErr, "create")
			}
		})
	}

	th := mustCreate(t, s, newThread(refA, "t", by, t0))
	_, err := s.Threads().AddMessage(ctx(t), th.ID, msg(strings.Repeat("a", store.MaxMessageBytes+1), by, t0))
	wantErr(t, err, store.ErrLimit, "oversized reply")
}

var errAny = errors.New("any error")

func testThreadAddMessage(t *testing.T, s store.Store) {
	th := mustCreate(t, s, newThread(refA, "t", human("alice"), t0))
	at := t0.Add(time.Minute)
	m, err := s.Threads().AddMessage(ctx(t), th.ID, msg("reply", human("bob"), at))
	check(t, err, "add")
	if m.ID == "" || m.ThreadID != th.ID {
		t.Fatalf("message = %+v", m)
	}
	equal(t, m.CreatedAt, ms(at), "message time")
	got, err := s.Threads().Get(ctx(t), th.ID)
	check(t, err, "get")
	equal(t, got.MessageCount, 2, "message count")
	equal(t, got.UpdatedAt, ms(at), "updated at bumped")
	equal(t, got.CreatedAt, ms(t0), "created at unchanged")

	_, err = s.Threads().AddMessage(ctx(t), "missing", msg("x", human("bob"), at))
	wantErr(t, err, store.ErrNotFound, "add to missing thread")
}

func testMessagePagination(t *testing.T, s store.Store) {
	th := mustCreate(t, s, newThread(refA, "t", human("alice"), t0))
	for i := range 24 {
		// Groups of three messages share a timestamp to exercise the id tie-break.
		at := t0.Add(time.Duration(i/3+1) * time.Second)
		_, err := s.Threads().AddMessage(ctx(t), th.ID, msg(fmt.Sprint(i), human("bob"), at))
		check(t, err, "add")
	}
	all, next, err := s.Threads().Messages(ctx(t), th.ID, "", 500)
	check(t, err, "all")
	equal(t, next, "", "single page")
	equal(t, len(all), 25, "messages")
	for i := 1; i < len(all); i++ {
		a, b := all[i-1], all[i]
		if a.CreatedAt.After(b.CreatedAt) || (a.CreatedAt.Equal(b.CreatedAt) && a.ID >= b.ID) {
			t.Fatalf("messages not in (createdAt, id) order at %d", i)
		}
	}

	var paged []store.Message
	cursor := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("pagination does not terminate")
		}
		items, next, err := s.Threads().Messages(ctx(t), th.ID, cursor, 7)
		check(t, err, "page")
		if len(items) > 7 {
			t.Fatalf("page has %d items, limit 7", len(items))
		}
		paged = append(paged, items...)
		if next == "" {
			break
		}
		cursor = next
	}
	equal(t, ids(paged), ids(all), "paged messages equal the full list")

	_, _, err = s.Threads().Messages(ctx(t), "missing", "", 10)
	wantErr(t, err, store.ErrNotFound, "messages of missing thread")
	if _, _, err := s.Threads().Messages(ctx(t), th.ID, "!!not-a-cursor!!", 10); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func ids[T store.Thread | store.Message](v []T) []string {
	out := make([]string, len(v))
	for i, x := range v {
		switch x := any(x).(type) {
		case store.Thread:
			out[i] = x.ID
		case store.Message:
			out[i] = x.ID
		}
	}
	return out
}

func testThreadListFilters(t *testing.T, s store.Store) {
	refB := store.ResourceRef{Cluster: "prod", Group: "kustomize.toolkit.fluxcd.io", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}
	refC := store.ResourceRef{Cluster: "staging"}
	mk := func(ref store.ResourceRef, title string, by string, typ store.ThreadType, vis store.Visibility, at time.Time) store.Thread {
		th := newThread(ref, title, human(by), at)
		th.Type, th.Visibility = typ, vis
		return mustCreate(t, s, th)
	}
	a1 := mk(refA, "a1", "alice", store.ThreadDiscussion, store.VisibilityResource, t0.Add(1*time.Second))
	a2 := mk(refA, "a2", "bob", store.ThreadDiscussion, store.VisibilityResource, t0.Add(2*time.Second))
	b1 := mk(refB, "b1", "alice", store.ThreadDiscussion, store.VisibilityResource, t0.Add(3*time.Second))
	c1 := mk(refC, "c1", "bob", store.ThreadDiscussion, store.VisibilityResource, t0.Add(4*time.Second))
	pa := mk(refA, "private alice", "alice", store.ThreadAsk, store.VisibilityPrivate, t0.Add(5*time.Second))
	pb := mk(refA, "private bob", "bob", store.ThreadAsk, store.VisibilityPrivate, t0.Add(6*time.Second))
	check(t, s.Threads().SetStatus(ctx(t), a2.ID, store.ThreadResolved, "bob", t0.Add(2500*time.Millisecond)), "resolve a2")

	tests := []struct {
		name string
		f    store.ThreadFilter
		want []store.Thread
	}{
		{"all, no viewer", store.ThreadFilter{}, []store.Thread{c1, b1, a2, a1}},
		{"viewer alice sees her private", store.ThreadFilter{Viewer: "alice"}, []store.Thread{pa, c1, b1, a2, a1}},
		{"viewer bob sees his private", store.ThreadFilter{Viewer: "bob"}, []store.Thread{pb, c1, b1, a2, a1}},
		{"cluster", store.ThreadFilter{Ref: store.ResourceRef{Cluster: "staging"}}, []store.Thread{c1}},
		{"kind", store.ThreadFilter{Ref: store.ResourceRef{Kind: "Kustomization"}}, []store.Thread{b1}},
		{"namespace", store.ThreadFilter{Ref: store.ResourceRef{Namespace: "apps"}, Viewer: "alice"}, []store.Thread{pa, a2, a1}},
		{"exact ref", store.ThreadFilter{Ref: refA}, []store.Thread{a2, a1}},
		{"group", store.ThreadFilter{Ref: store.ResourceRef{Group: "kustomize.toolkit.fluxcd.io"}}, []store.Thread{b1}},
		{"name", store.ThreadFilter{Ref: store.ResourceRef{Name: "podinfo"}}, []store.Thread{a2, a1}},
		{"status open", store.ThreadFilter{Status: store.ThreadOpen}, []store.Thread{c1, b1, a1}},
		{"status resolved", store.ThreadFilter{Status: store.ThreadResolved}, []store.Thread{a2}},
		{"type ask", store.ThreadFilter{Type: store.ThreadAsk, Viewer: "alice"}, []store.Thread{pa}},
		{"no match", store.ThreadFilter{Ref: store.ResourceRef{Cluster: "nope"}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, next, err := s.Threads().List(ctx(t), tc.f)
			check(t, err, "list")
			equal(t, next, "", "next")
			equal(t, ids(got), ids(tc.want), "threads")
		})
	}

	got, _, err := s.Threads().List(ctx(t), store.ThreadFilter{Status: store.ThreadResolved})
	check(t, err, "list resolved")
	if got[0].ResolvedAt == nil || got[0].ResolvedBy != "bob" || got[0].UpdatedAt != ms(t0.Add(2500*time.Millisecond)) {
		t.Fatalf("resolved thread fields = %+v", got[0])
	}
}

func testThreadListPagination(t *testing.T, s store.Store) {
	var created []store.Thread
	for i := range 30 {
		// Pairs share UpdatedAt to exercise the id tie-break.
		created = append(created, mustCreate(t, s, newThread(refA, fmt.Sprint(i), human("alice"), t0.Add(time.Duration(i/2)*time.Second))))
	}
	all, next, err := s.Threads().List(ctx(t), store.ThreadFilter{Limit: 200})
	check(t, err, "list all")
	equal(t, next, "", "single page")
	equal(t, len(all), 30, "threads")
	for i := 1; i < len(all); i++ {
		a, b := all[i-1], all[i]
		if a.UpdatedAt.Before(b.UpdatedAt) || (a.UpdatedAt.Equal(b.UpdatedAt) && a.ID <= b.ID) {
			t.Fatalf("threads not in (updatedAt desc, id desc) order at %d", i)
		}
	}

	// Page through while new threads keep arriving at the top: every
	// original thread must be seen exactly once.
	seen := map[string]int{}
	cursor := ""
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("pagination does not terminate")
		}
		items, next, err := s.Threads().List(ctx(t), store.ThreadFilter{Cursor: cursor, Limit: 7})
		check(t, err, "page")
		if len(items) > 7 {
			t.Fatalf("page has %d items, limit 7", len(items))
		}
		for _, th := range items {
			seen[th.ID]++
		}
		mustCreate(t, s, newThread(refA, "late", human("bob"), t0.Add(time.Hour+time.Duration(page)*time.Second)))
		if next == "" {
			break
		}
		cursor = next
	}
	for _, th := range created {
		if seen[th.ID] != 1 {
			t.Fatalf("thread %s seen %d times", th.ID, seen[th.ID])
		}
	}
	if len(seen) != len(created) {
		t.Fatalf("pagination returned %d threads, want %d (late arrivals must not appear)", len(seen), len(created))
	}

	if _, _, err := s.Threads().List(ctx(t), store.ThreadFilter{Cursor: "%%%"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func testThreadSetStatus(t *testing.T, s store.Store) {
	th := mustCreate(t, s, newThread(refA, "t", human("alice"), t0))
	at := t0.Add(time.Minute)
	check(t, s.Threads().SetStatus(ctx(t), th.ID, store.ThreadResolved, "bob", at), "resolve")
	got, err := s.Threads().Get(ctx(t), th.ID)
	check(t, err, "get")
	equal(t, got.Status, store.ThreadResolved, "status")
	equal(t, got.ResolvedBy, "bob", "resolved by")
	equal(t, got.ResolvedAt, ptr(ms(at)), "resolved at")
	equal(t, got.UpdatedAt, ms(at), "updated at")

	at2 := at.Add(time.Minute)
	check(t, s.Threads().SetStatus(ctx(t), th.ID, store.ThreadOpen, "alice", at2), "reopen")
	got, err = s.Threads().Get(ctx(t), th.ID)
	check(t, err, "get")
	equal(t, got.Status, store.ThreadOpen, "status")
	equal(t, got.ResolvedBy, "", "resolved by cleared")
	equal(t, got.ResolvedAt, (*time.Time)(nil), "resolved at cleared")
	equal(t, got.UpdatedAt, ms(at2), "updated at")

	wantErr(t, s.Threads().SetStatus(ctx(t), "missing", store.ThreadResolved, "bob", at), store.ErrNotFound, "missing")
	if err := s.Threads().SetStatus(ctx(t), th.ID, "archived", "bob", at); err == nil {
		t.Fatal("invalid status accepted")
	}
}

func testThreadDeleteCascades(t *testing.T, s store.Store) {
	by := human("alice")
	th := newThread(refA, "t", by, t0)
	th.ID = "thread-1"
	first := msg("first", by, t0)
	first.ID = "message-1"
	_, _, err := s.Threads().Create(ctx(t), th, first)
	check(t, err, "create")
	reply := msg("reply", by, t0.Add(time.Second))
	reply.ID = "message-2"
	_, err = s.Threads().AddMessage(ctx(t), th.ID, reply)
	check(t, err, "add")
	_, err = s.Threads().AddMessage(ctx(t), th.ID, reply)
	wantErr(t, err, store.ErrConflict, "duplicate message id")
	other := mustCreate(t, s, newThread(refA, "other", by, t0))

	check(t, s.Threads().Delete(ctx(t), th.ID), "delete")
	_, err = s.Threads().Get(ctx(t), th.ID)
	wantErr(t, err, store.ErrNotFound, "get deleted")
	_, _, err = s.Threads().Messages(ctx(t), th.ID, "", 10)
	wantErr(t, err, store.ErrNotFound, "messages of deleted")
	wantErr(t, s.Threads().Delete(ctx(t), th.ID), store.ErrNotFound, "delete again")

	// Re-creating the same ids only works if the old messages were removed.
	_, _, err = s.Threads().Create(ctx(t), th, first)
	check(t, err, "re-create with same ids")
	_, err = s.Threads().AddMessage(ctx(t), th.ID, reply)
	check(t, err, "re-add message with same id")
	msgs, _, err := s.Threads().Messages(ctx(t), th.ID, "", 10)
	check(t, err, "messages")
	equal(t, len(msgs), 2, "only new messages")

	_, err = s.Threads().Get(ctx(t), other.ID)
	check(t, err, "other thread survives")
}

func testConcurrentAddMessage(t *testing.T, s store.Store) {
	th := mustCreate(t, s, newThread(refA, "busy", human("alice"), t0))
	const workers, perWorker = 20, 55 // 1100 attempts for 999 free slots
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ok, full int
		other    []error
	)
	for w := range workers {
		wg.Go(func() {
			for i := range perWorker {
				_, err := s.Threads().AddMessage(context.Background(), th.ID,
					msg(fmt.Sprintf("%d-%d", w, i), human("bob"), t0.Add(time.Duration(i)*time.Millisecond)))
				mu.Lock()
				switch {
				case err == nil:
					ok++
				case errors.Is(err, store.ErrLimit):
					full++
				default:
					other = append(other, err)
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other[0])
	}
	equal(t, ok, store.MaxMessagesPerThread-1, "successful replies")
	equal(t, full, workers*perWorker-ok, "replies rejected with ErrLimit")

	got, err := s.Threads().Get(ctx(t), th.ID)
	check(t, err, "get")
	equal(t, got.MessageCount, store.MaxMessagesPerThread, "message count")
	var total int
	seen := map[string]bool{}
	cursor := ""
	for {
		items, next, err := s.Threads().Messages(ctx(t), th.ID, cursor, 500)
		check(t, err, "messages")
		for _, m := range items {
			if seen[m.ID] {
				t.Fatalf("duplicate message %s", m.ID)
			}
			seen[m.ID] = true
		}
		total += len(items)
		if next == "" {
			break
		}
		cursor = next
	}
	equal(t, total, store.MaxMessagesPerThread, "stored messages")
}

// ---- audit ----

func event(action, subject string, at time.Time) store.AuditEvent {
	return store.AuditEvent{Time: at, Subject: subject, Groups: []string{"eddy:dev"}, Via: "web", Action: action, Result: store.AuditOK}
}

func testAuditAppendQuery(t *testing.T, s store.Store) {
	e := store.AuditEvent{
		Time:      t0,
		RequestID: "req-1",
		Subject:   "alice",
		Groups:    []string{"eddy:dev", "eddy:ops"},
		Via:       "mcp",
		TokenID:   "abcdefabcdef",
		Action:    "reconcile",
		Target:    refA,
		Result:    store.AuditDenied,
		Detail:    json.RawMessage(`{"reason":"rbac"}`),
	}
	check(t, s.Audit().Append(ctx(t), e), "append")
	check(t, s.Audit().Append(ctx(t), event("login", "bob", t0)), "append")
	got, next, err := s.Audit().Query(ctx(t), store.AuditFilter{Subject: "alice"})
	check(t, err, "query")
	equal(t, next, "", "next")
	equal(t, len(got), 1, "events")
	if got[0].ID <= 0 {
		t.Fatalf("event id = %d, want > 0", got[0].ID)
	}
	want := e
	want.ID = got[0].ID
	want.Time = ms(t0)
	var gotDetail, wantDetail any
	check(t, json.Unmarshal(got[0].Detail, &gotDetail), "detail JSON")
	check(t, json.Unmarshal(want.Detail, &wantDetail), "detail JSON")
	equal(t, gotDetail, wantDetail, "detail")
	got[0].Detail, want.Detail = nil, nil
	equal(t, got[0], want, "event round trip")

	all, _, err := s.Audit().Query(ctx(t), store.AuditFilter{})
	check(t, err, "query all")
	equal(t, len(all), 2, "all events")
	if all[0].ID <= all[1].ID {
		t.Fatalf("equal timestamps must order by id desc: %d, %d", all[0].ID, all[1].ID)
	}
	if all[0].Detail != nil {
		t.Fatalf("empty detail = %q, want nil", all[0].Detail)
	}
}

func testAuditFilters(t *testing.T, s store.Store) {
	refB := store.ResourceRef{Cluster: "staging", Kind: "Kustomization", Namespace: "flux-system", Name: "apps"}
	add := func(subject string, ref store.ResourceRef, at time.Time) {
		e := event("reconcile", subject, at)
		e.Target = ref
		check(t, s.Audit().Append(ctx(t), e), "append")
	}
	add("alice", refA, t0)
	add("alice", refB, t0.Add(time.Minute))
	add("bob", refA, t0.Add(2*time.Minute))
	add("bob", store.ResourceRef{}, t0.Add(3*time.Minute))

	type row struct {
		subject string
		cluster string
	}
	tests := []struct {
		name string
		f    store.AuditFilter
		want []row
	}{
		{"all", store.AuditFilter{}, []row{{"bob", ""}, {"bob", "prod"}, {"alice", "staging"}, {"alice", "prod"}}},
		{"subject", store.AuditFilter{Subject: "alice"}, []row{{"alice", "staging"}, {"alice", "prod"}}},
		{"cluster", store.AuditFilter{Target: store.ResourceRef{Cluster: "prod"}}, []row{{"bob", "prod"}, {"alice", "prod"}}},
		{"full target", store.AuditFilter{Target: refB}, []row{{"alice", "staging"}}},
		{"subject and target", store.AuditFilter{Subject: "bob", Target: refA}, []row{{"bob", "prod"}}},
		{"since is inclusive", store.AuditFilter{Since: t0.Add(2 * time.Minute)}, []row{{"bob", ""}, {"bob", "prod"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := s.Audit().Query(ctx(t), tc.f)
			check(t, err, "query")
			var rows []row
			for _, e := range got {
				rows = append(rows, row{e.Subject, e.Target.Cluster})
			}
			equal(t, rows, tc.want, "events")
		})
	}
}

func testAuditPagination(t *testing.T, s store.Store) {
	for i := range 23 {
		check(t, s.Audit().Append(ctx(t), event(fmt.Sprint(i), "alice", t0.Add(time.Duration(i/4)*time.Second))), "append")
	}
	all, _, err := s.Audit().Query(ctx(t), store.AuditFilter{Limit: 500})
	check(t, err, "query all")
	equal(t, len(all), 23, "events")

	var paged []int64
	cursor := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("pagination does not terminate")
		}
		items, next, err := s.Audit().Query(ctx(t), store.AuditFilter{Cursor: cursor, Limit: 5})
		check(t, err, "page")
		if len(items) > 5 {
			t.Fatalf("page has %d items", len(items))
		}
		for _, e := range items {
			paged = append(paged, e.ID)
		}
		// New events arrive at the top and must not show up in later pages.
		check(t, s.Audit().Append(ctx(t), event("late", "alice", t0.Add(time.Hour))), "append late")
		if next == "" {
			break
		}
		cursor = next
	}
	var want []int64
	for _, e := range all {
		want = append(want, e.ID)
	}
	equal(t, paged, want, "paged events equal the full list")

	if _, _, err := s.Audit().Query(ctx(t), store.AuditFilter{Cursor: "***"}); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}

func testAuditDetailLimit(t *testing.T, s store.Store) {
	e := event("x", "alice", t0)
	e.Detail = json.RawMessage(`"` + strings.Repeat("a", 4<<10-2) + `"`)
	check(t, s.Audit().Append(ctx(t), e), "detail at 4 KiB")
	e.Detail = json.RawMessage(`"` + strings.Repeat("a", 4<<10-1) + `"`)
	wantErr(t, s.Audit().Append(ctx(t), e), store.ErrLimit, "detail over 4 KiB")
}

// ---- prefs ----

func testPrefs(t *testing.T, s store.Store) {
	_, err := s.Prefs().Get(ctx(t), "alice")
	wantErr(t, err, store.ErrNotFound, "missing prefs")

	check(t, s.Prefs().Put(ctx(t), "alice", json.RawMessage(`{"theme":"dark"}`)), "put")
	check(t, s.Prefs().Put(ctx(t), "alice", json.RawMessage(`{"theme":"light"}`)), "overwrite")
	got, err := s.Prefs().Get(ctx(t), "alice")
	check(t, err, "get")
	equal(t, string(got), `{"theme":"light"}`, "prefs")
	_, err = s.Prefs().Get(ctx(t), "bob")
	wantErr(t, err, store.ErrNotFound, "prefs are per subject")

	atLimit := json.RawMessage(`"` + strings.Repeat("a", 16<<10-2) + `"`)
	check(t, s.Prefs().Put(ctx(t), "bob", atLimit), "prefs at 16 KiB")
	overLimit := json.RawMessage(`"` + strings.Repeat("a", 16<<10-1) + `"`)
	wantErr(t, s.Prefs().Put(ctx(t), "bob", overLimit), store.ErrLimit, "prefs over 16 KiB")
	if err := s.Prefs().Put(ctx(t), "bob", json.RawMessage(`{`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	got, err = s.Prefs().Get(ctx(t), "bob")
	check(t, err, "get bob")
	equal(t, len(got), len(atLimit), "failed puts leave prefs unchanged")
}

// ---- prune ----

const day = 24 * time.Hour

var now = t0.Add(365 * day)

func testPruneSessions(t *testing.T, s store.Store) {
	expired := session("expired", "alice", now.Add(-9*time.Hour)) // expires now-1h
	edge := session("edge", "alice", now.Add(-8*time.Hour))       // expires exactly now
	live := session("live", "alice", now.Add(-time.Hour))
	for _, v := range []store.Session{expired, edge, live} {
		check(t, s.Sessions().Create(ctx(t), v), "create")
	}
	st, err := s.Prune(ctx(t), now, store.Retention{})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{Sessions: 2}, "stats")
	_, err = s.Sessions().LatestGroups(ctx(t), "alice")
	check(t, err, "live session remains")
	_, err = s.Sessions().Get(ctx(t), live.IDHash, now)
	check(t, err, "live session")
}

func testPruneTokens(t *testing.T, s store.Store) {
	mk := func(id string, expires time.Time, revoked *time.Time) store.Token {
		v := token(id, "alice", t0)
		v.ExpiresAt, v.RevokedAt = expires, revoked
		check(t, s.Tokens().Create(ctx(t), v, 0), "create")
		return v
	}
	mk("expiredLong0", now.Add(-31*day), nil)
	keepExpired := mk("expiredShort", now.Add(-29*day), nil)
	mk("revokedLong0", now.Add(10*day), ptr(now.Add(-31*day)))
	keepRevoked := mk("revokedShort", now.Add(10*day), ptr(now.Add(-day)))
	keepActive := mk("active000000", now.Add(10*day), nil)

	st, err := s.Prune(ctx(t), now, store.Retention{TokenPurgeAfter: 30 * day})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{Tokens: 2}, "stats")
	list, err := s.Tokens().List(ctx(t), "alice")
	check(t, err, "list")
	var got []string
	for _, v := range list {
		got = append(got, v.ID)
	}
	slices.Sort(got)
	want := []string{keepActive.ID, keepExpired.ID, keepRevoked.ID}
	slices.Sort(want)
	equal(t, got, want, "remaining tokens")
}

func testPruneAudit(t *testing.T, s store.Store) {
	check(t, s.Audit().Append(ctx(t), event("old", "alice", now.Add(-91*day))), "append")
	check(t, s.Audit().Append(ctx(t), event("recent", "alice", now.Add(-89*day))), "append")
	st, err := s.Prune(ctx(t), now, store.Retention{AuditDays: 90})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{Audit: 1}, "stats")
	got, _, err := s.Audit().Query(ctx(t), store.AuditFilter{})
	check(t, err, "query")
	equal(t, len(got), 1, "remaining events")
	equal(t, got[0].Action, "recent", "remaining event")
}

func testPruneAuditBatches(t *testing.T, s store.Store) {
	if testing.Short() {
		t.Skip("slow in -short mode")
	}
	const n = 5003 // more than one prune batch of 5000
	for i := range n {
		check(t, s.Audit().Append(ctx(t), event("old", "alice", now.Add(-100*day+time.Duration(i)*time.Millisecond))), "append")
	}
	check(t, s.Audit().Append(ctx(t), event("recent", "alice", now)), "append")
	st, err := s.Prune(ctx(t), now, store.Retention{AuditDays: 90})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{Audit: n}, "stats")
	got, _, err := s.Audit().Query(ctx(t), store.AuditFilter{})
	check(t, err, "query")
	equal(t, len(got), 1, "remaining events")
}

func testPruneResolvedThreads(t *testing.T, s store.Store) {
	oldResolved := mustCreate(t, s, newThread(refA, "old resolved", human("alice"), now.Add(-30*day)))
	check(t, s.Threads().SetStatus(ctx(t), oldResolved.ID, store.ThreadResolved, "alice", now.Add(-8*day)), "resolve")
	_, err := s.Threads().AddMessage(ctx(t), oldResolved.ID, msg("late reply", human("bob"), now.Add(-time.Hour)))
	check(t, err, "reply after resolve")
	newResolved := mustCreate(t, s, newThread(refA, "new resolved", human("alice"), now.Add(-30*day)))
	check(t, s.Threads().SetStatus(ctx(t), newResolved.ID, store.ThreadResolved, "alice", now.Add(-6*day)), "resolve")
	oldOpen := mustCreate(t, s, newThread(refA, "old open", human("alice"), now.Add(-300*day)))

	st, err := s.Prune(ctx(t), now, store.Retention{ResolvedThreadsDays: 7})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{Threads: 1}, "stats")
	_, err = s.Threads().Get(ctx(t), oldResolved.ID)
	wantErr(t, err, store.ErrNotFound, "old resolved thread is pruned by resolved_at")
	for _, th := range []store.Thread{newResolved, oldOpen} {
		_, err = s.Threads().Get(ctx(t), th.ID)
		check(t, err, "kept "+th.Title)
	}
}

func testPruneAskThreads(t *testing.T, s store.Store) {
	ask := func(title string, created, lastReply time.Time) store.Thread {
		th := newThread(refA, title, human("alice"), created)
		th.Type, th.Visibility = store.ThreadAsk, store.VisibilityPrivate
		th = mustCreate(t, s, th)
		if !lastReply.IsZero() {
			_, err := s.Threads().AddMessage(ctx(t), th.ID, msg("answer", human("alice"), lastReply))
			check(t, err, "reply")
		}
		return th
	}
	stale := ask("stale", now.Add(-40*day), now.Add(-31*day))
	active := ask("active", now.Add(-40*day), now.Add(-29*day))
	fresh := ask("fresh", now.Add(-day), time.Time{})
	discussion := mustCreate(t, s, newThread(refA, "old discussion", human("alice"), now.Add(-300*day)))

	st, err := s.Prune(ctx(t), now, store.Retention{AskThreadsDays: 30})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{Threads: 1}, "stats")
	_, err = s.Threads().Get(ctx(t), stale.ID)
	wantErr(t, err, store.ErrNotFound, "stale ask thread")
	for _, th := range []store.Thread{active, fresh, discussion} {
		_, err = s.Threads().Get(ctx(t), th.ID)
		check(t, err, "kept "+th.Title)
	}
}

func testPruneDisabled(t *testing.T, s store.Store) {
	tok := token("old000000000", "alice", t0)
	tok.ExpiresAt = t0.Add(day)
	check(t, s.Tokens().Create(ctx(t), tok, 0), "create token")
	check(t, s.Audit().Append(ctx(t), event("old", "alice", t0)), "append")
	th := newThread(refA, "old ask", human("alice"), t0)
	th.Type, th.Visibility = store.ThreadAsk, store.VisibilityPrivate
	th = mustCreate(t, s, th)
	check(t, s.Threads().SetStatus(ctx(t), th.ID, store.ThreadResolved, "alice", t0), "resolve")

	st, err := s.Prune(ctx(t), now, store.Retention{})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{}, "zero retention keeps everything but expired sessions")
}

func testPruneShared(t *testing.T, s store.Store) {
	rl := s.RateLimits()
	_, _, err := rl.Hit(ctx(t), "ended", time.Minute, 0, now.Add(-2*time.Minute))
	check(t, err, "hit ended")
	_, _, err = rl.Hit(ctx(t), "edge", time.Minute, 0, now.Add(-time.Minute)) // ends exactly now
	check(t, err, "hit edge")
	_, _, err = rl.Hit(ctx(t), "open", time.Minute, 0, now.Add(-time.Second))
	check(t, err, "hit open")

	as := s.AgentSessions()
	stale := agentSession("prod", "hub-0", "agent-a", 1, now.Add(-time.Hour))
	stale.HeartbeatAt = now.Add(-store.AgentSessionPruneAfter)
	fresh := agentSession("prod", "hub-0", "agent-b", 1, now.Add(-time.Hour))
	fresh.HeartbeatAt = now.Add(-store.AgentSessionPruneAfter + time.Second)
	check(t, as.Upsert(ctx(t), stale), "upsert stale")
	check(t, as.Upsert(ctx(t), fresh), "upsert fresh")

	st, err := s.Prune(ctx(t), now, store.Retention{})
	check(t, err, "prune")
	equal(t, st, store.PruneStats{RateLimits: 2, AgentSessions: 1}, "stats")
	n, _, err := rl.Get(ctx(t), "open", now)
	check(t, err, "get open")
	equal(t, n, 1, "open window kept")
	list, err := as.List(ctx(t), "", time.Time{})
	check(t, err, "list")
	equal(t, list, []store.AgentSession{normAgent(fresh)}, "fresh agent session kept")
}

// ---- rate limits ----

func testRateLimitWindow(t *testing.T, s store.Store) {
	rl := s.RateLimits()
	hit := func(key string, at time.Time) (int, bool) {
		t.Helper()
		n, ok, err := rl.Hit(ctx(t), key, time.Minute, 3, at)
		check(t, err, "hit")
		return n, ok
	}
	type res struct {
		n  int
		ok bool
	}
	var got []res
	for _, at := range []time.Duration{0, time.Second, 2 * time.Second, 59 * time.Second} {
		n, ok := hit("login:ip:10.0.0.1", t0.Add(at))
		got = append(got, res{n, ok})
	}
	equal(t, got, []res{{1, true}, {2, true}, {3, true}, {4, false}}, "hits in the first window")

	n, ok := hit("login:ip:10.0.0.2", t0.Add(30*time.Second))
	equal(t, res{n, ok}, res{1, true}, "other key is independent")

	// The window is anchored at the first hit (t0) and ends at t0+1m.
	n, ok = hit("login:ip:10.0.0.1", t0.Add(time.Minute))
	equal(t, res{n, ok}, res{1, true}, "first hit of the next window")
	n, ok = hit("login:ip:10.0.0.1", t0.Add(time.Minute+time.Second))
	equal(t, res{n, ok}, res{2, true}, "second hit of the next window")

	cnt, reset, err := rl.Get(ctx(t), "login:ip:10.0.0.1", t0.Add(90*time.Second))
	check(t, err, "get")
	equal(t, cnt, 2, "count")
	equal(t, reset, ms(t0.Add(2*time.Minute)), "window end")

	n, ok, err = rl.Hit(ctx(t), "unlimited", time.Minute, 0, t0)
	check(t, err, "hit unlimited")
	equal(t, res{n, ok}, res{1, true}, "limit 0 is unlimited")
}

func testRateLimitGetReset(t *testing.T, s store.Store) {
	rl := s.RateLimits()
	n, reset, err := rl.Get(ctx(t), "missing", t0)
	check(t, err, "get missing")
	equal(t, n, 0, "missing count")
	equal(t, reset, time.Time{}, "missing reset")

	// A single hit with a long window works as a lockout of exact length.
	_, _, err = rl.Hit(ctx(t), "login:lock:alice", 15*time.Minute, 1, t0)
	check(t, err, "lock")
	n, reset, err = rl.Get(ctx(t), "login:lock:alice", t0.Add(14*time.Minute))
	check(t, err, "get locked")
	equal(t, n, 1, "locked count")
	equal(t, reset, ms(t0.Add(15*time.Minute)), "locked until")
	n, _, err = rl.Get(ctx(t), "login:lock:alice", t0.Add(15*time.Minute))
	check(t, err, "get after lock")
	equal(t, n, 0, "lock ended")

	for range 3 {
		_, _, err = rl.Hit(ctx(t), "login:user:bob", 15*time.Minute, 5, t0)
		check(t, err, "hit")
	}
	check(t, rl.Reset(ctx(t), "login:user:bob"), "reset")
	check(t, rl.Reset(ctx(t), "login:user:bob"), "reset is idempotent")
	n, _, err = rl.Get(ctx(t), "login:user:bob", t0)
	check(t, err, "get after reset")
	equal(t, n, 0, "count after reset")
	n, _, err = rl.Hit(ctx(t), "login:user:bob", 15*time.Minute, 5, t0)
	check(t, err, "hit after reset")
	equal(t, n, 1, "new window after reset")
}

func testRateLimitValidation(t *testing.T, s store.Store) {
	_, _, err := s.RateLimits().Hit(ctx(t), "", time.Minute, 1, t0)
	wantErr(t, err, store.ErrInvalid, "empty key")
	_, _, err = s.RateLimits().Hit(ctx(t), "k", 0, 1, t0)
	wantErr(t, err, store.ErrInvalid, "zero window")
}

func testRateLimitConcurrent(t *testing.T, s store.Store) {
	const workers, perWorker, limit = 25, 40, 600 // 1000 hits against a limit of 600
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
		counts  = map[int]bool{}
		errs    []error
	)
	for w := range workers {
		wg.Go(func() {
			for i := range perWorker {
				// Spread the hits over the window; none reaches its end.
				at := t0.Add(time.Duration(w*perWorker+i) * time.Millisecond)
				n, ok, err := s.RateLimits().Hit(context.Background(), "mcp:calls:tok", time.Hour, limit, at)
				mu.Lock()
				if err != nil {
					errs = append(errs, err)
				} else {
					if counts[n] {
						errs = append(errs, fmt.Errorf("count %d returned twice", n))
					}
					counts[n] = true
					if ok {
						allowed++
					}
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs[0])
	}
	equal(t, allowed, limit, "allowed hits")
	equal(t, len(counts), workers*perWorker, "distinct counts")
	n, _, err := s.RateLimits().Get(ctx(t), "mcp:calls:tok", t0.Add(time.Minute))
	check(t, err, "get")
	equal(t, n, workers*perWorker, "final count")
}

// ---- agent sessions ----

func agentSession(cluster, hub, instance string, seq int64, at time.Time) store.AgentSession {
	return store.AgentSession{
		Cluster: cluster, HubPod: hub, HubAddr: hub + ".peers:8444", AgentInstance: instance,
		Seq: seq, ConnectedAt: at, HeartbeatAt: at,
	}
}

func normAgent(v store.AgentSession) store.AgentSession {
	v.ConnectedAt, v.HeartbeatAt = ms(v.ConnectedAt), ms(v.HeartbeatAt)
	return v
}

func agentKeys(v []store.AgentSession) []string {
	var out []string
	for _, a := range v {
		out = append(out, fmt.Sprintf("%s/%s@%s#%d", a.Cluster, a.AgentInstance, a.HubPod, a.Seq))
	}
	return out
}

func testAgentUpsertList(t *testing.T, s store.Store) {
	as := s.AgentSessions()
	b := agentSession("prod", "hub-1", "agent-b", 1, t0.Add(time.Second))
	a := agentSession("prod", "hub-0", "agent-a", 3, t0)
	c := agentSession("prod", "hub-0", "agent-c", 1, t0.Add(time.Second)) // same time as b: hub-0 sorts first
	other := agentSession("dev", "hub-1", "agent-a", 1, t0.Add(-time.Second))
	for _, v := range []store.AgentSession{b, a, c, other} {
		check(t, as.Upsert(ctx(t), v), "upsert")
	}
	got, err := as.List(ctx(t), "prod", time.Time{})
	check(t, err, "list prod")
	equal(t, got, []store.AgentSession{normAgent(a), normAgent(c), normAgent(b)}, "prod sessions, oldest first")

	got, err = as.List(ctx(t), "", time.Time{})
	check(t, err, "list all")
	equal(t, agentKeys(got), []string{"dev/agent-a@hub-1#1", "prod/agent-a@hub-0#3", "prod/agent-c@hub-0#1", "prod/agent-b@hub-1#1"}, "all sessions")

	got, err = as.List(ctx(t), "prod", t0)
	check(t, err, "list fresh")
	equal(t, agentKeys(got), []string{"prod/agent-c@hub-0#1", "prod/agent-b@hub-1#1"}, "heartbeat after freshAfter only")

	got, err = as.List(ctx(t), "missing", time.Time{})
	check(t, err, "list missing")
	equal(t, len(got), 0, "no sessions")

	// Defaults: ConnectedAt = now, HeartbeatAt = ConnectedAt.
	before := time.Now().Add(-time.Second)
	check(t, as.Upsert(ctx(t), store.AgentSession{Cluster: "qa", HubPod: "hub-0", AgentInstance: "x"}), "upsert defaults")
	got, err = as.List(ctx(t), "qa", before)
	check(t, err, "list qa")
	if len(got) != 1 || got[0].ConnectedAt.IsZero() || !got[0].HeartbeatAt.Equal(got[0].ConnectedAt) {
		t.Fatalf("defaults not applied: %+v", got)
	}
}

func testAgentTakeover(t *testing.T, s store.Store) {
	as := s.AgentSessions()
	check(t, as.Upsert(ctx(t), agentSession("prod", "hub-0", "agent-a", 7, t0)), "upsert on hub-0")

	// A refresh from the same replica with the same seq is accepted.
	refresh := agentSession("prod", "hub-0", "agent-a", 7, t0)
	refresh.HeartbeatAt = t0.Add(time.Second)
	check(t, as.Upsert(ctx(t), refresh), "same seq, same hub")

	// Same seq on another replica, or an older seq, loses.
	wantErr(t, as.Upsert(ctx(t), agentSession("prod", "hub-1", "agent-a", 7, t0.Add(time.Second))), store.ErrConflict, "same seq, other hub")
	wantErr(t, as.Upsert(ctx(t), agentSession("prod", "hub-1", "agent-a", 6, t0.Add(time.Second))), store.ErrConflict, "older seq")

	// The same instance reconnecting to hub-1 with a higher seq takes over.
	newer := agentSession("prod", "hub-1", "agent-a", 8, t0.Add(2*time.Second))
	check(t, as.Upsert(ctx(t), newer), "newer seq")
	got, err := as.List(ctx(t), "prod", time.Time{})
	check(t, err, "list")
	equal(t, got, []store.AgentSession{normAgent(newer)}, "one session per instance")

	// The old replica can no longer heartbeat or delete it.
	wantErr(t, as.Heartbeat(ctx(t), "prod", "hub-0", "agent-a", t0.Add(3*time.Second)), store.ErrNotFound, "old hub heartbeat")
	check(t, as.Delete(ctx(t), "prod", "hub-0", "agent-a"), "old hub delete is a no-op")
	got, err = as.List(ctx(t), "prod", time.Time{})
	check(t, err, "list")
	equal(t, len(got), 1, "session survives old hub delete")
}

func testAgentHeartbeat(t *testing.T, s store.Store) {
	as := s.AgentSessions()
	v := agentSession("prod", "hub-0", "agent-a", 1, t0)
	check(t, as.Upsert(ctx(t), v), "upsert")
	got, err := as.List(ctx(t), "prod", t0.Add(20*time.Second))
	check(t, err, "list")
	equal(t, len(got), 0, "stale before heartbeat")

	check(t, as.Heartbeat(ctx(t), "prod", "hub-0", "agent-a", t0.Add(30*time.Second)), "heartbeat")
	got, err = as.List(ctx(t), "prod", t0.Add(20*time.Second))
	check(t, err, "list")
	v.HeartbeatAt = t0.Add(30 * time.Second)
	equal(t, got, []store.AgentSession{normAgent(v)}, "fresh after heartbeat, connectedAt unchanged")

	wantErr(t, as.Heartbeat(ctx(t), "prod", "hub-0", "agent-z", t0), store.ErrNotFound, "unknown instance")
	wantErr(t, as.Heartbeat(ctx(t), "dev", "hub-0", "agent-a", t0), store.ErrNotFound, "other cluster")
}

func testAgentDelete(t *testing.T, s store.Store) {
	as := s.AgentSessions()
	for _, v := range []store.AgentSession{
		agentSession("prod", "hub-0", "agent-a", 1, t0),
		agentSession("prod", "hub-0", "agent-b", 1, t0),
		agentSession("dev", "hub-0", "agent-c", 1, t0),
		agentSession("prod", "hub-1", "agent-d", 1, t0),
	} {
		check(t, as.Upsert(ctx(t), v), "upsert")
	}
	check(t, as.Delete(ctx(t), "prod", "hub-0", "agent-a"), "delete")
	check(t, as.Delete(ctx(t), "prod", "hub-0", "agent-a"), "delete is idempotent")
	got, err := as.List(ctx(t), "", time.Time{})
	check(t, err, "list")
	equal(t, agentKeys(got), []string{"prod/agent-b@hub-0#1", "dev/agent-c@hub-0#1", "prod/agent-d@hub-1#1"}, "after delete")

	check(t, as.DeleteByHub(ctx(t), "hub-0"), "delete by hub")
	check(t, as.DeleteByHub(ctx(t), "hub-0"), "delete by hub is idempotent")
	got, err = as.List(ctx(t), "", time.Time{})
	check(t, err, "list")
	equal(t, agentKeys(got), []string{"prod/agent-d@hub-1#1"}, "after delete by hub")
}

func testAgentValidation(t *testing.T, s store.Store) {
	as := s.AgentSessions()
	for name, v := range map[string]store.AgentSession{
		"no cluster":   {HubPod: "h", AgentInstance: "a"},
		"no hub":       {Cluster: "c", AgentInstance: "a"},
		"no instance":  {Cluster: "c", HubPod: "h"},
		"negative seq": {Cluster: "c", HubPod: "h", AgentInstance: "a", Seq: -1},
	} {
		wantErr(t, as.Upsert(ctx(t), v), store.ErrInvalid, name)
	}
}

// ---- events ----

// EventTimeout bounds how long the events tests wait for a delivery.
const EventTimeout = 10 * time.Second

func receive(t *testing.T, ch <-chan store.Event, what string) store.Event {
	t.Helper()
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("%s: channel closed", what)
			}
			if e.Kind == store.EventResync {
				continue
			}
			return e
		case <-time.After(EventTimeout):
			t.Fatalf("%s: no event after %s", what, EventTimeout)
		}
	}
}

func testEventsPublishSubscribe(t *testing.T, s store.Store) {
	a, err := s.Events().Subscribe(ctx(t))
	check(t, err, "subscribe a")
	b, err := s.Events().Subscribe(ctx(t))
	check(t, err, "subscribe b")

	sent := []store.Event{
		{Kind: store.EventThread, ID: "0199-thread", Cluster: "prod"},
		{Kind: store.EventRevoke, ID: "alice"},
		{Kind: store.EventAgent, Cluster: "prod"},
	}
	for _, e := range sent {
		check(t, s.Events().Publish(ctx(t), e), "publish")
	}
	for name, ch := range map[string]<-chan store.Event{"a": a, "b": b} {
		for i, want := range sent {
			equal(t, receive(t, ch, fmt.Sprintf("subscriber %s event %d", name, i)), want, "event")
		}
	}
}

func testEventsUnsubscribe(t *testing.T, s store.Store) {
	sub, cancel := context.WithCancel(ctx(t))
	ch, err := s.Events().Subscribe(sub)
	check(t, err, "subscribe")
	other, err := s.Events().Subscribe(ctx(t))
	check(t, err, "subscribe other")
	cancel()
	deadline := time.After(EventTimeout)
	for open := true; open; {
		select {
		case _, open = <-ch:
		case <-deadline:
			t.Fatal("channel not closed after its context was cancelled")
		}
	}
	check(t, s.Events().Publish(ctx(t), store.Event{Kind: store.EventThread, ID: "x"}), "publish")
	equal(t, receive(t, other, "other subscriber"), store.Event{Kind: store.EventThread, ID: "x"}, "other still receives")

	done, stop := context.WithCancel(ctx(t))
	stop()
	if _, err := s.Events().Subscribe(done); err == nil {
		t.Fatal("subscribe with a done context succeeded")
	}
}

func testEventsValidation(t *testing.T, s store.Store) {
	wantErr(t, s.Events().Publish(ctx(t), store.Event{}), store.ErrInvalid, "empty kind")
	wantErr(t, s.Events().Publish(ctx(t), store.Event{Kind: store.EventResync}), store.ErrInvalid, "resync is store-only")
	wantErr(t, s.Events().Publish(ctx(t), store.Event{Kind: "bogus"}), store.ErrInvalid, "unknown kind")
	wantErr(t, s.Events().Publish(ctx(t), store.Event{Kind: store.EventThread, ID: strings.Repeat("x", store.MaxEventBytes)}),
		store.ErrLimit, "oversized event")
}

// ---- join tokens ----

func joinToken(id, cluster string, created time.Time) store.JoinToken {
	return store.JoinToken{
		ID: id, Cluster: cluster, Hash: []byte("hash-" + id), CreatedBy: "local:alice",
		CreatedAt: created, ExpiresAt: created.Add(time.Hour),
	}
}

func testJoinCreateGetList(t *testing.T, s store.Store) {
	ctx := t.Context()
	j := s.JoinTokens()
	t0 := time.UnixMilli(1_700_000_000_000).UTC()
	a := joinToken("aaaaaaaaaaaa", "prod", t0)
	if err := j.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := j.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, a) {
		t.Fatalf("Get = %+v, want %+v", got, a)
	}
	if err := j.Create(ctx, a); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate id: %v", err)
	}
	dupHash := joinToken("bbbbbbbbbbbb", "prod", t0)
	dupHash.Hash = a.Hash
	if err := j.Create(ctx, dupHash); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate hash: %v", err)
	}
	// A new token revokes the cluster's unused predecessor, not other clusters'.
	other := joinToken("cccccccccccc", "staging", t0)
	if err := j.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	b := joinToken("dddddddddddd", "prod", t0.Add(time.Minute))
	if err := j.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := j.Get(ctx, a.ID); got.RevokedAt == nil || !got.RevokedAt.Equal(b.CreatedAt) {
		t.Fatalf("predecessor not revoked: %+v", got)
	}
	if got, _ := j.Get(ctx, other.ID); got.RevokedAt != nil {
		t.Fatal("another cluster's token was revoked")
	}
	list, err := j.List(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID || list[0].Hash != nil {
		t.Fatalf("List = %+v", list)
	}
	if _, err := j.Get(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Get unknown: %v", err)
	}
	for _, bad := range []store.JoinToken{{Cluster: "x", Hash: []byte("h"), ExpiresAt: t0}, {ID: "x", Hash: []byte("h"), ExpiresAt: t0}, {ID: "x", Cluster: "x", ExpiresAt: t0}, {ID: "x", Cluster: "x", Hash: []byte("h")}} {
		if err := j.Create(ctx, bad); !errors.Is(err, store.ErrInvalid) {
			t.Fatalf("Create(%+v) = %v, want ErrInvalid", bad, err)
		}
	}
}

func testJoinConsume(t *testing.T, s store.Store) {
	ctx := t.Context()
	j := s.JoinTokens()
	t0 := time.UnixMilli(1_700_000_000_000).UTC()
	a := joinToken("aaaaaaaaaaaa", "prod", t0)
	if err := j.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Consume(ctx, a.ID, a.ExpiresAt); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("consume at expiry: %v", err)
	}
	now := t0.Add(time.Minute)
	got, err := j.Consume(ctx, a.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.UsedAt == nil || !got.UsedAt.Equal(now) || got.Cluster != "prod" || string(got.Hash) != string(a.Hash) {
		t.Fatalf("Consume = %+v", got)
	}
	if _, err := j.Consume(ctx, a.ID, now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second consume: %v", err)
	}
	if _, err := j.Consume(ctx, "unknown", now); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown consume: %v", err)
	}
	// A used token is not revoked by its successor.
	b := joinToken("bbbbbbbbbbbb", "prod", now)
	if err := j.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := j.Get(ctx, a.ID); got.RevokedAt != nil {
		t.Fatal("used token revoked")
	}
	c := joinToken("cccccccccccc", "prod", now.Add(time.Second))
	if err := j.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Consume(ctx, b.ID, now.Add(2*time.Second)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked consume: %v", err)
	}
}

func testJoinConsumeConcurrent(t *testing.T, s store.Store) {
	ctx := t.Context()
	j := s.JoinTokens()
	t0 := time.UnixMilli(1_700_000_000_000).UTC()
	if err := j.Create(ctx, joinToken("aaaaaaaaaaaa", "prod", t0)); err != nil {
		t.Fatal(err)
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for range 8 {
		wg.Go(func() {
			if _, err := j.Consume(ctx, "aaaaaaaaaaaa", t0.Add(time.Second)); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if !errors.Is(err, store.ErrNotFound) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("%d concurrent consumes succeeded, want 1", ok)
	}
}

func testJoinRevokePrune(t *testing.T, s store.Store) {
	ctx := t.Context()
	j := s.JoinTokens()
	t0 := time.UnixMilli(1_700_000_000_000).UTC()
	if err := j.Create(ctx, joinToken("aaaaaaaaaaaa", "prod", t0)); err != nil {
		t.Fatal(err)
	}
	if err := j.RevokeByCluster(ctx, "prod", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := j.RevokeByCluster(ctx, "prod", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := j.Get(ctx, "aaaaaaaaaaaa")
	if err != nil || got.RevokedAt == nil || !got.RevokedAt.Equal(t0.Add(time.Second)) {
		t.Fatalf("revoked %+v %v", got, err)
	}
	if err := j.Create(ctx, joinToken("bbbbbbbbbbbb", "prod", t0.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	st, err := s.Prune(ctx, t0.Add(time.Second).Add(store.JoinTokenPruneAfter), store.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if st.JoinTokens != 1 {
		t.Fatalf("pruned %d join tokens, want 1", st.JoinTokens)
	}
	if _, err := j.Get(ctx, "aaaaaaaaaaaa"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("old token kept")
	}
	if _, err := j.Get(ctx, "bbbbbbbbbbbb"); err != nil {
		t.Fatal("live token pruned")
	}
}

func testConnectionAttempts(t *testing.T, s store.Store) {
	ctx := t.Context()
	c := s.ConnectionAttempts()
	t0 := time.UnixMilli(1_700_000_000_000).UTC()
	for i := range store.MaxAttemptsPerCluster + 5 {
		a := store.ConnectionAttempt{Cluster: "prod", At: t0.Add(time.Duration(i) * time.Second), Reason: store.AttemptBadToken, Detail: fmt.Sprint(i), Peer: "10.0.0.1"}
		if err := c.Record(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Record(ctx, store.ConnectionAttempt{Cluster: "staging", At: t0, Reason: store.AttemptJoinUsed, Detail: strings.Repeat("x", 1000)}); err != nil {
		t.Fatal(err)
	}
	list, err := c.List(ctx, "prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != store.MaxAttemptsPerCluster || list[0].Detail != fmt.Sprint(store.MaxAttemptsPerCluster+4) || list[len(list)-1].Detail != "5" {
		t.Fatalf("List kept %d, first %+v", len(list), list[0])
	}
	if !list[0].At.Equal(t0.Add(time.Duration(store.MaxAttemptsPerCluster+4)*time.Second)) || list[0].Peer != "10.0.0.1" || list[0].Reason != store.AttemptBadToken {
		t.Fatalf("attempt %+v", list[0])
	}
	st, _ := c.List(ctx, "staging")
	if len(st) != 1 || len(st[0].Detail) > 300 {
		t.Fatalf("staging %+v", st)
	}
	if err := c.DeleteByCluster(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if list, _ := c.List(ctx, "prod"); len(list) != 0 {
		t.Fatalf("after delete: %+v", list)
	}
	if err := c.Record(ctx, store.ConnectionAttempt{Cluster: "prod"}); !errors.Is(err, store.ErrInvalid) {
		t.Fatalf("no reason: %v", err)
	}
}
