package flux

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const userRBACFile = "../../deploy/rbac/eddy-user-rbac.yaml"

// TestChartUserRolesMatchKinds checks the chart's eddy-viewer and eddy-operator against the
// kind table: read on every watched kind (preset kinds only with their preset), patch on the
// Flux kinds for operators only, and nothing else that writes.
func TestChartUserRolesMatchKinds(t *testing.T) {
	for _, tt := range presetCases {
		t.Run("presets="+tt.presets, func(t *testing.T) {
			docs := renderManifests(t, presetArgs(tt.presets)...)
			for _, role := range []string{"eddy-viewer", "eddy-operator"} {
				m, ok := find(docs, "ClusterRole", role)
				if !ok {
					t.Fatalf("no ClusterRole %s rendered", role)
				}
				rules := ruleMap(m.Rules)
				for _, k := range All() {
					key := k.Group + "/" + k.Plural
					verbs := rules[key]
					want := k.Preset == "" || slices.Contains(tt.enabled, k.Preset)
					if has := hasRead(verbs); want != has {
						t.Errorf("%s %s: rule %v, want read access %v", role, key, verbs, want)
					}
					wantPatch := role == "eddy-operator" && k.Flux
					if has := slices.Contains(verbs, "patch"); has != wantPatch {
						t.Errorf("%s %s: rule %v, want patch %v", role, key, verbs, wantPatch)
					}
				}
				if verbs := rules["/pods/log"]; !slices.Equal(verbs, []string{"get"}) {
					t.Errorf("%s pods/log: %v, want [get]", role, verbs)
				}
				for key, verbs := range rules {
					for _, v := range verbs {
						if !slices.Contains([]string{"get", "list", "watch", "patch"}, v) {
							t.Errorf("%s %s: unexpected verb %q", role, key, v)
						}
					}
				}
				for _, never := range []string{"/secrets", "/configmaps"} {
					if _, ok := rules[never]; ok {
						t.Errorf("%s grants %s", role, never)
					}
				}
			}
		})
	}
}

// TestChartUserRolesMatchReferenceFile keeps deploy/rbac/eddy-user-rbac.yaml (for installs
// without Helm) equal to the chart's roles: with every preset the rules are identical, and by
// default they are the file's rules without the preset kinds.
func TestChartUserRolesMatchReferenceFile(t *testing.T) {
	raw, err := os.ReadFile(userRBACFile)
	if err != nil {
		t.Fatal(err)
	}
	file := parseManifests(t, string(raw))
	presetGroups := map[string]bool{}
	for _, k := range All() {
		if k.Preset != "" {
			presetGroups[k.Group] = true
		}
	}
	for _, tt := range []struct {
		name    string
		presets string
		keep    func(rbacRule) bool
	}{
		{"all presets", "karpenter,externalSecrets", func(rbacRule) bool { return true }},
		{"defaults", "", func(r rbacRule) bool {
			return !slices.ContainsFunc(r.APIGroups, func(g string) bool { return presetGroups[g] })
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			docs := renderManifests(t, presetArgs(tt.presets)...)
			for _, role := range []string{"eddy-viewer", "eddy-operator"} {
				want, ok := find(file, "ClusterRole", role)
				if !ok {
					t.Fatalf("%s: no ClusterRole %s", userRBACFile, role)
				}
				got, ok := find(docs, "ClusterRole", role)
				if !ok {
					t.Fatalf("chart: no ClusterRole %s", role)
				}
				wantRules := slices.DeleteFunc(slices.Clone(want.Rules), func(r rbacRule) bool { return !tt.keep(r) })
				if !reflect.DeepEqual(got.Rules, wantRules) {
					t.Errorf("%s: chart rules differ from %s\nchart: %+v\nfile:  %+v", role, userRBACFile, got.Rules, wantRules)
				}
			}
		})
	}
}

type subject struct{ kind, name string }

type binding struct {
	kind, namespace, role string
	subjects              []subject
}

// userBindings returns every binding except the agent's own, keyed by kind/namespace/name.
func userBindings(docs []manifest) map[string]binding {
	out := map[string]binding{}
	for _, d := range docs {
		if (d.Kind != "ClusterRoleBinding" && d.Kind != "RoleBinding") || strings.HasPrefix(d.RoleRef.Name, "eddy-agent") {
			continue
		}
		b := binding{kind: d.Kind, namespace: d.Metadata.Namespace, role: d.RoleRef.Name}
		for _, s := range d.Subjects {
			b.subjects = append(b.subjects, subject{s.Kind, s.Name})
		}
		out[d.Kind+"/"+d.Metadata.Namespace+"/"+d.Metadata.Name] = b
	}
	return out
}

func TestChartUserRBACBindings(t *testing.T) {
	viewerAll := binding{"ClusterRoleBinding", "", "eddy-viewer", []subject{{"Group", "eddy:authenticated"}}}
	for _, tt := range []struct {
		name  string
		args  []string
		want  map[string]binding
		roles []string
	}{
		{name: "defaults", want: map[string]binding{"ClusterRoleBinding//eddy-viewer": viewerAll}},
		{
			name: "opt out of the default viewer binding",
			args: []string{"--set-json", "userRBAC.viewer.groups=[]"},
			want: map[string]binding{},
		},
		{
			name: "operator team and user",
			args: []string{"--set", "userRBAC.operator.groups={eddy:github:acme/platform}", "--set", "userRBAC.operator.users={alice@example.com}"},
			want: map[string]binding{
				"ClusterRoleBinding//eddy-viewer": viewerAll,
				"ClusterRoleBinding//eddy-operator": {"ClusterRoleBinding", "", "eddy-operator", []subject{
					{"Group", "eddy:github:acme/platform"}, {"User", "alice@example.com"}}},
			},
		},
		{
			name: "namespaces",
			args: []string{"--set", "userRBAC.namespaces={team-a,team-b}", "--set", "userRBAC.operator.groups={eddy:team-a}"},
			want: map[string]binding{
				"RoleBinding/team-a/eddy-viewer":   {"RoleBinding", "team-a", "eddy-viewer", []subject{{"Group", "eddy:authenticated"}}},
				"RoleBinding/team-b/eddy-viewer":   {"RoleBinding", "team-b", "eddy-viewer", []subject{{"Group", "eddy:authenticated"}}},
				"RoleBinding/team-a/eddy-operator": {"RoleBinding", "team-a", "eddy-operator", []subject{{"Group", "eddy:team-a"}}},
				"RoleBinding/team-b/eddy-operator": {"RoleBinding", "team-b", "eddy-operator", []subject{{"Group", "eddy:team-a"}}},
			},
		},
		{
			name: "extra bindings",
			args: []string{"--set", "userRBAC.namespaces={team-a}",
				"--set-json", `userRBAC.bindings=[{"name":"prod-eu","role":"operator","groups":["eddy:operators:prod-eu"],"namespaces":[]},{"name":"team-c","role":"viewer","groups":["eddy:team-c"]},{"name":"empty","role":"operator"}]`},
			want: map[string]binding{
				"RoleBinding/team-a/eddy-viewer":            {"RoleBinding", "team-a", "eddy-viewer", []subject{{"Group", "eddy:authenticated"}}},
				"ClusterRoleBinding//eddy-operator-prod-eu": {"ClusterRoleBinding", "", "eddy-operator", []subject{{"Group", "eddy:operators:prod-eu"}}},
				"RoleBinding/team-a/eddy-viewer-team-c":     {"RoleBinding", "team-a", "eddy-viewer", []subject{{"Group", "eddy:team-c"}}},
			},
		},
		{
			name:  "role names",
			args:  []string{"--set", "userRBAC.roleNames.viewer=team-viewer", "--set", "userRBAC.roleNames.operator=team-operator"},
			want:  map[string]binding{"ClusterRoleBinding//team-viewer": {"ClusterRoleBinding", "", "team-viewer", []subject{{"Group", "eddy:authenticated"}}}},
			roles: []string{"team-viewer", "team-operator"},
		},
		{
			name: "disabled",
			args: []string{"--set", "userRBAC.create=false"},
			want: map[string]binding{},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			docs := renderManifests(t, tt.args...)
			if got := userBindings(docs); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("bindings:\n got %+v\nwant %+v", got, tt.want)
			}
			roles := tt.roles
			if roles == nil && !slices.Contains(tt.args, "userRBAC.create=false") {
				roles = []string{"eddy-viewer", "eddy-operator"}
			}
			for _, r := range roles {
				if _, ok := find(docs, "ClusterRole", r); !ok {
					t.Errorf("no ClusterRole %s", r)
				}
			}
			// The agent's own binding is untouched: only its ServiceAccount, only its read-only role.
			crb, ok := find(docs, "ClusterRoleBinding", "eddy-agent")
			if !ok || crb.RoleRef.Name != "eddy-agent" || len(crb.Subjects) != 1 || crb.Subjects[0].Kind != "ServiceAccount" {
				t.Errorf("agent ClusterRoleBinding changed: %+v", crb)
			}
			for _, d := range docs {
				for _, s := range d.Subjects {
					if s.Kind == "ServiceAccount" && d.RoleRef.Name != "eddy-agent" && d.RoleRef.Name != "eddy-agent-token" {
						t.Errorf("%s %s binds a ServiceAccount to %s", d.Kind, d.Metadata.Name, d.RoleRef.Name)
					}
				}
			}
		})
	}
}

func TestChartUserRBACRejects(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"system:masters group", []string{"--set", "userRBAC.operator.groups={system:masters}"}},
		{"system group in a binding", []string{"--set-json", `userRBAC.bindings=[{"name":"x","role":"viewer","groups":["system:authenticated"]}]`}},
		{"group without the prefix", []string{"--set", "userRBAC.operator.groups={platform}"}},
		{"group with another prefix", []string{"--set", "userRBAC.viewer.groups={oidc:platform}"}},
		{"system user", []string{"--set", "userRBAC.viewer.users={system:admin}"}},
		{"agent ServiceAccount", []string{"--set", "userRBAC.operator.users={system:serviceaccount:eddy-system:eddy-agent}"}},
		{"agent ServiceAccount without schema", []string{"--skip-schema-validation", "--set", "userRBAC.operator.users={system:serviceaccount:eddy-system:eddy-agent}"}},
		{"system group without schema", []string{"--skip-schema-validation", "--set", "userRBAC.operator.groups={system:masters}"}},
		{"denied user prefix", []string{"--set", "userRBAC.operator.users={kubernetes-admin}"}},
		{"system prefix", []string{"--set", "userRBAC.groupPrefix=system:"}},
		{"prefix the agent may not impersonate", []string{"--set", "userRBAC.groupPrefix=acme:"}},
		{"unknown role", []string{"--set-json", `userRBAC.bindings=[{"name":"x","role":"admin","groups":["eddy:x"]}]`}},
		{"duplicate binding", []string{"--set-json", `userRBAC.bindings=[{"name":"x","role":"viewer"},{"name":"x","role":"viewer"}]`}},
		{"same role names", []string{"--set", "userRBAC.roleNames.operator=eddy-viewer"}},
		{"bad namespace", []string{"--set", "userRBAC.namespaces={Team_A}"}},
		{"unknown key", []string{"--set", "userRBAC.admins.groups={eddy:x}"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if out, err := helmTemplate(t, tt.args...); err == nil {
				t.Errorf("helm template %v succeeded, want an error\n%s", tt.args, out)
			}
		})
	}
	// A prefix inside an allowed one works, as long as every group carries it.
	renderManifests(t, "--set", "userRBAC.groupPrefix=eddy:github:", "--set", "userRBAC.viewer.groups={eddy:github:acme}")
}
