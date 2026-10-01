//go:build dev

package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeKubeconfig writes a throwaway kubeconfig whose clusters point at an
// unroutable loopback port. Nothing in these tests connects to it.
func writeKubeconfig(t *testing.T, current string, contexts ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("apiVersion: v1\nkind: Config\ncurrent-context: " + current + "\nclusters:\n")
	for _, c := range contexts {
		b.WriteString("- name: " + c + "\n  cluster:\n    server: https://127.0.0.1:1\n")
	}
	b.WriteString("users:\n- name: u\n  user:\n    token: fake\ncontexts:\n")
	for _, c := range contexts {
		b.WriteString("- name: " + c + "\n  context:\n    cluster: " + c + "\n    user: u\n")
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	// Never fall back to the developer's real kubeconfig.
	t.Setenv("KUBECONFIG", path)
	return path
}

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func parseLocal(t *testing.T, env func(string) string, args ...string) *localFlags {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	lf := addLocalFlags(fs, env)
	if err := fs.Parse(args); err != nil {
		t.Fatal(err)
	}
	if err := lf.validate(fs); err != nil {
		t.Fatal(err)
	}
	return lf
}

func TestPlanLocalActivation(t *testing.T) {
	arn := "arn:aws:eks:eu-west-2:123:cluster/prod-eu"
	kc := writeKubeconfig(t, "kind-eddy", "kind-eddy", arn)
	token := strings.Repeat("t", 64)
	dev := map[string]string{"EDDY_DEV_MODE": "1", "EDDY_DEV_AGENT_TOKEN": token}
	tests := []struct {
		name string
		env  map[string]string
		args []string
		err  string
	}{
		{name: "ok", env: dev, args: []string{"--kubeconfig", kc}},
		{name: "no dev mode", env: map[string]string{"EDDY_DEV_AGENT_TOKEN": token}, args: []string{"--kubeconfig", kc}, err: "EDDY_DEV_MODE=1"},
		{name: "remote hub flag", env: dev, args: []string{"--kubeconfig", kc, "--hub", "wss://eddy.example.com/agent/v1/connect"}, err: "loopback"},
		{name: "remote hub env", env: map[string]string{"EDDY_DEV_MODE": "1", "EDDY_DEV_AGENT_TOKEN": token, "EDDY_HUB_URL": "ws://192.168.1.10:8443/agent/v1/connect"}, args: []string{"--kubeconfig", kc}, err: "loopback"},
		{name: "protected without writes", env: dev, args: []string{"--kubeconfig", kc, "--allow-writes-protected"}, err: "needs --allow-writes"},
		{name: "no token", env: map[string]string{"EDDY_DEV_MODE": "1"}, args: []string{"--kubeconfig", kc, "--token-file", filepath.Join(t.TempDir(), "missing")}, err: "no agent token"},
		{name: "bad protect", env: dev, args: []string{"--kubeconfig", kc, "--protect", "("}, err: "--protect"},
		{name: "unknown context", env: dev, args: []string{"--kubeconfig", kc, "--contexts", "nope"}, err: "not in the kubeconfig"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lf := parseLocal(t, envOf(tt.env), append([]string{"--local"}, tt.args...)...)
			plan, err := planLocal(lf, envOf(tt.env))
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("want error with %q, got %v", tt.err, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.token != token || len(plan.targets) != 1 || plan.targets[0].Cluster != "kind-eddy" {
				t.Fatalf("plan %+v", plan)
			}
		})
	}

	lf := parseLocal(t, envOf(dev), "--local", "--kubeconfig", kc, "--contexts", "kind-eddy,"+arn)
	plan, err := planLocal(lf, envOf(dev))
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.targets[1]; got.Cluster != "prod-eu" || !got.Protected {
		t.Fatalf("arn target %+v", got)
	}
	for _, tgt := range plan.targets {
		if ro, _ := tgt.ReadOnly(lf.allowWrites, lf.allowWritesProtected); !ro {
			t.Fatalf("%s must default to read-only", tgt.Context)
		}
	}
}

func TestLocalFlagsNeedLocal(t *testing.T) {
	writeKubeconfig(t, "kind-eddy", "kind-eddy")
	var stderr bytes.Buffer
	code := runMain([]string{"--contexts", "kind-eddy", "--allow-writes"}, envOf(nil), &bytes.Buffer{}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "only apply with --local") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

func TestRunMainLocalRefusesWithoutDevMode(t *testing.T) {
	kc := writeKubeconfig(t, "kind-eddy", "kind-eddy")
	var stderr bytes.Buffer
	code := runMain([]string{"--local", "--kubeconfig", kc}, envOf(map[string]string{"EDDY_AGENT_TOKEN": "x"}), &bytes.Buffer{}, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "EDDY_DEV_MODE=1") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

func TestLocalEnvEquivalents(t *testing.T) {
	kc := writeKubeconfig(t, "kind-eddy", "kind-eddy", "staging", "prod")
	env := envOf(map[string]string{
		"EDDY_DEV_MODE": "1", "EDDY_DEV_AGENT_TOKEN": strings.Repeat("t", 64),
		"EDDY_AGENT_LOCAL": "1", "EDDY_AGENT_CONTEXTS": "staging,prod", "EDDY_AGENT_ALLOW_WRITES": "true",
		"EDDY_AGENT_PROTECT": "^prod$",
	})
	lf := parseLocal(t, env, "--kubeconfig", kc)
	if !lf.enabled() || !lf.allowWrites || lf.allowWritesProtected || lf.protect != "^prod$" {
		t.Fatalf("flags from env %+v", lf)
	}
	plan, err := planLocal(lf, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.targets) != 2 || plan.targets[0].Protected || !plan.targets[1].Protected {
		t.Fatalf("targets %+v", plan.targets)
	}
	if ro, _ := plan.targets[0].ReadOnly(lf.allowWrites, lf.allowWritesProtected); ro {
		t.Fatal("staging should be writable with EDDY_AGENT_ALLOW_WRITES=true")
	}
	if ro, _ := plan.targets[1].ReadOnly(lf.allowWrites, lf.allowWritesProtected); !ro {
		t.Fatal("prod must stay read-only without --allow-writes-protected")
	}

	// Flags win over the environment.
	lf = parseLocal(t, env, "--kubeconfig", kc, "--contexts", "kind-eddy", "--allow-writes=false")
	if !lf.enabled() || lf.allowWrites || lf.contexts != "kind-eddy" {
		t.Fatalf("flags must override env: %+v", lf)
	}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	lf = addLocalFlags(fs, env)
	if err := fs.Parse([]string{"--local=false"}); err != nil || lf.enabled() || lf.validate(fs) != nil {
		t.Fatalf("--local=false must override EDDY_AGENT_LOCAL: %+v", lf)
	}

	// EDDY_AGENT_LOCAL=1 still needs EDDY_DEV_MODE=1.
	var stderr bytes.Buffer
	noDev := envOf(map[string]string{"EDDY_AGENT_LOCAL": "1", "EDDY_AGENT_TOKEN": "x"})
	if code := runMain([]string{"--kubeconfig", kc}, noDev, &bytes.Buffer{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "EDDY_DEV_MODE=1") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	stderr.Reset()
	bad := envOf(map[string]string{"EDDY_AGENT_LOCAL": "yes please"})
	if code := runMain(nil, bad, &bytes.Buffer{}, &stderr); code != 2 || !strings.Contains(stderr.String(), "not a boolean") {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
}

func TestLocalPresets(t *testing.T) {
	for _, tt := range []struct {
		env, want string
		err       bool
	}{
		{"", "karpenter,externalSecrets", false},
		{"none", "", false},
		{"karpenter", "karpenter", false},
		{"karpenter,datadog", "", true},
	} {
		got, err := localPresets(func(k string) string {
			if k == envPresets {
				return tt.env
			}
			return ""
		})
		if (err != nil) != tt.err || strings.Join(got, ",") != tt.want {
			t.Errorf("%s=%q: %v %v", envPresets, tt.env, got, err)
		}
	}
}
