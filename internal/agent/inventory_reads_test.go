package agent

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

func testDiscovery(lists ...*metav1.APIResourceList) *fakediscovery.FakeDiscovery {
	d := &fakediscovery.FakeDiscovery{Fake: &fake.NewClientset().Fake}
	d.Resources = lists
	return d
}

var baseResources = []*metav1.APIResourceList{
	{GroupVersion: "v1", APIResources: []metav1.APIResource{
		{Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: []string{"get", "list"}},
		{Name: "secrets", Kind: "Secret", Namespaced: true, Verbs: []string{"get", "list"}},
	}},
	{GroupVersion: "rbac.authorization.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "clusterroles", Kind: "ClusterRole", Verbs: []string{"get", "list"}},
	}},
	{GroupVersion: "example.com/v1beta1", APIResources: []metav1.APIResource{
		{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list"}},
	}},
	{GroupVersion: "external-secrets.io/v1", APIResources: []metav1.APIResource{
		{Name: "externalsecrets", Kind: "ExternalSecret", Namespaced: true, Verbs: []string{"get", "list"}},
	}},
}

func TestDiscoveryResolver(t *testing.T) {
	d := testDiscovery(baseResources...)
	now := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	r := newDiscoveryResolver(d, func() time.Time { return now }, time.Minute)

	gvr, namespaced, err := r.Resolve("example.com", "Widget")
	if err != nil || gvr != (schema.GroupVersionResource{Group: "example.com", Version: "v1beta1", Resource: "widgets"}) || !namespaced {
		t.Fatalf("Widget = %v %v %v", gvr, namespaced, err)
	}
	if gvr, namespaced, err := r.Resolve("rbac.authorization.k8s.io", "ClusterRole"); err != nil || gvr.Resource != "clusterroles" || namespaced {
		t.Fatalf("ClusterRole = %v %v %v", gvr, namespaced, err)
	}
	if _, _, err := r.Resolve("other.example.com", "Widget"); !meta.IsNoMatchError(err) {
		t.Fatalf("unknown group: %v", err)
	}

	// A CRD installed later resolves after a refresh; refreshes are spaced.
	d.Resources = append(d.Resources, &metav1.APIResourceList{GroupVersion: "gadgets.example.com/v1", APIResources: []metav1.APIResource{
		{Name: "gadgets", Kind: "Gadget", Namespaced: true, Verbs: []string{"get"}},
	}})
	if _, _, err := r.Resolve("gadgets.example.com", "Gadget"); !meta.IsNoMatchError(err) {
		t.Fatalf("refresh within the interval: %v", err)
	}
	calls := len(d.Actions())
	now = now.Add(2 * time.Minute)
	if gvr, _, err := r.Resolve("gadgets.example.com", "Gadget"); err != nil || gvr.Resource != "gadgets" {
		t.Fatalf("after refresh: %v %v", gvr, err)
	}
	if len(d.Actions()) == calls {
		t.Fatal("no rediscovery")
	}
	// Hits are served from the cache.
	calls = len(d.Actions())
	for range 5 {
		if _, _, err := r.Resolve("example.com", "Widget"); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.Actions()) != calls {
		t.Fatalf("cache hits called discovery %d times", len(d.Actions())-calls)
	}
}

func inventoryOps(t *testing.T, objs ...runtime.Object) *opsFixture {
	t.Helper()
	f := newOps(t, objs...)
	f.h.Kinds = newDiscoveryResolver(testDiscovery(baseResources...), time.Now, time.Minute)
	served := maps.Clone(testServed)
	f.h.Served = served // no preset kinds: ExternalSecret resolves through discovery
	return f
}

func yamlOf(t *testing.T, f *opsFixture, target model.Ref) (string, *protocol.Error) {
	t.Helper()
	res, perr := f.h.Handle(t.Context(), request(protocol.OpYAML, target, nil), nil)
	if perr != nil {
		return "", perr
	}
	var out protocol.YAMLResult
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	return out.YAML, nil
}

func TestInventoryYAMLMatrix(t *testing.T) {
	cm := u("v1", "ConfigMap", "apps", "settings", nil)
	cm.Object["data"] = map[string]any{"password": "hunter2"}
	cm.Object["binaryData"] = map[string]any{"blob": "aGVsbG8="}
	secret := u("v1", "Secret", "apps", "creds", nil)
	secret.Object["data"] = map[string]any{"password": "aHVudGVyMg=="}
	widget := u("example.com/v1beta1", "Widget", "apps", "w", map[string]any{"size": "large", "template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "c", "env": []any{map[string]any{"name": "TOKEN", "value": "hunter2"}}}},
	}}})
	role := u("rbac.authorization.k8s.io/v1", "ClusterRole", "", "admin", nil)
	role.Object["rules"] = []any{map[string]any{"verbs": []any{"*"}}}
	es := u("external-secrets.io/v1", "ExternalSecret", "apps", "db", map[string]any{"refreshInterval": "1h"})

	tests := []struct {
		name   string
		target model.Ref
		code   int
		keep   []string
	}{
		{"configmap is stripped", model.Ref{Kind: "ConfigMap", Namespace: "apps", Name: "settings"}, 0, []string{"name: settings"}},
		{"custom resource", model.Ref{Group: "example.com", Kind: "Widget", Namespace: "apps", Name: "w"}, 0, []string{"size: large", "TOKEN"}},
		{"cluster-scoped, namespace ignored", model.Ref{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Namespace: "apps", Name: "admin"}, 0, []string{"name: admin"}},
		{"preset kind with its preset off", model.Ref{Group: flux.GroupESO, Kind: flux.KindExternalSecret, Namespace: "apps", Name: "db"}, 0, []string{"refreshInterval: 1h"}},
		{"secret", model.Ref{Kind: "Secret", Namespace: "apps", Name: "creds"}, 403, nil},
		{"secret, any spelling", model.Ref{Kind: "SECRET", Namespace: "apps", Name: "creds"}, 403, nil},
		{"unmapped kind", model.Ref{Group: "other.example.com", Kind: "Widget", Namespace: "apps", Name: "w"}, 404, nil},
		{"missing object", model.Ref{Kind: "ConfigMap", Namespace: "apps", Name: "nope"}, 404, nil},
		{"namespaced kind without namespace", model.Ref{Kind: "ConfigMap", Name: "settings"}, 400, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := inventoryOps(t, cm, secret, widget, role, es)
			y, perr := yamlOf(t, f, tt.target)
			if tt.code != 0 {
				if perr == nil || perr.Code != tt.code {
					t.Fatalf("got %v, want %d", perr, tt.code)
				}
				if tt.code == 403 && len(f.calls) != 0 {
					t.Fatal("secret yaml reached the impersonating client")
				}
				return
			}
			if perr != nil {
				t.Fatal(perr)
			}
			for _, leak := range []string{"hunter2", "aGVsbG8", "\ndata:", "binaryData"} {
				if strings.Contains(y, leak) {
					t.Errorf("yaml contains %q:\n%s", leak, y)
				}
			}
			for _, k := range tt.keep {
				if !strings.Contains(y, k) {
					t.Errorf("yaml lacks %q:\n%s", k, y)
				}
			}
			if len(f.calls) != 1 || f.calls[0].User != alice.User {
				t.Fatalf("not read as alice: %v", f.calls)
			}
		})
	}

	// The API server decides: its Forbidden is a 403 with its message.
	f := inventoryOps(t, cm)
	f.dyn.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "settings", errors.New(`User "alice@example.com" cannot get resource "configmaps"`))
	})
	if _, perr := yamlOf(t, f, model.Ref{Kind: "ConfigMap", Namespace: "apps", Name: "settings"}); perr == nil || perr.Code != 403 || !strings.Contains(perr.Message, "alice@example.com") {
		t.Fatalf("forbidden: %v", perr)
	}

	// Defence in depth: whatever the resolver says, a Secret is never rendered.
	f = inventoryOps(t, secret)
	f.h.Kinds = staticResolver{gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, namespaced: true}
	if _, perr := yamlOf(t, f, model.Ref{Group: "example.com", Kind: "Credential", Namespace: "apps", Name: "creds"}); perr == nil || perr.Code != 403 {
		t.Fatalf("secret behind another kind: %v", perr)
	}
}

type staticResolver struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}

func (s staticResolver) Resolve(string, string) (schema.GroupVersionResource, bool, error) {
	return s.gvr, s.namespaced, nil
}

func TestInventoryEvents(t *testing.T) {
	f := inventoryOps(t)
	var ns, selector string
	f.kube.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		ns = a.GetNamespace()
		selector = a.(k8stesting.ListAction).GetListRestrictions().Fields.String()
		return true, &corev1.EventList{Items: []corev1.Event{
			{InvolvedObject: corev1.ObjectReference{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "ClusterRole", Name: "admin"}, Reason: "Mine", Type: "Normal"},
			{InvolvedObject: corev1.ObjectReference{APIVersion: "other.example.com/v1", Kind: "ClusterRole", Name: "admin"}, Reason: "OtherGroup", Type: "Normal"},
			{InvolvedObject: corev1.ObjectReference{Kind: "ClusterRole", Name: "admin"}, Reason: "NoAPIVersion", Type: "Warning"},
		}}, nil
	})
	res, perr := f.h.Handle(t.Context(), request(protocol.OpEvents, model.Ref{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "admin"}, nil), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	var out protocol.EventsResult
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	if ns != metav1.NamespaceDefault || !strings.Contains(selector, "involvedObject.kind=ClusterRole") {
		t.Fatalf("listed events in %q with %q", ns, selector)
	}
	var reasons []string
	for _, e := range out.Events {
		reasons = append(reasons, e.Reason)
	}
	if strings.Join(reasons, ",") != "Mine,NoAPIVersion" {
		t.Fatalf("events %v", reasons)
	}
	if len(f.calls) != 1 {
		t.Fatal("events not impersonated")
	}

	// Secret events are allowed (no data is read).
	f = inventoryOps(t)
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpEvents, model.Ref{Kind: "Secret", Namespace: "apps", Name: "creds"}, nil), nil); perr != nil {
		t.Fatalf("secret events: %v", perr)
	}
	// Only yaml and events accept kinds outside the table.
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpReconcile, model.Ref{Kind: "ConfigMap", Namespace: "apps", Name: "x"}, nil), nil); perr == nil || perr.Code != 400 {
		t.Fatalf("reconcile of a ConfigMap: %v", perr)
	}
}
