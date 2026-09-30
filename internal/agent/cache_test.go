package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
)

func owned(obj *unstructured.Unstructured, apiVersion, kind, name string) *unstructured.Unstructured {
	yes := true
	obj.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID("uid-" + name), Controller: &yes}})
	return obj
}

func podObj(name, rs string) *unstructured.Unstructured {
	p := u("v1", "Pod", "apps", name, map[string]any{"containers": []any{map[string]any{"name": "app", "image": "app:1", "env": []any{map[string]any{"name": "X", "value": "secret"}}}}})
	p.Object["status"] = map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"name": "app", "ready": true}}}
	p.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"})
	p.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "x"}})
	if rs != "" {
		owned(p, "apps/v1", "ReplicaSet", rs)
	}
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startCache(t *testing.T, served flux.Served, namespaces []string, objs ...runtime.Object) (*Cache, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...)
	c, err := NewCache(dyn, served, namespaces, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c.Start(ctx)
	if !c.WaitForSync(ctx) {
		t.Fatal("cache did not sync")
	}
	return c, dyn
}

func byID(rs []model.Resource) map[string]model.Resource {
	m := map[string]model.Resource{}
	for _, r := range rs {
		m[r.ID] = r
	}
	return m
}

func TestCacheSnapshotOwnershipAndDeltas(t *testing.T) {
	deploy := u("apps/v1", "Deployment", "apps", "web", map[string]any{"replicas": int64(1)})
	deploy.SetLabels(map[string]string{flux.LabelKustomizeName: "apps", flux.LabelKustomizeNamespace: "flux-system"})
	rs := owned(u("apps/v1", "ReplicaSet", "apps", "web-abc", nil), "apps/v1", "Deployment", "web")
	ks := u("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "apps", map[string]any{"interval": "5m"})
	c, dyn := startCache(t, testServed, nil, deploy, rs, podObj("web-abc-1", "web-abc"), ks)

	var snap map[string]model.Resource
	waitFor(t, "pod owned by deployment", func() bool {
		snap = byID(c.Snapshot())
		p, ok := snap["/Pod/apps/web-abc-1"]
		return ok && p.Owner != nil && p.Owner.Kind == "Deployment"
	})
	if len(snap) != 3 {
		t.Fatalf("snapshot has %d resources, want 3 (no ReplicaSets): %v", len(snap), snap)
	}
	if d := snap["apps/Deployment/apps/web"]; d.Owner == nil || d.Owner.Kind != flux.KindKustomization {
		t.Fatalf("deployment owner %+v", d.Owner)
	}
	if ups, dels := c.Drain(); len(ups)+len(dels) != 0 {
		t.Fatalf("snapshot must clear pending, got %v %v", ups, dels)
	}

	// Informer objects are trimmed.
	for _, inf := range c.pods {
		for _, o := range inf.GetStore().List() {
			p := o.(*unstructured.Unstructured)
			if p.GetAnnotations() != nil || p.GetManagedFields() != nil {
				t.Fatalf("pod not trimmed: %v", p.Object)
			}
			if _, ok := p.Object["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)["env"]; ok {
				t.Fatal("pod env kept in cache")
			}
		}
	}

	ctx := t.Context()
	podGVR := flux.All()[len(flux.All())-1].GVR("v1")
	if err := dyn.Resource(podGVR).Namespace("apps").Delete(ctx, "web-abc-1", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	ksGVR, _ := testServed.GVR("Kustomization")
	ks2 := ks.DeepCopy()
	ks2.Object["spec"] = map[string]any{"interval": "1m"}
	if _, err := dyn.Resource(ksGVR).Namespace("flux-system").Update(ctx, ks2, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	var ups []model.Resource
	var dels []string
	waitFor(t, "delta", func() bool {
		u2, d2 := c.Drain()
		ups, dels = append(ups, u2...), append(dels, d2...)
		return len(ups) >= 1 && len(dels) >= 1
	})
	if dels[0] != "/Pod/apps/web-abc-1" || ups[0].ID != ksRef.ID() || ups[0].Interval != "1m" {
		t.Fatalf("delta upserts %v deletes %v", ups, dels)
	}
}

func TestCacheReplicaSetArrivingLate(t *testing.T) {
	c, dyn := startCache(t, testServed, []string{"apps"}, podObj("web-abc-1", "web-abc"))
	if p := byID(c.Snapshot())["/Pod/apps/web-abc-1"]; p.Owner == nil || p.Owner.Kind != "ReplicaSet" {
		t.Fatalf("owner before replicaset: %+v", p.Owner)
	}
	rsGVR, _ := testServed.GVR("ReplicaSet")
	rs := owned(u("apps/v1", "ReplicaSet", "apps", "web-abc", nil), "apps/v1", "Deployment", "web")
	if _, err := dyn.Resource(rsGVR).Namespace("apps").Create(t.Context(), rs, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "pod re-attributed", func() bool {
		ups, _ := c.Drain()
		return len(ups) == 1 && ups[0].Owner != nil && ups[0].Owner.Kind == "Deployment"
	})
}

func TestCacheSkipsUnservedKinds(t *testing.T) {
	c, _ := startCache(t, flux.Served{"Pod": "v1"}, nil, podObj("p", ""))
	if len(c.synced) != 1 {
		t.Fatalf("%d informers, want 1", len(c.synced))
	}
	if !c.Synced() || len(c.Snapshot()) != 1 {
		t.Fatal("expected one pod")
	}
}

func TestHealthHandler(t *testing.T) {
	synced, connected := false, true
	h := healthHandler(func() bool { return synced }, func() bool { return connected })
	get := func(path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, _ := get("/healthz"); code != 200 {
		t.Fatalf("healthz %d", code)
	}
	if code, body := get("/readyz"); code != 503 || !strings.Contains(body, `"connected":true`) {
		t.Fatalf("readyz unsynced %d %s", code, body)
	}
	synced = true
	if code, _ := get("/readyz"); code != 200 {
		t.Fatalf("readyz synced %d", code)
	}
}
