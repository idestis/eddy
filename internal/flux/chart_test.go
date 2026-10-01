package flux

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// renderClusterRole runs `helm template` on the eddy-agent chart and returns
// the agent ClusterRole's read rules as "group/resource" → verbs.
func renderClusterRole(t *testing.T, presets string) map[string][]string {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	args := []string{"template", "eddy", "../../deploy/charts/eddy-agent",
		"--set", "cluster.name=test", "--set", "hub.url=wss://hub.example.com/agent/v1/connect", "--set", "token.value=x"}
	if presets != "" {
		args = append(args, "--set", "watch.presets={"+presets+"}")
	}
	out, err := exec.Command(helm, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	for _, doc := range strings.Split(string(out), "\n---") {
		var o struct {
			Kind  string `json:"kind"`
			Rules []struct {
				APIGroups     []string `json:"apiGroups"`
				Resources     []string `json:"resources"`
				Verbs         []string `json:"verbs"`
				ResourceNames []string `json:"resourceNames"`
			} `json:"rules"`
		}
		if err := yaml.Unmarshal([]byte(doc), &o); err != nil || o.Kind != "ClusterRole" {
			continue
		}
		rules := map[string][]string{}
		for _, r := range o.Rules {
			for _, g := range r.APIGroups {
				for _, res := range r.Resources {
					rules[g+"/"+res] = append(rules[g+"/"+res], r.Verbs...)
				}
			}
		}
		return rules
	}
	t.Fatal("no ClusterRole rendered")
	return nil
}

func TestChartClusterRoleMatchesKinds(t *testing.T) {
	for _, tt := range []struct {
		presets string
		enabled []string
	}{
		{"", nil},
		{"karpenter", []string{PresetKarpenter}},
		{"karpenter,externalSecrets", Presets()},
	} {
		t.Run("presets="+tt.presets, func(t *testing.T) {
			rules := renderClusterRole(t, tt.presets)
			for _, k := range All() {
				verbs := rules[k.Group+"/"+k.Plural]
				want := k.Preset == "" || slices.Contains(tt.enabled, k.Preset)
				has := slices.Contains(verbs, "get") && slices.Contains(verbs, "list") && slices.Contains(verbs, "watch")
				if want != has {
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
