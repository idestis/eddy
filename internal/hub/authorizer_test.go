package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eddy-gitops/eddy/internal/identity"
	"github.com/eddy-gitops/eddy/internal/model"
	"github.com/eddy-gitops/eddy/internal/protocol"
)

// countingSender answers from allow and records every batch.
type countingSender struct {
	mu      sync.Mutex
	batches [][]protocol.AccessCheck
	allow   func(protocol.Identity, protocol.AccessCheck) bool
	err     error
	delay   time.Duration
}

func (c *countingSender) send(ctx context.Context, _ string, id protocol.Identity, checks []protocol.AccessCheck) ([]bool, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	c.batches = append(c.batches, checks)
	err := c.err
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	out := make([]bool, len(checks))
	for i, ch := range checks {
		out[i] = c.allow(id, ch)
	}
	return out, nil
}

func (c *countingSender) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

var alice = identity.Principal{User: "local:alice", Groups: []string{"eddy:team-a", "eddy:authenticated"}}

func TestAuthorizerCacheTTL(t *testing.T) {
	cs := &countingSender{allow: defaultAllow}
	a := newAuthorizer(cs.send, newMetrics())
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	check := []protocol.AccessCheck{{Verb: "list", Group: "apps", Resource: "deployments", Namespace: "team-a"}}

	for i := range 3 {
		res, err := a.check(context.Background(), alice, "dev", check)
		if err != nil || !res[0] {
			t.Fatalf("check %d: %v %v", i, res, err)
		}
	}
	if cs.calls() != 1 {
		t.Fatalf("agent asked %d times within the TTL, want 1", cs.calls())
	}
	now = now.Add(accessTTL + time.Second)
	if _, err := a.check(context.Background(), alice, "dev", check); err != nil {
		t.Fatal(err)
	}
	if cs.calls() != 2 {
		t.Fatalf("agent asked %d times after expiry, want 2", cs.calls())
	}
	// Other groups are another subject: a membership change is never served from cache.
	other := alice
	other.Groups = []string{"eddy:team-b"}
	if res, _ := a.check(context.Background(), other, "dev", check); res[0] {
		t.Fatal("answer for different groups reused")
	}
	// Another cluster is asked separately.
	if _, err := a.check(context.Background(), alice, "prod", check); err != nil {
		t.Fatal(err)
	}
	if cs.calls() != 4 {
		t.Fatalf("calls %d, want 4", cs.calls())
	}
}

func TestAuthorizerBatching(t *testing.T) {
	cs := &countingSender{allow: defaultAllow}
	a := newAuthorizer(cs.send, newMetrics())
	var checks []protocol.AccessCheck
	for i := range 250 {
		checks = append(checks, protocol.AccessCheck{Verb: "list", Group: "apps", Resource: "deployments", Namespace: fmt.Sprintf("ns-%d", i)})
	}
	checks = append(checks, checks[0]) // duplicates are asked once
	res, err := a.check(context.Background(), alice, "dev", checks)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != len(checks) {
		t.Fatalf("got %d answers", len(res))
	}
	if cs.calls() != 3 {
		t.Fatalf("250 distinct checks sent in %d requests, want 3 (at most %d each)", cs.calls(), maxChecksPerRequest)
	}
	for _, b := range cs.batches {
		if len(b) > maxChecksPerRequest {
			t.Fatalf("batch of %d", len(b))
		}
	}
}

func TestAuthorizerSingleflight(t *testing.T) {
	cs := &countingSender{allow: defaultAllow, delay: 50 * time.Millisecond}
	a := newAuthorizer(cs.send, newMetrics())
	check := []protocol.AccessCheck{{Verb: "get", Group: "apps", Resource: "deployments", Namespace: "team-a", Name: "web"}}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if res, err := a.check(context.Background(), alice, "dev", check); err != nil || !res[0] {
				t.Errorf("check: %v %v", res, err)
			}
		})
	}
	wg.Wait()
	if cs.calls() != 1 {
		t.Fatalf("20 concurrent identical checks made %d agent calls, want 1", cs.calls())
	}
}

func TestAuthorizerErrorsAreNotCached(t *testing.T) {
	cs := &countingSender{allow: defaultAllow, err: errors.New("agent gone")}
	a := newAuthorizer(cs.send, newMetrics())
	check := []protocol.AccessCheck{{Verb: "list", Resource: "pods", Namespace: "team-a"}}
	if _, err := a.check(context.Background(), alice, "dev", check); err == nil {
		t.Fatal("error swallowed")
	}
	cs.mu.Lock()
	cs.err = nil
	cs.mu.Unlock()
	if res, err := a.check(context.Background(), alice, "dev", check); err != nil || !res[0] {
		t.Fatalf("after recovery: %v %v", res, err)
	}
}

func TestAuthorizerBounded(t *testing.T) {
	cs := &countingSender{allow: defaultAllow}
	a := newAuthorizer(cs.send, newMetrics())
	a.max = 10
	for i := range 30 {
		_, _ = a.check(context.Background(), alice, "dev", []protocol.AccessCheck{{Verb: "list", Namespace: fmt.Sprint(i)}})
	}
	if a.size() > 10 {
		t.Fatalf("cache holds %d entries, bound is 10", a.size())
	}
}

func TestAuthorizerFilterList(t *testing.T) {
	cs := &countingSender{allow: defaultAllow}
	a := newAuthorizer(cs.send, newMetrics())
	rs := []model.Resource{
		res("Kustomization", "team-a", "k1", model.StatusReady),
		res("Kustomization", "team-a", "k2", model.StatusReady),
		res("HelmRelease", "team-a", "h1", model.StatusReady),
		res("Kustomization", "team-b", "k3", model.StatusReady),
		res("Deployment", "team-b", "d1", model.StatusReady),
		{Ref: model.Ref{Kind: "Secret", Namespace: "team-a", Name: "s"}},
	}
	got, err := a.filter(context.Background(), alice, "dev", rs)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.Name] = true
	}
	if len(got) != 3 || !names["k1"] || !names["k2"] || !names["h1"] {
		t.Fatalf("filtered %v", names)
	}
	// One batch with one check per distinct (group, resource, namespace).
	if cs.calls() != 1 || len(cs.batches[0]) != 4 {
		t.Fatalf("batches %v", cs.batches)
	}
	for _, c := range cs.batches[0] {
		if c.Verb != "list" || c.Name != "" {
			t.Fatalf("unexpected check %+v", c)
		}
	}
}
