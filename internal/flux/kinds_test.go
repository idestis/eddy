package flux

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/eddy-gitops/eddy/internal/model"
)

func TestKindByName(t *testing.T) {
	tests := []struct {
		in, group, plural string
		ok                bool
	}{
		{"Kustomization", GroupKustomize, "kustomizations", true},
		{"helmrelease", GroupHelm, "helmreleases", true},
		{"OCIRepository", GroupSource, "ocirepositories", true},
		{"Pod", GroupCore, "pods", true},
		{"Deployment", GroupApps, "deployments", true},
		{"Secret", "", "", false},
		{"ConfigMap", "", "", false},
		{"", "", "", false},
	}
	for _, tt := range tests {
		k, ok := KindByName(tt.in)
		if ok != tt.ok || k.Group != tt.group || k.Plural != tt.plural {
			t.Errorf("KindByName(%q) = %+v, %v", tt.in, k, ok)
		}
		if YAMLAllowed(tt.in) != tt.ok {
			t.Errorf("YAMLAllowed(%q) = %v", tt.in, !tt.ok)
		}
	}
	if gvr, ok := GVRFor("HelmRelease", "v2"); !ok || gvr != (schema.GroupVersionResource{Group: GroupHelm, Version: "v2", Resource: "helmreleases"}) {
		t.Errorf("GVRFor = %v %v", gvr, ok)
	}
	if _, ok := GVRFor("HelmRelease", ""); ok {
		t.Error("GVRFor with empty version should fail")
	}
	all := All()
	all[0].Versions[0] = "mutated"
	if k, _ := KindByName(KindKustomization); k.Versions[0] != "v1" {
		t.Error("All must return a copy")
	}
}

func TestDiscover(t *testing.T) {
	d := &fakediscovery.FakeDiscovery{Fake: &fake.NewClientset().Fake}
	d.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods"}, {Name: "secrets"}}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments"}, {Name: "replicasets"}}},
		{GroupVersion: "kustomize.toolkit.fluxcd.io/v1beta2", APIResources: []metav1.APIResource{{Name: "kustomizations"}}},
		{GroupVersion: "kustomize.toolkit.fluxcd.io/v1", APIResources: []metav1.APIResource{{Name: "kustomizations"}}},
		{GroupVersion: "helm.toolkit.fluxcd.io/v2beta2", APIResources: []metav1.APIResource{{Name: "helmreleases"}}},
		{GroupVersion: "source.toolkit.fluxcd.io/v1", APIResources: []metav1.APIResource{{Name: "gitrepositories"}}},
	}
	served, err := Discover(d)
	if err != nil {
		t.Fatal(err)
	}
	want := Served{"Pod": "v1", "Deployment": "v1", "ReplicaSet": "v1", "Kustomization": "v1", "HelmRelease": "v2beta2", "GitRepository": "v1"}
	if len(served) != len(want) {
		t.Fatalf("served %v, want %v", served, want)
	}
	for k, v := range want {
		if served[k] != v {
			t.Errorf("%s: got %q, want %q", k, served[k], v)
		}
	}
	if gvr, ok := served.GVR("helmrelease"); !ok || gvr.Version != "v2beta2" {
		t.Errorf("GVR = %v %v", gvr, ok)
	}
	if _, ok := served.GVR("Bucket"); ok {
		t.Error("Bucket is not served")
	}
}

func TestDetectFluxVersion(t *testing.T) {
	ns := func(labels map[string]string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: FluxNamespace, Labels: labels}}
	}
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: FluxNamespace, Name: "source-controller"},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "manager", Image: "ghcr.io/fluxcd/source-controller:v1.4.1@sha256:abc"},
		}}}},
	}
	tests := []struct {
		name string
		kube *fake.Clientset
		want string
	}{
		{"namespace label", fake.NewClientset(ns(map[string]string{"app.kubernetes.io/version": "v2.4.0"})), "v2.4.0"},
		{"image tag fallback", fake.NewClientset(ns(nil), deploy), "v1.4.1"},
		{"nothing", fake.NewClientset(), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectFluxVersion(context.Background(), tt.kube, FluxNamespace); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	if got := imageTag("localhost:5000/img"); got != "" {
		t.Errorf("imageTag with registry port = %q", got)
	}
}

func TestParseInventoryID(t *testing.T) {
	tests := []struct {
		id      string
		want    model.Ref
		wantErr bool
	}{
		{"apps_web_apps_Deployment", model.Ref{Namespace: "apps", Name: "web", Group: "apps", Kind: "Deployment"}, false},
		{"apps_web__Service", model.Ref{Namespace: "apps", Name: "web", Kind: "Service"}, false},
		{"_admin_rbac.authorization.k8s.io_ClusterRole", model.Ref{Name: "admin", Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}, false},
		{"_my_role_rbac.authorization.k8s.io_ClusterRole", model.Ref{Name: "my_role", Group: "rbac.authorization.k8s.io", Kind: "ClusterRole"}, false},
		{"apps_web_apps", model.Ref{}, true},
		{"apps__apps_Deployment", model.Ref{}, true},
		{"", model.Ref{}, true},
	}
	for _, tt := range tests {
		got, err := ParseInventoryID(tt.id)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseInventoryID(%q) = %+v, %v", tt.id, got, err)
		}
	}
}

func TestSanitizeYAML(t *testing.T) {
	u := newObj("apps/v1", "Deployment", "apps", "web", obj{
		"template": obj{"spec": obj{
			"containers": []any{obj{"name": "app", "image": "app:1", "env": []any{
				obj{"name": "PASSWORD", "value": "hunter2"},
				obj{"name": "TOKEN", "valueFrom": obj{"secretKeyRef": obj{"name": "s", "key": "k"}}},
			}}},
			"initContainers": []any{obj{"name": "init", "env": []any{obj{"name": "X", "value": "init-secret"}}}},
		}},
	}, nil)
	meta := u.Object["metadata"].(obj)
	meta["managedFields"] = []any{obj{"manager": "kubectl"}}
	meta["annotations"] = obj{lastAppliedAnnotation: `{"secret":"leak"}`, "keep": "me"}
	u.Object["data"] = obj{"k": "c2VjcmV0"}
	u.Object["stringData"] = obj{"k": "plain"}

	out, err := SanitizeYAML(u)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, leak := range []string{"hunter2", "init-secret", "managedFields", "last-applied", "leak", "c2VjcmV0", "plain", "stringData"} {
		if strings.Contains(s, leak) {
			t.Errorf("sanitized yaml contains %q:\n%s", leak, s)
		}
	}
	for _, keep := range []string{"PASSWORD", "value: '[REDACTED]'", "secretKeyRef", "keep: me", "image: app:1"} {
		if !strings.Contains(s, keep) {
			t.Errorf("sanitized yaml lacks %q:\n%s", keep, s)
		}
	}
	if _, ok := u.Object["data"]; !ok {
		t.Error("SanitizeYAML must not modify its input")
	}

	for _, gvk := range []schema.GroupVersionKind{{Version: "v1", Kind: "Secret"}, {Version: "v1", Kind: "ConfigMap"}, {Group: "example.com", Version: "v1", Kind: "Kustomization"}} {
		x := &unstructured.Unstructured{Object: obj{}}
		x.SetGroupVersionKind(gvk)
		if _, err := SanitizeYAML(x); !errors.Is(err, ErrKindNotAllowed) {
			t.Errorf("%s: err = %v", gvk, err)
		}
	}
}

func TestTrimKeepsSummary(t *testing.T) {
	pod := newObj("v1", "Pod", "apps", "p", obj{"containers": []any{obj{"name": "a", "image": "a:1", "env": []any{obj{"name": "X", "value": "y"}}}}, "volumes": []any{obj{"name": "v"}}},
		obj{"phase": "Running", "containerStatuses": []any{obj{"name": "a", "ready": true}}})
	pod.Object["metadata"].(obj)["annotations"] = obj{"big": "x"}
	hr := newObj("helm.toolkit.fluxcd.io/v2", KindHelmRelease, "apps", "h", obj{"values": obj{"password": "p"}, "chart": obj{"spec": obj{"chart": "c", "sourceRef": obj{"kind": "HelmRepository", "name": "r"}}}}, nil)
	for _, u := range []*unstructured.Unstructured{pod, hr} {
		k := mustKind(t, u.GetKind())
		before := Summarize(k, u, nil)
		Trim(k, u)
		after := Summarize(k, u, nil)
		if before.Status != after.Status || before.Message != after.Message || strings.Join(before.Images, ",") != strings.Join(after.Images, ",") || before.Chart != after.Chart || (before.Source == nil) != (after.Source == nil) {
			t.Errorf("%s: summary changed by Trim: %+v vs %+v", k.Kind, before, after)
		}
		if u.GetAnnotations() != nil {
			t.Errorf("%s: annotations kept", k.Kind)
		}
	}
	if _, ok := pod.Object["spec"].(obj)["volumes"]; ok {
		t.Error("pod volumes kept")
	}
	if _, ok := hr.Object["spec"].(obj)["values"]; ok {
		t.Error("helm values kept")
	}
}
