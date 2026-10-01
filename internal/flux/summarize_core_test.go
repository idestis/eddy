package flux

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/idestis/eddy/internal/model"
)

func TestSummarizeNamespace(t *testing.T) {
	ns := `
apiVersion: v1
kind: Namespace
metadata:
  name: payments
  resourceVersion: "10"
  labels:
    kubernetes.io/metadata.name: payments
    pod-security.kubernetes.io/enforce: restricted
    team: payments
    kustomize.toolkit.fluxcd.io/name: tenants
    kustomize.toolkit.fluxcd.io/namespace: flux-system
status: {phase: Active}
`
	r := summarizeYAML(t, ns)
	if r.Status != model.StatusReady || r.Message != "Active" || r.Namespace != "" || r.ID != "/Namespace//payments" || r.Project != "kubernetes" {
		t.Fatalf("namespace %+v", r)
	}
	if r.Labels["pod-security.kubernetes.io/enforce"] != "restricted" || r.Labels["team"] != "" || r.Labels["kubernetes.io/metadata.name"] != "" {
		t.Fatalf("labels %v", r.Labels)
	}
	if r.Owner == nil || r.Owner.Name != "tenants" || r.Owner.Namespace != "flux-system" {
		t.Fatalf("owner %+v", r.Owner)
	}
	term := strings.Replace(ns, "status: {phase: Active}", `status:
  phase: Terminating
  conditions:
    - {type: NamespaceContentRemaining, status: "True", reason: SomeResourcesRemain, message: "Some resources are remaining: pods. has 2 resource instances"}`, 1)
	if r := summarizeYAML(t, term); r.Status != model.StatusReconciling || !strings.HasPrefix(r.Message, "Terminating: Some resources are remaining") {
		t.Fatalf("terminating %s %q", r.Status, r.Message)
	}
}

func TestSummarizeStorageClass(t *testing.T) {
	sc := `
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: gp3
  resourceVersion: "3"
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
    kubectl.kubernetes.io/last-applied-configuration: '{"big":"blob"}'
provisioner: ebs.csi.aws.com
parameters: {type: gp3, encrypted: "true"}
reclaimPolicy: Retain
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
`
	r := summarizeYAML(t, sc)
	if r.Status != model.StatusReady || r.Message != "ebs.csi.aws.com · default · Retain · WaitForFirstConsumer" {
		t.Fatalf("storage class %s %q", r.Status, r.Message)
	}
	if detail(r, "Default class") != "yes" || detail(r, "Volume expansion") != "allowed" {
		t.Fatalf("details %+v", r.Details)
	}
	u := fromYAML(t, sc)
	Trim(mustKind(t, KindStorageClass), u)
	if ann := u.GetAnnotations(); len(ann) != 1 || ann[AnnotationDefaultStorageClass] != "true" {
		t.Fatalf("trimmed annotations %v", ann)
	}
	plain := fromYAML(t, "apiVersion: storage.k8s.io/v1\nkind: StorageClass\nmetadata: {name: standard}\nprovisioner: rancher.io/local-path\n")
	if r := Summarize(mustKind(t, KindStorageClass), plain, nil); r.Message != "rancher.io/local-path · Delete · Immediate" || detail(r, "Default class") != "" {
		t.Fatalf("defaults %q %+v", r.Message, r.Details)
	}
}

func TestSummarizePDB(t *testing.T) {
	pdb := func(spec, status string) string {
		return "apiVersion: policy/v1\nkind: PodDisruptionBudget\nmetadata: {name: web, namespace: apps, generation: 1, resourceVersion: \"4\"}\n" +
			"spec: " + spec + "\nstatus: " + status + "\n"
	}
	tests := []struct {
		name, spec, status string
		want               model.Status
		msg, replicas      string
	}{
		{"allows one", "{minAvailable: 2, selector: {matchLabels: {app: web}}}",
			`{observedGeneration: 1, currentHealthy: 3, desiredHealthy: 2, disruptionsAllowed: 1, expectedPods: 3, conditions: [{type: DisruptionAllowed, status: "True", reason: SufficientPods}]}`,
			model.StatusReady, "1 disruptions allowed · 3 of 3 healthy", "3/2"},
		{"blocks evictions", "{maxUnavailable: 0}",
			`{observedGeneration: 1, currentHealthy: 3, desiredHealthy: 3, disruptionsAllowed: 0, expectedPods: 3, conditions: [{type: DisruptionAllowed, status: "False", reason: InsufficientPods}]}`,
			model.StatusReady, "0 disruptions allowed · blocks voluntary evictions", "3/3"},
		{"unhealthy", "{minAvailable: \"50%\"}",
			`{observedGeneration: 1, currentHealthy: 1, desiredHealthy: 2, disruptionsAllowed: 0, expectedPods: 3, conditions: [{type: DisruptionAllowed, status: "False", reason: InsufficientPods}]}`,
			model.StatusReconciling, "1 of 2 required pods healthy", "1/2"},
		{"no pods", "{minAvailable: 1}",
			`{observedGeneration: 1, currentHealthy: 0, desiredHealthy: 1, disruptionsAllowed: 0, expectedPods: 0, conditions: [{type: DisruptionAllowed, status: "False", reason: InsufficientPods}]}`,
			model.StatusReady, "No pods selected", "0/1"},
		{"sync failed", "{minAvailable: 1}",
			`{observedGeneration: 1, conditions: [{type: DisruptionAllowed, status: "False", reason: SyncFailed, message: "found no controllers for pod web-0"}]}`,
			model.StatusFailed, "found no controllers for pod web-0", "0/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := summarizeYAML(t, pdb(tt.spec, tt.status))
			if r.Status != tt.want || r.Message != tt.msg || r.Replicas != tt.replicas {
				t.Fatalf("got %s %q %q", r.Status, r.Message, r.Replicas)
			}
		})
	}
	if r := summarizeYAML(t, pdb("{minAvailable: \"50%\"}", "{}")); detail(r, "Min available") != "50%" {
		t.Fatalf("details %+v", r.Details)
	}
}

func TestSummarizeServiceAccountAndNetworkPolicy(t *testing.T) {
	sa := "apiVersion: v1\nkind: ServiceAccount\nmetadata: {name: app, namespace: apps}\nsecrets: [{name: app-token-abcde}]\nimagePullSecrets: [{name: regcred}]\n"
	r := summarizeYAML(t, sa)
	if r.Status != model.StatusReady || r.Message != "" || len(r.Details) != 0 {
		t.Fatalf("service account %+v", r)
	}
	np := `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata: {name: web, namespace: apps}
spec:
  podSelector: {matchLabels: {app: web, tier: front}}
  policyTypes: [Ingress, Egress]
  ingress: [{from: [{podSelector: {}}]}, {ports: [{port: 443}]}]
  egress: [{to: [{namespaceSelector: {}}]}]
`
	r = summarizeYAML(t, np)
	if r.Status != model.StatusReady || r.Message != "Ingress, Egress · app=web, tier=front" || detail(r, "Ingress rules") != "2" || detail(r, "Egress rules") != "1" {
		t.Fatalf("network policy %q %+v", r.Message, r.Details)
	}
	deny := "apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata: {name: deny, namespace: apps}\nspec: {podSelector: {}}\n"
	if r := summarizeYAML(t, deny); r.Message != "Ingress · all pods" {
		t.Fatalf("default deny %q", r.Message)
	}
}

func TestProjectOf(t *testing.T) {
	tests := map[string]string{
		"": "kubernetes", "apps": "kubernetes", "batch": "kubernetes", "networking.k8s.io": "kubernetes",
		"policy": "kubernetes", "storage.k8s.io": "kubernetes", "rbac.authorization.k8s.io": "kubernetes", "autoscaling": "kubernetes",
		"kustomize.toolkit.fluxcd.io": "flux", "helm.toolkit.fluxcd.io": "flux", "source.toolkit.fluxcd.io": "flux",
		"image.toolkit.fluxcd.io": "flux", "notification.toolkit.fluxcd.io": "flux",
		"karpenter.sh": "karpenter", "karpenter.k8s.aws": "karpenter",
		"external-secrets.io": "external-secrets", "generators.external-secrets.io": "external-secrets",
		"cert-manager.io": "cert-manager.io", "notfluxcd.io": "notfluxcd.io", "evil-external-secrets.io": "evil-external-secrets.io",
	}
	for group, want := range tests {
		if got := ProjectOf(group); got.ID != want {
			t.Errorf("ProjectOf(%q) = %+v, want %s", group, got, want)
		}
	}
	if p := ProjectOf("cert-manager.io"); p.Name != "cert-manager.io" {
		t.Errorf("unknown group name %q", p.Name)
	}
	for _, k := range All() {
		if want := ProjectOf(k.Group).ID; k.Preset != "" && want != map[string]string{PresetKarpenter: "karpenter", PresetExternalSecrets: "external-secrets"}[k.Preset] {
			t.Errorf("%s: preset %s in project %s", k.Kind, k.Preset, want)
		}
	}
}

func TestPresets(t *testing.T) {
	got, err := ParsePresets([]string{" externalSecrets", "", "karpenter", "karpenter"})
	if err != nil || !slices.Equal(got, []string{PresetKarpenter, PresetExternalSecrets}) {
		t.Fatalf("ParsePresets = %v %v", got, err)
	}
	for _, bad := range []string{"datadog", "certManager", "Karpenter"} {
		if _, err := ParsePresets([]string{bad}); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	served := Served{"Kustomization": "v1", "NodePool": "v1", "NodeClaim": "v1", "EC2NodeClass": "v1", "ExternalSecret": "v1beta1", "Namespace": "v1"}
	if s := served.ForPresets(nil); len(s) != 2 || s["Namespace"] != "v1" || s["NodePool"] != "" {
		t.Fatalf("no presets: %v", s)
	}
	s := served.ForPresets([]string{PresetKarpenter})
	if len(s) != 5 || s["ExternalSecret"] != "" {
		t.Fatalf("karpenter: %v", s)
	}
	if !slices.Equal(s.SurfacedKinds(), []string{"kustomize.toolkit.fluxcd.io/Kustomization", "/Namespace", "karpenter.sh/NodePool", "karpenter.sh/NodeClaim", "karpenter.k8s.aws/EC2NodeClass"}) {
		t.Fatalf("surfaced %v", s.SurfacedKinds())
	}
	// An ExternalSecret stays an inventory-only row while its preset is off.
	if served.ForPresets(nil).Watches(GroupESO, KindExternalSecret) {
		t.Fatal("watched without its preset")
	}
	for _, k := range All() {
		if k.Preset != "" && !slices.Contains(Presets(), k.Preset) {
			t.Errorf("%s has unknown preset %q", k.Kind, k.Preset)
		}
	}
}

func TestSanitizeYAMLMatrix(t *testing.T) {
	cm := fromYAML(t, `
apiVersion: v1
kind: ConfigMap
metadata: {name: app, namespace: apps, managedFields: [{manager: kubectl}], labels: {app: web}}
data: {config.yaml: "password: hunter2"}
binaryData: {blob: aGVsbG8=}
`)
	crd := fromYAML(t, `
apiVersion: argoproj.io/v1alpha1
kind: Rollout
metadata: {name: web, namespace: apps}
spec:
  template:
    spec:
      containers: [{name: app, image: app:1, env: [{name: TOKEN, value: hunter2}, {name: REF, valueFrom: {secretKeyRef: {name: s, key: k}}}]}]
stringData: {leak: hunter2}
`)
	for _, tt := range []struct {
		name string
		u    *unstructured.Unstructured
		keep []string
	}{
		{"ConfigMap keeps only metadata", cm, []string{"name: app", "app: web"}},
		{"custom workload redacts env", crd, []string{"TOKEN", "secretKeyRef", "image: app:1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out, err := SanitizeYAML(tt.u)
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			for _, leak := range []string{"hunter2", "aGVsbG8", "managedFields", "binaryData", "stringData", "\ndata:"} {
				if strings.Contains(s, leak) {
					t.Errorf("contains %q:\n%s", leak, s)
				}
			}
			for _, k := range tt.keep {
				if !strings.Contains(s, k) {
					t.Errorf("lacks %q:\n%s", k, s)
				}
			}
			var back map[string]any
			if err := yaml.Unmarshal(out, &back); err != nil {
				t.Fatal(err)
			}
		})
	}
}
