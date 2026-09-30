//go:build dev

package agent

import (
	"encoding/json"
	"strings"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

// newLocalOps is newOps switched to local mode: f.kube and f.dyn stand for
// the kubeconfig identity's own clients.
func newLocalOps(t *testing.T, readOnly string) (*opsFixture, protocol.Hello) {
	t.Helper()
	f := newOps(t, fixtures()...)
	var hello protocol.Hello
	configureLocal(f.h, &hello, f.kube, f.dyn, LocalOptions{Context: "arn:aws:eks:eu-west-2:123:cluster/dev-eu", ReadOnly: readOnly})
	return f, hello
}

func TestLocalHello(t *testing.T) {
	_, hello := newLocalOps(t, "read-only local mode (start with --allow-writes)")
	if hello.Mode != protocol.ModeLocal || !hello.ReadOnly || hello.Context != "arn:aws:eks:eu-west-2:123:cluster/dev-eu" {
		t.Fatalf("hello %+v", hello)
	}
	_, hello = newLocalOps(t, "")
	if hello.Mode != protocol.ModeLocal || hello.ReadOnly {
		t.Fatalf("writable hello %+v", hello)
	}
	b, _ := json.Marshal(protocol.Hello{})
	if strings.Contains(string(b), "mode") || strings.Contains(string(b), "readOnly") || strings.Contains(string(b), "context") {
		t.Fatalf("local fields must be omitted for normal agents: %s", b)
	}
}

func TestLocalAccessUsesSelfSubjectAccessReview(t *testing.T) {
	f, _ := newLocalOps(t, "")
	var ssars int
	f.kube.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		ssar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		ssars++
		ssar.Status.Allowed = ssar.Spec.ResourceAttributes.Verb == "list"
		return true, ssar, nil
	})
	f.self.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		t.Error("local mode must not create SubjectAccessReviews")
		return true, &authorizationv1.SubjectAccessReview{}, nil
	})
	args := protocol.AccessArgs{Checks: []protocol.AccessCheck{
		{Verb: "list", Group: flux.GroupKustomize, Resource: "kustomizations", Namespace: "flux-system"},
		{Verb: "patch", Group: flux.GroupKustomize, Resource: "kustomizations", Namespace: "flux-system", Name: "apps"},
	}}
	// The hub-supplied identity does not change the answer.
	for _, id := range []protocol.Identity{alice, {User: "bob", Groups: []string{"eddy:team-b"}}} {
		req := request(protocol.OpAccess, model.Ref{}, args)
		req.Identity = id
		res, perr := f.h.Handle(t.Context(), req, nil)
		if perr != nil {
			t.Fatal(perr)
		}
		var out protocol.AccessResult
		if err := json.Unmarshal(res, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Allowed) != 2 || !out.Allowed[0] || out.Allowed[1] {
			t.Fatalf("%s: allowed %v", id.User, out.Allowed)
		}
	}
	if ssars != 4 {
		t.Fatalf("want 4 self subject access reviews, got %d", ssars)
	}
}

func TestLocalReadsIgnoreIdentityButRefuseSystem(t *testing.T) {
	f, _ := newLocalOps(t, "read-only local mode (start with --allow-writes)")
	req := request(protocol.OpYAML, ksRef, nil)
	req.Identity = protocol.Identity{User: "bob", Groups: []string{"eddy:team-b"}}
	if _, perr := f.h.Handle(t.Context(), req, nil); perr != nil {
		t.Fatalf("yaml as bob: %v", perr)
	}
	if len(f.calls) != 0 {
		t.Fatal("local mode must not build impersonating clients")
	}
	for _, id := range []protocol.Identity{
		{User: "system:admin"},
		{User: "alice", Groups: []string{"system:masters"}},
	} {
		req.Identity = id
		if _, perr := f.h.Handle(t.Context(), req, nil); perr == nil || perr.Code != 403 {
			t.Fatalf("%+v: want 403, got %v", id, perr)
		}
	}
}

func TestLocalWrites(t *testing.T) {
	f, _ := newLocalOps(t, "read-only local mode: context \"prod\" matches --protect (start with --allow-writes-protected to override)")
	_, perr := f.h.Handle(t.Context(), request(protocol.OpSuspend, ksRef, nil), nil)
	if perr == nil || perr.Code != 403 || !strings.Contains(perr.Message, "--allow-writes-protected") {
		t.Fatalf("protected: %v", perr)
	}
	if targets, _ := patches(f); len(targets) != 0 {
		t.Fatalf("patched %v", targets)
	}

	f, _ = newLocalOps(t, "")
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpReconcile, ksRef, nil), nil); perr != nil {
		t.Fatalf("writable reconcile: %v", perr)
	}
	if targets, _ := patches(f); len(targets) != 1 || targets[0] != "kustomizations/flux-system/apps" {
		t.Fatalf("patches %v", targets)
	}
}
