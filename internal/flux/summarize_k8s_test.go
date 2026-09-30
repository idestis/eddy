package flux

import (
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/idestis/eddy/internal/model"
)

func TestSummarizeService(t *testing.T) {
	tests := []struct {
		name      string
		spec      obj
		status    obj
		want      model.Status
		wantMsg   string
		wantPorts []string
	}{
		{"cluster ip", obj{"type": "ClusterIP", "clusterIP": "10.0.0.7", "ports": []any{obj{"port": int64(80), "protocol": "TCP", "targetPort": int64(8080)}, obj{"port": int64(443), "targetPort": int64(443)}}},
			nil, model.StatusReady, "ClusterIP 10.0.0.7", []string{"80/TCP → 8080", "443/TCP"}},
		{"named target port", obj{"clusterIP": "10.0.0.8", "ports": []any{obj{"port": int64(53), "protocol": "UDP", "targetPort": "dns"}}},
			nil, model.StatusReady, "ClusterIP 10.0.0.8", []string{"53/UDP → dns"}},
		{"headless", obj{"clusterIP": "None"}, nil, model.StatusReady, "ClusterIP (headless)", nil},
		{"lb pending", obj{"type": "LoadBalancer", "clusterIP": "10.0.0.9"}, nil, model.StatusReconciling, "LoadBalancer: waiting for an address", nil},
		{"lb ready", obj{"type": "LoadBalancer"}, obj{"loadBalancer": obj{"ingress": []any{obj{"hostname": "a.elb.example.com"}, obj{"ip": "1.2.3.4"}}}},
			model.StatusReady, "LoadBalancer a.elb.example.com, 1.2.3.4", nil},
		{"external name", obj{"type": "ExternalName", "externalName": "db.example.com"}, nil, model.StatusReady, "ExternalName db.example.com", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Summarize(mustKind(t, KindService), newObj("v1", KindService, "apps", "web", tt.spec, tt.status), nil)
			if r.Status != tt.want || r.Message != tt.wantMsg || !slices.Equal(r.Ports, tt.wantPorts) {
				t.Fatalf("got %s %q %q", r.Status, r.Message, r.Ports)
			}
		})
	}
}

func TestSummarizeIngress(t *testing.T) {
	spec := obj{
		"ingressClassName": "nginx",
		"rules":            []any{obj{"host": "shop.example.com"}, obj{"host": "api.shop.example.com"}, obj{"host": "shop.example.com"}},
		"tls":              []any{obj{"hosts": []any{"shop.example.com"}}},
	}
	r := Summarize(mustKind(t, KindIngress), newObj("networking.k8s.io/v1", KindIngress, "apps", "shop", spec,
		obj{"loadBalancer": obj{"ingress": []any{obj{"ip": "10.1.2.3"}}}}), nil)
	if r.Status != model.StatusReady || r.Message != "nginx · shop.example.com +1 · TLS · 10.1.2.3" || !slices.Equal(r.Hosts, []string{"shop.example.com", "api.shop.example.com"}) {
		t.Fatalf("got %s %q %v", r.Status, r.Message, r.Hosts)
	}
	r = Summarize(mustKind(t, KindIngress), newObj("networking.k8s.io/v1", KindIngress, "apps", "any", obj{"defaultBackend": obj{}}, nil), nil)
	if r.Status != model.StatusReady || r.Message != "any host" || r.Hosts != nil {
		t.Fatalf("default backend: %s %q %v", r.Status, r.Message, r.Hosts)
	}
}

func TestSummarizeJob(t *testing.T) {
	tpl := obj{"template": obj{"spec": obj{"containers": []any{obj{"name": "backup", "image": "backup:3"}}}}}
	with := func(extra obj) obj {
		out := obj{}
		for k, v := range tpl {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	tests := []struct {
		name         string
		spec, status obj
		want         model.Status
		wantMsg      string
		wantReplicas string
		wantComplete string
	}{
		{"complete", tpl, obj{"succeeded": int64(1), "startTime": "2026-02-01T00:00:00Z", "completionTime": "2026-02-01T00:02:14Z", "conditions": conds(cond("Complete", "True", "", ""))},
			model.StatusCompleted, "Completed in 2m14s", "", "1/1"},
		{"complete without times", tpl, obj{"succeeded": int64(1), "conditions": conds(cond("Complete", "True", "", ""))}, model.StatusCompleted, "Completed", "", "1/1"},
		{"failed", tpl, obj{"failed": int64(6), "conditions": conds(cond("Failed", "True", "BackoffLimitExceeded", "Job has reached the specified backoff limit"))},
			model.StatusFailed, "BackoffLimitExceeded: Job has reached the specified backoff limit", "", "0/1"},
		{"running", with(obj{"completions": int64(3)}), obj{"active": int64(2), "succeeded": int64(1), "failed": int64(1)}, model.StatusReconciling, "Running, 2 active, 1/3 succeeded, 1 failed", "1/3", "1/3"},
		{"running single", tpl, obj{"active": int64(1)}, model.StatusReconciling, "Running, 1 active", "0/1", "0/1"},
		{"suspended", with(obj{"suspend": true}), nil, model.StatusSuspended, "Job suspended", "0/1", "0/1"},
		{"pending", tpl, nil, model.StatusReconciling, "Waiting for pods", "0/1", "0/1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Summarize(mustKind(t, KindJob), newObj("batch/v1", KindJob, "apps", "backup-1", tt.spec, tt.status), nil)
			if r.Status != tt.want || r.Message != tt.wantMsg || r.Replicas != tt.wantReplicas || r.Completions != tt.wantComplete || !slices.Equal(r.Images, []string{"backup:3"}) {
				t.Fatalf("got %s %q replicas %q completions %q %v", r.Status, r.Message, r.Replicas, r.Completions, r.Images)
			}
		})
	}
}

func TestSummarizeCronJob(t *testing.T) {
	spec := obj{"schedule": "0 2 * * *", "jobTemplate": obj{"spec": obj{"template": obj{"spec": obj{"containers": []any{obj{"name": "b", "image": "backup:3"}}}}}}}
	r := Summarize(mustKind(t, KindCronJob), newObj("batch/v1", KindCronJob, "apps", "nightly", spec,
		obj{"lastScheduleTime": "2026-03-04T02:00:00Z", "active": []any{obj{"name": "nightly-1"}}}), nil)
	if r.Status != model.StatusReady || r.Schedule != "0 2 * * *" || r.Message != "Last scheduled 2026-03-04T02:00:00Z, 1 active" || r.Images[0] != "backup:3" {
		t.Fatalf("got %+v", r)
	}
	if r.LastChanged.Format("2006-01-02") != "2026-03-04" {
		t.Fatalf("lastChanged %v", r.LastChanged)
	}
	spec["suspend"] = true
	r = Summarize(mustKind(t, KindCronJob), newObj("batch/v1", KindCronJob, "apps", "nightly", spec, nil), nil)
	if r.Status != model.StatusSuspended || !r.Suspended || r.Message != "Suspended. Not scheduled yet" {
		t.Fatalf("suspended: %s %q", r.Status, r.Message)
	}
}

func TestSummarizeHPA(t *testing.T) {
	spec := obj{"minReplicas": int64(2), "maxReplicas": int64(6), "metrics": []any{obj{"type": "Resource", "resource": obj{"name": "cpu", "target": obj{"type": "Utilization", "averageUtilization": int64(80)}}}}}
	cur := []any{obj{"type": "Resource", "resource": obj{"name": "cpu", "current": obj{"averageUtilization": int64(41)}}}}
	tests := []struct {
		name         string
		status       obj
		want         model.Status
		wantMsg      string
		wantReplicas string
	}{
		{"steady", obj{"currentReplicas": int64(2), "desiredReplicas": int64(2), "currentMetrics": cur, "conditions": conds(cond("AbleToScale", "True", "ReadyForNewScale", ""), cond("ScalingActive", "True", "ValidMetricFound", ""))},
			model.StatusReady, "2 replicas (min 2, max 6), CPU 41% of 80%", "2/2"},
		{"scaling", obj{"currentReplicas": int64(2), "desiredReplicas": int64(4)}, model.StatusReconciling, "Scaling from 2 to 4 replicas", "2/4"},
		{"no metrics", obj{"currentReplicas": int64(2), "desiredReplicas": int64(2), "conditions": conds(cond("ScalingActive", "False", "FailedGetResourceMetric", "failed to get cpu utilization"))},
			model.StatusFailed, "failed to get cpu utilization", "2/2"},
		{"disabled", obj{"conditions": conds(cond("ScalingActive", "False", "ScalingDisabled", "scaling is disabled since the replica count of the target is zero"))},
			model.StatusSuspended, "Scaling disabled (target scaled to zero)", "0/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Summarize(mustKind(t, KindHPA), newObj("autoscaling/v2", KindHPA, "apps", "web", spec, tt.status), nil)
			if r.Status != tt.want || r.Message != tt.wantMsg || r.Replicas != tt.wantReplicas {
				t.Fatalf("got %s %q %q", r.Status, r.Message, r.Replicas)
			}
		})
	}
}

func TestSummarizePVC(t *testing.T) {
	spec := obj{"storageClassName": "gp3", "resources": obj{"requests": obj{"storage": "10Gi"}}}
	tests := []struct {
		name    string
		status  obj
		want    model.Status
		wantMsg string
	}{
		{"bound", obj{"phase": "Bound", "capacity": obj{"storage": "8Gi"}}, model.StatusReady, "Bound · 8Gi · gp3"},
		{"pending", obj{"phase": "Pending"}, model.StatusReconciling, "Pending · 10Gi · gp3"},
		{"lost", obj{"phase": "Lost"}, model.StatusFailed, "Lost · 10Gi · gp3"},
		{"resizing", obj{"phase": "Bound", "capacity": obj{"storage": "8Gi"}, "conditions": conds(cond("FileSystemResizePending", "True", "", ""))}, model.StatusReconciling, "Bound · 8Gi · gp3 · resize pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Summarize(mustKind(t, KindPVC), newObj("v1", KindPVC, "apps", "data", spec, tt.status), nil)
			if r.Status != tt.want || r.Message != tt.wantMsg {
				t.Fatalf("got %s %q", r.Status, r.Message)
			}
		})
	}
}

func TestBatchOwnership(t *testing.T) {
	yes := true
	job := newObj("batch/v1", KindJob, "apps", "nightly-29260001", nil, nil)
	job.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: KindCronJob, Name: "nightly", Controller: &yes}})
	if r := Summarize(mustKind(t, KindJob), job, nil); r.Owner == nil || *r.Owner != (model.Ref{Group: GroupBatch, Kind: KindCronJob, Namespace: "apps", Name: "nightly"}) {
		t.Fatalf("job owner %+v", r.Owner)
	}
	pod := newObj("v1", KindPod, "apps", "nightly-29260001-abcde", nil, obj{"phase": "Succeeded"})
	pod.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: KindJob, Name: "nightly-29260001", Controller: &yes}})
	if r := Summarize(mustKind(t, KindPod), pod, nil); r.Owner == nil || r.Owner.Kind != KindJob || r.Owner.Group != GroupBatch || r.Status != model.StatusCompleted {
		t.Fatalf("pod owner %+v", r.Owner)
	}
	svc := newObj("v1", KindService, "apps", "web", obj{"clusterIP": "10.0.0.1"}, nil)
	svc.SetLabels(map[string]string{LabelHelmName: "web", LabelHelmNamespace: "flux-system"})
	if r := Summarize(mustKind(t, KindService), svc, nil); r.Owner == nil || r.Owner.Kind != KindHelmRelease || r.Owner.Namespace != "flux-system" {
		t.Fatalf("service owner %+v", r.Owner)
	}
}

func TestSanitizeYAMLRedactsBatchTemplates(t *testing.T) {
	env := []any{obj{"name": "PASSWORD", "value": "hunter2"}}
	job := newObj("batch/v1", KindJob, "apps", "j", obj{"template": obj{"spec": obj{"containers": []any{obj{"name": "a", "env": env}}}}}, nil)
	cron := newObj("batch/v1", KindCronJob, "apps", "c", obj{"jobTemplate": obj{"spec": obj{"template": obj{"spec": obj{"containers": []any{obj{"name": "a", "env": env}}}}}}}, nil)
	for _, u := range []*unstructured.Unstructured{job, cron} {
		out, err := SanitizeYAML(u)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "hunter2") {
			t.Fatalf("%s: env value leaked:\n%s", u.GetKind(), out)
		}
		k := mustKind(t, u.GetKind())
		Trim(k, u)
		if strings.Contains(strings.Join(Summarize(k, u, nil).Images, ""), "hunter2") {
			t.Fatal("trim")
		}
	}
}

func TestInventoryOnly(t *testing.T) {
	ks := newObj("kustomize.toolkit.fluxcd.io/v1", KindKustomization, "flux-system", "apps", nil, obj{"inventory": obj{"entries": []any{
		obj{"id": "apps_web_apps_Deployment", "v": "v1"},
		obj{"id": "apps_web__Service", "v": "v1"},
		obj{"id": "apps_web-config__ConfigMap", "v": "v1"},
		obj{"id": "apps_web-config__ConfigMap", "v": "v1"},
		obj{"id": "apps_creds__Secret", "v": "v1"},
		obj{"id": "_certificates.cert-manager.io_apiextensions.k8s.io_CustomResourceDefinition", "v": "v1"},
		obj{"id": "_letsencrypt_cert-manager.io_ClusterIssuer", "v": "v1"},
		obj{"id": "not-an-id"},
		obj{"id": "apps_x__lowercase"},
	}}})
	served := Served{KindDeployment: "v1", KindService: "v1"}
	rows := InventoryOnly(ks, served.Watches)
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.ID)
		if !r.InventoryOnly || r.Status != model.StatusUnknown || r.Owner == nil || *r.Owner != (model.Ref{Group: GroupKustomize, Kind: KindKustomization, Namespace: "flux-system", Name: "apps"}) {
			t.Fatalf("row %+v", r)
		}
		if r.Message != "" || r.Labels != nil || r.Conditions != nil {
			t.Fatalf("inventory-only row carries more than a name: %+v", r)
		}
	}
	want := []string{"/ConfigMap/apps/web-config", "/Secret/apps/creds", "apiextensions.k8s.io/CustomResourceDefinition//certificates.cert-manager.io", "cert-manager.io/ClusterIssuer//letsencrypt"}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids %v, want %v", ids, want)
	}
	// A kind in the table that the cluster does not serve is inventory-only too.
	if rows := InventoryOnly(ks, Served{}.Watches); len(rows) != 6 {
		t.Fatalf("unserved: %d rows", len(rows))
	}
	if InventoryOnly(newObj("helm.toolkit.fluxcd.io/v2", KindHelmRelease, "a", "b", nil, nil), served.Watches) != nil {
		t.Fatal("HelmReleases have no inventory")
	}
	if p, ok := InventoryPlural("", "Secret"); !ok || p != "secrets" {
		t.Fatal("secret plural")
	}
	if _, ok := InventoryPlural("cert-manager.io", "ClusterIssuer"); ok {
		t.Fatal("unknown plural")
	}
	if YAMLAllowed("Secret") || YAMLAllowed("ConfigMap") {
		t.Fatal("inventory kinds must never be yaml-allowed")
	}
}
