package flux

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const agentChart = "../../deploy/charts/eddy-agent"

// rbacRule is one rule of a rendered (Cluster)Role.
type rbacRule struct {
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	Verbs         []string `json:"verbs"`
	ResourceNames []string `json:"resourceNames,omitempty"`
}

// manifest is the part of a rendered RBAC object the tests look at.
type manifest struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Rules   []rbacRule `json:"rules"`
	RoleRef struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"roleRef"`
	Subjects []struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"subjects"`
}

// helmTemplate runs `helm template` on the eddy-agent chart (release "eddy" in namespace
// eddy-system) with the required values plus args, and returns the output and error.
func helmTemplate(t *testing.T, args ...string) (string, error) {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	base := []string{"template", "eddy", agentChart, "--namespace", "eddy-system",
		"--set", "cluster.name=test", "--set", "hub.url=wss://hub.example.com/agent/v1/connect", "--set", "token.value=x"}
	out, err := exec.Command(helm, append(base, args...)...).CombinedOutput()
	return string(out), err
}

// renderManifests renders the chart and returns every object it produced.
func renderManifests(t *testing.T, args ...string) []manifest {
	t.Helper()
	out, err := helmTemplate(t, args...)
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", args, err, out)
	}
	return parseManifests(t, out)
}

func parseManifests(t *testing.T, out string) []manifest {
	t.Helper()
	var docs []manifest
	for _, doc := range strings.Split(out, "\n---") {
		var m manifest
		if err := yaml.Unmarshal([]byte(doc), &m); err != nil {
			t.Fatalf("parse manifest: %v\n%s", err, doc)
		}
		if m.Kind != "" {
			docs = append(docs, m)
		}
	}
	return docs
}

func find(docs []manifest, kind, name string) (manifest, bool) {
	for _, d := range docs {
		if d.Kind == kind && d.Metadata.Name == name {
			return d, true
		}
	}
	return manifest{}, false
}

// ruleMap flattens rules to "group/resource" → verbs.
func ruleMap(rules []rbacRule) map[string][]string {
	out := map[string][]string{}
	for _, r := range rules {
		for _, g := range r.APIGroups {
			for _, res := range r.Resources {
				out[g+"/"+res] = append(out[g+"/"+res], r.Verbs...)
			}
		}
	}
	return out
}

func presetArgs(presets string) []string {
	if presets == "" {
		return nil
	}
	return []string{"--set", "watch.presets={" + presets + "}"}
}

// renderClusterRole returns the agent ClusterRole's rules as "group/resource" → verbs.
func renderClusterRole(t *testing.T, presets string) map[string][]string {
	t.Helper()
	role, ok := find(renderManifests(t, presetArgs(presets)...), "ClusterRole", "eddy-agent")
	if !ok {
		t.Fatal("no agent ClusterRole rendered")
	}
	return ruleMap(role.Rules)
}

var presetCases = []struct {
	presets string
	enabled []string
}{
	{"", nil},
	{"karpenter", []string{PresetKarpenter}},
	{"externalSecrets", []string{PresetExternalSecrets}},
	{"karpenter,externalSecrets", Presets()},
}

func hasRead(verbs []string) bool {
	return slices.Contains(verbs, "get") && slices.Contains(verbs, "list") && slices.Contains(verbs, "watch")
}

func TestChartClusterRoleMatchesKinds(t *testing.T) {
	for _, tt := range presetCases {
		t.Run("presets="+tt.presets, func(t *testing.T) {
			rules := renderClusterRole(t, tt.presets)
			for _, k := range All() {
				verbs := rules[k.Group+"/"+k.Plural]
				want := k.Preset == "" || slices.Contains(tt.enabled, k.Preset)
				if has := hasRead(verbs); want != has {
					t.Errorf("%s/%s: rule %v, want read access %v", k.Group, k.Plural, verbs, want)
				}
			}
			for key, verbs := range rules {
				for _, v := range verbs {
					if !slices.Contains([]string{"get", "list", "watch", "create", "impersonate"}, v) {
						t.Errorf("%s: write verb %q", key, v)
					}
				}
			}
			for _, never := range []string{"/secrets", "/configmaps"} {
				if _, ok := rules[never]; ok {
					t.Errorf("ClusterRole grants %s", never)
				}
			}
		})
	}
}
