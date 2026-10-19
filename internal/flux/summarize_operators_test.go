package flux

import (
	"strings"
	"testing"

	"github.com/idestis/eddy/internal/model"
)

const cnpgClusterHealthy = `
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: {name: orders, namespace: db, generation: 3, resourceVersion: "77"}
spec:
  instances: 3
  imageName: ghcr.io/cloudnative-pg/postgresql:17.2
  storage: {size: 50Gi, storageClass: gp3}
  walStorage: {size: 10Gi}
  env: [{name: PGPASSWORD, value: hunter2}]
  bootstrap:
    initdb: {database: app, owner: app, postInitSQL: ["CREATE ROLE ro PASSWORD 'hunter2'"]}
  postgresql: {parameters: {max_connections: "200"}}
status:
  phase: Cluster in healthy state
  instances: 3
  readyInstances: 3
  currentPrimary: orders-1
  image: ghcr.io/cloudnative-pg/postgresql:17.2
  lastSuccessfulBackup: "2026-10-18T02:00:12Z"
  certificates: {serverCASecret: orders-ca}
  instancesStatus: {healthy: [orders-1, orders-2, orders-3]}
  conditions:
    - {type: Ready, status: "True", reason: ClusterIsReady, message: Cluster is Ready}
    - {type: ContinuousArchiving, status: "True", reason: ContinuousArchivingSuccess}
`

func cnpgCluster(phase, reason, ready string, extra ...string) string {
	return `
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata: {name: orders, namespace: db}
spec: {instances: 3}
status:
  phase: "` + phase + `"
  phaseReason: "` + reason + `"
  readyInstances: 2
  conditions:
    - {type: Ready, status: "` + ready + `", reason: R}
` + strings.Join(extra, "\n")
}

func TestSummarizeCNPGCluster(t *testing.T) {
	r := summarizeYAML(t, cnpgClusterHealthy)
	if r.Status != model.StatusReady || r.Message != "3/3 instances · primary orders-1" || r.Project != "cloudnative-pg" {
		t.Fatalf("healthy: %s %q %s", r.Status, r.Message, r.Project)
	}
	if r.Replicas != "3/3" || len(r.Images) != 1 || detail(r, "Storage") != "50Gi" || detail(r, "Last backup") != "2026-10-18T02:00:12Z" {
		t.Fatalf("healthy details %+v %v", r.Details, r.Images)
	}

	tests := []struct {
		name string
		in   string
		want model.Status
		msg  string
	}{
		{"unrecoverable", cnpgCluster("Cluster is in an unrecoverable state, needs manual intervention", "", "False"), model.StatusFailed, "Cluster is in an unrecoverable state, needs manual intervention"},
		{"unable with reason", cnpgCluster("Unable to create required cluster objects", "quota exceeded", "False"), model.StatusFailed, "Unable to create required cluster objects: quota exceeded"},
		{"upgrading", cnpgCluster("Upgrading cluster", "", "False"), model.StatusReconciling, "Upgrading cluster"},
		{"failing over", cnpgCluster("Failing over", "", "False"), model.StatusReconciling, "Failing over"},
		{"backup failing", cnpgCluster("Cluster in healthy state", "", "True", "    - {type: LastBackupSucceeded, status: \"False\", reason: LastBackupFailed}"), model.StatusReady, "2/3 instances · LastBackupSucceeded: LastBackupFailed"},
		{"hibernated", cnpgCluster("Cluster in healthy state", "", "False", "    - {type: cnpg.io/hibernation, status: \"True\", reason: Hibernated}"), model.StatusSuspended, "Hibernated"},
		{"new", "apiVersion: postgresql.cnpg.io/v1\nkind: Cluster\nmetadata: {name: n, namespace: db}\nspec: {instances: 1}\n", model.StatusUnknown, "Waiting for the operator to report status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := summarizeYAML(t, tt.in)
			if r.Status != tt.want || r.Message != tt.msg {
				t.Fatalf("got %s %q", r.Status, r.Message)
			}
		})
	}
}

func TestSummarizeCNPGBackups(t *testing.T) {
	backup := func(phase, extra string) string {
		return `
apiVersion: postgresql.cnpg.io/v1
kind: Backup
metadata: {name: orders-20261018, namespace: db}
spec: {cluster: {name: orders}, method: barmanObjectStore}
status:
  phase: "` + phase + `"
  startedAt: "2026-10-18T02:00:00Z"
` + extra
	}
	tests := []struct {
		name string
		in   string
		want model.Status
		msg  string
	}{
		{"completed", backup("completed", "  stoppedAt: \"2026-10-18T02:03:30Z\"\n"), model.StatusCompleted, "Completed in 3m30s"},
		{"failed", backup("failed", "  error: \"can't upload: AccessDenied\"\n"), model.StatusFailed, "can't upload: AccessDenied"},
		{"running", backup("running", ""), model.StatusReconciling, "Running"},
		{"pending", backup("", ""), model.StatusReconciling, "Pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := summarizeYAML(t, tt.in)
			if r.Status != tt.want || r.Message != tt.msg {
				t.Fatalf("got %s %q", r.Status, r.Message)
			}
			if r.Source == nil || r.Source.ID() != "postgresql.cnpg.io/Cluster/db/orders" || detail(r, "Method") != "barmanObjectStore" {
				t.Fatalf("source %+v details %+v", r.Source, r.Details)
			}
		})
	}

	sched := `
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata: {name: orders-daily, namespace: db}
spec: {schedule: "0 0 2 * * *", cluster: {name: orders}, method: volumeSnapshot, backupOwnerReference: self}
status:
  lastCheckTime: "2026-10-18T02:00:00Z"
  lastScheduleTime: "2026-10-18T02:00:00Z"
  nextScheduleTime: "2026-10-19T02:00:00Z"
`
	r := summarizeYAML(t, sched)
	if r.Status != model.StatusReady || r.Message != "Next backup at 2026-10-19T02:00:00Z" || r.Schedule != "0 0 2 * * *" || r.Source == nil {
		t.Fatalf("ScheduledBackup %s %q %q %+v", r.Status, r.Message, r.Schedule, r.Source)
	}
	r = summarizeYAML(t, strings.Replace(sched, "backupOwnerReference: self", "suspend: true", 1))
	if r.Status != model.StatusSuspended || !r.Suspended {
		t.Fatalf("suspended ScheduledBackup %s", r.Status)
	}

	pooler := `
apiVersion: postgresql.cnpg.io/v1
kind: Pooler
metadata: {name: orders-rw, namespace: db}
spec:
  cluster: {name: orders}
  instances: 2
  type: rw
  pgbouncer: {poolMode: transaction, parameters: {max_client_conn: "1000"}}
  template: {spec: {containers: [{name: pgbouncer, env: [{name: X, value: y}]}]}}
status: {instances: 1}
`
	r = summarizeYAML(t, pooler)
	if r.Status != model.StatusReconciling || r.Replicas != "1/2" || detail(r, "Pool mode") != "transaction" || detail(r, "Type") != "rw" {
		t.Fatalf("Pooler %s %q %+v", r.Status, r.Replicas, r.Details)
	}
	r = summarizeYAML(t, strings.Replace(pooler, "status: {instances: 1}", "status: {instances: 2}", 1))
	if r.Status != model.StatusReady || r.Message != "2/2 instances" {
		t.Fatalf("ready Pooler %s %q", r.Status, r.Message)
	}
}

func rabbit(conds string) string {
	return `
apiVersion: rabbitmq.com/v1beta1
kind: RabbitmqCluster
metadata: {name: events, namespace: mq, generation: 2}
spec:
  replicas: 3
  image: rabbitmq:3.13-management
  persistence: {storage: 20Gi}
  rabbitmq: {additionalConfig: "default_pass = hunter2"}
status:
  observedGeneration: 2
  conditions:
` + conds
}

func TestSummarizeRabbitmqCluster(t *testing.T) {
	const ok = `    - {type: AllReplicasReady, status: "True"}
    - {type: ClusterAvailable, status: "True"}
    - {type: NoWarnings, status: "True"}
    - {type: ReconcileSuccess, status: "True", reason: Success}
`
	tests := []struct {
		name  string
		conds string
		want  model.Status
		msg   string
	}{
		{"ready", ok, model.StatusReady, "3/3 replicas"},
		{"warning", strings.Replace(ok, `{type: NoWarnings, status: "True"}`, `{type: NoWarnings, status: "False", reason: MemoryAlarm, message: memory alarm}`, 1), model.StatusReady, "3/3 replicas · memory alarm"},
		{"rolling", strings.Replace(ok, `{type: AllReplicasReady, status: "True"}`, `{type: AllReplicasReady, status: "False", reason: NotAllReplicasReady, message: 1/3 ready}`, 1), model.StatusReconciling, "NotAllReplicasReady: 1/3 ready"},
		{"reconcile error", strings.Replace(ok, `{type: ReconcileSuccess, status: "True", reason: Success}`, `{type: ReconcileSuccess, status: "False", reason: Error, message: bad config}`, 1), model.StatusFailed, "Error: bad config"},
		{"unavailable", strings.Replace(ok, `{type: ClusterAvailable, status: "True"}`, `{type: ClusterAvailable, status: "False", reason: NoEndpoints}`, 1), model.StatusFailed, "NoEndpoints"},
		{"no status", "    []\n", model.StatusUnknown, "Waiting for the operator to report status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := summarizeYAML(t, rabbit(tt.conds))
			if r.Status != tt.want || r.Message != tt.msg || r.Project != "rabbitmq" {
				t.Fatalf("got %s %q %s", r.Status, r.Message, r.Project)
			}
		})
	}
}

const datadogAgent = `
apiVersion: datadoghq.com/v2alpha1
kind: DatadogAgent
metadata: {name: datadog, namespace: datadog}
spec:
  global:
    site: datadoghq.eu
    clusterName: prod
    credentials: {apiKey: "0123456789abcdef", appKey: "fedcba9876543210"}
    clusterAgentToken: "abcdefghijklmnopqrstuvwxyz012345"
  override:
    nodeAgent:
      env: [{name: DD_SECRET, value: hunter2}]
      containers: {agent: {env: [{name: DD_OTHER, value: hunter3}]}}
status:
  agent: {desired: 12, current: 12, ready: 12, available: 12, upToDate: 12, state: Running, status: "Running (12/12/12)"}
  clusterAgent: {replicas: 2, readyReplicas: 2, availableReplicas: 2, state: Running, generatedToken: "tok-tok-tok"}
  conditions:
    - {type: DatadogAgentReconcileError, status: "False", reason: ""}
`

func TestSummarizeDatadogAgent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want model.Status
		msg  string
	}{
		{"ready", datadogAgent, model.StatusReady, "Node agent 12/12 · cluster agent 2/2"},
		{"rolling", strings.Replace(datadogAgent, "ready: 12,", "ready: 9,", 1), model.StatusReconciling, "Node agent 9/12 ready"},
		{"failed state", strings.Replace(datadogAgent, "readyReplicas: 2, availableReplicas: 2, state: Running", "readyReplicas: 0, state: Failed", 1), model.StatusFailed, "Cluster agent failed"},
		{"error condition", strings.Replace(datadogAgent, `status: "False", reason: ""`, `status: "True", reason: Error, message: invalid credentials`, 1), model.StatusFailed, "Error: invalid credentials"},
		{"no status", "apiVersion: datadoghq.com/v2alpha1\nkind: DatadogAgent\nmetadata: {name: d, namespace: dd}\n", model.StatusUnknown, "Waiting for the operator to report status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := summarizeYAML(t, tt.in)
			if r.Status != tt.want || r.Message != tt.msg || r.Project != "datadog" {
				t.Fatalf("got %s %q %s", r.Status, r.Message, r.Project)
			}
		})
	}
	r := summarizeYAML(t, datadogAgent)
	if r.Replicas != "12/12" || detail(r, "Site") != "datadoghq.eu" || detail(r, "Cluster agent") != "2/2" {
		t.Fatalf("details %q %+v", r.Replicas, r.Details)
	}
}

// secretsIn are the inline credentials of the fixtures above.
var secretsIn = []string{"hunter", "0123456789abcdef", "fedcba9876543210", "abcdefghijklmnopqrstuvwxyz012345", "tok-tok-tok", "default_pass"}

func TestOperatorKindsNeverLeakCredentials(t *testing.T) {
	for _, in := range []string{cnpgClusterHealthy, rabbit("    []\n"), datadogAgent} {
		u := fromYAML(t, in)
		out, err := SanitizeYAML(u)
		if err != nil {
			t.Fatal(err)
		}
		k := mustKind(t, u.GetKind())
		Trim(k, u)
		trimmed, err := SanitizeYAML(u)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secretsIn {
			if strings.Contains(string(out), s) {
				t.Errorf("%s YAML leaks %q:\n%s", k.Kind, s, out)
			}
			if strings.Contains(string(trimmed), s) {
				t.Errorf("%s trimmed object keeps %q:\n%s", k.Kind, s, trimmed)
			}
		}
	}
}
