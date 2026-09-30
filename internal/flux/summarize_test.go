package flux

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/idestis/eddy/internal/model"
)

type obj = map[string]any

func newObj(apiVersion, kind, ns, name string, spec, status obj) *unstructured.Unstructured {
	o := obj{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": obj{
			"namespace":         ns,
			"name":              name,
			"resourceVersion":   "42",
			"generation":        int64(3),
			"creationTimestamp": "2026-01-02T03:04:05Z",
		},
	}
	if spec != nil {
		o["spec"] = spec
	}
	if status != nil {
		o["status"] = status
	}
	return &unstructured.Unstructured{Object: o}
}

func cond(typ, status, reason, msg string) obj {
	return obj{"type": typ, "status": status, "reason": reason, "message": msg, "lastTransitionTime": "2026-02-01T00:00:00Z"}
}

func conds(c ...obj) []any {
	out := make([]any, len(c))
	for i := range c {
		out[i] = c[i]
	}
	return out
}

func mustKind(t *testing.T, name string) Kind {
	t.Helper()
	k, ok := KindByName(name)
	if !ok {
		t.Fatalf("unknown kind %s", name)
	}
	return k
}

func TestSummarizeFluxStatus(t *testing.T) {
	ks := "kustomize.toolkit.fluxcd.io/v1"
	tests := []struct {
		name    string
		spec    obj
		status  obj
		want    model.Status
		wantMsg string
	}{
		{"ready", nil, obj{"observedGeneration": int64(3), "conditions": conds(cond("Ready", "True", "ReconciliationSucceeded", "Applied revision: main@sha1:abc"))}, model.StatusReady, "Applied revision: main@sha1:abc"},
		{"failed", nil, obj{"conditions": conds(cond("Ready", "False", "BuildFailed", "kustomize build failed:\n  line 2"))}, model.StatusFailed, "kustomize build failed: line 2"},
		{"suspend wins over failed", obj{"suspend": true}, obj{"conditions": conds(cond("Ready", "False", "BuildFailed", "x"))}, model.StatusSuspended, "Reconciliation suspended"},
		{"reconciling", nil, obj{"conditions": conds(cond("Ready", "Unknown", "Progressing", "p"), cond("Reconciling", "True", "Progressing", "Running health checks"))}, model.StatusReconciling, "Running health checks"},
		{"retrying is failed", nil, obj{"conditions": conds(cond("Ready", "False", "HealthCheckFailed", "timeout"), cond("Reconciling", "True", "ProgressingWithRetry", "retry"))}, model.StatusFailed, "timeout"},
		{"stalled", nil, obj{"conditions": conds(cond("Stalled", "True", "InvalidPath", "path not found"), cond("Ready", "False", "InvalidPath", "path not found"))}, model.StatusFailed, "path not found"},
		{"ready unknown", nil, obj{"conditions": conds(cond("Ready", "Unknown", "Progressing", "reconciliation in progress"))}, model.StatusReconciling, "reconciliation in progress"},
		{"stale generation", nil, obj{"observedGeneration": int64(2), "conditions": conds(cond("Ready", "True", "Succeeded", "ok"))}, model.StatusReconciling, "Waiting for the controller to observe the latest spec"},
		{"no status", nil, nil, model.StatusUnknown, "Waiting for the controller to report status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newObj(ks, KindKustomization, "flux-system", "apps", tt.spec, tt.status)
			r := Summarize(mustKind(t, KindKustomization), u, nil)
			if r.Status != tt.want || r.Message != tt.wantMsg {
				t.Fatalf("got %s %q, want %s %q", r.Status, r.Message, tt.want, tt.wantMsg)
			}
			if r.Suspended != (tt.spec != nil && tt.spec["suspend"] == true) {
				t.Fatalf("suspended = %v", r.Suspended)
			}
		})
	}
}

func TestSummarizeFluxDetails(t *testing.T) {
	tests := []struct {
		name  string
		u     *unstructured.Unstructured
		check func(t *testing.T, r model.Resource)
	}{
		{
			name: "kustomization",
			u: newObj("kustomize.toolkit.fluxcd.io/v1", KindKustomization, "flux-system", "apps",
				obj{"interval": "10m", "sourceRef": obj{"kind": "GitRepository", "name": "repo"}},
				obj{"lastAppliedRevision": "main@sha1:abc", "inventory": obj{"entries": []any{obj{"id": "a_b__Service"}, obj{"id": "a_c_apps_Deployment"}}}}),
			check: func(t *testing.T, r model.Resource) {
				wantSrc := model.Ref{Group: GroupSource, Kind: "GitRepository", Namespace: "flux-system", Name: "repo"}
				if r.Revision != "main@sha1:abc" || r.Interval != "10m" || r.Inventory != 2 || r.Source == nil || *r.Source != wantSrc {
					t.Fatalf("unexpected %+v source %+v", r, r.Source)
				}
				if r.ID != "kustomize.toolkit.fluxcd.io/Kustomization/flux-system/apps" || r.Version != "v1" || r.ResourceVersion != "42" {
					t.Fatalf("identity fields: %+v", r)
				}
			},
		},
		{
			name: "helmrelease chart template with history",
			u: newObj("helm.toolkit.fluxcd.io/v2", KindHelmRelease, "apps", "podinfo",
				obj{"interval": "5m", "chart": obj{"spec": obj{"chart": "podinfo", "version": "6.x", "sourceRef": obj{"kind": "HelmRepository", "name": "podinfo", "namespace": "flux-system"}}}},
				obj{"history": []any{obj{"chartName": "podinfo", "chartVersion": "6.5.0"}}}),
			check: func(t *testing.T, r model.Resource) {
				if r.Chart != "podinfo@6.5.0" || r.Revision != "6.5.0" {
					t.Fatalf("chart %q revision %q", r.Chart, r.Revision)
				}
				want := model.Ref{Group: GroupSource, Kind: "HelmRepository", Namespace: "flux-system", Name: "podinfo"}
				if r.Source == nil || *r.Source != want {
					t.Fatalf("source %+v", r.Source)
				}
			},
		},
		{
			name: "helmrelease chartRef",
			u: newObj("helm.toolkit.fluxcd.io/v2", KindHelmRelease, "apps", "podinfo",
				obj{"chartRef": obj{"kind": "OCIRepository", "name": "podinfo"}}, obj{"lastAttemptedRevision": "6.6.0"}),
			check: func(t *testing.T, r model.Resource) {
				want := model.Ref{Group: GroupSource, Kind: "OCIRepository", Namespace: "apps", Name: "podinfo"}
				if r.Source == nil || *r.Source != want || r.Revision != "6.6.0" || r.Chart != "" {
					t.Fatalf("got %+v source %+v", r, r.Source)
				}
			},
		},
		{
			name: "gitrepository",
			u: newObj("source.toolkit.fluxcd.io/v1", KindGitRepository, "flux-system", "repo",
				obj{"url": "https://github.com/example/repo", "interval": "1m"}, obj{"artifact": obj{"revision": "main@sha1:def"}}),
			check: func(t *testing.T, r model.Resource) {
				if r.URL != "https://github.com/example/repo" || r.Revision != "main@sha1:def" || r.Interval != "1m" {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name: "helmchart",
			u: newObj("source.toolkit.fluxcd.io/v1", KindHelmChart, "flux-system", "apps-podinfo",
				obj{"chart": "podinfo", "version": "6.x", "sourceRef": obj{"kind": "HelmRepository", "name": "podinfo"}}, obj{"artifact": obj{"revision": "6.5.0"}}),
			check: func(t *testing.T, r model.Resource) {
				if r.Chart != "podinfo@6.5.0" || r.Source == nil || r.Source.Kind != "HelmRepository" {
					t.Fatalf("got %+v", r)
				}
			},
		},
		{
			name: "bucket",
			u: newObj("source.toolkit.fluxcd.io/v1", KindBucket, "flux-system", "b",
				obj{"endpoint": "minio.example.com/", "bucketName": "manifests"}, nil),
			check: func(t *testing.T, r model.Resource) {
				if r.URL != "minio.example.com/manifests" {
					t.Fatalf("url %q", r.URL)
				}
			},
		},
		{
			name: "oci v1beta2 version",
			u:    newObj("source.toolkit.fluxcd.io/v1beta2", KindOCIRepository, "flux-system", "o", obj{"url": "oci://ghcr.io/x"}, nil),
			check: func(t *testing.T, r model.Resource) {
				if r.Version != "v1beta2" || r.URL != "oci://ghcr.io/x" {
					t.Fatalf("got %+v", r)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, Summarize(mustKind(t, tt.u.GetKind()), tt.u, nil))
		})
	}
}

func TestSummarizeWorkloads(t *testing.T) {
	tpl := obj{"template": obj{"spec": obj{"containers": []any{obj{"name": "app", "image": "ghcr.io/x/app:1"}, obj{"name": "side", "image": "ghcr.io/x/app:1"}}}}}
	withSpec := func(extra obj) obj {
		s := obj{"replicas": int64(3)}
		for k, v := range tpl {
			s[k] = v
		}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	tests := []struct {
		name     string
		kind     string
		spec     obj
		status   obj
		want     model.Status
		replicas string
	}{
		{"deployment ready", KindDeployment, withSpec(nil), obj{"observedGeneration": int64(3), "replicas": int64(3), "readyReplicas": int64(3), "updatedReplicas": int64(3), "availableReplicas": int64(3)}, model.StatusReady, "3/3"},
		{"deployment rolling", KindDeployment, withSpec(nil), obj{"observedGeneration": int64(3), "replicas": int64(4), "readyReplicas": int64(3), "updatedReplicas": int64(1)}, model.StatusReconciling, "3/3"},
		{"deployment deadline", KindDeployment, withSpec(nil), obj{"observedGeneration": int64(3), "replicas": int64(4), "readyReplicas": int64(2), "updatedReplicas": int64(1), "conditions": conds(cond("Progressing", "False", "ProgressDeadlineExceeded", "ReplicaSet x has timed out progressing."))}, model.StatusFailed, "2/3"},
		{"deployment unavailable", KindDeployment, withSpec(nil), obj{"observedGeneration": int64(3), "replicas": int64(3), "readyReplicas": int64(1), "updatedReplicas": int64(3), "conditions": conds(cond("Available", "False", "MinimumReplicasUnavailable", "Deployment does not have minimum availability."))}, model.StatusFailed, "1/3"},
		{"deployment paused", KindDeployment, withSpec(obj{"paused": true}), obj{}, model.StatusSuspended, "0/3"},
		{"deployment scaled to zero", KindDeployment, withSpec(obj{"replicas": int64(0)}), obj{"observedGeneration": int64(3)}, model.StatusReady, "0/0"},
		{"deployment stale generation", KindDeployment, withSpec(nil), obj{"observedGeneration": int64(2), "replicas": int64(3), "readyReplicas": int64(3), "updatedReplicas": int64(3)}, model.StatusReconciling, "3/3"},
		{"statefulset partial", KindStatefulSet, withSpec(nil), obj{"readyReplicas": int64(2), "updatedReplicas": int64(3), "currentRevision": "a", "updateRevision": "a"}, model.StatusReconciling, "2/3"},
		{"statefulset ready", KindStatefulSet, withSpec(nil), obj{"readyReplicas": int64(3), "updatedReplicas": int64(3), "replicas": int64(3), "currentRevision": "a", "updateRevision": "a"}, model.StatusReady, "3/3"},
		{"daemonset ready", KindDaemonSet, tpl, obj{"desiredNumberScheduled": int64(5), "numberReady": int64(5), "updatedNumberScheduled": int64(5), "currentNumberScheduled": int64(5)}, model.StatusReady, "5/5"},
		{"daemonset rolling", KindDaemonSet, tpl, obj{"desiredNumberScheduled": int64(5), "numberReady": int64(5), "updatedNumberScheduled": int64(2), "currentNumberScheduled": int64(5)}, model.StatusReconciling, "5/5"},
		{"replicaset failure", KindReplicaSet, withSpec(nil), obj{"conditions": conds(cond("ReplicaFailure", "True", "FailedCreate", "quota exceeded"))}, model.StatusFailed, "0/3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newObj("apps/v1", tt.kind, "apps", "web", tt.spec, tt.status)
			r := Summarize(mustKind(t, tt.kind), u, nil)
			if r.Status != tt.want || r.Replicas != tt.replicas {
				t.Fatalf("got %s %s (%q), want %s %s", r.Status, r.Replicas, r.Message, tt.want, tt.replicas)
			}
			if len(r.Images) != 1 || r.Images[0] != "ghcr.io/x/app:1" {
				t.Fatalf("images %v", r.Images)
			}
		})
	}
}

func TestSummarizePods(t *testing.T) {
	spec := obj{"containers": []any{obj{"name": "app", "image": "app:1"}}}
	cs := func(ready bool, state obj) []any {
		return []any{obj{"name": "app", "ready": ready, "state": state}}
	}
	tests := []struct {
		name    string
		status  obj
		want    model.Status
		msgPart string
	}{
		{"running", obj{"phase": "Running", "containerStatuses": cs(true, obj{"running": obj{}})}, model.StatusReady, "Running"},
		{"crashloop", obj{"phase": "Running", "containerStatuses": cs(false, obj{"waiting": obj{"reason": "CrashLoopBackOff", "message": "back-off 5m"}})}, model.StatusFailed, "app: CrashLoopBackOff: back-off 5m"},
		{"image pull", obj{"phase": "Pending", "containerStatuses": cs(false, obj{"waiting": obj{"reason": "ImagePullBackOff"}})}, model.StatusFailed, "ImagePullBackOff"},
		{"err image pull", obj{"phase": "Pending", "containerStatuses": cs(false, obj{"waiting": obj{"reason": "ErrImagePull"}})}, model.StatusFailed, "ErrImagePull"},
		{"config error", obj{"phase": "Pending", "containerStatuses": cs(false, obj{"waiting": obj{"reason": "CreateContainerConfigError", "message": "secret not found"}})}, model.StatusFailed, "secret not found"},
		{"init container crash", obj{"phase": "Pending", "initContainerStatuses": []any{obj{"name": "init", "state": obj{"waiting": obj{"reason": "CrashLoopBackOff"}}}}}, model.StatusFailed, "init: CrashLoopBackOff"},
		{"creating", obj{"phase": "Pending", "containerStatuses": cs(false, obj{"waiting": obj{"reason": "ContainerCreating"}})}, model.StatusReconciling, "app: ContainerCreating"},
		{"not ready", obj{"phase": "Running", "containerStatuses": cs(false, obj{"running": obj{}})}, model.StatusReconciling, "0 of 1 containers ready"},
		{"succeeded", obj{"phase": "Succeeded"}, model.StatusReady, "Completed"},
		{"evicted", obj{"phase": "Failed", "reason": "Evicted", "message": "The node was low on resource: memory."}, model.StatusFailed, "low on resource"},
		{"no status", nil, model.StatusUnknown, "No status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Summarize(mustKind(t, KindPod), newObj("v1", "Pod", "apps", "web-1", spec, tt.status), nil)
			if r.Status != tt.want || !strings.Contains(r.Message, tt.msgPart) {
				t.Fatalf("got %s %q, want %s containing %q", r.Status, r.Message, tt.want, tt.msgPart)
			}
			if r.Group != "" || r.ID != "/Pod/apps/web-1" {
				t.Fatalf("id %q", r.ID)
			}
			if len(r.Containers) != 1 || r.Containers[0] != "app" {
				t.Fatalf("containers %v", r.Containers)
			}
		})
	}
}

func TestSummarizeTerminating(t *testing.T) {
	u := newObj("v1", "Pod", "apps", "p", obj{"containers": []any{obj{"name": "a", "image": "a"}}}, obj{"phase": "Running", "containerStatuses": []any{obj{"name": "a", "ready": true}}})
	u.Object["metadata"].(obj)["deletionTimestamp"] = "2026-03-01T00:00:00Z"
	if r := Summarize(mustKind(t, KindPod), u, nil); r.Status != model.StatusReconciling || r.Message != "Terminating" {
		t.Fatalf("got %s %q", r.Status, r.Message)
	}
}

func TestOwnershipAndLabels(t *testing.T) {
	boolPtr := true
	rsToDeploy := func(ref model.Ref) (model.Ref, bool) {
		if ref.Kind == "ReplicaSet" && ref.Name == "web-abc" {
			return model.Ref{Group: "apps", Kind: "Deployment", Namespace: ref.Namespace, Name: "web"}, true
		}
		return model.Ref{}, false
	}
	tests := []struct {
		name   string
		labels map[string]string
		owners []any
		lookup OwnerLookup
		want   *model.Ref
	}{
		{"none", nil, nil, nil, nil},
		{"kustomize labels", map[string]string{LabelKustomizeName: "apps", LabelKustomizeNamespace: "flux-system"}, nil, nil,
			&model.Ref{Group: GroupKustomize, Kind: KindKustomization, Namespace: "flux-system", Name: "apps"}},
		{"helm labels win", map[string]string{LabelKustomizeName: "apps", LabelHelmName: "podinfo", LabelHelmNamespace: "apps"}, nil, nil,
			&model.Ref{Group: GroupHelm, Kind: KindHelmRelease, Namespace: "apps", Name: "podinfo"}},
		{"owner ref resolved to deployment", map[string]string{LabelKustomizeName: "ignored"},
			[]any{obj{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "web-abc", "uid": "1", "controller": boolPtr}}, rsToDeploy,
			&model.Ref{Group: "apps", Kind: "Deployment", Namespace: "apps", Name: "web"}},
		{"owner ref unresolved", nil,
			[]any{obj{"apiVersion": "batch/v1", "kind": "Job", "name": "migrate", "uid": "1", "controller": boolPtr}}, rsToDeploy,
			&model.Ref{Group: "batch", Kind: "Job", Namespace: "apps", Name: "migrate"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newObj("v1", "Pod", "apps", "web-abc-1", nil, nil)
			u.SetLabels(tt.labels)
			if tt.owners != nil {
				u.Object["metadata"].(obj)["ownerReferences"] = tt.owners
			}
			r := Summarize(mustKind(t, KindPod), u, tt.lookup)
			switch {
			case tt.want == nil && r.Owner != nil, tt.want != nil && (r.Owner == nil || *r.Owner != *tt.want):
				t.Fatalf("owner %+v, want %+v", r.Owner, tt.want)
			}
		})
	}

	u := newObj("apps/v1", "Deployment", "apps", "web", nil, nil)
	u.SetLabels(map[string]string{"app.kubernetes.io/name": "web", LabelKustomizeName: "apps", "team": "secret-team", "pod-template-hash": "x"})
	got := Summarize(mustKind(t, KindDeployment), u, nil).Labels
	if len(got) != 2 || got["app.kubernetes.io/name"] != "web" || got[LabelKustomizeName] != "apps" {
		t.Fatalf("labels %v", got)
	}
}

func TestOneLine(t *testing.T) {
	long := strings.Repeat("é", 400)
	if got := OneLine(long, maxMessage); len([]rune(got)) != maxMessage || !strings.HasSuffix(got, "…") {
		t.Fatalf("len %d", len([]rune(got)))
	}
	if got := OneLine("a\n\n  b\tc", 10); got != "a b c" {
		t.Fatalf("got %q", got)
	}
}
