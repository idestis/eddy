package hub

import (
	"net/http"
	"testing"

	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/store"
)

// Threads on inventory-only objects (kinds outside the kind table) follow
// the row's visibility rule, and only their author may resolve them.
func TestThreadsOnInventoryRows(t *testing.T) {
	e := newEnv(t, "")
	e.connectAgent("dev", testToken, []model.Resource{
		res("Kustomization", "team-a", "apps", model.StatusReady),
		invRow("", "ConfigMap", "team-a", "settings", "team-a"),
		// Same kind name and object in two API groups: ambiguous without group.
		invRow("cert-manager.io", "Certificate", "team-a", "web", "team-a"),
		invRow("example.com", "Certificate", "team-a", "web", "team-a"),
	})
	alice, bob, ops := e.login("alice"), e.login("bob"), e.login("ops")

	var created struct{ Thread store.Thread }
	alice.do("POST", "/api/v1/threads", map[string]any{
		"ref":   map[string]string{"cluster": "dev", "group": "core", "kind": "ConfigMap", "namespace": "team-a", "name": "settings"},
		"title": "Rotate this", "body": "b",
	}, &created, http.StatusCreated)
	if got := created.Thread.Ref; got.Group != "" || got.Kind != "ConfigMap" {
		t.Fatalf("stored ref %+v, want the core group", got)
	}
	// Without group: resolved from the one visible row.
	var second struct{ Thread store.Thread }
	alice.do("POST", "/api/v1/threads", map[string]any{
		"ref":   map[string]string{"cluster": "dev", "kind": "ConfigMap", "namespace": "team-a", "name": "settings"},
		"title": "Second", "body": "b",
	}, &second, http.StatusCreated)

	var page struct{ Items []store.Thread }
	alice.do("GET", "/api/v1/threads?cluster=dev&group=core&kind=ConfigMap&namespace=team-a&name=settings", nil, &page, http.StatusOK)
	if len(page.Items) != 2 {
		t.Fatalf("alice lists %d threads, want 2", len(page.Items))
	}
	alice.do("GET", "/api/v1/threads?cluster=dev&kind=ConfigMap&namespace=team-a&name=settings", nil, &page, http.StatusOK)
	if len(page.Items) != 2 {
		t.Fatalf("alice lists %d threads without group, want 2", len(page.Items))
	}
	alice.do("GET", "/api/v1/threads/"+created.Thread.ID, nil, nil, http.StatusOK)

	// bob cannot see the parent Kustomization: no threads, no detail.
	bob.do("GET", "/api/v1/threads?cluster=dev&group=core&kind=ConfigMap&namespace=team-a&name=settings", nil, &page, http.StatusOK)
	if len(page.Items) != 0 {
		t.Fatalf("bob lists %d threads", len(page.Items))
	}
	if code, _ := bob.errorCode("GET", "/api/v1/threads/"+created.Thread.ID, nil); code != http.StatusNotFound {
		t.Fatalf("bob reads the thread: %d, want 404", code)
	}
	if code, _ := bob.errorCode("POST", "/api/v1/threads", map[string]any{
		"ref":   map[string]string{"cluster": "dev", "group": "core", "kind": "ConfigMap", "namespace": "team-a", "name": "settings"},
		"title": "x", "body": "y",
	}); code != http.StatusNotFound {
		t.Fatalf("bob creates a thread on a row he cannot see: %d, want 404", code)
	}

	// Only the author resolves: patch is never granted outside the table.
	if code, _ := ops.errorCode("POST", "/api/v1/threads/"+created.Thread.ID+"/resolve", nil); code != http.StatusForbidden {
		t.Fatalf("ops resolves alice's inventory thread: %d, want 403", code)
	}
	alice.do("POST", "/api/v1/threads/"+created.Thread.ID+"/resolve", nil, nil, http.StatusOK)

	// Ambiguous kind names need group.
	if code, _ := alice.errorCode("POST", "/api/v1/threads", map[string]any{
		"ref":   map[string]string{"cluster": "dev", "kind": "Certificate", "namespace": "team-a", "name": "web"},
		"title": "x", "body": "y",
	}); code != http.StatusBadRequest {
		t.Fatalf("ambiguous kind without group: %d, want 400", code)
	}
	if code, _ := alice.errorCode("GET", "/api/v1/threads?cluster=dev&kind=Certificate&namespace=team-a&name=web", nil); code != http.StatusBadRequest {
		t.Fatalf("ambiguous list without group: %d, want 400", code)
	}
	alice.do("POST", "/api/v1/threads", map[string]any{
		"ref":   map[string]string{"cluster": "dev", "group": "cert-manager.io", "kind": "Certificate", "namespace": "team-a", "name": "web"},
		"title": "x", "body": "y",
	}, nil, http.StatusCreated)

	// Malformed kinds are still refused.
	if code, _ := alice.errorCode("GET", "/api/v1/threads?cluster=dev&kind=config/map", nil); code != http.StatusBadRequest {
		t.Fatalf("malformed kind: %d, want 400", code)
	}
}
