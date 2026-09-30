package agent

import (
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"strings"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/idestis/eddy/internal/flux"
	"github.com/idestis/eddy/internal/model"
	"github.com/idestis/eddy/internal/protocol"
)

var testServed = flux.Served{
	"Kustomization": "v1", "HelmRelease": "v2", "GitRepository": "v1", "OCIRepository": "v1",
	"HelmRepository": "v1", "HelmChart": "v1", "Bucket": "v1",
	"Deployment": "v1", "StatefulSet": "v1", "DaemonSet": "v1", "ReplicaSet": "v1", "Pod": "v1",
}

func listKinds() map[schema.GroupVersionResource]string {
	m := map[schema.GroupVersionResource]string{}
	for _, k := range flux.All() {
		m[k.GVR(testServed[k.Kind])] = k.Kind + "List"
	}
	return m
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func u(apiVersion, kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	o := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"namespace": ns, "name": name},
	}}
	if spec != nil {
		o.Object["spec"] = spec
	}
	return o
}

type opsFixture struct {
	h     *Handler
	dyn   *dynamicfake.FakeDynamicClient
	kube  *fake.Clientset
	self  *fake.Clientset
	calls []protocol.Identity
}

var fixedNow = time.Date(2026, 9, 30, 12, 0, 0, 123456789, time.UTC)

func newOps(t *testing.T, objs ...runtime.Object) *opsFixture {
	t.Helper()
	f := &opsFixture{
		dyn:  dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds(), objs...),
		kube: fake.NewClientset(),
		self: fake.NewClientset(),
	}
	f.h = &Handler{
		Policy: Policy{AllowedGroupPrefixes: []string{"eddy:"}},
		Served: maps.Clone(testServed),
		Impersonate: func(id protocol.Identity) (Clients, error) {
			f.calls = append(f.calls, id)
			return Clients{Dynamic: f.dyn, Kube: f.kube}, nil
		},
		Self:   f.self,
		Now:    func() time.Time { return fixedNow },
		Logger: discardLogger(),
	}
	return f
}

var alice = protocol.Identity{User: "alice@example.com", Groups: []string{"eddy:platform"}}

func request(op protocol.Op, target model.Ref, args any) protocol.Request {
	req := protocol.Request{Op: op, Identity: alice, Target: target}
	if args != nil {
		req.Args, _ = json.Marshal(args)
	}
	return req
}

// patches returns "<resource>/<ns>/<name>" and the body of every patch.
func patches(f *opsFixture) (targets []string, bodies []map[string]any) {
	for _, a := range f.dyn.Actions() {
		p, ok := a.(k8stesting.PatchAction)
		if !ok {
			continue
		}
		targets = append(targets, p.GetResource().Resource+"/"+p.GetNamespace()+"/"+p.GetName())
		var body map[string]any
		_ = json.Unmarshal(p.GetPatch(), &body)
		bodies = append(bodies, body)
	}
	return targets, bodies
}

var (
	ksRef = model.Ref{Group: flux.GroupKustomize, Kind: flux.KindKustomization, Namespace: "flux-system", Name: "apps"}
	hrRef = model.Ref{Group: flux.GroupHelm, Kind: flux.KindHelmRelease, Namespace: "apps", Name: "podinfo"}
)

func fixtures() []runtime.Object {
	return []runtime.Object{
		u("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "apps", map[string]any{
			"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"},
		}),
		u("source.toolkit.fluxcd.io/v1", "GitRepository", "flux-system", "repo", nil),
		u("helm.toolkit.fluxcd.io/v2", "HelmRelease", "apps", "podinfo", map[string]any{
			"chart": map[string]any{"spec": map[string]any{"chart": "podinfo", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "podinfo", "namespace": "flux-system"}}},
		}),
		u("source.toolkit.fluxcd.io/v1", "HelmChart", "flux-system", "apps-podinfo", nil),
		u("helm.toolkit.fluxcd.io/v2", "HelmRelease", "apps", "oci", map[string]any{
			"chartRef": map[string]any{"kind": "OCIRepository", "name": "oci"},
		}),
		u("source.toolkit.fluxcd.io/v1", "OCIRepository", "apps", "oci", nil),
	}
}

func TestReconcilePatches(t *testing.T) {
	stamp := fixedNow.Format(time.RFC3339Nano)
	tests := []struct {
		name       string
		target     model.Ref
		withSource bool
		want       []string
	}{
		{"kustomization", ksRef, false, []string{"kustomizations/flux-system/apps"}},
		{"kustomization with source", ksRef, true, []string{"gitrepositories/flux-system/repo", "kustomizations/flux-system/apps"}},
		{"helmrelease via helmchart", hrRef, true, []string{"helmcharts/flux-system/apps-podinfo", "helmreleases/apps/podinfo"}},
		{"helmrelease chartRef", model.Ref{Group: flux.GroupHelm, Kind: flux.KindHelmRelease, Namespace: "apps", Name: "oci"}, true, []string{"ocirepositories/apps/oci", "helmreleases/apps/oci"}},
		{"source has no source", model.Ref{Group: flux.GroupSource, Kind: flux.KindGitRepository, Namespace: "flux-system", Name: "repo"}, true, []string{"gitrepositories/flux-system/repo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOps(t, fixtures()...)
			_, perr := f.h.Handle(t.Context(), request(protocol.OpReconcile, tt.target, protocol.ReconcileArgs{WithSource: tt.withSource}), nil)
			if perr != nil {
				t.Fatal(perr)
			}
			targets, bodies := patches(f)
			if strings.Join(targets, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("patched %v, want %v", targets, tt.want)
			}
			for _, b := range bodies {
				ann := b["metadata"].(map[string]any)["annotations"].(map[string]any)
				if ann[RequestedAtAnnotation] != stamp || len(b) != 1 {
					t.Fatalf("patch body %v", b)
				}
			}
			if len(f.calls) != 1 || f.calls[0].User != alice.User {
				t.Fatalf("impersonated %v", f.calls)
			}
		})
	}
}

func TestSuspendResume(t *testing.T) {
	f := newOps(t, fixtures()...)
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpSuspend, ksRef, nil), nil); perr != nil {
		t.Fatal(perr)
	}
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpResume, ksRef, nil), nil); perr != nil {
		t.Fatal(perr)
	}
	_, bodies := patches(f)
	if len(bodies) != 2 {
		t.Fatalf("bodies %v", bodies)
	}
	if bodies[0]["spec"].(map[string]any)["suspend"] != true || bodies[0]["metadata"] != nil {
		t.Errorf("suspend patch %v", bodies[0])
	}
	if bodies[1]["spec"].(map[string]any)["suspend"] != false || bodies[1]["metadata"] == nil {
		t.Errorf("resume patch %v", bodies[1])
	}
	got, _ := f.dyn.Resource(flux.All()[0].GVR("v1")).Namespace("flux-system").Get(t.Context(), "apps", metav1.GetOptions{})
	if suspend, _, _ := unstructured.NestedBool(got.Object, "spec", "suspend"); suspend {
		t.Error("object still suspended")
	}
}

func TestHandleRejections(t *testing.T) {
	deploy := model.Ref{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web"}
	tests := []struct {
		name string
		req  protocol.Request
		code int
	}{
		{"system user", protocol.Request{Op: protocol.OpYAML, Identity: protocol.Identity{User: "system:admin"}, Target: ksRef}, 403},
		{"bad group", protocol.Request{Op: protocol.OpYAML, Identity: protocol.Identity{User: "bob", Groups: []string{"admins"}}, Target: ksRef}, 403},
		{"secret", request(protocol.OpYAML, model.Ref{Kind: "Secret", Namespace: "a", Name: "b"}, nil), 400},
		{"wrong group", request(protocol.OpYAML, model.Ref{Group: "example.com", Kind: "Kustomization", Namespace: "a", Name: "b"}, nil), 400},
		{"missing namespace", request(protocol.OpYAML, model.Ref{Group: flux.GroupKustomize, Kind: "Kustomization", Name: "b"}, nil), 400},
		{"reconcile workload", request(protocol.OpReconcile, deploy, nil), 400},
		{"suspend workload", request(protocol.OpSuspend, deploy, nil), 400},
		{"logs of deployment", request(protocol.OpLogs, deploy, nil), 400},
		{"not found", request(protocol.OpYAML, model.Ref{Group: flux.GroupKustomize, Kind: "Kustomization", Namespace: "x", Name: "missing"}, nil), 404},
		{"unknown op", request("delete", ksRef, nil), 400},
		{"bad args", protocol.Request{Op: protocol.OpReconcile, Identity: alice, Target: ksRef, Args: json.RawMessage(`[1]`)}, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newOps(t, fixtures()...)
			_, perr := f.h.Handle(t.Context(), tt.req, nil)
			if perr == nil || perr.Code != tt.code {
				t.Fatalf("got %v, want code %d", perr, tt.code)
			}
			if tt.code == 403 && len(f.calls) != 0 {
				t.Fatal("rejected identity must not reach the client factory")
			}
		})
	}

	f := newOps(t)
	delete(f.h.Served, "Bucket")
	_, perr := f.h.Handle(t.Context(), request(protocol.OpYAML, model.Ref{Group: flux.GroupSource, Kind: "Bucket", Namespace: "a", Name: "b"}, nil), nil)
	if perr == nil || perr.Code != 404 {
		t.Fatalf("unserved kind: %v", perr)
	}
}

func TestForbiddenIsMapped(t *testing.T) {
	f := newOps(t, fixtures()...)
	f.dyn.PrependReactor("patch", "*", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: flux.GroupKustomize, Resource: "kustomizations"}, "apps", nil)
	})
	_, perr := f.h.Handle(t.Context(), request(protocol.OpReconcile, ksRef, nil), nil)
	if perr == nil || perr.Code != 403 || !strings.Contains(perr.Message, "forbidden") {
		t.Fatalf("got %v", perr)
	}
	if got := toProtocolError(apierrors.NewConflict(schema.GroupResource{}, "x", nil)); got.Code != 409 {
		t.Fatalf("conflict code %d", got.Code)
	}
}

func TestYAMLIsSanitized(t *testing.T) {
	d := u("apps/v1", "Deployment", "apps", "web", map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "app", "image": "app:1", "env": []any{map[string]any{"name": "DB_PASSWORD", "value": "hunter2"}}}},
	}}})
	d.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl"}})
	f := newOps(t, d)
	res, perr := f.h.Handle(t.Context(), request(protocol.OpYAML, model.Ref{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web"}, nil), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	var out protocol.YAMLResult
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.YAML, "hunter2") || strings.Contains(out.YAML, "managedFields") || !strings.Contains(out.YAML, "DB_PASSWORD") {
		t.Fatalf("yaml:\n%s", out.YAML)
	}
}

func TestEvents(t *testing.T) {
	f := newOps(t)
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	var items []corev1.Event
	for i := range 120 {
		items = append(items, corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Namespace: "flux-system", Name: "e"},
			Type:           "Normal",
			Reason:         "ReconciliationSucceeded",
			Message:        "line one\nline two",
			LastTimestamp:  metav1.NewTime(base.Add(time.Duration(i) * time.Minute)),
			FirstTimestamp: metav1.NewTime(base),
			Source:         corev1.EventSource{Component: "kustomize-controller"},
		})
	}
	items = append(items, corev1.Event{
		ObjectMeta:          metav1.ObjectMeta{Namespace: "flux-system", Name: "series"},
		Type:                "Warning",
		Reason:              "Failed",
		EventTime:           metav1.NewMicroTime(base),
		Series:              &corev1.EventSeries{Count: 7, LastObservedTime: metav1.NewMicroTime(base.Add(24 * time.Hour))},
		ReportingController: "helm-controller",
	})
	var selector string
	f.kube.PrependReactor("list", "events", func(a k8stesting.Action) (bool, runtime.Object, error) {
		selector = a.(k8stesting.ListAction).GetListRestrictions().Fields.String()
		return true, &corev1.EventList{Items: items}, nil
	})
	res, perr := f.h.Handle(t.Context(), request(protocol.OpEvents, ksRef, nil), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	var out protocol.EventsResult
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"involvedObject.name=apps", "involvedObject.namespace=flux-system", "involvedObject.kind=Kustomization"} {
		if !strings.Contains(selector, want) {
			t.Errorf("selector %q lacks %q", selector, want)
		}
	}
	if len(out.Events) != maxEvents {
		t.Fatalf("got %d events", len(out.Events))
	}
	first := out.Events[0]
	if first.Reason != "Failed" || first.Count != 7 || first.Source != "helm-controller" || !first.Last.Equal(base.Add(24*time.Hour)) {
		t.Fatalf("newest event %+v", first)
	}
	if out.Events[1].Message != "line one line two" || out.Events[1].Count != 1 || out.Events[1].Source != "kustomize-controller" {
		t.Fatalf("event %+v", out.Events[1])
	}
}

func TestAccessUsesOwnClient(t *testing.T) {
	f := newOps(t)
	f.self.PrependReactor("create", "subjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		sar := a.(k8stesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		if sar.Spec.User != alice.User || len(sar.Spec.Groups) != 1 {
			t.Errorf("sar spec %+v", sar.Spec)
		}
		sar.Status.Allowed = sar.Spec.ResourceAttributes.Verb == "get"
		return true, sar, nil
	})
	args := protocol.AccessArgs{Checks: []protocol.AccessCheck{
		{Verb: "get", Group: flux.GroupKustomize, Resource: "kustomizations", Namespace: "flux-system"},
		{Verb: "patch", Group: flux.GroupKustomize, Resource: "kustomizations", Namespace: "flux-system"},
		{Verb: "get", Resource: "pods", Subresource: "log", Namespace: "apps"},
	}}
	res, perr := f.h.Handle(t.Context(), request(protocol.OpAccess, model.Ref{}, args), nil)
	if perr != nil {
		t.Fatal(perr)
	}
	var out protocol.AccessResult
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Allowed) != 3 || !out.Allowed[0] || out.Allowed[1] || !out.Allowed[2] {
		t.Fatalf("allowed %v", out.Allowed)
	}
	if len(f.calls) != 0 {
		t.Fatal("access must not use impersonated clients")
	}

	tooMany := protocol.AccessArgs{Checks: make([]protocol.AccessCheck, maxAccessChecks+1)}
	if _, perr := f.h.Handle(t.Context(), request(protocol.OpAccess, model.Ref{}, tooMany), nil); perr == nil || perr.Code != 400 {
		t.Fatalf("too many checks: %v", perr)
	}
}

func TestLogsStream(t *testing.T) {
	f := newOps(t)
	var chunks []protocol.LogChunk
	pod := model.Ref{Kind: "Pod", Namespace: "apps", Name: "web-1"}
	_, perr := f.h.Handle(t.Context(), request(protocol.OpLogs, pod, protocol.LogsArgs{Container: "app", TailLines: 99999}), func(c protocol.LogChunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if perr != nil {
		t.Fatal(perr)
	}
	if len(chunks) != 1 || chunks[0].Lines[0] != "fake logs" {
		t.Fatalf("chunks %v", chunks)
	}
	for _, a := range f.kube.Actions() {
		if g, ok := a.(k8stesting.GenericAction); ok && a.GetSubresource() == "log" {
			opts := g.GetValue().(*corev1.PodLogOptions)
			if *opts.TailLines != maxTailLines || opts.Container != "app" {
				t.Fatalf("log options %+v", opts)
			}
			return
		}
	}
	t.Fatal("no log request recorded")
}

func TestPumpLines(t *testing.T) {
	var b strings.Builder
	for i := range 250 {
		b.WriteString("line ")
		b.WriteString(strings.Repeat("x", i))
		b.WriteByte('\n')
	}
	b.WriteString(strings.Repeat("y", maxLineBytes*3) + "\n")
	b.WriteString("last line without newline")
	var chunks []protocol.LogChunk
	err := pumpLines(t.Context(), strings.NewReader(b.String()), func(c protocol.LogChunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range chunks {
		if len(c.Lines) > logChunkLines {
			t.Fatalf("chunk of %d lines", len(c.Lines))
		}
		lines = append(lines, c.Lines...)
	}
	if len(lines) != 252 || lines[0] != "line " || len(lines[250]) != maxLineBytes || lines[251] != "last line without newline" {
		t.Fatalf("got %d lines, long line %d bytes, last %q", len(lines), len(lines[250]), lines[len(lines)-1])
	}
}
