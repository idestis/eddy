package agent

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	"github.com/idestis/eddy/internal/protocol"
)

func TestPolicyValidate(t *testing.T) {
	p := Policy{AllowedGroupPrefixes: []string{"eddy:"}, DenyUserPrefixes: []string{"admin:"}}
	strict := Policy{AllowedGroupPrefixes: []string{"eddy:"}, AllowedGroups: []string{"eddy:platform"}}
	tests := []struct {
		name   string
		policy Policy
		id     protocol.Identity
		ok     bool
	}{
		{"valid", p, protocol.Identity{User: "alice@example.com", Groups: []string{"eddy:platform", "eddy:authenticated"}}, true},
		{"no groups", p, protocol.Identity{User: "alice@example.com"}, true},
		{"empty user", p, protocol.Identity{User: "  ", Groups: []string{"eddy:x"}}, false},
		{"system user", p, protocol.Identity{User: "system:admin"}, false},
		{"service account", p, protocol.Identity{User: "system:serviceaccount:flux-system:kustomize-controller"}, false},
		{"denied user prefix", p, protocol.Identity{User: "admin:root"}, false},
		{"system group", p, protocol.Identity{User: "alice", Groups: []string{"system:masters"}}, false},
		{"system authenticated", p, protocol.Identity{User: "alice", Groups: []string{"eddy:a", "system:authenticated"}}, false},
		{"unprefixed group", p, protocol.Identity{User: "alice", Groups: []string{"cluster-admins"}}, false},
		{"exact allowlist hit", strict, protocol.Identity{User: "alice", Groups: []string{"eddy:platform"}}, true},
		{"exact allowlist miss", strict, protocol.Identity{User: "alice", Groups: []string{"eddy:other"}}, false},
		{"empty prefix list denies groups", Policy{AllowedGroupPrefixes: []string{""}}, protocol.Identity{User: "alice", Groups: []string{"anything"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.policy.Validate(tt.id)
			if (err == nil) != tt.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, tt.ok)
			}
			if err != nil && err.Code != 403 {
				t.Fatalf("code %d", err.Code)
			}
		})
	}
}

func TestClientCacheLRUAndTTL(t *testing.T) {
	now := time.Unix(0, 0)
	c := newClientCache(2, time.Minute, func() time.Time { return now })
	c.put("a", Clients{})
	c.put("b", Clients{})
	if _, ok := c.get("a"); !ok {
		t.Fatal("a missing")
	}
	c.put("c", Clients{}) // evicts b, the least recently used
	if _, ok := c.get("b"); ok {
		t.Fatal("b should be evicted")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get("a"); ok {
		t.Fatal("a should be expired")
	}
	if identityKey(protocol.Identity{User: "u", Groups: []string{"b", "a"}}) != identityKey(protocol.Identity{User: "u", Groups: []string{"a", "b"}}) {
		t.Fatal("identity key must ignore group order")
	}
}

func TestImpersonatingFactorySetsHeaders(t *testing.T) {
	var mu sync.Mutex
	var users []string
	var groups [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		users = append(users, r.Header.Get("Impersonate-User"))
		groups = append(groups, r.Header.Values("Impersonate-Group"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"kind":"Namespace","apiVersion":"v1","metadata":{"name":"default"}}`))
	}))
	defer srv.Close()

	f := ImpersonatingFactory(&rest.Config{Host: srv.URL}, 4, time.Minute)
	id := protocol.Identity{User: "alice@example.com", Groups: []string{"eddy:platform"}}
	cl, err := f(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cl.Kube.CoreV1().Namespaces().Get(t.Context(), "default", metav1.GetOptions{}); err != nil {
		t.Fatal(err)
	}
	again, _ := f(id)
	if again.Kube != cl.Kube {
		t.Error("clients should be cached per identity")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(users) != 1 || users[0] != "alice@example.com" || !slices.Equal(groups[0], []string{"eddy:platform"}) {
		t.Fatalf("impersonation headers: users %v groups %v", users, groups)
	}
}
