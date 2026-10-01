package flux

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/idestis/eddy/internal/model"
)

// fromYAML parses a fixture the way the dynamic client would deliver it.
func fromYAML(t *testing.T, s string) *unstructured.Unstructured {
	t.Helper()
	var o map[string]any
	if err := yaml.Unmarshal([]byte(s), &o); err != nil {
		t.Fatal(err)
	}
	return &unstructured.Unstructured{Object: o}
}

func summarizeYAML(t *testing.T, s string) model.Resource {
	t.Helper()
	u := fromYAML(t, s)
	k := mustKind(t, u.GetKind())
	if !k.Matches(u.GroupVersionKind().Group, u.GetKind()) {
		t.Fatalf("fixture group %s is not %s", u.GroupVersionKind().Group, k.Group)
	}
	// Summaries must survive the informer transform.
	before := Summarize(k, u.DeepCopy(), nil)
	Trim(k, u)
	after := Summarize(k, u, nil)
	if before.Status != after.Status || before.Message != after.Message || len(before.Details) != len(after.Details) {
		t.Fatalf("%s: Trim changed the summary:\n%+v\n%+v", k.Kind, before, after)
	}
	return after
}

func detail(r model.Resource, label string) string {
	for _, d := range r.Details {
		if d.Label == label {
			return d.Value
		}
	}
	return ""
}

const nodePoolReady = `
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
  generation: 4
  resourceVersion: "9001"
  creationTimestamp: "2026-05-01T10:00:00Z"
  labels: {app.kubernetes.io/managed-by: flux}
spec:
  weight: 10
  limits: {cpu: "1000", memory: 1000Gi}
  disruption: {consolidationPolicy: WhenEmptyOrUnderutilized, consolidateAfter: 1m}
  template:
    spec:
      expireAfter: 720h
      nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: default}
      requirements:
        - {key: karpenter.sh/capacity-type, operator: In, values: [spot, on-demand]}
status:
  resources: {cpu: "12", memory: 48Gi, nodes: "3", pods: "330", ephemeral-storage: 60Gi}
  conditions:
    - {type: ValidationSucceeded, status: "True", reason: ValidationSucceeded, message: "", lastTransitionTime: "2026-05-01T10:00:05Z", observedGeneration: 4}
    - {type: NodeClassReady, status: "True", reason: NodeClassReady, message: "", lastTransitionTime: "2026-05-01T10:00:05Z", observedGeneration: 4}
    - {type: Ready, status: "True", reason: Ready, message: "", lastTransitionTime: "2026-05-01T10:00:06Z", observedGeneration: 4}
`

func TestSummarizeKarpenter(t *testing.T) {
	r := summarizeYAML(t, nodePoolReady)
	if r.Status != model.StatusReady || r.Message != "3 nodes · CPU 12 / 1000 · memory 48Gi / 1000Gi" || r.Project != "karpenter" {
		t.Fatalf("NodePool %s %q %s", r.Status, r.Message, r.Project)
	}
	if detail(r, "Nodes") != "3" || detail(r, "Node class") != "EC2NodeClass/default" || detail(r, "Weight") != "10" {
		t.Fatalf("NodePool details %+v", r.Details)
	}

	notReady := strings.Replace(nodePoolReady, `{type: Ready, status: "True", reason: Ready, message: ""`,
		`{type: Ready, status: "False", reason: NodeClassNotReady, message: "NodeClassReady=False"`, 1)
	if r := summarizeYAML(t, notReady); r.Status != model.StatusFailed || r.Message != "NodeClassNotReady: NodeClassReady=False" {
		t.Fatalf("NodePool not ready %s %q", r.Status, r.Message)
	}
	stale := strings.Replace(nodePoolReady, "generation: 4", "generation: 5", 1)
	if r := summarizeYAML(t, stale); r.Status != model.StatusReconciling {
		t.Fatalf("NodePool with a stale condition: %s", r.Status)
	}

	claim := `
apiVersion: karpenter.sh/v1
kind: NodeClaim
metadata:
  name: default-x7k2p
  generation: 1
  resourceVersion: "77"
  creationTimestamp: "2026-05-02T08:00:00Z"
  labels:
    karpenter.sh/nodepool: default
    karpenter.sh/capacity-type: spot
    node.kubernetes.io/instance-type: m6i.large
    topology.kubernetes.io/zone: eu-west-1a
    karpenter.k8s.aws/instance-family: m6i
    kubernetes.io/arch: amd64
  ownerReferences:
    - {apiVersion: karpenter.sh/v1, kind: NodePool, name: default, uid: 1a2b, blockOwnerDeletion: true}
spec:
  nodeClassRef: {group: karpenter.k8s.aws, kind: EC2NodeClass, name: default}
  requirements: [{key: node.kubernetes.io/instance-type, operator: In, values: [m6i.large]}]
status:
  providerID: aws:///eu-west-1a/i-0abc
  nodeName: ip-10-0-1-23.eu-west-1.compute.internal
  imageID: ami-0123456789abcdef0
  capacity: {cpu: "2", memory: 7910Mi, pods: "29"}
  conditions:
    - {type: Launched, status: "True", reason: Launched, message: "", lastTransitionTime: "2026-05-02T08:00:20Z"}
    - {type: Registered, status: "True", reason: Registered, message: "", lastTransitionTime: "2026-05-02T08:01:00Z"}
    - {type: Initialized, status: "True", reason: Initialized, message: "", lastTransitionTime: "2026-05-02T08:01:30Z"}
    - {type: Ready, status: "True", reason: Ready, message: "", lastTransitionTime: "2026-05-02T08:01:30Z"}
`
	r = summarizeYAML(t, claim)
	if r.Status != model.StatusReady || r.Message != "m6i.large · spot · eu-west-1a · ip-10-0-1-23.eu-west-1.compute.internal" {
		t.Fatalf("NodeClaim %s %q", r.Status, r.Message)
	}
	if r.Owner == nil || r.Owner.Kind != KindNodePool || r.Owner.Name != "default" || r.Owner.Namespace != "" {
		t.Fatalf("NodeClaim owner %+v", r.Owner)
	}
	if r.Labels["karpenter.sh/capacity-type"] != "spot" || r.Labels["node.kubernetes.io/instance-type"] != "m6i.large" || r.Labels["kubernetes.io/arch"] != "" {
		t.Fatalf("NodeClaim labels %v", r.Labels)
	}
	if detail(r, "Node pool") != "default" || detail(r, "Image") != "ami-0123456789abcdef0" {
		t.Fatalf("NodeClaim details %+v", r.Details)
	}
	for _, tt := range []struct {
		from, to string
		want     model.Status
		msg      string
	}{
		{`{type: Launched, status: "True", reason: Launched, message: ""`, `{type: Launched, status: "False", reason: InsufficientCapacityError, message: "all requested instance types were unavailable"`,
			model.StatusFailed, "Launch failed: InsufficientCapacityError: all requested instance types were unavailable"},
		{`{type: Launched, status: "True", reason: Launched, message: ""`, `{type: Launched, status: "Unknown", reason: AwaitingReconciliation, message: ""`, model.StatusReconciling, "Launching"},
		{`{type: Registered, status: "True"`, `{type: Registered, status: "Unknown"`, model.StatusReconciling, "Waiting for the node to register"},
		{`{type: Initialized, status: "True"`, `{type: Initialized, status: "False"`, model.StatusReconciling, "Initializing the node"},
		{`{type: Ready, status: "True", reason: Ready, message: ""`, `{type: Ready, status: "False", reason: NodeNotReady, message: "kubelet stopped posting node status"`, model.StatusFailed, "NodeNotReady: kubelet stopped posting node status"},
	} {
		if r := summarizeYAML(t, strings.Replace(claim, tt.from, tt.to, 1)); r.Status != tt.want || r.Message != tt.msg {
			t.Errorf("NodeClaim %q: %s %q", tt.to, r.Status, r.Message)
		}
	}

	nodeClass := `
apiVersion: karpenter.k8s.aws/v1
kind: EC2NodeClass
metadata: {name: default, generation: 2, resourceVersion: "5", creationTimestamp: "2026-05-01T10:00:00Z"}
spec:
  role: KarpenterNodeRole-prod
  amiSelectorTerms: [{alias: al2023@latest}]
  subnetSelectorTerms: [{tags: {karpenter.sh/discovery: prod}}]
  userData: |
    #!/bin/bash
    echo "token=s3cr3t" > /etc/bootstrap
status:
  amis: [{id: ami-0a, name: al2023-ami-x86_64}, {id: ami-0b, name: al2023-ami-arm64}]
  instanceProfile: prod_1234
  conditions:
    - {type: AMIsReady, status: "True", reason: AMIsReady, message: ""}
    - {type: Ready, status: "True", reason: Ready, message: "", observedGeneration: 2}
`
	r = summarizeYAML(t, nodeClass)
	if r.Status != model.StatusReady || r.Message != "al2023@latest" || detail(r, "Role") != "KarpenterNodeRole-prod" || detail(r, "AMIs") != "2" {
		t.Fatalf("EC2NodeClass %s %q %+v", r.Status, r.Message, r.Details)
	}
	u := fromYAML(t, nodeClass)
	out, err := SanitizeYAML(u)
	if err != nil || strings.Contains(string(out), "s3cr3t") || !strings.Contains(string(out), "userData: '[REDACTED]'") {
		t.Fatalf("EC2NodeClass yaml %v:\n%s", err, out)
	}
	Trim(mustKind(t, KindEC2NodeClass), u)
	if _, ok := u.Object["spec"].(map[string]any)["userData"]; ok {
		t.Fatal("Trim kept userData")
	}
}

func TestSummarizeExternalSecrets(t *testing.T) {
	es := `
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: db-credentials
  namespace: payments
  generation: 2
  resourceVersion: "311"
  creationTimestamp: "2026-06-01T09:00:00Z"
  labels: {kustomize.toolkit.fluxcd.io/name: payments, kustomize.toolkit.fluxcd.io/namespace: flux-system}
spec:
  refreshInterval: 1h
  secretStoreRef: {name: aws-secrets-manager, kind: ClusterSecretStore}
  target:
    name: db-creds
    creationPolicy: Owner
    template:
      data: {DSN: "postgres://app:literal-password@db:5432/app"}
  data:
    - secretKey: password
      remoteRef: {key: prod/payments/db, property: password}
status:
  refreshTime: "2026-09-30T11:00:00Z"
  syncedResourceVersion: 1-0a1b2c
  binding: {name: db-creds}
  conditions:
    - {type: Ready, status: "True", reason: SecretSynced, message: Secret was synced, lastTransitionTime: "2026-06-01T09:00:03Z"}
`
	r := summarizeYAML(t, es)
	if r.Status != model.StatusReady || r.Message != "Secret was synced" || r.Interval != "1h" || r.Project != "external-secrets" {
		t.Fatalf("ExternalSecret %s %q %q", r.Status, r.Message, r.Interval)
	}
	if r.Source == nil || *r.Source != (model.Ref{Group: GroupESO, Kind: KindClusterSecretStore, Name: "aws-secrets-manager"}) {
		t.Fatalf("store ref %+v", r.Source)
	}
	if detail(r, "Target secret") != "db-creds" || detail(r, "Last refresh") != "2026-09-30T11:00:00Z" || detail(r, "Store") != "ClusterSecretStore/aws-secrets-manager" {
		t.Fatalf("details %+v", r.Details)
	}
	if r.Owner == nil || r.Owner.Kind != KindKustomization {
		t.Fatalf("owner %+v", r.Owner)
	}
	failed := strings.Replace(es, "{type: Ready, status: \"True\", reason: SecretSynced, message: Secret was synced",
		"{type: Ready, status: \"False\", reason: SecretSyncedError, message: \"could not get secret data from provider\"", 1)
	if r := summarizeYAML(t, failed); r.Status != model.StatusFailed || r.Message != "SecretSyncedError: could not get secret data from provider" {
		t.Fatalf("failed ExternalSecret %s %q", r.Status, r.Message)
	}
	u := fromYAML(t, es)
	Trim(mustKind(t, KindExternalSecret), u)
	if b, _ := yaml.Marshal(u.Object); strings.Contains(string(b), "literal-password") {
		t.Fatalf("Trim kept the target template:\n%s", b)
	}

	// v1beta1, with the target name defaulting to the ExternalSecret's.
	beta := `
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata: {name: api-key, namespace: web, generation: 1, resourceVersion: "3"}
spec:
  refreshInterval: 15m
  secretStoreRef: {name: vault}
  dataFrom: [{extract: {key: web/api}}]
status:
  conditions: [{type: Ready, status: "True", reason: SecretSynced, message: Secret was synced}]
`
	r = summarizeYAML(t, beta)
	if detail(r, "Target secret") != "api-key" || r.Source == nil || *r.Source != (model.Ref{Group: GroupESO, Kind: KindSecretStore, Namespace: "web", Name: "vault"}) {
		t.Fatalf("v1beta1 ExternalSecret %+v %+v", r.Details, r.Source)
	}

	ces := `
apiVersion: external-secrets.io/v1beta1
kind: ClusterExternalSecret
metadata: {name: registry-creds, generation: 1, resourceVersion: "8"}
spec:
  externalSecretName: registry
  namespaceSelectors: [{matchLabels: {registry: "true"}}]
  refreshTime: 1m
  externalSecretSpec:
    refreshInterval: 1h
    secretStoreRef: {name: aws, kind: ClusterSecretStore}
    target: {name: regcred}
status:
  provisionedNamespaces: [web, api]
  failedNamespaces: [{namespace: batch, reason: "external secret already exists in namespace"}]
  conditions: [{type: Ready, status: "True", reason: "", message: ""}]
`
	r = summarizeYAML(t, ces)
	if r.Status != model.StatusFailed || !strings.HasPrefix(r.Message, "Failed in 1 namespaces: batch") || detail(r, "Namespaces") != "2 provisioned, 1 failed" || detail(r, "Target secret") != "regcred" {
		t.Fatalf("ClusterExternalSecret %s %q %+v", r.Status, r.Message, r.Details)
	}

	store := `
apiVersion: external-secrets.io/v1
kind: ClusterSecretStore
metadata: {name: aws-secrets-manager, generation: 1, resourceVersion: "4"}
spec:
  provider:
    aws:
      service: SecretsManager
      region: eu-west-1
      auth: {jwt: {serviceAccountRef: {name: external-secrets, namespace: external-secrets}}}
status:
  capabilities: ReadWrite
  conditions: [{type: Ready, status: "True", reason: Valid, message: store validated}]
`
	r = summarizeYAML(t, store)
	if r.Status != model.StatusReady || r.Message != "aws · ReadWrite · store validated" || detail(r, "Provider") != "aws" {
		t.Fatalf("ClusterSecretStore %s %q", r.Status, r.Message)
	}
	fake := `
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata: {name: fake, namespace: dev, generation: 1}
spec:
  provider:
    fake:
      data: [{key: /db/password, value: hunter2, version: v1}]
status:
  conditions: [{type: Ready, status: "False", reason: InvalidProviderConfig, message: "unable to validate store"}]
`
	r = summarizeYAML(t, fake)
	if r.Status != model.StatusFailed || r.Message != "InvalidProviderConfig: unable to validate store" {
		t.Fatalf("SecretStore %s %q", r.Status, r.Message)
	}
	out, err := SanitizeYAML(fromYAML(t, fake))
	if err != nil || strings.Contains(string(out), "hunter2") {
		t.Fatalf("fake provider yaml %v:\n%s", err, out)
	}
	u = fromYAML(t, fake)
	Trim(mustKind(t, KindSecretStore), u)
	if b, _ := yaml.Marshal(u.Object); strings.Contains(string(b), "hunter2") || !strings.Contains(string(b), "fake: {}") {
		t.Fatalf("Trim kept provider config:\n%s", b)
	}

	push := `
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata: {name: push-tls, namespace: edge, generation: 3, resourceVersion: "12"}
spec:
  refreshInterval: 10m
  secretStoreRefs: [{name: vault, kind: SecretStore}, {kind: ClusterSecretStore, labelSelector: {matchLabels: {team: edge}}}]
  selector: {secret: {name: edge-tls}}
  data: [{match: {secretKey: tls.crt, remoteRef: {remoteKey: edge/tls}}}]
status:
  refreshTime: "2026-09-30T10:50:00Z"
  conditions: [{type: Ready, status: "True", reason: Synced, message: PushSecret synced successfully, observedGeneration: 2}]
`
	r = summarizeYAML(t, push)
	if r.Status != model.StatusReconciling {
		t.Fatalf("PushSecret with a stale Ready condition: %s", r.Status)
	}
	if detail(r, "Source secret") != "edge-tls" || detail(r, "Stores") != "SecretStore/vault, ClusterSecretStore (team=edge)" || r.Interval != "10m" {
		t.Fatalf("PushSecret details %+v", r.Details)
	}
}

func TestSummarizeConditionsGeneric(t *testing.T) {
	tests := []struct {
		name   string
		spec   obj
		status obj
		want   model.Status
		msg    string
	}{
		{"suspended", obj{"suspend": true}, obj{"conditions": conds(cond("Ready", "True", "Ok", "fine"))}, model.StatusSuspended, "Suspended"},
		{"no conditions", nil, nil, model.StatusUnknown, "Waiting for the controller to report status"},
		{"ready", nil, obj{"observedGeneration": int64(3), "conditions": conds(cond("Ready", "True", "Ok", "fine"))}, model.StatusReady, "fine"},
		{"available", nil, obj{"conditions": conds(cond("Available", "True", "MinimumReplicasAvailable", ""))}, model.StatusReady, "MinimumReplicasAvailable"},
		{"synced false", nil, obj{"conditions": conds(cond("Synced", "False", "ReconcileError", "boom"))}, model.StatusFailed, "ReconcileError: boom"},
		{"unknown", nil, obj{"conditions": conds(cond("Ready", "Unknown", "Progressing", ""))}, model.StatusReconciling, "Progressing"},
		{"stale", nil, obj{"observedGeneration": int64(2), "conditions": conds(cond("Ready", "True", "Ok", ""))}, model.StatusReconciling, "Waiting for the controller to observe the latest spec"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newObj("example.com/v1", "Widget", "a", "w", tt.spec, tt.status)
			r := model.Resource{Conditions: conditions(u.Object)}
			summarizeConditions(u.Object, &r)
			if r.Status != tt.want || r.Message != tt.msg {
				t.Fatalf("got %s %q", r.Status, r.Message)
			}
		})
	}
}
