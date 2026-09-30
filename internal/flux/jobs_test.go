package flux

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestJobNamePrefix(t *testing.T) {
	tests := []struct {
		name, want string
		ok         bool
	}{
		{"backup-29260001", "backup", true},
		{"db-migrate-x7k2p", "db-migrate", true},
		{"build-1234", "build", true},
		{"release-abcdef0123456789", "release", true},
		{"hasty-cougar", "", false}, // Prefect flow run names are words
		{"onyx-hog", "", false},
		{"cleanup", "", false},
		{"x-", "", false},
		{"-7", "", false},
		{"sync-ABC12", "", false},
		{"too-long-suffix-abcdefghijklmnop1", "", false},
	}
	for _, tt := range tests {
		got, ok := JobNamePrefix(tt.name)
		if got != tt.want || ok != tt.ok {
			t.Errorf("JobNamePrefix(%q) = %q, %v; want %q, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
}

// prefectLabels is the label set the Prefect Kubernetes worker puts on a
// flow run's Job.
func prefectLabels(deployment, pool string) map[string]string {
	l := map[string]string{
		"prefect.io/flow-run-id":   "0b9e3c1e-6a4f-4e0e-9a5c-2f1d3b4c5d6e",
		"prefect.io/flow-run-name": "hasty-cougar",
		"prefect.io/flow-id":       "7c1d2e3f-0000-4000-8000-000000000001",
		"prefect.io/flow-name":     "expire-applications",
		"prefect.io/version":       "3.1.4",
		"prefect.io/worker-name":   "KubernetesWorker 1f2e3d4c",
	}
	if deployment != "" {
		l["prefect.io/deployment-id"] = "5a6b7c8d-0000-4000-8000-000000000002"
		l[LabelPrefectDeployment] = deployment
	}
	if pool != "" {
		l[LabelPrefectWorkPool] = pool
	}
	return l
}

func TestJobGroupPrecedence(t *testing.T) {
	yes := true
	tests := []struct {
		name        string
		job         string
		labels      map[string]string
		annotations map[string]string
		owner       bool
		generate    string
		wantBy      string
		wantName    string
		wantLabel   string
	}{
		{"cronjob owner wins over labels", "nightly-29260001", prefectLabels("d", "p"), nil, true, "", JobGroupOwner, "CronJob/nightly", "CronJob nightly"},
		{"cronjob-name label", "nightly-29260001", map[string]string{LabelCronJobName: "nightly"}, nil, false, "", JobGroupLabel, LabelCronJobName + "=nightly", "CronJob nightly"},
		{"prefect deployment", "hasty-cougar", prefectLabels("application-domain-expire-applications-job", "k8s-pool"), nil, false, "", JobGroupLabel,
			LabelPrefectDeployment + "=application-domain-expire-applications-job", "prefect deployment application-domain-expire-applications-job"},
		{"prefect work pool without a deployment", "onyx-hog", prefectLabels("", "k8s-pool"), nil, false, "", JobGroupLabel, LabelPrefectWorkPool + "=k8s-pool", "prefect work pool k8s-pool"},
		{"helm hook by release", "web-migrate", map[string]string{LabelAppInstance: "web", LabelAppName: "web", LabelHelmChart: "web-1.2.3"},
			map[string]string{AnnotationHelmHook: "pre-upgrade"}, false, "", JobGroupLabel, LabelAppInstance + "=web", "helm hooks of web"},
		{"app instance and name", "web-migrate", map[string]string{LabelAppInstance: "web", LabelAppName: "migrate"}, nil, false, "", JobGroupLabel,
			LabelAppInstance + "=web," + LabelAppName + "=migrate", "app migrate (web)"},
		{"app name", "report", map[string]string{LabelAppName: "report"}, nil, false, "", JobGroupLabel, LabelAppName + "=report", "app report"},
		{"chart", "report", map[string]string{LabelHelmChart: "report-0.1.0"}, nil, false, "", JobGroupLabel, LabelHelmChart + "=report-0.1.0", "chart report-0.1.0"},
		{"generateName", "etl-x7k2p", nil, nil, false, "etl-", JobGroupGenerateName, "etl-", "generateName etl-"},
		{"name prefix", "backup-29260001", nil, nil, false, "", JobGroupPrefix, "backup", "name prefix backup"},
		{"prefect names without labels fall back to the namespace", "hasty-cougar", map[string]string{"prefect.io/flow-run-name": "hasty-cougar"}, nil, false, "", JobGroupNamespace, "", "other Jobs in prefect"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := newObj("batch/v1", KindJob, "prefect", tt.job, nil, nil)
			j.SetLabels(tt.labels)
			j.SetAnnotations(tt.annotations)
			j.SetGenerateName(tt.generate)
			if tt.owner {
				j.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: KindCronJob, Name: "nightly", Controller: &yes}})
			}
			Trim(mustKind(t, KindJob), j) // grouping must survive the informer transform
			g := JobFactsOf(j).Group
			if g.By != tt.wantBy || g.Name != tt.wantName || g.Label != tt.wantLabel {
				t.Fatalf("group %+v", g)
			}
		})
	}
}

func TestJobFacts(t *testing.T) {
	done := newObj("batch/v1", KindJob, "apps", "a", obj{"ttlSecondsAfterFinished": int64(600)},
		obj{"completionTime": "2026-03-01T00:00:00Z", "conditions": conds(cond("Complete", "True", "", ""))})
	f := JobFactsOf(done)
	if !f.Finished || f.Failed || !f.HasTTL || f.Owned || !f.FinishedAt.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("complete facts %+v", f)
	}
	failed := newObj("batch/v1", KindJob, "apps", "b", nil, obj{"conditions": conds(
		obj{"type": "FailureTarget", "status": "True"}, obj{"type": "Failed", "status": "True"})})
	f = JobFactsOf(failed)
	if !f.Finished || !f.Failed || f.HasTTL || !f.FinishedAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("failed facts %+v", f)
	}
	running := newObj("batch/v1", KindJob, "apps", "c", nil, obj{"active": int64(1)})
	if f := JobFactsOf(running); f.Finished || !f.FinishedAt.IsZero() {
		t.Fatalf("running facts %+v", f)
	}
}
